// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"overscan.gureet.ca/internal/media"
	"overscan.gureet.ca/internal/store"
)

// Devices: the boxes and players that use Overscan (D23). The administrator
// approves a box that waits, by the six-digit code it shows (D39) or by its
// MAC address, labels devices, blocks them and removes them. These wrap the
// store's functions, which the command line uses too.

// maxLabelChars caps a device label, as the command line does.
const maxLabelChars = 100

// householdAccount is the account that devices are approved for unless
// another is named: the one a household starts with (D19).
const householdAccount = 1

// pairingCode matches the code a box shows while it waits to be paired.
var pairingCode = regexp.MustCompile(`^[0-9]{6}$`)

// deviceError is a mistake in a request about a device, which its sender can
// put right.
type deviceError struct {
	status int
	code   string
	text   string
}

func (e *deviceError) Error() string { return e.text }

func badDevice(status int, code, format string, args ...any) error {
	return &deviceError{status, code, fmt.Sprintf(format, args...)}
}

// checkLabel accepts a label of up to 100 printable characters.
func checkLabel(label string) error {
	switch {
	case !utf8.ValidString(label):
		return badDevice(http.StatusBadRequest, "bad_label", "A label must be text.")
	case utf8.RuneCountInString(label) > maxLabelChars:
		return badDevice(http.StatusBadRequest, "bad_label", "A label can have at most %d characters.", maxLabelChars)
	case strings.ContainsFunc(label, func(r rune) bool { return !unicode.IsPrint(r) }):
		return badDevice(http.StatusBadRequest, "bad_label", "A label cannot contain control characters.")
	}
	return nil
}

// checkAccount returns an error unless the account exists.
func (a *Admin) checkAccount(ctx context.Context, id int64) error {
	if _, err := a.store.Account(ctx, id); errors.Is(err, store.ErrNotFound) {
		return badDevice(http.StatusBadRequest, "no_account", "There is no account %d.", id)
	} else if err != nil {
		return err
	}
	return nil
}

// approveDevice approves a device by its MAC address, adding it if it has
// not connected yet.
func (a *Admin) approveDevice(ctx context.Context, mac string, account int64, label string) (store.Device, error) {
	if err := checkLabel(label); err != nil {
		return store.Device{}, err
	}
	if err := a.checkAccount(ctx, account); err != nil {
		return store.Device{}, err
	}
	d, err := a.store.ApproveDevice(ctx, mac, account, strings.TrimSpace(label), a.now())
	if err != nil {
		return store.Device{}, deviceStoreError(err, mac)
	}
	a.log.Info("device approved in the admin", "mac", d.MAC, "account", d.AccountID, "label", d.Label, "paired", d.Paired())
	return d, nil
}

// approveCode approves, and pairs, the box that waits with the code.
func (a *Admin) approveCode(ctx context.Context, code string, account int64, label string) (store.Device, error) {
	code = strings.TrimSpace(code)
	if !pairingCode.MatchString(code) {
		return store.Device{}, badDevice(http.StatusBadRequest, "bad_code", "A code has six digits.")
	}
	if err := checkLabel(label); err != nil {
		return store.Device{}, err
	}
	if err := a.checkAccount(ctx, account); err != nil {
		return store.Device{}, err
	}
	d, err := a.store.ApprovePairing(ctx, code, account, strings.TrimSpace(label), a.now())
	if errors.Is(err, store.ErrNotFound) {
		return store.Device{}, badDevice(http.StatusNotFound, "no_such_code", "No box is waiting with the code %s.", code)
	}
	if err != nil {
		return store.Device{}, err
	}
	a.log.Info("device paired in the admin", "mac", d.MAC, "account", d.AccountID, "label", d.Label)
	return d, nil
}

// blockDevice blocks a device, adding it if it has not connected yet.
func (a *Admin) blockDevice(ctx context.Context, mac string) (store.Device, error) {
	d, err := a.store.BlockDevice(ctx, mac)
	if err != nil {
		return store.Device{}, deviceStoreError(err, mac)
	}
	a.log.Info("device blocked in the admin", "mac", d.MAC)
	return d, nil
}

