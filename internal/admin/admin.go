// SPDX-License-Identifier: AGPL-3.0-or-later

// Package admin is Overscan's admin: the pages at /admin/ and the JSON API
// at /api/admin/v1/, behind the administrator's sign-in (D44), and the admin
// token that guards the debug paths.
package admin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	"overscan.gureet.ca/internal/routes"
	"overscan.gureet.ca/internal/store"
	"overscan.gureet.ca/internal/tmdb"
	"overscan.gureet.ca/web"
)

// Admin serves the admin pages.
type Admin struct {
	store   *store.Store
	log     *slog.Logger
	version string
	started time.Time
	now     func() time.Time
	params  Params // for new password hashes
	limit   *limiter
	pages   map[string]*template.Template
	static  fs.FS
	hashes  map[string]string // static file name: a hash of its content
	sources Sources           // reads sources; nil if this server cannot
	guides  Guides            // reads the guides of the household's own (D139); nil if this server cannot
	streams Streams           // lists what is playing; nil if none
	support Support           // what the support bundle needs from outside the admin
	// tmdbBase is TMDB's address, which a test changes (D88).
	tmdbBase string
	recorder Recorder // records programmes; nil if this server cannot (D89)
	folders  Folders  // reads the folder sources again, and looks pictures up on TMDB; nil for none (D88, D143)
	lists    Lists    // reads a source's films or series again; nil for none (D135)
}

// New returns the admin, which keeps its administrator and sessions in st.
func New(st *store.Store, log *slog.Logger, version string) *Admin {
	static, err := fs.Sub(web.Admin, "admin/static")
	if err != nil {
		panic(err) // the directory is embedded at build time
	}
	a := &Admin{
		store:    st,
		log:      log,
		version:  version,
		started:  time.Now().UTC().Truncate(time.Second),
		now:      time.Now,
		params:   DefaultParams,
		limit:    newLimiter(),
		static:   static,
		hashes:   map[string]string{},
		tmdbBase: tmdb.DefaultBase,
	}
	err = fs.WalkDir(static, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(static, name)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		a.hashes[name] = hex.EncodeToString(sum[:5])
		return nil
	})
	if err != nil {
		panic(err)
	}
	a.pages = a.parsePages()
	return a
}

