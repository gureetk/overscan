// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"overscan.gureet.ca/internal/store"
)

// APIPrefix is where the admin's JSON API is (D14). docs/api/admin.yaml
// documents it. It shares the pages' sessions: a script signs in with POST
// session, keeps the cookie, and sends the session's CSRF token in the
// X-CSRF-Token header with every request that changes something.
const APIPrefix = "/api/admin/v1/"

// maxJSONBytes caps a request's JSON body.
const maxJSONBytes = 64 << 10

// apiRoute is an operation of the API.
type apiRoute struct {
	method, path string // path below APIPrefix
	open         bool   // needs no session
	fn           signedInFn
	openFn       http.HandlerFunc
}

// apiRoutes lists the API's operations. A test checks that the OpenAPI
// document describes each of them, and nothing else.
func (a *Admin) apiRoutes() []apiRoute {
	return []apiRoute{
		{method: "POST", path: "session", open: true, openFn: a.apiSignIn},
		{method: "GET", path: "session", fn: a.apiSession},
		{method: "DELETE", path: "session", fn: a.apiSignOut},
		{method: "GET", path: "server", fn: a.apiServer},
		{method: "GET", path: "streams", fn: a.apiStreams},
		{method: "GET", path: "capture", fn: a.apiCaptures},
		{method: "DELETE", path: "capture", fn: a.apiClearCaptures},
		{method: "GET", path: "devices", fn: a.apiDevices},
		{method: "POST", path: "devices/{mac}/approve", fn: a.apiApproveDevice},
		{method: "POST", path: "devices/{mac}/block", fn: a.apiBlockDevice},
		{method: "PATCH", path: "devices/{mac}", fn: a.apiChangeDevice},
		{method: "DELETE", path: "devices/{mac}", fn: a.apiRemoveDevice},
		{method: "DELETE", path: "devices/{mac}/playback-check", fn: a.apiForgetCheck},
		{method: "POST", path: "pairings/{code}/approve", fn: a.apiApproveCode},
		{method: "GET", path: "accounts", fn: a.apiAccounts},
		{method: "POST", path: "accounts", fn: a.apiAddAccount},
		{method: "GET", path: "accounts/{id}", fn: a.apiAccount},
		{method: "PATCH", path: "accounts/{id}", fn: a.apiChangeAccount},
		{method: "DELETE", path: "accounts/{id}", fn: a.apiRemoveAccount},
		{method: "PUT", path: "accounts/{id}/login", fn: a.apiSetLogin},
		{method: "DELETE", path: "accounts/{id}/login", fn: a.apiRemoveLogin},
		{method: "GET", path: "accounts/{id}/token", fn: a.apiToken},
		{method: "POST", path: "accounts/{id}/token", fn: a.apiNewToken},
		{method: "PUT", path: "accounts/{id}/expiry", fn: a.apiSetAccountExpiry},
		{method: "DELETE", path: "accounts/{id}/expiry", fn: a.apiRemoveAccountExpiry},
		{method: "PUT", path: "accounts/{id}/pin", fn: a.apiSetAccountPIN},
		{method: "DELETE", path: "accounts/{id}/pin", fn: a.apiRemoveAccountPIN},
		{method: "GET", path: "messages", fn: a.apiMessages},
		{method: "POST", path: "messages", fn: a.apiSendMessage},
		{method: "DELETE", path: "messages/{id}", fn: a.apiDeleteMessage},
		{method: "GET", path: "themes", fn: a.apiThemes},
		{method: "POST", path: "themes", fn: a.apiInstallTheme},
		{method: "DELETE", path: "themes/{id}", fn: a.apiRemoveTheme},
		{method: "PUT", path: "theme", fn: a.apiSetTheme},
		{method: "GET", path: "profiles", fn: a.apiProfiles},
		{method: "POST", path: "profiles", fn: a.apiAddProfile},
		{method: "PATCH", path: "profiles/{id}", fn: a.apiChangeProfile},
		{method: "DELETE", path: "profiles/{id}", fn: a.apiRemoveProfile},
		{method: "GET", path: "pin", fn: a.apiPIN},
		{method: "PUT", path: "pin", fn: a.apiSetPIN},
		{method: "DELETE", path: "pin", fn: a.apiRemovePIN},
		{method: "GET", path: "expiry", fn: a.apiExpiry},
		{method: "PUT", path: "expiry", fn: a.apiSetExpiry},
		{method: "DELETE", path: "expiry", fn: a.apiRemoveExpiry},
		{method: "GET", path: "settings", fn: a.apiSettings},
		{method: "PATCH", path: "settings", fn: a.apiChangeSettings},
		{method: "GET", path: "recordings", fn: a.apiRecordings},
		{method: "POST", path: "recordings", fn: a.apiSchedule},
		{method: "POST", path: "recordings/{id}/cancel", fn: a.apiCancelRecording},
		{method: "DELETE", path: "recordings/{id}", fn: a.apiDeleteRecording},
		{method: "GET", path: "lineups", fn: a.apiLineups},
		{method: "POST", path: "lineups", fn: a.apiLineupRoute(a.apiCopyLineup)},
		{method: "PATCH", path: "lineups/{id}", fn: a.apiChangeLineup},
		{method: "DELETE", path: "lineups/{id}", fn: a.apiRemoveLineup},
		{method: "GET", path: "lineups/file", fn: a.apiExportLineups},
		{method: "POST", path: "lineups/file", fn: a.apiImportLineups},
		{method: "GET", path: "lineup/groups", fn: a.apiLineupRoute(a.apiGroups)},
		{method: "POST", path: "lineup/groups", fn: a.apiLineupRoute(a.apiCreateGroup)},
		{method: "PATCH", path: "lineup/groups/{id}", fn: a.apiLineupRoute(a.apiChangeGroup)},
		{method: "DELETE", path: "lineup/groups/{id}", fn: a.apiLineupRoute(a.apiDeleteGroup)},
		{method: "POST", path: "lineup/groups/{id}/merge", fn: a.apiLineupRoute(a.apiMergeGroup)},
		{method: "POST", path: "lineup/groups/{id}/number", fn: a.apiLineupRoute(a.apiNumberGroup)},
		{method: "PUT", path: "lineup/categories/{id}", fn: a.apiLineupRoute(a.apiMoveCategory)},
		{method: "GET", path: "lineup/channels", fn: a.apiLineupRoute(a.apiEntries)},
		{method: "PATCH", path: "lineup/channels", fn: a.apiLineupRoute(a.apiChangeEntries)},
		{method: "PATCH", path: "lineup/channels/{id}", fn: a.apiLineupRoute(a.apiChangeEntry)},
		{method: "GET", path: "lineup/channels/{id}/backups", fn: a.apiLineupRoute(a.apiBackups)},
		{method: "PUT", path: "lineup/channels/{id}/backups", fn: a.apiLineupRoute(a.apiSetBackups)},
		{method: "GET", path: "lineup/rules", fn: a.apiLineupRoute(a.apiRules)},
		{method: "POST", path: "lineup/rules", fn: a.apiLineupRoute(a.apiAddRule)},
		{method: "PUT", path: "lineup/rules/{id}", fn: a.apiLineupRoute(a.apiSetRule)},
		{method: "DELETE", path: "lineup/rules/{id}", fn: a.apiLineupRoute(a.apiRemoveRule)},
		{method: "POST", path: "lineup/rules/{id}/move", fn: a.apiLineupRoute(a.apiMoveRule)},
		{method: "GET", path: "library/{kind}/shelves", fn: a.apiShelves},
		{method: "PATCH", path: "library/{kind}/shelves/{id}", fn: a.apiChangeShelf},
		{method: "PUT", path: "library/{kind}/order", fn: a.apiOrderShelves},
		{method: "DELETE", path: "library/{kind}/order", fn: a.apiResetOrder},
		{method: "GET", path: "library/{kind}/titles", fn: a.apiTitles},
		{method: "PATCH", path: "library/titles/{id}", fn: a.apiChangeTitle},
		{method: "GET", path: "sources", fn: a.apiSources},
		{method: "POST", path: "sources", fn: a.apiAddSource},
		{method: "POST", path: "sources/test", fn: a.apiTestNewSource},
		{method: "PATCH", path: "sources/{id}", fn: a.apiChangeSource},
		{method: "DELETE", path: "sources/{id}", fn: a.apiDeleteSource},
		{method: "POST", path: "sources/{id}/refresh", fn: a.apiRefreshSource},
		{method: "POST", path: "sources/{id}/test", fn: a.apiTestSource},
	}
}

