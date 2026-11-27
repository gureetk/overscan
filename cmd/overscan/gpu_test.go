// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The gpu command says why there are no graphics, and exits with 1: with
// them off, and without ffmpeg.
func TestGPUCommand(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "overscan.yaml")
	if err := os.WriteFile(missing, []byte("log:\n  level: error\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := func(vars map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) { v, ok := vars[k]; return v, ok }
	}
	if _, err := exec.LookPath("ffmpeg"); err == nil {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"-config", missing, "gpu"}, nil, &stdout, &stderr, env(map[string]string{"OVERSCAN_GPU": "off"}))
		if code != 1 || !strings.Contains(stdout.String(), "No graphics to transcode on: The configuration turns the graphics off.") {
			t.Errorf("off: exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"-config", missing, "gpu"}, nil, &stdout, &stderr, env(map[string]string{"OVERSCAN_FFMPEG": "off"}))
	if code != 1 || !strings.Contains(stderr.String(), "cannot run ffmpeg, which the graphics need") {
		t.Errorf("no ffmpeg: exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
	if code := run(context.Background(), []string{"-config", missing, "gpu", "now"}, nil, &stdout, &stderr, noEnv); code != 2 {
		t.Errorf("an argument: exit %d", code)
	}
}
