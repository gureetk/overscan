// SPDX-License-Identifier: Apache-2.0

package xtream_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

// The account: player_api.php with no action (notes, section 2).
func TestAccount(t *testing.T) {
	var got struct {
		User *struct {
			Auth    flexInt    `json:"auth"`
			User    flexString `json:"username"`
			Status  flexString `json:"status"`
			Expires flexString `json:"exp_date"`
			Max     flexInt    `json:"max_connections"`
			Active  flexInt    `json:"active_cons"`
			Formats []string   `json:"allowed_output_formats"`
		} `json:"user_info"`
		Server *struct {
			Timezone  flexString `json:"timezone"`
			Timestamp flexInt    `json:"timestamp_now"`
			Protocol  flexString `json:"server_protocol"`
		} `json:"server_info"`
	}
	api(t, &got)
	if got.User == nil || got.Server == nil {
		t.Fatalf("the account call lacks user_info or server_info: %+v", got)
	}
	u, s := got.User, got.Server
	if u.Auth != 1 || string(u.User) != username || u.Status != "Active" || u.Max < 1 || u.Active < 0 ||
		!slices.Contains(u.Formats, "ts") {
		t.Errorf("user_info: %+v", *u)
	}
	if s.Timezone == "" || s.Protocol != "http" {
		t.Errorf("server_info: %+v", *s)
	}
	if now := time.Now().Unix(); int64(s.Timestamp) < now-60 || int64(s.Timestamp) > now+60 {
		t.Errorf("timestamp_now %d is not now (%d)", s.Timestamp, now)
	}
}

// A wrong login is a failed sign-in: no user_info, or auth other than 1
// (section 2); a stream with it is refused (section 4).
func TestWrongLogin(t *testing.T) {
	q := url.Values{"username": {username}, "password": {"not-" + password}}
	resp, body := get(t, "/player_api.php?"+q.Encode())
	if resp.StatusCode == http.StatusOK {
		var got struct {
			User *struct {
				Auth flexInt `json:"auth"`
			} `json:"user_info"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("a wrong login: %v in %q", err, body)
		}
		if got.User != nil && got.User.Auth == 1 {
			t.Errorf("a wrong login signed in: %q", body)
		}
	}
	resp, _ = get(t, "/live/"+username+"/not-"+password+"/1.ts")
	if resp.StatusCode < 400 {
		t.Errorf("a stream with a wrong login: %s", resp.Status)
	}
	// Nothing of the login reaches an error.
	if strings.Contains(string(body), "not-"+password) {
		t.Errorf("the answer repeats the wrong password: %q", body)
	}
}

// An action the server does not know is answered with an empty list, which
// a player reads as nothing to show (section 10).
func TestUnknownAction(t *testing.T) {
	var got []any
	api(t, &got, "action", "get_example_things")
	if len(got) != 0 {
		t.Errorf("an unknown action: %v", got)
	}
}
