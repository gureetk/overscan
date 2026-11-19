// SPDX-License-Identifier: AGPL-3.0-or-later

package xtream

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/source"
)

const (
	user = "viewer"
	pass = "p@ss word/1&x"
)

// account is the answer of a fictional panel, with numbers as strings, as
// some panels send them.
const account = `{"user_info":{"username":"viewer","password":"p@ss word/1&x","message":"","auth":1,"status":"Active",
	"exp_date":"1798761600","is_trial":"0","active_cons":"0","created_at":"1767225600","max_connections":"2",
	"allowed_output_formats":["m3u8","ts","rtmp"]},
	"server_info":{"url":"provider.example","port":"8080","https_port":"8443","server_protocol":"http","rtmp_port":"8001",
	"timezone":"Europe/London","timestamp_now":1767225600,"time_now":"2026-01-01 00:00:00","process":true}}`

const categories = `[{"category_id":"1","category_name":"News","parent_id":0},
	{"category_id":2,"category_name":"Sport","parent_id":0},{"category_id":"","category_name":"Nothing"}]`

const streams = `[
	{"num":1,"name":"News One","stream_type":"live","stream_id":101,"stream_icon":"http://img.example/1.png",
	 "epg_channel_id":"news.one","added":"1767225600","category_id":"1","custom_sid":"","tv_archive":1,
	 "direct_source":"","tv_archive_duration":"3"},
	{"num":"2","name":"Sport One","stream_type":"live","stream_id":"102","stream_icon":"","epg_channel_id":null,
	 "category_id":null,"tv_archive":"0","tv_archive_duration":7,"is_adult":1},
	{"num":3,"name":"Radio One","stream_type":"radio_streams","stream_id":103,"category_id":2},
	"not an object",
	{"num":4,"name":"No ID","stream_type":"live"},
	{"num":5,"name":"Odd","stream_type":"live","stream_id":105,"tv_archive":true,"tv_archive_duration":"lots"}
]`

