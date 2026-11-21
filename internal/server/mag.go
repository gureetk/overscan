// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"errors"
	"io/fs"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"overscan.gureet.ca/internal/clientaddr"
	"overscan.gureet.ca/internal/store"
	"overscan.gureet.ca/internal/theme"
	"overscan.gureet.ca/web"
)

// magPaths are where the MAG page is served: /c/, and /stalker_portal/c/,
// where a box set up for a Stalker portal loads it.
var magPaths = []string{"/c/", "/stalker_portal/c/"}

// hashedAsset matches the names of the page's files whose names carry a hash
// of their content: its script and stylesheet, fonts and pictures.
var hashedAsset = regexp.MustCompile(`^[a-z0-9-]+\.[0-9a-f]{10}\.(js|css|woff|jpg|png|svg)$`)

// magLogInterval is how long the server waits before it logs another
// request for the page from the same address. A box requests the page twice
// when it loads a portal.
const magLogInterval = 10 * time.Second

// pageLog remembers when the page was last logged for each address.
type pageLog struct {
	mu   sync.Mutex
	last map[string]time.Time
}

// magFiles serves the MAG page under prefix. index.html is never cached, so
// a box always runs the page that the server ships, while the files whose
// names change with their content are cached for a year.
func (s *Server) magFiles(prefix string) http.Handler {
	files, err := fs.Sub(web.MAG, "mag/dist")
	if err != nil {
		panic(err) // the directory is embedded at build time
	}
	server := http.StripPrefix(prefix, http.FileServerFS(files))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, prefix)
		if id, ok := theme.ParsePath(name); ok {
			s.themeStylesheet(w, r, id, name)
			return
		}
		switch {
		case name == "" || name == "index.html":
			w.Header().Set("Cache-Control", "no-store")
			if name == "" {
				s.noteMAGPage(r)
			}
		case hashedAsset.MatchString(name):
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			if strings.HasSuffix(name, ".woff") {
				// Not in every system's table of types.
				w.Header().Set("Content-Type", "font/woff")
			}
		}
		server.ServeHTTP(w, r)
	})
}

// themeStylesheet serves the stylesheet of an installed theme (D97), beside
// the page, so that the fonts it names load from where the page's do. Its
// name holds a hash of the theme and of the page's own stylesheet, so a
// name that is not the theme's now answers 404, and one that is may be
// cached for a year. It holds colours and a few choices, and nothing
// secret, so it needs no session.
func (s *Server) themeStylesheet(w http.ResponseWriter, r *http.Request, id int64, name string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	th, err := s.services.Store.Theme(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) || err == nil && theme.Path(th.ID, th.Data) != name {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.log.Error("cannot read a theme", "theme", id, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	css, err := s.themes.get(name, th.Data)
	if err != nil {
		// What the store keeps was checked before: this is a theme of a
		// later format, after a downgrade.
		s.log.Warn("a theme cannot be shown", "theme", th.Name, "error", err)
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write([]byte(css))
}

// themeSheets keeps the stylesheets that themes made, by name, so that each
// is made once. It keeps a few: a household has few themes.
type themeSheets struct {
	mu    sync.Mutex
	sheet map[string]string
}

func (t *themeSheets) get(name string, data []byte) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if css, ok := t.sheet[name]; ok {
		return css, nil
	}
	css, err := theme.Stylesheet(data)
	if err != nil {
		return "", err
	}
	if t.sheet == nil || len(t.sheet) >= 16 {
		t.sheet = map[string]string{}
	}
	t.sheet[name] = css
	return css, nil
}

// noteMAGPage logs a request for the page, so the administrator sees a box
// load it, at most once per address in magLogInterval.
func (s *Server) noteMAGPage(r *http.Request) {
	from := clientaddr.Of(r).String()
	now := time.Now()
	s.pages.mu.Lock()
	last, seen := s.pages.last[from]
	log := !seen || now.Sub(last) >= magLogInterval
	if log {
		if s.pages.last == nil || len(s.pages.last) >= 1000 {
			s.pages.last = map[string]time.Time{}
		}
		s.pages.last[from] = now
	}
	s.pages.mu.Unlock()
	if log {
		s.log.Info("MAG page requested", "from", from, "user_agent", truncate(r.UserAgent(), 200))
	}
}

// truncate shortens s to at most n bytes.
func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// handleRoot sends a box or browser that loads the server's address to the
// MAG page.
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, magPaths[0], http.StatusFound)
}
