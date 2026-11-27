// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"overscan.gureet.ca/internal/store"
	"overscan.gureet.ca/internal/streamprofile"
)

// The Transcoding page (D147): what is transcoded of live TV as a whole,
// and the profiles, fixed and made by the administrator. Groups and
// channels say their own on Live TV.

// transcodeOption is a setting of what is transcoded, as a select offers
// it: "as_needed", "never", or "always:" and a profile's name.
type transcodeOption struct {
	Value, Label string
}

// transcodeValue is a setting as transcodeOption names it.
func transcodeValue(t store.Transcode) string {
	switch {
	case t.Never:
		return store.ModeNever
	case t.Profile != "":
		return store.ModeAlways + ":" + t.Profile
	}
	return store.ModeAsNeeded
}

// parseTranscodeValue reads a setting as transcodeOption names it.
func parseTranscodeValue(v string) (store.Transcode, error) {
	mode, profile, _ := strings.Cut(v, ":")
	return parseTranscode(apiTranscode{Mode: mode, Profile: &profile})
}

// transcodeOptions are the settings that a select offers, after its first,
// which says what as needed does there: never, then always with each
// profile that transcodes.
func (a *Admin) transcodeOptions(ctx context.Context, asNeeded string) []transcodeOption {
	out := []transcodeOption{{store.ModeAsNeeded, asNeeded}, {store.ModeNever, "Never"}}
	ps, err := a.allTranscodeProfiles(ctx)
	if err != nil {
		a.log.Error("cannot list the profiles", "error", err)
	}
	for _, p := range ps {
		if p.Transcodes() {
			out = append(out, transcodeOption{store.ModeAlways + ":" + p.Name, "Always, " + p.Label})
		}
	}
	return out
}

// transcodeText says what a setting does, briefly, such as "always, with
// small" or "never".
func transcodeText(t store.Transcode) string {
	switch {
	case t.Never:
		return "never"
	case t.Profile != "":
		return "always, with " + t.Profile
	}
	return "as needed"
}

// transcodeProfileRow is a profile as the page lists it.
type transcodeProfileRow struct {
	P              streamprofile.Profile
	Uses           store.ProfileUses
	DeleteQuestion string
}

// transcodeProfileForm is the form that makes or changes a profile.
type transcodeProfileForm struct {
	Was string // the name of the profile changed, "" for a new one
	P   streamprofile.Profile
}

// Rates are the highest bit rates that the form offers, in kbit/s, with
// the profile's own if it is another.
func (f transcodeProfileForm) Rates() []int {
	rates := []int{0, 1500, 3000, 5000, 8000, 12000, 20000}
	if !slices.Contains(rates, f.P.MaxKbps) {
		rates = append(rates, f.P.MaxKbps)
		slices.Sort(rates)
	}
	return rates
}

// RateLabel names a highest bit rate for the form.
func (transcodeProfileForm) RateLabel(kbps int) string {
	if kbps == 0 {
		return "None"
	}
	p := streamprofile.Profile{Made: true, MaxKbps: kbps}
	d := p.Describe()
	_, rate, _ := strings.Cut(d, "at most ")
	rate, _, _ = strings.Cut(rate, ",")
	return rate
}

type transcodingData struct {
	CanTranscode bool
	Missing      string // what this server's ffmpeg lacks to transcode, "" if nothing
	HEVC         bool   // its ffmpeg has libx265
	AtOnce       int    // D73's number at a time, 0 for off
	Live         store.Transcode
	LiveText     string
	LiveOptions  []transcodeOption
	Profiles     []transcodeProfileRow
	Full         bool
	Editing      bool
	Form         transcodeProfileForm
}

// LiveValue is live TV's setting as its select names it.
func (d transcodingData) LiveValue() string { return transcodeValue(d.Live) }

func (a *Admin) transcodingData(ctx context.Context, form transcodeProfileForm, editing bool) (transcodingData, error) {
	can, _, missing := a.transcoder()
	d := transcodingData{CanTranscode: can, Missing: missing, Form: form, Editing: editing}
	if a.streams != nil {
		d.HEVC = a.streams.Encoder().HEVC
	}
	var err error
	if d.AtOnce, err = a.store.Transcodes(ctx); err != nil {
		return d, err
	}
	if d.Live, err = a.store.LiveTranscode(ctx); err != nil {
		return d, err
	}
	ps, err := a.allTranscodeProfiles(ctx)
	if err != nil {
		return d, err
	}
	describe := func(name string) string {
		for _, p := range ps {
			if p.Name == name {
				return p.Describe()
			}
		}
		return ""
	}
	d.LiveText = liveTranscodeText(d.Live, describe)
	d.LiveOptions = a.transcodeOptions(ctx, "As needed")
	made := 0
	for _, p := range ps {
		if p.Made {
			made++
		}
		u, err := a.store.TranscodeProfileUses(ctx, p.Name)
		if err != nil {
			return d, err
		}
		d.Profiles = append(d.Profiles, transcodeProfileRow{P: p, Uses: u,
			DeleteQuestion: "Remove the profile " + p.Name + "?"})
	}
	d.Full = made >= store.MaxTranscodeProfiles
	return d, nil
}

