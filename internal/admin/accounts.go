// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"overscan.gureet.ca/internal/portal/output"
	"overscan.gureet.ca/internal/store"
	"overscan.gureet.ca/internal/streamprofile"
)

// Accounts (D102): who uses Overscan. A household starts with one account,
// and the administrator can add more, for people who watch on devices and
// players of their own. Each account has its devices and profiles, its end
// date (D95), its parental PIN (D55) and theme (D97), a limit on its streams
// at once, and may have a login and a playlist token for the outputs that
// players use. The Accounts page and the API's accounts/ paths change them;
// the household's account cannot be removed.

// maxOutputPassword caps a login's password, in bytes.
const maxOutputPassword = 128

// checkOutputPassword says why password cannot be a login's, or returns
// nil. Players put a login's password into the addresses that they ask
// for, often without escaping it, so it holds only what needs no escaping
// there: ASCII letters, digits, '.', '_', '-' and '~'.
func checkOutputPassword(password string) error {
	switch {
	case utf8.RuneCountInString(password) < MinPasswordChars:
		return badDevice(http.StatusBadRequest, "bad_password", "A password needs at least %d characters.", MinPasswordChars)
	case len(password) > maxOutputPassword:
		return badDevice(http.StatusBadRequest, "bad_password", "A password can have at most %d characters.", maxOutputPassword)
	}
	for _, r := range password {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-', r == '~':
		default:
			return badDevice(http.StatusBadRequest, "bad_password",
				"A password can hold letters without accents, digits, '.', '_', '-' and '~': players put it into addresses, where other characters can break it.")
		}
	}
	return nil
}

// accountStoreError turns the store's errors about an account into
// mistakes that the request can put right.
func accountStoreError(err error) error {
	switch {
	case errors.Is(err, store.ErrAccountName):
		return badDevice(http.StatusBadRequest, "bad_name",
			"An account's name has 1 to %d characters, none of them control characters, and is not another account's.", store.MaxAccountName)
	case errors.Is(err, store.ErrTooManyAccounts):
		return badDevice(http.StatusConflict, "too_many_accounts", "A server can have at most %d accounts.", store.MaxAccounts)
	case errors.Is(err, store.ErrHousehold):
		return badDevice(http.StatusConflict, "household", "The household's account cannot be removed.")
	case errors.Is(err, store.ErrAccountInUse):
		return badDevice(http.StatusConflict, "has_devices", "Devices are approved for the account: move them to another account, or remove them, first.")
	case errors.Is(err, store.ErrUsername):
		return badDevice(http.StatusBadRequest, "bad_username",
			"A username has 1 to %d letters without accents, digits, '.', '_', '-' or '@'.", store.MaxUsername)
	case errors.Is(err, store.ErrUsernameTaken):
		return badDevice(http.StatusConflict, "username_taken", "Another account has this username.")
	case errors.Is(err, store.ErrNoPassword):
		return badDevice(http.StatusBadRequest, "bad_password", "Give the login a password.")
	case errors.Is(err, store.ErrAccountStreams):
		return badDevice(http.StatusBadRequest, "bad_streams", "An account plays from 1 to %d streams at once, or any number.", store.MaxAccountStreams)
	case errors.Is(err, store.ErrNoKey):
		return badDevice(http.StatusConflict, "no_key", "This server has no key file to keep a token with.")
	case errors.Is(err, store.ErrNotFound):
		return badDevice(http.StatusNotFound, "no_such_account", "There is no such account.")
	case errors.Is(err, store.ErrNoSuchProfile):
		return badDevice(http.StatusBadRequest, "bad_output_profile", "Choose an output profile: %s.", profileNames())
	case errors.Is(err, store.ErrNoSources):
		return badDevice(http.StatusBadRequest, "bad_sources", "Tick one source at least, or choose every source.")
	case errors.Is(err, store.ErrNoSuchSource):
		return badDevice(http.StatusBadRequest, "bad_sources", "Choose among this server's sources.")
	}
	return err
}

// account returns an account, or a mistake if there is none.
func (a *Admin) account(ctx context.Context, id int64) (store.Account, error) {
	acct, err := a.store.Account(ctx, id)
	if err != nil {
		return store.Account{}, accountStoreError(err)
	}
	return acct, nil
}

// addAccount adds an account, which sees the household's lineup.
func (a *Admin) addAccount(ctx context.Context, name string) (store.Account, error) {
	acct, err := a.store.AddAccount(ctx, name)
	if err != nil {
		return store.Account{}, accountStoreError(err)
	}
	a.log.Info("account added in the admin", "account", acct.Name)
	return acct, nil
}

// renameAccount gives an account another name.
func (a *Admin) renameAccount(ctx context.Context, id int64, name string) (store.Account, error) {
	old, err := a.account(ctx, id)
	if err != nil {
		return store.Account{}, err
	}
	if err := a.store.RenameAccount(ctx, id, name); err != nil {
		return store.Account{}, accountStoreError(err)
	}
	acct, err := a.account(ctx, id)
	if err == nil && acct.Name != old.Name {
		a.log.Info("account renamed in the admin", "account", acct.Name, "was", old.Name)
	}
	return acct, err
}