// apiMux serves the API's routes, with JSON answers for paths and methods
// that it does not have.
func (a *Admin) apiMux() http.Handler {
	mux := http.NewServeMux()
	for _, rt := range a.apiRoutes() {
		pattern := rt.method + " " + APIPrefix + rt.path
		if rt.open {
			mux.HandleFunc(pattern, rt.openFn)
		} else {
			mux.Handle(pattern, a.api(rt.fn))
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pattern := mux.Handler(r); pattern != "" {
			mux.ServeHTTP(w, r) // which also fills in the path's wildcards
			return
		}
		// Ask the mux what it would answer, and say it in JSON.
		probe := &statusProbe{header: http.Header{}}
		mux.ServeHTTP(probe, r)
		if probe.code == http.StatusMethodNotAllowed {
			w.Header().Set("Allow", probe.header.Get("Allow"))
			apiError(w, http.StatusMethodNotAllowed, "method_not_allowed", "This path does not take "+r.Method+".")
			return
		}
		apiError(w, http.StatusNotFound, "not_found", "There is no such path in the API.")
	})
}

// statusProbe keeps the status and headers that a handler writes.
type statusProbe struct {
	header http.Header
	code   int
}

func (p *statusProbe) Header() http.Header         { return p.header }
func (p *statusProbe) Write(b []byte) (int, error) { return len(b), nil }
func (p *statusProbe) WriteHeader(code int)        { p.code = code }

