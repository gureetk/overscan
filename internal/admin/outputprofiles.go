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

// profileNames lists the fixed output profiles' names, for an error: the
// profiles that the administrator made can be named too (D147).
func profileNames() string {
	names := make([]string, len(streamprofile.Profiles))
	for i, p := range streamprofile.Profiles {
		names[i] = p.Name
	}
	return strings.Join(names, ", ") + ", or one made on the Transcoding page"
}

// profileText says how a profile sends live channels, after "get live
// channels", such as "as their sources send them".
func profileText(name string) string {
	p, fixed := streamprofile.Find(name)
	switch {
	case fixed && p.Transcodes():
		return "made " + p.Label + " by this server"
	case fixed || name == "":
		return "as their sources send them"
	}
	return "transcoded with " + name + " by this server"
}

// profileOption is an output profile as a form offers it.
type profileOption struct {
	Name, Label string
}

// profileOptions are the output profiles as the forms offer them: the
// fixed ones, then those that the administrator made.
func (a *Admin) profileOptions(ctx context.Context) []profileOption {
	ps, err := a.allTranscodeProfiles(ctx)
	if err != nil {
		a.log.Error("cannot list the profiles", "error", err)
		ps = streamprofile.Profiles
	}
	out := make([]profileOption, len(ps))
	for i, p := range ps {
		out[i] = profileOption{p.Name, p.Label}
		if p.Made {
			out[i].Label = p.Name + ": " + p.Describe()
		}
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
	return profileText(d.OutputProfile)
}