// labelDevice sets a device's label; an empty one clears it.
func (a *Admin) labelDevice(ctx context.Context, mac, label string) (store.Device, error) {
	if err := checkLabel(label); err != nil {
		return store.Device{}, err
	}
	d, err := a.store.SetDeviceLabel(ctx, mac, strings.TrimSpace(label))
	if err != nil {
		return store.Device{}, deviceStoreError(err, mac)
	}
	return d, nil
}

// removeDevice forgets a device; if it connects again, it waits again.
func (a *Admin) removeDevice(ctx context.Context, mac string) error {
	if err := a.store.RemoveDevice(ctx, mac); err != nil {
		return deviceStoreError(err, mac)
	}
	m, _ := store.ParseMAC(mac)
	a.log.Info("device removed in the admin", "mac", m)
	return nil
}

// forgetCheck forgets what a device's playback check found (D70): it plays
// films by its model's profile again, or as the last box of its model that
// was checked.
func (a *Admin) forgetCheck(ctx context.Context, mac string) (store.Device, error) {
	d, err := a.store.Device(ctx, mac)
	if err != nil {
		return store.Device{}, deviceStoreError(err, mac)
	}
	err = a.store.ForgetPlaybackCheck(ctx, d.ID)
	if errors.Is(err, store.ErrNotFound) {
		return store.Device{}, badDevice(http.StatusNotFound, "no_check", "%s has not been checked.", deviceName(d))
	}
	if err != nil {
		return store.Device{}, err
	}
	a.log.Info("a box's playback check forgotten in the admin", "mac", d.MAC, "label", d.Label)
	return d, nil
}

// deviceStoreError turns the store's errors about a device into mistakes
// that the request can put right.
func deviceStoreError(err error, mac string) error {
	switch {
	case errors.Is(err, store.ErrInvalidMAC):
		return badDevice(http.StatusBadRequest, "bad_mac", "%q is not a MAC address.", truncate(mac, 40))
	case errors.Is(err, store.ErrNotFound):
		return badDevice(http.StatusNotFound, "no_such_device", "There is no device %s.", truncate(mac, 40))
	}
	return err
}

// The pages.

// deviceRow is a device as the devices page shows it.
type deviceRow struct {
	store.Device
	Account string // the account's name, "" if none
	// Check is what a playback check found that the device plays films by
	// (D70); nil if none.
	Check *checkView
}

// checkView is what a playback check found, as the devices page shows it.
type checkView struct {
	At       time.Time
	Firmware string
	// Plays and Not name what it found to play, and not to play.
	Plays, Not []string
	// By names the box of the same model whose check a device that was not
	// checked plays by; "" for a device's own.
	By string
}

// viewOf returns what c found, as the page shows it.
func viewOf(c store.PlaybackCheck) *checkView {
	v := &checkView{At: c.CheckedAt, Firmware: c.Firmware}
	for _, k := range media.Capabilities() {
		plays, ok := c.Findings[k]
		switch {
		case !ok:
		case plays:
			v.Plays = append(v.Plays, media.CapabilityLabel(k))
		default:
			v.Not = append(v.Not, media.CapabilityLabel(k))
		}
	}
	return v
}

// Household reports whether it is approved for the household, and so plays
// as one of the household's profiles (D94).
func (r deviceRow) Household() bool {
	return r.State == store.DeviceApproved && r.AccountID == householdAccount
}

// Code is its pairing code as its screen shows it, in two groups of three
// digits.
func (r deviceRow) Code() string {
	if len(r.PairingCode) == 6 {
		return r.PairingCode[:3] + " " + r.PairingCode[3:]
	}
	return r.PairingCode
}

// checkEntry is what a device's own playback check found, as the devices
// page lists it.
type checkEntry struct {
	ID               int64
	MAC, Name, Model string
	*checkView
}

