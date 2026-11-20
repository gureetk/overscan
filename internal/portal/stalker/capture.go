// SPDX-License-Identifier: AGPL-3.0-or-later

package stalker

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

	"overscan.gureet.ca/internal/store"
)

const (
	maxCaptureParams   = 30  // parameters kept per captured request
	maxCaptureNameLen  = 64  // bytes of a module, action or parameter name
	maxCaptureValueLen = 100 // bytes of a parameter value
	maxCaptureAgentLen = 200 // bytes of the user agent
)

// redacted replaces the values of parameters that can carry credentials or
// identify a device.
const redacted = "REDACTED"

// capture records a request the API does not implement (D13): its module,
// action and parameter names, with an example whose sensitive values are
// redacted and whose other values are cut short.
func (a *API) capture(ctx context.Context, r *http.Request, d store.Device, module, action string) {
	var names []string
	for name := range r.Form {
		switch name {
		case "type", "action", "JsHttpRequest":
			continue
		}
		names = append(names, name)
	}
	slices.Sort(names)
	if len(names) > maxCaptureParams {
		names = names[:maxCaptureParams]
	}
	example := url.Values{}
	for i, name := range names {
		value := r.Form.Get(name)
		if store.SensitiveParam(name) {
			value = redacted
		}
		names[i] = cut(name, maxCaptureNameLen)
		example.Set(names[i], cut(value, maxCaptureValueLen))
	}
	c := store.Capture{
		Module:    cut(module, maxCaptureNameLen),
		Action:    cut(action, maxCaptureNameLen),
		Params:    strings.Join(names, ","),
		Example:   example.Encode(),
		UserAgent: cut(r.UserAgent(), maxCaptureAgentLen),
		DeviceID:  d.ID,
	}
	stored, err := a.store.CaptureRequest(ctx, c, a.now(), maxCaptureKinds)
	if err != nil {
		a.log.Error("cannot record an unknown request", "error", err)
		return
	}
	if stored {
		a.log.Info("unknown stalker request captured", "type", c.Module, "action", c.Action, "params", c.Params)
	}
}

// cut shortens s to at most n bytes without splitting a character.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
