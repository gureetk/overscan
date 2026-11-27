// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"overscan.gureet.ca/internal/store"
	"overscan.gureet.ca/internal/streamprofile"
)

// What is transcoded, and the profiles that the administrator makes
// (D147): live TV as a whole, a lineup's group and a channel are each
// transcoded as needed, always with a profile, or never; profiles of a
// codec, a largest size, a quality, a frame rate, deinterlacing, HDR made
// SDR, and the sound, beside the fixed ones (D110).

// apiTranscode is a setting of what is transcoded.
type apiTranscode struct {
	Mode    string  `json:"mode"`
	Profile *string `json:"profile"`
}

// apiTranscodeFrom is the setting that wins for a channel, and where it
// comes from: "channel", "group" or "live_tv"; null when none says
// anything.
type apiTranscodeFrom struct {
	apiTranscode
	SetBy *string `json:"set_by"`
}

func transcodeBody(t store.Transcode) apiTranscode {
	out := apiTranscode{Mode: t.Mode()}
	if t.Profile != "" {
		p := t.Profile
		out.Profile = &p
	}
	return out
}

func transcodeFromBody(t store.Transcode, by string) apiTranscodeFrom {
	out := apiTranscodeFrom{apiTranscode: transcodeBody(t)}
	if by != "" {
		s := strings.ReplaceAll(by, " ", "_")
		s = strings.ToLower(s)
		out.SetBy = &s
	}
	return out
}

// parseTranscode reads a setting of what is transcoded.
func parseTranscode(v apiTranscode) (store.Transcode, error) {
	profile := ""
	if v.Profile != nil {
		profile = strings.TrimSpace(*v.Profile)
	}
	switch v.Mode {
	case store.ModeAsNeeded:
		return store.Transcode{}, nil
	case store.ModeNever:
		return store.Transcode{Never: true}, nil
	case store.ModeAlways:
		if profile == "" {
			return store.Transcode{}, badEdit(http.StatusBadRequest, "bad_transcode", "To transcode always, name the profile to transcode with.")
		}
		return store.Transcode{Profile: profile}, nil
	}
	return store.Transcode{}, badEdit(http.StatusBadRequest, "bad_transcode", `mode is "as_needed", "always" or "never".`)
}

// readTranscode decodes a setting of what is transcoded from JSON.
func readTranscode(v json.RawMessage) (store.Transcode, error) {
	var t apiTranscode
	if err := json.Unmarshal(v, &t); err != nil {
		return store.Transcode{}, badEdit(http.StatusBadRequest, "bad_transcode",
			`transcode is {"mode": "as_needed"}, {"mode": "never"}, or {"mode": "always", "profile": "<name>"}.`)
	}
	return parseTranscode(t)
}

// transcodeError says what is wrong with a setting of what is transcoded,
// or with a profile.
func transcodeError(err error, profile string) error {
	var bad *streamprofile.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &bad):
		return badEdit(http.StatusBadRequest, "bad_profile", "%s", bad.Text)
	case errors.Is(err, store.ErrNoSuchProfile):
		return badEdit(http.StatusBadRequest, "bad_transcode", "There is no profile called %s.", profile)
	case errors.Is(err, store.ErrNotTranscoding):
		return badEdit(http.StatusBadRequest, "bad_transcode", "%s sends streams as their sources send them; choose a profile that transcodes.", profile)
	case errors.Is(err, store.ErrProfileExists):
		return badEdit(http.StatusConflict, "name_taken", "Another profile is called %s.", profile)
	case errors.Is(err, store.ErrTooManyTranscodeProfiles):
		return badEdit(http.StatusConflict, "too_many", "A server keeps at most %d profiles of its own.", store.MaxTranscodeProfiles)
	case errors.Is(err, store.ErrProfileFixed):
		return badEdit(http.StatusConflict, "fixed_profile", "%s is one of Overscan's own profiles, which stay as they are.", profile)
	case errors.Is(err, store.ErrNotFound):
		return badEdit(http.StatusNotFound, "not_found", "There is no such profile.")
	}
	return err
}

