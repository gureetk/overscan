// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"overscan.gureet.ca/internal/store"
	"overscan.gureet.ca/internal/theme"
)

// Themes (D97): the administrator installs theme files, which the theme
// package checks, and chooses the household's theme, and a profile's own.
// A box shows its profile's theme, else the household's, else Overscan's
// own.

// themeRow is an installed theme as the Settings page lists it.
type themeRow struct {
	store.Theme
	// Colours are the theme's ground, text, accent and focus, for its
	// swatch; Brand the name it shows beside the mark.
	Ground, Text, Accent, Focus string
	Brand                       string
	// UsedBy says who has it: the household, and profiles by name.
	UsedBy string
}

// themeRows returns the installed themes, as the page lists them, with who
// has each: the household, whose theme is household, and its profiles.
func (a *Admin) themeRows(ctx context.Context, household int64, profiles []store.Profile) ([]themeRow, error) {
	list, err := a.store.Themes(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]themeRow, 0, len(list))
	for _, th := range list {
		var users []string
		if th.ID == household {
			users = append(users, "the household")
		}
		for _, p := range profiles {
			if p.ThemeID == th.ID {
				users = append(users, p.Name)
			}
		}
		row := themeRow{Theme: th, Ground: "#000000", Text: "#ffffff", Accent: "#3dd9c1", Focus: "#ffffff"}
		if t, err := theme.Parse(th.Data); err == nil {
			row.Ground, row.Text, row.Brand = t.Background, t.Text, t.Brand
			row.Focus = t.Text
			if t.Focus != "" {
				row.Focus = t.Focus
			}
			if t.Accent != "" {
				row.Accent = t.Accent
			}
		}
		switch len(users) {
		case 0:
		case 1:
			row.UsedBy = users[0]
		default:
			row.UsedBy = strings.Join(users[:len(users)-1], ", ") + " and " + users[len(users)-1]
		}
		out = append(out, row)
	}
	return out, nil
}

// installTheme checks a theme file and installs it, or replaces the
// installed theme of its name.
func (a *Admin) installTheme(ctx context.Context, data []byte) (store.Theme, bool, error) {
	t, err := theme.Parse(data)
	var refused *theme.Error
	if errors.As(err, &refused) {
		return store.Theme{}, false, badEdit(http.StatusBadRequest, "bad_theme", "%s", refused.Text)
	}
	if err != nil {
		return store.Theme{}, false, err
	}
	th, replaced, err := a.store.InstallTheme(ctx, t.Name, t.JSON(), a.now())
	if err != nil {
		return store.Theme{}, false, err
	}
	a.log.Info("a theme installed in the admin", "theme", th.Name, "replaced", replaced)
	return th, replaced, nil
}

// themeID reads a theme's ID from a form or a body: 0 for Overscan's own.
func themeID(v string) (int64, error) {
	if v == "" || v == "0" {
		return 0, nil
	}
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil || id < 0 {
		return 0, badEdit(http.StatusBadRequest, "no_such_theme", "Choose a theme.")
	}
	return id, nil
}

// themeStoreError turns the store's errors about a theme into mistakes the
// request can put right.
func themeStoreError(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return badEdit(http.StatusNotFound, "no_such_theme", "There is no such theme.")
	}
	return err
}

// setHouseholdTheme gives the household's boxes a theme, or with 0
// Overscan's own.
func (a *Admin) setHouseholdTheme(ctx context.Context, id int64) error {
	if err := a.store.SetAccountTheme(ctx, householdAccount, id); err != nil {
		return themeStoreError(err)
	}
	a.log.Info("the household's theme chosen in the admin", "theme", id)
	return nil
}

// setProfileTheme gives one of the household's profiles a theme of its
// own, or with 0 the household's.
func (a *Admin) setProfileTheme(ctx context.Context, profileID, id int64) error {
	p, err := a.householdProfile(ctx, profileID)
	if err != nil {
		var bad *deviceError
		if errors.As(err, &bad) {
			return badEdit(bad.status, bad.code, "%s", bad.text)
		}
		return err
	}
	if err := a.store.SetProfileTheme(ctx, p.ID, id); err != nil {
		return themeStoreError(err)
	}
	a.log.Info("a profile's theme chosen in the admin", "profile", p.Name, "theme", id)
	return nil
}

// removeTheme removes an installed theme; what used it shows the
// household's theme, or Overscan's own.
func (a *Admin) removeTheme(ctx context.Context, id int64) (store.Theme, error) {
	th, err := a.store.Theme(ctx, id)
	if err != nil {
		return store.Theme{}, themeStoreError(err)
	}
	if err := a.store.RemoveTheme(ctx, id); err != nil {
		return store.Theme{}, themeStoreError(err)
	}
	a.log.Info("a theme removed in the admin", "theme", th.Name)
	return th, nil
}

// The Settings page's forms.

// themeUpload reads the theme file that the Settings page's form sends.
func themeUpload(r *http.Request) ([]byte, error) {
	f, _, err := r.FormFile("file")
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return nil, badEdit(http.StatusRequestEntityTooLarge, "bad_theme", "That file is too large to be a theme: a theme file is a few lines of text.")
	}
	if err != nil {
		return nil, badEdit(http.StatusBadRequest, "bad_theme", "Choose a theme file.")
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, theme.MaxFileBytes+1))
}

