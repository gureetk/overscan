// SPDX-License-Identifier: Apache-2.0

package xtream_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A player's side of the API: the calls, and fields read leniently, as the
// notes say a player should (section 8).

// userAgent is what the suite's player says it is.
const userAgent = "ExamplePlayer/1.0 (conformance)"

// get asks the server for a path, as a player, and returns the answer and
// its body.
func get(t *testing.T, path string, header ...string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest("GET", srv.base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("User-Agent", userAgent)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, b
}

// open starts a stream, as a player, and returns the answer with its body
// unread; closing it stops the stream.
func open(t *testing.T, path string, header ...string) *http.Response {
	t.Helper()
	req, err := http.NewRequest("GET", srv.base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("User-Agent", userAgent)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// login is the query of the suite's login.
func login() url.Values {
	return url.Values{"username": {username}, "password": {password}}
}

// api calls player_api.php with the login and the given parameters, and
// decodes its answer into v.
func api(t *testing.T, v any, params ...string) {
	t.Helper()
	q := login()
	for i := 0; i+1 < len(params); i += 2 {
		q.Set(params[i], params[i+1])
	}
	resp, body := get(t, "/player_api.php?"+q.Encode())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("player_api.php %v: %s %q", params, resp.Status, body)
	}
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("player_api.php %v: %v in %q", params, err, body)
	}
}

// streamPath is the path of a stream of a kind (section 4): live, movie,
// series or timeshift, for the login.
func streamPath(kind string, rest ...string) string {
	return "/" + kind + "/" + url.PathEscape(username) + "/" + url.PathEscape(password) + "/" + strings.Join(rest, "/")
}

// flexInt decodes a number that may arrive as a JSON number, a string, an
// empty string or null (section 8).
type flexInt int64

func (n *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	i, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		f, ferr := strconv.ParseFloat(s, 64)
		if ferr != nil {
			return err
		}
		i = int64(f)
	}
	*n = flexInt(i)
	return nil
}

// flexString decodes a string that may arrive as a number or null.
type flexString string

func (s *flexString) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*s = ""
		return nil
	}
	var text string
	if err := json.Unmarshal(b, &text); err == nil {
		*s = flexString(text)
		return nil
	}
	*s = flexString(strings.TrimSpace(string(b)))
	return nil
}

// serverZone returns the time zone that server_info names, in which a
// timeshift address writes its start (section 6).
func serverZone(t *testing.T) *time.Location {
	t.Helper()
	var got struct {
		Server struct {
			Timezone string `json:"timezone"`
		} `json:"server_info"`
	}
	api(t, &got)
	if got.Server.Timezone == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(got.Server.Timezone)
	if err != nil {
		t.Fatalf("server_info's time zone %q: %v", got.Server.Timezone, err)
	}
	return loc
}
