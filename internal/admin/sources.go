// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"overscan.gureet.ca/internal/catalog"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

// The sources (D46): the administrator adds, changes, tests, reads, turns on
// or off and deletes them. A saved address or login is never shown again,
// only the address's host. An Xtream source is a server read through its API
// with a login (D50). A Jellyfin source is a server read for its films and
// series with an API key, and as a user if one is named (D66); the user is
// no secret, so its form shows the saved one. A Plex source is a server
// read for its films and series with a token (D72). A folder is a folder of
// films and series on this server (D67).

// Sources reads sources for the admin; the catalog's refresher does.
type Sources interface {
	// Test reads a source without storing anything.
	Test(ctx context.Context, settings source.Settings) (catalog.Tested, error)
	// RefreshNow reads a source and stores its channels.
	RefreshNow(ctx context.Context, id int64) (store.CatalogChanges, error)
	// Reload makes the reader take the sources' new settings.
	Reload()
}

// UseSources gives the admin what reads the sources. Without it, the admin
// can change sources but cannot test or read them.
func (a *Admin) UseSources(s Sources) { a.sources = s }

// sourceError turns the store's and the reader's errors about a source into
// mistakes that the request can put right.
func sourceError(err error) error {
	var bad *source.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &bad):
		return badEdit(http.StatusBadRequest, "bad_"+bad.Field, "%s", bad.Text)
	case errors.Is(err, store.ErrSourceExists):
		return badEdit(http.StatusConflict, "name_taken", "Another source has that name.")
	case errors.Is(err, store.ErrNotFound):
		return badEdit(http.StatusNotFound, "not_found", "There is no such source.")
	case errors.Is(err, store.ErrNoAddress):
		return badEdit(http.StatusConflict, "no_address", "The source has no address saved; enter one.")
	case errors.Is(err, store.ErrUnreadable):
		return badEdit(http.StatusConflict, "address_locked",
			"The saved address was encrypted with another key file; enter it again, or put back the key file that saved it.")
	}
	return err
}

