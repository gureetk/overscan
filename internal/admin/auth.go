// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"overscan.gureet.ca/internal/store"
)

// CookieName is the cookie that holds an admin session's token.
const CookieName = "overscan_admin"

// Sessions end after a week without use, and a month after signing in.
const (
	sessionIdle = 7 * 24 * time.Hour
	sessionMax  = 30 * 24 * time.Hour
	touchEvery  = time.Minute // how often a session's last use is written down
)

// Limits on what a request to the admin may send.
const (
	maxFormBytes = 64 << 10
	maxUserAgent = 200
	maxUsername  = 64
)

// The CSRF token comes in a form field, or in a header from scripts.
const (
	csrfField  = "csrf"
	csrfHeader = "X-CSRF-Token"
)

// signedInFn is a handler for a signed-in administrator.
type signedInFn func(http.ResponseWriter, *http.Request, store.AdminSession)

// page serves fn to a signed-in administrator and sends others to the
// sign-in page. A request that is meant to change something must carry the
// session's CSRF token.
func (a *Admin) page(fn signedInFn) http.Handler {
	return a.pageWithin(maxFormBytes, fn)
}

// pageWithin is page for a form that sends up to limit bytes, such as one
// that sends a file.
func (a *Admin) pageWithin(limit int64, fn signedInFn) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, ok := a.authenticate(r)
		if !ok {
			toSignIn(w, r)
			return
		}
		if !safeMethod(r.Method) {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			if !validCSRF(r, s) {
				a.message(w, r, http.StatusForbidden, "This form has expired",
					"The page that sent it is out of date. Go back, reload it and try again.", &s)
				return
			}
		}
		fn(w, r, s)
	})
}

// authenticate returns the session that the request's cookie names, if it is
// still valid. It ends a session that has expired.
func (a *Admin) authenticate(r *http.Request) (store.AdminSession, bool) {
	c, err := r.Cookie(CookieName)
	if err != nil || len(c.Value) < 32 || len(c.Value) > 64 {
		return store.AdminSession{}, false
	}
	s, err := a.store.AdminSession(r.Context(), store.HashSecret(c.Value))
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			a.log.Error("cannot read an admin session", "error", err)
		}
		return store.AdminSession{}, false
	}
	now := a.now()
	if now.Sub(s.CreatedAt) > sessionMax || now.Sub(s.LastSeen) > sessionIdle {
		if err := a.store.EndAdminSession(r.Context(), s.ID); err != nil {
			a.log.Warn("cannot end an expired admin session", "error", err)
		}
		return store.AdminSession{}, false
	}
	if now.Sub(s.LastSeen) >= touchEvery {
		if err := a.store.TouchAdminSession(r.Context(), s.ID, now); err != nil {
			a.log.Warn("cannot record the use of an admin session", "error", err)
		}
		s.LastSeen = now
	}
	return s, true
}

