// SPDX-License-Identifier: Apache-2.0

package stalker_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// What a client imitating a MAG250 sends (protocol notes, section 2).
const (
	userAgent  = "Mozilla/5.0 (QtEmbedded; U; Linux; C) AppleWebKit/533.3 (KHTML, like Gecko) MAG200 stbapp ver: 2 rev: 250 Safari/533.3"
	xUserAgent = "Model: MAG250; Link: WiFi"
)

var macs atomic.Int32

// newMAC returns a fictional MAC address that no other test uses.
func newMAC() string {
	return fmt.Sprintf("00:1A:79:00:01:%02X", macs.Add(1))
}

// client is a Stalker client acting as one box.
type client struct {
	t     *testing.T
	base  string // the portal's base, http://host:port/stalker_portal/
	mac   string
	token string
}

// newClient returns a client for a new box, unknown to the server.
func newClient(t *testing.T) *client {
	return &client{t: t, base: srv.base + "/stalker_portal/", mac: newMAC()}
}

// approvedClient returns a client for a new box that the administrator has
// approved.
func approvedClient(t *testing.T) *client {
	c := newClient(t)
	srv.devices(t, "approve", c.mac)
	return c
}

// get calls the API at path, under the server's root, with a box's cookies
// and headers, and returns the status and body.
func (c *client) get(path string, params url.Values) (int, []byte) {
	c.t.Helper()
	params.Set("JsHttpRequest", "1-xml")
	req, err := http.NewRequest(http.MethodGet, srv.base+path+"?"+params.Encode(), nil)
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("X-User-Agent", xUserAgent)
	req.Header.Set("Referer", c.base+"c/")
	req.Header.Set("Cookie", "mac="+url.QueryEscape(c.mac)+"; stb_lang=en; timezone=Europe%2FLondon")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	return resp.StatusCode, body
}

// call makes a portal call at /stalker_portal/server/load.php: type, action
// and the action's parameters, as name and value pairs.
func (c *client) call(module, action string, params ...string) (int, []byte) {
	c.t.Helper()
	return c.get("/stalker_portal/server/load.php", values(module, action, params...))
}

func values(module, action string, params ...string) url.Values {
	v := url.Values{"type": {module}, "action": {action}}
	for i := 0; i+1 < len(params); i += 2 {
		v.Set(params[i], params[i+1])
	}
	return v
}

// js makes a call and decodes the "js" member of its answer into v.
func (c *client) js(v any, module, action string, params ...string) {
	c.t.Helper()
	status, body := c.call(module, action, params...)
	if status != http.StatusOK {
		c.t.Fatalf("%s/%s: status %d: %s", module, action, status, body)
	}
	if err := decodeJS(body, v); err != nil {
		c.t.Fatalf("%s/%s: %v: %s", module, action, err, body)
	}
}

func decodeJS(body []byte, v any) error {
	var answer map[string]json.RawMessage
	if err := json.Unmarshal(body, &answer); err != nil {
		return err
	}
	js, ok := answer["js"]
	if !ok {
		return errNoJS
	}
	return json.Unmarshal(js, v)
}

// handshake starts a session, as clients do, with an empty token; the
// client keeps the token it gets.
func (c *client) handshake() {
	c.t.Helper()
	var hs struct{ Token string }
	c.js(&hs, "stb", "handshake", "token", "", "prehash", "0")
	if hs.Token == "" {
		c.t.Fatal("handshake: no token")
	}
	c.token = hs.Token
}

// profileParams are what a client sends with get_profile (protocol notes,
// section 3), with fictional values.
var profileParams = []string{
	"hd", "1", "ver", "ImageDescription: 0.2.18-r23-250; ImageDate: Fri Jan 1 00:00:00 UTC 2021; PORTAL version: 5.6.6; API Version: JS API version: 343",
	"num_banks", "2", "sn", "000000000001", "stb_type", "MAG250", "client_type", "STB",
	"image_version", "218", "video_out", "hdmi", "device_id", "", "device_id2", "", "signature", "",
	"auth_second_step", "0", "hw_version", "1.7-BD-00", "not_valid_token", "0", "hw_version_2", "0000000000000000000000000000000000000000",
	"timestamp", strconv.FormatInt(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).Unix(), 10), "api_signature", "262", "prehash", "",
}

// profile is the part of a profile that clients read.
type profile struct {
	ID              string `json:"id"`
	Blocked         string `json:"blocked"`
	WatchdogTimeout string `json:"watchdog_timeout"`
}

func (c *client) profile() profile {
	c.t.Helper()
	var p profile
	c.js(&p, "stb", "get_profile", profileParams...)
	return p
}

// signIn handshakes and reads the profile, as a client does when it starts.
func (c *client) signIn() profile {
	c.t.Helper()
	c.handshake()
	return c.profile()
}

// refused reports whether an answer says that the call lacks a valid
// session: HTTP 401 or 403, or a body that starts "Authorization failed."
// (protocol notes, section 3).
func refused(status int, body []byte) bool {
	return status == http.StatusUnauthorized || status == http.StatusForbidden ||
		bytes.HasPrefix(body, []byte("Authorization failed."))
}

// flexInt decodes a number that may arrive as a JSON number or a string.
type flexInt int

func (n *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	i, err := strconv.Atoi(s)
	*n = flexInt(i)
	return err
}
