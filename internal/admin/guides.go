// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"overscan.gureet.ca/internal/guide"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

// Guides of the household's own (D139): XMLTV guides that the
// administrator adds, at an address or in a file on the server, which fill
// the channels whose own sources give them no programme. The admin adds,
// changes, reads, puts in order, turns on and off and removes them. A
// saved address is never shown again, only its host or the file's name.

// Guides reads the guides of the household's own for the admin; what
// guide.Own does.
type Guides interface {
	// ReadNow reads a guide now, once the read under way ends.
	ReadNow(ctx context.Context, id int64) (guide.OwnResult, error)
	// Changed says that the guides changed, so that the channels are
	// matched with them again.
	Changed()
	// Reading returns the guide being read, 0 if none is.
	Reading() int64
}

// UseGuides gives the admin what reads the guides. Without it, the admin
// can change guides but cannot read them.
func (a *Admin) UseGuides(g Guides) { a.guides = g }

// guideWait is how long the admin waits for a guide's read before saying
// that it goes on.
var guideWait = 20 * time.Second

// guideError turns the store's and the reader's errors about a guide into
// mistakes that the request can put right.
func guideError(err error) error {
	var bad *source.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &bad):
		return badEdit(http.StatusBadRequest, "bad_"+bad.Field, "%s", bad.Text)
	case errors.Is(err, store.ErrGuideExists):
		return badEdit(http.StatusConflict, "name_taken", "Another guide has that name.")
	case errors.Is(err, store.ErrTooManyGuides):
		return badEdit(http.StatusConflict, "too_many", "A server keeps at most %d guides of its own.", store.MaxGuides)
	case errors.Is(err, store.ErrNotFound):
		return badEdit(http.StatusNotFound, "not_found", "There is no such guide.")
	case errors.Is(err, store.ErrUnreadable):
		return badEdit(http.StatusConflict, "address_locked",
			"The saved address was encrypted with another key file; enter it again, or put back the key file that saved it.")
	case errors.Is(err, guide.ErrBusy):
		return badEdit(http.StatusServiceUnavailable, "busy", "Other reads of guides wait; try again in a while.")
	}
	return err
}

var errNoGuideReader = badEdit(http.StatusServiceUnavailable, "no_reader", "This server cannot read guides from the admin.")

func (a *Admin) guidesChanged() {
	if a.guides != nil {
		a.guides.Changed()
	}
}

func (a *Admin) addGuide(ctx context.Context, g store.GuideSettings) (int64, error) {
	id, err := a.store.AddGuide(ctx, g, a.now())
	if err != nil {
		return 0, guideError(err)
	}
	a.log.Info("guide added in the admin", "guide", g.Name)
	a.guidesChanged()
	return id, nil
}

func (a *Admin) updateGuide(ctx context.Context, id int64, g store.GuideSettings) error {
	if err := a.store.UpdateGuide(ctx, id, g); err != nil {
		return guideError(err)
	}
	a.log.Info("guide changed in the admin", "guide", g.Name, "new_address", g.Address != "")
	a.guidesChanged()
	return nil
}

func (a *Admin) setGuideEnabled(ctx context.Context, id int64, on bool) error {
	if err := a.store.SetGuideEnabled(ctx, id, on); err != nil {
		return guideError(err)
	}
	a.log.Info("guide turned on or off in the admin", "guide_id", id, "enabled", on)
	a.guidesChanged()
	return nil
}

func (a *Admin) moveGuide(ctx context.Context, id int64, position int) error {
	if err := a.store.MoveGuide(ctx, id, position); err != nil {
		return guideError(err)
	}
	a.log.Info("guide moved in the admin", "guide_id", id, "position", position)
	a.guidesChanged()
	return nil
}

func (a *Admin) deleteGuide(ctx context.Context, id int64) (string, error) {
	g, err := a.store.Guide(ctx, id)
	if err == nil {
		err = a.store.DeleteGuide(ctx, id)
	}
	if err != nil {
		return "", guideError(err)
	}
	a.log.Info("guide removed in the admin", "guide", g.Name)
	a.guidesChanged()
	return g.Name, nil
}

// readGuide reads a guide now, waiting up to guideWait for it: a read that
// takes longer goes on after it returns, reading set.
func (a *Admin) readGuide(ctx context.Context, id int64) (res guide.OwnResult, reading bool, err error) {
	if a.guides == nil {
		return res, false, errNoGuideReader
	}
	if _, err := a.store.Guide(ctx, id); err != nil {
		return res, false, guideError(err)
	}
	wait, cancel := context.WithTimeout(ctx, guideWait)
	defer cancel()
	res, err = a.guides.ReadNow(wait, id)
	switch {
	case err == nil:
		return res, false, nil
	case errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil:
		return res, true, nil
	}
	if err := guideError(err); isEditError(err) || errors.Is(err, context.Canceled) {
		return res, false, err
	}
	return res, false, badEdit(http.StatusBadGateway, "guide_failed", "The guide could not be read: %s.", strings.TrimSuffix(err.Error(), "."))
}

