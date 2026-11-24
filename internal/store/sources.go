// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/ncruces/go-sqlite3"

	"overscan.gureet.ca/internal/source"
)

// Sources live in the database (D46). A source's address can hold the
// credentials of an account, so the store keeps it sealed with its key, and
// shows only its host. A source in the configuration file is imported once,
// by name.

// How a source's channels are numbered (D40).
const (
	NumbersPlaylist = source.NumbersPlaylist
	NumbersProvider = source.NumbersProvider
)

var (
	// ErrSourceExists is returned for a source named as another is.
	ErrSourceExists = errors.New("store: a source has that name")
	// ErrNoAddress is returned for a source that has no address saved.
	ErrNoAddress = errors.New("store: the source has no address")
)

// AddressState says whether a source's address can be used.
type AddressState int

// Address states.
const (
	AddressMissing AddressState = iota // none is saved: the source cannot be read
	AddressSaved                       // saved, and the store's key opens it
	AddressLocked                      // saved with another key file: it must be entered again
)

// Source is a source of channels, without its address.
type Source struct {
	ID             int64
	Name           string
	Type           string
	Host           string // where its address points (source.Host)
	UserAgent      string // "" for Overscan's own
	Enabled        bool
	MaxConnections int // 0 for no limit
	Refresh        time.Duration
	Numbers        string // NumbersPlaylist or NumbersProvider
	// CatchupZone is the time zone of its catch-up addresses (D81): "" for
	// the server's own.
	CatchupZone string
	// GuideLogos says how the icons of the source's guide are used as its
	// channels' logos (D141): a GuideLogos way, GuideLogosNone at first.
	GuideLogos string
	// NewCategoriesHidden makes the categories that the source adds later
	// start hidden in the lineup (D47).
	NewCategoriesHidden bool
	// PublicOnly keeps the source's requests off the server's own networks
	// (D121).
	PublicOnly   bool
	RefreshedAt  time.Time // zero if no refresh has ended
	RefreshError string    // why the last refresh failed, "" if it worked
	Address      AddressState
	Channels     int // the channels it lists now
	// Account is what an Xtream server last said about the account; zero
	// for other sources, and until a server has said.
	Account SourceAccount
	// Guide is what the last read of the source's guide found (D51).
	Guide GuideState
}

// SourceAccount is what an Xtream server says about the account that a
// source reads it with (D50).
type SourceAccount struct {
	Status         string    // such as Active or Expired; "" if not known
	ExpiresAt      time.Time // zero if it does not expire, or is not known
	MaxConnections int       // how many streams it may run at once; 0 if not known
	TimeZone       string    // the server's IANA time zone, "" if not known
}

// StreamLimit returns how many streams the source may run at once: its own
// setting, or the account's limit where that is lower. 0 means no limit.
func (src Source) StreamLimit() int {
	limit := src.MaxConnections
	if a := src.Account.MaxConnections; a > 0 && (limit == 0 || a < limit) {
		limit = a
	}
	return limit
}

// Settings returns the source's settings, without its address.
func (src Source) Settings() source.Settings {
	return source.Settings{
		Name:           src.Name,
		Type:           src.Type,
		UserAgent:      src.UserAgent,
		MaxConnections: src.MaxConnections,
		Refresh:        src.Refresh,
		Numbers:        src.Numbers,
		CatchupZone:    src.CatchupZone,
		PublicOnly:     src.PublicOnly,
	}
}

const sourceSelect = `
	SELECT s.id, s.name, s.type, s.host, s.user_agent, s.enabled, s.max_connections, s.refresh_seconds,
		s.numbers, s.catchup_zone, s.new_categories_hidden, s.guide_logos, s.public_only, s.refreshed_at, s.refresh_error, ss.secret,
		(SELECT count(*) FROM channels c WHERE c.source_id = s.id AND c.missing_since IS NULL),
		s.account_status, s.account_expires, s.account_connections, s.account_time_zone,
		g.read_at, COALESCE(g.read_from, ''), COALESCE(g.read_error, ''), COALESCE(g.channels, 0),
		COALESCE(g.programmes, 0), COALESCE(g.listed_url IS NOT NULL, 0)
	FROM sources s LEFT JOIN source_secrets ss ON ss.source_id = s.id
		LEFT JOIN source_guides g ON g.source_id = s.id`

