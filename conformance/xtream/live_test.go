// SPDX-License-Identifier: Apache-2.0

package xtream_test

import (
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Live TV, the guide and catch-up (notes, sections 3 to 6), with the
// fixture lineup: Example News (News, a guide, an archive of a day),
// Example Sports (Sports, a guide) and Example Weather (News, no guide).

type category struct {
	ID       flexString `json:"category_id"`
	Name     string     `json:"category_name"`
	ParentID flexInt    `json:"parent_id"`
}

type liveStream struct {
	Num         flexInt    `json:"num"`
	Name        string     `json:"name"`
	StreamType  flexString `json:"stream_type"`
	StreamID    flexInt    `json:"stream_id"`
	StreamIcon  flexString `json:"stream_icon"`
	EPGID       flexString `json:"epg_channel_id"`
	CategoryID  flexString `json:"category_id"`
	TVArchive   flexInt    `json:"tv_archive"`
	ArchiveDays flexInt    `json:"tv_archive_duration"`
}

// liveStreams returns the channels by name.
func liveStreams(t *testing.T, params ...string) map[string]liveStream {
	t.Helper()
	var list []liveStream
	api(t, &list, append([]string{"action", "get_live_streams"}, params...)...)
	out := map[string]liveStream{}
	for _, s := range list {
		out[s.Name] = s
	}
	if len(out) != len(list) {
		t.Errorf("two channels share a name: %+v", list)
	}
	return out
}

func TestLiveCategoriesAndStreams(t *testing.T) {
	var cats []category
	api(t, &cats, "action", "get_live_categories")
	ids := map[string]string{}
	var names []string
	for _, c := range cats {
		ids[c.Name] = string(c.ID)
		names = append(names, c.Name)
		if c.ID == "" || c.ParentID != 0 {
			t.Errorf("category %+v", c)
		}
	}
	if strings.Join(names, ",") != "News,Sports" {
		t.Errorf("categories: %v", names)
	}

	streams := liveStreams(t)
	if len(streams) != 3 {
		t.Fatalf("streams: %+v", streams)
	}
	seen := map[flexInt]bool{}
	var nums []int
	for name, s := range streams {
		if s.StreamID <= 0 || seen[s.StreamID] || s.StreamType != "live" || s.EPGID == "" {
			t.Errorf("%s: %+v", name, s)
		}
		seen[s.StreamID] = true
		nums = append(nums, int(s.Num))
	}
	slices.Sort(nums)
	if !slices.Equal(nums, []int{1, 2, 3}) {
		t.Errorf("num: %v", nums)
	}
	news, sports, weather := streams["Example News"], streams["Example Sports"], streams["Example Weather"]
	if string(news.CategoryID) != ids["News"] || string(weather.CategoryID) != ids["News"] || string(sports.CategoryID) != ids["Sports"] {
		t.Errorf("category_id: %+v", streams)
	}
	if news.TVArchive != 1 || news.ArchiveDays != 1 || sports.TVArchive != 0 || weather.TVArchive != 0 {
		t.Errorf("tv_archive: %+v", streams)
	}
	// The logo is the server's to give: never the provider's address.
	if !strings.HasPrefix(string(news.StreamIcon), srv.base+"/") || sports.StreamIcon != "" {
		t.Errorf("stream_icon: %q, %q", news.StreamIcon, sports.StreamIcon)
	}
	only := liveStreams(t, "category_id", ids["Sports"])
	if len(only) != 1 || only["Example Sports"].StreamID != sports.StreamID {
		t.Errorf("Sports' streams: %+v", only)
	}
}

type listing struct {
	ID         flexString `json:"id"`
	Title      string     `json:"title"`
	Start      string     `json:"start"`
	StartStamp flexInt    `json:"start_timestamp"`
	StopStamp  flexInt    `json:"stop_timestamp"`
	ChannelID  flexString `json:"channel_id"`
	NowPlaying flexInt    `json:"now_playing"`
	HasArchive flexInt    `json:"has_archive"`
}

// title decodes a listing's title, which is Base64 (section 5).
func (l listing) title(t *testing.T) string {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(l.Title)
	if err != nil {
		b, err = base64.RawStdEncoding.DecodeString(l.Title)
	}
	if err != nil {
		t.Errorf("title %q is not Base64: %v", l.Title, err)
	}
	return string(b)
}

func TestShortGuide(t *testing.T) {
	streams := liveStreams(t)
	news := streams["Example News"]
	var got struct {
		Listings []listing `json:"epg_listings"`
	}
	api(t, &got, "action", "get_short_epg", "stream_id", fmt.Sprint(news.StreamID))
	if len(got.Listings) != 4 {
		t.Fatalf("the short guide holds %d programmes, want 4 without a limit", len(got.Listings))
	}
	now := time.Now().Unix()
	first := got.Listings[0]
	if int64(first.StartStamp) > now || int64(first.StopStamp) <= now {
		t.Errorf("the first programme is not on now: %+v", first)
	}
	if want := fmt.Sprintf("News %d", (now-guideStart.Unix())/3600); first.title(t) != want {
		t.Errorf("the programme on now: %q, want %q", first.title(t), want)
	}
	if first.ChannelID != news.EPGID {
		t.Errorf("channel_id %q, want the channel's epg_channel_id %q", first.ChannelID, news.EPGID)
	}
	api(t, &got, "action", "get_short_epg", "stream_id", fmt.Sprint(news.StreamID), "limit", "2")
	if len(got.Listings) != 2 {
		t.Errorf("limit 2: %d programmes", len(got.Listings))
	}
	api(t, &got, "action", "get_short_epg", "stream_id", fmt.Sprint(streams["Example Weather"].StreamID))
	if len(got.Listings) != 0 {
		t.Errorf("a channel without a guide: %+v", got.Listings)
	}
}

func TestDayTable(t *testing.T) {
	news := liveStreams(t)["Example News"]
	var got struct {
		Listings []listing `json:"epg_listings"`
	}
	api(t, &got, "action", "get_simple_data_table", "stream_id", fmt.Sprint(news.StreamID))
	now := time.Now().Unix()
	playing, archived := 0, 0
	for _, l := range got.Listings {
		ended := int64(l.StopStamp) <= now
		if l.NowPlaying == 1 {
			playing++
			if int64(l.StartStamp) > now || ended {
				t.Errorf("marked on now: %+v", l)
			}
		}
		if l.HasArchive == 1 {
			archived++
			if !ended {
				t.Errorf("marked in the archive before it ended: %+v", l)
			}
		}
	}
	if len(got.Listings) < 4 || playing != 1 || archived < 1 {
		t.Errorf("the day's table: %d programmes, %d on now, %d in the archive", len(got.Listings), playing, archived)
	}
}

// readPackets reads the first packets of a stream and checks that they are
// MPEG-TS.
func readPackets(t *testing.T, what string, resp *http.Response) {
	t.Helper()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s: %s %q", what, resp.Status, b)
	}
	buf := make([]byte, 188*2)
	if _, err := io.ReadFull(resp.Body, buf); err != nil || buf[0] != 0x47 || buf[188] != 0x47 {
		t.Errorf("%s: %v %x", what, err, buf[:4])
	}
}