// The API.

// apiOwnGuide is a guide of the household's own as the API describes it,
// without its address.
type apiOwnGuide struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	Host       string  `json:"host"`
	Address    string  `json:"address"` // saved or locked
	UserAgent  string  `json:"user_agent"`
	PublicOnly bool    `json:"public_only"`
	Enabled    bool    `json:"enabled"`
	Position   int     `json:"position"`
	Reading    bool    `json:"reading"`
	ReadAt     *string `json:"read_at"`
	ReadError  string  `json:"read_error"`
	Listed     int     `json:"listed"`
	Channels   int     `json:"channels"`
	Programmes int     `json:"programmes"`
	Fills      int     `json:"fills"`
	ByName     int     `json:"by_name"`
	Waiting    int     `json:"waiting"`
}

func (a *Admin) ownGuideBody(g store.Guide) apiOwnGuide {
	out := apiOwnGuide{ID: g.ID, Name: g.Name, Host: g.Host, Address: addressNames[g.Address], UserAgent: g.UserAgent,
		PublicOnly: g.PublicOnly, Enabled: g.Enabled, Position: g.Position, Reading: a.guideReading() == g.ID,
		ReadError: g.ReadError, Listed: g.Listed, Channels: g.Channels, Programmes: g.Programmes,
		Fills: g.Matched, ByName: g.ByName, Waiting: g.Waiting}
	if !g.ReadAt.IsZero() {
		t := g.ReadAt.UTC().Format("2006-01-02T15:04:05Z")
		out.ReadAt = &t
	}
	return out
}

func (a *Admin) guideReading() int64 {
	if a.guides == nil {
		return 0
	}
	return a.guides.Reading()
}

func (a *Admin) apiGuides(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	gs, err := a.store.Guides(r.Context())
	if a.editAnswer(w, err) {
		return
	}
	without, err := a.store.ChannelsWithoutGuide(r.Context())
	if a.editAnswer(w, err) {
		return
	}
	out := make([]apiOwnGuide, len(gs))
	for i, g := range gs {
		out[i] = a.ownGuideBody(g)
	}
	writeJSON(w, http.StatusOK, struct {
		Guides []apiOwnGuide `json:"guides"`
		// WithoutGuide counts the channels of enabled sources that have
		// no programme of their own, nor a guide of the household's own.
		WithoutGuide int `json:"without_guide"`
	}{out, without})
}

func (a *Admin) writeOwnGuide(w http.ResponseWriter, r *http.Request, status int, id int64) {
	g, err := a.store.Guide(r.Context(), id)
	if a.editAnswer(w, guideError(err)) {
		return
	}
	writeJSON(w, status, a.ownGuideBody(g))
}

func (a *Admin) apiOwnGuide(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	id, err := pathID(r)
	if a.editAnswer(w, err) {
		return
	}
	a.writeOwnGuide(w, r, http.StatusOK, id)
}

// apiGuideChange is the body of a request that adds or changes a guide.
// What it leaves out stays as it is, or takes its default for a new guide.
type apiGuideChange struct {
	Name       *string `json:"name"`
	Address    *string `json:"address"`
	UserAgent  *string `json:"user_agent"`
	PublicOnly *bool   `json:"public_only"`
	Enabled    *bool   `json:"enabled"`
	Position   *int    `json:"position"`
}

// apply puts the body's settings over g.
func (c apiGuideChange) apply(g store.GuideSettings) store.GuideSettings {
	if c.Name != nil {
		g.Name = strings.TrimSpace(*c.Name)
	}
	if c.Address != nil {
		g.Address = strings.TrimSpace(*c.Address)
	}
	if c.UserAgent != nil {
		g.UserAgent = strings.TrimSpace(*c.UserAgent)
	}
	if c.PublicOnly != nil {
		g.PublicOnly = *c.PublicOnly
	}
	return g
}