// apiTranscodeProfile is a profile, fixed or made by the administrator.
type apiTranscodeProfile struct {
	Name      string `json:"name"`
	Fixed     bool   `json:"fixed"`
	Label     string `json:"label"`
	Describes string `json:"describes"`
	// The settings of a profile that the administrator made; null for a
	// fixed one.
	Codec       *string          `json:"codec"`
	MaxLines    *int             `json:"max_lines"`
	Quality     *string          `json:"quality"`
	MaxKbps     *int             `json:"max_kbps"`
	FrameRate   *string          `json:"frame_rate"`
	Deinterlace *bool            `json:"deinterlace"`
	HDRToSDR    *bool            `json:"hdr_to_sdr"`
	Sound       *string          `json:"sound"`
	Uses        apiTranscodeUses `json:"uses"`
}

type apiTranscodeUses struct {
	Accounts int  `json:"accounts"`
	Devices  int  `json:"devices"`
	Channels int  `json:"channels"`
	Groups   int  `json:"groups"`
	LiveTV   bool `json:"live_tv"`
}

func transcodeProfileBody(p streamprofile.Profile, u store.ProfileUses) apiTranscodeProfile {
	out := apiTranscodeProfile{Name: p.Name, Fixed: !p.Made, Label: p.Label, Describes: p.Describe(),
		Uses: apiTranscodeUses{u.Accounts, u.Devices, u.Channels, u.Groups, u.LiveTV}}
	if p.Made {
		out.Codec, out.MaxLines, out.Quality, out.MaxKbps = &p.Codec, &p.MaxLines, &p.Quality, &p.MaxKbps
		out.FrameRate, out.Deinterlace, out.HDRToSDR, out.Sound = &p.FrameRate, &p.Deinterlace, &p.ToneMap, &p.Sound
	}
	return out
}

// apiTranscodeProfileChange is the body of a request that makes or changes a
// profile. What it leaves out stays as it is, or takes its default for a
// new profile: H.264, the source's size, medium quality, no highest bit
// rate, the source's frame rate, deinterlacing, HDR made SDR, and AAC in
// stereo.
type apiTranscodeProfileChange struct {
	Name        *string `json:"name"`
	Codec       *string `json:"codec"`
	MaxLines    *int    `json:"max_lines"`
	Quality     *string `json:"quality"`
	MaxKbps     *int    `json:"max_kbps"`
	FrameRate   *string `json:"frame_rate"`
	Deinterlace *bool   `json:"deinterlace"`
	HDRToSDR    *bool   `json:"hdr_to_sdr"`
	Sound       *string `json:"sound"`
}

// apply puts the body's settings over p.
func (c apiTranscodeProfileChange) apply(p streamprofile.Profile) streamprofile.Profile {
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = strings.TrimSpace(*v)
		}
	}
	set(&p.Name, c.Name)
	set(&p.Codec, c.Codec)
	set(&p.Quality, c.Quality)
	set(&p.FrameRate, c.FrameRate)
	set(&p.Sound, c.Sound)
	if c.MaxLines != nil {
		p.MaxLines = *c.MaxLines
	}
	if c.MaxKbps != nil {
		p.MaxKbps = *c.MaxKbps
	}
	if c.Deinterlace != nil {
		p.Deinterlace = *c.Deinterlace
	}
	if c.HDRToSDR != nil {
		p.ToneMap = *c.HDRToSDR
	}
	return p
}

// newTranscodeProfile is what a new profile starts as, before its form or body.
func newTranscodeProfile() streamprofile.Profile {
	return streamprofile.Profile{Made: true, Codec: streamprofile.H264, Quality: streamprofile.Medium,
		FrameRate: streamprofile.RateSource, Deinterlace: true, ToneMap: true, Sound: streamprofile.SoundAAC}
}

