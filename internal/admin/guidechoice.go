// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"overscan.gureet.ca/internal/guide"
	"overscan.gureet.ca/internal/store"
)

// A channel's guide chosen by hand (D140): the channels and where their
// programmes come from, those without a guide with suggestions of close
// names to take, those that a name matched, to check, and those chosen by
// hand; and, for one channel, a search of every guide's channels and of
// the channels with programmes of their own, with what is on now and next.

const (
	// channelsPage is how many channels a page of the list shows.
	channelsPage = 100
	// maxChannelsAPI caps how many the API lists at once.
	maxChannelsAPI = 500
	// shownSuggestions is how many suggestions a row of the list offers.
	shownSuggestions = 2
	// programmesShown is how far ahead now and next are looked for.
	programmesAhead = 12 * time.Hour
	// optionsWithProgrammes caps the options found whose programmes a
	// search shows.
	optionsWithProgrammes = 20
)

// choiceError turns the store's errors about a choice into mistakes that
// the request can put right.
func choiceError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrNotFound):
		return badEdit(http.StatusNotFound, "not_found", "There is no such channel.")
	case errors.Is(err, store.ErrNoGuideChoice):
		return badEdit(http.StatusBadRequest, "no_such_guide",
			"There is no such channel to take a guide from: another channel, or a guide's channel, that exists.")
	}
	return err
}

// chooseGuide makes a channel's choice, logs it and has the guides matched
// again.
func (a *Admin) chooseGuide(ctx context.Context, c store.GuideChoice) error {
	if err := a.store.SetGuideChoice(ctx, c, a.now()); err != nil {
		return choiceError(err)
	}
	args := []any{}
	if g, err := a.store.ChannelGuideOf(ctx, c.ChannelID); err == nil {
		args = append(args, "channel", g.Name, "source", g.Source)
		switch g.From() {
		case "none":
			args = append(args, "takes", "no guide")
		case "channel":
			args = append(args, "takes", "the guide of "+g.UseName, "from", g.UseSource)
		case "guide":
			name := g.GuideXMLTVID
			if len(g.GuideNames) > 0 {
				name = g.GuideNames[0]
			}
			args = append(args, "takes", name, "from", g.Guide)
		}
	}
	a.log.Info("a channel's guide chosen by hand in the admin", args...)
	if a.guides != nil {
		a.guides.Changed()
	}
	return nil
}

// unchooseGuide takes a channel's choice back.
func (a *Admin) unchooseGuide(ctx context.Context, channelID int64) error {
	g, err := a.store.ChannelGuideOf(ctx, channelID)
	if err != nil {
		return choiceError(err)
	}
	had, err := a.store.ClearGuideChoice(ctx, channelID)
	if err != nil || !had {
		return err
	}
	a.log.Info("a channel's guide chosen by hand taken back in the admin", "channel", g.Name, "source", g.Source)
	if a.guides != nil {
		a.guides.Changed()
	}
	return nil
}

// guideNowNext returns the programme on at now and the one after it, from
// where a channel's programmes come from, before any lineup's rules.
func (a *Admin) guideNowNext(ctx context.Context, g store.ChannelGuide, now time.Time) ([]store.Programme, error) {
	switch g.From() {
	case "own":
		return firstTwo(a.store.ChannelProgrammes(ctx, g.ChannelID, now, now.Add(programmesAhead)))
	case "guide":
		if !g.GuideOn {
			return nil, nil
		}
		return firstTwo(a.store.GuideChannelProgrammes(ctx, g.GuideChannelID, now, now.Add(programmesAhead)))
	case "channel":
		// The other channel's guide as devices see it there: the guide's
		// channel that fills it, by a match or by hand, else its own
		// source's; never what it takes from a third channel.
		other, err := a.store.ChannelGuideOf(ctx, g.UseChannel)
		switch {
		case errors.Is(err, store.ErrNotFound):
			return nil, nil
		case err != nil:
			return nil, err
		case other.GuideChannelID != 0 && other.GuideOn:
			return firstTwo(a.store.GuideChannelProgrammes(ctx, other.GuideChannelID, now, now.Add(programmesAhead)))
		}
		return firstTwo(a.store.ChannelProgrammes(ctx, other.ChannelID, now, now.Add(programmesAhead)))
	}
	return nil, nil
}

