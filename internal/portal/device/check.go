// SPDX-License-Identifier: AGPL-3.0-or-later

package device

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode"

	"overscan.gureet.ca/internal/media"
	"overscan.gureet.ca/internal/playcheck"
	"overscan.gureet.ca/internal/store"
)

// The playback check (D70): the page plays clips that ffmpeg made on the
// server and asks after each whether it plays; the server keeps what it
// found, and plays films for the box by it.

const (
	// clipTTL is how long the links to the check's clips last.
	clipTTL = 2 * time.Hour
	// clipTokenVersion is the first byte of a clip link's token, apart from
	// those of session tokens.
	clipTokenVersion = 3
	clipClaimsLen    = 1 + 8 + 8 // version, device ID, expiry
	// maxCheckBytes caps the body of PUT check, which carries an answer
	// for each clip.
	maxCheckBytes = 32 << 10
	// maxFirmwareLen caps the firmware version that a box reports.
	maxFirmwareLen = 64
	// maxPlayerInfo caps what the box's player said of a clip's picture,
	// and of its sound.
	maxPlayerInfo = 300
	// maxPlayerEvents caps the player's events, as the box gave them, that
	// the page sends of a clip or of a film's start.
	maxPlayerEvents = 200
)

// clipTypes are the content types of the clips, by their extension.
var clipTypes = map[string]string{
	"mkv": "video/x-matroska",
	"mp4": "video/mp4",
	"ts":  "video/mp2t",
	"avi": "video/x-msvideo",
	"vob": "video/mpeg",
}

// UseChecks gives the API the clips of the playback check. Without them,
// the check is unavailable.
func (a *API) UseChecks(c *playcheck.Clips) { a.checks = c }