func (a *Admin) apiAddGuide(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	var body apiGuideChange
	if !readJSON(w, r, &body) {
		return
	}
	if body.Address == nil || strings.TrimSpace(*body.Address) == "" {
		apiError(w, http.StatusBadRequest, "bad_address", "A new guide needs its address: an http or https address, or a file's path.")
		return
	}
	if body.Position != nil {
		apiError(w, http.StatusBadRequest, "bad_position", "A new guide goes after the others; move it with PATCH guides/{id}.")
		return
	}
	ctx := r.Context()
	id, err := a.addGuide(ctx, body.apply(store.GuideSettings{}))
	if a.editAnswer(w, err) {
		return
	}
	if body.Enabled != nil && !*body.Enabled {
		err = a.setGuideEnabled(ctx, id, false)
	}
	if a.editAnswer(w, err) {
		return
	}
	a.writeOwnGuide(w, r, http.StatusCreated, id)
}

func (a *Admin) apiChangeGuide(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	ctx := r.Context()
	id, err := pathID(r)
	if a.editAnswer(w, err) {
		return
	}
	var body apiGuideChange
	if !readJSON(w, r, &body) {
		return
	}
	if body == (apiGuideChange{}) {
		apiError(w, http.StatusBadRequest, "bad_json", "Say what to change.")
		return
	}
	if body.Address != nil && strings.TrimSpace(*body.Address) == "" {
		apiError(w, http.StatusBadRequest, "bad_address", "Leave address out to keep the saved one.")
		return
	}
	g, err := a.store.Guide(ctx, id)
	if a.editAnswer(w, guideError(err)) {
		return
	}
	if body.Name != nil || body.Address != nil || body.UserAgent != nil || body.PublicOnly != nil {
		settings := body.apply(store.GuideSettings{Name: g.Name, UserAgent: g.UserAgent, PublicOnly: g.PublicOnly})
		if a.editAnswer(w, a.updateGuide(ctx, id, settings)) {
			return
		}
	}
	if body.Enabled != nil && a.editAnswer(w, a.setGuideEnabled(ctx, id, *body.Enabled)) {
		return
	}
	if body.Position != nil {
		if *body.Position < 1 {
			apiError(w, http.StatusBadRequest, "bad_position", "Positions count from 1, the first guide's.")
			return
		}
		if a.editAnswer(w, a.moveGuide(ctx, id, *body.Position)) {
			return
		}
	}
	a.writeOwnGuide(w, r, http.StatusOK, id)
}