// removeAccount removes an account that no device is approved for.
func (a *Admin) removeAccount(ctx context.Context, id int64) (store.Account, error) {
	acct, err := a.account(ctx, id)
	if err != nil {
		return store.Account{}, err
	}
	if err := a.store.RemoveAccount(ctx, id); err != nil {
		return store.Account{}, accountStoreError(err)
	}
	a.log.Info("account removed in the admin", "account", acct.Name)
	a.stopPlayers(playersOf(id), "the account was removed")
	return acct, nil
}

// samePassword reports whether password is one that the server already
// uses elsewhere: the administrator's, or a source's. A login's password is
// never one of those (plan, section 21): it travels in players' addresses.
func (a *Admin) samePassword(ctx context.Context, password string) (string, error) {
	if user, err := a.store.AdminUser(ctx); err == nil {
		same, err := CheckPassword(ctx, user.PasswordHash, password)
		if err != nil && !errors.Is(err, ErrBadHash) {
			return "", err
		}
		if same {
			return "the admin's", nil
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	sources, err := a.store.Sources(ctx)
	if err != nil {
		return "", err
	}
	for _, src := range sources {
		settings, err := a.store.SourceSettings(ctx, src.ID)
		if err != nil {
			continue // one that cannot be opened is not compared
		}
		if settings.Password != "" && settings.Password == password {
			return "a source's", nil
		}
	}
	return "", nil
}

// setLogin sets an account's login. With an empty password, the login
// keeps the one it has.
func (a *Admin) setLogin(ctx context.Context, id int64, username, password string) (store.Account, error) {
	old, err := a.account(ctx, id)
	if err != nil {
		return store.Account{}, err
	}
	hash := ""
	if password != "" {
		if err := checkOutputPassword(password); err != nil {
			return store.Account{}, err
		}
		whose, err := a.samePassword(ctx, password)
		if err != nil {
			return store.Account{}, err
		}
		if whose != "" {
			return store.Account{}, badDevice(http.StatusBadRequest, "password_in_use",
				"That password is %s. Give the login a password of its own: players send it in their addresses.", whose)
		}
		if hash, err = HashPassword(ctx, password, a.params); err != nil {
			return store.Account{}, err
		}
	}
	if err := a.store.SetAccountLogin(ctx, id, username, hash); err != nil {
		return store.Account{}, accountStoreError(err)
	}
	acct, err := a.account(ctx, id)
	if err != nil {
		return store.Account{}, err
	}
	a.log.Info("an account's login set in the admin", "account", acct.Name, "new_login", old.Username == "",
		"new_password", password != "")
	if old.Username != "" && (password != "" || !strings.EqualFold(old.Username, acct.Username)) {
		a.stopPlayers(playersOf(id, output.Xtream), "the account's login changed")
	}
	return acct, nil
}

// removeLogin takes an account's login away.
func (a *Admin) removeLogin(ctx context.Context, id int64) (store.Account, error) {
	acct, err := a.account(ctx, id)
	if err != nil {
		return store.Account{}, err
	}
	if err := a.store.RemoveAccountLogin(ctx, id); err != nil {
		return store.Account{}, accountStoreError(err)
	}
	a.log.Info("an account's login removed in the admin", "account", acct.Name)
	a.stopPlayers(playersOf(id, output.Xtream), "the account's login was removed")
	acct.Username, acct.PasswordHash = "", ""
	return acct, nil
}

// newToken gives an account a new playlist token, which replaces the one
// it had.
func (a *Admin) newToken(ctx context.Context, id int64) (store.Account, string, error) {
	acct, err := a.account(ctx, id)
	if err != nil {
		return store.Account{}, "", err
	}
	token, err := a.store.NewAccountToken(ctx, id)
	if err != nil {
		return store.Account{}, "", accountStoreError(err)
	}
	a.log.Info("an account's playlist token made in the admin", "account", acct.Name, "replaced", acct.HasToken)
	if acct.HasToken {
		a.stopPlayers(playersOf(id, output.M3U), "the account's playlist has new addresses")
	}
	acct.HasToken = true
	return acct, token, nil
}

// setStreams sets the most streams an account plays at once, 0 for any
// number.
func (a *Admin) setStreams(ctx context.Context, id int64, n int) (store.Account, error) {
	acct, err := a.account(ctx, id)
	if err != nil {
		return store.Account{}, err
	}
	if err := a.store.SetAccountStreams(ctx, id, n); err != nil {
		return store.Account{}, accountStoreError(err)
	}
	if n != acct.MaxStreams {
		a.log.Info("an account's limit of streams set in the admin", "account", acct.Name, "streams", n)
	}
	acct.MaxStreams = n
	return acct, nil
}

// setOutputProfile sets the output profile of an account's devices and
// players (D110). Their streams take it from their next channel.
func (a *Admin) setOutputProfile(ctx context.Context, id int64, name string) (store.Account, error) {
	acct, err := a.account(ctx, id)
	if err != nil {
		return store.Account{}, err
	}
	if err := a.store.SetAccountOutputProfile(ctx, id, name); err != nil {
		return store.Account{}, accountStoreError(err)
	}
	if name != acct.OutputProfile {
		a.log.Info("an account's output profile set in the admin", "account", acct.Name, "output_profile", name)
	}
	acct.OutputProfile = name
	return acct, nil
}

// setAccountSources sets the sources that an account uses (D114). What its
// devices, players and recordings play from a source that it no longer
// uses stops.
func (a *Admin) setAccountSources(ctx context.Context, id int64, u store.Sources) (store.Account, error) {
	old, err := a.account(ctx, id)
	if err != nil {
		return store.Account{}, err
	}
	if err := a.store.SetAccountSources(ctx, id, u); err != nil {
		return store.Account{}, accountStoreError(err)
	}
	acct, err := a.account(ctx, id)
	if err != nil {
		return store.Account{}, err
	}
	if acct.Sources.Only == old.Sources.Only && slices.Equal(acct.Sources.IDs, old.Sources.IDs) {
		return acct, nil
	}
	names, err := a.sourceNames(ctx, acct.Sources)
	if err != nil {
		return store.Account{}, err
	}
	a.log.Info("an account's sources set in the admin", "account", acct.Name, "sources", names)
	if a.streams != nil && acct.Sources.Only {
		a.streams.StopSources(id, acct.Sources, "the account no longer uses the stream's source")
	}
	return acct, nil
}

// sourceNames names the sources that an account uses, as the log and the
// Accounts page do: every source, or their names.
func (a *Admin) sourceNames(ctx context.Context, u store.Sources) (string, error) {
	if !u.Only {
		return "every source", nil
	}
	sources, err := a.store.Sources(ctx)
	if err != nil {
		return "", err
	}
	var names []string
	for _, src := range sources {
		if u.Uses(src.ID) {
			names = append(names, src.Name)
		}
	}
	if len(names) == 0 {
		return "no source", nil
	}
	return andList(names), nil
}

// setAccountExpiry sets when an account ends; the zero time means never
// (D95).
func (a *Admin) setAccountExpiry(ctx context.Context, id int64, t time.Time) (store.Account, error) {
	if id == householdAccount {
		if err := a.setExpiry(ctx, t); err != nil {
			return store.Account{}, accountStoreError(err)
		}
		return a.account(ctx, id)
	}
	acct, err := a.account(ctx, id)
	if err != nil {
		return store.Account{}, err
	}
	if err := a.store.SetAccountExpiry(ctx, id, t); err != nil {
		return store.Account{}, accountStoreError(err)
	}
	ended := !t.IsZero() && !a.now().Before(t)
	if t.IsZero() {
		a.log.Info("an account's end date removed in the admin", "account", acct.Name)
	} else {
		a.log.Info("an account's end date set in the admin", "account", acct.Name,
			"ends", t.UTC().Format(time.RFC3339), "ended", ended)
	}
	if ended {
		// Its boxes stop by themselves; its players are stopped.
		a.stopPlayers(playersOf(id), "the account has ended")
	}
	acct.ExpiresAt = t
	return acct, nil
}

// setAccountPIN sets an account's parental PIN, or with "" takes it away
// (D55).
func (a *Admin) setAccountPIN(ctx context.Context, id int64, pin string) (store.Account, error) {
	acct, err := a.account(ctx, id)
	if err != nil {
		return store.Account{}, err
	}
	hash := ""
	if pin != "" {
		if err := checkPIN(pin); err != nil {
			return store.Account{}, err
		}
		if hash, err = HashPassword(ctx, pin, a.params); err != nil {
			return store.Account{}, err
		}
	}
	if err := a.store.SetAccountPIN(ctx, id, hash); err != nil {
		return store.Account{}, accountStoreError(err)
	}
	if pin == "" {
		a.log.Info("an account's parental PIN removed in the admin", "account", acct.Name)
	} else {
		a.log.Info("an account's parental PIN set in the admin", "account", acct.Name)
	}
	acct.PINHash = hash
	return acct, nil
}

// setAccountTheme chooses the theme of an account's boxes, 0 for
// Overscan's own (D97).
func (a *Admin) setAccountTheme(ctx context.Context, id, themeID int64) (store.Account, error) {
	acct, err := a.account(ctx, id)
	if err != nil {
		return store.Account{}, err
	}
	if themeID != 0 {
		if _, err := a.store.Theme(ctx, themeID); errors.Is(err, store.ErrNotFound) {
			return store.Account{}, badDevice(http.StatusNotFound, "no_such_theme", "There is no such theme.")
		} else if err != nil {
			return store.Account{}, err
		}
	}
	if err := a.store.SetAccountTheme(ctx, id, themeID); err != nil {
		return store.Account{}, accountStoreError(err)
	}
	a.log.Info("an account's theme chosen in the admin", "account", acct.Name, "theme", themeID)
	acct.ThemeID = themeID
	return acct, nil
}

// setPlaylist lets an account's players read its playlist and guide, or
// stops them, making it a playlist token if it has none (D103).
func (a *Admin) setPlaylist(ctx context.Context, id int64, on bool) (store.Account, error) {
	acct, err := a.account(ctx, id)
	if err != nil {
		return store.Account{}, err
	}
	if on && !acct.HasToken {
		if acct, _, err = a.newToken(ctx, id); err != nil {
			return store.Account{}, err
		}
	}
	o := acct.Outputs
	o.M3U = on
	return a.setOutputs(ctx, id, o)
}

// setXtream lets an account's players sign in with its login through the
// Xtream-compatible API, or stops them (D104).
func (a *Admin) setXtream(ctx context.Context, id int64, on bool) (store.Account, error) {
	acct, err := a.account(ctx, id)
	if err != nil {
		return store.Account{}, err
	}
	o := acct.Outputs
	o.Xtream = on
	return a.setOutputs(ctx, id, o)
}

// setOutputs sets the outputs that an account may use.
func (a *Admin) setOutputs(ctx context.Context, id int64, o store.Outputs) (store.Account, error) {
	acct, err := a.account(ctx, id)
	if err != nil {
		return store.Account{}, err
	}
	if err := a.store.SetAccountOutputs(ctx, id, o); err != nil {
		return store.Account{}, accountStoreError(err)
	}
	if o != acct.Outputs {
		a.log.Info("an account's outputs set in the admin", "account", acct.Name, "m3u", o.M3U, "xtream", o.Xtream)
	}
	if acct.Outputs.M3U && !o.M3U {
		a.stopPlayers(playersOf(id, output.M3U), "the account's playlist was turned off")
	}
	if acct.Outputs.Xtream && !o.Xtream {
		a.stopPlayers(playersOf(id, output.Xtream), "the account's Xtream login was turned off")
	}
	acct.Outputs = o
	return acct, nil
}

// moveDevice moves an approved device to another account, whose first
// profile it plays as from then on.
func (a *Admin) moveDevice(ctx context.Context, mac string, accountID int64) (store.Device, store.Account, error) {
	acct, err := a.account(ctx, accountID)
	if err != nil {
		return store.Device{}, store.Account{}, err
	}
	d, err := a.store.Device(ctx, mac)
	if err != nil {
		return store.Device{}, store.Account{}, deviceStoreError(err, mac)
	}
	if d.State != store.DeviceApproved {
		return store.Device{}, store.Account{}, badDevice(http.StatusConflict, "not_approved",
			"%s is not approved, so it belongs to no account. Approve it for one instead.", deviceName(d))
	}
	if d.AccountID == accountID {
		return d, acct, nil
	}
	moved, err := a.store.SetDeviceAccount(ctx, mac, accountID)
	if err != nil {
		return store.Device{}, store.Account{}, deviceStoreError(err, mac)
	}
	a.log.Info("a device moved to another account in the admin", "mac", moved.MAC, "label", moved.Label,
		"account", acct.Name, "from", d.AccountID)
	return moved, acct, nil
}

// The page.

// accountRow is an account as the Accounts page shows it.
type accountRow struct {
	store.Account
	Devices   int    // approved devices
	Profiles  int    // its profiles
	Token     string // its playlist token, "" if none
	ThemeName string // its theme's, "" for Overscan's own
	EndText   string // the last day it plays, "" if it never ends
	EndDay    string // the same, as a date input takes it
	IsEnded   bool
	// Playlist and Guide are its playlist's and guide's addresses, while
	// it may use them and has a token (D103); "" otherwise.
	Playlist, Guide string
	// Server is the address that its players sign in at, while it may use
	// the Xtream API and has a login (D104); "" otherwise.
	Server string
	// LineupName is the name of the lineup it sees (D106).
	LineupName string
}

// Household says that it is the household's account, which cannot be
// removed.
func (r accountRow) Household() bool { return r.ID == householdAccount }

// SourcesText says which sources it uses, if only some (D114), for its
// card's summary.
func (r accountRow) SourcesText() string {
	switch {
	case !r.Sources.Only:
		return ""
	case len(r.Sources.IDs) == 0:
		return "no source"
	case len(r.Sources.IDs) == 1:
		return "1 source"
	}
	return strconv.Itoa(len(r.Sources.IDs)) + " sources"
}

// LiveText says how its devices and players get live channels, if not as
// their sources send them (D110), for its card's summary.
func (r accountRow) LiveText() string {
	p, ok := streamprofile.Find(r.OutputProfile)
	if !ok || !p.Transcodes() {
		return ""
	}
	return "live as " + p.Label
}

// accountsData is what the Accounts page shows.
type accountsData struct {
	Accounts []accountRow
	// OutputsOn says that the outputs that players use are on, and
	// Anywhere that they answer players from any address (D103).
	OutputsOn, Anywhere bool
	Themes              []store.Theme
	Name                string // typed into the form that adds an account
	Open                int64  // the account whose forms are open
	Streams             []int  // the limits the page offers
	Lineups             []store.LineupInfo
	// OutputProfiles are the output profiles that the page offers (D110),
	// and TranscodeNote says why they would transcode nothing now.
	OutputProfiles []profileOption
	TranscodeNote  string
	// Sources are the server's sources, which each account may use or not
	// (D114).
	Sources []sourceRow
}

// accountsData returns what the Accounts page shows; base is the portal's
// address, for the addresses of the accounts' playlists.
func (a *Admin) accountsData(ctx context.Context, base string) (accountsData, error) {
	accounts, err := a.store.Accounts(ctx)
	if err != nil {
		return accountsData{}, err
	}
	devices, err := a.store.Devices(ctx)
	if err != nil {
		return accountsData{}, err
	}
	themes, err := a.store.Themes(ctx)
	if err != nil {
		return accountsData{}, err
	}
	themeNames := map[int64]string{}
	for _, th := range themes {
		themeNames[th.ID] = th.Name
	}
	approved := map[int64]int{}
	for _, d := range devices {
		if d.State == store.DeviceApproved {
			approved[d.AccountID]++
		}
	}
	lineups, err := a.store.Lineups(ctx)
	if err != nil {
		return accountsData{}, err
	}
	lineupNames := map[int64]string{}
	for _, l := range lineups {
		lineupNames[l.ID] = l.Name
	}
	now := a.now()
	data := accountsData{Themes: themes, Streams: []int{1, 2, 3, 4, 5, 6, 8, 10}, Lineups: lineups, OutputProfiles: profileOptions()}
	if data.TranscodeNote, err = a.transcodeNote(ctx); err != nil {
		return accountsData{}, err
	}
	if data.OutputsOn, err = a.store.Outputs(ctx); err != nil {
		return accountsData{}, err
	}
	if data.Anywhere, err = a.store.OutputsAnywhere(ctx); err != nil {
		return accountsData{}, err
	}
	sources, err := a.store.Sources(ctx)
	if err != nil {
		return accountsData{}, err
	}
	for _, src := range sources {
		data.Sources = append(data.Sources, sourceRow{Source: src})
	}
	for _, acct := range accounts {
		row := accountRow{Account: acct, Devices: approved[acct.ID], ThemeName: themeNames[acct.ThemeID], IsEnded: acct.Ended(now),
			LineupName: lineupNames[acct.LineupID]}
		profiles, err := a.store.Profiles(ctx, acct.ID)
		if err != nil {
			return accountsData{}, err
		}
		row.Profiles = len(profiles)
		if acct.HasToken {
			if row.Token, err = a.store.AccountToken(ctx, acct.ID); err != nil {
				a.log.Warn("cannot read an account's playlist token", "account", acct.Name, "error", err)
			}
		}
		if row.Token != "" && acct.Outputs.M3U && base != "" {
			row.Playlist, row.Guide = base+"/playlist/"+row.Token+".m3u", base+"/epg/"+row.Token+".xml.gz"
		}
		if acct.Username != "" && acct.Outputs.Xtream {
			row.Server = base
		}
		if !acct.ExpiresAt.IsZero() {
			day := lastDay(acct.ExpiresAt, time.Local)
			row.EndText, row.EndDay = day.Format("2 January 2006"), day.Format(dayForm)
		}
		data.Accounts = append(data.Accounts, row)
	}
	return data, nil
}

// accountsPage shows the accounts.
func (a *Admin) accountsPage(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	a.showAccounts(w, r, s, http.StatusOK, view{})
}

// showAccounts renders the Accounts page with v's error or notice.
func (a *Admin) showAccounts(w http.ResponseWriter, r *http.Request, s store.AdminSession, status int, v view) {
	data, err := a.accountsData(r.Context(), portalBase(r.Host, a.support.Listen))
	if err != nil {
		a.log.Error("cannot list the accounts", "error", err)
		a.message(w, r, http.StatusInternalServerError, "Something went wrong", "The server's log says what.", &s)
		return
	}
	if typed, ok := v.Data.(accountsData); ok {
		data.Name, data.Open = typed.Name, typed.Open
	}
	v.Title, v.Section, v.Session, v.Data = "Accounts", "accounts", &s, data
	a.render(w, r, status, "accounts", v)
}

// accountAction serves the Accounts page's forms: adding an account, and
// the path's last part for one of them.
func (a *Admin) accountAction(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	ctx := r.Context()
	action := r.PathValue("action")
	var id int64
	if action != "" {
		n, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			a.notFound(w, r)
			return
		}
		id = n
	}
	var acct store.Account
	var notice string
	var err error
	switch action {
	case "":
		if acct, err = a.addAccount(ctx, r.PostFormValue("name")); err == nil {
			id = acct.ID
			notice = "Added the account " + acct.Name + ". Approve devices for it, or move devices to it on the Devices page."
		}
	case "rename":
		if acct, err = a.renameAccount(ctx, id, r.PostFormValue("name")); err == nil {
			notice = "Renamed the account " + acct.Name + "."
		}
	case "login":
		if acct, err = a.setLogin(ctx, id, r.PostFormValue("username"), r.PostFormValue("password")); err == nil {
			notice = "Saved the login of " + acct.Name + ": " + acct.Username + "."
		}
	case "remove-login":
		if acct, err = a.removeLogin(ctx, id); err == nil {
			notice = "Removed the login of " + acct.Name + "."
		}
	case "token":
		if acct, _, err = a.newToken(ctx, id); err == nil {
			notice = "Made a new playlist token for " + acct.Name + ". Addresses with the old one stop working."
		}
	case "streams":
		n, perr := strconv.Atoi(r.PostFormValue("streams"))
		if perr != nil {
			err = badDevice(http.StatusBadRequest, "bad_streams", "Choose how many streams it plays at once.")
		} else if acct, err = a.setStreams(ctx, id, n); err == nil {
			notice = acct.Name + " plays " + streamsText(n) + " at once now."
		}
	case "expiry":
		day := strings.TrimSpace(r.PostFormValue("date"))
		if r.PostFormValue("remove") != "" || day == "" {
			if acct, err = a.setAccountExpiry(ctx, id, time.Time{}); err == nil {
				notice = "Removed the end date of " + acct.Name + ": it never ends."
			}
			break
		}
		d, perr := time.ParseInLocation(dayForm, day, time.Local)
		if perr != nil || d.Year() < 2000 || d.Year() > 9998 {
			err = badDevice(http.StatusBadRequest, "bad_date", "Give the end date as a day, such as 2027-12-31.")
			break
		}
		ends := endOfDay(d, time.Local)
		if acct, err = a.setAccountExpiry(ctx, id, ends); err == nil {
			notice = acct.Name + " plays until the end of " + d.Format("2 January 2006") + "."
			if !a.now().Before(ends) {
				notice = acct.Name + " has ended: its devices play nothing."
			}
		}
	case "pin":
		pin := r.PostFormValue("pin")
		if pin == "" {
			err = badDevice(http.StatusBadRequest, "bad_pin", "Type the PIN.")
		} else if acct, err = a.setAccountPIN(ctx, id, pin); err == nil {
			notice = "Set the parental PIN of " + acct.Name + "."
		}
	case "remove-pin":
		if acct, err = a.setAccountPIN(ctx, id, ""); err == nil {
			notice = "Removed the parental PIN of " + acct.Name + ": its locks do nothing until it has one."
		}
	case "theme":
		themeID, terr := themeID(r.PostFormValue("theme"))
		if terr != nil {
			err = terr
		} else if acct, err = a.setAccountTheme(ctx, id, themeID); err == nil {
			notice = "Chose the theme of " + acct.Name + ". Its boxes show it within five minutes."
		}
	case "playlist":
		on := r.PostFormValue("on") != ""
		if acct, err = a.setPlaylist(ctx, id, on); err == nil {
			notice = acct.Name + "'s players can no longer read its playlist and guide."
			if on {
				notice = acct.Name + "'s players can read its playlist and guide at the addresses below."
			}
		}
	case "sources":
		u, perr := sourcesForm(r)
		if perr != nil {
			err = perr
		} else if acct, err = a.setAccountSources(ctx, id, u); err == nil {
			names, nerr := a.sourceNames(ctx, acct.Sources)
			if nerr != nil {
				err = nerr
				break
			}
			notice = acct.Name + " uses " + names + " now."
			if acct.Sources.Only {
				notice += " What its devices and players played from another source has stopped."
			}
		}
	case "live":
		if acct, err = a.setOutputProfile(ctx, id, r.PostFormValue("output_profile")); err == nil {
			notice = acct.Name + "'s devices and players get live channels " + profileText(acct.OutputProfile) +
				", from the next channel they start."
		}
	case "lineup":
		lineupID, _, ok := formInt(r, "lineup")
		if !ok || lineupID <= 0 {
			err = badDevice(http.StatusBadRequest, "bad_lineup", "Choose a lineup.")
		} else if acct, err = a.setAccountLineup(ctx, id, lineupID); err == nil {
			notice = acct.Name + " sees the lineup " + a.lineupName(ctx, lineupID) + " now. Its boxes show it within five minutes."
		}
	case "xtream":
		on := r.PostFormValue("on") != ""
		if acct, err = a.setXtream(ctx, id, on); err == nil {
			switch {
			case !on:
				notice = acct.Name + "'s players can no longer sign in with its login."
			case acct.Username == "":
				notice = acct.Name + "'s players can sign in with its login once it has one: give it one below."
			default:
				notice = acct.Name + "'s players can sign in as " + acct.Username + " at the address below."
			}
		}
	case "remove":
		if acct, err = a.removeAccount(ctx, id); err == nil {
			notice = "Removed the account " + acct.Name + ", with its profiles."
			id = 0
		}
	default:
		a.notFound(w, r)
		return
	}
	a.accountsDone(w, r, s, err, notice, accountsData{Name: truncate(strings.TrimSpace(r.PostFormValue("name")), 400), Open: id})
}

// streamsText says how many streams an account plays at once.
func streamsText(n int) string {
	if n == 0 {
		return "any number of streams"
	}
	return strconv.Itoa(n) + " " + plural(n, "stream")
}

// accountsDone answers a form of the Accounts page, as devicesDone does.
func (a *Admin) accountsDone(w http.ResponseWriter, r *http.Request, s store.AdminSession, err error, notice string, typed accountsData) {
	var bad *deviceError
	var badEditErr *editError
	switch {
	case errors.As(err, &bad):
		a.showAccounts(w, r, s, bad.status, view{Error: bad.text, Data: typed})
	case errors.As(err, &badEditErr):
		a.showAccounts(w, r, s, badEditErr.status, view{Error: badEditErr.text, Data: typed})
	case err != nil:
		a.log.Error("cannot change an account", "error", err)
		a.showAccounts(w, r, s, http.StatusInternalServerError, view{Error: "Something went wrong; the server's log says what."})
	case r.Header.Get("HX-Request") == "true":
		a.showAccounts(w, r, s, http.StatusOK, view{Notice: notice, Data: accountsData{Open: typed.Open}})
	default:
		http.Redirect(w, r, "/admin/accounts", http.StatusSeeOther)
	}
}

// The API.

// accountJSON is an account as the API describes it. It never holds the
// password's hash; the token only on asking for one.
type accountJSON struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	LineupID   int64      `json:"lineup_id"`
	Username   *string    `json:"username"` // null without a login
	Token      bool       `json:"token"`    // it has a playlist token
	MaxStreams int        `json:"max_streams"`
	Outputs    apiOutputs `json:"outputs"`
	ExpiresAt  *time.Time `json:"expires_at"`
	Ended      bool       `json:"ended"`
	PIN        bool       `json:"pin"`
	ThemeID    int64      `json:"theme_id"`
	Devices    int        `json:"devices"`
	Profiles   int        `json:"profiles"`
	// OutputProfile names how its devices and players get live channels
	// (D110).
	OutputProfile string `json:"output_profile"`
	// Sources are the IDs of the sources that it uses (D114), null for
	// every source.
	Sources []int64 `json:"sources"`
}