// readError says why a source could not be read. The reader's errors never
// hold the address.
func readError(err error) error {
	if err := sourceError(err); err == nil || isEditError(err) {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return badEdit(http.StatusBadGateway, "source_failed", "The source could not be read: %s.", strings.TrimSuffix(err.Error(), "."))
}

func isEditError(err error) bool {
	var bad *editError
	return errors.As(err, &bad)
}

var errNoReader = badEdit(http.StatusServiceUnavailable, "no_reader", "This server cannot read sources from the admin.")

func (a *Admin) reloadSources() {
	if a.sources != nil {
		a.sources.Reload()
	}
}

// addSource adds a source. With onlyTicked, it brings only the categories
// that the administrator ticks once its first read lists them (D135), and
// that read keeps the categories alone.
func (a *Admin) addSource(ctx context.Context, settings source.Settings, onlyTicked bool) (int64, error) {
	id, err := a.store.AddSource(ctx, settings)
	if err != nil {
		return 0, sourceError(err)
	}
	if onlyTicked {
		// Before the reader hears of the source, so that its first read
		// keeps to it.
		for _, k := range sourceKinds(store.Source{Type: settings.WithDefaults().Type}) {
			if _, err := a.store.SetCategoryChoice(ctx, id, k.Kind, true, false, a.now()); err != nil {
				a.reloadSources()
				return id, err
			}
		}
	}
	a.log.Info("source added in the admin", "source", settings.Name, "only_ticked_categories", onlyTicked)
	a.reloadSources()
	return id, nil
}

// updateSource saves a source's settings; an empty URL keeps the saved
// address.
func (a *Admin) updateSource(ctx context.Context, id int64, settings source.Settings) error {
	if err := a.store.UpdateSource(ctx, id, settings); err != nil {
		return sourceError(err)
	}
	a.log.Info("source changed in the admin", "source", settings.Name,
		"new_address", settings.URL != "" || settings.Username != "" || settings.Password != "")
	a.reloadSources()
	return nil
}

func (a *Admin) setSourceEnabled(ctx context.Context, id int64, on bool) error {
	if err := a.store.SetSourceEnabled(ctx, id, on); err != nil {
		return sourceError(err)
	}
	a.log.Info("source turned on or off in the admin", "source_id", id, "enabled", on)
	a.reloadSources()
	return nil
}

func (a *Admin) deleteSource(ctx context.Context, id int64) (string, error) {
	src, err := a.store.Source(ctx, id)
	if err == nil {
		err = a.store.DeleteSource(ctx, id)
	}
	if err != nil {
		return "", sourceError(err)
	}
	a.log.Info("source deleted in the admin", "source", src.Name)
	a.reloadSources()
	return src.Name, nil
}

func (a *Admin) refreshSource(ctx context.Context, id int64) (store.CatalogChanges, error) {
	if a.sources == nil {
		return store.CatalogChanges{}, errNoReader
	}
	ch, err := a.sources.RefreshNow(ctx, id)
	return ch, readError(err)
}

// testSource reads a source without saving anything. For a saved source
// (id not 0) that keeps its type, an empty address, username or password
// means the saved one.
func (a *Admin) testSource(ctx context.Context, id int64, settings source.Settings) (catalog.Tested, error) {
	if a.sources == nil {
		return catalog.Tested{}, errNoReader
	}
	settings = settings.WithDefaults()
	if id != 0 && settings.Type == source.TypeStalker {
		if err := a.fillPortal(ctx, id, &settings); err != nil {
			return catalog.Tested{}, err
		}
	}
	login := settings.Type == source.TypeXtream || settings.Type == source.TypeJellyfin || settings.Type == source.TypePlex
	if id != 0 && settings.Type != source.TypeStalker && (settings.URL == "" || login && settings.Password == "" ||
		settings.Type == source.TypeXtream && settings.Username == "") {
		src, err := a.store.Source(ctx, id)
		if err != nil {
			return catalog.Tested{}, sourceError(err)
		}
		if src.Type == settings.Type {
			saved, err := a.store.SourceSettings(ctx, id)
			if err != nil {
				return catalog.Tested{}, sourceError(err)
			}
			fill := []struct {
				to   *string
				from string
			}{{&settings.URL, saved.URL}, {&settings.Password, saved.Password}}
			if settings.Type == source.TypeXtream {
				// A Jellyfin source's user is as given: "" is every library.
				fill = append(fill, struct {
					to   *string
					from string
				}{&settings.Username, saved.Username})
			}
			for _, f := range fill {
				if *f.to == "" {
					*f.to = f.from
				}
			}
		}
	}
	settings = settings.WithDefaults()
	if err := settings.Check(); err != nil {
		return catalog.Tested{}, sourceError(err)
	}
	t, err := a.sources.Test(ctx, settings)
	return t, readError(err)
}

// fillPortal fills in what a test of a saved Stalker portal leaves out, as
// saving does: the address and login each from the saved ones, and the
// box's details too unless a MAC address is given, when the box is as
// given (store.Store.UpdateSource).
func (a *Admin) fillPortal(ctx context.Context, id int64, settings *source.Settings) error {
	src, err := a.store.Source(ctx, id)
	if err != nil {
		return sourceError(err)
	}
	if src.Type != source.TypeStalker {
		return nil
	}
	saved, err := a.store.SourceSettings(ctx, id)
	if err != nil {
		if settings.URL == "" || settings.Device.MAC == "" {
			return sourceError(err)
		}
		return nil
	}
	fill := func(to *string, from string) {
		if *to == "" {
			*to = from
		}
	}
	fill(&settings.URL, saved.URL)
	fill(&settings.Username, saved.Username)
	fill(&settings.Password, saved.Password)
	if d := &settings.Device; d.MAC == "" {
		fill(&d.MAC, saved.Device.MAC)
		fill(&d.Serial, saved.Device.Serial)
		fill(&d.DeviceID, saved.Device.DeviceID)
		fill(&d.DeviceID2, saved.Device.DeviceID2)
		fill(&d.Signature, saved.Device.Signature)
		fill(&d.Model, saved.Device.Model)
	}
	return nil
}

// The API.

// apiSource is a source as the API describes it, without its address.
type apiSource struct {
	ID                  int64   `json:"id"`
	Name                string  `json:"name"`
	Type                string  `json:"type"`
	Host                string  `json:"host"`
	Address             string  `json:"address"` // saved, missing or locked
	Enabled             bool    `json:"enabled"`
	MaxConnections      int     `json:"max_connections"`
	RefreshSeconds      int64   `json:"refresh_seconds"`
	UserAgent           string  `json:"user_agent"`
	Numbers             string  `json:"numbers"`
	CatchupZone         string  `json:"catchup_zone"`
	NewCategoriesHidden bool    `json:"new_categories_hidden"`
	GuideLogos          string  `json:"guide_logos"`
	PublicOnly          bool    `json:"public_only"`
	Channels            int     `json:"channels"`
	RefreshedAt         *string `json:"refreshed_at"`
	RefreshError        string  `json:"refresh_error"`
	// StreamLimit is what Overscan keeps to: MaxConnections, or the
	// account's limit where that is lower; 0 for no limit.
	StreamLimit int         `json:"stream_limit"`
	Account     *apiAccount `json:"account"`
	Guide       apiGuide    `json:"guide"`
	// Films and Series are what the reads of an Xtream, a Jellyfin or a Plex
	// source's films and series did (D63); nil for M3U sources.
	Films  *apiLibraryRead `json:"films"`
	Series *apiLibraryRead `json:"series"`
	// Categories says what the source brings of each kind of category that
	// it has, by kind: live, movies and series (D135); none for a folder.
	Categories map[string]apiBrings `json:"categories"`
}

// apiBrings is what a source brings of a kind of category.
type apiBrings struct {
	OnlyTicked bool `json:"only_ticked"`
	Listed     int  `json:"listed"` // the categories that its reads listed
	Ticked     int  `json:"ticked"` // of those, the ticked ones
}

// apiLibraryRead is what the reads of a source's films, or series, did.
type apiLibraryRead struct {
	ReadAt  *string `json:"read_at"`
	TriedAt *string `json:"tried_at"`
	Error   string  `json:"error"`
	Titles  int     `json:"titles"`
}

func libraryReadBody(r store.LibraryRead) *apiLibraryRead {
	out := &apiLibraryRead{Error: r.Error, Titles: r.Titles}
	if !r.ReadAt.IsZero() {
		t := r.ReadAt.UTC().Format("2006-01-02T15:04:05Z")
		out.ReadAt = &t
	}
	if !r.TriedAt.IsZero() {
		t := r.TriedAt.UTC().Format("2006-01-02T15:04:05Z")
		out.TriedAt = &t
	}
	return out
}

// apiGuide is what the last read of a source's guide found (D51).
type apiGuide struct {
	Listed     bool    `json:"listed"` // the source's playlist names a guide
	ReadAt     *string `json:"read_at"`
	Error      string  `json:"error"`
	Channels   int     `json:"channels"`
	Programmes int     `json:"programmes"`
}

// apiAccount is what an Xtream server says about a source's account.
type apiAccount struct {
	Status         string  `json:"status"`
	ExpiresAt      *string `json:"expires_at"`
	MaxConnections int     `json:"max_connections"`
	TimeZone       string  `json:"time_zone"`
}

// accountBody returns what the API says about an account, nil if nothing is
// known.
func accountBody(a store.SourceAccount) *apiAccount {
	if a.Status == "" {
		return nil
	}
	out := &apiAccount{Status: a.Status, MaxConnections: a.MaxConnections, TimeZone: a.TimeZone}
	if !a.ExpiresAt.IsZero() {
		t := a.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z")
		out.ExpiresAt = &t
	}
	return out
}

var addressNames = map[store.AddressState]string{
	store.AddressSaved: "saved", store.AddressMissing: "missing", store.AddressLocked: "locked",
}

func sourceBody(s store.Source, reads libraryReads, brings map[int64][]bringSummary) apiSource {
	out := apiSource{ID: s.ID, Name: s.Name, Type: s.Type, Host: s.Host, Address: addressNames[s.Address], Enabled: s.Enabled,
		MaxConnections: s.MaxConnections, RefreshSeconds: int64(s.Refresh / time.Second), UserAgent: s.UserAgent,
		Numbers: s.Numbers, CatchupZone: s.CatchupZone, NewCategoriesHidden: s.NewCategoriesHidden, GuideLogos: s.GuideLogos,
		PublicOnly:   s.PublicOnly,
		Channels:     s.Channels,
		RefreshError: s.RefreshError,
		StreamLimit:  s.StreamLimit(), Account: accountBody(s.Account),
		Guide: apiGuide{Listed: s.Guide.Listed, Error: s.Guide.Error, Channels: s.Guide.Channels, Programmes: s.Guide.Programmes}}
	if !s.Guide.ReadAt.IsZero() {
		t := s.Guide.ReadAt.UTC().Format("2006-01-02T15:04:05Z")
		out.Guide.ReadAt = &t
	}
	if !s.RefreshedAt.IsZero() {
		t := s.RefreshedAt.UTC().Format("2006-01-02T15:04:05Z")
		out.RefreshedAt = &t
	}
	if s.Settings().HasLibrary() {
		out.Films = libraryReadBody(reads[s.ID][store.KindMovie])
		out.Series = libraryReadBody(reads[s.ID][store.KindSeries])
	}
	out.Categories = map[string]apiBrings{}
	for _, b := range brings[s.ID] {
		out.Categories[b.Kind.Path] = apiBrings{OnlyTicked: b.OnlyTicked, Listed: b.Listed, Ticked: b.Ticked}
	}
	return out
}

// libraryReads is what the reads of the sources' films and series did, by
// source and kind.
type libraryReads map[int64]map[string]store.LibraryRead

func (a *Admin) libraryReads(ctx context.Context) (libraryReads, error) {
	list, err := a.store.LibraryReads(ctx)
	if err != nil {
		return nil, err
	}
	out := libraryReads{}
	for _, r := range list {
		if out[r.SourceID] == nil {
			out[r.SourceID] = map[string]store.LibraryRead{}
		}
		out[r.SourceID][r.Kind] = r
	}
	return out, nil
}

func (a *Admin) apiSources(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	srcs, err := a.store.Sources(r.Context())
	if a.editAnswer(w, err) {
		return
	}
	reads, err := a.libraryReads(r.Context())
	if a.editAnswer(w, err) {
		return
	}
	brings, err := a.sourceBrings(r.Context(), srcs)
	if a.editAnswer(w, err) {
		return
	}
	out := make([]apiSource, len(srcs))
	for i, s := range srcs {
		out[i] = sourceBody(s, reads, brings)
	}
	writeJSON(w, http.StatusOK, struct {
		Sources []apiSource `json:"sources"`
	}{out})
}

// writeSource answers with a source.
func (a *Admin) writeSource(w http.ResponseWriter, r *http.Request, status int, id int64) {
	src, err := a.store.Source(r.Context(), id)
	if a.editAnswer(w, sourceError(err)) {
		return
	}
	reads, err := a.libraryReads(r.Context())
	if a.editAnswer(w, err) {
		return
	}
	brings, err := a.sourceBrings(r.Context(), []store.Source{src})
	if a.editAnswer(w, err) {
		return
	}
	writeJSON(w, status, sourceBody(src, reads, brings))
}

// apiSourceChange is the body of a request that adds or changes a source.
// What it leaves out stays as it is, or takes its default for a new source.
type apiSourceChange struct {
	Name                *string `json:"name"`
	Type                *string `json:"type"`
	URL                 *string `json:"url"`
	Username            *string `json:"username"`
	Password            *string `json:"password"`
	UserAgent           *string `json:"user_agent"`
	MaxConnections      *int    `json:"max_connections"`
	RefreshSeconds      *int64  `json:"refresh_seconds"`
	Numbers             *string `json:"numbers"`
	CatchupZone         *string `json:"catchup_zone"`
	Enabled             *bool   `json:"enabled"`
	NewCategoriesHidden *bool   `json:"new_categories_hidden"`
	GuideLogos          *string `json:"guide_logos"`
	PublicOnly          *bool   `json:"public_only"`
	// Bring is what a new source brings: "every" category, as before, or
	// only those "ticked" once its first read lists them (D135). A source
	// that exists changes it at sources/{id}/categories/{kind}.
	Bring *string `json:"bring"`
	// A Stalker portal's box (D76).
	MAC       *string `json:"mac"`
	Serial    *string `json:"serial"`
	DeviceID  *string `json:"device_id"`
	DeviceID2 *string `json:"device_id2"`
	Signature *string `json:"signature"`
	Model     *string `json:"model"`
}

// changesSettings reports whether the body changes any of the source's
// settings, besides whether it is on and how new categories start.
func (c apiSourceChange) changesSettings() bool {
	return c.Name != nil || c.Type != nil || c.URL != nil || c.Username != nil || c.Password != nil ||
		c.UserAgent != nil || c.MaxConnections != nil || c.RefreshSeconds != nil || c.Numbers != nil || c.CatchupZone != nil ||
		c.PublicOnly != nil || c.MAC != nil || c.Serial != nil || c.DeviceID != nil || c.DeviceID2 != nil || c.Signature != nil || c.Model != nil
}

// newSettings returns the settings of a new source that the body describes:
// what it leaves out takes its default.
func (c apiSourceChange) newSettings() source.Settings {
	s := c.apply(source.Settings{})
	if c.MaxConnections == nil {
		s.MaxConnections = source.DefaultLimit(s.WithDefaults().Type)
	}
	return s
}

// keepJellyfinUser puts back the saved user of a Jellyfin source that stays
// one when the body leaves the user out, since "" means every library. A
// saved user that cannot be read stays out.
func (a *Admin) keepJellyfinUser(ctx context.Context, id int64, src store.Source, c apiSourceChange, s *source.Settings) {
	if c.Username != nil || src.Type != source.TypeJellyfin || s.Type != source.TypeJellyfin {
		return
	}
	if saved, err := a.store.SourceSettings(ctx, id); err == nil {
		s.Username = saved.Username
	}
}

// apply puts the body's settings over s.
func (c apiSourceChange) apply(s source.Settings) source.Settings {
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = *v
		}
	}
	set(&s.Name, c.Name)
	set(&s.Type, c.Type)
	set(&s.URL, c.URL)
	set(&s.Username, c.Username)
	set(&s.Password, c.Password)
	set(&s.UserAgent, c.UserAgent)
	set(&s.Numbers, c.Numbers)
	set(&s.CatchupZone, c.CatchupZone)
	set(&s.Device.MAC, c.MAC)
	set(&s.Device.Serial, c.Serial)
	set(&s.Device.DeviceID, c.DeviceID)
	set(&s.Device.DeviceID2, c.DeviceID2)
	set(&s.Device.Signature, c.Signature)
	set(&s.Device.Model, c.Model)
	if c.MaxConnections != nil {
		s.MaxConnections = *c.MaxConnections
	}
	if c.RefreshSeconds != nil {
		s.Refresh = time.Duration(*c.RefreshSeconds) * time.Second
	}
	if c.PublicOnly != nil {
		s.PublicOnly = *c.PublicOnly
	}
	return s
}

