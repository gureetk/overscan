// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"overscan.gureet.ca/internal/source"
)

// DefaultLineupID is the lineup that new channels join: the household's,
// created with schema 1.
const DefaultLineupID = 1

// MissingGrace is how long a channel that its source stopped listing is kept,
// so that it keeps its ID and number if it comes back.
const MissingGrace = 7 * 24 * time.Hour

// CatalogChannel is a channel as a refresh of its source lists it.
type CatalogChannel struct {
	// IdentityKey stays the same for the same channel across refreshes;
	// each kind of source defines it.
	IdentityKey string
	// AltKey is the key that another kind of source would give the same
	// channel, "" if none. A channel stored under it, and not under
	// IdentityKey, takes IdentityKey, so that a source whose type changes
	// keeps its channels' IDs, numbers and edits.
	AltKey   string
	Name     string
	Number   int    // the source's number, 0 if none
	Logo     string // the source's logo address
	Category string // "" if none
	XMLTVID  string
	// StreamURL is what the source needs to play the channel: its address,
	// or for an Xtream source, the reference that source.Settings.StreamURL
	// turns into one.
	StreamURL   string
	ArchiveDays int
}

// CatalogChanges counts what ApplyCatalog changed.
type CatalogChanges struct {
	Added   int // channels new to the catalog
	Updated int // channels it had already
	Missing int // channels the source stopped listing
	Purged  int // channels missing for longer than MissingGrace, now removed
}