// optionNowNext returns an option's programme on now and the one after it,
// where Overscan holds its programmes.
func (a *Admin) optionNowNext(ctx context.Context, o guide.Option, now time.Time) ([]store.Programme, error) {
	switch {
	case o.ChannelID != 0:
		return firstTwo(a.store.ChannelProgrammes(ctx, o.ChannelID, now, now.Add(programmesAhead)))
	case o.Kept:
		return firstTwo(a.store.GuideChannelProgrammes(ctx, o.GuideChannelID, now, now.Add(programmesAhead)))
	}
	return nil, nil
}

func firstTwo(ps []store.Programme, err error) ([]store.Programme, error) {
	if len(ps) > 2 {
		ps = ps[:2]
	}
	return ps, err
}

// guideFromText says where a channel's programmes come from, in words.
func guideFromText(g store.ChannelGuide) string {
	guideChannel := func() string {
		name := g.GuideXMLTVID
		if len(g.GuideNames) > 0 && g.GuideNames[0] != "" {
			name = g.GuideNames[0]
		}
		return g.Guide + "'s " + name
	}
	switch g.From() {
	case "none":
		return "No guide, chosen by hand."
	case "channel":
		return "The guide of " + g.UseName + ", from " + g.UseSource + ", chosen by hand."
	case "guide":
		text := guideChannel()
		switch {
		case g.Chosen && !g.GuideOn:
			return text + ", chosen by hand. The guide is off, so the channel shows nothing until you turn it on."
		case g.Chosen:
			return text + ", chosen by hand."
		case g.ByName:
			return text + ", matched by its name."
		}
		return text + ", matched by its guide ID."
	case "own":
		return "Its source's guide."
	}
	return "None: its source's guide has no programmes for it, and no guide of your own matches it."
}

// The API.

type apiProgramme struct {
	Start string `json:"start"`
	Stop  string `json:"stop"`
	Title string `json:"title"`
}

func programmesBody(ps []store.Programme) []apiProgramme {
	out := make([]apiProgramme, len(ps))
	for i, p := range ps {
		out[i] = apiProgramme{Start: p.Start.UTC().Format(time.RFC3339), Stop: p.Stop.UTC().Format(time.RFC3339), Title: p.Title}
	}
	return out
}

// apiGuideOption is something a channel's guide can be chosen from.
type apiGuideOption struct {
	GuideChannelID int64          `json:"guide_channel_id,omitempty"`
	ChannelID      int64          `json:"channel_id,omitempty"`
	Name           string         `json:"name"`
	Names          []string       `json:"names"`
	XMLTVID        string         `json:"xmltv_id"`
	GuideID        int64          `json:"guide_id,omitempty"`
	Guide          string         `json:"guide,omitempty"`
	SourceID       int64          `json:"source_id,omitempty"`
	Source         string         `json:"source,omitempty"`
	Programmes     []apiProgramme `json:"programmes"`
}

func (a *Admin) optionBody(ctx context.Context, o guide.Option, now time.Time, withProgrammes bool) (apiGuideOption, error) {
	out := apiGuideOption{GuideChannelID: o.GuideChannelID, ChannelID: o.ChannelID, Name: o.Name(), Names: o.Names,
		XMLTVID: o.XMLTVID, GuideID: o.GuideID, Guide: o.Guide, SourceID: o.SourceID, Source: o.Source, Programmes: []apiProgramme{}}
	if out.Names == nil {
		out.Names = []string{}
	}
	if withProgrammes {
		ps, err := a.optionNowNext(ctx, o, now)
		if err != nil {
			return out, err
		}
		out.Programmes = programmesBody(ps)
	}
	return out, nil
}

type apiGuideMatch struct {
	GuideChannelID int64    `json:"guide_channel_id"`
	GuideID        int64    `json:"guide_id"`
	Guide          string   `json:"guide"`
	Name           string   `json:"name"`
	Names          []string `json:"names"`
	XMLTVID        string   `json:"xmltv_id"`
	On             bool     `json:"on"`
	ByName         bool     `json:"by_name"`
	ByHand         bool     `json:"by_hand"`
}

type apiGuideChosen struct {
	None           bool   `json:"none"`
	ChannelID      int64  `json:"channel_id,omitempty"`
	Name           string `json:"name,omitempty"`
	Source         string `json:"source,omitempty"`
	GuideChannelID int64  `json:"guide_channel_id,omitempty"`
	ChosenAt       string `json:"chosen_at"`
}