// allTranscodeProfiles returns the fixed profiles and those that the
// administrator made, in the order that forms offer them.
func (a *Admin) allTranscodeProfiles(ctx context.Context) ([]streamprofile.Profile, error) {
	made, err := a.store.TranscodeProfiles(ctx)
	if err != nil {
		return nil, err
	}
	return append(append([]streamprofile.Profile{}, streamprofile.Profiles...), made...), nil
}

// addTranscodeProfile makes a profile.
func (a *Admin) addTranscodeProfile(ctx context.Context, p streamprofile.Profile) (streamprofile.Profile, error) {
	p, err := streamprofile.Check(p)
	if err != nil {
		return p, transcodeError(err, p.Name)
	}
	if err := a.store.AddTranscodeProfile(ctx, p); err != nil {
		return p, transcodeError(err, p.Name)
	}
	a.log.Info("a profile made in the admin", "profile", p.Name, "makes", p.Describe())
	return p, nil
}

// changeTranscodeProfile changes the profile named name to p.
func (a *Admin) changeTranscodeProfile(ctx context.Context, name string, p streamprofile.Profile) (streamprofile.Profile, error) {
	if _, fixed := streamprofile.Find(name); fixed {
		return p, transcodeError(store.ErrProfileFixed, name)
	}
	p, err := streamprofile.Check(p)
	if err != nil {
		return p, transcodeError(err, p.Name)
	}
	if err := a.store.ChangeTranscodeProfile(ctx, name, p); err != nil {
		return p, transcodeError(err, p.Name)
	}
	a.log.Info("a profile changed in the admin", "profile", p.Name, "was", name, "makes", p.Describe())
	return p, nil
}

// removeTranscodeProfile removes a profile that nothing names.
func (a *Admin) removeTranscodeProfile(ctx context.Context, name string) error {
	if _, fixed := streamprofile.Find(name); fixed {
		return transcodeError(store.ErrProfileFixed, name)
	}
	err := a.store.RemoveTranscodeProfile(ctx, name)
	if errors.Is(err, store.ErrProfileInUse) {
		u, uerr := a.store.TranscodeProfileUses(ctx, name)
		if uerr != nil {
			return uerr
		}
		return badEdit(http.StatusConflict, "profile_in_use", "%s is used by %s; choose another profile for them first.", name, u)
	}
	if err != nil {
		return transcodeError(err, name)
	}
	a.log.Info("a profile removed in the admin", "profile", name)
	return nil
}

// setLiveTranscode sets live TV's own setting of what is transcoded.
func (a *Admin) setLiveTranscode(ctx context.Context, t store.Transcode) error {
	if err := a.store.SetLiveTranscode(ctx, t); err != nil {
		return transcodeError(err, t.Profile)
	}
	a.log.Info("live TV's transcoding set in the admin", "mode", t.Mode(), "profile", t.Profile)
	return nil
}

// setGroupTranscode sets a group's setting of what is transcoded.
func (a *Admin) setGroupTranscode(ctx context.Context, groupID int64, t store.Transcode) error {
	lineup, err := a.lineupID(ctx)
	if err != nil {
		return err
	}
	if err := a.store.SetLineupGroupTranscode(ctx, lineup, groupID, t); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return lineupStoreError(err, "group")
		}
		return transcodeError(err, t.Profile)
	}
	a.log.Info("a group's transcoding set in the admin", "lineup", lineup, "group", groupID, "mode", t.Mode(), "profile", t.Profile)
	return nil
}

// setEntriesTranscode sets what is transcoded of the channels of entries.
func (a *Admin) setEntriesTranscode(ctx context.Context, lineup int64, ids []int64, t store.Transcode) error {
	if err := a.store.SetEntriesTranscode(ctx, lineup, ids, t); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return lineupStoreError(err, "channel")
		}
		return transcodeError(err, t.Profile)
	}
	a.log.Info("channels' transcoding set in the admin", "lineup", lineup, "channels", len(ids), "mode", t.Mode(), "profile", t.Profile)
	return nil
}

