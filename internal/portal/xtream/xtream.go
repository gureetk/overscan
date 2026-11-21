// SPDX-License-Identifier: AGPL-3.0-or-later

// Package xtream serves the Xtream-compatible API to players (D104): an
// account's login signs in at player_api.php, which lists the channels
// that the account sees, its films and series, and their guide, and the
// stream paths play them through the hub as the account. What it answers
// is in docs/protocol/xtream-api-notes.md, section 10, and every choice
// the notes leave open is marked there.
package xtream

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"overscan.gureet.ca/internal/admin"
	"overscan.gureet.ca/internal/hub"
	"overscan.gureet.ca/internal/library"
	"overscan.gureet.ca/internal/portal/output"
	"overscan.gureet.ca/internal/routes"
	"overscan.gureet.ca/internal/store"
)

// How long a call waits for the library: for a source's list when a
// player first asks for films or series, and for a title's details.
const (
	libraryWait = 8 * time.Second
	openWait    = 8 * time.Second
)

// maxLoginField caps a username or a password that a player sends, in
// bytes: a login holds far less (D102).
const maxLoginField = 256

// Output serves the Xtream-compatible API.
type Output struct {
	store   *store.Store
	hub     *hub.Hub
	library *library.Library // nil if the server has none: no films or series
	logins  *admin.Logins
	log     *slog.Logger
	now     func() time.Time
}

// New returns the output, which checks logins with logins.
func New(st *store.Store, h *hub.Hub, logins *admin.Logins, log *slog.Logger) *Output {
	return &Output{store: st, hub: h, logins: logins, log: log, now: time.Now}
}

// UseLibrary gives the output the films and series to list.
func (o *Output) UseLibrary(l *library.Library) { o.library = l }

// Register adds the output's paths to mux. The hub's own /live/ paths stay
// the hub's: a path of three parts below /live/ whose second part is "r"
// and whose first is one of the hub's tokens is an HLS resource of a
// session, which goes to the hub.
func (o *Output) Register(mux *routes.Mux) {
	mux.HandleFunc("GET /player_api.php", o.api)
	mux.HandleFunc("POST /player_api.php", o.api)
	mux.HandleFunc("GET /get.php", o.playlist)
	mux.HandleFunc("GET /xmltv.php", o.guide)
	mux.HandleFunc("GET /live/{user}/{pass}/{file}", o.live)
	mux.HandleFunc("GET /movie/{user}/{pass}/{file}", o.movie)
	mux.HandleFunc("GET /series/{user}/{pass}/{file}", o.episode)
	mux.HandleFunc("GET /timeshift/{user}/{pass}/{file}", o.archive)
	mux.HandleFunc("GET /timeshift/{user}/{pass}/{minutes}/{start}/{file}", o.timeshift)
}

// Errors of a login, as a player sees them.
var (
	errNoLogin   = errors.New("wrong username or password")
	errNotXtream = errors.New("this account may not use the Xtream API")
)

// login returns the account whose login a request carries, if the outputs
// answer the request and the account may use the Xtream API. Otherwise it
// returns why not: a refusal of the outputs, errNoLogin, errNotXtream, a
// *admin.LoginWait, or the server's own error.
func (o *Output) login(ctx context.Context, r *http.Request, username, password string) (store.Account, *output.Refusal, error) {
	refusal, err := output.Gate(ctx, o.store, r)
	if err != nil || refusal != nil {
		return store.Account{}, refusal, err
	}
	if username == "" || password == "" || len(username) > maxLoginField || len(password) > maxLoginField {
		return store.Account{}, nil, errNoLogin
	}
	acct, err := o.logins.Check(ctx, username, password, output.ClientAddress(r))
	switch {
	case errors.Is(err, admin.ErrLogin):
		return store.Account{}, nil, errNoLogin
	case err != nil:
		return store.Account{}, nil, err
	case !acct.Outputs.Xtream:
		return store.Account{}, nil, errNotXtream
	}
	return acct, nil, nil
}