// apiOutputs are the outputs that an account may use.
type apiOutputs struct {
	M3U    bool `json:"m3u"`
	Xtream bool `json:"xtream"`
}

// accountBody returns an account as the API describes it.
func (a *Admin) accountBody(ctx context.Context, acct store.Account) (accountJSON, error) {
	out := accountJSON{ID: acct.ID, Name: acct.Name, LineupID: acct.LineupID, Token: acct.HasToken, MaxStreams: acct.MaxStreams,
		Outputs: apiOutputs{M3U: acct.Outputs.M3U, Xtream: acct.Outputs.Xtream}, Ended: acct.Ended(a.now()),
		PIN: acct.PINHash != "", ThemeID: acct.ThemeID, OutputProfile: acct.OutputProfile}
	if acct.Sources.Only {
		out.Sources = append([]int64{}, acct.Sources.IDs...)
	}
	if acct.Username != "" {
		u := acct.Username
		out.Username = &u
	}
	if !acct.ExpiresAt.IsZero() {
		t := acct.ExpiresAt.UTC()
		out.ExpiresAt = &t
	}
	devices, err := a.store.Devices(ctx)
	if err != nil {
		return accountJSON{}, err
	}
	for _, d := range devices {
		if d.State == store.DeviceApproved && d.AccountID == acct.ID {
			out.Devices++
		}
	}
	profiles, err := a.store.Profiles(ctx, acct.ID)
	if err != nil {
		return accountJSON{}, err
	}
	out.Profiles = len(profiles)
	return out, nil
}