// panel serves the answers in replies, by action, to the right login.
func panel(t *testing.T, replies map[string]string) (*httptest.Server, *[]*http.Request) {
	t.Helper()
	var seen []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r)
		if r.URL.Path != "/player_api.php" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if q.Get("username") != user || q.Get("password") != pass {
			_, _ = w.Write([]byte(`{"user_info":{"auth":0}}`))
			return
		}
		body, ok := replies[q.Get("action")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if code, rest, ok := strings.Cut(body, " "); ok && len(code) == 3 && code[0] >= '1' && code[0] <= '5' {
			w.WriteHeader(map[string]int{"401": 401, "403": 403, "503": 503}[code])
			body = rest
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func client(srv *httptest.Server, password string) *Client {
	return New(source.Settings{Type: source.TypeXtream, URL: srv.URL + "/", Username: user, Password: password},
		srv.Client(), "Overscan/test")
}

// noSecrets fails if err quotes the login or an address that holds it. The
// server's host may show, as it does in the admin.
func noSecrets(t *testing.T, srv *httptest.Server, err error) {
	t.Helper()
	if err == nil {
		return
	}
	for _, s := range []string{pass, "p@ss", "password=", "player_api", srv.URL + "/"} {
		if strings.Contains(err.Error(), s) {
			t.Errorf("error %q quotes %q", err, s)
		}
	}
}

func TestAccount(t *testing.T) {
	srv, seen := panel(t, map[string]string{"": account})
	a, err := client(srv, pass).Account(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := Account{Status: "Active", ExpiresAt: time.Unix(1798761600, 0).UTC(), MaxConnections: 2,
		Formats: []string{"m3u8", "ts", "rtmp"}, TimeZone: "Europe/London"}
	if a.Status != want.Status || !a.ExpiresAt.Equal(want.ExpiresAt) || a.MaxConnections != 2 || a.ActiveConnections != 0 ||
		strings.Join(a.Formats, ",") != "m3u8,ts,rtmp" || a.TimeZone != want.TimeZone {
		t.Errorf("account %+v", a)
	}
	if a.Format() != "ts" {
		t.Errorf("format %q", a.Format())
	}
	r := (*seen)[0]
	if got := r.Header.Get("User-Agent"); got != "Overscan/test" {
		t.Errorf("user agent %q", got)
	}
	if r.URL.Query().Get("action") != "" || r.URL.Query().Get("password") != pass {
		t.Errorf("query %q", r.URL.RawQuery)
	}
}

func TestAccountNumbersAsNumbers(t *testing.T) {
	srv, _ := panel(t, map[string]string{"": `{"user_info":{"auth":"1","status":"Active","exp_date":null,
		"max_connections":1,"active_cons":1,"allowed_output_formats":"m3u8"},"server_info":{}}`})
	a, err := client(srv, pass).Account(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !a.ExpiresAt.IsZero() || a.MaxConnections != 1 || a.ActiveConnections != 1 || a.Format() != "m3u8" || a.TimeZone != "" {
		t.Errorf("account %+v", a)
	}
}

func TestLoginRefused(t *testing.T) {
	for name, reply := range map[string]string{
		"auth 0":       `{"user_info":{"auth":0}}`,
		"no user_info": `{"server_info":{}}`,
		"list":         `[]`,
		"false":        `false`,
	} {
		srv, _ := panel(t, map[string]string{"": reply})
		_, err := client(srv, pass).Account(context.Background())
		if !errors.Is(err, ErrLogin) {
			t.Errorf("%s: %v", name, err)
		}
		noSecrets(t, srv, err)
	}
	srv, _ := panel(t, map[string]string{"": account})
	_, err := client(srv, "wrong").Account(context.Background())
	if !errors.Is(err, ErrLogin) {
		t.Errorf("wrong password: %v", err)
	}
}

func TestAccountCannotPlay(t *testing.T) {
	srv, _ := panel(t, map[string]string{"": strings.Replace(account, `"Active"`, `"Expired\n"`, 1)})
	a, err := client(srv, pass).Account(context.Background())
	var aerr *AccountError
	if !errors.As(err, &aerr) || aerr.Status != "Expired" || a.MaxConnections != 2 {
		t.Fatalf("%+v, %v", a, err)
	}
	if err.Error() != "the server says the account is Expired" {
		t.Errorf("error %q", err)
	}
}

func TestServerErrors(t *testing.T) {
	for name, reply := range map[string]string{
		"status":   `403 Forbidden`,
		"html":     `<html><body>p@ss word/1&x</body></html>`,
		"empty":    ``,
		"bare":     `Unauthorized`,
		"not json": `{"user_info":`,
	} {
		srv, _ := panel(t, map[string]string{"": reply, "get_live_streams": reply})
		_, err := client(srv, pass).Account(context.Background())
		if err == nil || errors.Is(err, ErrLogin) {
			t.Errorf("%s: account %v", name, err)
		}
		noSecrets(t, srv, err)
		_, _, err = client(srv, pass).LiveStreams(context.Background())
		if err == nil {
			t.Errorf("%s: streams taken", name)
		}
		noSecrets(t, srv, err)
	}
	srv, _ := panel(t, map[string]string{"": account})
	srv.Close()
	_, err := client(srv, pass).Account(context.Background())
	if err == nil {
		t.Fatal("a closed server answered")
	}
	noSecrets(t, srv, err)
}

func TestLiveLists(t *testing.T) {
	srv, _ := panel(t, map[string]string{"get_live_categories": categories, "get_live_streams": streams})
	c := client(srv, pass)
	cats, err := c.LiveCategories(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cats) != 2 || cats[0] != (Category{ID: "1", Name: "News"}) || cats[1] != (Category{ID: "2", Name: "Sport"}) {
		t.Errorf("categories %+v", cats)
	}
	got, skipped, err := c.LiveStreams(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Stream{
		{ID: 101, Num: 1, Name: "News One", Type: "live", Icon: "http://img.example/1.png", EPGChannelID: "news.one",
			CategoryID: "1", ArchiveDays: 3},
		{ID: 102, Num: 2, Name: "Sport One", Type: "live", Adult: true},
		{ID: 103, Num: 3, Name: "Radio One", Type: "radio_streams", CategoryID: "2"},
		{ID: 105, Num: 5, Name: "Odd", Type: "live"},
	}
	if skipped != 2 || len(got) != len(want) {
		t.Fatalf("skipped %d, got %+v", skipped, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("stream %d: %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestEmptyLists(t *testing.T) {
	for _, reply := range []string{`null`, `false`, `{}`, `[]`} {
		srv, _ := panel(t, map[string]string{"get_live_streams": reply})
		got, _, err := client(srv, pass).LiveStreams(context.Background())
		if err != nil || len(got) != 0 {
			t.Errorf("%s: %v, %v", reply, got, err)
		}
	}
	srv, _ := panel(t, map[string]string{"get_live_streams": `{"1":{"stream_id":1}}`})
	if _, _, err := client(srv, pass).LiveStreams(context.Background()); err == nil {
		t.Error("an object of streams taken")
	}
}

func TestLimitAndUserAgent(t *testing.T) {
	srv, seen := panel(t, map[string]string{"get_live_streams": streams})
	c := New(source.Settings{Type: source.TypeXtream, URL: srv.URL, Username: user, Password: pass, UserAgent: "Player/2"},
		srv.Client(), "Overscan/test")
	c.MaxBytes = 100
	_, _, err := c.LiveStreams(context.Background())
	if err == nil || !strings.Contains(err.Error(), "larger") {
		t.Errorf("limit: %v", err)
	}
	if got := (*seen)[0].Header.Get("User-Agent"); got != "Player/2" {
		t.Errorf("user agent %q", got)
	}
}

func TestTypes(t *testing.T) {
	var v struct {
		A, B, C, D, E number
		F, G, H, I    text
		J, K, L, M    flag
		N, O, P       textList
	}
	err := json.Unmarshal([]byte(`{"A":"12","B":3.0,"C":"x","D":null,"E":"-4",
		"F":12,"G":true,"H":{"x":1},"I":"y",
		"J":"1","K":true,"L":"true","M":"2",
		"N":["a",1],"O":"b","P":null}`), &v)
	if err != nil {
		t.Fatal(err)
	}
	if v.A != 12 || v.B != 3 || v.C != 0 || v.D != 0 || v.E != -4 {
		t.Errorf("numbers %+v", v)
	}
	if v.F != "12" || v.G != "true" || v.H != "" || v.I != "y" {
		t.Errorf("text %+v", v)
	}
	if !v.J || !v.K || !v.L || v.M {
		t.Errorf("flags %+v", v)
	}
	if strings.Join(v.N, ",") != "a,1" || strings.Join(v.O, ",") != "b" || v.P != nil {
		t.Errorf("lists %+v", v)
	}
}