func (a *Admin) apiAddSource(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	var body apiSourceChange
	if !readJSON(w, r, &body) {
		return
	}
	if body.URL == nil || *body.URL == "" {
		apiError(w, http.StatusBadRequest, "bad_url", "A new source needs its address, url.")
		return
	}
	settings := body.newSettings()
	onlyTicked := false
	if body.Bring != nil {
		switch *body.Bring {
		case "every":
		case "ticked":
			onlyTicked = true
		default:
			apiError(w, http.StatusBadRequest, "bad_bring", `What a source brings is "every" category or only those "ticked".`)
			return
		}
	}
	id, err := a.addSource(r.Context(), settings, onlyTicked)
	if a.editAnswer(w, err) {
		return
	}
	ctx := r.Context()
	if body.Enabled != nil && !*body.Enabled {
		err = a.setSourceEnabled(ctx, id, false)
	}
	if err == nil && body.NewCategoriesHidden != nil {
		err = sourceError(a.store.SetNewCategoriesHidden(ctx, id, *body.NewCategoriesHidden))
	}
	if err == nil && body.GuideLogos != nil {
		err = a.setSourceGuideLogos(ctx, id, *body.GuideLogos)
	}
	if a.editAnswer(w, err) {
		return
	}
	a.writeSource(w, r, http.StatusCreated, id)
}