// scanSource reads a row of sourceSelect, and returns the source and its
// sealed address.
func (s *Store) scanSource(row rowScanner) (Source, []byte, error) {
	var src Source
	var seconds int64
	var refreshed, expires, guideRead sql.NullString
	var sealed []byte
	if err := row.Scan(&src.ID, &src.Name, &src.Type, &src.Host, &src.UserAgent, &src.Enabled, &src.MaxConnections,
		&seconds, &src.Numbers, &src.CatchupZone, &src.NewCategoriesHidden, &src.GuideLogos, &src.PublicOnly, &refreshed, &src.RefreshError, &sealed, &src.Channels,
		&src.Account.Status, &expires, &src.Account.MaxConnections, &src.Account.TimeZone,
		&guideRead, &src.Guide.ReadFrom, &src.Guide.Error, &src.Guide.Channels, &src.Guide.Programmes,
		&src.Guide.Listed); err != nil {
		return Source{}, nil, err
	}
	src.Refresh = time.Duration(seconds) * time.Second
	for _, t := range []struct {
		text sql.NullString
		to   *time.Time
	}{{refreshed, &src.RefreshedAt}, {expires, &src.Account.ExpiresAt}, {guideRead, &src.Guide.ReadAt}} {
		if t.text.Valid {
			v, err := parseTime(t.text.String)
			if err != nil {
				return Source{}, nil, err
			}
			*t.to = v
		}
	}
	switch box := s.box.Load(); {
	case sealed == nil:
		src.Address = AddressMissing
	case box == nil || box.Owns(sealed):
		src.Address = AddressSaved
	default:
		src.Address = AddressLocked
	}
	return src, sealed, nil
}

// Sources returns every source, by name.
func (s *Store) Sources(ctx context.Context) ([]Source, error) {
	rows, err := s.db.QueryContext(ctx, sourceSelect+" ORDER BY s.name")
	if err != nil {
		return nil, fmt.Errorf("store: sources: %w", err)
	}
	defer rows.Close()
	var out []Source
	for rows.Next() {
		src, _, err := s.scanSource(rows)
		if err != nil {
			return nil, fmt.Errorf("store: sources: %w", err)
		}
		out = append(out, src)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: sources: %w", err)
	}
	return out, nil
}