// ApplyCatalog makes the catalog of a source match a refresh that listed
// channels, in order, at the given time. It all happens in one transaction:
// listed channels are added or updated, unlisted ones are marked missing,
// channels missing for longer than MissingGrace are removed with their lineup
// entries, new categories join a group of the default lineup (D47), and new
// channels join the default lineup, under the number they want when that is
// free and after the highest number otherwise (D40).
func (s *Store) ApplyCatalog(ctx context.Context, sourceID int64, channels []CatalogChannel, at time.Time) (CatalogChanges, error) {
	var ch CatalogChanges
	fail := func(err error) (CatalogChanges, error) {
		return CatalogChanges{}, fmt.Errorf("store: catalog of source %d: %w", sourceID, err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fail(err)
	}
	defer tx.Rollback()
	when := formatTime(at)

	// Categories, in order of first appearance.
	categories := map[string]int64{}
	for _, c := range channels {
		if c.Category == "" {
			continue
		}
		if _, ok := categories[c.Category]; ok {
			continue
		}
		var id int64
		err := tx.QueryRowContext(ctx, `
			INSERT INTO categories (source_id, kind, name, position) VALUES (?, 'live', ?, ?)
			ON CONFLICT (source_id, kind, name) DO UPDATE SET position = excluded.position
			RETURNING id`,
			sourceID, c.Category, len(categories)+1).Scan(&id)
		if err != nil {
			return fail(err)
		}
		categories[c.Category] = id
	}

	known := map[string]bool{}
	rows, err := tx.QueryContext(ctx, "SELECT identity_key FROM channels WHERE source_id = ?", sourceID)
	if err != nil {
		return fail(err)
	}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return fail(err)
		}
		known[key] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fail(err)
	}

	// Channels stored under the key that another kind of source gives them
	// take their key from this one.
	for _, c := range channels {
		if c.AltKey == "" || c.AltKey == c.IdentityKey || known[c.IdentityKey] || !known[c.AltKey] {
			continue
		}
		if _, err := tx.ExecContext(ctx, "UPDATE channels SET identity_key = ? WHERE source_id = ? AND identity_key = ?",
			c.IdentityKey, sourceID, c.AltKey); err != nil {
			return fail(err)
		}
		delete(known, c.AltKey)
		known[c.IdentityKey] = true
	}

	// Stream addresses are sealed (D46); stream_url is for those that older
	// versions left unsealed.
	upsert, err := tx.PrepareContext(ctx, `
		INSERT INTO channels (source_id, identity_key, name, number, logo, category_id, xmltv_id,
			stream_url, stream_secret, archive_days, position, listed_at, missing_since)
		VALUES (?, ?, ?, ?, ?, ?, ?, '', ?, ?, ?, ?, NULL)
		ON CONFLICT (source_id, identity_key) DO UPDATE SET
			name = excluded.name,
			number = excluded.number,
			logo = excluded.logo,
			category_id = excluded.category_id,
			xmltv_id = excluded.xmltv_id,
			stream_url = '',
			stream_secret = excluded.stream_secret,
			archive_days = excluded.archive_days,
			position = excluded.position,
			listed_at = excluded.listed_at,
			missing_since = NULL`)
	if err != nil {
		return fail(err)
	}
	defer upsert.Close()
	for i, c := range channels {
		var category any
		if id, ok := categories[c.Category]; ok {
			category = id
		}
		sealed, err := s.seal(streamContext(sourceID), c.StreamURL)
		if err != nil {
			return fail(err)
		}
		if _, err := upsert.ExecContext(ctx, sourceID, c.IdentityKey, c.Name, c.Number, c.Logo, category,
			c.XMLTVID, sealed, c.ArchiveDays, i+1, when); err != nil {
			return fail(err)
		}
		if known[c.IdentityKey] {
			ch.Updated++
		} else {
			ch.Added++
			known[c.IdentityKey] = true
		}
	}

	res, err := tx.ExecContext(ctx, `
		UPDATE channels SET missing_since = ?
		WHERE source_id = ? AND listed_at <> ? AND missing_since IS NULL`,
		when, sourceID, when)
	if err != nil {
		return fail(err)
	}
	if ch.Missing, err = rowsAffected(res); err != nil {
		return fail(err)
	}
	res, err = tx.ExecContext(ctx, `
		DELETE FROM channels
		WHERE source_id = ? AND missing_since IS NOT NULL AND missing_since < ?`,
		sourceID, formatTime(at.Add(-MissingGrace)))
	if err != nil {
		return fail(err)
	}
	if ch.Purged, err = rowsAffected(res); err != nil {
		return fail(err)
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM categories
		WHERE source_id = ? AND NOT EXISTS (SELECT 1 FROM channels WHERE category_id = categories.id)`,
		sourceID); err != nil {
		return fail(err)
	}

	if err := groupCategories(ctx, tx, DefaultLineupID, sourceID); err != nil {
		return fail(err)
	}
	if err := numberChannels(ctx, tx, DefaultLineupID, sourceID, false); err != nil {
		return fail(err)
	}
	if err := tx.Commit(); err != nil {
		return fail(err)
	}
	return ch, nil
}

// Numbering (D40, D47). A channel that its source lists wants a number: its
// place in the playlist, counting from 1, or the number the playlist gives it
// (tvg-chno), by the source's numbers setting. It gets that number unless a
// listed channel holds it already, or the administrator has pinned it to
// another channel. A channel the source no longer lists, kept for
// MissingGrace, gives its number up to a listed channel that wants it, unless
// the number is pinned; its entry keeps its edits, without a number, and gets
// one again when the channel comes back. A channel that wants no number, or
// cannot have the one it wants, goes after the highest number in the lineup.
// Renumbering never moves a pinned number.

// renumberOffset moves the entries being numbered again out of the way, so
// that no number is held twice meanwhile. Real numbers stay below it.
const renumberOffset = 1_000_000

// MaxNumber is the highest number a channel can have.
const MaxNumber = renumberOffset - 1

// numberChannels numbers the listed channels of a source in a lineup, in the
// order of the playlist. Without renumber, it numbers only the channels that
// have no entry yet, or an entry without a number. With renumber, it numbers
// every listed channel again whose number is not pinned, keeping its entry,
// and takes the numbers of the source's missing channels that are not pinned.
func numberChannels(ctx context.Context, tx *sql.Tx, lineupID, sourceID int64, renumber bool) error {
	var numbers string
	if err := tx.QueryRowContext(ctx, "SELECT numbers FROM sources WHERE id = ?", sourceID).Scan(&numbers); err != nil {
		return err
	}
	if renumber {
		if _, err := tx.ExecContext(ctx, `
			UPDATE lineup_entries SET number = NULL
			WHERE lineup_id = ? AND pinned = 0 AND channel_id IN (
				SELECT id FROM channels WHERE source_id = ? AND missing_since IS NOT NULL)`,
			lineupID, sourceID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE lineup_entries SET number = number + ?
			WHERE lineup_id = ? AND pinned = 0 AND number IS NOT NULL AND channel_id IN (
				SELECT id FROM channels WHERE source_id = ? AND missing_since IS NULL)`,
			renumberOffset, lineupID, sourceID); err != nil {
			return err
		}
	}

	// The numbers held in the lineup, apart from those being numbered again.
	type holder struct {
		entryID int64
		missing bool
		pinned  bool
	}
	held := map[int]holder{}
	highest := 0
	rows, err := tx.QueryContext(ctx, `
		SELECT e.id, e.number, c.missing_since IS NOT NULL, e.pinned
		FROM lineup_entries e JOIN channels c ON c.id = e.channel_id
		WHERE e.lineup_id = ? AND e.number < ?`,
		lineupID, renumberOffset)
	if err != nil {
		return err
	}
	for rows.Next() {
		var h holder
		var n int
		if err := rows.Scan(&h.entryID, &n, &h.missing, &h.pinned); err != nil {
			rows.Close()
			return err
		}
		held[n] = h
		highest = max(highest, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	type candidate struct {
		channelID int64
		entryID   int64 // 0 for a channel without an entry
		wants     int   // 0 for none
	}
	var cands []candidate
	rows, err = tx.QueryContext(ctx, `
		SELECT c.id, COALESCE(e.id, 0), c.number, c.position
		FROM channels c
		LEFT JOIN lineup_entries e ON e.channel_id = c.id AND e.lineup_id = ?
		WHERE c.source_id = ? AND c.missing_since IS NULL AND COALESCE(e.pinned, 0) = 0
			AND (? OR e.id IS NULL OR e.number IS NULL)
		ORDER BY c.position`,
		lineupID, sourceID, renumber)
	if err != nil {
		return err
	}
	for rows.Next() {
		var c candidate
		var own, position int
		if err := rows.Scan(&c.channelID, &c.entryID, &own, &position); err != nil {
			rows.Close()
			return err
		}
		c.wants = own
		if numbers == NumbersPlaylist {
			c.wants = position
		}
		cands = append(cands, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// Channels that can have the number they want come first, so that a
	// channel without one cannot take a number that a later channel wants.
	assigned := make([]int, len(cands))
	var evicted []int64
	for i, c := range cands {
		h, taken := held[c.wants]
		if c.wants <= 0 || c.wants > MaxNumber || (taken && (!h.missing || h.pinned)) {
			continue
		}
		if taken {
			evicted = append(evicted, h.entryID)
		}
		held[c.wants] = holder{entryID: c.entryID}
		assigned[i] = c.wants
		highest = max(highest, c.wants)
	}
	next := highest + 1
	for i := range cands {
		if assigned[i] != 0 {
			continue
		}
		for {
			if _, taken := held[next]; !taken {
				break
			}
			next++
		}
		assigned[i] = next
		held[next] = holder{entryID: cands[i].entryID}
	}

	// An entry that gives its number up keeps its edits.
	for _, id := range evicted {
		if _, err := tx.ExecContext(ctx, "UPDATE lineup_entries SET number = NULL WHERE id = ?", id); err != nil {
			return err
		}
	}
	insert, err := tx.PrepareContext(ctx, "INSERT INTO lineup_entries (lineup_id, channel_id, number) VALUES (?, ?, ?)")
	if err != nil {
		return err
	}
	defer insert.Close()
	update, err := tx.PrepareContext(ctx, "UPDATE lineup_entries SET number = ? WHERE id = ?")
	if err != nil {
		return err
	}
	defer update.Close()
	for i, c := range cands {
		if c.entryID != 0 {
			_, err = update.ExecContext(ctx, assigned[i], c.entryID)
		} else {
			_, err = insert.ExecContext(ctx, lineupID, c.channelID, assigned[i])
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// groupCategories puts each live category of a source that has no group in
// the lineup yet into the group of its name, making one if there is none,
// hidden if the source makes new categories start hidden (D47). It then
// removes the groups that Overscan made and that have nothing in them.
func groupCategories(ctx context.Context, tx *sql.Tx, lineupID, sourceID int64) error {
	var hidden bool
	if err := tx.QueryRowContext(ctx, "SELECT new_categories_hidden FROM sources WHERE id = ?", sourceID).Scan(&hidden); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT k.name FROM categories k
		WHERE k.source_id = ? AND k.kind = 'live' AND NOT EXISTS (
			SELECT 1 FROM lineup_categories m
			WHERE m.lineup_id = ? AND m.source_id = k.source_id AND m.category = k.name)
		ORDER BY k.position`,
		sourceID, lineupID)
	if err != nil {
		return err
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		names = append(names, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, name := range names {
		var groupID int64
		err := tx.QueryRowContext(ctx, `
			INSERT INTO lineup_groups (lineup_id, name, hidden) VALUES (?, ?, ?)
			ON CONFLICT (lineup_id, name) DO UPDATE SET name = excluded.name
			RETURNING id`,
			lineupID, name, hidden).Scan(&groupID)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO lineup_categories (lineup_id, source_id, category, group_id) VALUES (?, ?, ?, ?)",
			lineupID, sourceID, name, groupID); err != nil {
			return err
		}
	}
	return removeEmptyGroups(ctx, tx, lineupID)
}

// removeEmptyGroups removes the groups of a lineup that Overscan made, that
// the administrator has not changed, and that hold no category that exists
// and no channel moved there.
func removeEmptyGroups(ctx context.Context, tx *sql.Tx, lineupID int64) error {
	_, err := tx.ExecContext(ctx, `
		DELETE FROM lineup_groups
		WHERE lineup_id = ? AND edited = 0
			AND NOT EXISTS (
				SELECT 1 FROM lineup_categories m
				JOIN categories k ON k.source_id = m.source_id AND k.kind = 'live' AND k.name = m.category
				WHERE m.group_id = lineup_groups.id)
			AND NOT EXISTS (SELECT 1 FROM lineup_entries e WHERE e.group_id = lineup_groups.id)`,
		lineupID)
	return err
}

func rowsAffected(res sql.Result) (int, error) {
	n, err := res.RowsAffected()
	return int(n), err
}

// LineupChannel is a channel as a lineup shows it, with the administrator's
// edits (D47). It carries no stream address: devices never get one from the
// catalog.
type LineupChannel struct {
	EntryID     int64
	ChannelID   int64
	Number      int
	Name        string
	Logo        string // the source's logo address, "" if none
	GroupID     int64  // 0 if none
	Group       string // "" if none
	XMLTVID     string
	ArchiveDays int
	Locked      bool // it, or its group, needs the parental PIN (D55)
}

// lineupEntryJoins joins a lineup entry e to its channel c, the channel's
// source s, its category k and the entry's group g: the one the entry names,
// else its category's (D47).
const lineupEntryJoins = `
		FROM lineup_entries e
		JOIN channels c ON c.id = e.channel_id
		JOIN sources s ON s.id = c.source_id
		LEFT JOIN categories k ON k.id = c.category_id
		LEFT JOIN lineup_categories m ON m.lineup_id = e.lineup_id AND m.source_id = c.source_id AND m.category = k.name
		LEFT JOIN lineup_groups g ON g.id = COALESCE(e.group_id, m.group_id)`

// lockedEntry is true of an entry that needs the parental PIN: it or its
// group is locked (D55).
const lockedEntry = `(e.locked = 1 OR COALESCE(g.locked, 0) = 1)`

// shownEntry is true of an entry that devices see: numbered, not hidden on
// its own or by its group, for a channel that its source still lists, from
// an enabled source.
const shownEntry = `e.number IS NOT NULL AND COALESCE(e.hidden, g.hidden, 0) = 0
		AND c.missing_since IS NULL AND s.enabled = 1`

// LineupChannels returns what a lineup shows, by number.
func (s *Store) LineupChannels(ctx context.Context, lineupID int64) ([]LineupChannel, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.id, c.id, e.number, COALESCE(e.name, c.name), c.logo, COALESCE(g.id, 0), COALESCE(g.name, ''),
			c.xmltv_id, c.archive_days, `+lockedEntry+lineupEntryJoins+`
		WHERE e.lineup_id = ? AND `+shownEntry+`
		ORDER BY e.number`,
		lineupID)
	if err != nil {
		return nil, fmt.Errorf("store: lineup %d: %w", lineupID, err)
	}
	defer rows.Close()
	var out []LineupChannel
	for rows.Next() {
		var c LineupChannel
		if err := rows.Scan(&c.EntryID, &c.ChannelID, &c.Number, &c.Name, &c.Logo, &c.GroupID, &c.Group,
			&c.XMLTVID, &c.ArchiveDays, &c.Locked); err != nil {
			return nil, fmt.Errorf("store: lineup %d: %w", lineupID, err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: lineup %d: %w", lineupID, err)
	}
	return out, nil
}

// PlayableEntry is what the stream hub needs to play a lineup entry. It
// holds the stream address, with any credentials in it, so it never goes to
// a device.
type PlayableEntry struct {
	EntryID   int64
	LineupID  int64
	ChannelID int64
	SourceID  int64
	Name      string
	StreamURL string
	Locked    bool // it needs the parental PIN (D55)
	Repair    bool // its stream goes through ffmpeg: its own switch says so, or else the default does (D57, D58)
	RepairHLS bool // the server repairs HLS streams too, made into one continuous stream (D61)
}

// PlayableEntry returns the lineup entry with the given ID if a device may
// play it: devices see it in its lineup. Otherwise it returns ErrNotFound.
func (s *Store) PlayableEntry(ctx context.Context, entryID int64) (PlayableEntry, error) {
	var p PlayableEntry
	var sealed, sourceSealed []byte
	var sourceName, sourceType string
	err := s.db.QueryRowContext(ctx, `
		SELECT e.id, e.lineup_id, c.id, c.source_id, COALESCE(e.name, c.name), c.stream_url, c.stream_secret,
			s.name, s.type, (SELECT secret FROM source_secrets WHERE source_id = s.id), `+lockedEntry+`, COALESCE(c.repair, `+repairDefaultSQL+`), `+repairHLSSQL+
		lineupEntryJoins+`
		WHERE e.id = ? AND `+shownEntry,
		entryID).Scan(&p.EntryID, &p.LineupID, &p.ChannelID, &p.SourceID, &p.Name, &p.StreamURL, &sealed,
		&sourceName, &sourceType, &sourceSealed, &p.Locked, &p.Repair, &p.RepairHLS)
	if errors.Is(err, sql.ErrNoRows) {
		return PlayableEntry{}, ErrNotFound
	}
	if err == nil && sealed != nil {
		p.StreamURL, err = s.open(streamContext(p.SourceID), sealed)
	}
	if err == nil && sourceType == source.TypeXtream {
		// The stream's address is made now, from the source's login (D50).
		var secret sourceSecret
		if sourceSealed == nil {
			err = ErrNoAddress
		} else if secret, err = s.openSource(p.SourceID, sourceSealed); err == nil {
			settings := source.Settings{Name: sourceName, Type: sourceType, URL: secret.URL,
				Username: secret.Username, Password: secret.Password}
			p.StreamURL, err = settings.StreamURL(p.StreamURL)
		}
	}
	if err != nil {
		return PlayableEntry{}, fmt.Errorf("store: lineup entry %d: %w", entryID, err)
	}
	return p, nil
}

// StreamsByKey returns the stream addresses, or for an Xtream source the
// references, of a source's channels by their identity keys, leaving out
// keys that start with except. They can hold credentials, so they never go
// to a device or a log.
func (s *Store) StreamsByKey(ctx context.Context, sourceID int64, except string) (map[string]string, error) {
	fail := func(err error) (map[string]string, error) {
		return nil, fmt.Errorf("store: streams of source %d: %w", sourceID, err)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT identity_key, stream_url, stream_secret FROM channels
		WHERE source_id = ? AND substr(identity_key, 1, length(?)) <> ?`, sourceID, except, except)
	if err != nil {
		return fail(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var key, plain string
		var sealed []byte
		if err := rows.Scan(&key, &plain, &sealed); err != nil {
			return fail(err)
		}
		if sealed != nil {
			if plain, err = s.open(streamContext(sourceID), sealed); err != nil {
				return fail(err)
			}
		}
		out[key] = plain
	}
	if err := rows.Err(); err != nil {
		return fail(err)
	}
	return out, nil
}

// streamContext is the context that a channel's stream address is sealed
// for: its source.
func streamContext(sourceID int64) string {
	return "stream-url/" + strconv.FormatInt(sourceID, 10)
}

// SealStreamURLs seals the stream addresses that older versions stored as
// they were (D46), and returns how many it sealed.
func (s *Store) SealStreamURLs(ctx context.Context) (int, error) {
	fail := func(err error) (int, error) { return 0, fmt.Errorf("store: seal stream addresses: %w", err) }
	if s.box.Load() == nil {
		return fail(ErrNoKey)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fail(err)
	}
	defer tx.Rollback()
	type plain struct {
		id, sourceID int64
		url          string
	}
	var todo []plain
	rows, err := tx.QueryContext(ctx, "SELECT id, source_id, stream_url FROM channels WHERE stream_url <> ''")
	if err != nil {
		return fail(err)
	}
	for rows.Next() {
		var p plain
		if err := rows.Scan(&p.id, &p.sourceID, &p.url); err != nil {
			rows.Close()
			return fail(err)
		}
		todo = append(todo, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fail(err)
	}
	update, err := tx.PrepareContext(ctx, "UPDATE channels SET stream_secret = ?, stream_url = '' WHERE id = ?")
	if err != nil {
		return fail(err)
	}
	defer update.Close()
	for _, p := range todo {
		sealed, err := s.seal(streamContext(p.sourceID), p.url)
		if err != nil {
			return fail(err)
		}
		if _, err := update.ExecContext(ctx, sealed, p.id); err != nil {
			return fail(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fail(err)
	}
	if len(todo) > 0 {
		// SQLite leaves what it replaces in the file's free space, and the
		// write-ahead log keeps older copies of pages: rewrite the file, and
		// empty the log, so that no address stays readable there.
		if _, err := s.db.ExecContext(ctx, "VACUUM"); err != nil {
			return fail(err)
		}
		var busy, frames, done int
		if err := s.db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &frames, &done); err != nil {
			return fail(err)
		}
		if busy != 0 {
			return fail(errors.New("the write-ahead log is in use and cannot be emptied"))
		}
	}
	return len(todo), nil
}

// StreamsSealedElsewhere counts, by source name, the channels whose stream
// address was sealed with another key than the store's: they cannot play
// until their source is read again.
func (s *Store) StreamsSealedElsewhere(ctx context.Context) (map[string]int, error) {
	box := s.box.Load()
	if box == nil {
		return nil, fmt.Errorf("store: stream addresses: %w", ErrNoKey)
	}
	// A sealed value starts with a version byte and its key's ID (secrets).
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.name, count(*) FROM channels c JOIN sources s ON s.id = c.source_id
		WHERE c.stream_secret IS NOT NULL AND substr(c.stream_secret, 2, ?) <> ?
		GROUP BY s.name`,
		len(box.KeyID()), box.KeyID())
	if err != nil {
		return nil, fmt.Errorf("store: stream addresses: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			return nil, fmt.Errorf("store: stream addresses: %w", err)
		}
		out[name] = n
	}
	return out, rows.Err()
}
