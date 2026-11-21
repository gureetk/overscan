// SPDX-License-Identifier: AGPL-3.0-or-later

// Package routes is an http.ServeMux that keeps the patterns it is given,
// so that a test can walk every route of the server and check who may call
// each one (internal/server's TestEveryRouteIsGuarded).
package routes

import (
	"net/http"
	"slices"
)

// Mux routes requests as an http.ServeMux does, and keeps its patterns.
type Mux struct {
	mux      *http.ServeMux
	patterns []string
}

// NewMux returns an empty Mux.
func NewMux() *Mux { return &Mux{mux: http.NewServeMux()} }

// A Lister is a handler that knows its own routes: a part of the server
// that a Mux serves below a prefix.
type Lister interface {
	Patterns() []string
}

// Handle registers h for pattern, as http.ServeMux does. When h lists
// routes of its own (Lister), the Mux keeps those in place of pattern.
func (m *Mux) Handle(pattern string, h http.Handler) {
	m.mux.Handle(pattern, h)
	if l, ok := h.(Lister); ok {
		m.patterns = append(m.patterns, l.Patterns()...)
		return
	}
	m.patterns = append(m.patterns, pattern)
}

// HandleFunc registers f for pattern, as http.ServeMux does.
func (m *Mux) HandleFunc(pattern string, f func(http.ResponseWriter, *http.Request)) {
	m.mux.HandleFunc(pattern, f)
	m.patterns = append(m.patterns, pattern)
}

// ServeHTTP serves a request with the handler whose pattern matches it.
func (m *Mux) ServeHTTP(w http.ResponseWriter, r *http.Request) { m.mux.ServeHTTP(w, r) }

// Handler returns the handler that would serve r, and its pattern, as
// http.ServeMux.Handler does.
func (m *Mux) Handler(r *http.Request) (http.Handler, string) { return m.mux.Handler(r) }

// Patterns returns the patterns of every route, each once, in the order
// they were registered.
func (m *Mux) Patterns() []string {
	var out []string
	for _, p := range m.patterns {
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// With returns a handler that serves as h does and lists the routes of l:
// for a handler that puts headers or checks in front of a Mux.
func With(h http.Handler, l Lister) http.Handler { return with{h, l} }

type with struct {
	http.Handler
	l Lister
}

func (w with) Patterns() []string { return w.l.Patterns() }