// Source returns a source, or ErrNotFound.
func (s *Store) Source(ctx context.Context, id int64) (Source, error) {
	src, _, err := s.scanSource(s.db.QueryRowContext(ctx, sourceSelect+" WHERE s.id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Source{}, ErrNotFound
	}
	if err != nil {
		return Source{}, fmt.Errorf("store: source %d: %w", id, err)
	}
	return src, nil
}

// SourceSettings returns a source's settings with its address, for reading
// the source. It returns ErrNoAddress if none is saved, and ErrUnreadable if
// the store's key cannot open it.
func (s *Store) SourceSettings(ctx context.Context, id int64) (source.Settings, error) {
	src, sealed, err := s.scanSource(s.db.QueryRowContext(ctx, sourceSelect+" WHERE s.id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	if err == nil && sealed == nil {
		err = ErrNoAddress
	}
	var secret sourceSecret
	if err == nil {
		secret, err = s.openSource(id, sealed)
	}
	if err != nil {
		return source.Settings{}, fmt.Errorf("store: source %d: %w", id, err)
	}
	settings := src.Settings()
	settings.URL, settings.Username, settings.Password = secret.URL, secret.Username, secret.Password
	if secret.Device != nil {
		settings.Device = *secret.Device
	}
	return settings, nil
}

// sourceSecret is what a source keeps sealed: its address, an Xtream
// source's login, and a Stalker portal's box and login (D76).
type sourceSecret struct {
	URL      string         `json:"url"`
	Username string         `json:"username,omitempty"`
	Password string         `json:"password,omitempty"`
	Device   *source.Device `json:"device,omitempty"`
}

func sourceContext(id int64) string { return "source/" + strconv.FormatInt(id, 10) }

func (s *Store) sealSource(id int64, secret sourceSecret) ([]byte, error) {
	data, err := json.Marshal(secret)
	if err != nil {
		return nil, err
	}
	return s.seal(sourceContext(id), string(data))
}

func (s *Store) openSource(id int64, sealed []byte) (sourceSecret, error) {
	var secret sourceSecret
	data, err := s.open(sourceContext(id), sealed)
	if err != nil {
		return secret, err
	}
	if err := json.Unmarshal([]byte(data), &secret); err != nil {
		return secret, ErrUnreadable
	}
	return secret, nil
}

// AddSource adds a source, with the defaults where the settings have none,
// and returns its ID. Settings that break a rule give a *source.Error; a
// name that another source has gives ErrSourceExists.
func (s *Store) AddSource(ctx context.Context, settings source.Settings) (int64, error) {
	settings = settings.WithDefaults()
	if err := settings.Check(); err != nil {
		return 0, err
	}
	fail := func(err error) (int64, error) { return 0, fmt.Errorf("store: add source: %w", err) }
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fail(err)
	}
	defer tx.Rollback()
	id, err := insertSource(ctx, tx, settings)
	if err != nil {
		return fail(err)
	}
	if err := s.saveAddress(ctx, tx, id, settings); err != nil {
		return fail(err)
	}
	if err := tx.Commit(); err != nil {
		return fail(err)
	}
	return id, nil
}

func insertSource(ctx context.Context, tx *sql.Tx, settings source.Settings) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `
		INSERT INTO sources (name, type, host, user_agent, max_connections, refresh_seconds, numbers)
		VALUES (?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		settings.Name, settings.Type, settings.Host(), settings.UserAgent, settings.MaxConnections,
		int64(settings.Refresh/time.Second), settings.Numbers).Scan(&id)
	if errors.Is(err, sqlite3.CONSTRAINT_UNIQUE) {
		return 0, ErrSourceExists
	}
	if err == nil && settings.CatchupZone != "" {
		_, err = tx.ExecContext(ctx, "UPDATE sources SET catchup_zone = ? WHERE id = ?", settings.CatchupZone, id)
	}
	if err == nil && settings.PublicOnly {
		_, err = tx.ExecContext(ctx, "UPDATE sources SET public_only = 1 WHERE id = ?", id)
	}
	return id, err
}

// saveAddress seals a source's address and login and keeps them, with the
// address's host.
func (s *Store) saveAddress(ctx context.Context, tx *sql.Tx, id int64, settings source.Settings) error {
	secret := sourceSecret{URL: settings.URL, Username: settings.Username, Password: settings.Password}
	if settings.Device != (source.Device{}) {
		secret.Device = &settings.Device
	}
	sealed, err := s.sealSource(id, secret)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO source_secrets (source_id, secret) VALUES (?, ?)
		ON CONFLICT (source_id) DO UPDATE SET secret = excluded.secret`, id, sealed); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE sources SET host = ? WHERE id = ?", settings.Host(), id)
	return err
}

