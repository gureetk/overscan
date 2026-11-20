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

	"overscan.gureet.ca/internal/rules"
	"overscan.gureet.ca/internal/store"
)

// A lineup's rules (D107), on the Live TV page's Rules view and in the API:
// ordered rules that match channels and show, hide, rename, regroup, number
// or give a logo to them, show another channel's guide on them or move
// their guide's times (D108), or merge their copies into one channel
// (D113), after every refresh. The administrator's own edits of a channel
// win over them.

// ruleError turns the store's errors about rules into mistakes that the
// request can put right.
func ruleError(err error) error {
	var bad *rules.Error
	switch {
	case errors.As(err, &bad):
		return badEdit(http.StatusBadRequest, "bad_rule", "%s", bad.Text)
	case errors.Is(err, store.ErrNotFound):
		return badEdit(http.StatusNotFound, "not_found", "There is no such rule.")
	case errors.Is(err, store.ErrTooManyRules):
		return badEdit(http.StatusConflict, "too_many_rules", "A lineup has at most %d rules.", rules.MaxRules)
	}
	return err
}

// ruleSources returns the names of the sources by their IDs.
func (a *Admin) ruleSources(ctx context.Context) (map[int64]string, []store.Source, error) {
	list, err := a.store.Sources(ctx)
	if err != nil {
		return nil, nil, err
	}
	names := make(map[int64]string, len(list))
	for _, src := range list {
		names[src.ID] = src.Name
	}
	return names, list, nil
}

// describeRule says what a rule does, as the page lists it, such as
// "Hides channels from main named like “*Backup*”."
func describeRule(r rules.Rule, sources map[int64]string) string {
	var who []string
	if r.Match.Source != 0 {
		name, ok := sources[r.Match.Source]
		if !ok {
			name = "a source that is gone"
		}
		who = append(who, "from "+name)
	}
	if r.Match.Group != "" {
		who = append(who, "in categories like "+quote(r.Match.Group))
	}
	if r.Match.Name != "" {
		who = append(who, "named like "+quote(r.Match.Name))
	}
	if r.Match.Guide != "" {
		who = append(who, "with guide IDs like "+quote(r.Match.Guide))
	}
	if r.Match.Quality != "" {
		who = append(who, "in "+r.Match.Quality)
	}
	what, names := "every channel", "every channel's name"
	if len(who) > 0 {
		what = "channels " + andList(who)
		names = "the names of " + what
	}
	switch r.Action {
	case rules.Include:
		return "Shows " + what + ", even in a hidden group."
	case rules.Exclude:
		return "Hides " + what + "."
	case rules.Rename:
		switch {
		case r.Name != "":
			return "Names " + what + " " + quote(r.Name) + "."
		case r.With == "":
			return "Takes " + quote(r.Replace) + " out of " + names + "."
		}
		return "Replaces " + quote(r.Replace) + " with " + quote(r.With) + " in " + names + "."
	case rules.Regroup:
		return "Moves " + what + " to the group " + quote(r.Group) + "."
	case rules.Renumber:
		return "Numbers " + what + " from " + strconv.Itoa(r.From) + "."
	case rules.Logo:
		return "Gives " + what + " the logo at " + r.Logo + "."
	case rules.Guide:
		switch {
		case r.Channel != "":
			return "Gives " + what + " the guide of the channel named " + quote(r.Channel) + "."
		case r.With == "":
			return "Gives " + what + " the guide of the channel with the same name without " + quote(r.Replace) + "."
		}
		return "Gives " + what + " the guide of the channel with the same name, " + quote(r.Replace) + " replaced by " +
			quote(r.With) + "."
	case rules.Shift:
		return "Shows the guide of " + what + " " + shiftText(r.Minutes) + "."
	case rules.Merge:
		what = "the " + what
		if len(who) == 0 {
			what = "the channels"
		}
		same := "the same guide ID"
		if r.Copies == rules.ByName {
			same = "the same name, without quality words such as HD"
			switch {
			case r.Replace != "" && r.With == "":
				same += ", once " + quote(r.Replace) + " is taken out"
			case r.Replace != "":
				same += ", once " + quote(r.Replace) + " is replaced by " + quote(r.With)
			}
		}
		first := "the first in the lineup"
		if r.Prefer != "" {
			first = "the first in " + r.Prefer + ", else the first in the lineup,"
		}
		return "Merges into one " + what + " that have " + same + ": " + first + " shows, with the others as its backups."
	}
	return "Does nothing to " + what + "."
}