// apiChannelGuide is where a channel's programmes come from, before any
// lineup's rules.
type apiChannelGuide struct {
	ChannelID   int64            `json:"channel_id"`
	Name        string           `json:"name"`
	XMLTVID     string           `json:"xmltv_id"`
	Category    string           `json:"category"`
	SourceID    int64            `json:"source_id"`
	Source      string           `json:"source"`
	Missing     bool             `json:"missing"`
	From        string           `json:"from"`
	Guide       *apiGuideMatch   `json:"guide"`
	Chosen      *apiGuideChosen  `json:"chosen"`
	Programmes  []apiProgramme   `json:"programmes,omitempty"`
	Suggestions []apiGuideOption `json:"suggestions,omitempty"`
}

func channelGuideBody(g store.ChannelGuide) apiChannelGuide {
	out := apiChannelGuide{ChannelID: g.ChannelID, Name: g.Name, XMLTVID: g.XMLTVID, Category: g.Category, SourceID: g.SourceID,
		Source: g.Source, Missing: g.Missing, From: g.From()}
	if g.GuideChannelID != 0 {
		out.Guide = &apiGuideMatch{GuideChannelID: g.GuideChannelID, GuideID: g.GuideID, Guide: g.Guide, Names: g.GuideNames,
			XMLTVID: g.GuideXMLTVID, On: g.GuideOn, ByName: g.ByName, ByHand: g.ByHand}
		if out.Guide.Names == nil {
			out.Guide.Names = []string{}
		}
		out.Guide.Name = g.GuideXMLTVID
		if len(g.GuideNames) > 0 && g.GuideNames[0] != "" {
			out.Guide.Name = g.GuideNames[0]
		}
	}
	if g.Chosen {
		out.Chosen = &apiGuideChosen{None: g.From() == "none", ChannelID: g.UseChannel, Name: g.UseName, Source: g.UseSource,
			ChosenAt: g.ChosenAt.UTC().Format(time.RFC3339)}
		if g.ByHand {
			out.Chosen.GuideChannelID = g.GuideChannelID
		}
	}
	return out
}

// suggestFor is what a channel's suggestions are worked out from.
func suggestFor(g store.ChannelGuide) guide.SuggestFor {
	return guide.SuggestFor{ChannelID: g.ChannelID, Name: g.Name, XMLTVID: g.XMLTVID, Category: g.Category}
}

// suggestions returns, for each channel, at most limit suggestions; none
// if the server cannot read guides.
func (a *Admin) suggestions(ctx context.Context, gs []store.ChannelGuide, limit int) (map[int64][]guide.Option, error) {
	if a.guides == nil || len(gs) == 0 {
		return nil, nil
	}
	fors := make([]guide.SuggestFor, len(gs))
	for i, g := range gs {
		fors[i] = suggestFor(g)
	}
	return a.guides.Suggest(ctx, fors, limit)
}

