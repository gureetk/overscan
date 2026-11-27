// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	mathrand "math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"overscan.gureet.ca/internal/adapters/folder"
	"overscan.gureet.ca/internal/media"
	"overscan.gureet.ca/internal/store"
	"overscan.gureet.ca/internal/streamprofile"
	"overscan.gureet.ca/internal/subtitles"
)

// Films and episodes that a box cannot play as they are (D68). Before a
// film or an episode plays, the hub knows what its file holds: ffprobe
// reads it the first time it plays, through the session's own turn with
// its source, and the store keeps what it found. The device's profile then
// says whether the box plays the file as it is. If it cannot play its
// format or its sound, ffmpeg copies the video into an MPEG-TS stream with
// every sound track, converting those the box cannot play to Dolby
// Digital. Such a stream has no byte positions to seek to, so the device
// asks for it from a time, and ffmpeg starts again there; first it asks
// where the stream will truly start, at the picture at or before that
// time, so that it can count positions from there.

const (
	// maxConverts caps the streams that ffmpeg makes of films and
	// episodes at once.
	maxConverts = 6

	// probeTimeout caps reading what a file holds, and keyframeTimeout
	// finding where a converted stream starts: the MAG page waits 15 s
	// for an answer.
	probeTimeout    = 12 * time.Second
	keyframeTimeout = 10 * time.Second

	// maxProbeOutput caps what ffprobe may answer.
	maxProbeOutput = 4 << 20

	// convertDrain is how long ffmpeg may take to stop once its stream is
	// no longer wanted.
	convertDrain = 5 * time.Second
)

// convertFirstOutput is how long ffmpeg may take to make the start of a
// stream before the hub stops it.
var convertFirstOutput = 30 * time.Second

// ErrLimit is why a stream cannot start: its source is at its connection
// limit, and none of the device's own streams there can make room.
var ErrLimit = errors.New("the source is at its connection limit")

// ErrAccountLimit is why a stream cannot start: the device's account plays
// as many streams as it may at once, and none of the device's own streams
// can make room (D102).
var ErrAccountLimit = errors.New("the account plays as many streams as it may")

// UseFFprobe gives the hub the ffprobe program, path, that reads what the
// files of films and episodes hold. With it and ffmpeg, the hub converts
// what a box cannot play; without, files play as they are. It is called
// before the hub serves anything.
func (h *Hub) UseFFprobe(path string) { h.ffprobe = path }

// CanConvert reports whether the hub converts films and episodes that a
// box cannot play.
func (h *Hub) CanConvert() bool { return h.ffmpeg != "" && h.ffprobe != "" }

// Playback is how a device plays a film or an episode.
type Playback struct {
	// Path is the link's path: /vod/<token>.<ext> for the file as it is,
	// or /vod/<token>.ts for the stream that ffmpeg makes of it.
	Path string
	// Converted says that the link plays through ffmpeg. The device then
	// seeks by asking for the stream from a time, with ?t= in seconds,
	// after asking StartPath, with the same ?t=, where it truly starts.
	Converted bool
	StartPath string
	// Transcoded, with Converted, says that the source's server
	// transcodes it (D71), with one sound track at a time: the device
	// picks one of Tracks by asking for the stream again with ?a= and its
	// place among them.
	Transcoded bool
	// Duration is how long it lasts, in seconds, 0 if not known.
	Duration float64
	// Tracks are the sound tracks of a converted stream, in its order.
	Tracks []media.Track
	// Subtitles are the subtitle tracks that the device may load, as SRT,
	// at their paths: with ?t= and the time a converted stream starts at,
	// they come moved earlier by it (D69).
	Subtitles []PlaybackSubtitle
}

// PlaybackSubtitle is a subtitle track that a device may load over a film
// or an episode.
type PlaybackSubtitle struct {
	Path  string // /vod/<token>/sub/<n>.srt
	Track subtitles.Track
}