// failed answers a request that the server could not, and logs why,
// without the request's address, which holds a login.
func (o *Output) failed(w http.ResponseWriter, what string, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	o.log.Error("the Xtream output failed", "while", what, "error", err)
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// refuse answers a stream, a playlist or a guide that a login does not
// open: the outputs' refusal, or 403 with the reason in a few words.
func (o *Output) refuse(w http.ResponseWriter, refusal *output.Refusal, err error) {
	var wait *admin.LoginWait
	switch {
	case refusal != nil:
		refusal.Refuse(w)
	case errors.As(err, &wait):
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.For.Seconds())+1))
		(&output.Refusal{Status: http.StatusTooManyRequests, Text: "Too many wrong logins from this address; wait a while."}).Refuse(w)
	case errors.Is(err, errNoLogin):
		(&output.Refusal{Status: http.StatusForbidden, Text: "Wrong username or password."}).Refuse(w)
	case errors.Is(err, errNotXtream):
		(&output.Refusal{Status: http.StatusForbidden, Text: "This account may not use the Xtream API."}).Refuse(w)
	default:
		o.failed(w, "check a login", err)
	}
}

// writeJSON answers with v as JSON, which is never cached.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

// api serves player_api.php: the account with no action, or what the
// action asks for. A wrong login answers the account as one that did not
// sign in, as panels do (notes, section 2).
func (o *Output) api(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	acct, refusal, err := o.login(ctx, r, r.Form.Get("username"), r.Form.Get("password"))
	if refusal != nil {
		refusal.Refuse(w)
		return
	}
	var wait *admin.LoginWait
	switch {
	case errors.As(err, &wait), errors.Is(err, errNoLogin), errors.Is(err, errNotXtream):
		writeJSON(w, map[string]any{"user_info": map[string]any{"auth": 0}})
		return
	case err != nil:
		o.failed(w, "check a login", err)
		return
	}
	action := r.Form.Get("action")
	if action != "" && acct.Ended(o.now()) {
		// An account that has ended reads its own state, and nothing
		// else (D95).
		writeJSON(w, []any{})
		return
	}
	switch action {
	case "":
		o.account(w, r, acct)
	case "get_live_categories":
		o.liveCategories(w, r, acct)
	case "get_live_streams":
		o.liveStreams(w, r, acct)
	case "get_vod_categories":
		o.categories(w, r, acct, store.KindMovie)
	case "get_series_categories":
		o.categories(w, r, acct, store.KindSeries)
	case "get_vod_streams":
		o.films(w, r, acct)
	case "get_series":
		o.seriesList(w, r, acct)
	case "get_vod_info":
		o.filmInfo(w, r, acct)
	case "get_series_info":
		o.seriesInfo(w, r, acct)
	case "get_short_epg":
		o.shortGuide(w, r, acct)
	case "get_simple_data_table":
		o.dayTable(w, r, acct)
	default:
		o.capture(ctx, r, action)
		writeJSON(w, []any{})
	}
}

// capture adds an action that the output does not know to the capture
// log, for the administrator to see what players ask for: the names of
// its parameters, at most 30, and an example whose login, and whatever
// else can carry a secret, is taken out, and whose values are cut short,
// as the Stalker API's capture does.
func (o *Output) capture(ctx context.Context, r *http.Request, action string) {
	var names []string
	for k := range r.Form {
		names = append(names, truncate(k, 64))
	}
	sort.Strings(names)
	names = slices.Compact(names)
	if len(names) > 30 {
		names = names[:30]
	}
	q := url.Values{}
	for _, k := range names {
		v := r.Form.Get(k)
		if store.SensitiveParam(k) {
			v = "REDACTED"
		}
		q.Set(k, truncate(v, 100))
	}
	_, err := o.store.CaptureRequest(ctx, store.Capture{Module: "xtream", Action: truncate(action, 64), Params: strings.Join(names, ","),
		Example: q.Encode(), UserAgent: truncate(r.UserAgent(), 200)}, o.now(), captureKinds)
	if err != nil {
		o.log.Warn("cannot add a request to the capture log", "error", err)
	}
}

// captureKinds caps the kinds of request in the capture log, as the
// Stalker API's does.
const captureKinds = 200

// truncate cuts s to at most n bytes, without splitting a character.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}