// devicesData is what the devices page shows.
type devicesData struct {
	Waiting  []deviceRow  // pending, or asking to pair again
	Known    []deviceRow  // approved or blocked
	Checks   []checkEntry // the devices' own playback checks
	Accounts []store.Account
	// Profiles are the household's (D94): the first first, then in the
	// order they were added.
	Profiles []store.Profile
	// Boxes are the household's approved devices that run Overscan's page,
	// which messages go to (D96); Messages are the last messages sent.
	Boxes       []store.Device
	Messages    []messageRow
	Code        string // as typed, after a mistake
	MAC         string // as typed, after a mistake
	Label       string // as typed, after a mistake
	ProfileName string // as typed, after a mistake
	MessageText string // as typed, after a mistake
	MessageTo   string // as chosen, after a mistake
	Now         time.Time
}

func (a *Admin) devicesData(ctx context.Context) (devicesData, error) {
	ds, err := a.store.Devices(ctx)
	if err != nil {
		return devicesData{}, err
	}
	accounts, err := a.store.Accounts(ctx)
	if err != nil {
		return devicesData{}, err
	}
	names := map[int64]string{}
	for _, acct := range accounts {
		names[acct.ID] = acct.Name
	}
	checks, err := a.store.PlaybackChecks(ctx)
	if err != nil {
		return devicesData{}, err
	}
	profiles, err := a.store.Profiles(ctx, householdAccount)
	if err != nil {
		return devicesData{}, err
	}
	// The latest check of each model, which the devices of the model that
	// were not checked play by.
	byID := map[int64]store.Device{}
	latest := map[string]store.PlaybackCheck{}
	for _, d := range ds {
		byID[d.ID] = d
	}
	for _, c := range checks {
		if l, ok := latest[c.Model]; !ok || c.CheckedAt.After(l.CheckedAt) || c.CheckedAt.Equal(l.CheckedAt) && c.DeviceID > l.DeviceID {
			latest[c.Model] = c
		}
	}
	messages, err := a.messageRows(ctx)
	if err != nil {
		return devicesData{}, err
	}
	data := devicesData{Accounts: accounts, Profiles: profiles, Messages: messages, Now: a.now()}
	for _, d := range ds {
		row := deviceRow{Device: d, Account: names[d.AccountID]}
		if c, ok := checks[d.ID]; ok {
			row.Check = viewOf(c)
			data.Checks = append(data.Checks, checkEntry{ID: d.ID, MAC: d.MAC, Name: deviceName(d), Model: c.Model, checkView: row.Check})
		} else if c, ok := latest[d.Model]; ok && d.Model != "" {
			row.Check = viewOf(c)
			row.Check.By = deviceName(byID[c.DeviceID])
		}
		if d.State == store.DevicePending || d.PairingCode != "" {
			data.Waiting = append(data.Waiting, row)
		}
		if d.State == store.DeviceApproved && d.AccountID == householdAccount && d.Paired() {
			data.Boxes = append(data.Boxes, d)
		}
		if d.State != store.DevicePending {
			data.Known = append(data.Known, row)
		}
	}
	return data, nil
}

// devicesPage shows the devices.
func (a *Admin) devicesPage(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	a.showDevices(w, r, s, http.StatusOK, view{})
}

// devicesWaiting shows the devices that wait, for the page to refresh that
// part of itself.
func (a *Admin) devicesWaiting(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	data, err := a.devicesData(r.Context())
	if err != nil {
		a.log.Error("cannot list the devices", "error", err)
		http.Error(w, "Something went wrong; the server's log says what.", http.StatusInternalServerError)
		return
	}
	a.fragment(w, r, http.StatusOK, "devices", "waiting-refresh", view{Section: "devices", Session: &s, Data: data})
}