func TestLivePlays(t *testing.T) {
	sports := liveStreams(t)["Example Sports"]
	resp := open(t, streamPath("live", fmt.Sprintf("%d.ts", sports.StreamID)))
	defer resp.Body.Close()
	readPackets(t, "a live channel", resp)
}

// A past programme plays from the archive, from the start that the
// timeshift address gives in the server's time zone (section 6).
func TestTimeshift(t *testing.T) {
	news := liveStreams(t)["Example News"]
	start := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour)
	at := start.In(serverZone(t)).Format("2006-01-02:15-04")
	resp := open(t, streamPath("timeshift", "60", at, fmt.Sprintf("%d.ts", news.StreamID)))
	defer resp.Body.Close()
	readPackets(t, "a past programme", resp)
	archiveAsked.Lock()
	asked := slices.Contains(archiveAsked.starts, strconv.FormatInt(start.Unix(), 10))
	archiveAsked.Unlock()
	if !asked {
		t.Errorf("the provider was not asked for the archive from %d: %v", start.Unix(), archiveAsked.starts)
	}
	// A programme to come has no archive.
	soon := time.Now().Add(2 * time.Hour).In(serverZone(t)).Format("2006-01-02:15-04")
	if resp, _ := get(t, streamPath("timeshift", "60", soon, fmt.Sprintf("%d.ts", news.StreamID))); resp.StatusCode < 400 {
		t.Errorf("a programme to come: %s", resp.Status)
	}
}

// get.php lists the channels with the stream addresses of section 4, which
// play, and names xmltv.php's guide, whose channels are the epg_channel_id
// of get_live_streams (sections 5 and 7).
func TestPlaylistAndGuide(t *testing.T) {
	q := login()
	q.Set("type", "m3u_plus")
	q.Set("output", "ts")
	resp, body := get(t, "/get.php?"+q.Encode())
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(string(body), "#EXTM3U") {
		t.Fatalf("get.php: %s %.200q", resp.Status, body)
	}
	streams := liveStreams(t)
	for name, s := range streams {
		address := srv.base + streamPath("live", fmt.Sprintf("%d.ts", s.StreamID))
		if !strings.Contains(string(body), ","+name+"\n"+address+"\n") {
			t.Errorf("get.php lacks %s at %s:\n%s", name, address, body)
		}
	}
	if !strings.Contains(string(body), srv.base+"/xmltv.php?") {
		t.Errorf("get.php names no guide:\n%.300s", body)
	}

	resp, body = get(t, "/xmltv.php?"+login().Encode())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("xmltv.php: %s", resp.Status)
	}
	var tv struct {
		Channels []struct {
			ID string `xml:"id,attr"`
		} `xml:"channel"`
		Programmes []struct {
			Start   string `xml:"start,attr"`
			Channel string `xml:"channel,attr"`
		} `xml:"programme"`
	}
	if err := xml.Unmarshal(body, &tv); err != nil {
		t.Fatalf("xmltv.php is not XMLTV: %v", err)
	}
	var channels []string
	for _, c := range tv.Channels {
		channels = append(channels, c.ID)
	}
	counts := map[string]int{}
	for _, p := range tv.Programmes {
		counts[p.Channel]++
	}
	for name, s := range streams {
		if !slices.Contains(channels, string(s.EPGID)) {
			t.Errorf("xmltv.php lacks %s, %q: %v", name, s.EPGID, channels)
		}
	}
	if counts[string(streams["Example News"].EPGID)] == 0 || counts[string(streams["Example Sports"].EPGID)] == 0 {
		t.Errorf("programmes by channel: %v", counts)
	}
}