// toSignIn sends a request without a valid session to the sign-in page,
// which comes back to the page asked for.
func toSignIn(w http.ResponseWriter, r *http.Request) {
	htmx := r.Header.Get("HX-Request") == "true"
	var next string
	switch {
	case htmx:
		// htmx asked for part of a page; the browser comes back to the
		// page it was on.
		if u, err := url.Parse(r.Header.Get("HX-Current-URL")); err == nil && u.Host == r.Host {
			next = u.RequestURI()
		}
	case r.Method == http.MethodGet:
		next = r.URL.RequestURI()
	}
	target := "/admin/sign-in"
	if next != "" && next != "/admin/" {
		target += "?next=" + url.QueryEscape(next)
	}
	if htmx {
		// htmx would put the sign-in page inside the page; this makes it
		// load the sign-in page instead.
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// safeMethod reports whether a request with the method changes nothing.
func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// validCSRF reports whether the request carries the session's CSRF token.
func validCSRF(r *http.Request, s store.AdminSession) bool {
	got := r.Header.Get(csrfHeader)
	if got == "" {
		got = r.PostFormValue(csrfField)
	}
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(s.CSRFToken)) == 1
}

// signInData is what the sign-in page shows.
type signInData struct {
	NoAdmin  bool   // no password has been set yet
	Username string // as typed
	Next     string // where to go after signing in
}

// signInPage shows the sign-in form, or says how to set the first password.
func (a *Admin) signInPage(w http.ResponseWriter, r *http.Request) {
	next := safeNext(r.URL.Query().Get("next"))
	if _, ok := a.authenticate(r); ok {
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	_, err := a.store.AdminUser(r.Context())
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		a.log.Error("cannot read the administrator", "error", err)
		a.message(w, r, http.StatusInternalServerError, "Something went wrong", "The server's log says what.", nil)
		return
	}
	data := signInData{NoAdmin: err != nil, Next: next}
	a.render(w, r, http.StatusOK, "sign-in", view{Title: "Sign in", Data: data})
}

// signInFailure says why credentials did not start a session.
type signInFailure struct {
	status  int
	code    string // for the API
	message string // for people; "" when there is no administrator yet
	wait    time.Duration
}

// startSession checks the credentials of a request to sign in and, if they
// are right, starts a session and returns its cookie's token. Failures from
// one address, and from all, are slowed down.
func (a *Admin) startSession(r *http.Request, username, password string) (string, store.AdminSession, *signInFailure) {
	ctx := r.Context()
	addr := clientAddress(r)
	now := a.now()
	broken := &signInFailure{http.StatusInternalServerError, "internal", "Something went wrong; the server's log says what.", 0}

	if wait := a.limit.wait(addr, now); wait > 0 {
		minutes := int(math.Ceil(wait.Minutes()))
		a.log.Warn("admin sign-in refused after too many failures", "address", addr)
		return "", store.AdminSession{}, &signInFailure{http.StatusTooManyRequests, "too_many_failures",
			fmt.Sprintf("Too many failed sign-ins. Try again in %d %s.", minutes, plural(minutes, "minute")), wait}
	}
	user, err := a.store.AdminUser(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return "", store.AdminSession{}, &signInFailure{http.StatusForbidden, "no_administrator", "", 0}
	}
	if err != nil {
		a.log.Error("cannot read the administrator", "error", err)
		return "", store.AdminSession{}, broken
	}
	// The password is checked even for a wrong username, so that a
	// username cannot be told from the time an answer takes.
	ok, err := CheckPassword(ctx, user.PasswordHash, password)
	if err != nil {
		if ctx.Err() == nil {
			a.log.Error("cannot check the administrator's password", "error", err)
		}
		return "", store.AdminSession{}, broken
	}
	if !ok || subtle.ConstantTimeCompare([]byte(username), []byte(user.Username)) != 1 {
		a.limit.fail(addr, now)
		a.log.Warn("admin sign-in failed", "address", addr)
		return "", store.AdminSession{}, &signInFailure{http.StatusUnauthorized, "wrong_credentials", "The username or password is wrong.", 0}
	}
	a.limit.succeed(addr)

	if _, err := a.store.EndExpiredAdminSessions(ctx, now.Add(-sessionMax), now.Add(-sessionIdle)); err != nil {
		a.log.Warn("cannot end expired admin sessions", "error", err)
	}
	token := randomToken()
	s, err := a.store.CreateAdminSession(ctx, store.HashSecret(token), randomToken(), addr,
		truncate(r.UserAgent(), maxUserAgent), now)
	if err != nil {
		a.log.Error("cannot start an admin session", "error", err)
		return "", store.AdminSession{}, broken
	}
	a.log.Info("admin signed in", "address", addr, "session", s.ID)
	return token, s, nil
}

// signIn signs in from the sign-in page.
func (a *Admin) signIn(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		a.message(w, r, http.StatusBadRequest, "Sign in", "The form could not be read.", nil)
		return
	}
	username := strings.TrimSpace(r.PostFormValue("username"))
	data := signInData{Username: truncate(username, maxUsername), Next: safeNext(r.PostFormValue("next"))}
	token, _, fail := a.startSession(r, username, r.PostFormValue("password"))
	if fail != nil {
		if r.Context().Err() != nil {
			return
		}
		if fail.wait > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(fail.wait.Seconds()))))
		}
		data.NoAdmin = fail.code == "no_administrator"
		a.render(w, r, fail.status, "sign-in", view{Title: "Sign in", Error: fail.message, Data: data})
		return
	}
	http.SetCookie(w, sessionCookie(r, token, int(sessionMax.Seconds())))
	http.Redirect(w, r, data.Next, http.StatusSeeOther)
}