func (a *Admin) apiDeleteGuide(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	id, err := pathID(r)
	if a.editAnswer(w, err) {
		return
	}
	if _, err := a.deleteGuide(r.Context(), id); a.editAnswer(w, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// apiGuideRead is what a read of a guide of the household's own found, or
// that it goes on.
type apiGuideRead struct {
	Reading    bool `json:"reading"` // the read goes on: the guide's state says how it ends
	Listed     int  `json:"listed"`
	Fills      int  `json:"fills"`
	ByName     int  `json:"by_name"`
	Channels   int  `json:"channels"`
	Programmes int  `json:"programmes"`
	Skipped    int  `json:"skipped"`
}

func (a *Admin) apiReadGuide(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	id, err := pathID(r)
	if a.editAnswer(w, err) {
		return
	}
	res, reading, err := a.readGuide(r.Context(), id)
	if a.editAnswer(w, err) {
		return
	}
	status := http.StatusOK
	if reading {
		status = http.StatusAccepted
	}
	writeJSON(w, status, apiGuideRead{Reading: reading, Listed: res.Listed, Fills: res.Fills, ByName: res.ByName,
		Channels: res.Channels, Programmes: res.Programmes, Skipped: res.Skipped})
}

// The pages.

// guideForm is what the form to add or change a guide shows.
type guideForm struct {
	ID       int64 // 0 for a new guide
	Settings store.GuideSettings
	Host     string
	Address  store.AddressState
}

// AddressLocked reports whether the saved address was saved with another
// key file.
func (f guideForm) AddressLocked() bool { return f.ID != 0 && f.Address == store.AddressLocked }

// guideRow is a guide on the list.
type guideRow struct {
	store.Guide
	Now     time.Time
	Reading bool
	Count   int // how many guides there are
}

// Order says where the guide is among the guides.
func (r guideRow) Order() string {
	if r.Count < 2 {
		return ""
	}
	if r.Position == 1 {
		return "First: where two guides could fill a channel, this one does"
	}
	return fmt.Sprintf("%s of %d", ordinal(r.Position), r.Count)
}

// First and Last report whether the guide is first or last in order.
func (r guideRow) First() bool { return r.Position <= 1 }

// Last: see First.
func (r guideRow) Last() bool { return r.Position >= r.Count }

// ordinal writes 2 as 2nd and so on.
func ordinal(n int) string {
	suffix := "th"
	switch {
	case n%100 >= 11 && n%100 <= 13:
	case n%10 == 1:
		suffix = "st"
	case n%10 == 2:
		suffix = "nd"
	case n%10 == 3:
		suffix = "rd"
	}
	return strconv.Itoa(n) + suffix
}

// AddressLocked reports whether the address was saved with another key
// file.
func (r guideRow) AddressLocked() bool { return r.Address == store.AddressLocked }

// IsFile reports whether the guide is a file on the server.
func (r guideRow) IsFile() bool { return strings.HasPrefix(r.Host, "file ") }

// HostText is what the card shows of the guide's address.
func (r guideRow) HostText() string {
	if name, ok := strings.CutPrefix(r.Host, "file "); ok {
		return name + ", a file on this server"
	}
	return r.Host
}

// LastRead says when it was last read.
func (r guideRow) LastRead() string { return ago(r.ReadAt, r.Now) }

// Unread reports whether no read of the guide has worked yet.
func (r guideRow) Unread() bool { return r.ReadAt.IsZero() || r.Listed == 0 && r.ReadError != "" }

// FillsLine says which channels the guide fills.
func (r guideRow) FillsLine() string {
	switch {
	case r.Unread():
		return "None yet: it has not been read"
	case r.Matched == 0:
		return "None of the channels without programmes of their own: it lists none of their guide IDs, nor their names alone"
	}
	text := fmt.Sprintf("%s %s", count(r.Matched), plural(r.Matched, "channel"))
	byID := r.Matched - r.ByName
	switch {
	case r.ByName == 0:
		text += ", by guide ID"
	case byID == 0:
		text += ", by name"
	default:
		text += fmt.Sprintf(": %s by guide ID, %s by name", count(byID), count(r.ByName))
	}
	if r.Waiting > 0 {
		text += fmt.Sprintf(". Its next read brings the programmes of %s of them", count(r.Waiting))
	}
	return text
}

// ListsLine says what the guide's last read listed and kept.
func (r guideRow) ListsLine() string {
	switch {
	case r.Unread():
		return "Nothing read yet"
	case r.Programmes == 0 && r.Matched == 0:
		return fmt.Sprintf("%s %s; it keeps none of their programmes, since none of them fills a channel", count(r.Listed),
			plural(r.Listed, "channel"))
	case r.Programmes == 0:
		return fmt.Sprintf("%s %s; those that fill a channel have no programmes", count(r.Listed), plural(r.Listed, "channel"))
	}
	return fmt.Sprintf("%s %s; it keeps %s %s, of %s of them", count(r.Listed), plural(r.Listed, "channel"),
		count(r.Programmes), plural(r.Programmes, "programme"), count(r.Channels))
}

// DeleteQuestion asks whether to remove the guide.
func (r guideRow) DeleteQuestion() string {
	return "Remove " + r.Name + "? The channels that it fills lose its programmes, and take another guide's where one has their guide IDs or names."
}

// guidesData is what the guides page shows.
type guidesData struct {
	Guides  []guideRow
	Form    guideForm
	Editing bool // the page is a guide's own, to change it
	// WithoutGuide counts the channels of enabled sources that have no
	// programme of their own, nor a guide of the household's own.
	WithoutGuide int
	Filled       int // the channels that the guides fill
	Full         bool
	Now          time.Time
}

func (a *Admin) showGuides(w http.ResponseWriter, r *http.Request, s store.AdminSession, status int, form guideForm, editing bool, v view) {
	data := guidesData{Form: form, Editing: editing, Now: a.now()}
	gs, err := a.store.Guides(r.Context())
	if err == nil {
		data.WithoutGuide, err = a.store.ChannelsWithoutGuide(r.Context())
	}
	if err != nil {
		a.log.Error("cannot show the guides", "error", err)
		a.message(w, r, http.StatusInternalServerError, "Something went wrong", "The server's log says what.", &s)
		return
	}
	reading := a.guideReading()
	for _, g := range gs {
		data.Guides = append(data.Guides, guideRow{Guide: g, Now: data.Now, Reading: reading == g.ID, Count: len(gs)})
		if g.Enabled {
			data.Filled += g.Matched
		}
	}
	data.Full = len(gs) >= store.MaxGuides
	v.Title, v.Section, v.Session, v.Data = "Guides", "guides", &s, data
	if editing {
		v.Title = form.Settings.Name + " · Guides"
	}
	a.render(w, r, status, "guides", v)
}

func (a *Admin) guidesPage(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	a.showGuides(w, r, s, http.StatusOK, guideForm{}, false, view{})
}

// guidePage shows a guide's own page, to change it.
func (a *Admin) guidePage(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		a.notFound(w, r)
		return
	}
	g, err := a.store.Guide(r.Context(), id)
	if err != nil {
		a.notFound(w, r)
		return
	}
	form := guideForm{ID: id, Settings: store.GuideSettings{Name: g.Name, UserAgent: g.UserAgent, PublicOnly: g.PublicOnly},
		Host: g.Host, Address: g.Address}
	a.showGuides(w, r, s, http.StatusOK, form, true, view{})
}

// formGuide reads a guide's settings from the form.
func formGuide(r *http.Request) store.GuideSettings {
	return store.GuideSettings{
		Name:       strings.TrimSpace(r.PostFormValue("name")),
		Address:    strings.TrimSpace(r.PostFormValue("address")),
		UserAgent:  strings.TrimSpace(r.PostFormValue("user_agent")),
		PublicOnly: r.PostFormValue("public_only") == "1",
	}
}

// guideDone finishes a change on the guides page: a browser without htmx
// goes back to the list; with htmx, the list comes back with what was
// done. A mistake shows the form again, as it was sent.
func (a *Admin) guideDone(w http.ResponseWriter, r *http.Request, s store.AdminSession, err error, notice string, form guideForm, editing bool) {
	var bad *editError
	switch {
	case errors.As(err, &bad):
		a.showGuides(w, r, s, bad.status, form, editing, view{Error: bad.text})
	case err != nil:
		a.log.Error("cannot change a guide", "error", err)
		a.showGuides(w, r, s, http.StatusInternalServerError, form, editing, view{Error: "Something went wrong; the server's log says what."})
	case r.Header.Get("HX-Request") == "true":
		w.Header().Set("HX-Replace-Url", "/admin/guides")
		a.showGuides(w, r, s, http.StatusOK, guideForm{}, false, view{Notice: notice})
	default:
		http.Redirect(w, r, "/admin/guides", http.StatusSeeOther)
	}
}

// addGuideForm adds a guide.
func (a *Admin) addGuideForm(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	settings := formGuide(r)
	form := guideForm{Settings: settings}
	var err error
	if settings.Address == "" {
		err = badEdit(http.StatusBadRequest, "bad_address", "Enter the guide's address: an http or https address, or a file's path.")
	}
	if err == nil {
		_, err = a.addGuide(r.Context(), settings)
	}
	a.guideDone(w, r, s, err, "Added "+settings.Name+". Overscan is reading it now; the channels that it fills show its programmes once it has.", form, false)
}

// saveGuideForm saves a guide's settings.
func (a *Admin) saveGuideForm(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		a.notFound(w, r)
		return
	}
	g, err := a.store.Guide(r.Context(), id)
	if err != nil {
		a.notFound(w, r)
		return
	}
	settings := formGuide(r)
	form := guideForm{ID: id, Settings: settings, Host: g.Host, Address: g.Address}
	err = a.updateGuide(r.Context(), id, settings)
	a.guideDone(w, r, s, err, "Saved "+settings.Name+".", form, true)
}

