// SPDX-License-Identifier: AGPL-3.0-or-later

// Package stalker serves the Stalker portal API that third-party Stalker
// clients use. What it implements follows the protocol notes in
// docs/protocol/stalker-protocol-notes.md, which are a hypothesis until real
// clients confirm them.
//
// A device is identified by the mac cookie. An unknown device is recorded as
// pending and refused until the administrator approves it. A box paired with
// the MAG page is refused too, since its MAC address alone is not enough
// (D39). A handshake gives
// an approved or blocked device a token, which later calls send as a bearer
// token; a new handshake replaces it. Requests the API does not implement get
// 404 and go to the capture log.
package stalker

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"overscan.gureet.ca/internal/devices"
	"overscan.gureet.ca/internal/hub"
	"overscan.gureet.ca/internal/library"
	"overscan.gureet.ca/internal/store"
)

const (
	// watchdogTimeout is the profile's watchdog_timeout in seconds. Clients
	// call watchdog/get_events at two thirds of it.
	watchdogTimeout = 120

	// refusalLogInterval is how often a refusal of a paired box's MAC
	// address is logged at most, for each address.
	refusalLogInterval = 10 * time.Minute

	// maxCaptureKinds caps how many kinds of unknown request the capture log
	// keeps.
	maxCaptureKinds = 1000

	// maxBodyBytes caps a POST body.
	maxBodyBytes = 64 << 10
)

// API is the Stalker portal API, an http.Handler.
type API struct {
	store   *store.Store
	devices *devices.Registry
	hub     *hub.Hub
	library *library.Library // nil if clients see no films or series
	log     *slog.Logger
	now     func() time.Time
	local   *time.Location // the zone of a device that names none that is known

	mu       sync.Mutex
	refusals map[string]time.Time // when a paired box's MAC address was last refused, by address
}

// New returns the API, keeping its state in st, admitting devices through
// reg and playing streams through h.
func New(st *store.Store, reg *devices.Registry, h *hub.Hub, log *slog.Logger) *API {
	return &API{store: st, devices: reg, hub: h, log: log, now: time.Now, local: time.Local}
}

// ServeHTTP answers one portal call: GET or POST with the type and action
// parameters.
func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	module, action := r.Form.Get("type"), r.Form.Get("action")

	mac, ok := requestMAC(r)
	if !ok {
		a.log.Debug("stalker request without a valid mac cookie", "type", module, "action", action)
		authFailed(w)
		return
	}
	d, err := a.devices.Contact(ctx, mac, reportedModel(r))
	if err != nil {
		a.internalError(w, "look up device", err)
		return
	}
	if d.Paired() {
		a.refusePaired(d.MAC)
		authFailed(w)
		return
	}
	switch d.State {
	case store.DeviceApproved, store.DeviceBlocked:
	default:
		a.log.Debug("stalker request from a device waiting for approval", "mac", d.MAC)
		authFailed(w)
		return
	}

	if module == "stb" && action == "handshake" {
		a.handshake(w, r, d)
		return
	}
	if !a.validToken(ctx, r, d) {
		authFailed(w)
		return
	}
	// A blocked device learns that it is blocked from its profile, and
	// nothing else.
	if d.State == store.DeviceBlocked && !(module == "stb" && action == "get_profile") {
		authFailed(w)
		return
	}

	switch module + "/" + action {
	case "stb/get_profile":
		a.getProfile(w, r, d)
	case "stb/do_auth":
		a.doAuth(w, r, d)
	case "account_info/get_main_info":
		a.getMainInfo(w, r, d)
	case "watchdog/get_events":
		a.getEvents(w, r, d)
	case "itv/get_genres":
		a.getGenres(w, r, d)
	case "itv/get_all_channels":
		a.getAllChannels(w, r, d)
	case "itv/get_ordered_list":
		a.getOrderedList(w, r, d)
	case "itv/create_link":
		a.createLink(w, r, d)
	case "itv/get_short_epg":
		a.getShortEPG(w, r, d)
	case "itv/get_epg_info":
		a.getEPGInfo(w, r, d)
	case "epg/get_simple_data_table":
		a.getSimpleDataTable(w, r, d)
	case "vod/get_categories":
		a.getCategories(w, r, store.KindMovie)
	case "series/get_categories":
		a.getCategories(w, r, store.KindSeries)
	case "vod/get_ordered_list":
		a.getTitles(w, r, store.KindMovie)
	case "series/get_ordered_list":
		a.getTitles(w, r, store.KindSeries)
	case "vod/create_link", "series/create_link":
		a.createTitleLink(w, r, d)
	default:
		a.capture(ctx, r, d, module, action)
		http.NotFound(w, r)
	}
}

// requestMAC returns the device's MAC address from the mac cookie, which
// clients send URL-encoded.
func requestMAC(r *http.Request) (string, bool) {
	c, err := r.Cookie("mac")
	if err != nil {
		return "", false
	}
	v, err := url.QueryUnescape(c.Value)
	if err != nil {
		return "", false
	}
	mac, err := store.ParseMAC(v)
	return mac, err == nil
}

// reportedModel returns the model a device reports: the stb_type parameter
// of get_profile, or else the Model part of X-User-Agent ("Model: MAG250;
// Link: WiFi").
func reportedModel(r *http.Request) string {
	if t := strings.TrimSpace(r.Form.Get("stb_type")); t != "" {
		return t
	}
	for part := range strings.SplitSeq(r.Header.Get("X-User-Agent"), ";") {
		k, v, ok := strings.Cut(part, ":")
		if ok && strings.EqualFold(strings.TrimSpace(k), "model") {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// refusePaired logs, at most once in refusalLogInterval for each address,
// that a Stalker client used the MAC address of a box paired with the MAG
// page.
func (a *API) refusePaired(mac string) {
	now := a.now()
	a.mu.Lock()
	last, seen := a.refusals[mac]
	log := !seen || now.Sub(last) >= refusalLogInterval
	if log {
		if a.refusals == nil || len(a.refusals) >= 1000 {
			a.refusals = map[string]time.Time{}
		}
		a.refusals[mac] = now
	}
	a.mu.Unlock()
	if log {
		a.log.Info("Stalker client refused: its MAC address belongs to a box paired with the MAG page", "mac", mac,
			"hint", "give the client a MAC address of its own")
	}
}

// newToken returns a random token of 32 upper-case hex digits.
func newToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // never fails; see crypto/rand
	return strings.ToUpper(hex.EncodeToString(b))
}

// hashToken returns the form in which a token is stored.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// bearerToken returns the token from an "Authorization: Bearer" header.
func bearerToken(r *http.Request) string {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

// validToken reports whether r carries d's current token.
func (a *API) validToken(ctx context.Context, r *http.Request, d store.Device) bool {
	token := bearerToken(r)
	if token == "" {
		return false
	}
	want, err := a.store.StalkerTokenHash(ctx, d.ID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			a.log.Error("cannot read a device's token", "mac", d.MAC, "error", err)
		}
		return false
	}
	return subtle.ConstantTimeCompare([]byte(hashToken(token)), []byte(want)) == 1
}

// writeJS answers with value wrapped as {"js": value}.
func writeJS(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(struct {
		JS any `json:"js"`
	}{value})
}

// authFailed answers a call that lacks a valid device or token. Clients treat
// HTTP 401, or a body starting with "Authorization failed.", as a cue to
// handshake again.
func authFailed(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("WWW-Authenticate", "Bearer")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = io.WriteString(w, "Authorization failed.")
}

func (a *API) internalError(w http.ResponseWriter, what string, err error) {
	a.log.Error("stalker API failed", "while", what, "error", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}
