// SPDX-License-Identifier: AGPL-3.0-or-later

package logging

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestJSONFormat(t *testing.T) {
	var buf bytes.Buffer
	log, err := New("INFO", "json", &buf)
	if err != nil {
		t.Fatal(err)
	}
	log.Info("hello", "key", "value")
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("not JSON: %q", buf.String())
	}
	if line["msg"] != "hello" || line["key"] != "value" {
		t.Errorf("got %v", line)
	}
}

func TestLevelFilters(t *testing.T) {
	var buf bytes.Buffer
	log, err := New("warn", "text", &buf)
	if err != nil {
		t.Fatal(err)
	}
	log.Info("quiet")
	log.Warn("loud")
	if out := buf.String(); strings.Contains(out, "quiet") || !strings.Contains(out, "loud") {
		t.Errorf("got %q", out)
	}
}

func TestInvalidSettings(t *testing.T) {
	if _, err := New("verbose", "text", &bytes.Buffer{}); err == nil {
		t.Error("no error for an unknown level")
	}
	if _, err := New("info", "xml", &bytes.Buffer{}); err == nil {
		t.Error("no error for an unknown format")
	}
}