func (a *Admin) apiChannelGuides(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	ctx := r.Context()
	q := r.URL.Query()
	show := q.Get("show")
	if show == "" {
		show = store.ShowAll
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	limit, err := strconv.Atoi(q.Get("limit"))
	if err != nil || limit <= 0 {
		limit = channelsPage
	}
	limit = min(limit, maxChannelsAPI)
	if _, ok := map[string]bool{store.ShowAll: true, store.ShowWithoutGuide: true, store.ShowByName: true, store.ShowByHand: true}[show]; !ok {
		apiError(w, http.StatusBadRequest, "bad_show", "show is one of all, without, by_name and by_hand.")
		return
	}
	gs, total, err := a.store.ChannelGuides(ctx, show, q.Get("find"), offset, limit)
	if a.editAnswer(w, err) {
		return
	}
	counts, err := a.store.ChannelGuideCounts(ctx)
	if a.editAnswer(w, err) {
		return
	}
	var sugg map[int64][]guide.Option
	if q.Get("suggest") == "true" {
		if sugg, err = a.suggestions(ctx, gs, 3); a.editAnswer(w, err) {
			return
		}
	}
	now := a.now()
	out := make([]apiChannelGuide, len(gs))
	for i, g := range gs {
		out[i] = channelGuideBody(g)
		for _, o := range sugg[g.ChannelID] {
			body, err := a.optionBody(ctx, o, now, false)
			if a.editAnswer(w, err) {
				return
			}
			out[i].Suggestions = append(out[i].Suggestions, body)
		}
	}
	writeJSON(w, http.StatusOK, struct {
		Channels []apiChannelGuide `json:"channels"`
		Total    int               `json:"total"`
		Counts   map[string]int    `json:"counts"`
	}{out, total, counts})
}

func (a *Admin) apiChannelGuide(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	id, err := pathID(r)
	if a.editAnswer(w, err) {
		return
	}
	a.writeChannelGuide(w, r, id)
}

// writeChannelGuide answers with where a channel's programmes come from,
// what is on now and next, and suggestions.
func (a *Admin) writeChannelGuide(w http.ResponseWriter, r *http.Request, id int64) {
	ctx := r.Context()
	g, err := a.store.ChannelGuideOf(ctx, id)
	if a.editAnswer(w, choiceError(err)) {
		return
	}
	now := a.now()
	out := channelGuideBody(g)
	ps, err := a.guideNowNext(ctx, g, now)
	if a.editAnswer(w, err) {
		return
	}
	out.Programmes = programmesBody(ps)
	sugg, err := a.suggestions(ctx, []store.ChannelGuide{g}, 0)
	if a.editAnswer(w, err) {
		return
	}
	out.Suggestions = []apiGuideOption{}
	for _, o := range sugg[g.ChannelID] {
		body, err := a.optionBody(ctx, o, now, true)
		if a.editAnswer(w, err) {
			return
		}
		out.Suggestions = append(out.Suggestions, body)
	}
	writeJSON(w, http.StatusOK, out)
}

// apiGuideChoice is the body of a request that chooses a channel's guide:
// one of a guide's channel, another channel, or none.
type apiGuideChoice struct {
	GuideChannelID *int64 `json:"guide_channel_id"`
	ChannelID      *int64 `json:"channel_id"`
	None           *bool  `json:"none"`
}

func (a *Admin) apiChooseGuide(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	id, err := pathID(r)
	if a.editAnswer(w, err) {
		return
	}
	var body apiGuideChoice
	if !readJSON(w, r, &body) {
		return
	}
	c := store.GuideChoice{ChannelID: id}
	given := 0
	if body.GuideChannelID != nil {
		given++
		c.GuideChannelID = *body.GuideChannelID
	}
	if body.ChannelID != nil {
		given++
		c.UseChannel = *body.ChannelID
	}
	if body.None != nil && *body.None {
		given++
	}
	if given != 1 || body.GuideChannelID != nil && *body.GuideChannelID <= 0 || body.ChannelID != nil && *body.ChannelID <= 0 {
		apiError(w, http.StatusBadRequest, "bad_choice", "Give one of guide_channel_id, channel_id, or none: true.")
		return
	}
	if a.editAnswer(w, a.chooseGuide(r.Context(), c)) {
		return
	}
	a.writeChannelGuide(w, r, id)
}

func (a *Admin) apiUnchooseGuide(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	id, err := pathID(r)
	if a.editAnswer(w, err) {
		return
	}
	if a.editAnswer(w, a.unchooseGuide(r.Context(), id)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *Admin) apiGuideOptions(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	ctx := r.Context()
	find := strings.TrimSpace(r.URL.Query().Get("find"))
	if find == "" {
		apiError(w, http.StatusBadRequest, "bad_find", "Say what to find: find=, a part of a name or a guide ID.")
		return
	}
	if a.guides == nil {
		writeJSON(w, http.StatusOK, struct {
			Options []apiGuideOption `json:"options"`
		}{[]apiGuideOption{}})
		return
	}
	found, err := a.guides.Search(ctx, find, 0)
	if a.editAnswer(w, err) {
		return
	}
	now := a.now()
	out := make([]apiGuideOption, len(found))
	for i, o := range found {
		if out[i], err = a.optionBody(ctx, o, now, i < optionsWithProgrammes); a.editAnswer(w, err) {
			return
		}
	}
	writeJSON(w, http.StatusOK, struct {
		Options []apiGuideOption `json:"options"`
	}{out})
}

// The pages.

// optionRow is something a channel's guide can be chosen from, as a page
// shows it.
type optionRow struct {
	guide.Option
	Programmes []store.Programme
	Current    bool // the channel's guide comes from it now
}

// Value names the option in a form.
func (o optionRow) Value() string {
	if o.ChannelID != 0 {
		return "channel:" + strconv.FormatInt(o.ChannelID, 10)
	}
	return "guide:" + strconv.FormatInt(o.GuideChannelID, 10)
}

// Where says where the option is from: its guide, or its source.
func (o optionRow) Where() string {
	if o.ChannelID != 0 {
		return "the guide of " + o.Source + "'s channel"
	}
	return o.Guide
}

// Waits says that Overscan holds none of the option's programmes yet.
func (o optionRow) Waits() bool { return o.ChannelID == 0 && !o.Kept }

// channelRow is a channel on the list of channels and their guides.
type channelRow struct {
	G           store.ChannelGuide
	FromText    string
	Programmes  []store.Programme
	Suggestions []optionRow
}

// guideChannelsData is what the list of channels and their guides shows.
type guideChannelsData struct {
	Show      string
	Find      string
	Counts    map[string]int
	Rows      []channelRow
	Total     int
	Offset    int
	CanSearch bool // the server reads guides, so it can suggest and search
	CSRF      string
	Now       time.Time
}

// URL returns the list's address, showing another list from offset.
func (d guideChannelsData) URL(show string, offset int) string {
	return "/admin/guides/channels?" + d.query(show, offset)
}

func (d guideChannelsData) query(show string, offset int) string {
	q := url.Values{"show": {show}}
	if d.Find != "" {
		q.Set("find", d.Find)
	}
	if offset > 0 {
		q.Set("offset", strconv.Itoa(offset))
	}
	return q.Encode()
}

// Here is the list's own address, to come back to.
func (d guideChannelsData) Here() string { return d.URL(d.Show, d.Offset) }

// AllURL is the list's address without what it finds.
func (d guideChannelsData) AllURL() string {
	d.Find = ""
	return d.URL(d.Show, 0)
}

// First and Last are the places in the list of the page's first and last
// channels, counted from 1.
func (d guideChannelsData) First() int { return d.Offset + 1 }
func (d guideChannelsData) Last() int  { return d.Offset + len(d.Rows) }

// IsList says that the page is the list of channels.
func (guideChannelsData) IsList() bool { return true }

// Query is what the list's forms send to come back to it.
func (d guideChannelsData) Query() string { return d.query(d.Show, d.Offset) }

// Next is the offset of the next page, 0 if there is none.
func (d guideChannelsData) Next() int {
	if d.Offset+len(d.Rows) < d.Total {
		return d.Offset + channelsPage
	}
	return 0
}

// Previous is the offset of the page before, -1 if there is none.
func (d guideChannelsData) Previous() int {
	if d.Offset == 0 {
		return -1
	}
	return max(0, d.Offset-channelsPage)
}

func (a *Admin) guideChannelsPage(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	a.showGuideChannels(w, r, s, http.StatusOK, view{})
}

func (a *Admin) showGuideChannels(w http.ResponseWriter, r *http.Request, s store.AdminSession, status int, v view) {
	ctx := r.Context()
	q := r.URL.Query()
	if r.Method == http.MethodPost {
		q, _ = url.ParseQuery(r.PostFormValue("list"))
	}
	d := guideChannelsData{Show: q.Get("show"), Find: strings.TrimSpace(q.Get("find")), CanSearch: a.guides != nil,
		CSRF: s.CSRFToken, Now: a.now()}
	if _, ok := map[string]bool{store.ShowWithoutGuide: true, store.ShowByName: true, store.ShowByHand: true}[d.Show]; !ok {
		d.Show = store.ShowWithoutGuide
	}
	d.Offset, _ = strconv.Atoi(q.Get("offset"))
	d.Offset = max(d.Offset, 0)
	err := a.fillGuideChannels(ctx, &d)
	if err != nil {
		a.log.Error("cannot show the channels' guides", "error", err)
		a.message(w, r, http.StatusInternalServerError, "Something went wrong", "The server's log says what.", &s)
		return
	}
	v.Title, v.Section, v.Session, v.Data = "Channels and their guides · Guides", "guides", &s, d
	a.render(w, r, status, "guide-channels", v)
}

func (a *Admin) fillGuideChannels(ctx context.Context, d *guideChannelsData) error {
	var err error
	if d.Counts, err = a.store.ChannelGuideCounts(ctx); err != nil {
		return err
	}
	gs, total, err := a.store.ChannelGuides(ctx, d.Show, d.Find, d.Offset, channelsPage)
	if err != nil {
		return err
	}
	d.Total = total
	var sugg map[int64][]guide.Option
	if d.Show == store.ShowWithoutGuide {
		if sugg, err = a.suggestions(ctx, gs, shownSuggestions); err != nil {
			return err
		}
	}
	for _, g := range gs {
		row := channelRow{G: g, FromText: guideFromText(g)}
		if row.Programmes, err = a.guideNowNext(ctx, g, d.Now); err != nil {
			return err
		}
		for _, o := range sugg[g.ChannelID] {
			row.Suggestions = append(row.Suggestions, optionRow{Option: o})
		}
		d.Rows = append(d.Rows, row)
	}
	return nil
}

// guideChoiceData is what a channel's page of its guide shows.
type guideChoiceData struct {
	G           store.ChannelGuide
	FromText    string
	Programmes  []store.Programme
	Suggestions []optionRow
	Find        string
	Found       []optionRow
	Searched    bool
	CanSearch   bool
	Back        string // the page that the administrator came from, "" if none
	BackText    string
	CSRF        string
	Now         time.Time
}

// IsList says that the page is a channel's own, not the list.
func (guideChoiceData) IsList() bool { return false }

// Here is the page's own address, keeping where it came back to.
func (d guideChoiceData) Here() string {
	out := "/admin/guides/channels/" + strconv.FormatInt(d.G.ChannelID, 10)
	if d.Back != "" {
		out += "?" + url.Values{"back": {d.Back}}.Encode()
	}
	return out
}

// safeBack returns the address of an admin page to go back to, "" if back
// is not one.
func safeBack(back string) string {
	if !strings.HasPrefix(back, "/admin/") || strings.ContainsAny(back, "\\\r\n") || strings.HasPrefix(back, "//") {
		return ""
	}
	return back
}

// backText names the page that back leads to.
func backText(back string) string {
	switch {
	case strings.HasPrefix(back, "/admin/lineup"):
		return "Live TV"
	case strings.HasPrefix(back, "/admin/guides/channels"):
		return "Channels and their guides"
	}
	return "Back"
}

func (a *Admin) guideChoicePage(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		a.notFound(w, r)
		return
	}
	a.showGuideChoice(w, r, s, id, http.StatusOK, view{})
}

func (a *Admin) showGuideChoice(w http.ResponseWriter, r *http.Request, s store.AdminSession, id int64, status int, v view) {
	ctx := r.Context()
	q := r.URL.Query()
	back, find := q.Get("back"), q.Get("find")
	if r.Method == http.MethodPost {
		back, find = r.PostFormValue("back"), r.PostFormValue("find")
	}
	g, err := a.store.ChannelGuideOf(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		a.notFound(w, r)
		return
	}
	d := guideChoiceData{G: g, FromText: guideFromText(g), Find: strings.TrimSpace(find), CanSearch: a.guides != nil,
		Back: safeBack(back), CSRF: s.CSRFToken, Now: a.now()}
	d.BackText = backText(d.Back)
	if err == nil {
		err = a.fillGuideChoice(ctx, &d)
	}
	if err != nil {
		a.log.Error("cannot show a channel's guide", "error", err)
		a.message(w, r, http.StatusInternalServerError, "Something went wrong", "The server's log says what.", &s)
		return
	}
	v.Title, v.Section, v.Session, v.Data = "The guide of "+g.Name+" · Guides", "guides", &s, d
	if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-Target") == "guide-found" {
		a.fragment(w, r, status, "guide-channels", "guide-found", v)
		return
	}
	a.render(w, r, status, "guide-channels", v)
}