// copiesWord is "copy" or "copies".
func copiesWord(n int) string {
	if n == 1 {
		return "copy"
	}
	return "copies"
}

// shiftText says how far a guide moves, such as "60 minutes later".
func shiftText(minutes int) string {
	when := "later"
	if minutes < 0 {
		minutes, when = -minutes, "earlier"
	}
	return fmt.Sprintf("%d %s %s", minutes, plural(minutes, "minute"), when)
}

// quote puts text in quotation marks, as a sentence about a rule shows it.
func quote(s string) string { return "“" + s + "”" }

// ruleRow is a rule as the page lists it.
type ruleRow struct {
	store.LineupRule
	Text  string
	First bool
	Last  bool
}

// Effect says what a rule does now: how many channels it matches, or, for
// a merge rule that is on, how many copies it merges into how many
// channels (D113).
func (r ruleRow) Effect() string { return ruleEffect(r.LineupRule) }

func ruleEffect(r store.LineupRule) string {
	switch {
	case r.Action == rules.Merge && !r.Off && r.CopiesMerged == 0:
		return "It finds no copies now."
	case r.Action == rules.Merge && !r.Off:
		return fmt.Sprintf("It merges %s %s into %s %s.", count(r.CopiesMerged), copiesWord(r.CopiesMerged),
			count(r.ChannelsMerged), plural(r.ChannelsMerged, "channel"))
	case r.Matches == 0:
		return "It matches no channel now."
	}
	return fmt.Sprintf("It matches %s %s.", count(r.Matches), plural(r.Matches, "channel"))
}

// rulesData is what the Rules view shows.
type rulesData struct {
	View      lineupView
	Lineups   []store.LineupInfo
	Current   store.LineupInfo
	Rules     []ruleRow
	Sources   []store.Source
	Groups    []store.LineupGroup // the lineup's groups, by name
	Qualities []string
	Actions   []string
	Channels  int // the lineup's channels

	// Draft is what was sent for a rule, or for a new one (DraftID 0),
	// when it could not be saved: its form shows it again.
	Draft   *rules.Rule
	DraftID int64
}

// SeenBy names the accounts that see the lineup shown.
func (d rulesData) SeenBy() string { return andList(d.Current.Accounts) }

// FormRule is what a rule's form shows: the rule, or what was sent for it
// when it could not be saved.
func (d rulesData) FormRule(id int64, r rules.Rule) rules.Rule {
	if d.IsOpen(id) {
		return *d.Draft
	}
	return r
}

// NewRule is what the form for a new rule shows.
func (d rulesData) NewRule() rules.Rule { return d.FormRule(0, rules.Rule{}) }

// IsOpen reports whether what was sent for a rule could not be saved.
func (d rulesData) IsOpen(id int64) bool { return d.Draft != nil && d.DraftID == id }

// ActionName names an action as the page offers it.
func (rulesData) ActionName(action string) string {
	return map[string]string{
		rules.Include:  "Show them",
		rules.Exclude:  "Hide them",
		rules.Rename:   "Rename them",
		rules.Regroup:  "Move them to a group",
		rules.Renumber: "Number them",
		rules.Logo:     "Give them a logo",
		rules.Guide:    "Use another channel's guide",
		rules.Shift:    "Move their guide's times",
		rules.Merge:    "Merge copies into one channel",
	}[action]
}

