// SPDX-License-Identifier: Apache-2.0

package stalker_test

import (
	"net/http"
	"strconv"
	"testing"
	"time"
)

// Sign-in as a MAG (protocol notes, section 3).
func TestSignIn(t *testing.T) {
	c := approvedClient(t)
	p := c.signIn()
	if p.ID == "" || p.Blocked != "0" {
		t.Errorf("profile %+v", p)
	}
	if n, err := strconv.Atoi(p.WatchdogTimeout); err != nil || n <= 0 {
		t.Errorf("watchdog_timeout %q is not a number of seconds", p.WatchdogTimeout)
	}

	var ok bool
	c.js(&ok, "stb", "do_auth", "login", "", "password", "", "device_id", "", "device_id2", "")
	if !ok {
		t.Error("do_auth: false")
	}

	var info struct {
		EndDate *string `json:"end_date"`
	}
	c.js(&info, "account_info", "get_main_info")
	if info.EndDate != nil && *info.EndDate != "" && !endDate(*info.EndDate) {
		t.Errorf("end_date %q is in none of the forms clients accept", *info.EndDate)
	}
}

// endDate reports whether s is in a form that clients read as an end date
// (protocol notes, section 3).
func endDate(s string) bool {
	if _, err := strconv.ParseInt(s, 10, 64); err == nil {
		return true
	}
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02", "January 2, 2006, 3:04 pm"} {
		if _, err := time.Parse(layout, s); err == nil {
			return true
		}
	}
	return false
}

// An unknown box is refused until the administrator approves it.
func TestUnknownBox(t *testing.T) {
	c := newClient(t)
	if status, body := c.call("stb", "handshake", "token", ""); !refused(status, body) {
		t.Fatalf("unknown box: status %d, %s", status, body)
	}
	srv.devices(t, "approve", c.mac)
	c.signIn()
}

// A call without a token, or with one that a later handshake replaced, is
// refused, and the client handshakes again (protocol notes, section 3).
func TestTokens(t *testing.T) {
	c := approvedClient(t)
	if status, body := c.call("stb", "get_profile", profileParams...); !refused(status, body) {
		t.Errorf("no token: status %d, %s", status, body)
	}

	c.handshake()
	old := c.token
	c.handshake()
	if c.token == old {
		t.Fatal("a second handshake gave the same token")
	}
	c.profile()

	c.token = old
	if status, body := c.call("stb", "get_profile", profileParams...); !refused(status, body) {
		t.Errorf("replaced token: status %d, %s", status, body)
	}
	c.token = "0123456789ABCDEF0123456789ABCDEF"
	if status, body := c.call("itv", "get_genres"); !refused(status, body) {
		t.Errorf("made-up token: status %d, %s", status, body)
	}
}

// A blocked box learns from its profile that it is blocked, and gets
// nothing else.
func TestBlockedBox(t *testing.T) {
	c := approvedClient(t)
	srv.devices(t, "block", c.mac)
	c.handshake()
	if p := c.profile(); p.Blocked != "1" {
		t.Errorf("blocked box's profile: %+v", p)
	}
	if status, body := c.call("itv", "get_genres"); !refused(status, body) {
		t.Errorf("blocked box listing genres: status %d, %s", status, body)
	}
}

// A portal answers an action it lacks with HTTP 404 (protocol notes,
// section 3).
func TestUnknownAction(t *testing.T) {
	c := approvedClient(t)
	c.signIn()
	if status, _ := c.call("itv", "no_such_action"); status != http.StatusNotFound {
		t.Errorf("status %d, want 404", status)
	}
}

// The keep-alive call (protocol notes, section 7).
func TestWatchdog(t *testing.T) {
	c := approvedClient(t)
	c.signIn()
	var events struct {
		Data struct {
			Msgs *flexInt `json:"msgs"`
		} `json:"data"`
	}
	c.js(&events, "watchdog", "get_events", "init", "0", "cur_play_type", "1", "event_active_id", "0")
	if events.Data.Msgs == nil || *events.Data.Msgs != 0 {
		t.Errorf("events %+v", events)
	}
}

// Clients find the API at load.php under the portal, at the server's root
// or at /portal.php (protocol notes, section 1).
func TestWhereTheAPILives(t *testing.T) {
	c := approvedClient(t)
	for _, path := range []string{"/stalker_portal/server/load.php", "/server/load.php", "/portal.php"} {
		status, body := c.get(path, values("stb", "handshake", "token", ""))
		var hs struct{ Token string }
		if status != http.StatusOK || decodeJS(body, &hs) != nil || hs.Token == "" {
			t.Errorf("%s: status %d, %s", path, status, body)
		}
	}
}