// themeForm installs a theme from the Settings page.
func (a *Admin) themeForm(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	data, err := themeUpload(r)
	if err != nil {
		a.settingsDone(w, r, s, err, "")
		return
	}
	th, replaced, err := a.installTheme(r.Context(), data)
	done := "Installed the theme " + th.Name + ". Choose it for the household, or for a profile."
	if replaced {
		done = "Replaced the theme " + th.Name + ". Boxes that show it show the new one when they next load the portal, or within five minutes."
	}
	a.settingsDone(w, r, s, err, done)
}

// themeChoiceForm chooses the household's theme, or a profile's, from the
// Settings page.
func (a *Admin) themeChoiceForm(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	ctx := r.Context()
	id, err := themeID(r.PostFormValue("theme"))
	if err != nil {
		a.settingsDone(w, r, s, err, "")
		return
	}
	if p := r.PostFormValue("profile"); p != "" {
		profileID, perr := strconv.ParseInt(p, 10, 64)
		if perr != nil {
			a.settingsDone(w, r, s, badEdit(http.StatusBadRequest, "no_such_profile", "Choose a profile."), "")
			return
		}
		a.settingsDone(w, r, s, a.setProfileTheme(ctx, profileID, id), "Chose the profile's theme. Its boxes show it within five minutes.")
		return
	}
	a.settingsDone(w, r, s, a.setHouseholdTheme(ctx, id), "Chose the household's theme. Boxes show it within five minutes.")
}

// themeRemoveForm removes a theme from the Settings page.
func (a *Admin) themeRemoveForm(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		a.notFound(w, r)
		return
	}
	th, err := a.removeTheme(r.Context(), id)
	a.settingsDone(w, r, s, err, "Removed the theme "+th.Name+".")
}

// fileNameForm is what a downloaded theme's file name keeps of its name.
var fileNameForm = regexp.MustCompile(`[^A-Za-z0-9-]+`)

// themeFile downloads an installed theme's file, to change and install
// again.
func (a *Admin) themeFile(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	id, err := strconv.ParseInt(strings.TrimSuffix(r.PathValue("file"), ".json"), 10, 64)
	if err != nil || !strings.HasSuffix(r.PathValue("file"), ".json") {
		a.notFound(w, r)
		return
	}
	th, err := a.store.Theme(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		a.notFound(w, r)
		return
	}
	if err != nil {
		a.log.Error("cannot read a theme", "error", err)
		a.message(w, r, http.StatusInternalServerError, "Something went wrong", "The server's log says what.", &s)
		return
	}
	name := strings.Trim(fileNameForm.ReplaceAllString(strings.ToLower(th.Name), "-"), "-")
	if name == "" {
		name = "theme"
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.json"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(th.Data)
}

// The API.

// apiTheme is an installed theme as the API describes it.
type apiTheme struct {
	ID   int64    `json:"id"`
	Name string   `json:"name"`
	Uses []string `json:"uses"`
	// File is the theme file, as Overscan keeps it.
	File string `json:"file"`
}

func themeBody(th store.Theme) apiTheme {
	uses := th.Uses
	if uses == nil {
		uses = []string{}
	}
	return apiTheme{ID: th.ID, Name: th.Name, Uses: uses, File: string(th.Data)}
}

func (a *Admin) apiThemes(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	list, err := a.store.Themes(r.Context())
	if err != nil {
		a.apiInternal(w, "cannot list the themes", err)
		return
	}
	acct, err := a.store.Account(r.Context(), householdAccount)
	if err != nil {
		a.apiInternal(w, "cannot list the themes", err)
		return
	}
	out := make([]apiTheme, len(list))
	for i, th := range list {
		out[i] = themeBody(th)
	}
	writeJSON(w, http.StatusOK, struct {
		Household int64      `json:"household"`
		Themes    []apiTheme `json:"themes"`
	}{acct.ThemeID, out})
}

// apiInstallTheme installs the theme file that is the request's body.
func (a *Admin) apiInstallTheme(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		apiError(w, http.StatusUnsupportedMediaType, "not_json", "Send the theme file as application/json.")
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, theme.MaxFileBytes+1))
	if err != nil {
		apiError(w, http.StatusBadRequest, "bad_theme", "The body could not be read.")
		return
	}
	th, replaced, err := a.installTheme(r.Context(), data)
	if a.apiFailed(w, err) {
		return
	}
	status := http.StatusCreated
	if replaced {
		status = http.StatusOK
	}
	writeJSON(w, status, themeBody(th))
}

func (a *Admin) apiRemoveTheme(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	id, err := pathID(r)
	if err != nil {
		apiError(w, http.StatusNotFound, "no_such_theme", "There is no such theme.")
		return
	}
	if _, err := a.removeTheme(r.Context(), id); a.apiFailed(w, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// apiSetTheme chooses the household's theme: {"theme_id": 0} for
// Overscan's own.
func (a *Admin) apiSetTheme(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	var body struct {
		ThemeID *int64 `json:"theme_id"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.ThemeID == nil || *body.ThemeID < 0 {
		apiError(w, http.StatusBadRequest, "bad_json", "Give theme_id: a theme's ID, or 0 for Overscan's own.")
		return
	}
	if a.apiFailed(w, a.setHouseholdTheme(r.Context(), *body.ThemeID)) {
		return
	}
	a.apiThemes(w, r, s)
}
