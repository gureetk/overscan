// SPDX-License-Identifier: AGPL-3.0-or-later

// Package device serves the device API, which Overscan's own MAG page uses.
// docs/api/device.md describes it.
//
// The page starts a session with its box's MAC address and the secret it
// keeps (D39). A box without the secret asks to be paired: it is recorded as
// pending, gets a secret to keep and shows a code, and the administrator
// approves it by the code or by the MAC address. A paired box with its secret
// gets a session token, which later calls send as a bearer token. Tokens are
// signed with a key held in memory, so a restart ends every session and the
// page starts a new one.
package device

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"mime"
	"net/http"
	"strings"
	"time"

	"overscan.gureet.ca/internal/devices"
	"overscan.gureet.ca/internal/hub"
	"overscan.gureet.ca/internal/library"
	"overscan.gureet.ca/internal/playcheck"
	"overscan.gureet.ca/internal/store"
)

const (
	// Prefix is the path under which the API is served.
	Prefix = "/api/device/v1/"

	// sessionTTL is how long a session token lasts.
	sessionTTL = 24 * time.Hour

	// maxBodyBytes caps a request body.
	maxBodyBytes = 4 << 10

	// maxSecretLen caps the secret a box sends; the ones Overscan gives out
	// have 43 characters.
	maxSecretLen = 100
)

// Error codes, in the error member of an error's body.
const (
	errBadRequest   = "bad_request"
	errUnauthorized = "unauthorized" // start a new session
	errBlocked      = "blocked"
	errNotFound     = "not_found"
	errLimit        = "limit"  // the source has no free connection
	errBusy         = "busy"   // the server transcodes as many films as it may at once (D73)
	errSource       = "source" // the source could not be read, or gave no address (D77)
	errInternal     = "internal"
	errPINRequired  = "pin_required" // the channel is locked (D55)
	errPINWrong     = "pin_wrong"
	errPINLocked    = "pin_locked" // too many wrong PINs; wait
)

// API is the device API, an http.Handler for the paths under Prefix.
type API struct {
	store   *store.Store
	devices *devices.Registry
	hub     *hub.Hub
	library *library.Library // films and series; nil for none
	checks  *playcheck.Clips // the playback check's clips; nil for none
	// libraryWait is how long a call waits for a first read of a list of
	// films or series, before it answers that the list is loading.
	libraryWait time.Duration
	log         *slog.Logger
	version     string
	key         []byte // signs session tokens
	now         func() time.Time
	mux         *http.ServeMux
	pins        pinGuard
	reports     reportLimiter // POST playback, per device
}

// New returns the API, keeping its state in st, admitting devices through
// reg and playing streams through h. Sessions report version as the server's.
func New(st *store.Store, reg *devices.Registry, h *hub.Hub, log *slog.Logger, version string) *API {
	a := &API{store: st, devices: reg, hub: h, log: log, version: version, key: make([]byte, 32), now: time.Now,
		libraryWait: libraryWait}
	_, _ = rand.Read(a.key) // never fails; see crypto/rand

	mux := http.NewServeMux()
	mux.HandleFunc("POST "+Prefix+"session", a.session)
	mux.HandleFunc("GET "+Prefix+"channels", a.authorized(a.channels))
	mux.HandleFunc("GET "+Prefix+"channels/version", a.authorized(a.channelsVersion))
	mux.HandleFunc("POST "+Prefix+"channels/{id}/play", a.authorized(a.play))
	mux.HandleFunc("PUT "+Prefix+"channels/{id}/favourite", a.authorized(a.favourite(true)))
	mux.HandleFunc("DELETE "+Prefix+"channels/{id}/favourite", a.authorized(a.favourite(false)))
	mux.HandleFunc("GET "+Prefix+"guide", a.authorized(a.guide))
	mux.HandleFunc("GET "+Prefix+"channels/{id}/guide", a.authorized(a.channelGuide))
	mux.HandleFunc("GET "+Prefix+"search", a.authorized(a.search))
	mux.HandleFunc("GET "+Prefix+"movies", a.authorized(a.libraryState(store.KindMovie)))
	mux.HandleFunc("GET "+Prefix+"movies/shelves/{id}", a.authorized(a.shelfTitles(store.KindMovie)))
	mux.HandleFunc("GET "+Prefix+"movies/{id}", a.authorized(a.openTitle(store.KindMovie)))
	mux.HandleFunc("POST "+Prefix+"movies/{id}/play", a.authorized(a.playItem(store.KindMovie)))
	mux.HandleFunc("PUT "+Prefix+"movies/{id}/progress", a.authorized(a.putProgress(store.KindMovie)))
	mux.HandleFunc("GET "+Prefix+"series", a.authorized(a.libraryState(store.KindSeries)))
	mux.HandleFunc("GET "+Prefix+"series/shelves/{id}", a.authorized(a.shelfTitles(store.KindSeries)))
	mux.HandleFunc("GET "+Prefix+"series/{id}", a.authorized(a.openTitle(store.KindSeries)))
	mux.HandleFunc("POST "+Prefix+"episodes/{id}/play", a.authorized(a.playItem(store.KindEpisode)))
	mux.HandleFunc("PUT "+Prefix+"episodes/{id}/progress", a.authorized(a.putProgress(store.KindEpisode)))
	mux.HandleFunc("GET "+Prefix+"settings", a.authorized(a.getSettings))
	mux.HandleFunc("PATCH "+Prefix+"settings", a.authorized(a.patchSettings))
	mux.HandleFunc("GET "+Prefix+"check", a.authorized(a.getCheck))
	mux.HandleFunc("PUT "+Prefix+"check", a.authorized(a.putCheck))
	mux.HandleFunc("POST "+Prefix+"playback", a.authorized(a.reportPlayback))
	mux.HandleFunc("GET "+Prefix+"check/{token}/{file}", a.checkFile)
	mux.HandleFunc(Prefix, func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, errNotFound)
	})
	a.mux = mux
	return a
}

