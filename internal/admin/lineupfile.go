// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"overscan.gureet.ca/internal/lineupfile"
	"overscan.gureet.ca/internal/store"
)

// Lineup files (D112): the admin exports every lineup to one YAML file,
// and imports the lineups of such a file as new lineups, which no account
// sees until the administrator chooses them for one. An import never
// changes a lineup that is there.

// maxUploadBytes caps a form that sends a lineup file: the file, and the
// form's own fields.
const maxUploadBytes = lineupfile.MaxBytes + maxFormBytes

// lineupFileType is the type of a lineup file, as the admin sends it.
const lineupFileType = "application/yaml; charset=utf-8"

// lineupFileName is the name that an exported file is saved as.
func lineupFileName(at time.Time) string {
	return "overscan-lineups-" + at.UTC().Format("2006-01-02") + ".yaml"
}

// sendLineupFile answers with every lineup, as a file to save.
func (a *Admin) sendLineupFile(w http.ResponseWriter, r *http.Request) error {
	f, err := a.store.ExportLineups(r.Context())
	if err != nil {
		return err
	}
	now := a.now()
	data, err := lineupfile.Marshal(f, fmt.Sprintf(
		"Overscan's lineups: the groups made or changed in each, its channels changed by hand, and its rules.\n"+
			"Exported from Overscan %s on %s. Importing the file adds its lineups as new ones.",
		a.version, now.UTC().Format("2 January 2006 at 15:04 UTC")))
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", lineupFileType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+lineupFileName(now)+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
	a.log.Info("the lineups exported", "lineups", len(f.Lineups), "bytes", len(data))
	if !lineupfile.Fits(data) {
		a.log.Warn("the lineups' file is larger than an import takes; split it into files of fewer lineups to import it",
			"bytes", len(data), "most", lineupfile.MaxBytes)
	}
	return nil
}

// importLineups adds the lineups of a file.
func (a *Admin) importLineups(ctx context.Context, data []byte) ([]store.LineupImport, error) {
	f, err := lineupfile.Parse(data)
	if lineupfile.IsError(err) {
		return nil, badEdit(http.StatusBadRequest, "bad_file", "%s", err.Error())
	}
	if err != nil {
		return nil, err
	}
	got, err := a.store.ImportLineups(ctx, f)
	if errors.Is(err, store.ErrTooManyLineups) {
		return nil, badEdit(http.StatusConflict, "too_many_lineups",
			"The file holds %d %s, and a server has at most %d. Remove some lineups first, or take some out of the file.",
			len(f.Lineups), plural(len(f.Lineups), "lineup"), store.MaxLineups)
	}
	if err != nil {
		return nil, err
	}
	for _, l := range got {
		a.log.Info("a lineup imported in the admin", "lineup", l.Name, "groups", l.Groups, "channels", l.Channels,
			"rules", l.Rules, "left_out", l.LeftOut.Total())
	}
	return got, nil
}

// importNotice says what an import did, in a sentence or three.
func importNotice(got []store.LineupImport) string {
	names := make([]string, len(got))
	var left store.LeftOut
	for i, l := range got {
		names[i] = l.Name
		left.Categories += l.LeftOut.Categories
		left.Channels += l.LeftOut.Channels
		left.Backups += l.LeftOut.Backups
		left.Rules += l.LeftOut.Rules
	}
	them := "them"
	if len(got) == 1 {
		them = "it"
	}
	text := "Added the " + plural(len(got), "lineup") + " " + andList(names) + " from the file. No account sees " +
		them + " yet: choose " + them + " for an account on the Accounts page."
	if left.Total() == 0 {
		return text
	}
	var parts []string
	for _, p := range []struct {
		n    int
		what string
	}{{left.Channels, "channel"}, {left.Backups, "backup"}, {left.Rules, "rule"}, {left.Categories, "category"}} {
		if p.n > 0 {
			word := plural(p.n, p.what)
			if p.what == "category" && p.n != 1 {
				word = "categories"
			}
			parts = append(parts, count(p.n)+" "+word)
		}
	}
	return text + " Left out, since this server has no such source or channel: " + andList(parts) + "."
}

// readLineupUpload reads the lineup file that the Live TV page's form
// sends.
func readLineupUpload(r *http.Request) ([]byte, error) {
	f, _, err := r.FormFile("file")
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return nil, badEdit(http.StatusRequestEntityTooLarge, "bad_file", "That file is too large to be a lineup file.")
	}
	if err != nil {
		return nil, badEdit(http.StatusBadRequest, "bad_file", "Choose a lineup file.")
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, lineupfile.MaxBytes+1))
}

// exportLineupsForm serves every lineup as a file, from the Live TV page.
func (a *Admin) exportLineupsForm(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	if err := a.sendLineupFile(w, r); err != nil {
		a.log.Error("cannot export the lineups", "error", err)
		a.message(w, r, http.StatusInternalServerError, "Something went wrong", "The server's log says what.", &s)
	}
}

// importLineupsForm adds the lineups of a file from the Live TV page, and
// shows the first of them.
func (a *Admin) importLineupsForm(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	ctx := r.Context()
	lv := parseLineupView(backValues(r)).In()
	data, err := readLineupUpload(r)
	var got []store.LineupImport
	if err == nil {
		got, err = a.importLineups(ctx, data)
	}
	if err != nil {
		a.lineupDoneAt(w, r, s, lv, err, "", 0)
		return
	}
	lv = lineupView{Lineup: got[0].ID, Page: 1}
	r = r.WithContext(inLineup(ctx, got[0].ID))
	a.lineupDoneAt(w, r, s, lv, nil, importNotice(got), 0)
}

// The API's lineup file.

func (a *Admin) apiExportLineups(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	if err := a.sendLineupFile(w, r); err != nil {
		a.apiInternal(w, "cannot export the lineups", err)
	}
}

// apiLineupImport is what the import of a lineup did, as the API says it.
type apiLineupImport struct {
	ID       int64      `json:"id"`
	Name     string     `json:"name"`
	Groups   int        `json:"groups"`
	Channels int        `json:"channels"`
	Rules    int        `json:"rules"`
	LeftOut  apiLeftOut `json:"left_out"`
}

type apiLeftOut struct {
	Categories int `json:"categories"`
	Channels   int `json:"channels"`
	Backups    int `json:"backups"`
	Rules      int `json:"rules"`
}

func (a *Admin) apiImportLineups(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, lineupfile.MaxBytes))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		apiError(w, http.StatusRequestEntityTooLarge, "bad_file", "That file is too large to be a lineup file.")
		return
	}
	if err != nil {
		apiError(w, http.StatusBadRequest, "bad_file", "Send a lineup file as the request's body.")
		return
	}
	got, err := a.importLineups(r.Context(), data)
	if a.editAnswer(w, err) {
		return
	}
	out := make([]apiLineupImport, len(got))
	for i, l := range got {
		out[i] = apiLineupImport{ID: l.ID, Name: l.Name, Groups: l.Groups, Channels: l.Channels, Rules: l.Rules,
			LeftOut: apiLeftOut{l.LeftOut.Categories, l.LeftOut.Channels, l.LeftOut.Backups, l.LeftOut.Rules}}
	}
	writeJSON(w, http.StatusCreated, struct {
		Lineups []apiLineupImport `json:"lineups"`
	}{out})
}