// UpdateSource changes a source's settings. When the settings leave out the
// address, and for an Xtream source the username and password too, the
// saved ones stay. An Xtream source that keeps its type can also be given
// only some of them, and keeps the rest, and a Jellyfin or a Plex source
// its address, or its API key or token; a source that changes its type
// needs all of them. A change of
// numbering numbers the source's channels again (D40); their pinned numbers
// stay (D47). A new address or login, or a new type, makes the source's
// films and series due to be read again (D63).
func (s *Store) UpdateSource(ctx context.Context, id int64, settings source.Settings) error {
	settings = settings.WithDefaults()
	fail := func(err error) error { return fmt.Errorf("store: source %d: %w", id, err) }
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fail(err)
	}
	defer tx.Rollback()
	var numbers, oldType string
	var sealed []byte
	err = tx.QueryRowContext(ctx, `
		SELECT s.numbers, s.type, ss.secret FROM sources s LEFT JOIN source_secrets ss ON ss.source_id = s.id
		WHERE s.id = ?`, id).Scan(&numbers, &oldType, &sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return fail(ErrNotFound)
	}
	if err != nil {
		return fail(err)
	}
	keep, err := s.fillLogin(id, oldType, sealed, &settings)
	if err != nil {
		return err
	}
	check := settings
	if keep && settings.Type != source.TypeStalker {
		// The saved address and login, checked when they were saved.
		check.URL, check.Username, check.Password = "/kept", "", ""
		switch check.Type {
		case source.TypeXtream, source.TypeJellyfin:
			check.URL, check.Username, check.Password = "http://kept.invalid", "kept", "kept"
		case source.TypePlex:
			check.URL, check.Password = "http://kept.invalid", "kept"
		}
	}
	if err := check.Check(); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE sources SET name = ?, type = ?, user_agent = ?, max_connections = ?, refresh_seconds = ?, numbers = ?,
			catchup_zone = ?, public_only = ?
		WHERE id = ?`,
		settings.Name, settings.Type, settings.UserAgent, settings.MaxConnections, int64(settings.Refresh/time.Second),
		settings.Numbers, settings.CatchupZone, settings.PublicOnly, id)
	if errors.Is(err, sqlite3.CONSTRAINT_UNIQUE) {
		return fail(ErrSourceExists)
	}
	if err != nil {
		return fail(err)
	}
	if !keep {
		if err := s.saveAddress(ctx, tx, id, settings); err != nil {
			return fail(err)
		}
	}
	if oldType != settings.Type {
		if err := setAccount(ctx, tx, id, SourceAccount{}); err != nil {
			return fail(err)
		}
	}
	if !keep || oldType != settings.Type {
		// What the old ones listed stays until a device asks for the list,
		// which then waits for the new one.
		if _, err := tx.ExecContext(ctx,
			"UPDATE library_reads SET read_at = NULL, read_error = '' WHERE source_id = ?", id); err != nil {
			return fail(err)
		}
	}
	if numbers != settings.Numbers {
		if err := numberEverywhere(ctx, tx, id); err != nil {
			return fail(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fail(err)
	}
	return nil
}

// fillLogin fills in what settings leave out of a source's address and
// login from the saved ones, and reports whether they leave out all of it,
// so that the saved ones stay as they are. Only a source that keeps its type
// keeps any of them; an Xtream source can be given part of its login. A
// Jellyfin or a Plex source keeps its address and its API key or token
// when they are left out, but a Jellyfin source's user is always as given,
// since "" means every library.
func (s *Store) fillLogin(id int64, oldType string, sealed []byte, settings *source.Settings) (keep bool, err error) {
	if settings.Type == source.TypeJellyfin || settings.Type == source.TypePlex {
		return s.fillServer(id, oldType, sealed, settings)
	}
	if settings.Type == source.TypeStalker {
		return s.fillPortal(id, oldType, sealed, settings)
	}
	xtream := settings.Type == source.TypeXtream
	enterAll := &source.Error{Field: "url", Text: "Enter the playlist's address."}
	switch settings.Type {
	case source.TypeXtream:
		enterAll = &source.Error{Field: "url", Text: "Enter the server's address, the username and the password."}
	case source.TypeFolder:
		enterAll = &source.Error{Field: "url", Text: "Enter the folder's path."}
	}
	given := settings.URL != "" || settings.Username != "" || settings.Password != ""
	switch {
	case !given && settings.Type == oldType:
		return true, nil
	case !given:
		return false, enterAll
	case !xtream || settings.URL != "" && settings.Username != "" && settings.Password != "":
		return false, nil
	case oldType != source.TypeXtream || sealed == nil:
		return false, enterAll
	}
	saved, err := s.openSource(id, sealed)
	if err != nil {
		enterAll.Text = "The saved login cannot be read. " + enterAll.Text
		return false, enterAll
	}
	for _, f := range []struct {
		to   *string
		from string
	}{{&settings.URL, saved.URL}, {&settings.Username, saved.Username}, {&settings.Password, saved.Password}} {
		if *f.to == "" {
			*f.to = f.from
		}
	}
	return false, nil
}

// SetSourceAccount records what an Xtream server said about a source's
// account, and reports whether the account's limit of streams changed.
func (s *Store) SetSourceAccount(ctx context.Context, sourceID int64, a SourceAccount) (bool, error) {
	var before int
	err := s.db.QueryRowContext(ctx, "SELECT account_connections FROM sources WHERE id = ?", sourceID).Scan(&before)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	if err == nil {
		err = setAccount(ctx, s.db, sourceID, a)
	}
	if err != nil {
		return false, fmt.Errorf("store: source %d: %w", sourceID, err)
	}
	return before != max(0, a.MaxConnections), nil
}

// execer runs a statement, in a transaction or not.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func setAccount(ctx context.Context, db execer, sourceID int64, a SourceAccount) error {
	var expires any
	if !a.ExpiresAt.IsZero() {
		expires = formatTime(a.ExpiresAt)
	}
	_, err := db.ExecContext(ctx, `
		UPDATE sources SET account_status = ?, account_expires = ?, account_connections = ?, account_time_zone = ?
		WHERE id = ?`, a.Status, expires, max(0, a.MaxConnections), a.TimeZone, sourceID)
	return err
}

// SetSourceEnabled turns a source on or off. Devices see no channel of a
// source that is off, and it is not read.
func (s *Store) SetSourceEnabled(ctx context.Context, id int64, enabled bool) error {
	res, err := s.db.ExecContext(ctx, "UPDATE sources SET enabled = ? WHERE id = ?", enabled, id)
	if err == nil {
		err = mustAffect(res)
	}
	if err != nil {
		return fmt.Errorf("store: source %d: %w", id, err)
	}
	return nil
}

// DeleteSource removes a source with its address, categories and channels,
// and the channels' lineup entries. The groups that Overscan made for its
// categories go, unless the administrator has changed them, and so do the
// rules that name it (D107).
func (s *Store) DeleteSource(ctx context.Context, id int64) error {
	fail := func(err error) error { return fmt.Errorf("store: delete source %d: %w", id, err) }
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fail(err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, "DELETE FROM sources WHERE id = ?", id)
	if err == nil {
		err = mustAffect(res)
	}
	var lineups []int64
	if err == nil {
		lineups, err = lineupIDs(ctx, tx)
	}
	for _, lineupID := range lineups {
		if err == nil {
			err = dropSourceRules(ctx, tx, lineupID, id)
		}
		if err == nil {
			err = removeEmptyGroups(ctx, tx, lineupID)
		}
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return fail(err)
	}
	return nil
}

// SetRefreshResult records that a refresh of a source ended at the given
// time, and why it failed ("" if it worked).
func (s *Store) SetRefreshResult(ctx context.Context, sourceID int64, at time.Time, refreshErr string) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE sources SET refreshed_at = ?, refresh_error = ? WHERE id = ?",
		formatTime(at), refreshErr, sourceID)
	if err != nil {
		return fmt.Errorf("store: source %d: %w", sourceID, err)
	}
	return nil
}

// ImportOutcome is what ImportSources did with a source.
type ImportOutcome int

// Import outcomes.
const (
	Imported       ImportOutcome = iota // imported now
	ImportedBefore                      // imported at an earlier start: the file's entry is ignored
	NameTaken                           // a source that the admin made has its name: the entry is ignored
)

// Import is what ImportSources did with a source of the configuration file.
type Import struct {
	Name    string
	ID      int64 // the source it became, if imported now
	Outcome ImportOutcome
}

// ImportSources imports the configuration file's sources (D46), each once:
// a name imported before is not imported again, even if the admin has since
// deleted its source. A source that older versions recorded under the same
// name, without an address, becomes the imported one, so that its channels
// keep their IDs and numbers; if its numbering changes, its channels are
// numbered again (D40).
func (s *Store) ImportSources(ctx context.Context, specs []source.Settings) ([]Import, error) {
	fail := func(err error) ([]Import, error) { return nil, fmt.Errorf("store: import sources: %w", err) }
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fail(err)
	}
	defer tx.Rollback()
	var out []Import
	for _, spec := range specs {
		spec = spec.WithDefaults()
		if err := spec.Check(); err != nil {
			return fail(fmt.Errorf("%s: %w", spec.Name, err))
		}
		imp := Import{Name: spec.Name}
		var before int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM config_imports WHERE name = ?", spec.Name).Scan(&before); err != nil {
			return fail(err)
		}
		var numbers string
		var hasAddress bool
		err := tx.QueryRowContext(ctx, `
			SELECT s.id, s.numbers, ss.source_id IS NOT NULL
			FROM sources s LEFT JOIN source_secrets ss ON ss.source_id = s.id WHERE s.name = ?`,
			spec.Name).Scan(&imp.ID, &numbers, &hasAddress)
		exists := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fail(err)
		}
		switch {
		case before > 0:
			imp.ID, imp.Outcome = 0, ImportedBefore
		case hasAddress:
			imp.ID, imp.Outcome = 0, NameTaken
		case exists:
			if _, err := tx.ExecContext(ctx, `
				UPDATE sources SET type = ?, user_agent = ?, max_connections = ?, refresh_seconds = ?, numbers = ?,
					catchup_zone = ?, public_only = ?, enabled = 1
				WHERE id = ?`,
				spec.Type, spec.UserAgent, spec.MaxConnections, int64(spec.Refresh/time.Second), spec.Numbers,
				spec.CatchupZone, spec.PublicOnly, imp.ID); err != nil {
				return fail(err)
			}
			if numbers != spec.Numbers {
				if err := numberEverywhere(ctx, tx, imp.ID); err != nil {
					return fail(err)
				}
			}
		default:
			if imp.ID, err = insertSource(ctx, tx, spec); err != nil {
				return fail(err)
			}
		}
		if imp.Outcome == Imported {
			if err := s.saveAddress(ctx, tx, imp.ID, spec); err != nil {
				return fail(err)
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO config_imports (name, imported_at) VALUES (?, ?)",
				spec.Name, formatTime(time.Now())); err != nil {
				return fail(err)
			}
		}
		out = append(out, imp)
	}
	if err := tx.Commit(); err != nil {
		return fail(err)
	}
	return out, nil
}

// fillPortal fills in what settings leave out of a Stalker portal's
// address and login from the saved ones, each on its own, and of its box:
// with a MAC address given, the box is as given, and a detail left out is
// none; without one, each detail left out is as saved. It reports whether
// they are then all as saved. A portal that was another type of source
// needs its address and MAC address.
func (s *Store) fillPortal(id int64, oldType string, sealed []byte, settings *source.Settings) (keep bool, err error) {
	enterAll := &source.Error{Field: "url", Text: "Enter the portal's address and the box's MAC address."}
	given := settings.URL != "" && settings.Device.MAC != ""
	if oldType != settings.Type || sealed == nil {
		if !given {
			return false, enterAll
		}
		return false, nil
	}
	saved, err := s.openSource(id, sealed)
	if err != nil {
		if !given {
			enterAll.Text = "The saved address and box cannot be read. " + enterAll.Text
			return false, enterAll
		}
		return false, nil
	}
	var device source.Device
	if saved.Device != nil {
		device = *saved.Device
	}
	type field struct {
		to   *string
		from string
	}
	fields := []field{{&settings.URL, saved.URL}, {&settings.Username, saved.Username}, {&settings.Password, saved.Password}}
	if d := &settings.Device; d.MAC == "" {
		fields = append(fields, field{&d.MAC, device.MAC}, field{&d.Serial, device.Serial}, field{&d.DeviceID, device.DeviceID},
			field{&d.DeviceID2, device.DeviceID2}, field{&d.Signature, device.Signature}, field{&d.Model, device.Model})
	}
	for _, f := range fields {
		if *f.to == "" {
			*f.to = f.from
		}
	}
	return settings.URL == saved.URL && settings.Username == saved.Username && settings.Password == saved.Password &&
		settings.Device == device, nil
}

// fillServer fills in a media server's address and its API key or token
// from the saved ones where settings leave them out, and reports whether
// the address, the key and the user are then all as saved.
func (s *Store) fillServer(id int64, oldType string, sealed []byte, settings *source.Settings) (keep bool, err error) {
	given := settings.URL != "" && settings.Password != ""
	secret := "the API key"
	if settings.Type == source.TypePlex {
		secret = "the token"
	}
	enterAll := &source.Error{Field: "url", Text: "Enter the server's address and " + secret + "."}
	if oldType != settings.Type || sealed == nil {
		if !given {
			return false, enterAll
		}
		return false, nil
	}
	saved, err := s.openSource(id, sealed)
	if err != nil {
		if !given {
			enterAll.Text = "The saved address and " + secret + " cannot be read. " + enterAll.Text
			return false, enterAll
		}
		return false, nil
	}
	if settings.URL == "" {
		settings.URL = saved.URL
	}
	if settings.Password == "" {
		settings.Password = saved.Password
	}
	return settings.URL == saved.URL && settings.Password == saved.Password && settings.Username == saved.Username, nil
}
