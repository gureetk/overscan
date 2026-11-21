// SPDX-License-Identifier: AGPL-3.0-or-later

package xtream

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"overscan.gureet.ca/internal/source"
)

// answering is a transport that answers every request with body, as a
// server might, without a network.
type answering []byte

func (a answering) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: http.Header{},
		Body: io.NopCloser(bytes.NewReader(a)), Request: r}, nil
}

// FuzzAnswers feeds what a server might answer to the account, the live
// categories and the live streams, which must never fail on it, and never
// keep a stream without an ID or with an archive past a year.
func FuzzAnswers(f *testing.F) {
	f.Add([]byte(account))
	f.Add([]byte(categories))
	f.Add([]byte(streams))
	f.Add([]byte(`{"user_info":{"auth":"1","exp_date":"1e999","max_connections":[]},"server_info":null}`))
	f.Add([]byte(`[{"stream_id":"-1"},{"stream_id":9223372036854775807,"tv_archive":"1","tv_archive_duration":"99999"}]`))
	f.Add([]byte("\xef\xbb\xbf[]"))
	f.Fuzz(func(t *testing.T, body []byte) {
		c := New(source.Settings{Type: source.TypeXtream, URL: "http://panel.example", Username: user, Password: pass},
			&http.Client{Transport: answering(body)}, "")
		ctx := context.Background()
		if a, err := c.Account(ctx); err == nil && (a.MaxConnections < 0 || strings.ContainsAny(a.Status, "\r\n")) {
			t.Errorf("account %+v", a)
		}
		if cats, err := c.LiveCategories(ctx); err == nil {
			for _, cat := range cats {
				if cat.ID == "" {
					t.Errorf("kept a category without an ID: %+v", cat)
				}
			}
		}
		got, _, err := c.LiveStreams(ctx)
		if err != nil {
			return
		}
		for _, s := range got {
			if s.ID <= 0 || s.Num < 0 || s.ArchiveDays < 0 || s.ArchiveDays > 365 {
				t.Errorf("kept %+v", s)
			}
		}
	})
}