// Prepare returns how a device whose player plays profile plays a film or
// an episode, with a new link, and the subtitle tracks subs that it may
// load. A file whose contents the hub does not know yet is read first,
// through the session that the link starts, so that the read counts
// against the source's connections: Prepare fails with ErrLimit if the
// source has none free. A file that cannot be read, or a hub without ffmpeg
// and ffprobe, plays as it is.
func (h *Hub) Prepare(ctx context.Context, deviceID int64, item store.PlayableItem, profile media.Profile,
	subs []subtitles.Track) (Playback, error) {
	c := h.itemClaims(deviceID, item)
	token := h.sign(c)
	direct := Playback{Path: "/vod/" + token + "." + item.Container}
	// withSubtitles gives pb the subtitle tracks, in the session that
	// serves them, which it opens if need be.
	withSubtitles := func(pb Playback, s *session, token string) (Playback, error) {
		if len(subs) == 0 {
			return pb, nil
		}
		if s == nil {
			var err error
			if s, err = h.openItem(ctx, c, token, item); err != nil {
				return Playback{}, err
			}
		}
		s.subtitles.Store(&subs)
		for i, t := range subs {
			pb.Subtitles = append(pb.Subtitles, PlaybackSubtitle{Path: "/vod/" + token + "/sub/" + strconv.Itoa(i) + ".srt", Track: t})
		}
		return pb, nil
	}
	if !h.CanConvert() {
		return withSubtitles(direct, nil, token)
	}
	src := h.source(item.SourceID).Name
	titleID, episodeID := item.MediaKey()
	info, known, err := h.store.Media(ctx, titleID, episodeID)
	if err != nil {
		h.log.Warn("cannot look up what a title's file holds", "title", item.Name, "error", err)
	}
	var size, mtime int64
	if item.Folder != "" {
		// A file in a folder is read again when it has changed.
		if size, mtime, err = fileVersion(item); err != nil {
			return withSubtitles(direct, nil, token) // the player's request says what is wrong
		}
		known = known && info.Size == size && info.ModTime == mtime
	}

	// What was kept before D73 does not say whether the video has a high
	// dynamic range, nor its frame rate, which its transcode needs: such a
	// file is read again before ffmpeg transcodes it.
	stale := known && info.Probed < media.ProbeVersion && h.transcodeLimit(ctx, item, media.Decide(info, profile)) > 0

	var s *session
	if !known || stale {
		if s, err = h.openItem(ctx, c, token, item); err != nil {
			return Playback{}, err
		}
		started := h.now()
		read, perr := h.probe(ctx, s)
		switch {
		case perr != nil && stale:
			if ctx.Err() == nil {
				h.log.Warn("cannot read again what a title's file holds; it plays as Overscan read it before", "title", item.Name,
					"source", src, "error", perr)
			}
		case perr != nil:
			if ctx.Err() == nil {
				h.log.Warn("cannot read what a title's file holds, so it plays as it is", "title", item.Name,
					"source", src, "error", perr)
			}
			return withSubtitles(direct, s, token)
		default:
			info = read
			if item.Folder != "" {
				info.Size, info.ModTime = size, mtime
			}
			h.log.Info("read what a title's file holds", "title", item.Name, "source", src, "file", info.Summary(),
				"took", h.now().Sub(started).Round(10*time.Millisecond))
			if err := h.store.SetMedia(ctx, titleID, episodeID, info, h.now()); err != nil {
				h.log.Warn("cannot keep what a title's file holds", "title", item.Name, "error", err)
			}
		}
	}

	plan := media.Decide(info, profile)
	limit := h.transcodeLimit(ctx, item, plan)
	// What is transcoded of the title (D150): its own setting, its
	// shelf's, or films' and series'.
	tc, tcBy := h.titleTranscode(ctx, item)
	var always streamprofile.Profile
	if tc.Profile != "" && info.First(media.Video) != nil {
		always, _ = h.alwaysProfile(ctx, item, tc, tcBy)
	}
	busy := func(most string) (Playback, error) {
		h.log.Warn("a title's video must be transcoded, and Overscan transcodes as many titles as it may at once, so it cannot start",
			"title", item.Name, "source", src, "device", deviceID, "most", most)
		if s != nil {
			h.mu.Lock()
			h.end(s, "the server transcodes as many titles as it may at once")
			h.mu.Unlock()
		}
		return Playback{}, ErrBusy
	}
	switch {
	case always.Made:
		// Its setting transcodes it always with a profile, whether or not
		// the box plays it.
		if !h.roomToEncode(ctx, deviceID, always) {
			return busy(h.encodeMost(ctx, always))
		}
		plan = media.ProfilePlan(info, profile, always, h.encoder.ToneMap)
	case tc.Never && plan.VideoNote != "":
		h.log.Warn("the box may not play a title's video, which is set never to be transcoded, so it plays as it is",
			"title", item.Name, "source", src, "video", plan.VideoNote, "profile", profile.Name, "set_by", tcBy)
	case plan.VideoNote != "" && item.TranscodeURL != "" && h.serverTranscodes(ctx):
		// The source's server transcodes it (D71).
		plan = media.TranscodePlan(info, profile)
	case limit > 0:
		// This server's ffmpeg transcodes it (D73), if it may now: on the
		// graphics, or in software.
		if !h.roomToEncode(ctx, deviceID, media.D73Profile) {
			return busy(h.encodeMost(ctx, media.D73Profile))
		}
		plan = media.EncodePlan(info, profile, h.encoder.ToneMap)
		if v := info.First(media.Video); v != nil && v.HDR != "" && !plan.ToneMap && !h.gpuMapsHDR(v) {
			h.log.Warn("this server's ffmpeg cannot map a title's high dynamic range to the standard one, so its colours may look washed out",
				"title", item.Name, "source", src)
		}
	case plan.VideoNote != "" && item.TranscodeURL != "":
		h.log.Warn("the box may not play a title's video, which its source's server is set not to transcode, so it plays as it is",
			"title", item.Name, "source", src, "video", plan.VideoNote, "profile", profile.Name)
	case plan.VideoNote != "" && h.CanTranscode():
		h.log.Warn("the box may not play a title's video, which Overscan is not set to transcode, so it plays as it is",
			"title", item.Name, "source", src, "video", plan.VideoNote, "profile", profile.Name)
	case plan.VideoNote != "":
		h.log.Warn("the box may not play a title's video, which Overscan does not convert, so it plays as it is",
			"title", item.Name, "source", src, "video", plan.VideoNote, "profile", profile.Name)
	case plan.Convert && !h.convertsSound(ctx):
		h.log.Warn("the box may not play a title, which Overscan is set not to convert, so it plays as it is",
			"title", item.Name, "source", src, "why", plan.Why, "profile", profile.Name)
		plan = media.Plan{Video: -1}
	case plan.Why != "" && !plan.Convert:
		h.log.Warn("the box may not play a title, and ffmpeg cannot copy its video, so it plays as it is",
			"title", item.Name, "source", src, "why", plan.Why, "profile", profile.Name)
	}
	pb := direct
	pb.Duration = info.Duration
	if !plan.Convert && !plan.Transcode {
		if s != nil {
			s.info.Store(&info)
		}
		return withSubtitles(pb, s, token)
	}
	if s == nil {
		if s, err = h.openItem(ctx, c, token, item); err != nil {
			return Playback{}, err
		}
	}
	s.info.Store(&info)
	s.plan.Store(&plan)
	c.convert, c.transcode = true, plan.Transcode
	token = h.sign(c)
	switch {
	case plan.Transcode:
		h.log.Info("a title plays through its source's server, which transcodes what the box cannot play", "title", item.Name,
			"source", src, "device", deviceID, "why", plan.Why, "sound", trackSummary(plan), "profile", profile.Name)
	case plan.Encode && plan.Profile.Made:
		h.log.Info("a title plays through ffmpeg, which transcodes it as its setting says", "title", item.Name,
			"source", src, "device", deviceID, "with", plan.Profile.Name, "set_by", tcBy, "video", encodeSummary(plan),
			"sound", trackSummary(plan), "profile", profile.Name)
	case plan.Encode:
		h.log.Info("a title plays through ffmpeg, which transcodes its video for the box", "title", item.Name,
			"source", src, "device", deviceID, "why", plan.Why, "video", encodeSummary(plan),
			"sound", trackSummary(plan), "profile", profile.Name)
	default:
		h.log.Info("a title plays through ffmpeg, which converts what the box cannot play", "title", item.Name,
			"source", src, "device", deviceID, "why", plan.Why, "sound", trackSummary(plan), "profile", profile.Name)
	}
	return withSubtitles(Playback{Path: "/vod/" + token + ".ts", Converted: true, Transcoded: plan.Transcode,
		StartPath: "/vod/" + token + "/start", Duration: info.Duration, Tracks: plan.Tracks}, s, token)
}