// showDevices renders the devices page with v's error or notice.
func (a *Admin) showDevices(w http.ResponseWriter, r *http.Request, s store.AdminSession, status int, v view) {
	data, err := a.devicesData(r.Context())
	if err != nil {
		a.log.Error("cannot list the devices", "error", err)
		a.message(w, r, http.StatusInternalServerError, "Something went wrong", "The server's log says what.", &s)
		return
	}
	if typed, ok := v.Data.(devicesData); ok {
		data.Code, data.MAC, data.Label, data.ProfileName = typed.Code, typed.MAC, typed.Label, typed.ProfileName
		data.MessageText, data.MessageTo = typed.MessageText, typed.MessageTo
	}
	v.Title, v.Section, v.Session, v.Data = "Devices", "devices", &s, data
	a.render(w, r, status, "devices", v)
}

// deviceAction serves the forms of the devices page: the action is fixed, or
// the path's last part. After a change, a browser without htmx is sent back
// to the page; with htmx, the page comes back at once, with what was done.
func (a *Admin) deviceAction(fixed string) signedInFn {
	return func(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
		action := fixed
		if action == "" {
			action = r.PathValue("action")
		}
		a.doDeviceAction(w, r, s, action)
	}
}

func (a *Admin) doDeviceAction(w http.ResponseWriter, r *http.Request, s store.AdminSession, action string) {
	ctx := r.Context()
	mac := r.PathValue("mac")
	if mac == "" {
		mac = strings.TrimSpace(r.PostFormValue("mac"))
	}
	label := r.PostFormValue("label")
	account := int64(householdAccount)
	if v := r.PostFormValue("account"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			a.showDevices(w, r, s, http.StatusBadRequest, view{Error: "Choose an account."})
			return
		}
		account = n
	}

	var notice string
	var err error
	switch action {
	case "approve":
		var d store.Device
		if d, err = a.approveDevice(ctx, mac, account, label); err == nil {
			notice = "Approved " + deviceName(d) + "."
		}
	case "pair":
		var d store.Device
		if d, err = a.approveCode(ctx, r.PostFormValue("code"), account, label); err == nil {
			notice = "Paired " + deviceName(d) + "."
		}
	case "block":
		var d store.Device
		if d, err = a.blockDevice(ctx, mac); err == nil {
			notice = "Blocked " + deviceName(d) + "."
		}
	case "label":
		var d store.Device
		if d, err = a.labelDevice(ctx, mac, label); err == nil {
			notice = "Saved the label of " + d.MAC + "."
		}
	case "remove":
		if err = a.removeDevice(ctx, mac); err == nil {
			m, _ := store.ParseMAC(mac)
			notice = "Removed " + m + "."
		}
	case "forget-check":
		var d store.Device
		if d, err = a.forgetCheck(ctx, mac); err == nil {
			notice = "Forgot what the playback check of " + deviceName(d) + " found."
		}
	case "profile":
		var d store.Device
		var p store.Profile
		id, perr := strconv.ParseInt(r.PostFormValue("profile"), 10, 64)
		if perr != nil {
			err = badDevice(http.StatusBadRequest, "no_such_profile", "Choose a profile.")
		} else if d, p, err = a.setDeviceProfile(ctx, mac, id); err == nil {
			notice = deviceName(d) + " plays as " + p.Name + " now."
		}
	default:
		a.notFound(w, r)
		return
	}

	if r.PostFormValue("back") == "overview" {
		a.overviewDone(w, r, s, err, notice)
		return
	}
	a.devicesDone(w, r, s, err, notice, devicesData{Code: truncate(r.PostFormValue("code"), 6), MAC: truncate(mac, 40), Label: truncate(label, 400)})
}