func (a *Admin) rulesData(ctx context.Context, lv lineupView) (rulesData, error) {
	lineup, err := a.lineupID(ctx)
	if err != nil {
		return rulesData{}, err
	}
	d := rulesData{View: lv, Qualities: rules.QualityTags, Actions: rules.Actions}
	if d.Lineups, err = a.store.Lineups(ctx); err != nil {
		return rulesData{}, err
	}
	for _, l := range d.Lineups {
		if l.ID == lineup {
			d.Current = l
		}
	}
	names, sources, err := a.ruleSources(ctx)
	if err != nil {
		return rulesData{}, err
	}
	d.Sources = sources
	if d.Groups, err = a.store.LineupGroups(ctx, lineup); err != nil {
		return rulesData{}, err
	}
	slices.SortFunc(d.Groups, func(a, b store.LineupGroup) int {
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	list, err := a.store.LineupRules(ctx, lineup)
	if err != nil {
		return rulesData{}, err
	}
	for i, r := range list {
		d.Rules = append(d.Rules, ruleRow{LineupRule: r, Text: describeRule(r.Rule, names), First: i == 0, Last: i == len(list)-1})
	}
	if _, d.Channels, err = a.store.LineupEntries(ctx, lineup, store.EntryQuery{Limit: 1}); err != nil {
		return rulesData{}, err
	}
	return d, nil
}

// rulesPage shows a lineup's rules.
func (a *Admin) rulesPage(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	a.showRules(w, r, s, http.StatusOK, parseLineupView(r.URL.Query()).In(), view{}, nil, 0)
}

func (a *Admin) showRules(w http.ResponseWriter, r *http.Request, s store.AdminSession, status int, lv lineupView, v view,
	draft *rules.Rule, draftID int64) {
	data, err := a.rulesData(r.Context(), lv)
	if err != nil {
		a.log.Error("cannot show the lineup's rules", "error", err)
		a.message(w, r, http.StatusInternalServerError, "Something went wrong", "The server's log says what.", &s)
		return
	}
	data.Draft, data.DraftID = draft, draftID
	v.Title, v.Section, v.Session, v.Data = "Rules · Live TV", "lineup", &s, data
	a.render(w, r, status, "rules", v)
}

// ruleForm reads a rule from the page's form.
func ruleForm(r *http.Request) rules.Rule {
	source, _ := strconv.ParseInt(r.PostFormValue("source"), 10, 64)
	from, _ := strconv.Atoi(strings.TrimSpace(r.PostFormValue("from")))
	rule := rules.Rule{
		Match: rules.Match{Source: source, Group: r.PostFormValue("group"), Name: r.PostFormValue("name"),
			Guide: r.PostFormValue("guide"), Quality: r.PostFormValue("quality")},
		Action: r.PostFormValue("action"),
		Off:    r.PostFormValue("off") == "1",
		Group:  r.PostFormValue("to_group"),
		From:   from,
		Logo:   r.PostFormValue("logo"),
	}
	switch rule.Action {
	case rules.Guide:
		if r.PostFormValue("guide_from") == "replace" {
			rule.Replace, rule.With = r.PostFormValue("guide_replace"), r.PostFormValue("guide_with")
		} else {
			rule.Channel = r.PostFormValue("guide_channel")
		}
	case rules.Shift:
		rule.Minutes, _ = strconv.Atoi(strings.TrimSpace(r.PostFormValue("minutes")))
		if r.PostFormValue("when") == "earlier" {
			rule.Minutes = -rule.Minutes
		}
	case rules.Merge:
		rule.Copies, rule.Prefer = r.PostFormValue("copies"), r.PostFormValue("prefer")
		if rule.Copies == rules.ByName {
			rule.Replace, rule.With = r.PostFormValue("merge_replace"), r.PostFormValue("merge_with")
		}
	default:
		if r.PostFormValue("rename") == "replace" {
			rule.Replace, rule.With = r.PostFormValue("replace"), r.PostFormValue("with")
		} else {
			rule.Name = r.PostFormValue("new_name")
		}
	}
	return rule
}

// rulesAction serves the Rules view's forms: a new rule, a rule saved, and
// a rule moved, turned on or off, or removed.
func (a *Admin) rulesAction(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	ctx := r.Context()
	lv := parseLineupView(backValues(r)).In()
	lineup, err := a.lineupID(ctx)
	if err != nil {
		a.rulesDone(w, r, s, lv, err, "", nil, 0)
		return
	}
	var id int64
	if text := r.PathValue("id"); text != "" {
		if id, err = strconv.ParseInt(text, 10, 64); err != nil || id <= 0 {
			a.notFound(w, r)
			return
		}
	}
	var done string
	var draft *rules.Rule // what was sent, for a new rule or a rule saved
	switch action := r.PathValue("action"); {
	case id == 0 && action == "":
		sent := ruleForm(r)
		draft = &sent
		var newID int64
		if newID, err = a.store.AddLineupRule(ctx, lineup, sent); err == nil {
			done = a.ruleDone(ctx, lineup, newID, "Added the rule.")
			a.log.Info("a lineup's rule added in the admin", "lineup", a.lineupName(ctx, lineup), "action", sent.Action)
		}
	case action == "":
		sent := ruleForm(r)
		draft = &sent
		if err = a.store.SetLineupRule(ctx, lineup, id, sent); err == nil {
			done = a.ruleDone(ctx, lineup, id, "Saved the rule.")
			a.log.Info("a lineup's rule changed in the admin", "lineup", a.lineupName(ctx, lineup), "action", sent.Action)
		}
	case action == "up" || action == "down":
		var rule store.LineupRule
		if rule, err = a.lineupRule(ctx, lineup, id); err == nil {
			to := rule.Position - 1
			if action == "down" {
				to = rule.Position + 1
			}
			if err = a.store.MoveLineupRule(ctx, lineup, id, to); err == nil {
				done = "Moved the rule."
			}
		}
	case action == "on" || action == "off":
		var rule store.LineupRule
		if rule, err = a.lineupRule(ctx, lineup, id); err == nil {
			rule.Off = action == "off"
			if err = a.store.SetLineupRule(ctx, lineup, id, rule.Rule); err == nil {
				done = "Turned the rule on."
				if rule.Off {
					done = "Turned the rule off: it does nothing until it is on again."
				}
				a.log.Info("a lineup's rule turned on or off in the admin", "lineup", a.lineupName(ctx, lineup), "on", !rule.Off)
			}
		}
	case action == "remove":
		if err = a.store.RemoveLineupRule(ctx, lineup, id); err == nil {
			done = "Removed the rule. What it did to channels is undone."
			a.log.Info("a lineup's rule removed in the admin", "lineup", a.lineupName(ctx, lineup))
		}
	default:
		a.notFound(w, r)
		return
	}
	if err == nil {
		draft = nil
	}
	a.rulesDone(w, r, s, lv, ruleError(err), done, draft, id)
}

// lineupRule returns a rule of a lineup.
func (a *Admin) lineupRule(ctx context.Context, lineup, id int64) (store.LineupRule, error) {
	list, err := a.store.LineupRules(ctx, lineup)
	if err != nil {
		return store.LineupRule{}, err
	}
	for _, r := range list {
		if r.ID == id {
			return r, nil
		}
	}
	return store.LineupRule{}, store.ErrNotFound
}

// ruleDone says what a rule saved does now: how many channels it matches,
// or what it merges.
func (a *Admin) ruleDone(ctx context.Context, lineup, id int64, done string) string {
	r, err := a.lineupRule(ctx, lineup, id)
	if err != nil {
		return done
	}
	return done + " " + ruleEffect(r)
}

// rulesDone finishes a change from the Rules view, as lineupDone does
// from the channels; a rule that could not be saved keeps its form open,
// with what was sent.
func (a *Admin) rulesDone(w http.ResponseWriter, r *http.Request, s store.AdminSession, lv lineupView, err error, notice string,
	draft *rules.Rule, draftID int64) {
	var bad *editError
	switch {
	case errors.As(err, &bad):
		a.showRules(w, r, s, bad.status, lv, view{Error: bad.text}, draft, draftID)
	case err != nil:
		a.log.Error("cannot change the lineup's rules", "error", err)
		a.showRules(w, r, s, http.StatusInternalServerError, lv, view{Error: "Something went wrong; the server's log says what."}, draft, draftID)
	case r.Header.Get("HX-Request") == "true":
		a.showRules(w, r, s, http.StatusOK, lv, view{Notice: notice}, nil, 0)
	default:
		http.Redirect(w, r, string(lv.RulesURL()), http.StatusSeeOther)
	}
}

// apiRule is a rule as the API shows it.
type apiRule struct {
	ID       int64 `json:"id"`
	Position int   `json:"position"`
	Matches  int   `json:"matches"`
	// For a merge rule: the copies it merges, and the channels it merges
	// them into (D113).
	CopiesMerged   int `json:"copies_merged,omitempty"`
	ChannelsMerged int `json:"channels_merged,omitempty"`
	apiRuleBody
}

// apiRuleBody is a rule as the API takes it.
type apiRuleBody struct {
	Match   apiMatch `json:"match"`
	Action  string   `json:"action"`
	Off     bool     `json:"off"`
	Name    string   `json:"name,omitempty"`
	Replace string   `json:"replace,omitempty"`
	With    string   `json:"with,omitempty"`
	Group   string   `json:"group,omitempty"`
	From    int      `json:"from,omitempty"`
	Logo    string   `json:"logo,omitempty"`
	Channel string   `json:"channel,omitempty"`
	Minutes int      `json:"minutes,omitempty"`
	Copies  string   `json:"copies,omitempty"`
	Prefer  string   `json:"prefer,omitempty"`
}

type apiMatch struct {
	SourceID int64  `json:"source_id,omitempty"`
	Group    string `json:"group,omitempty"`
	Name     string `json:"name,omitempty"`
	Guide    string `json:"guide,omitempty"`
	Quality  string `json:"quality,omitempty"`
}

func ruleBody(r store.LineupRule) apiRule {
	return apiRule{ID: r.ID, Position: r.Position, Matches: r.Matches, CopiesMerged: r.CopiesMerged, ChannelsMerged: r.ChannelsMerged,
		apiRuleBody: apiRuleBody{
			Match: apiMatch{SourceID: r.Match.Source, Group: r.Match.Group, Name: r.Match.Name, Guide: r.Match.Guide,
				Quality: r.Match.Quality},
			Action: r.Action, Off: r.Off, Name: r.Name, Replace: r.Replace, With: r.With, Group: r.Group, From: r.From, Logo: r.Logo,
			Channel: r.Channel, Minutes: r.Minutes, Copies: r.Copies, Prefer: r.Prefer}}
}

func (b apiRuleBody) rule() rules.Rule {
	return rules.Rule{Match: rules.Match{Source: b.Match.SourceID, Group: b.Match.Group, Name: b.Match.Name,
		Guide: b.Match.Guide, Quality: b.Match.Quality}, Action: b.Action, Off: b.Off, Name: b.Name, Replace: b.Replace,
		With: b.With, Group: b.Group, From: b.From, Logo: b.Logo, Channel: b.Channel, Minutes: b.Minutes,
		Copies: b.Copies, Prefer: b.Prefer}
}

func (a *Admin) apiRules(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	lineup, err := a.lineupID(r.Context())
	if a.editAnswer(w, err) {
		return
	}
	list, err := a.store.LineupRules(r.Context(), lineup)
	if a.editAnswer(w, err) {
		return
	}
	out := make([]apiRule, len(list))
	for i, rule := range list {
		out[i] = ruleBody(rule)
	}
	writeJSON(w, http.StatusOK, struct {
		Rules []apiRule `json:"rules"`
	}{out})
}

// writeRule answers with a rule of the lineup.
func (a *Admin) writeRule(w http.ResponseWriter, r *http.Request, status int, lineup, id int64) {
	rule, err := a.lineupRule(r.Context(), lineup, id)
	if a.editAnswer(w, ruleError(err)) {
		return
	}
	writeJSON(w, status, ruleBody(rule))
}

func (a *Admin) apiAddRule(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	var body apiRuleBody
	if !readJSON(w, r, &body) {
		return
	}
	ctx := r.Context()
	lineup, err := a.lineupID(ctx)
	if a.editAnswer(w, err) {
		return
	}
	id, err := a.store.AddLineupRule(ctx, lineup, body.rule())
	if a.editAnswer(w, ruleError(err)) {
		return
	}
	a.log.Info("a lineup's rule added in the admin", "lineup", a.lineupName(ctx, lineup), "action", body.Action)
	a.writeRule(w, r, http.StatusCreated, lineup, id)
}

func (a *Admin) apiSetRule(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	id, err := pathID(r)
	if a.editAnswer(w, err) {
		return
	}
	var body apiRuleBody
	if !readJSON(w, r, &body) {
		return
	}
	ctx := r.Context()
	lineup, err := a.lineupID(ctx)
	if a.editAnswer(w, err) {
		return
	}
	if a.editAnswer(w, ruleError(a.store.SetLineupRule(ctx, lineup, id, body.rule()))) {
		return
	}
	a.log.Info("a lineup's rule changed in the admin", "lineup", a.lineupName(ctx, lineup), "action", body.Action)
	a.writeRule(w, r, http.StatusOK, lineup, id)
}

func (a *Admin) apiRemoveRule(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	id, err := pathID(r)
	if a.editAnswer(w, err) {
		return
	}
	ctx := r.Context()
	lineup, err := a.lineupID(ctx)
	if a.editAnswer(w, err) {
		return
	}
	if a.editAnswer(w, ruleError(a.store.RemoveLineupRule(ctx, lineup, id))) {
		return
	}
	a.log.Info("a lineup's rule removed in the admin", "lineup", a.lineupName(ctx, lineup))
	w.WriteHeader(http.StatusNoContent)
}

func (a *Admin) apiMoveRule(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	id, err := pathID(r)
	if a.editAnswer(w, err) {
		return
	}
	var body struct {
		Position int `json:"position"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.Position < 1 {
		apiError(w, http.StatusBadRequest, "bad_position", "A rule's place counts from 1.")
		return
	}
	ctx := r.Context()
	lineup, err := a.lineupID(ctx)
	if a.editAnswer(w, err) {
		return
	}
	if a.editAnswer(w, ruleError(a.store.MoveLineupRule(ctx, lineup, id, body.Position))) {
		return
	}
	a.writeRule(w, r, http.StatusOK, lineup, id)
}
