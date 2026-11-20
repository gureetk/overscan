// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// The guide (D51). Each source's guide is read as a whole into a new
// generation of its programmes, written a batch at a time, which then
// replaces the old one in a single step (schema 12).

// GuideState is what the last read of a source's guide found.
type GuideState struct {
	ReadAt     time.Time // zero if no read has ended
	ReadFrom   string    // a hash of what that read used: the address, and how it matched
	Error      string    // why it failed, "" if it worked
	Channels   int       // channels it found programmes for
	Programmes int
	Listed     bool // the source's playlist names a guide
}

// GuideAddressHash returns a hash of a guide's address, and of anything
// else that the reader adds to it, for GuideState.ReadFrom: a hash, since
// the address can hold credentials.
func GuideAddressHash(address string) string {
	sum := sha256.Sum256([]byte("overscan guide address\x00" + address))
	return hex.EncodeToString(sum[:8])
}

func guideContext(sourceID int64) string { return "source-guide/" + strconv.FormatInt(sourceID, 10) }

// SetListedGuide records the guide's address that a source's playlist names
// (url-tvg), sealed since it can hold credentials; "" records none.
func (s *Store) SetListedGuide(ctx context.Context, sourceID int64, address string) error {
	var sealed any
	if address != "" {
		b, err := s.seal(guideContext(sourceID), address)
		if err != nil {
			return fmt.Errorf("store: guide of source %d: %w", sourceID, err)
		}
		sealed = b
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO source_guides (source_id, listed_url) VALUES (?, ?)
		ON CONFLICT (source_id) DO UPDATE SET listed_url = excluded.listed_url`, sourceID, sealed)
	if err != nil {
		return fmt.Errorf("store: guide of source %d: %w", sourceID, err)
	}
	return nil
}

// ListedGuide returns the guide's address that a source's playlist names,
// "" if none.
func (s *Store) ListedGuide(ctx context.Context, sourceID int64) (string, error) {
	var sealed []byte
	err := s.db.QueryRowContext(ctx, "SELECT listed_url FROM source_guides WHERE source_id = ?", sourceID).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) || err == nil && sealed == nil {
		return "", nil
	}
	var address string
	if err == nil {
		address, err = s.open(guideContext(sourceID), sealed)
	}
	if err != nil {
		return "", fmt.Errorf("store: guide of source %d: %w", sourceID, err)
	}
	return address, nil
}

// SourceGuide returns what the last read of a source's guide found.
func (s *Store) SourceGuide(ctx context.Context, sourceID int64) (GuideState, error) {
	var g GuideState
	var readAt sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT read_at, read_from, read_error, channels, programmes, listed_url IS NOT NULL
		FROM source_guides WHERE source_id = ?`, sourceID).
		Scan(&readAt, &g.ReadFrom, &g.Error, &g.Channels, &g.Programmes, &g.Listed)
	if errors.Is(err, sql.ErrNoRows) {
		return GuideState{}, nil
	}
	if err == nil && readAt.Valid {
		g.ReadAt, err = parseTime(readAt.String)
	}
	if err != nil {
		return GuideState{}, fmt.Errorf("store: guide of source %d: %w", sourceID, err)
	}
	return g, nil
}

// GuideChannel is a channel that a source's guide can give programmes to.
type GuideChannel struct {
	ID          int64
	IdentityKey string // as CatalogChannel has it
	XMLTVID     string
	Name        string
	ArchiveDays int
}