// devicesDone answers a form of the devices page: with htmx, the page at
// once with what was done; without, a redirect to it; after a mistake, the
// page with what went wrong and what was typed.
func (a *Admin) devicesDone(w http.ResponseWriter, r *http.Request, s store.AdminSession, err error, notice string, typed devicesData) {
	var bad *deviceError
	switch {
	case errors.As(err, &bad):
		a.showDevices(w, r, s, bad.status, view{Error: bad.text, Data: typed})
	case err != nil:
		a.log.Error("cannot change a device", "error", err)
		a.showDevices(w, r, s, http.StatusInternalServerError, view{Error: "Something went wrong; the server's log says what."})
	case r.Header.Get("HX-Request") == "true":
		a.showDevices(w, r, s, http.StatusOK, view{Notice: notice})
	default:
		http.Redirect(w, r, "/admin/devices", http.StatusSeeOther)
	}
}

// deviceName is what the pages call a device: its label and MAC address.
func deviceName(d store.Device) string {
	if d.Label == "" {
		return d.MAC
	}
	return d.Label + " (" + d.MAC + ")"
}

// ago says how long ago t was, roughly; "never" for the zero time.
func ago(t, now time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		n := int(d / time.Minute)
		return fmt.Sprintf("%d %s ago", n, plural(n, "minute"))
	case d < 24*time.Hour:
		n := int(d / time.Hour)
		return fmt.Sprintf("%d %s ago", n, plural(n, "hour"))
	case d < 30*24*time.Hour:
		n := int(d / (24 * time.Hour))
		return fmt.Sprintf("%d %s ago", n, plural(n, "day"))
	}
	return t.Format("2 January 2006")
}

// The API.

// apiDevice is a device as the API describes it.
type apiDevice struct {
	MAC                string     `json:"mac"`
	State              string     `json:"state"`
	Label              string     `json:"label"`
	Model              string     `json:"model"`
	AccountID          *int64     `json:"account_id"`
	ProfileID          *int64     `json:"profile_id"`
	Paired             bool       `json:"paired"`
	PairingCode        *string    `json:"pairing_code"`
	PairingRequestedAt *time.Time `json:"pairing_requested_at"`
	LastSeen           *time.Time `json:"last_seen"`
	CreatedAt          time.Time  `json:"created_at"`
	PlaybackCheck      *apiCheck  `json:"playback_check"`
}

// apiCheck is what a device's playback check found (D70).
type apiCheck struct {
	CheckedAt   time.Time `json:"checked_at"`
	Firmware    string    `json:"firmware"`
	Plays       []string  `json:"plays"`
	DoesNotPlay []string  `json:"does_not_play"`
}

// deviceBody returns d as the API describes it, with what its playback
// check found, if it was checked.
func deviceBody(d store.Device, c *store.PlaybackCheck) apiDevice {
	out := apiDevice{
		MAC: d.MAC, State: string(d.State), Label: d.Label, Model: d.Model,
		Paired: d.Paired(), CreatedAt: d.CreatedAt,
	}
	if c != nil {
		v := viewOf(*c)
		out.PlaybackCheck = &apiCheck{CheckedAt: c.CheckedAt, Firmware: c.Firmware,
			Plays: append([]string{}, v.Plays...), DoesNotPlay: append([]string{}, v.Not...)}
	}
	if d.AccountID != 0 {
		out.AccountID = &d.AccountID
	}
	if d.ProfileID != 0 && d.State == store.DeviceApproved {
		out.ProfileID = &d.ProfileID
	}
	if d.PairingCode != "" {
		out.PairingCode = &d.PairingCode
		out.PairingRequestedAt = &d.PairingAt
	}
	if !d.LastSeen.IsZero() {
		out.LastSeen = &d.LastSeen
	}
	return out
}

// apiDevices lists the devices: those that wait first, then by MAC address.
func (a *Admin) apiDevices(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	ds, err := a.store.Devices(r.Context())
	if err != nil {
		a.apiInternal(w, "cannot list the devices", err)
		return
	}
	checks, err := a.store.PlaybackChecks(r.Context())
	if err != nil {
		a.apiInternal(w, "cannot list the devices", err)
		return
	}
	out := make([]apiDevice, len(ds))
	for i, d := range ds {
		var c *store.PlaybackCheck
		if found, ok := checks[d.ID]; ok {
			c = &found
		}
		out[i] = deviceBody(d, c)
	}
	writeJSON(w, http.StatusOK, struct {
		Devices []apiDevice `json:"devices"`
	}{out})
}