// api serves fn to a request with a valid session. A request that changes
// something must carry the session's CSRF token in the X-CSRF-Token header.
func (a *Admin) api(fn signedInFn) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, ok := a.authenticate(r)
		if !ok {
			apiError(w, http.StatusUnauthorized, "unauthenticated", "Sign in first, with POST "+APIPrefix+"session.")
			return
		}
		if !safeMethod(r.Method) {
			got := r.Header.Get(csrfHeader)
			if got == "" || !validCSRF(r, s) {
				apiError(w, http.StatusForbidden, "csrf", "Send the session's CSRF token in the "+csrfHeader+" header.")
				return
			}
		}
		fn(w, r, s)
	})
}

// apiErrorBody is the body of every error the API answers with.
type apiErrorBody struct {
	Error   string `json:"error"`   // a code for scripts
	Message string `json:"message"` // a sentence for people
}

func apiError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, apiErrorBody{Error: code, Message: message})
}

// writeJSON writes v as the answer, which is never cached.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if v != nil {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(v)
	}
}

// readJSON decodes a request's JSON body into v, answering for the request
// if it cannot.
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		apiError(w, http.StatusUnsupportedMediaType, "not_json", "Send the body as application/json.")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			apiError(w, http.StatusRequestEntityTooLarge, "too_large", "The body is too large.")
		} else {
			apiError(w, http.StatusBadRequest, "bad_json", "The body is not the JSON this path takes: "+err.Error()+".")
		}
		return false
	}
	if _, err := dec.Token(); err != io.EOF {
		apiError(w, http.StatusBadRequest, "bad_json", "The body holds more than one JSON value.")
		return false
	}
	return true
}

// apiSessionBody describes a session.
type apiSessionBody struct {
	Username  string    `json:"username"`
	CSRFToken string    `json:"csrf_token"`
	CreatedAt time.Time `json:"created_at"`
	LastSeen  time.Time `json:"last_seen"`
}

func sessionBody(s store.AdminSession) apiSessionBody {
	return apiSessionBody{Username: s.Username, CSRFToken: s.CSRFToken, CreatedAt: s.CreatedAt, LastSeen: s.LastSeen}
}

// apiSignIn starts a session from a username and password.
func (a *Admin) apiSignIn(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	token, s, fail := a.startSession(r, strings.TrimSpace(body.Username), body.Password)
	if fail != nil {
		if r.Context().Err() != nil {
			return
		}
		if fail.wait > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(fail.wait.Seconds()+0.999)))
		}
		message := fail.message
		if fail.code == "no_administrator" {
			message = "No password is set yet. On the server, run: overscan admin password."
		}
		apiError(w, fail.status, fail.code, message)
		return
	}
	http.SetCookie(w, sessionCookie(r, token, int(sessionMax.Seconds())))
	writeJSON(w, http.StatusOK, sessionBody(s))
}

// apiSession describes the request's session.
func (a *Admin) apiSession(w http.ResponseWriter, _ *http.Request, s store.AdminSession) {
	writeJSON(w, http.StatusOK, sessionBody(s))
}

// apiSignOut ends the request's session.
func (a *Admin) apiSignOut(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	if err := a.store.EndAdminSession(r.Context(), s.ID); err != nil {
		a.log.Error("cannot end an admin session", "error", err)
		apiError(w, http.StatusInternalServerError, "internal", "Something went wrong; the server's log says what.")
		return
	}
	http.SetCookie(w, sessionCookie(r, "", -1))
	a.log.Info("admin signed out", "address", clientAddress(r), "session", s.ID)
	w.WriteHeader(http.StatusNoContent)
}

// apiServer describes the server.
func (a *Admin) apiServer(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	schema, err := a.store.SchemaVersion(r.Context())
	if err != nil {
		a.log.Error("cannot read the schema version", "error", err)
		apiError(w, http.StatusInternalServerError, "internal", "Something went wrong; the server's log says what.")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Version       string    `json:"version"`
		SchemaVersion int       `json:"schema_version"`
		StartedAt     time.Time `json:"started_at"`
	}{a.version, schema, a.started})
}