func (a *Admin) fillGuideChoice(ctx context.Context, d *guideChoiceData) error {
	var err error
	if d.Programmes, err = a.guideNowNext(ctx, d.G, d.Now); err != nil {
		return err
	}
	current := func(o guide.Option) bool {
		return o.ChannelID != 0 && o.ChannelID == d.G.UseChannel ||
			o.GuideChannelID != 0 && o.GuideChannelID == d.G.GuideChannelID
	}
	rows := func(options []guide.Option, withProgrammes int) ([]optionRow, error) {
		var out []optionRow
		for i, o := range options {
			row := optionRow{Option: o, Current: current(o)}
			if i < withProgrammes {
				if row.Programmes, err = a.optionNowNext(ctx, o, d.Now); err != nil {
					return nil, err
				}
			}
			out = append(out, row)
		}
		return out, nil
	}
	if a.guides == nil {
		return nil
	}
	sugg, err := a.suggestions(ctx, []store.ChannelGuide{d.G}, 0)
	if err != nil {
		return err
	}
	if d.Suggestions, err = rows(sugg[d.G.ChannelID], len(sugg[d.G.ChannelID])); err != nil {
		return err
	}
	if d.Find != "" {
		d.Searched = true
		found, err := a.guides.Search(ctx, d.Find, 0)
		if err != nil {
			return err
		}
		if d.Found, err = rows(found, optionsWithProgrammes); err != nil {
			return err
		}
	}
	return nil
}

