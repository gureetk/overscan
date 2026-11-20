// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"overscan.gureet.ca/internal/store"
	"overscan.gureet.ca/internal/streamprofile"
)

// Output profiles (D110), as the admin's pages and the API name them.

// profileNames lists the output profiles' names, for an error.
func profileNames() string {
	names := make([]string, len(streamprofile.Profiles))
	for i, p := range streamprofile.Profiles {
		names[i] = p.Name
	}
	return strings.Join(names, ", ")
}

// profileText says how a profile sends live channels, after "get live
// channels", such as "as their sources send them".
func profileText(name string) string {
	p, ok := streamprofile.Find(name)
	if !ok || !p.Transcodes() {
		return "as their sources send them"
	}
	return "made " + p.Label + " by this server"
}

// profileOption is an output profile as a form offers it.
type profileOption struct {
	Name, Label string
}

// profileOptions are the output profiles as the forms offer them.
func profileOptions() []profileOption {
	out := make([]profileOption, len(streamprofile.Profiles))
	for i, p := range streamprofile.Profiles {
		out[i] = profileOption{p.Name, p.Label}
	}
	return out
}

// liveTranscodes reports whether this server transcodes live channels to
// their output profiles now: its ffmpeg can, and transcoding is on.
func (a *Admin) liveTranscodes(ctx context.Context) (bool, error) {
	can, _, _ := a.transcoder()
	n, err := a.store.Transcodes(ctx)
	return can && n > 0, err
}

// transcodeNote says why live channels play as their sources send them
// whatever their output profiles, "" if they need not.
func (a *Admin) transcodeNote(ctx context.Context) (string, error) {
	can, _, missing := a.transcoder()
	if !can {
		return "This server cannot transcode: it lacks " + missing + ", so every stream plays as its source sends it.", nil
	}
	n, err := a.store.Transcodes(ctx)
	if err != nil || n > 0 {
		return "", err
	}
	return "Transcoding is off on the Settings page, so every stream plays as its source sends it until it is on.", nil
}

// setDeviceOutputProfile sets a device's own output profile, "" for its
// account's.
func (a *Admin) setDeviceOutputProfile(ctx context.Context, mac, name string) (store.Device, error) {
	d, err := a.store.SetDeviceOutputProfile(ctx, mac, name)
	switch {
	case errors.Is(err, store.ErrNoSuchProfile):
		return store.Device{}, badDevice(http.StatusBadRequest, "bad_output_profile", "Choose an output profile: %s.", profileNames())
	case err != nil:
		return store.Device{}, deviceStoreError(err, mac)
	}
	a.log.Info("a device's output profile set in the admin", "mac", d.MAC, "output_profile", name)
	return d, nil
}

// deviceProfileText says how a device gets live channels, after "gets live
// channels".
func deviceProfileText(d store.Device) string {
	if d.OutputProfile == "" {
		return "as its account's output profile says"
	}
	p, _ := streamprofile.Find(d.OutputProfile)
	if !p.Transcodes() {
		return "as their sources send them"
	}
	return "made " + p.Label + " by this server"
}
