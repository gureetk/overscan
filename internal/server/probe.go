// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"overscan.gureet.ca/web"
)

const (
	// maxProbeReportBytes caps one probe report; real ones are a few kilobytes.
	maxProbeReportBytes = 256 << 10
	// maxProbeReports caps how many reports the data directory keeps, so that
	// the endpoint cannot fill a disk.
	maxProbeReports = 100
)

var errTooManyProbeReports = errors.New("too many probe reports saved")

// The probe page learns whether the browser keeps an HttpOnly cookie across
// a reboot with two cookies. probeCookie is long-lived and names when the
// page was served. probeLastCookie, for this visit only, passes on the
// probeCookie that the page's request brought, from the run before, so that
// the report can carry it back.
const (
	probeCookie     = "overscan_probe_http"
	probeLastCookie = "overscan_probe_http_last"
)

// probeFiles serves the probe page. Set-top browsers cache hard, and the probe
// must always run its latest version, so nothing it serves may be cached.
func probeFiles() http.Handler {
	files, err := fs.Sub(web.Probe, "probe")
	if err != nil {
		panic(err) // the directory is embedded at build time
	}
	server := http.StripPrefix("/probe/", http.FileServerFS(files))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path == "/probe/" || r.URL.Path == "/probe/index.html" {
			last := "none"
			if c, err := r.Cookie(probeCookie); err == nil {
				last = c.Value
			}
			http.SetCookie(w, &http.Cookie{
				Name: probeLastCookie, Value: last, Path: "/probe/", HttpOnly: true,
			})
			http.SetCookie(w, &http.Cookie{
				Name:     probeCookie,
				Value:    "served at " + time.Now().UTC().Format(time.RFC3339),
				Path:     "/probe/",
				MaxAge:   10 * 365 * 24 * 3600,
				HttpOnly: true,
			})
		}
		server.ServeHTTP(w, r)
	})
}

// handleProbeReport saves a report from the probe page in DataDir/probe.
func (s *Server) handleProbeReport(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxProbeReportBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "the report is too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "could not read the report", http.StatusBadRequest)
		return
	}

	// Only these fields are read, for the log line; the rest is stored as sent.
	var report struct {
		Environment struct {
			UserAgent string `json:"user_agent"`
		} `json:"environment"`
		Mag struct {
			Gstb *struct {
				Info map[string]any `json:"info"`
			} `json:"gstb"`
		} `json:"mag"`
	}
	if err := json.Unmarshal(body, &report); err != nil {
		http.Error(w, "the report is not a JSON object", http.StatusBadRequest)
		return
	}

	// Add what only the server sees: the HttpOnly cookie that the page's
	// request brought from the run before, if any.
	var full map[string]any
	if err := json.Unmarshal(body, &full); err != nil {
		http.Error(w, "the report is not a JSON object", http.StatusBadRequest)
		return
	}
	var found any
	if c, err := r.Cookie(probeLastCookie); err == nil && c.Value != "none" {
		found = c.Value
	}
	full["server"] = map[string]any{"http_cookie_from_last_run": found}
	body, err = json.Marshal(full)
	if err != nil {
		http.Error(w, "could not save the report", http.StatusInternalServerError)
		return
	}

	dir := filepath.Join(s.cfg.DataDir, "probe")
	path, err := saveProbeReport(dir, body)
	if errors.Is(err, errTooManyProbeReports) {
		s.log.Warn("probe report refused: too many saved", "dir", dir, "limit", maxProbeReports)
		http.Error(w, "too many probe reports are saved; delete some from the data directory", http.StatusInsufficientStorage)
		return
	}
	if err != nil {
		s.log.Error("could not save a probe report", "error", err)
		http.Error(w, "could not save the report", http.StatusInternalServerError)
		return
	}

	model := ""
	if report.Mag.Gstb != nil {
		model, _ = report.Mag.Gstb.Info["GetDeviceModel"].(string)
	}
	s.log.Info("probe report saved", "file", path, "user_agent", report.Environment.UserAgent, "model", model)
	w.WriteHeader(http.StatusNoContent)
}

// saveProbeReport writes report, indented, to a new file in dir, named by the
// time it arrived, and returns the file's path.
func saveProbeReport(dir string, report []byte) (string, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	saved, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return "", err
	}
	if len(saved) >= maxProbeReports {
		return "", errTooManyProbeReports
	}

	var pretty bytes.Buffer
	if err := json.Indent(&pretty, report, "", "  "); err != nil {
		return "", err
	}
	pretty.WriteByte('\n')

	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return "", err
	}
	path := filepath.Join(dir, time.Now().UTC().Format("20060102T150405Z")+"-"+hex.EncodeToString(suffix)+".json")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(pretty.Bytes()); err != nil {
		f.Close()
		return "", err
	}
	return path, f.Close()
}