func (a *Admin) showTranscoding(w http.ResponseWriter, r *http.Request, s store.AdminSession, status int, form transcodeProfileForm, editing bool, v view) {
	d, err := a.transcodingData(r.Context(), form, editing)
	if err != nil {
		a.log.Error("cannot show the transcoding page", "error", err)
		http.Error(w, "Something went wrong; the server's log says what.", http.StatusInternalServerError)
		return
	}
	v.Title, v.Section, v.Session, v.Data = "Transcoding", "settings", &s, d
	if editing {
		v.Title = "Change " + form.Was
	}
	a.render(w, r, status, "transcoding", v)
}

func (a *Admin) transcodingPage(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	a.showTranscoding(w, r, s, http.StatusOK, transcodeProfileForm{P: newTranscodeProfile()}, false, view{})
}

// transcodeProfilePage shows a profile's own page, to change it.
func (a *Admin) transcodeProfilePage(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	name := r.PathValue("name")
	p, err := a.store.TranscodeProfile(r.Context(), name)
	if err != nil || !p.Made {
		a.notFound(w, r)
		return
	}
	a.showTranscoding(w, r, s, http.StatusOK, transcodeProfileForm{Was: name, P: p}, true, view{})
}

// readTranscodeProfileForm reads the form that makes or changes a profile.
func readTranscodeProfileForm(r *http.Request) streamprofile.Profile {
	p := streamprofile.Profile{Made: true}
	p.Name = strings.TrimSpace(r.PostFormValue("name"))
	p.Codec = r.PostFormValue("codec")
	p.MaxLines, _ = strconv.Atoi(r.PostFormValue("max_lines"))
	p.Quality = r.PostFormValue("quality")
	p.MaxKbps, _ = strconv.Atoi(r.PostFormValue("max_kbps"))
	p.FrameRate = r.PostFormValue("frame_rate")
	p.Deinterlace = r.PostFormValue("deinterlace") != ""
	p.ToneMap = r.PostFormValue("hdr_to_sdr") != ""
	p.Sound = r.PostFormValue("sound")
	return p
}

// transcodingDone shows what a form on the page did, or what went wrong.
func (a *Admin) transcodingDone(w http.ResponseWriter, r *http.Request, s store.AdminSession, err error, notice string, form transcodeProfileForm, editing bool) {
	var bad *editError
	switch {
	case errors.As(err, &bad):
		a.showTranscoding(w, r, s, bad.status, form, editing, view{Error: bad.text})
	case err != nil:
		a.log.Error("cannot change transcoding", "error", err)
		a.showTranscoding(w, r, s, http.StatusInternalServerError, form, editing, view{Error: "Something went wrong; the server's log says what."})
	case r.Header.Get("HX-Request") == "true":
		w.Header().Set("HX-Replace-Url", "/admin/transcoding")
		a.showTranscoding(w, r, s, http.StatusOK, transcodeProfileForm{P: newTranscodeProfile()}, false, view{Notice: notice})
	default:
		http.Redirect(w, r, "/admin/transcoding", http.StatusSeeOther)
	}
}

// addTranscodeProfileForm makes a profile from the page's form.
func (a *Admin) addTranscodeProfileForm(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	form := transcodeProfileForm{P: readTranscodeProfileForm(r)}
	p, err := a.addTranscodeProfile(r.Context(), form.P)
	a.transcodingDone(w, r, s, err, "Made "+p.Name+": "+p.Describe()+". Choose it for live TV, a group, a channel, an account or a device.", form, false)
}

// saveTranscodeProfileForm changes a profile from its page.
func (a *Admin) saveTranscodeProfileForm(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	name := r.PathValue("name")
	if old, err := a.store.TranscodeProfile(r.Context(), name); err != nil || !old.Made {
		a.notFound(w, r)
		return
	}
	form := transcodeProfileForm{Was: name, P: readTranscodeProfileForm(r)}
	p, err := a.changeTranscodeProfile(r.Context(), name, form.P)
	a.transcodingDone(w, r, s, err, "Saved "+p.Name+": "+p.Describe()+". Streams that play now keep what they had until they start again.", form, true)
}

// transcodeProfileAction removes a profile from the page.
func (a *Admin) transcodeProfileAction(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	name := r.PathValue("name")
	if r.PathValue("action") != "delete" {
		a.notFound(w, r)
		return
	}
	a.transcodingDone(w, r, s, a.removeTranscodeProfile(r.Context(), name), "Removed "+name+".", transcodeProfileForm{P: newTranscodeProfile()}, false)
}

// liveTranscodeForm sets live TV's own setting from the page.
func (a *Admin) liveTranscodeForm(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	t, err := parseTranscodeValue(r.PostFormValue("transcode"))
	if err == nil {
		err = a.setLiveTranscode(r.Context(), t)
	}
	a.transcodingDone(w, r, s, err, "Live TV is transcoded "+transcodeText(t)+", where its groups and channels say nothing of their own.",
		transcodeProfileForm{P: newTranscodeProfile()}, false)
}