// GuideChannels returns the channels of a source that the source still
// lists, which its guide can give programmes to.
func (s *Store) GuideChannels(ctx context.Context, sourceID int64) ([]GuideChannel, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, identity_key, xmltv_id, name, archive_days FROM channels
		WHERE source_id = ? AND missing_since IS NULL ORDER BY position`, sourceID)
	if err != nil {
		return nil, fmt.Errorf("store: guide channels of source %d: %w", sourceID, err)
	}
	defer rows.Close()
	var out []GuideChannel
	for rows.Next() {
		var c GuideChannel
		if err := rows.Scan(&c.ID, &c.IdentityKey, &c.XMLTVID, &c.Name, &c.ArchiveDays); err != nil {
			return nil, fmt.Errorf("store: guide channels of source %d: %w", sourceID, err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: guide channels of source %d: %w", sourceID, err)
	}
	return out, nil
}

// GuideProgramme is a programme to store.
type GuideProgramme struct {
	ChannelID   int64
	Start, Stop time.Time
	Title       string
	SubTitle    string
	Description string
	Category    string
}

// guideBatch is how many programmes one transaction of a guide read writes.
const guideBatch = 2000

// GuideWriter writes a new generation of a source's programmes. Make one
// with BeginGuide, Add programmes, then Commit it or Abort it.
type GuideWriter struct {
	s          *Store
	sourceID   int64
	generation int64
	old        int64 // the generation that devices see until Commit
	batch      []GuideProgramme
	channels   map[int64]bool
	count      int
	keep       time.Duration // how long past programmes are carried over, at least; 0 for none
}

// BeginGuide starts a new generation of a source's programmes, and clears
// away any that an interrupted read left.
func (s *Store) BeginGuide(ctx context.Context, sourceID int64) (*GuideWriter, error) {
	var current int64
	err := s.db.QueryRowContext(ctx, "SELECT generation FROM source_guides WHERE source_id = ?", sourceID).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = s.db.ExecContext(ctx, "INSERT INTO source_guides (source_id) VALUES (?)", sourceID)
	}
	if err != nil {
		return nil, fmt.Errorf("store: guide of source %d: %w", sourceID, err)
	}
	w := &GuideWriter{s: s, sourceID: sourceID, generation: current + 1, old: current, channels: map[int64]bool{}}
	if err := s.deleteProgrammes(ctx, sourceID, current, false); err != nil {
		return nil, err
	}
	return w, nil
}

// Add adds a programme to the new generation.
func (w *GuideWriter) Add(ctx context.Context, p GuideProgramme) error {
	w.batch = append(w.batch, p)
	if len(w.batch) < guideBatch {
		return nil
	}
	return w.flush(ctx)
}

func (w *GuideWriter) flush(ctx context.Context) error {
	if len(w.batch) == 0 {
		return nil
	}
	err := w.s.inTx(ctx, func(tx *sql.Tx) error {
		insert, err := tx.PrepareContext(ctx, `
			INSERT INTO programmes (source_id, generation, channel_id, start, stop, title, subtitle, description, category)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer insert.Close()
		for _, p := range w.batch {
			if _, err := insert.ExecContext(ctx, w.sourceID, w.generation, p.ChannelID, p.Start.Unix(), p.Stop.Unix(),
				p.Title, p.SubTitle, p.Description, p.Category); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("store: guide of source %d: %w", w.sourceID, err)
	}
	for _, p := range w.batch {
		w.channels[p.ChannelID] = true
	}
	w.count += len(w.batch)
	w.batch = w.batch[:0]
	return nil
}

// KeepPast makes Commit carry over, from the generation that devices see
// now, the programmes that have ended that the new one lacks (D83): those
// that ended within behind of the commit, or within their channel's days of
// archive where it keeps more, and before the new generation's first
// programme on their channel. A guide that lists only what is to come then
// still shows what was on, and its archive can be played.
func (w *GuideWriter) KeepPast(behind time.Duration) { w.keep = behind }

// Commit makes the new generation the one devices see, records the read
// as done at the given time from the address with the given hash, and
// deletes the old generation.
func (w *GuideWriter) Commit(ctx context.Context, at time.Time, from string) error {
	if err := w.flush(ctx); err != nil {
		return err
	}
	channels, count := len(w.channels), w.count
	if w.keep > 0 && w.old > 0 {
		var err error
		if channels, count, err = w.carryPast(ctx, at); err != nil {
			return fmt.Errorf("store: guide of source %d: %w", w.sourceID, err)
		}
	}
	_, err := w.s.db.ExecContext(ctx, `
		UPDATE source_guides SET generation = ?, read_at = ?, read_from = ?, read_error = '', channels = ?, programmes = ?
		WHERE source_id = ?`, w.generation, formatTime(at), from, channels, count, w.sourceID)
	if err != nil {
		return fmt.Errorf("store: guide of source %d: %w", w.sourceID, err)
	}
	return w.s.deleteProgrammes(ctx, w.sourceID, w.generation, false)
}

// carryPast copies the old generation's past programmes that the new one
// lacks into it, as KeepPast says, and returns how many channels and
// programmes the new generation then has.
func (w *GuideWriter) carryPast(ctx context.Context, now time.Time) (int, int, error) {
	_, err := w.s.db.ExecContext(ctx, `
		INSERT INTO programmes (source_id, generation, channel_id, start, stop, title, subtitle, description, category)
		SELECT o.source_id, ?, o.channel_id, o.start, o.stop, o.title, o.subtitle, o.description, o.category
		FROM programmes o JOIN channels c ON c.id = o.channel_id
		WHERE o.source_id = ? AND o.generation = ? AND o.stop <= ?
			AND o.stop > ? - max(?, c.archive_days * 86400)
			AND o.stop <= COALESCE((SELECT min(n.start) FROM programmes n
				WHERE n.channel_id = o.channel_id AND n.generation = ?), ?)`,
		w.generation, w.sourceID, w.old, now.Unix(), now.Unix(), int64(w.keep/time.Second), w.generation, now.Unix())
	if err != nil {
		return 0, 0, err
	}
	var channels, count int
	err = w.s.db.QueryRowContext(ctx, `
		SELECT count(DISTINCT channel_id), count(*) FROM programmes WHERE source_id = ? AND generation = ?`,
		w.sourceID, w.generation).Scan(&channels, &count)
	return channels, count, err
}