func (a *Admin) apiChangeSource(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	ctx := r.Context()
	id, err := pathID(r)
	if a.editAnswer(w, err) {
		return
	}
	var body apiSourceChange
	if !readJSON(w, r, &body) {
		return
	}
	if body == (apiSourceChange{}) {
		apiError(w, http.StatusBadRequest, "bad_json", "Say what to change.")
		return
	}
	if body.Bring != nil {
		apiError(w, http.StatusBadRequest, "bad_bring", "Change what a source brings with PUT sources/{id}/categories/{kind}.")
		return
	}
	src, err := a.store.Source(ctx, id)
	if a.editAnswer(w, sourceError(err)) {
		return
	}
	if body.changesSettings() {
		settings := body.apply(src.Settings())
		for field, v := range map[string]*string{"url": body.URL, "username": body.Username, "password": body.Password,
			"mac": body.MAC} {
			// A Jellyfin source's user can be none: every library.
			if v != nil && *v == "" && !(field == "username" && settings.Type == source.TypeJellyfin) {
				apiError(w, http.StatusBadRequest, "bad_"+field, "Leave "+field+" out to keep the saved one.")
				return
			}
		}
		a.keepJellyfinUser(ctx, id, src, body, &settings)
		if a.editAnswer(w, a.updateSource(ctx, id, settings)) {
			return
		}
	}
	if body.Enabled != nil && a.editAnswer(w, a.setSourceEnabled(ctx, id, *body.Enabled)) {
		return
	}
	if body.NewCategoriesHidden != nil && a.editAnswer(w, sourceError(a.store.SetNewCategoriesHidden(ctx, id, *body.NewCategoriesHidden))) {
		return
	}
	if body.GuideLogos != nil && a.editAnswer(w, a.setSourceGuideLogos(ctx, id, *body.GuideLogos)) {
		return
	}
	a.writeSource(w, r, http.StatusOK, id)
}

// setSourceGuideLogos sets how a source's guide's icons are used as its
// channels' logos (D141).
func (a *Admin) setSourceGuideLogos(ctx context.Context, id int64, how string) error {
	if !store.ValidGuideLogos(how) {
		return badEdit(http.StatusBadRequest, "bad_guide_logos", `How its guide's logos are used is "none", "fill" or "first".`)
	}
	if err := a.store.SetSourceGuideLogos(ctx, id, how); err != nil {
		return sourceError(err)
	}
	a.log.Info("a source's guide's logos set in the admin", "source_id", id, "guide_logos", how)
	return nil
}