// itemClaims returns the claims of a new link that plays a film or an
// episode for a device.
func (h *Hub) itemClaims(deviceID int64, item store.PlayableItem) claims {
	kind := kindMovie
	if item.Kind == store.KindEpisode {
		kind = kindEpisode
	}
	return claims{
		kind:     kind,
		entryID:  item.ID,
		deviceID: deviceID,
		expires:  h.now().Add(linkTTL).Unix(),
		nonce:    mathrand.Uint64(),
	}
}

// itemSession returns a new session that plays item, for admit.
func itemSession(token string, item store.PlayableItem) *session {
	titleID, episodeID := item.MediaKey()
	return &session{token: token, sourceID: item.SourceID, channel: item.Name, streamURL: item.StreamURL,
		folder: item.Folder, file: item.File, mediaKey: [2]int64{titleID, episodeID}, transcodeURL: item.TranscodeURL}
}

// fileVersion returns the size and time of a film's or an episode's file
// in a folder, which change when the file does.
func fileVersion(item store.PlayableItem) (size, mtime int64, err error) {
	f, err := folder.OpenVideo(item.Folder, item.File)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, 0, err
	}
	return st.Size(), st.ModTime().UnixNano(), nil
}

// trackSummary describes a converted stream's sound tracks for the log,
// such as "dts 6ch eng to ac3, aac 2ch fre".
func trackSummary(plan media.Plan) string {
	parts := make([]string, 0, len(plan.Tracks))
	for _, t := range plan.Tracks {
		d := media.Stream{Type: media.Audio, Codec: t.Codec, Channels: t.Channels, Language: t.Language}.Describe()
		if t.Convert {
			d += " to " + plan.SoundCodec()
		}
		parts = append(parts, d)
	}
	return strings.Join(parts, ", ")
}