// Abort deletes the new generation, keeps the old one, and records why the
// read failed, at the given time, from the address with the given hash.
func (w *GuideWriter) Abort(ctx context.Context, at time.Time, from, why string) error {
	if err := w.s.deleteProgrammes(ctx, w.sourceID, w.generation, true); err != nil {
		return err
	}
	if _, err := w.s.db.ExecContext(ctx, `
		UPDATE source_guides SET read_at = ?, read_from = ?, read_error = ? WHERE source_id = ?`,
		formatTime(at), from, why, w.sourceID); err != nil {
		return fmt.Errorf("store: guide of source %d: %w", w.sourceID, err)
	}
	return nil
}

// Count returns how many programmes the writer has added, and for how many
// channels, so far.
func (w *GuideWriter) Count() (programmes, channels int) {
	more := map[int64]bool{}
	for _, p := range w.batch {
		if !w.channels[p.ChannelID] {
			more[p.ChannelID] = true
		}
	}
	return w.count + len(w.batch), len(w.channels) + len(more)
}

// deleteProgrammes deletes a source's programmes, a batch at a time: those
// of the given generation if only is true, and otherwise all but those.
func (s *Store) deleteProgrammes(ctx context.Context, sourceID, generation int64, only bool) error {
	cond := "generation <> ?"
	if only {
		cond = "generation = ?"
	}
	for {
		res, err := s.db.ExecContext(ctx, `
			DELETE FROM programmes WHERE id IN (
				SELECT id FROM programmes WHERE source_id = ? AND `+cond+` LIMIT ?)`,
			sourceID, generation, guideBatch)
		var n int
		if err == nil {
			n, err = rowsAffected(res)
		}
		if err != nil {
			return fmt.Errorf("store: guide of source %d: %w", sourceID, err)
		}
		if n < guideBatch {
			return nil
		}
	}
}

// Programme is a programme of a channel, as devices see it.
type Programme struct {
	ID          int64
	Start, Stop time.Time
	Title       string
	SubTitle    string
	Description string
	Category    string
}