// parseChoice reads a choice from a form: "guide:N", "channel:N", "none",
// or "auto" to take a choice back.
func parseChoice(channelID int64, value string) (c store.GuideChoice, auto bool, ok bool) {
	c.ChannelID = channelID
	switch kind, n, _ := strings.Cut(value, ":"); kind {
	case "none":
		return c, false, n == ""
	case "auto":
		return c, true, n == ""
	case "guide", "channel":
		id, err := strconv.ParseInt(n, 10, 64)
		if err != nil || id <= 0 {
			return c, false, false
		}
		if kind == "guide" {
			c.GuideChannelID = id
		} else {
			c.UseChannel = id
		}
		return c, false, true
	}
	return c, false, false
}

// chooseGuideForm chooses a channel's guide from its page, or from the list
// of channels, whichever sent it.
func (a *Admin) chooseGuideForm(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		a.notFound(w, r)
		return
	}
	ctx := r.Context()
	g, err := a.store.ChannelGuideOf(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		a.notFound(w, r)
		return
	}
	c, auto, ok := parseChoice(id, r.PostFormValue("choice"))
	var done string
	switch {
	case err != nil:
	case !ok:
		err = badEdit(http.StatusBadRequest, "bad_choice", "Choose a guide for the channel.")
	case auto:
		if err = a.unchooseGuide(ctx, id); err == nil {
			done = g.Name + "'s guide is found as before: its source's, or a guide of yours that matches it. The page shows it once the guides are matched again, in a moment."
		}
	default:
		if err = a.chooseGuide(ctx, c); err == nil {
			done = choiceDoneText(ctx, a, g.Name, c)
		}
	}
	v := view{Notice: done}
	status := http.StatusOK
	var bad *editError
	switch {
	case errors.As(err, &bad):
		v, status = view{Error: bad.text}, bad.status
	case err != nil:
		a.log.Error("cannot choose a channel's guide", "error", err)
		v, status = view{Error: "Something went wrong; the server's log says what."}, http.StatusInternalServerError
	}
	if list, ok := r.PostForm["list"]; ok {
		here := "/admin/guides/channels"
		if q, perr := url.ParseQuery(list[0]); perr == nil && len(q) > 0 {
			here += "?" + q.Encode()
		}
		if r.Header.Get("HX-Request") != "true" && err == nil {
			http.Redirect(w, r, here, http.StatusSeeOther)
			return
		}
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("HX-Replace-Url", here)
		}
		a.showGuideChannels(w, r, s, status, v)
		return
	}
	if r.Header.Get("HX-Request") != "true" && err == nil {
		http.Redirect(w, r, guideChoiceData{G: store.ChannelGuide{ChannelID: id}, Back: safeBack(r.PostFormValue("back"))}.Here(), http.StatusSeeOther)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Replace-Url", guideChoiceData{G: store.ChannelGuide{ChannelID: id}, Back: safeBack(r.PostFormValue("back"))}.Here())
	}
	a.showGuideChoice(w, r, s, id, status, v)
}

// choiceDoneText says what a choice did.
func choiceDoneText(ctx context.Context, a *Admin, name string, c store.GuideChoice) string {
	switch {
	case c.None():
		return name + " shows no guide."
	case c.UseChannel != 0:
		if other, err := a.store.ChannelGuideOf(ctx, c.UseChannel); err == nil {
			return name + " shows the guide of " + other.Name + ", from " + other.Source + "."
		}
	case c.GuideChannelID != 0:
		if g, err := a.store.ChannelGuideOf(ctx, c.ChannelID); err == nil && g.GuideChannelID != 0 {
			text := fmt.Sprintf("%s shows %s.", name, strings.TrimSuffix(guideFromText(g), ", chosen by hand."))
			if !g.GuideOn {
				return text
			}
			if gd, err := a.store.Guide(ctx, g.GuideID); err == nil && gd.Waiting > 0 {
				text += " Overscan reads its programmes from " + g.Guide + " now; they show once it has."
			}
			return text
		}
	}
	return "Chose " + name + "'s guide."
}