// checkClip is a clip of the check, as the page plays it.
type checkClip struct {
	ID string `json:"id"`
	// Label says what it tests, such as "H.265 at 10 bits".
	Label string `json:"label"`
	// Ask is what the page asks once it plays: whether the picture moves
	// (picture), whether the tone sounds (sound), or both.
	Ask      string `json:"ask"`
	URL      string `json:"url"`
	Solution string `json:"solution"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

// checkResponse says whether the box can be checked, and how.
type checkResponse struct {
	// State is "ready" once the clips are made, "preparing" while they
	// are, and "unavailable" if the server cannot make them.
	State string `json:"state"`
	// Ready of Total clips are made.
	Ready int `json:"ready"`
	Total int `json:"total"`
	// Checked is when the box was last checked, in Unix seconds; 0 if never.
	Checked int64 `json:"checked"`
	// Clips are the clips to play, in order, once they are ready.
	Clips []checkClip `json:"clips,omitempty"`
}

// getCheck answers GET check: whether the box can be checked, and the
// clips to play once they are made. The first call has them made.
func (a *API) getCheck(w http.ResponseWriter, r *http.Request, d store.Device) {
	resp := checkResponse{State: "unavailable"}
	c, err := a.store.PlaybackCheck(r.Context(), d.ID)
	switch {
	case err == nil:
		resp.Checked = c.CheckedAt.Unix()
	case !errors.Is(err, store.ErrNotFound):
		a.internalError(w, "read a playback check", err)
		return
	}
	if a.checks == nil {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	p := a.checks.Prepare()
	resp.Ready, resp.Total = p.Ready, p.Total
	switch {
	case p.Err != nil:
	case !p.Done:
		resp.State = "preparing"
	default:
		resp.State = "ready"
		token := a.signClip(d.ID, a.now().Add(clipTTL).Unix())
		for _, clip := range a.checks.Made() {
			resp.Clips = append(resp.Clips, checkClip{
				ID: clip.ID, Label: clip.Label, Ask: clip.Ask, Solution: fileSolution,
				URL:   "http://" + r.Host + Prefix + "check/" + token + "/" + clip.ID + "." + clip.Ext,
				Width: clip.Width, Height: clip.Height,
			})
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// checkResult is what the page found of a clip.
type checkResult struct {
	ID string `json:"id"`
	// Answer is "yes" or "no", as the viewer answered, or "" if they were
	// not asked, or did not know.
	Answer string `json:"answer"`
	// Started and Failed say whether the box said that it started playing
	// the clip, or could not open it.
	Started bool `json:"started"`
	Failed  bool `json:"failed"`
	// Ms is how long it took to start, in milliseconds.
	Ms int `json:"ms"`
	// Width and Height are the size of the picture as the box reported it,
	// 0 if it did not.
	Width  int `json:"width"`
	Height int `json:"height"`
	// Tracks is how many sound tracks the box listed.
	Tracks int `json:"tracks"`
	// Position is where the box said it got to, in seconds, when the viewer
	// answered.
	Position float64 `json:"position"`
	// Video and Audio are what the box's player said of the picture and
	// the sound, as it said it, for what a later check might read from
	// them.
	Video string `json:"video"`
	Audio string `json:"audio"`
	// Events are the player's events while the clip played, as the box
	// gave them, such as 4 2 7, or "4" when it gives them as text.
	Events string `json:"events,omitempty"`
}

// outcome returns what a result says of its clip: media.Plays,
// media.Fails, or "" if it says nothing. A clip that the box did not
// start, or could not open, does not play.
func (c checkResult) outcome() string {
	switch {
	case c.Answer == "yes":
		return media.Plays
	case c.Answer == "no", c.Failed, !c.Started:
		return media.Fails
	}
	return ""
}

// valid reports whether c is a result the page could have sent.
func (c checkResult) valid() bool {
	return (c.Answer == "" || c.Answer == "yes" || c.Answer == "no") &&
		c.Ms >= 0 && c.Ms <= 10*60*1000 &&
		c.Width >= 0 && c.Width <= 16384 && c.Height >= 0 && c.Height <= 16384 &&
		c.Tracks >= 0 && c.Tracks <= 64 &&
		!math.IsNaN(c.Position) && c.Position >= 0 && c.Position <= 24*3600 &&
		len(c.Video) <= maxPlayerInfo && len(c.Audio) <= maxPlayerInfo && len(c.Events) <= maxPlayerEvents &&
		strings.IndexFunc(c.Video, notPrintable) < 0 && strings.IndexFunc(c.Audio, notPrintable) < 0 &&
		strings.IndexFunc(c.Events, notPrintable) < 0
}

// checkRequest is the body of PUT check: what the page found of each clip
// that it played.
type checkRequest struct {
	// Firmware is the box's firmware version, if it says.
	Firmware string        `json:"firmware"`
	Results  []checkResult `json:"results"`
}

// checkFinding is a capability that the check found to play or not.
type checkFinding struct {
	Label string `json:"label"`
	Plays bool   `json:"plays"`
}

// checkSaved is what the server kept of a check.
type checkSaved struct {
	Checked  int64          `json:"checked"` // Unix seconds
	Findings []checkFinding `json:"findings"`
}

// putCheck answers PUT check: it keeps what the check found, which then
// decides how films and episodes play on the box, and answers with it. A
// check whose first clip did not play says nothing, and is refused.
func (a *API) putCheck(w http.ResponseWriter, r *http.Request, d store.Device) {
	var req checkRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || len(req.Results) > len(media.CheckClips) ||
		len(req.Firmware) > maxFirmwareLen || strings.IndexFunc(req.Firmware, notPrintable) >= 0 {
		writeError(w, http.StatusBadRequest, errBadRequest)
		return
	}
	outcomes := map[string]string{}
	for _, res := range req.Results {
		_, known := media.CheckClipByID(res.ID)
		if _, twice := outcomes[res.ID]; !known || twice || !res.valid() {
			writeError(w, http.StatusBadRequest, errBadRequest)
			return
		}
		outcomes[res.ID] = res.outcome()
	}
	if outcomes[media.CheckClips[0].ID] != media.Plays {
		writeError(w, http.StatusBadRequest, errBadRequest)
		return
	}
	results, _ := json.Marshal(req.Results) // checked results always marshal
	c := store.PlaybackCheck{DeviceID: d.ID, Model: d.Model, Firmware: req.Firmware,
		Findings: media.Findings(outcomes, media.ProfileFor(d.Model)), Results: results, CheckedAt: a.now()}
	if err := a.store.SetPlaybackCheck(r.Context(), c); err != nil {
		a.internalError(w, "keep a playback check", err)
		return
	}
	saved := checkSaved{Checked: c.CheckedAt.Unix(), Findings: findingsOf(c.Findings)}
	var plays, not []string
	for _, f := range saved.Findings {
		if f.Plays {
			plays = append(plays, f.Label)
		} else {
			not = append(not, f.Label)
		}
	}
	a.log.Info("a box's playback check found what it plays", "mac", d.MAC, "label", d.Label, "model", d.Model,
		"firmware", req.Firmware, "plays", strings.Join(plays, ", "), "does_not_play", strings.Join(not, ", "))
	writeJSON(w, http.StatusOK, saved)
}

// findingsOf lists findings in the order the check tests them, with their
// labels.
func findingsOf(found map[string]bool) []checkFinding {
	out := []checkFinding{}
	for _, k := range media.Capabilities() {
		if plays, ok := found[k]; ok {
			out = append(out, checkFinding{Label: media.CapabilityLabel(k), Plays: plays})
		}
	}
	return out
}

// notPrintable reports whether r has no place in a firmware version.
func notPrintable(r rune) bool { return !unicode.IsPrint(r) }

// checkFile answers GET check/{token}/{file}: a clip of the check, for the
// box's player, which sends no session token, so the link carries one of
// its own.
func (a *API) checkFile(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.verifyClip(r.PathValue("token")); !ok || a.checks == nil {
		writeError(w, http.StatusForbidden, errUnauthorized)
		return
	}
	id, ext, _ := strings.Cut(r.PathValue("file"), ".")
	clip, ok := media.CheckClipByID(id)
	if !ok || ext != clip.Ext {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	f, made, err := a.checks.Open(id)
	if err != nil {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", clipTypes[ext])
	http.ServeContent(w, r, r.PathValue("file"), made, f)
}

// signClip returns a token for the links to the check's clips of a
// device, which lasts until expires, in Unix seconds.
func (a *API) signClip(deviceID, expires int64) string {
	b := make([]byte, clipClaimsLen, clipClaimsLen+sha256.Size)
	b[0] = clipTokenVersion
	binary.BigEndian.PutUint64(b[1:], uint64(deviceID))
	binary.BigEndian.PutUint64(b[9:], uint64(expires))
	m := hmac.New(sha256.New, a.key)
	m.Write(b)
	return base64.RawURLEncoding.EncodeToString(m.Sum(b)[:clipClaimsLen+macLen])
}

// verifyClip returns the device of a clip link's token that this API
// signed, if it has not expired.
func (a *API) verifyClip(token string) (deviceID int64, ok bool) {
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(b) != clipClaimsLen+macLen || b[0] != clipTokenVersion {
		return 0, false
	}
	m := hmac.New(sha256.New, a.key)
	m.Write(b[:clipClaimsLen])
	if !hmac.Equal(m.Sum(nil)[:macLen], b[clipClaimsLen:]) {
		return 0, false
	}
	if a.now().Unix() >= int64(binary.BigEndian.Uint64(b[9:])) {
		return 0, false
	}
	return int64(binary.BigEndian.Uint64(b[1:])), true
}

// profile returns what a device's player plays (D70): its model's profile,
// changed by what its playback check found, or else by what the latest
// check of a box of its model found.
func (a *API) profile(ctx context.Context, d store.Device) media.Profile {
	base := media.ProfileFor(d.Model)
	c, err := a.store.PlaybackCheck(ctx, d.ID)
	if errors.Is(err, store.ErrNotFound) && d.Model != "" {
		c, err = a.store.LatestPlaybackCheck(ctx, d.Model)
	}
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			a.log.Warn("cannot read a playback check; the box plays by its model's profile", "mac", d.MAC, "error", err)
		}
		return base
	}
	name := c.Model + " (checked on " + c.CheckedAt.Format("2 January 2006") + ")"
	if c.DeviceID != d.ID {
		name = c.Model + " (as another box of the model was checked on " + c.CheckedAt.Format("2 January 2006") + ")"
	}
	return base.With(c.Findings, name)
}