func (a *Admin) apiDeleteSource(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	id, err := pathID(r)
	if a.editAnswer(w, err) {
		return
	}
	if _, err := a.deleteSource(r.Context(), id); a.editAnswer(w, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// apiRead is what reading a source found.
type apiRead struct {
	Channels int `json:"channels"`
	Added    int `json:"added"`
	Missing  int `json:"missing"`
	Removed  int `json:"removed"`
	// LeftOut counts the channels of categories that the source does not
	// bring (D135).
	LeftOut int `json:"left_out"`
}

// readBody is what the API says of a read of a source's channels.
func readBody(ch store.CatalogChanges) *apiRead {
	return &apiRead{Channels: ch.Added + ch.Updated, Added: ch.Added, Missing: ch.Missing, Removed: ch.Purged, LeftOut: ch.LeftOut}
}

func (a *Admin) apiRefreshSource(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	id, err := pathID(r)
	if a.editAnswer(w, err) {
		return
	}
	ch, err := a.refreshSource(r.Context(), id)
	if a.editAnswer(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, readBody(ch))
}

// apiTested is what a test of a source found.
type apiTested struct {
	Channels int         `json:"channels"`
	Skipped  int         `json:"skipped"`
	Account  *apiAccount `json:"account"` // what an Xtream server says about the account
	// Server is what a media server, Jellyfin or Plex, says about itself,
	// and Libraries the names of its libraries of films and series that
	// boxes would see.
	Server    *apiServer `json:"server"`
	Libraries []string   `json:"libraries"`
	// Folder is a folder's name, and Films and Series what it holds.
	Folder *apiFolder `json:"folder"`
}

// apiFolder is what a folder holds.
type apiFolder struct {
	Name   string `json:"name"`
	Films  int    `json:"films"`
	Series int    `json:"series"`
}

// apiServer is what a media server says about itself.
type apiServer struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func testedBody(t catalog.Tested) apiTested {
	out := apiTested{Channels: t.Channels, Skipped: t.Skipped, Libraries: t.Libraries}
	if a := t.Account; a != nil {
		out.Account = accountBody(store.SourceAccount{Status: a.Status, ExpiresAt: a.ExpiresAt,
			MaxConnections: a.MaxConnections, TimeZone: a.TimeZone})
	}
	if s := t.Server; s != nil {
		out.Server = &apiServer{Name: s.Name, Version: s.Version}
	}
	if t.Folder != "" {
		out.Folder = &apiFolder{Name: t.Folder, Films: t.Films, Series: t.Series}
	}
	return out
}

func (a *Admin) apiTestNewSource(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	var body apiSourceChange
	if !readJSON(w, r, &body) {
		return
	}
	if body.URL == nil || *body.URL == "" {
		apiError(w, http.StatusBadRequest, "bad_url", "Say which address to test, url.")
		return
	}
	settings := body.newSettings()
	if settings.Name == "" {
		settings.Name = "test"
	}
	t, err := a.testSource(r.Context(), 0, settings)
	if a.editAnswer(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, testedBody(t))
}

func (a *Admin) apiTestSource(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	id, err := pathID(r)
	if a.editAnswer(w, err) {
		return
	}
	var body apiSourceChange
	if r.ContentLength != 0 && !readJSON(w, r, &body) {
		return
	}
	src, err := a.store.Source(r.Context(), id)
	if a.editAnswer(w, sourceError(err)) {
		return
	}
	settings := body.apply(src.Settings())
	a.keepJellyfinUser(r.Context(), id, src, body, &settings)
	t, err := a.testSource(r.Context(), id, settings)
	if a.editAnswer(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, testedBody(t))
}

// The pages.

// refreshChoices are the intervals that the form offers, besides a source's
// own.
var refreshChoices = []time.Duration{15 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour, 3 * time.Hour,
	6 * time.Hour, 12 * time.Hour, 24 * time.Hour, 48 * time.Hour, 7 * 24 * time.Hour}

// every says how often, as "every 6 hours".
func every(d time.Duration) string {
	switch {
	case d%(24*time.Hour) == 0:
		n := int(d / (24 * time.Hour))
		return fmt.Sprintf("%d %s", n, plural(n, "day"))
	case d%time.Hour == 0:
		n := int(d / time.Hour)
		return fmt.Sprintf("%d %s", n, plural(n, "hour"))
	default:
		n := int(d / time.Minute)
		return fmt.Sprintf("%d %s", n, plural(n, "minute"))
	}
}

// count writes a number with commas between the thousands.
func count(n int) string {
	s := strconv.Itoa(n)
	if n < 0 {
		return "-" + count(-n)
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// sourceForm is what the form to add or change a source shows.
type sourceForm struct {
	ID        int64 // 0 for a new source
	Settings  source.Settings
	SavedType string // the saved source's type, "" for a new source
	Host      string
	Address   store.AddressState
	Tested    *catalog.Tested // what a test found
	TestErr   string          // why a test failed
	// Bring is set when a new source is to bring only the categories
	// ticked once its first read lists them (D135).
	Bring bool
}

// RefreshChoices returns the intervals to offer, with the form's own.
func (f sourceForm) RefreshChoices() []refreshChoice {
	own := f.Settings.Refresh
	durations := slices.Clone(refreshChoices)
	if own > 0 && !slices.Contains(durations, own) {
		durations = append(durations, own)
		slices.Sort(durations)
	}
	out := make([]refreshChoice, len(durations))
	for i, d := range durations {
		out[i] = refreshChoice{D: d, Selected: d == own}
	}
	return out
}

type refreshChoice struct {
	D        time.Duration
	Selected bool
}

// Seconds is the choice's value in the form.
func (c refreshChoice) Seconds() int64 { return int64(c.D / time.Second) }

// Label is how the form names the choice.
func (c refreshChoice) Label() string { return "Every " + every(c.D) }

// AddressLocked reports whether the saved address was saved with another
// key file.
func (f sourceForm) AddressLocked() bool { return f.ID != 0 && f.Address == store.AddressLocked }

// AddressMissing reports whether the source has no address saved.
func (f sourceForm) AddressMissing() bool { return f.ID != 0 && f.Address == store.AddressMissing }

// IsXtream reports whether the form is for an Xtream source.
func (f sourceForm) IsXtream() bool { return f.Settings.Type == source.TypeXtream }

// IsJellyfin reports whether the form is for a Jellyfin source.
func (f sourceForm) IsJellyfin() bool { return f.Settings.Type == source.TypeJellyfin }

// IsPlex reports whether the form is for a Plex source.
func (f sourceForm) IsPlex() bool { return f.Settings.Type == source.TypePlex }

// IsFolder reports whether the form is for a folder.
func (f sourceForm) IsFolder() bool { return f.Settings.Type == source.TypeFolder }

// IsStalker reports whether the form is for a Stalker portal.
func (f sourceForm) IsStalker() bool { return f.Settings.Type == source.TypeStalker }

// IsM3U reports whether the form is for an M3U source, the type it starts
// with.
func (f sourceForm) IsM3U() bool {
	return !f.IsXtream() && !f.IsJellyfin() && !f.IsPlex() && !f.IsFolder() && !f.IsStalker()
}

// ChannelLimit is the limit of streams that the form offers for a source of
// channels, and LibraryLimit the one for a media server or a folder: the
// form's own for its type, and the default for the other.
func (f sourceForm) ChannelLimit() int {
	if !f.Settings.HasChannels() {
		return source.DefaultMaxConnections
	}
	return f.Settings.MaxConnections
}

// LibraryLimit: see ChannelLimit.
func (f sourceForm) LibraryLimit() int {
	if !f.Settings.HasChannels() {
		return f.Settings.MaxConnections
	}
	return source.DefaultLimit(source.TypeJellyfin)
}

// Found says what a test found.
func (f sourceForm) Found() string {
	if f.Tested == nil {
		return ""
	}
	if f.Tested.Folder != "" {
		return fmt.Sprintf("Overscan can read the folder %s: it holds %s %s and %s series. Nothing has been saved yet.",
			f.Tested.Folder, count(f.Tested.Films), plural(f.Tested.Films, "film"), count(f.Tested.Series))
	}
	if s := f.Tested.Server; s != nil {
		text := "The server took the API key. It is Jellyfin " + s.Version
		if f.IsPlex() {
			text = "The server took the token. It is Plex Media Server " + s.Version
		}
		if s.Name != "" {
			text += ", named " + s.Name
		}
		libraries := "its " + count(len(f.Tested.Libraries)) + " libraries"
		if len(f.Tested.Libraries) == 1 {
			libraries = "its library"
		}
		return text + fmt.Sprintf(". Boxes would see the films and series in %s: %s. Nothing has been saved yet.",
			libraries, andList(f.Tested.Libraries))
	}
	text := fmt.Sprintf("The address works: it lists %s %s.", count(f.Tested.Channels), plural(f.Tested.Channels, "channel"))
	if a := f.Tested.Account; a != nil {
		text = "The server accepted the login. " + accountText(a.ExpiresAt, a.MaxConnections) +
			fmt.Sprintf(" It lists %s live %s.", count(f.Tested.Channels), plural(f.Tested.Channels, "channel"))
		if f.IsStalker() {
			text = "The portal took the box. " + accountText(a.ExpiresAt, 0) +
				fmt.Sprintf(" It lists %s live %s.", count(f.Tested.Channels), plural(f.Tested.Channels, "channel"))
		}
	}
	if n := f.Tested.Skipped; n == 1 {
		text += " It would leave out 1 other entry: a movie, a series or an unusable address."
	} else if n > 1 {
		text += fmt.Sprintf(" It would leave out %s other entries: movies, series or unusable addresses.", count(n))
	}
	return text + " Nothing has been saved yet."
}

// accountText says when an account runs out and how many streams it allows,
// as far as its server says.
func accountText(expires time.Time, limit int) string {
	text := "The account does not run out"
	if !expires.IsZero() {
		text = "The account runs until " + expires.UTC().Format("2 January 2006")
	}
	if limit > 0 {
		return text + fmt.Sprintf(", and allows %d %s at once.", limit, plural(limit, "stream"))
	}
	return text + "."
}

// andList joins names as in "Films, Shows and Home".
func andList(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// sourceRow is a source on the list.
type sourceRow struct {
	store.Source
	Now time.Time
	// Films and Series are what the reads of an Xtream, a Jellyfin or a Plex
	// source's films and series did; zero if none has ended.
	Films, Series store.LibraryRead
	// Brings is what it brings of each kind of category that it has
	// (D135); none for a folder.
	Brings []bringSummary
}

// FilmsLine says what the reads of the source's films did.
func (r sourceRow) FilmsLine() string { return r.libraryLine(r.Films, "film", "Movies") }

// SeriesLine says what the reads of the source's series did.
func (r sourceRow) SeriesLine() string { return r.libraryLine(r.Series, "series", "Series") }

// libraryLine says what the reads of a source's films or series did: how
// many it listed and when, or why the last read failed. A box's screen
// for them starts the first read (D63).
func (r sourceRow) libraryLine(read store.LibraryRead, noun, screen string) string {
	if read.ReadAt.IsZero() && read.Error == "" {
		return "Not read yet: Overscan reads them when a box first opens " + screen + "."
	}
	text := ""
	switch {
	case !read.ReadAt.IsZero() && r.IsStalker():
		// A portal's are read a page at a time as boxes browse them (D77).
		text = fmt.Sprintf("%s %s read so far, a page at a time as boxes browse them; its categories read %s",
			count(read.Titles), pluralNoun(read.Titles, noun), ago(read.ReadAt, r.Now))
	case !read.ReadAt.IsZero():
		text = fmt.Sprintf("%s %s, read %s", count(read.Titles), pluralNoun(read.Titles, noun), ago(read.ReadAt, r.Now))
	}
	if read.Error != "" {
		if text != "" {
			text += ". "
		}
		text += "The last read failed, " + ago(read.TriedAt, r.Now) + ": " + read.Error
	}
	return text
}

// pluralNoun returns noun for one and its plural otherwise; series is the
// same for both.
func pluralNoun(n int, noun string) string {
	if noun == "series" {
		return noun
	}
	return plural(n, noun)
}

// Every says how often the source is read.
func (r sourceRow) Every() string { return every(r.Refresh) }

// IsXtream reports whether the source is an Xtream server.
func (r sourceRow) IsXtream() bool { return r.Type == source.TypeXtream }

// IsJellyfin reports whether the source is a Jellyfin server.
func (r sourceRow) IsJellyfin() bool { return r.Type == source.TypeJellyfin }

// IsPlex reports whether the source is a Plex server.
func (r sourceRow) IsPlex() bool { return r.Type == source.TypePlex }

// IsStalker reports whether the source is a Stalker portal.
func (r sourceRow) IsStalker() bool { return r.Type == source.TypeStalker }

// Origin names where the source's channels come from, as its numbers line
// says it: the server, the portal or the playlist.
func (r sourceRow) Origin() string {
	switch r.Type {
	case source.TypeXtream:
		return "server"
	case source.TypeStalker:
		return "portal"
	}
	return "playlist"
}

// LibraryOnly reports whether the source has films and series and no
// channels, so that its read only checks it: a media server or a folder.
func (r sourceRow) LibraryOnly() bool { return !r.Settings().HasChannels() }

// HasLibrary reports whether the source has films and series.
func (r sourceRow) HasLibrary() bool { return r.Settings().HasLibrary() }

// Kind names the kind of source, as its badge does.
func (r sourceRow) Kind() string {
	switch r.Type {
	case source.TypeXtream:
		return "Xtream server"
	case source.TypeJellyfin:
		return "Jellyfin server"
	case source.TypePlex:
		return "Plex server"
	case source.TypeFolder:
		return "Folder"
	case source.TypeStalker:
		return "Stalker portal"
	}
	return "M3U playlist"
}

// DeleteQuestion asks whether to delete the source, saying what goes with
// it.
func (r sourceRow) DeleteQuestion() string {
	q := "Delete " + r.Name + "? "
	switch r.Type {
	case source.TypeXtream, source.TypeStalker:
		return q + "Its channels leave the lineup, with what you changed about them, and its films and series leave the boxes, with how far each profile got in them."
	case source.TypeJellyfin, source.TypePlex:
		return q + "Its films and series leave the boxes, with how far each profile got in them."
	case source.TypeFolder:
		return q + "Its films and series leave the boxes, with how far each profile got in them. The folder and its files stay as they are."
	}
	return q + "Its channels leave the lineup, with what you changed about them."
}

// What names what boxes see of the source.
func (r sourceRow) What() string {
	switch r.Type {
	case source.TypeXtream, source.TypeStalker:
		return "its channels, films and series"
	case source.TypeJellyfin, source.TypePlex, source.TypeFolder:
		return "its films and series"
	}
	return "its channels"
}

// AccountLine says what the server last said about the account, "" if it
// has said nothing.
func (r sourceRow) AccountLine() string {
	a := r.Account
	switch {
	case a.Status == "":
		return ""
	case !strings.EqualFold(a.Status, "active"):
		return a.Status
	case a.ExpiresAt.IsZero():
		return "Active"
	}
	return "Active until " + a.ExpiresAt.UTC().Format("2 Jan 2006")
}

// GuideLine says what the source's guide gave at its last read.
func (r sourceRow) GuideLine() string {
	g := r.Guide
	switch {
	case !r.IsXtream() && !r.IsStalker() && !g.Listed:
		return "None named"
	case g.ReadAt.IsZero():
		return "Not read yet"
	case g.Error != "" && g.Programmes == 0:
		return "Not read"
	}
	return fmt.Sprintf("%s %s, read %s", count(g.Channels), plural(g.Channels, "channel"), ago(g.ReadAt, r.Now))
}

// Limit says how many streams the source may run at once.
func (r sourceRow) Limit() string {
	limit := r.StreamLimit()
	switch {
	case limit == 0:
		return "No limit"
	case limit == r.Account.MaxConnections && limit != r.MaxConnections:
		return strconv.Itoa(limit) + ", the account's limit"
	}
	return strconv.Itoa(limit)
}

// AddressLocked reports whether the address was saved with another key file.
func (r sourceRow) AddressLocked() bool { return r.Address == store.AddressLocked }

// AddressMissing reports whether no address is saved.
func (r sourceRow) AddressMissing() bool { return r.Address == store.AddressMissing }

// ChannelCount is the number of channels it lists.
func (r sourceRow) ChannelCount() string { return count(r.Channels) }

// LastRead says when it was last read.
func (r sourceRow) LastRead() string { return ago(r.RefreshedAt, r.Now) }

// sourcesData is what the sources page shows.
type sourcesData struct {
	Sources []sourceRow
	Form    sourceForm
	Editing bool // the page is a source's own, to change it
	Now     time.Time
}

func (a *Admin) showSources(w http.ResponseWriter, r *http.Request, s store.AdminSession, status int, form sourceForm, editing bool, v view) {
	data := sourcesData{Form: form, Editing: editing, Now: a.now()}
	srcs, err := a.store.Sources(r.Context())
	if err != nil {
		a.log.Error("cannot show the sources", "error", err)
		a.message(w, r, http.StatusInternalServerError, "Something went wrong", "The server's log says what.", &s)
		return
	}
	reads, err := a.libraryReads(r.Context())
	var brings map[int64][]bringSummary
	if err == nil {
		brings, err = a.sourceBrings(r.Context(), srcs)
	}
	if err != nil {
		a.log.Error("cannot show the sources", "error", err)
		a.message(w, r, http.StatusInternalServerError, "Something went wrong", "The server's log says what.", &s)
		return
	}
	for _, src := range srcs {
		data.Sources = append(data.Sources, sourceRow{Source: src, Now: data.Now,
			Films: reads[src.ID][store.KindMovie], Series: reads[src.ID][store.KindSeries], Brings: brings[src.ID]})
	}
	v.Title, v.Section, v.Session, v.Data = "Sources", "sources", &s, data
	if editing {
		v.Title = form.Settings.Name + " · Sources"
	}
	a.render(w, r, status, "sources", v)
}

// newSourceForm is the empty form for a new source.
func newSourceForm() sourceForm {
	return sourceForm{Settings: source.Settings{MaxConnections: source.DefaultMaxConnections}.WithDefaults()}
}

func (a *Admin) sourcesPage(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	a.showSources(w, r, s, http.StatusOK, newSourceForm(), false, view{})
}

// sourcePage shows a source's own page, to change it.
func (a *Admin) sourcePage(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		a.notFound(w, r)
		return
	}
	src, err := a.store.Source(r.Context(), id)
	if err != nil {
		a.notFound(w, r)
		return
	}
	form := sourceForm{ID: id, Settings: src.Settings(), SavedType: src.Type, Host: src.Host, Address: src.Address}
	if src.Type == source.TypeJellyfin {
		// The form sends the user each time, since "" is every library,
		// so it starts with the saved one.
		if saved, err := a.store.SourceSettings(r.Context(), id); err == nil {
			form.Settings.Username = saved.Username
		}
	}
	a.showSources(w, r, s, http.StatusOK, form, true, view{})
}

// formSettings reads a source's settings from the form.
func formSettings(r *http.Request) (source.Settings, error) {
	settings := source.Settings{
		Name:      strings.TrimSpace(r.PostFormValue("name")),
		Type:      r.PostFormValue("type"),
		UserAgent: strings.TrimSpace(r.PostFormValue("user_agent")),
		Numbers:   r.PostFormValue("numbers"),
	}
	if settings.Type == "" {
		settings.Type = source.TypeM3U
	}
	// Space around a pasted login is a mistake, never a part of it.
	limit := "max_connections"
	switch settings.Type {
	case source.TypeXtream:
		settings.URL = strings.TrimSpace(r.PostFormValue("server"))
		settings.Username = strings.TrimSpace(r.PostFormValue("username"))
		settings.Password = strings.TrimSpace(r.PostFormValue("password"))
	case source.TypeJellyfin:
		settings.URL = strings.TrimSpace(r.PostFormValue("jf_server"))
		settings.Password = strings.TrimSpace(r.PostFormValue("jf_key"))
		settings.Username = strings.TrimSpace(r.PostFormValue("jf_user"))
		limit = "library_max_connections"
	case source.TypePlex:
		settings.URL = strings.TrimSpace(r.PostFormValue("px_server"))
		settings.Password = strings.TrimSpace(r.PostFormValue("px_token"))
		limit = "library_max_connections"
	case source.TypeFolder:
		settings.URL = strings.TrimSpace(r.PostFormValue("folder_path"))
		limit = "library_max_connections"
	case source.TypeStalker:
		settings.URL = strings.TrimSpace(r.PostFormValue("portal"))
		settings.Username = strings.TrimSpace(r.PostFormValue("portal_login"))
		settings.Password = strings.TrimSpace(r.PostFormValue("portal_password"))
		settings.Device = source.Device{
			MAC:       strings.TrimSpace(r.PostFormValue("mac")),
			Serial:    strings.TrimSpace(r.PostFormValue("serial")),
			DeviceID:  strings.TrimSpace(r.PostFormValue("device_id")),
			DeviceID2: strings.TrimSpace(r.PostFormValue("device_id2")),
			Signature: strings.TrimSpace(r.PostFormValue("signature")),
			Model:     strings.TrimSpace(r.PostFormValue("model")),
		}
	default:
		settings.URL = strings.TrimSpace(r.PostFormValue("url"))
	}
	if settings.Type == source.TypeXtream || settings.Type == source.TypeM3U {
		settings.CatchupZone = strings.TrimSpace(r.PostFormValue("catchup_zone"))
	}
	if settings.Type != source.TypeFolder {
		settings.PublicOnly = r.PostFormValue("public_only") == "1"
	}
	n, present, ok := formInt(r, limit)
	if !ok {
		return settings, badEdit(http.StatusBadRequest, "bad_max_connections", "Streams at once is a whole number, 0 for no limit.")
	}
	if present {
		settings.MaxConnections = int(n)
	}
	secs, _, ok := formInt(r, "refresh")
	if !ok {
		return settings, badEdit(http.StatusBadRequest, "bad_refresh", "Choose how often to read the source.")
	}
	settings.Refresh = time.Duration(secs) * time.Second
	return settings, nil
}

// sourceDone finishes a change on the sources page: a browser without htmx
// goes back to the list; with htmx, the list comes back with what was done.
// A mistake shows the form again, as it was sent.
func (a *Admin) sourceDone(w http.ResponseWriter, r *http.Request, s store.AdminSession, err error, notice string, form sourceForm, editing bool) {
	var bad *editError
	switch {
	case errors.As(err, &bad):
		a.showSources(w, r, s, bad.status, form, editing, view{Error: bad.text})
	case err != nil:
		a.log.Error("cannot change a source", "error", err)
		a.showSources(w, r, s, http.StatusInternalServerError, form, editing, view{Error: "Something went wrong; the server's log says what."})
	case r.Header.Get("HX-Request") == "true":
		w.Header().Set("HX-Replace-Url", "/admin/sources")
		a.showSources(w, r, s, http.StatusOK, newSourceForm(), false, view{Notice: notice})
	default:
		http.Redirect(w, r, "/admin/sources", http.StatusSeeOther)
	}
}

// addSourceForm adds a source, or tests the form's settings.
func (a *Admin) addSourceForm(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	settings, err := formSettings(r)
	form := sourceForm{Settings: settings, Bring: formBring(r)}
	if err == nil && r.PostFormValue("test") != "" {
		a.testForm(w, r, s, form, false)
		return
	}
	onlyTicked := formBring(r) && settings.Type != source.TypeFolder
	if err == nil {
		_, err = a.addSource(r.Context(), settings, onlyTicked)
	}
	done := "Added " + settings.Name + ". Overscan is reading it now; its channels join the lineup in a moment."
	switch {
	case onlyTicked && settings.HasChannels():
		done = "Added " + settings.Name + ". Overscan is reading its categories now: choose those it brings from its card, with Choose categories."
	case onlyTicked:
		done = "Added " + settings.Name + ". Its films and series bring only the categories you tick: choose them from its card, with Choose categories."
	case !settings.HasChannels():
		done = "Added " + settings.Name + ". Boxes find its films and series under Movies and Series."
	}
	a.sourceDone(w, r, s, err, done, form, false)
}

// saveSourceForm saves a source's settings, or tests them.
func (a *Admin) saveSourceForm(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		a.notFound(w, r)
		return
	}
	src, err := a.store.Source(r.Context(), id)
	if err != nil {
		a.notFound(w, r)
		return
	}
	settings, err := formSettings(r)
	form := sourceForm{ID: id, Settings: settings, SavedType: src.Type, Host: src.Host, Address: src.Address}
	if err == nil && r.PostFormValue("test") != "" {
		a.testForm(w, r, s, form, true)
		return
	}
	if err == nil {
		err = a.updateSource(r.Context(), id, settings)
	}
	a.sourceDone(w, r, s, err, "Saved "+settings.Name+".", form, true)
}

// testForm tests the form's settings and shows the form again with what the
// test found.
func (a *Admin) testForm(w http.ResponseWriter, r *http.Request, s store.AdminSession, form sourceForm, editing bool) {
	settings := form.Settings
	if settings.Name == "" {
		settings.Name = "test"
	}
	t, err := a.testSource(r.Context(), form.ID, settings)
	var bad *editError
	switch {
	case err == nil:
		form.Tested = &t
	case errors.As(err, &bad):
		form.TestErr = bad.text
	default:
		a.log.Error("cannot test a source", "error", err)
		form.TestErr = "Something went wrong; the server's log says what."
	}
	a.showSources(w, r, s, http.StatusOK, form, editing, view{})
}

// sourceAction serves a source's buttons on the list.
func (a *Admin) sourceAction(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	ctx := r.Context()
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		a.notFound(w, r)
		return
	}
	src, err := a.store.Source(ctx, id)
	if err != nil {
		a.sourceDone(w, r, s, sourceError(err), "", newSourceForm(), false)
		return
	}
	var done string
	switch r.PathValue("action") {
	case "refresh":
		var ch store.CatalogChanges
		if ch, err = a.refreshSource(ctx, id); err == nil {
			n := ch.Added + ch.Updated
			done = fmt.Sprintf("Read %s: %s %s, %s new, %s gone missing, %s removed.", src.Name, count(n), plural(n, "channel"),
				count(ch.Added), count(ch.Missing), count(ch.Purged))
			if ch.LeftOut > 0 {
				done = strings.TrimSuffix(done, ".") + fmt.Sprintf(", and %s left out with their categories.", count(ch.LeftOut))
			}
			switch src.Type {
			case source.TypeJellyfin:
				done = "Checked " + src.Name + ": the server answers, and takes the API key."
			case source.TypePlex:
				done = "Checked " + src.Name + ": the server answers, and takes the token."
			case source.TypeFolder:
				done = "Checked " + src.Name + ": Overscan can read the folder."
			}
		}
	case "on":
		err, done = a.setSourceEnabled(ctx, id, true), "Turned "+src.Name+" on. Overscan is reading it now."
	case "off":
		row := sourceRow{Source: src}
		err, done = a.setSourceEnabled(ctx, id, false), "Turned "+src.Name+" off: devices no longer see "+row.What()+"."
	case "delete":
		var name string
		row := sourceRow{Source: src}
		if name, err = a.deleteSource(ctx, id); err == nil {
			done = "Deleted " + name + ", with " + row.What() + "."
		}
	case "guide-logos":
		how := r.PostFormValue("guide_logos")
		if err = a.setSourceGuideLogos(ctx, id, how); err == nil {
			done = "The logos of " + src.Name + "'s guide: " + strings.ToLower(logosText[how][:1]) + logosText[how][1:] + "."
		}
	case "categories-hidden", "categories-shown":
		hidden := r.PathValue("action") == "categories-hidden"
		if err = lineupStoreError(a.store.SetNewCategoriesHidden(ctx, id, hidden), "source"); err == nil {
			done = "New categories from " + src.Name + " will start shown."
			if hidden {
				done = "New categories from " + src.Name + " will start hidden."
			}
		}
	default:
		a.notFound(w, r)
		return
	}
	a.sourceDone(w, r, s, err, done, newSourceForm(), false)
}