// approvalBody is what an approval may say. With no body, the device is
// approved for the household account, keeping its label.
type approvalBody struct {
	AccountID *int64 `json:"account_id"`
	Label     string `json:"label"`
}

func (a *Admin) readApproval(w http.ResponseWriter, r *http.Request) (int64, string, bool) {
	var body approvalBody
	if r.ContentLength != 0 && !readJSON(w, r, &body) {
		return 0, "", false
	}
	account := int64(householdAccount)
	if body.AccountID != nil {
		account = *body.AccountID
	}
	return account, body.Label, true
}

func (a *Admin) apiApproveDevice(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	account, label, ok := a.readApproval(w, r)
	if !ok {
		return
	}
	d, err := a.approveDevice(r.Context(), r.PathValue("mac"), account, label)
	a.deviceAnswer(w, r, d, err)
}

func (a *Admin) apiApproveCode(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	account, label, ok := a.readApproval(w, r)
	if !ok {
		return
	}
	d, err := a.approveCode(r.Context(), r.PathValue("code"), account, label)
	a.deviceAnswer(w, r, d, err)
}

func (a *Admin) apiBlockDevice(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	d, err := a.blockDevice(r.Context(), r.PathValue("mac"))
	a.deviceAnswer(w, r, d, err)
}

func (a *Admin) apiChangeDevice(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	var body struct {
		Label     *string `json:"label"`
		ProfileID *int64  `json:"profile_id"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.Label == nil && body.ProfileID == nil {
		apiError(w, http.StatusBadRequest, "bad_json", "Say what to change: label, profile_id or both.")
		return
	}
	ctx := r.Context()
	mac := r.PathValue("mac")
	var d store.Device
	var err error
	if body.Label != nil {
		if err = checkLabel(*body.Label); err != nil {
			a.deviceAnswer(w, r, d, err)
			return
		}
	}
	if body.ProfileID != nil {
		if d, _, err = a.setDeviceProfile(ctx, mac, *body.ProfileID); err != nil {
			a.deviceAnswer(w, r, d, err)
			return
		}
	}
	if body.Label != nil {
		d, err = a.labelDevice(ctx, mac, *body.Label)
	}
	a.deviceAnswer(w, r, d, err)
}

func (a *Admin) apiRemoveDevice(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	if err := a.removeDevice(r.Context(), r.PathValue("mac")); err != nil {
		a.deviceAnswer(w, r, store.Device{}, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *Admin) apiForgetCheck(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	if _, err := a.forgetCheck(r.Context(), r.PathValue("mac")); err != nil {
		a.deviceAnswer(w, r, store.Device{}, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deviceAnswer answers with the device, or with what went wrong.
func (a *Admin) deviceAnswer(w http.ResponseWriter, r *http.Request, d store.Device, err error) {
	var bad *deviceError
	switch {
	case errors.As(err, &bad):
		apiError(w, bad.status, bad.code, bad.text)
		return
	case err != nil:
		a.apiInternal(w, "cannot change a device", err)
		return
	}
	var check *store.PlaybackCheck
	switch c, err := a.store.PlaybackCheck(r.Context(), d.ID); {
	case err == nil:
		check = &c
	case !errors.Is(err, store.ErrNotFound):
		a.apiInternal(w, "cannot read a playback check", err)
		return
	}
	writeJSON(w, http.StatusOK, deviceBody(d, check))
}

// apiInternal answers for a failure of the server's own, which it logs.
func (a *Admin) apiInternal(w http.ResponseWriter, what string, err error) {
	a.log.Error(what, "error", err)
	apiError(w, http.StatusInternalServerError, "internal", "Something went wrong; the server's log says what.")
}