// probe reads what s's file holds, with ffprobe, in a turn of s.
func (h *Hub) probe(ctx context.Context, s *session) (media.Info, error) {
	ctx, release, ok := h.takeTurn(ctx, s, nil)
	if !ok {
		return media.Info{}, errors.New("the session ended")
	}
	defer release()
	h.begin(s)
	defer h.finish(s)
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	in, closeIn, err := h.openInput(ctx, s)
	if err != nil {
		return media.Info{}, err
	}
	defer closeIn()
	out, err := h.runProbe(ctx, media.ProbeArgs(in))
	if err != nil {
		return media.Info{}, err
	}
	return media.ParseProbe(out)
}

// keyframe returns where a converted stream of s asked for from the time
// at truly starts, in a turn of s that the caller holds.
func (h *Hub) keyframe(ctx context.Context, s *session, plan *media.Plan, info *media.Info, at float64) (float64, error) {
	ctx, cancel := context.WithTimeout(ctx, keyframeTimeout)
	defer cancel()
	in, closeIn, err := h.openInput(ctx, s)
	if err != nil {
		return 0, err
	}
	defer closeIn()
	out, err := h.runProbe(ctx, info.KeyframeArgs(in, plan.Video, at))
	if err != nil {
		return 0, err
	}
	k, ok := info.ParseKeyframe(out)
	if !ok {
		return 0, errors.New("ffprobe found no picture there")
	}
	return k, nil
}