// Programmes returns the programmes of a channel that end after from and
// start before to, by start time, from the generation that devices see.
func (s *Store) Programmes(ctx context.Context, channelID int64, from, to time.Time) ([]Programme, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.id, p.start, p.stop, p.title, p.subtitle, p.description, p.category
		FROM programmes p JOIN source_guides g ON g.source_id = p.source_id AND g.generation = p.generation
		WHERE p.channel_id = ? AND p.start >= ? AND p.start < ? AND p.stop > ?
		ORDER BY p.start`, channelID, from.Add(-longestProgramme).Unix(), to.Unix(), from.Unix())
	if err != nil {
		return nil, fmt.Errorf("store: programmes of channel %d: %w", channelID, err)
	}
	defer rows.Close()
	var out []Programme
	for rows.Next() {
		var p Programme
		var start, stop int64
		if err := rows.Scan(&p.ID, &start, &stop, &p.Title, &p.SubTitle, &p.Description, &p.Category); err != nil {
			return nil, fmt.Errorf("store: programmes of channel %d: %w", channelID, err)
		}
		p.Start, p.Stop = time.Unix(start, 0).UTC(), time.Unix(stop, 0).UTC()
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: programmes of channel %d: %w", channelID, err)
	}
	return out, nil
}

// longestProgramme bounds how long before a window a programme that overlaps
// it can start, so that queries can use the index on start.
const longestProgramme = 48 * time.Hour

// The channel whose programmes a lineup entry shows, and the seconds that
// their times move by: its own channel's, unmoved, unless a rule says
// otherwise (D108). Queries compare the programmes' own times with a window
// moved back by the shift, so that they keep the index on start.
const (
	guideChannelSQL = "COALESCE(e.rule_guide_id, c.id)"
	guideShiftSQL   = "COALESCE(e.rule_guide_shift, 0)"
)

// EntryProgrammes returns the programmes of a lineup entry's channel that end
// after from and start before to, by start time. It returns ErrNotFound for
// an entry that devices of the lineup do not see.
func (s *Store) EntryProgrammes(ctx context.Context, lineupID, entryID int64, from, to time.Time) ([]Programme, error) {
	var channelID, shift int64
	err := s.db.QueryRowContext(ctx, `SELECT `+guideChannelSQL+`, `+guideShiftSQL+lineupEntryJoins+`
		WHERE e.id = ? AND e.lineup_id = ? AND `+shownEntry, entryID, lineupID).Scan(&channelID, &shift)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: lineup entry %d: %w", entryID, err)
	}
	moved := time.Duration(shift) * time.Second
	ps, err := s.Programmes(ctx, channelID, from.Add(-moved), to.Add(-moved))
	for i := range ps {
		ps[i].Start, ps[i].Stop = ps[i].Start.Add(moved), ps[i].Stop.Add(moved)
	}
	return ps, err
}

// EachLineupProgramme calls fn with each programme of every channel that a
// lineup shows that ends after from and starts before to, by entry and
// then by start time, without holding them all in memory. It stops at fn's
// first error, which it returns.
func (s *Store) EachLineupProgramme(ctx context.Context, lineupID int64, from, to time.Time, fn func(entryID int64, p Programme) error) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.id, p.id, p.start + `+guideShiftSQL+`, p.stop + `+guideShiftSQL+`, p.title, p.subtitle, p.description,
			p.category`+lineupEntryJoins+`
		JOIN programmes p ON p.channel_id = `+guideChannelSQL+`
		JOIN source_guides sg ON sg.source_id = p.source_id AND sg.generation = p.generation
		WHERE e.lineup_id = ? AND `+shownEntry+` AND p.start >= ? - `+guideShiftSQL+` AND p.start < ? - `+guideShiftSQL+`
			AND p.stop > ? - `+guideShiftSQL+`
		ORDER BY e.id, p.start`,
		lineupID, from.Add(-longestProgramme).Unix(), to.Unix(), from.Unix())
	if err != nil {
		return fmt.Errorf("store: programmes of lineup %d: %w", lineupID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var entryID, start, stop int64
		var p Programme
		if err := rows.Scan(&entryID, &p.ID, &start, &stop, &p.Title, &p.SubTitle, &p.Description, &p.Category); err != nil {
			return fmt.Errorf("store: programmes of lineup %d: %w", lineupID, err)
		}
		p.Start, p.Stop = time.Unix(start, 0).UTC(), time.Unix(stop, 0).UTC()
		if err := fn(entryID, p); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: programmes of lineup %d: %w", lineupID, err)
	}
	return nil
}

// LineupProgrammes returns, by lineup entry, the programmes of every channel
// that a lineup shows, or of the entries given, that end after from and
// start before to, by start time. Entries without any are left out.
func (s *Store) LineupProgrammes(ctx context.Context, lineupID int64, from, to time.Time, entryIDs ...int64) (map[int64][]Programme, error) {
	args := []any{lineupID, from.Add(-longestProgramme).Unix(), to.Unix(), from.Unix()}
	only := ""
	if len(entryIDs) > 0 {
		only = " AND e.id IN (?" + strings.Repeat(", ?", len(entryIDs)-1) + ")"
		for _, id := range entryIDs {
			args = append(args, id)
		}
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.id, p.id, p.start + `+guideShiftSQL+`, p.stop + `+guideShiftSQL+`, p.title, p.subtitle, p.description,
			p.category`+lineupEntryJoins+`
		JOIN programmes p ON p.channel_id = `+guideChannelSQL+`
		JOIN source_guides sg ON sg.source_id = p.source_id AND sg.generation = p.generation
		WHERE e.lineup_id = ? AND `+shownEntry+` AND p.start >= ? - `+guideShiftSQL+` AND p.start < ? - `+guideShiftSQL+`
			AND p.stop > ? - `+guideShiftSQL+only+`
		ORDER BY e.id, p.start`,
		args...)
	if err != nil {
		return nil, fmt.Errorf("store: programmes of lineup %d: %w", lineupID, err)
	}
	defer rows.Close()
	out := map[int64][]Programme{}
	for rows.Next() {
		var entryID, start, stop int64
		var p Programme
		if err := rows.Scan(&entryID, &p.ID, &start, &stop, &p.Title, &p.SubTitle, &p.Description, &p.Category); err != nil {
			return nil, fmt.Errorf("store: programmes of lineup %d: %w", lineupID, err)
		}
		p.Start, p.Stop = time.Unix(start, 0).UTC(), time.Unix(stop, 0).UTC()
		out[entryID] = append(out[entryID], p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: programmes of lineup %d: %w", lineupID, err)
	}
	return out, nil
}