// Handler serves the admin's pages under /admin/ and its API under
// APIPrefix.
func (a *Admin) Handler() http.Handler {
	mux := routes.NewMux()
	mux.Handle(APIPrefix, a.apiMux())
	mux.HandleFunc("GET /admin/static/{name...}", a.serveStatic)
	mux.HandleFunc("GET /admin/sign-in", a.signInPage)
	mux.HandleFunc("POST /admin/sign-in", a.signIn)
	mux.Handle("POST /admin/sign-out", a.page(a.signOut))
	mux.Handle("GET /admin/password", a.page(a.passwordPage))
	mux.Handle("POST /admin/password", a.page(a.changePassword))
	mux.Handle("GET /admin/devices", a.page(a.devicesPage))
	mux.Handle("GET /admin/devices/waiting", a.page(a.devicesWaiting))
	mux.Handle("POST /admin/devices/pair", a.page(a.deviceAction("pair")))
	mux.Handle("POST /admin/devices/approve", a.page(a.deviceAction("approve")))
	mux.Handle("POST /admin/devices/{mac}/{action}", a.page(a.deviceAction("")))
	mux.Handle("GET /admin/accounts", a.page(a.accountsPage))
	mux.Handle("POST /admin/accounts", a.page(a.accountAction))
	mux.Handle("GET /admin/accounts/{id}", a.page(a.accountPage))
	mux.Handle("POST /admin/accounts/outputs/{which}", a.page(a.outputsForm))
	mux.Handle("POST /admin/accounts/{id}/{action}", a.page(a.accountAction))
	mux.Handle("POST /admin/profiles", a.page(a.profileAction))
	mux.Handle("POST /admin/profiles/{id}/{action}", a.page(a.profileAction))
	mux.Handle("POST /admin/messages", a.page(a.messageAction))
	mux.Handle("POST /admin/messages/{id}/{action}", a.page(a.messageAction))
	mux.Handle("GET /admin/sources", a.page(a.sourcesPage))
	mux.Handle("POST /admin/sources", a.page(a.addSourceForm))
	mux.Handle("GET /admin/sources/{id}", a.page(a.sourcePage))
	mux.Handle("POST /admin/sources/{id}", a.page(a.saveSourceForm))
	mux.Handle("GET /admin/sources/{id}/categories", a.page(a.categoriesPage))
	// A source's categories can run to a thousand, each sent with the form.
	mux.Handle("POST /admin/sources/{id}/categories", a.pageWithin(maxCategoriesFormBytes, a.categoriesForm))
	mux.Handle("POST /admin/sources/{id}/categories/read", a.page(a.categoriesRead))
	mux.Handle("GET /admin/sources/{id}/move", a.page(a.movePage))
	mux.Handle("POST /admin/sources/{id}/move", a.pageWithin(maxMoveFormBytes, a.moveForm))
	mux.Handle("POST /admin/sources/{id}/{action}", a.page(a.sourceAction))
	mux.Handle("GET /admin/guides", a.page(a.guidesPage))
	mux.Handle("POST /admin/guides", a.page(a.addGuideForm))
	mux.Handle("POST /admin/guides/placeholders", a.page(a.placeholdersForm))
	mux.Handle("GET /admin/guides/channels", a.page(a.guideChannelsPage))
	mux.Handle("GET /admin/guides/channels/{id}", a.page(a.guideChoicePage))
	mux.Handle("POST /admin/guides/channels/{id}", a.page(a.chooseGuideForm))
	mux.Handle("GET /admin/guides/{id}", a.page(a.guidePage))
	mux.Handle("POST /admin/guides/{id}", a.page(a.saveGuideForm))
	mux.Handle("POST /admin/guides/{id}/{action}", a.page(a.guideAction))
	mux.Handle("POST /admin/guides/sources/{id}/{action}", a.page(a.sourceGuideAction))
	mux.Handle("GET /admin/lineup", a.page(a.lineupRoute(a.lineupPage)))
	mux.Handle("GET /admin/lineup/channels/{id}/edit", a.page(a.lineupRoute(a.entryPanel)))
	mux.Handle("POST /admin/lineup/channels/{id}", a.page(a.lineupRoute(a.saveEntry)))
	mux.Handle("POST /admin/lineup/channels/{id}/backups", a.page(a.lineupRoute(a.backupsAction)))
	mux.Handle("POST /admin/lineup/channels/{id}/health", a.page(a.lineupRoute(a.entryHealthForm)))
	mux.Handle("POST /admin/lineup/channels", a.page(a.lineupRoute(a.changeSome)))
	mux.Handle("POST /admin/lineup/groups", a.page(a.lineupRoute(a.createGroupForm)))
	mux.Handle("POST /admin/lineup/groups/{id}/{action}", a.page(a.lineupRoute(a.groupAction)))
	mux.Handle("POST /admin/lineup/groups/replace", a.page(a.lineupRoute(a.replaceGroupsForm)))
	mux.Handle("POST /admin/lineup/categories", a.page(a.lineupRoute(a.moveCategoryForm)))
	// Every source's categories, to choose those a group takes (D136).
	mux.Handle("GET /admin/lineup/groups/{id}/categories", a.page(a.lineupRoute(a.groupCategoriesPage)))
	mux.Handle("POST /admin/lineup/groups/{id}/categories", a.pageWithin(maxCategoriesFormBytes, a.lineupRoute(a.groupCategoriesForm)))
	mux.Handle("POST /admin/lineups/{action}", a.page(a.lineupRoute(a.lineupsAction)))
	mux.Handle("GET /admin/lineups/export", a.page(a.exportLineupsForm))
	mux.Handle("POST /admin/lineups/import", a.pageWithin(maxUploadBytes, a.lineupRoute(a.importLineupsForm)))
	mux.Handle("GET /admin/lineup/rules", a.page(a.lineupRoute(a.rulesPage)))
	mux.Handle("POST /admin/lineup/rules", a.page(a.lineupRoute(a.rulesAction)))
	mux.Handle("POST /admin/lineup/rules/{id}", a.page(a.lineupRoute(a.rulesAction)))
	mux.Handle("POST /admin/lineup/rules/{id}/{action}", a.page(a.lineupRoute(a.rulesAction)))
	for _, k := range []struct{ path, kind string }{{"movies", store.KindMovie}, {"series", store.KindSeries}} {
		mux.Handle("GET /admin/"+k.path, a.page(a.libraryPage(k.kind)))
		mux.Handle("POST /admin/"+k.path+"/shelves/{id}/{action}", a.page(a.shelfAction(k.kind)))
		mux.Handle("POST /admin/"+k.path+"/shelves/replace", a.page(a.replaceShelvesForm(k.kind)))
		mux.Handle("POST /admin/"+k.path+"/order/reset", a.page(a.shelfOrderReset(k.kind)))
		mux.Handle("POST /admin/"+k.path+"/titles", a.page(a.titlesAction(k.kind)))
	}
	mux.Handle("GET /admin/health", a.page(a.healthPage))
	mux.Handle("POST /admin/health", a.page(a.healthForm))
	mux.Handle("POST /admin/streams/{id}/stop", a.page(a.stopStream))
	mux.Handle("GET /admin/capture", a.page(a.capturePage))
	mux.Handle("POST /admin/capture/clear", a.page(a.clearCapture))
	mux.Handle("GET /admin/settings", a.page(a.settingsPage))
	mux.Handle("POST /admin/settings/repair", a.page(a.repairForm))
	mux.Handle("POST /admin/settings/repair-hls", a.page(a.repairHLSForm))
	mux.Handle("POST /admin/settings/transcode", a.page(a.transcodeForm))
	mux.Handle("POST /admin/settings/convert", a.page(a.convertForm))
	mux.Handle("POST /admin/settings/server-transcode", a.page(a.serverTranscodeForm))
	mux.Handle("POST /admin/settings/pin", a.page(a.pinForm))
	mux.Handle("POST /admin/settings/expiry", a.page(a.expiryForm))
	mux.Handle("POST /admin/settings/themes", a.page(a.themeForm))
	mux.Handle("POST /admin/settings/themes/choose", a.page(a.themeChoiceForm))
	mux.Handle("POST /admin/settings/themes/{id}/remove", a.page(a.themeRemoveForm))
	mux.Handle("GET /admin/themes/{file}", a.page(a.themeFile))
	mux.Handle("POST /admin/settings/adult", a.page(a.adultForm))
	mux.Handle("POST /admin/settings/radio", a.page(a.radioForm))
	mux.Handle("POST /admin/settings/timeshift", a.page(a.timeshiftForm))
	mux.Handle("POST /admin/settings/shared", a.page(a.sharedForm))
	mux.Handle("POST /admin/settings/tmdb", a.page(a.tmdbForm))
	mux.Handle("POST /admin/settings/tmdb-pictures", a.page(a.tmdbPicturesForm))
	mux.Handle("POST /admin/settings/recordings", a.page(a.recordingsForm))
	mux.Handle("POST /admin/settings/recordings/end", a.page(a.recordingEndForm))
	mux.Handle("POST /admin/recordings/{id}/{action}", a.page(a.recordingAction))
	mux.Handle("GET /admin/support-bundle", a.page(a.supportBundle))
	mux.Handle("GET /admin/{$}", a.page(a.home))
	mux.HandleFunc("/admin/", a.notFound)
	// Browsers say where a request comes from; a form or script on another
	// site cannot change anything here, whatever its cookies.
	return routes.With(securityHeaders(http.NewCrossOriginProtection().Handler(mux)), mux)
}