// ServeHTTP answers one call to the API.
func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	limit := int64(maxBodyBytes)
	if r.URL.Path == Prefix+"check" {
		limit = maxCheckBytes
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	a.mux.ServeHTTP(w, r)
}

// sessionRequest is the body of a session call.
type sessionRequest struct {
	MAC    string `json:"mac"`
	Model  string `json:"model"`
	Secret string `json:"secret"` // "" if the box has none
}

// sessionResponse says whether the box may use Overscan, and if so gives it
// a session token.
type sessionResponse struct {
	State   store.DeviceState `json:"state"`
	MAC     string            `json:"mac"`
	Code    string            `json:"code,omitempty"`   // the code of the box's request to pair
	Secret  string            `json:"secret,omitempty"` // a new secret for the box to keep
	Token   string            `json:"token,omitempty"`
	Expires int64             `json:"expires,omitempty"` // Unix seconds
	Label   string            `json:"label,omitempty"`
	Version string            `json:"version"`
}

// session answers POST session (D39). A paired box that sends its secret
// gets a token. An approved box that was never paired, such as one approved
// by its MAC address before it first loaded the page, is paired at once with
// a new secret. A box that waits for its request to pair to be approved gets
// its code again. Any other box asks to be paired: it gets a new secret to
// keep and a code to show, and its request replaces any earlier one for the
// same MAC address. A blocked box is told so.
func (a *API) session(w http.ResponseWriter, r *http.Request) {
	// A JSON body cannot come from another site's page without a CORS
	// preflight, which the API never grants, so a page elsewhere cannot
	// start sessions or requests to pair in a browser on the network.
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, errBadRequest)
		return
	}
	var req sessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Secret) > maxSecretLen {
		writeError(w, http.StatusBadRequest, errBadRequest)
		return
	}
	mac, err := store.ParseMAC(req.MAC)
	if err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest)
		return
	}
	ctx := r.Context()
	model := strings.TrimSpace(req.Model)
	d, err := a.devices.Contact(ctx, mac, model)
	if err != nil {
		a.internalError(w, "look up device", err)
		return
	}
	resp := sessionResponse{State: store.DevicePending, MAC: d.MAC, Version: a.version}
	switch {
	case d.ID == 0:
		// Too many devices are pending to record this one; it waits
		// without a request to pair.
	case d.State == store.DeviceBlocked:
		resp.State = store.DeviceBlocked
		a.log.Debug("blocked device refused", "mac", d.MAC)
	case d.Paired() && secretMatches(req.Secret, d.SecretHash) && d.State == store.DeviceApproved:
		a.startSession(&resp, d)
	case !d.Paired() && d.State == store.DeviceApproved:
		secret := newSecret()
		paired, err := a.store.AdoptSecret(ctx, d.MAC, store.HashSecret(secret), a.now())
		if err != nil {
			a.internalError(w, "pair device", err)
			return
		}
		a.log.Info("device paired", "mac", d.MAC, "label", d.Label)
		resp.Secret = secret
		a.startSession(&resp, paired)
	case d.PairingHash != "" && secretMatches(req.Secret, d.PairingHash):
		resp.Code = d.PairingCode
		a.log.Debug("device waiting for approval", "mac", d.MAC, "code", d.PairingCode)
	default:
		secret := newSecret()
		requested, err := a.requestPairing(ctx, d.MAC, model, store.HashSecret(secret))
		if err != nil {
			a.internalError(w, "record a request to pair", err)
			return
		}
		a.log.Info("device asks to be paired", "mac", d.MAC, "code", requested.PairingCode,
			"hint", "overscan devices approve "+requested.PairingCode)
		resp.Code = requested.PairingCode
		resp.Secret = secret
	}
	writeJSON(w, http.StatusOK, resp)
}

