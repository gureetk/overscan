// SPDX-License-Identifier: AGPL-3.0-or-later

package routes

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func TestMuxKeepsItsPatterns(t *testing.T) {
	inner := NewMux()
	inner.HandleFunc("GET /part/a", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("a")) })
	inner.HandleFunc("POST /part/b/{id}", func(http.ResponseWriter, *http.Request) {})

	m := NewMux()
	m.HandleFunc("GET /healthz", func(http.ResponseWriter, *http.Request) {})
	// A part mounted below a prefix, behind something that adds a header.
	part := With(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Part", "yes")
		inner.ServeHTTP(w, r)
	}), inner)
	m.Handle("/part/", part)
	m.Handle("/part/again/", part)
	m.Handle("GET /files/", http.NotFoundHandler())

	want := []string{"GET /healthz", "GET /part/a", "POST /part/b/{id}", "GET /files/"}
	if got := m.Patterns(); !slices.Equal(got, want) {
		t.Errorf("patterns %q, want %q", got, want)
	}
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest("GET", "/part/a", nil))
	if rec.Body.String() != "a" || rec.Header().Get("X-Part") != "yes" {
		t.Errorf("GET /part/a: %d %q %v", rec.Code, rec.Body, rec.Header())
	}
	if _, pattern := m.Handler(httptest.NewRequest("GET", "/files/x", nil)); pattern != "GET /files/" {
		t.Errorf("pattern %q", pattern)
	}
}