// runProbe runs ffprobe with args and returns what it writes.
func (h *Hub) runProbe(ctx context.Context, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, h.ffprobe, args...)
	cmd.Env = []string{} // nothing from Overscan's environment
	cmd.Dir = "/"
	cmd.WaitDelay = time.Second
	keepChild(cmd)
	stdout, stderr := &capped{max: maxProbeOutput}, &capped{max: 4 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("ffprobe: %w", ctx.Err())
		}
		msg := strings.TrimSpace(stderr.buf.String())
		if i := strings.LastIndexByte(msg, '\n'); i >= 0 {
			msg = msg[i+1:]
		}
		return nil, fmt.Errorf("ffprobe: %w: %s", err, msg)
	}
	if stdout.over {
		return nil, errors.New("ffprobe said too much")
	}
	return stdout.buf.Bytes(), nil
}

// capped keeps what is written to it, up to max bytes.
type capped struct {
	max  int
	buf  bytes.Buffer
	over bool
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); len(p) > room {
		c.over = true
		c.buf.Write(p[:max(room, 0)])
	} else {
		c.buf.Write(p)
	}
	return len(p), nil
}

// timeParam returns the time that a request's t parameter asks for, in
// seconds from the file's start: 0 if there is none, and false if it makes
// no sense.
func timeParam(r *http.Request, info *media.Info) (float64, bool) {
	v := r.URL.Query().Get("t")
	if v == "" {
		return 0, true
	}
	t, err := strconv.ParseFloat(v, 64)
	if err != nil || t < 0 || t > 1e6 || math.IsNaN(t) {
		return 0, false
	}
	if info != nil && info.Duration > 0 && t > info.Duration {
		t = info.Duration
	}
	return t, true
}

// serveStart serves /vod/<token>/start?t=<seconds>: where a converted
// stream asked for from that time truly starts, as {"t": seconds}.
func (h *Hub) serveStart(w http.ResponseWriter, r *http.Request, s *session) {
	plan, info := s.plan.Load(), s.info.Load()
	at, ok := timeParam(r, info)
	if !ok {
		http.Error(w, "t must be a time in seconds", http.StatusBadRequest)
		return
	}
	start := at
	// The source's server transcodes from the very time asked (D71), and
	// so does ffmpeg (D73); ffmpeg copies from a picture.
	if at > 0 && plan.Video >= 0 && !plan.Transcode && !plan.Encode {
		ctx, release, ok := h.takeTurn(r.Context(), s, nil)
		if !ok {
			return
		}
		h.begin(s)
		k, err := h.keyframe(ctx, s, plan, info, at)
		h.finish(s)
		release()
		switch {
		case err == nil:
			start = k
		case r.Context().Err() == nil:
			h.log.Info("cannot find where a converted title starts from a time; it starts there", "title", s.channel,
				"source", h.source(s.sourceID).Name, "at", at, "error", err)
		}
	}
	// To the millisecond after, as ffmpeg's -ss takes it, so that the
	// stream asked for from this time starts at the same picture.
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		T float64 `json:"t"`
	}{T: math.Ceil(start*1000-1e-6) / 1000})
}