// startSession gives an approved, paired device a session token.
func (a *API) startSession(resp *sessionResponse, d store.Device) {
	now := a.now()
	resp.State = store.DeviceApproved
	resp.Token = a.sign(sessionClaims{deviceID: d.ID, mac: d.MAC, secretTag: secretTag(d.SecretHash), issued: now.Unix()})
	resp.Expires = now.Add(sessionTTL).Unix()
	resp.Label = d.Label
	a.log.Info("device session started", "mac", d.MAC, "label", d.Label, "model", d.Model)
}

// requestPairing records a request to pair with a new code, trying another
// code if one is taken.
func (a *API) requestPairing(ctx context.Context, mac, model, secretHash string) (store.Device, error) {
	for range 10 {
		d, err := a.store.RequestPairing(ctx, mac, model, newCode(), secretHash, a.now())
		if !errors.Is(err, store.ErrCodeTaken) {
			return d, err
		}
	}
	return store.Device{}, errors.New("no free pairing code")
}

// newSecret returns a new device secret: 256 random bits in URL-safe base64.
func newSecret() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // never fails; see crypto/rand
	return base64.RawURLEncoding.EncodeToString(b)
}

// newCode returns a random pairing code of six digits.
func newCode() string {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		panic(err) // crypto/rand never fails
	}
	return fmt.Sprintf("%06d", n.Int64())
}

// secretMatches reports whether secret is the one whose hash is hash.
func secretMatches(secret, hash string) bool {
	return secret != "" && hash != "" &&
		subtle.ConstantTimeCompare([]byte(store.HashSecret(secret)), []byte(hash)) == 1
}

// authorized wraps a handler for calls that need a session: an approved
// device's current token.
func (a *API) authorized(next func(http.ResponseWriter, *http.Request, store.Device)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, ok := a.verify(bearerToken(r))
		if !ok || a.now().Unix() >= c.issued+int64(sessionTTL/time.Second) {
			unauthorized(w)
			return
		}
		d, err := a.devices.Known(r.Context(), c.mac, "")
		if errors.Is(err, store.ErrNotFound) {
			unauthorized(w)
			return
		}
		if err != nil {
			a.internalError(w, "look up device", err)
			return
		}
		switch {
		case d.ID != c.deviceID: // removed, and a new device has its ID
			unauthorized(w)
		case c.secretTag != secretTag(d.SecretHash): // paired again since
			unauthorized(w)
		case d.State == store.DeviceApproved:
			next(w, r, d)
		case d.State == store.DeviceBlocked:
			writeError(w, http.StatusForbidden, errBlocked)
		default:
			unauthorized(w)
		}
	}
}

// bearerToken returns the token from an "Authorization: Bearer" header.
func bearerToken(r *http.Request) string {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

// writeJSON answers with v as JSON.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError answers with {"error": code}.
func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, struct {
		Error string `json:"error"`
	}{code})
}

// unauthorized answers a call without a valid session; the page starts a new
// one.
func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeError(w, http.StatusUnauthorized, errUnauthorized)
}

func (a *API) internalError(w http.ResponseWriter, what string, err error) {
	a.log.Error("device API failed", "while", what, "error", err)
	writeError(w, http.StatusInternalServerError, errInternal)
}