// securityHeaders keeps the admin's pages from being framed, sniffed or
// running scripts and styles from anywhere but the admin itself.
func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; object-src 'none'; "+
			"base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("X-Frame-Options", "DENY")
		hd.Set("Referrer-Policy", "same-origin")
		h.ServeHTTP(w, r)
	})
}

// view is what a page template gets.
type view struct {
	Title   string
	Section string              // the part of the admin the page belongs to
	Session *store.AdminSession // nil when signed out
	Version string
	Error   string // what went wrong with the last action
	Notice  string // what the last action did
	Data    any    // the page's own
	Pending int    // devices waiting for approval, for the rail
	Uptime  string // how long the server has run, for the rail
}

// pageNames are the pages in admin/templates, each of which defines
// "content" for the layout.
var pageNames = []string{"sign-in", "home", "password", "message", "devices", "accounts", "account", "lineup", "rules", "library", "sources", "guides", "guide-channels", "move", "categories", "group-categories", "capture", "health", "settings"}

// pageParts are the parts in admin/templates that pages share, by page.
var pageParts = map[string][]string{"lineup": {"lineup-nav", "replace-form"}, "rules": {"lineup-nav"}, "library": {"replace-form"},
	"accounts": {"account-parts"}, "account": {"account-parts"}}

func (a *Admin) parsePages() map[string]*template.Template {
	funcs := template.FuncMap{"asset": a.assetPath, "csrfHeaders": csrfHeaders, "ago": ago, "dict": dict, "count": count, "plural": plural, "initial": initial, "size": sizeText,
		"sub": func(a, b int) int { return a - b }, "abs": func(n int) int { return max(n, -n) }}
	pages := map[string]*template.Template{}
	for _, name := range pageNames {
		files := []string{"admin/templates/layout.html", "admin/templates/" + name + ".html"}
		for _, part := range pageParts[name] {
			files = append(files, "admin/templates/"+part+".html")
		}
		pages[name] = template.Must(template.New(name).Funcs(funcs).ParseFS(web.Admin, files...))
	}
	return pages
}