// signOut ends the session.
func (a *Admin) signOut(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	if err := a.store.EndAdminSession(r.Context(), s.ID); err != nil {
		a.log.Error("cannot end an admin session", "error", err)
		a.message(w, r, http.StatusInternalServerError, "Something went wrong", "The server's log says what.", &s)
		return
	}
	http.SetCookie(w, sessionCookie(r, "", -1))
	a.log.Info("admin signed out", "address", clientAddress(r), "session", s.ID)
	http.Redirect(w, r, "/admin/sign-in", http.StatusSeeOther)
}

// passwordPage shows the form that changes the password.
func (a *Admin) passwordPage(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	a.render(w, r, http.StatusOK, "password", view{Title: "Password", Section: "settings", Session: &s})
}

// changePassword sets a new password, given the current one, and ends every
// other session.
func (a *Admin) changePassword(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	ctx := r.Context()
	addr := clientAddress(r)
	now := a.now()
	failed := func(status int, text string) {
		a.render(w, r, status, "password", view{Title: "Password", Section: "settings", Session: &s, Error: text})
	}
	if wait := a.limit.wait(addr, now); wait > 0 {
		minutes := int(math.Ceil(wait.Minutes()))
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
		failed(http.StatusTooManyRequests, fmt.Sprintf("Too many wrong passwords. Try again in %d %s.", minutes, plural(minutes, "minute")))
		return
	}
	user, err := a.store.AdminUser(ctx)
	if err != nil {
		a.log.Error("cannot read the administrator", "error", err)
		failed(http.StatusInternalServerError, "Something went wrong; the server's log says what.")
		return
	}
	ok, err := CheckPassword(ctx, user.PasswordHash, r.PostFormValue("current"))
	if err != nil {
		if ctx.Err() == nil {
			a.log.Error("cannot check the administrator's password", "error", err)
			failed(http.StatusInternalServerError, "Something went wrong; the server's log says what.")
		}
		return
	}
	if !ok {
		a.limit.fail(addr, now)
		a.log.Warn("admin password change refused: wrong current password", "address", addr)
		failed(http.StatusBadRequest, "The current password is wrong.")
		return
	}
	next := r.PostFormValue("new")
	if err := CheckNewPassword(next); err != nil {
		failed(http.StatusBadRequest, sentence(err.Error()))
		return
	}
	if next != r.PostFormValue("again") {
		failed(http.StatusBadRequest, "The two new passwords differ.")
		return
	}
	hash, err := HashPassword(ctx, next, a.params)
	if err != nil {
		return // the request was cancelled
	}
	if err := a.store.SetAdminPassword(ctx, user.Username, hash, now, s.ID); err != nil {
		a.log.Error("cannot set the administrator's password", "error", err)
		failed(http.StatusInternalServerError, "Something went wrong; the server's log says what.")
		return
	}
	a.log.Info("admin password changed; the other admin sessions ended", "address", addr, "session", s.ID)
	a.render(w, r, http.StatusOK, "password", view{Title: "Password", Section: "settings", Session: &s,
		Notice: "The password is changed. Every other session is signed out."})
}

// sessionCookie returns the cookie that holds a session's token; maxAge -1
// deletes it.
func sessionCookie(r *http.Request, token string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
	}
}

// randomToken returns 32 random bytes, encoded for a cookie or a form.
func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// clientAddress returns the address that a request came from, without its
// port.
func clientAddress(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// safeNext returns where to go after signing in: next if it is a page of the
// admin, else the admin's first page.
func safeNext(next string) string {
	const home = "/admin/"
	if next == "" || strings.HasPrefix(next, "//") || strings.ContainsAny(next, "\\\r\n") {
		return home
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil ||
		!strings.HasPrefix(u.Path, home) || strings.HasPrefix(u.Path, "/admin/sign-in") || strings.Contains(u.Path, "/../") {
		return home
	}
	return u.RequestURI()
}

// truncate cuts s to at most n bytes, at a character boundary.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !isRuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// sentence makes an error message a sentence.
func sentence(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:] + "."
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// initial is the first letter of a name, as a capital, for an avatar.
func initial(name string) string {
	for _, r := range name {
		return strings.ToUpper(string(r))
	}
	return "?"
}