// guideAction serves a guide's buttons on the list.
func (a *Admin) guideAction(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	ctx := r.Context()
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		a.notFound(w, r)
		return
	}
	g, err := a.store.Guide(ctx, id)
	if err != nil {
		a.guideDone(w, r, s, guideError(err), "", guideForm{}, false)
		return
	}
	var done string
	switch r.PathValue("action") {
	case "read":
		var res guide.OwnResult
		var reading bool
		if res, reading, err = a.readGuide(ctx, id); err == nil {
			done = fmt.Sprintf("Read %s: it lists %s %s, and fills %s %s.", g.Name, count(res.Listed), plural(res.Listed, "channel"),
				count(res.Fills), plural(res.Fills, "channel"))
			if reading {
				done = "Overscan is still reading " + g.Name + ". Its card says how the read went once it ends."
			}
		}
	case "on":
		err, done = a.setGuideEnabled(ctx, id, true), "Turned "+g.Name+" on. It fills the channels that it can."
	case "off":
		err, done = a.setGuideEnabled(ctx, id, false), "Turned "+g.Name+" off: the channels that it filled take another guide's, or none."
	case "up":
		err, done = a.moveGuide(ctx, id, g.Position-1), "Moved "+g.Name+" up."
	case "down":
		err, done = a.moveGuide(ctx, id, g.Position+1), "Moved "+g.Name+" down."
	case "delete":
		var name string
		if name, err = a.deleteGuide(ctx, id); err == nil {
			done = "Removed " + name + "."
		}
	default:
		a.notFound(w, r)
		return
	}
	a.guideDone(w, r, s, err, done, guideForm{}, false)
}