// assetPath returns the address of a static file, which carries a hash of
// its content so that browsers can keep it for as long as it stays the same.
func (a *Admin) assetPath(name string) string {
	return "/admin/static/" + name + "?v=" + a.hashes[name]
}

// render writes the page, which is never cached.
func (a *Admin) render(w http.ResponseWriter, r *http.Request, status int, name string, v view) {
	a.fillView(r, &v)
	var buf bytes.Buffer
	if err := a.pages[name].ExecuteTemplate(&buf, "layout", v); err != nil {
		a.log.Error("cannot render an admin page", "page", name, "error", err)
		http.Error(w, "Something went wrong; the server's log says what.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// fillView adds what every page shows: the version and, when signed in,
// how many devices wait for approval and how long the server has run.
func (a *Admin) fillView(r *http.Request, v *view) {
	v.Version = a.version
	if v.Session != nil {
		v.Uptime = uptime(a.started, a.now())
		n, err := a.store.PendingDevices(r.Context())
		if err != nil {
			a.log.Warn("cannot count the devices waiting for approval", "error", err)
		}
		v.Pending = n
	}
}

// fragment writes the template name from page's set, without the layout,
// for htmx to put into a page.
func (a *Admin) fragment(w http.ResponseWriter, r *http.Request, status int, page, name string, v view) {
	a.fillView(r, &v)
	var buf bytes.Buffer
	if err := a.pages[page].ExecuteTemplate(&buf, name, v); err != nil {
		a.log.Error("cannot render part of an admin page", "page", page, "part", name, "error", err)
		http.Error(w, "Something went wrong; the server's log says what.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// message renders a page that only says something.
func (a *Admin) message(w http.ResponseWriter, r *http.Request, status int, title, text string, s *store.AdminSession) {
	a.render(w, r, status, "message", view{Title: title, Session: s, Data: text})
}

// serveStatic serves the admin's styles and images.
func (a *Admin) serveStatic(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	hash, ok := a.hashes[name]
	if !ok || name != path.Clean(name) || strings.HasPrefix(name, ".") {
		http.NotFound(w, r)
		return
	}
	if r.URL.Query().Get("v") == hash {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		// Files named in the styles, such as the fonts, carry no hash:
		// the browser asks again each time, and the tag spares it the
		// download when the file has not changed.
		w.Header().Set("Cache-Control", "no-cache")
	}
	w.Header().Set("ETag", `"`+hash+`"`)
	http.ServeFileFS(w, r, a.static, name)
}

// dict makes a map from keys and values, for passing several values to a
// template.
func dict(kv ...any) (map[string]any, error) {
	if len(kv)%2 != 0 {
		return nil, errors.New("dict: a key without a value")
	}
	m := make(map[string]any, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		k, ok := kv[i].(string)
		if !ok {
			return nil, errors.New("dict: a key that is not a string")
		}
		m[k] = kv[i+1]
	}
	return m, nil
}

// csrfHeaders returns the hx-headers attribute that makes htmx send a
// session's CSRF token with every request.
func csrfHeaders(s *store.AdminSession) (string, error) {
	b, err := json.Marshal(map[string]string{csrfHeader: s.CSRFToken})
	return string(b), err
}

// notFound answers for a page that does not exist.
func (a *Admin) notFound(w http.ResponseWriter, r *http.Request) {
	var s *store.AdminSession
	if sess, ok := a.authenticate(r); ok {
		s = &sess
	}
	a.message(w, r, http.StatusNotFound, "Not found", "There is no such page in the admin.", s)
}
