// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"overscan.gureet.ca/internal/store"
	"overscan.gureet.ca/internal/tmdb"
)

// Settings for the whole server: whether channels are repaired unless they
// say otherwise (D58), whether repaired channels whose address is HLS are
// made one stream so that they can be (D61), how many films and episodes at
// once ffmpeg may transcode whose video a box cannot play (D73), and
// whether films go through ffmpeg to convert their sound, and through
// their source's server to transcode their video (D78), what sources'
// marks for adults do (D86), whether boxes show radio stations on their
// Radio screen alone (D87), TMDB's key and language (D88), whose key the
// API never gives back, the room that recordings may take (D89), how
// many minutes of a box's live channel the server keeps (D92), whether
// one connection to a source serves every screen that plays a channel
// (D93), the length of placeholder programmes, in minutes, 0 for none
// (D142), whether providers' films and series take TMDB's pictures where
// theirs want replacing (D143), and what is transcoded of live TV as a
// whole (D147).

type apiSettings struct {
	RepairDefault    bool         `json:"repair_default"`
	RepairHLS        bool         `json:"repair_hls"`
	TranscodeVideo   int          `json:"transcode_video"`
	ConvertSound     bool         `json:"convert_sound"`
	ServerTranscodes bool         `json:"server_transcodes"`
	AdultMarks       string       `json:"adult_marks"`
	RadioApart       bool         `json:"radio_apart"`
	TMDBKeySet       bool         `json:"tmdb_key_set"`
	TMDBLanguage     string       `json:"tmdb_language"`
	RecordingsCap    int          `json:"recordings_cap"`
	RecordUntil      string       `json:"recordings_until"`
	RecordingHours   int          `json:"recordings_max_hours"`
	TimeshiftMinutes int          `json:"timeshift_minutes"`
	SharedTuner      bool         `json:"shared_tuner"`
	Outputs          bool         `json:"outputs"`
	OutputsAnywhere  bool         `json:"outputs_anywhere"`
	Placeholders     int          `json:"guide_placeholders"`
	TMDBPictures     bool         `json:"tmdb_pictures"`
	LiveTranscode    apiTranscode `json:"live_transcode"`
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
						if v.AdultMarks, err = a.store.AdultMarks(ctx); err == nil {
							if v.RadioApart, err = a.store.RadioApart(ctx); err == nil {
								var key string
								if key, err = a.store.TMDBKey(ctx); err == nil {
									v.TMDBKeySet = key != ""
									if v.TMDBLanguage, err = a.store.TMDBLanguage(ctx); err == nil {
										if v.RecordingsCap, err = a.store.RecordingsCap(ctx); err == nil {
											if v.TimeshiftMinutes, err = a.store.Timeshift(ctx); err == nil {
												if v.SharedTuner, err = a.store.SharedTuner(ctx); err == nil {
													if v.RecordUntil, err = a.store.RecordUntil(ctx); err == nil {
														v.RecordingHours, err = a.store.RecordingHours(ctx)
													}
												}
											}
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}
	if err == nil {
		if v.Outputs, err = a.store.Outputs(ctx); err == nil {
			if v.OutputsAnywhere, err = a.store.OutputsAnywhere(ctx); err == nil {
				var block time.Duration
				if block, err = a.store.Placeholders(ctx); err == nil {
					if v.TMDBPictures, err = a.store.TMDBPictures(ctx); err == nil {
						var live store.Transcode
						live, err = a.store.LiveTranscode(ctx)
						v.LiveTranscode = transcodeBody(live)
					}
				}
				v.Placeholders = int(block / time.Minute)
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
		case "live_transcode":
			t, err := readTranscode(v)
			if a.editAnswer(w, err) {
				return
			}
			if a.editAnswer(w, a.setLiveTranscode(r.Context(), t)) {
				return
			}
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
		case "radio_apart":
			var on bool
			if json.Unmarshal(v, &on) != nil {
				apiError(w, http.StatusBadRequest, "bad_json", "radio_apart is true or false.")
				return
			}
			if a.editAnswer(w, a.setRadioApart(r.Context(), on)) {
				return
			}
		case "tmdb_language":
			var language string
			if json.Unmarshal(v, &language) != nil {
				apiError(w, http.StatusBadRequest, "bad_json", "tmdb_language is a language such as en-GB, or \"\".")
				return
			}
			if a.editAnswer(w, a.setTMDBLanguage(r.Context(), language)) {
				return
			}
		case "tmdb_key":
			var key string
			if json.Unmarshal(v, &key) != nil {
				apiError(w, http.StatusBadRequest, "bad_json", "tmdb_key is TMDB's API key or read access token, or \"\" to remove it.")
				return
			}
			if _, err := a.setTMDBKey(r.Context(), key); a.editAnswer(w, err) {
				return
			}
		case "recordings_cap":
			var gb int
			if json.Unmarshal(v, &gb) != nil {
				apiError(w, http.StatusBadRequest, "bad_json", fmt.Sprintf("recordings_cap is a whole number of gigabytes from 0 to %d.", store.MaxRecordingsCap))
				return
			}
			if a.editAnswer(w, a.setRecordingsCap(r.Context(), gb)) {
				return
			}
		case "recordings_until":
			var until string
			if json.Unmarshal(v, &until) != nil {
				apiError(w, http.StatusBadRequest, "bad_json", `recordings_until is "programme" or "stopped".`)
				return
			}
			if a.editAnswer(w, a.setRecordUntil(r.Context(), until)) {
				return
			}
		case "recordings_max_hours":
			var hours int
			if json.Unmarshal(v, &hours) != nil {
				apiError(w, http.StatusBadRequest, "bad_json", fmt.Sprintf("recordings_max_hours is a whole number of hours from 1 to %d.", store.MaxRecordingHours))
				return
			}
			if a.editAnswer(w, a.setRecordingHours(r.Context(), hours)) {
				return
			}
		case "timeshift_minutes":
			var minutes int
			if json.Unmarshal(v, &minutes) != nil {
				apiError(w, http.StatusBadRequest, "bad_json", fmt.Sprintf("timeshift_minutes is a whole number of minutes from 0 to %d.", store.MaxTimeshift))
				return
			}
			if a.editAnswer(w, a.setTimeshift(r.Context(), minutes)) {
				return
			}
		case "shared_tuner":
			var on bool
			if json.Unmarshal(v, &on) != nil {
				apiError(w, http.StatusBadRequest, "bad_json", "shared_tuner is true or false.")
				return
			}
			if a.editAnswer(w, a.setSharedTuner(r.Context(), on)) {
				return
			}
		case "outputs":
			var on bool
			if json.Unmarshal(v, &on) != nil {
				apiError(w, http.StatusBadRequest, "bad_json", "outputs is true or false.")
				return
			}
			if a.editAnswer(w, a.setOutputsOn(r.Context(), on)) {
				return
			}
		case "outputs_anywhere":
			var on bool
			if json.Unmarshal(v, &on) != nil {
				apiError(w, http.StatusBadRequest, "bad_json", "outputs_anywhere is true or false.")
				return
			}
			if a.editAnswer(w, a.setOutputsAnywhere(r.Context(), on)) {
				return
			}
		case "tmdb_pictures":
			var on bool
			if json.Unmarshal(v, &on) != nil {
				apiError(w, http.StatusBadRequest, "bad_json", "tmdb_pictures is true or false.")
				return
			}
			if a.editAnswer(w, a.setTMDBPictures(r.Context(), on)) {
				return
			}
		case "guide_placeholders":
			var minutes int
			if json.Unmarshal(v, &minutes) != nil {
				apiError(w, http.StatusBadRequest, "bad_json", placeholdersHint)
				return
			}
			if a.editAnswer(w, a.setPlaceholders(r.Context(), minutes)) {
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

// setRadioApart sets whether boxes show radio stations on their Radio
// screen alone, and not among the channels of Live TV and the guide (D87).
func (a *Admin) setRadioApart(ctx context.Context, on bool) error {
	if err := a.store.SetRadioApart(ctx, on); err != nil {
		return err
	}
	a.log.Info("where boxes show radio stations set in the admin", "radio_apart", on)
	return nil
}

// radioForm sets from the Settings page whether boxes show radio stations
// on their Radio screen alone.
func (a *Admin) radioForm(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	on := r.PostFormValue("apart") != ""
	done := "Boxes show radio stations on their Radio screen, and among the channels of Live TV and the TV Guide too."
	if on {
		done = "Boxes show radio stations on their Radio screen alone."
	}
	a.settingsDone(w, r, s, a.setRadioApart(r.Context(), on), done)
}

// tmdbCheckTimeout bounds asking TMDB whether it takes a key.
const tmdbCheckTimeout = 10 * time.Second

// setTMDBKey keeps TMDB's key, once TMDB has taken it, or removes it when
// it is "" (D88). A key that TMDB cannot be asked about now is kept, and
// the warning says so.
func (a *Admin) setTMDBKey(ctx context.Context, key string) (warning string, err error) {
	key = strings.TrimSpace(key)
	if key == "" {
		if err := a.store.SetTMDBKey(ctx, ""); err != nil {
			return "", err
		}
		a.log.Info("TMDB's key removed in the admin; folders' titles show what their folders have")
		a.readFolders(ctx)
		return "", nil
	}
	if len(key) > 1024 || strings.ContainsAny(key, " \t\r\n") {
		return "", badEdit(http.StatusBadRequest, "bad_tmdb_key", "That is not a key for TMDB.")
	}
	language, err := a.store.TMDBLanguage(ctx)
	if err != nil {
		return "", err
	}
	check, cancel := context.WithTimeout(ctx, tmdbCheckTimeout)
	defer cancel()
	switch err := tmdb.New(key, language, "Overscan/"+a.version).WithBase(a.tmdbBase).Check(check); {
	case errors.Is(err, tmdb.ErrKey):
		return "", badEdit(http.StatusBadRequest, "bad_tmdb_key", "TMDB refused the key.")
	case err != nil:
		warning = "TMDB could not be asked whether it takes the key: " + err.Error() + "."
		a.log.Warn("TMDB's key saved in the admin, though TMDB could not be asked whether it takes it", "error", err)
	default:
		a.log.Info("TMDB's key saved in the admin; folders' titles get what their folders lack from TMDB")
	}
	if err := a.store.SetTMDBKey(ctx, key); err != nil {
		return "", err
	}
	a.readFolders(ctx)
	return warning, nil
}

// Folders reads the folder sources again, and looks providers' titles'
// pictures up on TMDB (D143).
type Folders interface {
	ReadFoldersAgain(ctx context.Context)
	FillPictures(ctx context.Context)
}

// UseFolders gives the admin what reads the folder sources again, which a
// change to TMDB's key or language does (D88).
func (a *Admin) UseFolders(f Folders) { a.folders = f }

// readFolders reads the folder sources again, and looks providers'
// titles' pictures up on TMDB, so that a change to TMDB's settings shows
// at once.
func (a *Admin) readFolders(ctx context.Context) {
	if a.folders != nil {
		a.folders.ReadFoldersAgain(ctx)
		a.folders.FillPictures(ctx)
	}
}

// setTMDBPictures turns TMDB's pictures for providers' titles on or off
// (D143).
func (a *Admin) setTMDBPictures(ctx context.Context, on bool) error {
	if err := a.store.SetTMDBPictures(ctx, on); err != nil {
		return err
	}
	a.log.Info("TMDB's pictures for providers' films and series set in the admin", "on", on)
	if on && a.folders != nil {
		a.folders.FillPictures(ctx)
	}
	return nil
}

// tmdbPicturesForm turns TMDB's pictures for providers' titles on or off
// from the Settings page.
func (a *Admin) tmdbPicturesForm(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	on := r.PostFormValue("pictures") != ""
	done := "Films and series of Xtream servers and Stalker portals show their sources' pictures."
	if on {
		done = "Films and series of Xtream servers and Stalker portals whose pictures want replacing take TMDB's, as they are looked up in the background."
	}
	a.settingsDone(w, r, s, a.setTMDBPictures(r.Context(), on), done)
}

// setTMDBLanguage sets the language that TMDB is asked in: such as en-GB,
// or "" for TMDB's own.
func (a *Admin) setTMDBLanguage(ctx context.Context, language string) error {
	language = strings.TrimSpace(language)
	if language != "" && !tmdb.ValidLanguage(language) {
		return badEdit(http.StatusBadRequest, "bad_tmdb_language", "A language for TMDB is two small letters, and a region if you like: en or en-GB.")
	}
	old, err := a.store.TMDBLanguage(ctx)
	if err != nil {
		return err
	}
	if err := a.store.SetTMDBLanguage(ctx, language); err != nil {
		return err
	}
	if language != old {
		a.log.Info("TMDB's language set in the admin", "language", language)
		a.readFolders(ctx)
	}
	return nil
}

// tmdbForm sets TMDB's key and language from the Settings page, or removes
// the key.
func (a *Admin) tmdbForm(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	ctx := r.Context()
	if r.PostFormValue("remove") != "" {
		_, err := a.setTMDBKey(ctx, "")
		a.settingsDone(w, r, s, err, "TMDB's key is removed. Titles in folders show what their folders have.")
		return
	}
	if err := a.setTMDBLanguage(ctx, r.PostFormValue("language")); err != nil {
		a.settingsDone(w, r, s, err, "")
		return
	}
	key := r.PostFormValue("key")
	if key == "" {
		a.settingsDone(w, r, s, nil, "TMDB's language is saved.")
		return
	}
	warning, err := a.setTMDBKey(ctx, key)
	a.settingsDone(w, r, s, err, strings.TrimSpace("TMDB's key is saved. The folders are read again, and their titles that lack a poster or a plot are looked up on TMDB in the background. "+warning))
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
