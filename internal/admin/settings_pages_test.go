// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"net/http"
	"strings"
	"testing"
)

func TestPortalAddresses(t *testing.T) {
	for _, tc := range []struct{ host, listen, mag, stalker string }{
		{"192.0.2.20:8080", ":8080", "http://192.0.2.20:8080/c/", "http://192.0.2.20:8080/stalker_portal/c/"},
		{"192.0.2.20:9090", ":8080", "http://192.0.2.20:8080/c/", "http://192.0.2.20:8080/stalker_portal/c/"}, // the admin on its own port
		{"overscan.lan", "0.0.0.0:8080", "http://overscan.lan:8080/c/", "http://overscan.lan:8080/stalker_portal/c/"},
		{"[2001:db8::1]:9090", "[::]:8080", "http://[2001:db8::1]:8080/c/", "http://[2001:db8::1]:8080/stalker_portal/c/"},
		{"admin.lan:9090", "192.0.2.5:8080", "http://192.0.2.5:8080/c/", "http://192.0.2.5:8080/stalker_portal/c/"},
		{"overscan.lan", "", "", ""},
		{"overscan.lan", "8080", "", ""},
		{"", ":8080", "", ""},
	} {
		mag, stalker := portalAddresses(tc.host, tc.listen)
		if mag != tc.mag || stalker != tc.stalker {
			t.Errorf("%q, %q: %q and %q, want %q and %q", tc.host, tc.listen, mag, stalker, tc.mag, tc.stalker)
		}
	}
}

func TestSettingsPage(t *testing.T) {
	ta := newTestAdmin(t)
	c, _ := ta.withLineup()
	ta.UseSupport(Support{Listen: ":8080", Facts: []Fact{{Name: "Data directory", Value: "/var/lib/overscan"}, {Name: "ffmpeg", Value: "6.1.1 at /usr/bin/ffmpeg"}}})
	ta.UseStreams(fakeStreams{})
	rec := ta.do("GET", "/admin/settings", nil, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	mustContain(t, "the settings", body,
		`<a href="/admin/settings" aria-current="page" title="Settings">`, "<h1>Settings</h1>",
		"ffmpeg 6.1.1 at /usr/bin/ffmpeg", "Repair every channel", `action="/admin/settings/pin"`,
		`<dt>Version</dt><dd>test</dd>`, `<dt>Data directory</dt><dd>/var/lib/overscan</dd>`,
		`<dd class="mono">http://example.com:8080/c/</dd>`, `<dd class="mono">http://example.com:8080/stalker_portal/c/</dd>`,
		`href="/admin/support-bundle" download`, "overscan backup", `href="/admin/password"`, "Running for")

	// The password page belongs to Settings.
	mustContain(t, "the password page", ta.do("GET", "/admin/password", nil, c).Body.String(),
		`<a href="/admin/settings" aria-current="page" title="Settings">`, `<p class="crumbs"><a href="/admin/settings">Settings</a></p>`)

	// Signed out, there is no page.
	if rec := ta.do("GET", "/admin/settings", nil, nil); rec.Code != http.StatusSeeOther {
		t.Errorf("signed out: status %d", rec.Code)
	}
}

func TestStaticFilesCarryTags(t *testing.T) {
	ta := newTestAdmin(t)
	rec := ta.do("GET", "/admin/static/fonts/instrument-sans-400.woff", nil, nil)
	tag := rec.Header().Get("ETag")
	if rec.Code != http.StatusOK || tag == "" || rec.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("a font: status %d, ETag %q, Cache-Control %q", rec.Code, tag, rec.Header().Get("Cache-Control"))
	}
	if rec := ta.do("GET", "/admin/static/fonts/instrument-sans-400.woff", nil, nil, "If-None-Match", tag); rec.Code != http.StatusNotModified {
		t.Errorf("asked again with its tag: status %d", rec.Code)
	}
	css := ta.do("GET", ta.assetPath("admin.css"), nil, nil)
	if !strings.Contains(css.Header().Get("Cache-Control"), "immutable") || !strings.Contains(css.Body.String(), `url("fonts/instrument-sans-400.woff")`) {
		t.Errorf("the styles: Cache-Control %q", css.Header().Get("Cache-Control"))
	}
}