func (a *Admin) apiTranscodeProfiles(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	ps, err := a.allTranscodeProfiles(r.Context())
	if a.editAnswer(w, err) {
		return
	}
	out := make([]apiTranscodeProfile, len(ps))
	for i, p := range ps {
		u, err := a.store.TranscodeProfileUses(r.Context(), p.Name)
		if a.editAnswer(w, err) {
			return
		}
		out[i] = transcodeProfileBody(p, u)
	}
	writeJSON(w, http.StatusOK, struct {
		Profiles []apiTranscodeProfile `json:"profiles"`
	}{out})
}

func (a *Admin) writeTranscodeProfile(w http.ResponseWriter, r *http.Request, status int, name string) {
	p, err := a.store.TranscodeProfile(r.Context(), name)
	if errors.Is(err, store.ErrNoSuchProfile) {
		err = transcodeError(store.ErrNotFound, name)
	}
	if a.editAnswer(w, err) {
		return
	}
	u, err := a.store.TranscodeProfileUses(r.Context(), name)
	if a.editAnswer(w, err) {
		return
	}
	writeJSON(w, status, transcodeProfileBody(p, u))
}

func (a *Admin) apiTranscodeProfileOne(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	a.writeTranscodeProfile(w, r, http.StatusOK, r.PathValue("name"))
}

func (a *Admin) apiAddTranscodeProfile(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	var body apiTranscodeProfileChange
	if !readJSON(w, r, &body) {
		return
	}
	p, err := a.addTranscodeProfile(r.Context(), body.apply(newTranscodeProfile()))
	if a.editAnswer(w, err) {
		return
	}
	a.writeTranscodeProfile(w, r, http.StatusCreated, p.Name)
}

func (a *Admin) apiChangeTranscodeProfile(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	name := r.PathValue("name")
	var body apiTranscodeProfileChange
	if !readJSON(w, r, &body) {
		return
	}
	if _, fixed := streamprofile.Find(name); fixed {
		a.editAnswer(w, transcodeError(store.ErrProfileFixed, name))
		return
	}
	old, err := a.store.TranscodeProfile(r.Context(), name)
	if errors.Is(err, store.ErrNoSuchProfile) {
		err = transcodeError(store.ErrNotFound, name)
	}
	if a.editAnswer(w, err) {
		return
	}
	p, err := a.changeTranscodeProfile(r.Context(), name, body.apply(old))
	if a.editAnswer(w, err) {
		return
	}
	a.writeTranscodeProfile(w, r, http.StatusOK, p.Name)
}

func (a *Admin) apiRemoveTranscodeProfile(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	if a.editAnswer(w, a.removeTranscodeProfile(r.Context(), r.PathValue("name"))) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// transcodeProfileChoices names the profiles that a setting can name, for an
// error.
func (a *Admin) transcodeProfileChoices(ctx context.Context) string {
	ps, err := a.allTranscodeProfiles(ctx)
	if err != nil {
		return profileNames()
	}
	names := make([]string, len(ps))
	for i, p := range ps {
		names[i] = p.Name
	}
	return strings.Join(names, ", ")
}

// liveTranscodeText says what live TV's own setting does, for its form.
func liveTranscodeText(t store.Transcode, describe func(string) string) string {
	switch {
	case t.Never:
		return "Never: every channel goes as its source sends it, whatever a device's profile, unless its group or the channel says otherwise."
	case t.Profile != "":
		return fmt.Sprintf("Always: channels are transcoded with %s (%s) for devices that would get them as their sources send them, unless their group or the channel says otherwise. Devices whose own profile transcodes keep theirs.",
			t.Profile, describe(t.Profile))
	}
	return "As needed: a channel is transcoded only when its device's or account's profile says so, as before, unless its group or the channel says otherwise."
}