// serveConverted serves /vod/<token>.ts?t=<seconds>: the stream that
// ffmpeg makes of a session's file, from the picture at or before that
// time. A new request ends the one before, as a seek does.
func (h *Hub) serveConverted(w http.ResponseWriter, r *http.Request, s *session) {
	plan, info := s.plan.Load(), s.info.Load()
	if plan.Transcode {
		h.serveTranscoded(w, r, s, plan, info)
		return
	}
	from, ok := timeParam(r, info)
	if !ok {
		http.Error(w, "t must be a time in seconds", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "video/mp2t")
	w.Header().Set("Accept-Ranges", "none")
	if r.Method == http.MethodHead {
		return
	}
	ctx, release, ok := h.takeTurn(r.Context(), s, w)
	if !ok {
		return
	}
	defer release()
	h.begin(s)
	defer h.finish(s)
	src := h.source(s.sourceID).Name
	about := []any{"title", s.channel, "source", src, "device", s.deviceID}
	if h.convertsRunning.Add(1) > maxConverts {
		h.convertsRunning.Add(-1)
		h.log.Warn("too many titles are being converted; this one cannot start", append(about, "most", maxConverts)...)
		http.Error(w, "the server is converting as many titles as it can", http.StatusServiceUnavailable)
		return
	}
	defer h.convertsRunning.Add(-1)
	// The ways to make the stream: for a transcode, on the graphics and
	// then in software (D150); otherwise ffmpeg's conversion alone.
	attempts := []filmAttempt{{plan: *plan, take: func() bool { return true }, give: func() {}}}
	if plan.Encode {
		attempts = h.filmAttempts(ctx, s, plan, info)
	}
	s.converting.Add(1)
	defer s.converting.Add(-1)

	in, closeIn, err := h.openInput(ctx, s)
	if err != nil {
		h.log.Error("cannot give ffmpeg a title's file", append(about, "error", err)...)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer closeIn()
	for i, a := range attempts {
		if !a.take() {
			continue
		}
		instead := ""
		if i+1 < len(attempts) {
			instead = attempts[i+1].instead()
		}
		failed := h.runConvert(ctx, w, s, &a, in, from, about, instead)
		a.give()
		if !failed {
			return
		}
	}
	h.log.Warn("Overscan transcodes as many titles as it may at once; this one cannot start",
		append(about, "most", h.encodeMost(ctx, plan.EncodeProfile()))...)
	http.Error(w, "the server transcodes as many titles as it may at once", http.StatusServiceUnavailable)
}

// instead says, for the log, what a way to transcode a title does when it
// takes over from the one before it.
func (a filmAttempt) instead() string {
	switch {
	case !a.gpu:
		return "it is transcoded in software"
	case a.plan.Hardware != nil && !a.plan.Hardware.Decode:
		return "it is transcoded on the graphics, decoded in software"
	}
	return "it is transcoded on the graphics"
}

// runConvert runs ffmpeg to make s's stream as a says, from the time from,
// and sends it to the player. Where ffmpeg fails before sending anything
// and instead says what the next way does, it logs so, answers nothing,
// and reports true, for the next way to take over.
func (h *Hub) runConvert(ctx context.Context, w http.ResponseWriter, s *session, a *filmAttempt, in string, from float64,
	about []any, instead string) (failed bool) {
	plan := &a.plan
	if plan.Encode {
		s.encoding.Add(1)
		defer s.encoding.Add(-1)
		if a.gpu {
			s.graphics.Add(1)
			defer s.graphics.Add(-1)
			decode := "software"
			if plan.EncodeProfile().DecodesOnGPU(*plan.Hardware) {
				decode = "graphics"
			}
			about = append(slices.Clip(about), "on", "graphics", "decode", decode)
		}
		if plan.Profile.Made {
			about = append(slices.Clip(about), "with", plan.Profile.Name)
		}
	}
	args := plan.Args(in, from)
	// ffmpeg says how far a transcode has got, so that the log can say
	// whether it keeps up.
	var progressIn, progressOut *os.File
	var err error
	if plan.Encode {
		if progressIn, progressOut, err = os.Pipe(); err != nil {
			h.log.Error("cannot start ffmpeg to transcode a title", append(about, "error", err)...)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return false
		}
		defer progressIn.Close()
		args = append([]string{"-progress", progressFD}, args...)
	}
	fctx, stop := context.WithCancel(ctx)
	defer stop()
	cmd := exec.CommandContext(fctx, h.ffmpeg, args...)
	cmd.Env = []string{} // nothing from Overscan's environment
	cmd.Dir = "/"
	cmd.WaitDelay = convertDrain
	messages := &messageTail{log: h.log, about: about}
	cmd.Stderr = messages
	if progressOut != nil {
		cmd.ExtraFiles = []*os.File{progressOut}
	}
	keepChild(cmd)
	stdout, err := cmd.StdoutPipe()
	if err == nil {
		err = cmd.Start()
	}
	if progressOut != nil {
		progressOut.Close() // ffmpeg has its own, and the progress ends with it
	}
	if err != nil {
		h.log.Error("cannot start ffmpeg to convert a title", append(about, "error", err)...)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return false
	}
	var progress *encodeProgress
	if plan.Encode {
		progress = followProgress(progressIn)
		h.log.Info("transcoding a title with ffmpeg", append(about, "from", clockTime(from))...)
	} else {
		h.log.Info("converting a title with ffmpeg", append(about, "from", clockTime(from))...)
	}

	rc := http.NewResponseController(w)
	buf := make([]byte, copyBufferBytes)
	sent := false
	// A player left waiting for a stream that never comes can hang a box:
	// ffmpeg that makes nothing in a while is stopped.
	var tooSlow atomic.Bool
	first := time.AfterFunc(convertFirstOutput, func() {
		tooSlow.Store(true)
		stop()
	})
	defer first.Stop()
	for {
		waiting := time.Now()
		n, rerr := stdout.Read(buf)
		if progress != nil {
			progress.read(n, time.Since(waiting))
			h.judgeEncode(progress, about)
		}
		if n > 0 {
			if !sent {
				first.Stop()
			}
			sent = true
			if _, werr := w.Write(buf[:n]); werr != nil {
				stop() // the player went away
				break
			}
			_ = rc.Flush()
		}
		if rerr != nil {
			break
		}
	}
	werr := cmd.Wait()
	if progress != nil && sent {
		h.encodeEnded(progress, about)
	}
	switch {
	case tooSlow.Load() && !sent && ctx.Err() == nil:
		h.log.Warn("ffmpeg made nothing of a title in a while, so the hub stopped it; the player can ask again",
			append(about, "waited", convertFirstOutput, "ffmpeg", messages.String())...)
		http.Error(w, "the title took too long to start", http.StatusGatewayTimeout)
	case ctx.Err() != nil || fctx.Err() != nil:
		// The player went away, or asked for the stream from elsewhere.
	case werr != nil && !sent && instead != "":
		h.log.Warn("ffmpeg could not transcode a title "+a.how+"; "+instead,
			append(about, "error", werr, "ffmpeg", messages.String())...)
		return true
	case werr != nil && !sent:
		h.log.Warn("ffmpeg could not convert a title", append(about, "error", werr, "ffmpeg", messages.String())...)
		http.Error(w, "the title cannot be converted", http.StatusBadGateway)
	case werr != nil:
		h.log.Warn("ffmpeg stopped converting a title", append(about, "error", werr, "ffmpeg", messages.String())...)
	}
	return false
}

// clockTime writes a time in seconds as h:mm:ss for the log.
func clockTime(t float64) string {
	d := time.Duration(t) * time.Second
	return fmt.Sprintf("%d:%02d:%02d", int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60)
}

// checkSize drops what is kept of a session's file when its source's
// answer shows that the file has changed size since it was read: it is
// read again when it next plays.
func (h *Hub) checkSize(s *session, resp *http.Response) {
	info := s.info.Load()
	if info == nil || info.Size <= 0 {
		return
	}
	total := int64(-1)
	if cr := resp.Header.Get("Content-Range"); cr != "" {
		if _, after, ok := strings.Cut(cr, "/"); ok {
			if n, err := strconv.ParseInt(after, 10, 64); err == nil {
				total = n
			}
		}
	} else if resp.StatusCode == http.StatusOK {
		total = resp.ContentLength
	}
	if total <= 0 || total == info.Size || s.changed.Swap(true) {
		return
	}
	h.log.Info("a title's file has changed since Overscan read what it holds; it is read again when it next plays",
		"title", s.channel, "source", h.source(s.sourceID).Name, "size_was", info.Size, "size", total)
	if err := h.store.ForgetMedia(context.Background(), s.mediaKey[0], s.mediaKey[1]); err != nil {
		h.log.Warn("cannot drop what is kept of a title's file", "title", s.channel, "error", err)
	}
}