// accountAnswer answers with an account as it is now, or with what went
// wrong.
func (a *Admin) accountAnswer(w http.ResponseWriter, r *http.Request, id int64, err error) {
	if a.apiFailed(w, err) {
		return
	}
	acct, err := a.account(r.Context(), id)
	if a.apiFailed(w, err) {
		return
	}
	out, err := a.accountBody(r.Context(), acct)
	if a.apiFailed(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// pathAccount returns the account ID in the path, answering 404 if it is
// not one.
func pathAccount(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		apiError(w, http.StatusNotFound, "no_such_account", "There is no such account.")
		return 0, false
	}
	return id, true
}

func (a *Admin) apiAccounts(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	accounts, err := a.store.Accounts(r.Context())
	if err != nil {
		a.apiInternal(w, "cannot list the accounts", err)
		return
	}
	out := make([]accountJSON, 0, len(accounts))
	for _, acct := range accounts {
		body, err := a.accountBody(r.Context(), acct)
		if err != nil {
			a.apiInternal(w, "cannot list the accounts", err)
			return
		}
		out = append(out, body)
	}
	writeJSON(w, http.StatusOK, struct {
		Accounts []accountJSON `json:"accounts"`
	}{out})
}

func (a *Admin) apiAccount(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	if id, ok := pathAccount(w, r); ok {
		a.accountAnswer(w, r, id, nil)
	}
}

func (a *Admin) apiAddAccount(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	var body struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	acct, err := a.addAccount(r.Context(), body.Name)
	if a.apiFailed(w, err) {
		return
	}
	out, err := a.accountBody(r.Context(), acct)
	if a.apiFailed(w, err) {
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (a *Admin) apiChangeAccount(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	id, ok := pathAccount(w, r)
	if !ok {
		return
	}
	var body struct {
		Name       *string     `json:"name"`
		MaxStreams *int        `json:"max_streams"`
		Outputs    *apiOutputs `json:"outputs"`
		ThemeID    *int64      `json:"theme_id"`
		LineupID   *int64      `json:"lineup_id"`
		Profile    *string     `json:"output_profile"`
		// Sources is null for every source, or the IDs of some (D114).
		Sources json.RawMessage `json:"sources"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.Name == nil && body.MaxStreams == nil && body.Outputs == nil && body.ThemeID == nil && body.LineupID == nil &&
		body.Profile == nil && body.Sources == nil {
		apiError(w, http.StatusBadRequest, "bad_json",
			"Say what to change: name, max_streams, outputs, theme_id, lineup_id, output_profile or sources.")
		return
	}
	var sources store.Sources
	if body.Sources != nil && string(body.Sources) != "null" {
		sources.Only = true
		if err := json.Unmarshal(body.Sources, &sources.IDs); err != nil {
			apiError(w, http.StatusBadRequest, "bad_sources", "Give sources as null, for every source, or a list of source IDs.")
			return
		}
	}
	ctx := r.Context()
	if _, err := a.account(ctx, id); a.apiFailed(w, err) {
		return
	}
	var err error
	if body.MaxStreams != nil {
		if _, err = a.setStreams(ctx, id, *body.MaxStreams); err != nil {
			a.accountAnswer(w, r, id, err)
			return
		}
	}
	if body.ThemeID != nil {
		if _, err = a.setAccountTheme(ctx, id, *body.ThemeID); err != nil {
			a.accountAnswer(w, r, id, err)
			return
		}
	}
	if body.LineupID != nil {
		if _, err = a.setAccountLineup(ctx, id, *body.LineupID); err != nil {
			a.accountAnswer(w, r, id, err)
			return
		}
	}
	if body.Profile != nil {
		if _, err = a.setOutputProfile(ctx, id, *body.Profile); err != nil {
			a.accountAnswer(w, r, id, err)
			return
		}
	}
	if body.Sources != nil {
		if _, err = a.setAccountSources(ctx, id, sources); err != nil {
			a.accountAnswer(w, r, id, err)
			return
		}
	}
	if body.Outputs != nil {
		if _, err = a.setOutputs(ctx, id, store.Outputs{M3U: body.Outputs.M3U, Xtream: body.Outputs.Xtream}); err != nil {
			a.accountAnswer(w, r, id, err)
			return
		}
	}
	if body.Name != nil {
		_, err = a.renameAccount(ctx, id, *body.Name)
	}
	a.accountAnswer(w, r, id, err)
}

func (a *Admin) apiRemoveAccount(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	id, ok := pathAccount(w, r)
	if !ok {
		return
	}
	if _, err := a.removeAccount(r.Context(), id); a.apiFailed(w, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *Admin) apiSetLogin(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	id, ok := pathAccount(w, r)
	if !ok {
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	_, err := a.setLogin(r.Context(), id, body.Username, body.Password)
	a.accountAnswer(w, r, id, err)
}

func (a *Admin) apiRemoveLogin(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	id, ok := pathAccount(w, r)
	if !ok {
		return
	}
	_, err := a.removeLogin(r.Context(), id)
	a.accountAnswer(w, r, id, err)
}

func (a *Admin) apiNewToken(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	id, ok := pathAccount(w, r)
	if !ok {
		return
	}
	_, token, err := a.newToken(r.Context(), id)
	if a.apiFailed(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Token string `json:"token"`
	}{token})
}

func (a *Admin) apiToken(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	id, ok := pathAccount(w, r)
	if !ok {
		return
	}
	if _, err := a.account(r.Context(), id); a.apiFailed(w, err) {
		return
	}
	token, err := a.store.AccountToken(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		apiError(w, http.StatusNotFound, "no_token", "The account has no playlist token yet.")
		return
	}
	if err != nil {
		a.apiInternal(w, "cannot read an account's token", err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Token string `json:"token"`
	}{token})
}

func (a *Admin) apiSetAccountExpiry(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	id, ok := pathAccount(w, r)
	if !ok {
		return
	}
	var body struct {
		ExpiresAt json.RawMessage `json:"expires_at"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	var t time.Time
	if len(body.ExpiresAt) == 0 || string(body.ExpiresAt) == "null" || json.Unmarshal(body.ExpiresAt, &t) != nil || t.IsZero() {
		apiError(w, http.StatusBadRequest, "bad_date", "Give expires_at as a time, such as 2028-01-01T00:00:00Z; to remove the end date, use DELETE.")
		return
	}
	_, err := a.setAccountExpiry(r.Context(), id, t)
	a.accountAnswer(w, r, id, err)
}

func (a *Admin) apiRemoveAccountExpiry(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	id, ok := pathAccount(w, r)
	if !ok {
		return
	}
	_, err := a.setAccountExpiry(r.Context(), id, time.Time{})
	a.accountAnswer(w, r, id, err)
}

func (a *Admin) apiSetAccountPIN(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	id, ok := pathAccount(w, r)
	if !ok {
		return
	}
	var body struct {
		PIN string `json:"pin"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.PIN == "" {
		apiError(w, http.StatusBadRequest, "bad_pin", "Give the PIN; to remove it, use DELETE.")
		return
	}
	_, err := a.setAccountPIN(r.Context(), id, body.PIN)
	a.accountAnswer(w, r, id, err)
}

func (a *Admin) apiRemoveAccountPIN(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	id, ok := pathAccount(w, r)
	if !ok {
		return
	}
	_, err := a.setAccountPIN(r.Context(), id, "")
	a.accountAnswer(w, r, id, err)
}

// sourcesForm reads the sources that an account uses from its card's form:
// every source, or those ticked.
func sourcesForm(r *http.Request) (store.Sources, error) {
	if r.PostFormValue("every") != "" {
		return store.EverySource, nil
	}
	u := store.Sources{Only: true}
	for _, v := range r.PostForm["source"] {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			return store.Sources{}, badDevice(http.StatusBadRequest, "bad_sources", "Choose among this server's sources.")
		}
		u.IDs = append(u.IDs, id)
	}
	return u, nil
}
