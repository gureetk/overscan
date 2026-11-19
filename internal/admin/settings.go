// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"overscan.gureet.ca/internal/store"
)

// Settings for the whole server: whether channels are repaired unless they
// say otherwise (D58), whether repaired channels whose address is HLS are
// made one stream so that they can be (D61), how many films and episodes at
// once ffmpeg may transcode whose video a box cannot play (D73), and
// whether films go through ffmpeg to convert their sound, and through
// their source's server to transcode their video (D78), and what sources'
// marks for adults do (D86).

type apiSettings struct {
	RepairDefault    bool   `json:"repair_default"`
	RepairHLS        bool   `json:"repair_hls"`
	TranscodeVideo   int    `json:"transcode_video"`
	ConvertSound     bool   `json:"convert_sound"`
	ServerTranscodes bool   `json:"server_transcodes"`
	AdultMarks       string `json:"adult_marks"`
}

func (a *Admin) apiSettings(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	ctx := r.Context()
	var v apiSettings
	var err error
	if v.RepairDefault, err = a.store.RepairDefault(ctx); err == nil {
		if v.RepairHLS, err = a.store.RepairHLS(ctx); err == nil {
			if v.TranscodeVideo, err = a.store.Transcodes(ctx); err == nil {
				if v.ConvertSound, err = a.store.ConvertsSound(ctx); err == nil {
					if v.ServerTranscodes, err = a.store.ServerTranscodes(ctx); err == nil {
						v.AdultMarks, err = a.store.AdultMarks(ctx)
					}
				}
			}
		}
	}
	if a.editAnswer(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (a *Admin) apiChangeSettings(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	var body map[string]json.RawMessage
	if !readJSON(w, r, &body) {
		return
	}
	for k, v := range body {
		switch k {
		case "repair_default":
			var on bool
			if json.Unmarshal(v, &on) != nil {
				apiError(w, http.StatusBadRequest, "bad_json", "repair_default is true or false.")
				return
			}
			if a.editAnswer(w, a.setRepairDefault(r.Context(), on)) {
				return
			}
		case "repair_hls":
			var on bool
			if json.Unmarshal(v, &on) != nil {
				apiError(w, http.StatusBadRequest, "bad_json", "repair_hls is true or false.")
				return
			}
			if a.editAnswer(w, a.setRepairHLS(r.Context(), on)) {
				return
			}
		case "transcode_video":
			var n int
			if json.Unmarshal(v, &n) != nil || n < 0 || n > store.MaxTranscodes {
				apiError(w, http.StatusBadRequest, "bad_json", fmt.Sprintf("transcode_video is a whole number from 0 to %d.", store.MaxTranscodes))
				return
			}
			if a.editAnswer(w, a.setTranscodes(r.Context(), n)) {
				return
			}
		case "convert_sound":
			var on bool
			if json.Unmarshal(v, &on) != nil {
				apiError(w, http.StatusBadRequest, "bad_json", "convert_sound is true or false.")
				return
			}
			if a.editAnswer(w, a.setConvertSound(r.Context(), on)) {
				return
			}
		case "server_transcodes":
			var on bool
			if json.Unmarshal(v, &on) != nil {
				apiError(w, http.StatusBadRequest, "bad_json", "server_transcodes is true or false.")
				return
			}
			if a.editAnswer(w, a.setServerTranscodes(r.Context(), on)) {
				return
			}
		case "adult_marks":
			var marks string
			if json.Unmarshal(v, &marks) != nil {
				apiError(w, http.StatusBadRequest, "bad_json", "adult_marks is nothing, lock or hide.")
				return
			}
			if a.editAnswer(w, a.setAdultMarks(r.Context(), marks)) {
				return
			}
		default:
			apiError(w, http.StatusBadRequest, "bad_json", "There is no setting named "+k+".")
			return
		}
	}
	a.apiSettings(w, r, store.AdminSession{})
}

// setRepairDefault sets whether channels are repaired unless they say
// otherwise.
func (a *Admin) setRepairDefault(ctx context.Context, on bool) error {
	if err := a.store.SetRepairDefault(ctx, on); err != nil {
		return err
	}
	a.log.Info("stream repair's default set in the admin", "repair_every_channel", on)
	return nil
}

// repairForm sets the default from the Settings page.
func (a *Admin) repairForm(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	on := r.PostFormValue("default") != ""
	done := "Channels are repaired only where they are set to be."
	if on {
		done = "Every channel is repaired now, unless it is set not to be."
	}
	a.settingsDone(w, r, s, a.setRepairDefault(r.Context(), on), done)
}

// setRepairHLS sets whether repaired channels whose address is HLS are made
// one stream, so that they can be repaired.
func (a *Admin) setRepairHLS(ctx context.Context, on bool) error {
	if err := a.store.SetRepairHLS(ctx, on); err != nil {
		return err
	}
	a.log.Info("the repair of HLS streams set in the admin", "repair_hls", on)
	return nil
}

// repairHLSForm sets from the Settings page whether HLS streams are
// repaired.
func (a *Admin) repairHLSForm(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	on := r.PostFormValue("hls") != ""
	done := "Channels whose address is HLS play as HLS again, unrepaired, from their next start."
	if on {
		done = "Repaired channels whose address is HLS are made one stream and repaired, from their next start."
	}
	a.settingsDone(w, r, s, a.setRepairHLS(r.Context(), on), done)
}

// setTranscodes sets how many films and episodes at once ffmpeg may
// transcode whose video a box cannot play.
func (a *Admin) setTranscodes(ctx context.Context, n int) error {
	if err := a.store.SetTranscodes(ctx, n); err != nil {
		return err
	}
	a.log.Info("transcoding set in the admin", "at_once", n)
	return nil
}

// transcodeForm sets from the Settings page how many films and episodes at
// once ffmpeg may transcode.
func (a *Admin) transcodeForm(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	n, err := strconv.Atoi(r.PostFormValue("at_once"))
	if err != nil || n < 0 || n > store.MaxTranscodes {
		a.settingsDone(w, r, s, badEdit(http.StatusBadRequest, "bad_form", "Choose how many films at a time, from none to %d.",
			store.MaxTranscodes), "")
		return
	}
	done := "Films whose video a box cannot play go to it as they are."
	if n > 0 {
		done = fmt.Sprintf("This server transcodes up to %d %s at a time whose video a box cannot play.", n, plural(n, "film"))
	}
	a.settingsDone(w, r, s, a.setTranscodes(r.Context(), n), done)
}

// setConvertSound sets whether films whose sound or file a box cannot play
// go through ffmpeg.
func (a *Admin) setConvertSound(ctx context.Context, on bool) error {
	if err := a.store.SetConvertsSound(ctx, on); err != nil {
		return err
	}
	a.log.Info("the conversion of films set in the admin", "convert_sound", on)
	return nil
}

// convertForm sets from the Settings page whether films are converted.
func (a *Admin) convertForm(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	on := r.PostFormValue("convert") != ""
	done := "Films whose sound or file a box cannot play go to it as they are, from their next start."
	if on {
		done = "Films whose sound or file a box cannot play go through ffmpeg, which converts them, from their next start."
	}
	a.settingsDone(w, r, s, a.setConvertSound(r.Context(), on), done)
}

// setServerTranscodes sets whether films whose video a box cannot play go
// through their source's server where it transcodes.
func (a *Admin) setServerTranscodes(ctx context.Context, on bool) error {
	if err := a.store.SetServerTranscodes(ctx, on); err != nil {
		return err
	}
	a.log.Info("transcoding by sources' servers set in the admin", "server_transcodes", on)
	return nil
}

// serverTranscodeForm sets from the Settings page whether sources' servers
// transcode films.
func (a *Admin) serverTranscodeForm(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	on := r.PostFormValue("server") != ""
	done := "Films of Jellyfin servers whose video a box cannot play go to it as they are, from their next start."
	if on {
		done = "Jellyfin servers transcode their films whose video a box cannot play, from their next start."
	}
	a.settingsDone(w, r, s, a.setServerTranscodes(r.Context(), on), done)
}

// setAdultMarks sets what the marks that sources put on channels and titles
// for adults do: nothing, lock or hide (D86).
func (a *Admin) setAdultMarks(ctx context.Context, marks string) error {
	if marks != store.AdultNothing && marks != store.AdultLock && marks != store.AdultHide {
		return badEdit(http.StatusBadRequest, "bad_adult_marks", "Marks for adults do nothing, lock or hide.")
	}
	if err := a.store.SetAdultMarks(ctx, marks); err != nil {
		return err
	}
	a.log.Info("what marks for adults do set in the admin", "adult_marks", marks)
	return nil
}

// adultForm sets from the Settings page what marks for adults do.
func (a *Admin) adultForm(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	marks := r.PostFormValue("adult")
	done := map[string]string{
		store.AdultNothing: "Channels, films and series that their sources mark for adults are like any other.",
		store.AdultLock:    "Channels, films and series that their sources mark for adults need the parental PIN.",
		store.AdultHide:    "Channels, films and series that their sources mark for adults are hidden from devices.",
	}[marks]
	a.settingsDone(w, r, s, a.setAdultMarks(r.Context(), marks), done)
}

// repairChoice reads a channel's repair switch from a form: "on" or "off",
// or "" for the default, which is nil. It reports false for anything else.
func repairChoice(v string) (*bool, bool) {
	on, off := true, false
	switch v {
	case "":
		return nil, true
	case "on":
		return &on, true
	case "off":
		return &off, true
	}
	return nil, false
}

// sameChoice reports whether two repair switches say the same.
func sameChoice(a, b *bool) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}
