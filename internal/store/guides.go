// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ncruces/go-sqlite3"

	"overscan.gureet.ca/internal/source"
)

// Guides of the household's own (D139). The administrator adds XMLTV
// guides, at an http or https address or in a file on the server, which
// fill the channels that their own sources give no programme. A guide's
// address is sealed, as a source's is (D46), since it can hold
// credentials. A read records the channels that the guide lists, then
// writes a new generation of the programmes of those of them that a
// channel uses, a batch at a time, which then replaces the old one in a
// single step, as a source's guide does (D51). The guide package works out
// which guide's channel each channel uses; the store keeps it
// (channel_guide_matches) and shows those programmes wherever the guide is
// read.

var (
	// ErrGuideExists is returned for a guide named as another is.
	ErrGuideExists = errors.New("store: a guide has that name")
	// ErrTooManyGuides is returned when a server has MaxGuides already.
	ErrTooManyGuides = errors.New("store: the server has as many guides as it can keep")
)

// MaxGuides is how many guides of its own a server keeps.
const MaxGuides = 20

// GuideSettings are what the administrator sets of a guide.
type GuideSettings struct {
	// Name identifies the guide in the admin and the log, as a source's
	// name does.
	Name string
	// Address is an http or https address, or the absolute path of a file
	// on the server. It can hold credentials, so it is never shown, logged
	// or put in an error; the admin shows source.Host(Address). When a
	// guide is changed, "" keeps the saved one.
	Address string
	// UserAgent is sent when the guide is fetched; "" for Overscan's own.
	UserAgent string
	// PublicOnly keeps the guide's requests off the server's own networks
	// (D121).
	PublicOnly bool
}

// Check reports the first setting that breaks a rule, as a *source.Error.
// For a change to a saved guide (saved true), an empty address keeps the
// one saved.
func (g GuideSettings) Check(saved bool) error {
	bad := func(field, text string) error { return &source.Error{Field: field, Text: text} }
	switch {
	case !source.ValidName(g.Name):
		return bad("name", "A guide's name is 1 to 32 lower-case letters, digits, hyphens or underscores, starting with a letter or digit.")
	case !(saved && g.Address == "") && !source.ValidAddress(g.Address):
		return bad("address", "The guide's address must be an http or https address, or the absolute path of a file on this server.")
	case !source.ValidUserAgent(g.UserAgent):
		return bad("user_agent", "A user agent is at most 200 bytes of text, without control characters.")
	}
	return nil
}

// IsFile reports whether a guide's address is a file on the server.
func (g GuideSettings) IsFile() bool { return strings.HasPrefix(g.Address, "/") }

// Guide is a guide of the household's own, without its address.
type Guide struct {
	ID         int64
	Name       string
	Host       string // what the admin shows of its address: its host, or "file" and a file's name
	UserAgent  string // "" for Overscan's own
	PublicOnly bool
	Enabled    bool
	// Position orders the guides, from 1: where two guides' channels could
	// fill a channel, the first's does.
	Position int
	AddedAt  time.Time
	Address  AddressState
	// What the last read found: when it ended (zero if none has), a hash
	// of what it read from, why it failed ("" if it worked), the channels
	// that the guide listed, and of those, the ones it kept programmes
	// for, and how many programmes.
	ReadAt     time.Time
	ReadFrom   string
	ReadError  string
	Listed     int
	Channels   int
	Programmes int
	// Matched counts the channels that the guide gives programmes to now,
	// ByName those of them that their names matched rather than their
	// guide IDs, and ByHand those whose guide was chosen by hand (D140).
	// Waiting counts those of them whose programmes the guide's next read
	// brings, since no channel used them at its last read.
	Matched, ByName, ByHand, Waiting int
}

func addedGuideContext(id int64) string { return "guide/" + strconv.FormatInt(id, 10) }

const guideSelect = `
	SELECT g.id, g.name, g.host, g.user_agent, g.public_only, g.enabled, g.position, g.added_at, g.address,
		g.read_at, g.read_from, g.read_error, g.listed, g.channels, g.programmes,
		(SELECT count(*) FROM channel_guide_matches m JOIN guide_channels gc ON gc.id = m.guide_channel_id
			WHERE gc.guide_id = g.id),
		(SELECT count(*) FROM channel_guide_matches m JOIN guide_channels gc ON gc.id = m.guide_channel_id
			WHERE gc.guide_id = g.id AND m.by_name = 1),
		(SELECT count(*) FROM channel_guide_matches m JOIN guide_channels gc ON gc.id = m.guide_channel_id
			WHERE gc.guide_id = g.id AND m.by_hand = 1),
		(SELECT count(*) FROM channel_guide_matches m JOIN guide_channels gc ON gc.id = m.guide_channel_id
			WHERE gc.guide_id = g.id AND gc.kept = 0)
	FROM guides g`

func (s *Store) scanGuide(row rowScanner) (Guide, error) {
	var g Guide
	var added string
	var read sql.NullString
	var sealed []byte
	if err := row.Scan(&g.ID, &g.Name, &g.Host, &g.UserAgent, &g.PublicOnly, &g.Enabled, &g.Position, &added, &sealed,
		&read, &g.ReadFrom, &g.ReadError, &g.Listed, &g.Channels, &g.Programmes, &g.Matched, &g.ByName, &g.ByHand, &g.Waiting); err != nil {
		return Guide{}, err
	}
	var err error
	if g.AddedAt, err = parseTime(added); err != nil {
		return Guide{}, err
	}
	if read.Valid {
		if g.ReadAt, err = parseTime(read.String); err != nil {
			return Guide{}, err
		}
	}
	g.Address = AddressSaved
	if _, err := s.open(addedGuideContext(g.ID), sealed); err != nil {
		g.Address = AddressLocked
	}
	return g, nil
}

// Guides returns the guides of the household's own, in their order.
func (s *Store) Guides(ctx context.Context) ([]Guide, error) {
	rows, err := s.db.QueryContext(ctx, guideSelect+" ORDER BY g.position, g.id")
	if err != nil {
		return nil, fmt.Errorf("store: guides: %w", err)
	}
	defer rows.Close()
	var out []Guide
	for rows.Next() {
		g, err := s.scanGuide(rows)
		if err != nil {
			return nil, fmt.Errorf("store: guides: %w", err)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: guides: %w", err)
	}
	return out, nil
}

// Guide returns a guide of the household's own, or ErrNotFound.
func (s *Store) Guide(ctx context.Context, id int64) (Guide, error) {
	g, err := s.scanGuide(s.db.QueryRowContext(ctx, guideSelect+" WHERE g.id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Guide{}, ErrNotFound
	}
	if err != nil {
		return Guide{}, fmt.Errorf("store: guide %d: %w", id, err)
	}
	return g, nil
}

// GuideSettings returns a guide's settings, with its address. It returns
// ErrNotFound for no such guide, and ErrUnreadable for an address that the
// store's key cannot open.
func (s *Store) GuideSettings(ctx context.Context, id int64) (GuideSettings, error) {
	var g GuideSettings
	var sealed []byte
	err := s.db.QueryRowContext(ctx, "SELECT name, address, user_agent, public_only FROM guides WHERE id = ?", id).
		Scan(&g.Name, &sealed, &g.UserAgent, &g.PublicOnly)
	if errors.Is(err, sql.ErrNoRows) {
		return GuideSettings{}, ErrNotFound
	}
	if err == nil {
		g.Address, err = s.open(addedGuideContext(id), sealed)
	}
	if err != nil {
		return GuideSettings{}, fmt.Errorf("store: guide %d: %w", id, err)
	}
	return g, nil
}

// AddGuide adds a guide after the others, turned on, and returns its ID.
// A name that another guide has gives ErrGuideExists, and a server that
// keeps MaxGuides already gives ErrTooManyGuides.
func (s *Store) AddGuide(ctx context.Context, g GuideSettings, at time.Time) (int64, error) {
	if err := g.Check(false); err != nil {
		return 0, err
	}
	var id int64
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM guides").Scan(&n); err != nil {
			return err
		}
		if n >= MaxGuides {
			return ErrTooManyGuides
		}
		err := tx.QueryRowContext(ctx, `
			INSERT INTO guides (name, address, host, user_agent, public_only, position, added_at)
			VALUES (?, x'', ?, ?, ?, (SELECT COALESCE(max(position), 0) + 1 FROM guides), ?) RETURNING id`,
			g.Name, source.Host(g.Address), g.UserAgent, g.PublicOnly, formatTime(at)).Scan(&id)
		if errors.Is(err, sqlite3.CONSTRAINT_UNIQUE) {
			return ErrGuideExists
		}
		if err != nil {
			return err
		}
		sealed, err := s.seal(addedGuideContext(id), g.Address)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE guides SET address = ? WHERE id = ?", sealed, id)
		return err
	})
	if errors.Is(err, ErrGuideExists) || errors.Is(err, ErrTooManyGuides) {
		return 0, err
	}
	if err != nil {
		return 0, fmt.Errorf("store: add guide: %w", err)
	}
	return id, nil
}

// UpdateGuide saves a guide's settings. An empty address keeps the saved
// one. A new address, user agent or choice of public addresses makes the
// guide due to be read at once.
func (s *Store) UpdateGuide(ctx context.Context, id int64, g GuideSettings) error {
	if err := g.Check(true); err != nil {
		return err
	}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var ua string
		var public bool
		err := tx.QueryRowContext(ctx, "SELECT user_agent, public_only FROM guides WHERE id = ?", id).Scan(&ua, &public)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, "UPDATE guides SET name = ?, user_agent = ?, public_only = ? WHERE id = ?",
			g.Name, g.UserAgent, g.PublicOnly, id)
		if errors.Is(err, sqlite3.CONSTRAINT_UNIQUE) {
			return ErrGuideExists
		}
		if err == nil {
			err = mustAffect(res)
		}
		if err != nil {
			return err
		}
		if g.Address != "" {
			sealed, err := s.seal(addedGuideContext(id), g.Address)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "UPDATE guides SET address = ?, host = ?, read_from = '' WHERE id = ?",
				sealed, source.Host(g.Address), id); err != nil {
				return err
			}
		}
		if g.UserAgent != ua || g.PublicOnly != public {
			_, err = tx.ExecContext(ctx, "UPDATE guides SET read_from = '' WHERE id = ?", id)
		}
		return err
	})
	if errors.Is(err, ErrGuideExists) || errors.Is(err, ErrNotFound) {
		return err
	}
	if err != nil {
		return fmt.Errorf("store: guide %d: %w", id, err)
	}
	return nil
}

// SetGuideEnabled turns a guide on or off. A guide turned off fills no
// channel from then on: its matches go at once, and the channels that it
// filled take another guide's when they are matched again.
func (s *Store) SetGuideEnabled(ctx context.Context, id int64, on bool) error {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "UPDATE guides SET enabled = ? WHERE id = ?", on, id)
		if err == nil {
			err = mustAffect(res)
		}
		if err == nil && !on {
			_, err = tx.ExecContext(ctx, `
				DELETE FROM channel_guide_matches WHERE guide_channel_id IN (SELECT id FROM guide_channels WHERE guide_id = ?)`, id)
		}
		return err
	})
	if errors.Is(err, ErrNotFound) {
		return err
	}
	if err != nil {
		return fmt.Errorf("store: guide %d: %w", id, err)
	}
	return nil
}

// MoveGuide puts a guide at a position, from 1, moving the others along.
// A position past the last puts it last.
func (s *Store) MoveGuide(ctx context.Context, id int64, position int) error {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		ids, err := guideOrder(ctx, tx)
		if err != nil {
			return err
		}
		at := -1
		for i, g := range ids {
			if g == id {
				at = i
			}
		}
		if at < 0 {
			return ErrNotFound
		}
		ids = append(ids[:at], ids[at+1:]...)
		to := min(max(position, 1), len(ids)+1) - 1
		ids = append(ids[:to], append([]int64{id}, ids[to:]...)...)
		return setGuideOrder(ctx, tx, ids)
	})
	if errors.Is(err, ErrNotFound) {
		return err
	}
	if err != nil {
		return fmt.Errorf("store: guide %d: %w", id, err)
	}
	return nil
}

// DeleteGuide removes a guide with its channels, programmes and matches.
// The guides after it move up.
func (s *Store) DeleteGuide(ctx context.Context, id int64) error {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM guides WHERE id = ?", id)
		if err == nil {
			err = mustAffect(res)
		}
		if err != nil {
			return err
		}
		ids, err := guideOrder(ctx, tx)
		if err != nil {
			return err
		}
		return setGuideOrder(ctx, tx, ids)
	})
	if errors.Is(err, ErrNotFound) {
		return err
	}
	if err != nil {
		return fmt.Errorf("store: delete guide %d: %w", id, err)
	}
	return nil
}

func guideOrder(ctx context.Context, tx *sql.Tx) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, "SELECT id FROM guides ORDER BY position, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func setGuideOrder(ctx context.Context, tx *sql.Tx, ids []int64) error {
	for i, id := range ids {
		if _, err := tx.ExecContext(ctx, "UPDATE guides SET position = ? WHERE id = ? AND position <> ?", i+1, id, i+1); err != nil {
			return err
		}
	}
	return nil
}

// GuideListing is a channel that a read of a guide lists.
type GuideListing struct {
	XMLTVID string   // the guide's ID for it
	Names   []string // its display names, the first first
	Icon    string   // the address of its icon, "" if none
}

// ListedChannel is a channel that a guide of the household's own lists,
// for matching (D139).
type ListedChannel struct {
	ID      int64
	GuideID int64
	XMLTVID string
	Names   []string
	// Kept says whether the guide's programmes hold it: whether a channel
	// used it when the guide was last read.
	Kept bool
}

// ListedChannels returns the channels that the enabled guides listed at
// their last reads, by guide in their order, each guide's in the order
// that it first listed them.
func (s *Store) ListedChannels(ctx context.Context) ([]ListedChannel, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT gc.id, gc.guide_id, gc.xmltv_id, gc.names, gc.kept FROM guide_channels gc JOIN guides g ON g.id = gc.guide_id
		WHERE g.enabled = 1 AND gc.missing_since IS NULL ORDER BY g.position, g.id, gc.id`)
	if err != nil {
		return nil, fmt.Errorf("store: guides' channels: %w", err)
	}
	defer rows.Close()
	var out []ListedChannel
	for rows.Next() {
		var c ListedChannel
		var names string
		if err := rows.Scan(&c.ID, &c.GuideID, &c.XMLTVID, &names, &c.Kept); err != nil {
			return nil, fmt.Errorf("store: guides' channels: %w", err)
		}
		c.Names = strings.Split(names, "\n")
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: guides' channels: %w", err)
	}
	return out, nil
}

// MatchCandidate is a channel that its source still lists and whose own
// source's guide gives it no programme: one that a guide of the
// household's own can fill (D139).
type MatchCandidate struct {
	ID          int64
	XMLTVID     string
	Name        string // as its source gives it
	Category    string // its category's name at its source, "" if none
	ArchiveDays int
}

// MatchCandidates returns the channels that a guide of the household's own
// can fill: those that their sources still list, without a programme in
// the guide that devices see of their own source, and whose guide nobody
// chose by hand (D140).
func (s *Store) MatchCandidates(ctx context.Context) ([]MatchCandidate, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, c.xmltv_id, c.name, COALESCE(k.name, ''), c.archive_days
		FROM channels c LEFT JOIN categories k ON k.id = c.category_id
		WHERE c.missing_since IS NULL AND NOT EXISTS (
			SELECT 1 FROM programmes p JOIN source_guides sg ON sg.source_id = p.source_id AND sg.generation = p.generation
			WHERE p.channel_id = c.id)
			AND NOT EXISTS (SELECT 1 FROM channel_guide_choices hc WHERE hc.channel_id = c.id)
		ORDER BY c.source_id, c.position`)
	if err != nil {
		return nil, fmt.Errorf("store: channels without a guide: %w", err)
	}
	defer rows.Close()
	var out []MatchCandidate
	for rows.Next() {
		var c MatchCandidate
		if err := rows.Scan(&c.ID, &c.XMLTVID, &c.Name, &c.Category, &c.ArchiveDays); err != nil {
			return nil, fmt.Errorf("store: channels without a guide: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: channels without a guide: %w", err)
	}
	return out, nil
}

// GuideMatch says which guide's channel gives a channel its programmes.
type GuideMatch struct {
	ChannelID      int64
	GuideChannelID int64
	ByName         bool // its name matched, rather than its guide ID
	ByHand         bool // it was chosen by hand (D140)
}

// GuideMatches returns every channel's match with a guide of the
// household's own.
func (s *Store) GuideMatches(ctx context.Context) ([]GuideMatch, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT channel_id, guide_channel_id, by_name, by_hand FROM channel_guide_matches ORDER BY channel_id")
	if err != nil {
		return nil, fmt.Errorf("store: guide matches: %w", err)
	}
	defer rows.Close()
	var out []GuideMatch
	for rows.Next() {
		var m GuideMatch
		if err := rows.Scan(&m.ChannelID, &m.GuideChannelID, &m.ByName, &m.ByHand); err != nil {
			return nil, fmt.Errorf("store: guide matches: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: guide matches: %w", err)
	}
	return out, nil
}

// SetGuideMatches makes matches every channel's match with a guide of the
// household's own, changing only what differs, and reports whether
// anything did.
func (s *Store) SetGuideMatches(ctx context.Context, matches []GuideMatch) (bool, error) {
	var changed bool
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		changed, err = setGuideMatches(ctx, tx, matches)
		return err
	})
	if err != nil {
		return false, fmt.Errorf("store: guide matches: %w", err)
	}
	return changed, nil
}

func setGuideMatches(ctx context.Context, tx *sql.Tx, matches []GuideMatch) (bool, error) {
	type match struct {
		guideChannel   int64
		byName, byHand bool
	}
	now := map[int64]match{}
	rows, err := tx.QueryContext(ctx, "SELECT channel_id, guide_channel_id, by_name, by_hand FROM channel_guide_matches")
	if err != nil {
		return false, err
	}
	for rows.Next() {
		var id int64
		var m match
		if err := rows.Scan(&id, &m.guideChannel, &m.byName, &m.byHand); err != nil {
			rows.Close()
			return false, err
		}
		now[id] = m
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}
	changed := false
	want := make(map[int64]bool, len(matches))
	for _, m := range matches {
		want[m.ChannelID] = true
		if was, ok := now[m.ChannelID]; ok && was == (match{m.GuideChannelID, m.ByName, m.ByHand}) {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO channel_guide_matches (channel_id, guide_channel_id, by_name, by_hand) VALUES (?, ?, ?, ?)
			ON CONFLICT (channel_id) DO UPDATE SET guide_channel_id = excluded.guide_channel_id, by_name = excluded.by_name,
				by_hand = excluded.by_hand`,
			m.ChannelID, m.GuideChannelID, m.ByName, m.ByHand); err != nil {
			return false, err
		}
		changed = true
	}
	for id := range now {
		if !want[id] {
			if _, err := tx.ExecContext(ctx, "DELETE FROM channel_guide_matches WHERE channel_id = ?", id); err != nil {
				return false, err
			}
			changed = true
		}
	}
	return changed, nil
}

// GuidesWaiting returns the enabled guides that a channel uses a channel
// of whose programmes they do not hold, since no channel used it at their
// last read: their next read brings them.
func (s *Store) GuidesWaiting(ctx context.Context) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT gc.guide_id FROM channel_guide_matches m JOIN guide_channels gc ON gc.id = m.guide_channel_id
		JOIN guides g ON g.id = gc.guide_id WHERE gc.kept = 0 AND g.enabled = 1 ORDER BY gc.guide_id`)
	if err != nil {
		return nil, fmt.Errorf("store: guides waiting: %w", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: guides waiting: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: guides waiting: %w", err)
	}
	return out, nil
}

// ChannelsWithoutGuide counts the channels of enabled sources that their
// sources still list that have no programme from their own source's
// guide, nor a match with a guide of the household's own, nor a guide
// chosen by hand, even none (D140).
func (s *Store) ChannelsWithoutGuide(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT count(*) FROM channels c JOIN sources s ON s.id = c.source_id
		WHERE c.missing_since IS NULL AND s.enabled = 1
			AND NOT EXISTS (SELECT 1 FROM channel_guide_matches m WHERE m.channel_id = c.id)
			AND NOT EXISTS (SELECT 1 FROM channel_guide_choices hc WHERE hc.channel_id = c.id)
			AND NOT EXISTS (
				SELECT 1 FROM programmes p JOIN source_guides sg ON sg.source_id = p.source_id AND sg.generation = p.generation
				WHERE p.channel_id = c.id)`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: channels without a guide: %w", err)
	}
	return n, nil
}

// GuideReader writes what a read of a guide of the household's own finds:
// the channels that it lists, then a new generation of programmes. Make
// one with BeginGuideRead, ListChannels, Add programmes, then Commit it or
// Abort it.
type GuideReader struct {
	s          *Store
	guideID    int64
	generation int64
	old        int64 // the generation that devices see until Commit
	batch      []GuideProgramme
	channels   map[int64]bool
	count      int
	keep       time.Duration
}

// BeginGuideRead starts a new generation of a guide's programmes, and
// clears away any that an interrupted read left. It returns ErrNotFound
// for no such guide.
func (s *Store) BeginGuideRead(ctx context.Context, guideID int64) (*GuideReader, error) {
	var current int64
	err := s.db.QueryRowContext(ctx, "SELECT generation FROM guides WHERE id = ?", guideID).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: guide %d: %w", guideID, err)
	}
	w := &GuideReader{s: s, guideID: guideID, generation: current + 1, old: current, channels: map[int64]bool{}}
	if err := s.deleteGuideProgrammes(ctx, guideID, current, false); err != nil {
		return nil, err
	}
	return w, nil
}

// ListChannels records the channels that the read lists, at the given
// time: those listed for the first time are added, those listed again take
// their names and icon, and those no longer listed are marked missing,
// and forgotten once they have been missing for MissingGrace. It returns,
// by the guide's ID for it, the ID of each channel listed.
func (w *GuideReader) ListChannels(ctx context.Context, listed []GuideListing, at time.Time) (map[string]int64, error) {
	ids := make(map[string]int64, len(listed))
	stamp := formatTime(at)
	fail := func(err error) (map[string]int64, error) {
		return nil, fmt.Errorf("store: channels of guide %d: %w", w.guideID, err)
	}
	for from := 0; from < len(listed); from += guideBatch {
		part := listed[from:min(from+guideBatch, len(listed))]
		err := w.s.inTx(ctx, func(tx *sql.Tx) error {
			upsert, err := tx.PrepareContext(ctx, `
				INSERT INTO guide_channels (guide_id, xmltv_id, names, icon, listed_at) VALUES (?, ?, ?, ?, ?)
				ON CONFLICT (guide_id, xmltv_id) DO UPDATE SET names = excluded.names, icon = excluded.icon,
					listed_at = excluded.listed_at, missing_since = NULL
				RETURNING id`)
			if err != nil {
				return err
			}
			defer upsert.Close()
			for _, c := range part {
				var id int64
				if err := upsert.QueryRowContext(ctx, w.guideID, c.XMLTVID, strings.Join(c.Names, "\n"), c.Icon, stamp).Scan(&id); err != nil {
					return err
				}
				ids[c.XMLTVID] = id
			}
			return nil
		})
		if err != nil {
			return fail(err)
		}
	}
	if _, err := w.s.db.ExecContext(ctx, `
		UPDATE guide_channels SET missing_since = ? WHERE guide_id = ? AND listed_at <> ? AND missing_since IS NULL`,
		stamp, w.guideID, stamp); err != nil {
		return fail(err)
	}
	if _, err := w.s.db.ExecContext(ctx, `
		DELETE FROM guide_channels WHERE guide_id = ? AND missing_since IS NOT NULL AND missing_since < ?`,
		w.guideID, formatTime(at.Add(-MissingGrace))); err != nil {
		return fail(err)
	}
	return ids, nil
}

// Add adds a programme of one of the guide's channels, by the ID that
// ListChannels gave it, to the new generation.
func (w *GuideReader) Add(ctx context.Context, p GuideProgramme) error {
	w.batch = append(w.batch, p)
	if len(w.batch) < guideBatch {
		return nil
	}
	return w.flush(ctx)
}

func (w *GuideReader) flush(ctx context.Context) error {
	if len(w.batch) == 0 {
		return nil
	}
	err := w.s.inTx(ctx, func(tx *sql.Tx) error {
		insert, err := tx.PrepareContext(ctx, `
			INSERT INTO guide_programmes (guide_id, generation, channel_id, start, stop, title, subtitle, description, category)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer insert.Close()
		for _, p := range w.batch {
			if _, err := insert.ExecContext(ctx, w.guideID, w.generation, p.ChannelID, p.Start.Unix(), p.Stop.Unix(),
				p.Title, p.SubTitle, p.Description, p.Category); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("store: guide %d: %w", w.guideID, err)
	}
	for _, p := range w.batch {
		w.channels[p.ChannelID] = true
	}
	w.count += len(w.batch)
	w.batch = w.batch[:0]
	return nil
}

// Count returns how many programmes the read has added, and for how many
// channels, so far.
func (w *GuideReader) Count() (programmes, channels int) {
	more := map[int64]bool{}
	for _, p := range w.batch {
		if !w.channels[p.ChannelID] {
			more[p.ChannelID] = true
		}
	}
	return w.count + len(w.batch), len(w.channels) + len(more)
}

// KeepPast makes Commit carry over, from the generation that devices see
// now, the programmes that have ended that the new one lacks (D83), of the
// channels that the read kept: those that ended within behind of the
// commit, or within the days of archive of the channels that use them,
// where one keeps more, and before the new generation's first programme
// on their channel.
func (w *GuideReader) KeepPast(behind time.Duration) { w.keep = behind }

// GuideRead is what a read of a guide found, for Commit.
type GuideRead struct {
	Listed int // the channels that the guide listed
	// Kept are the guide's channels whose programmes the read kept: those
	// that a channel uses.
	Kept []int64
	// Matches are every channel's matches with every guide, worked out
	// with the channels that this read listed.
	Matches []GuideMatch
}

// Commit makes the channels' matches r's, makes the new generation the one
// devices see, records the read as done at the given time from the address
// with the given hash, and deletes the old generation.
func (w *GuideReader) Commit(ctx context.Context, at time.Time, from string, r GuideRead) error {
	fail := func(err error) error { return fmt.Errorf("store: guide %d: %w", w.guideID, err) }
	if err := w.flush(ctx); err != nil {
		return err
	}
	kept, err := json.Marshal(append([]int64{}, r.Kept...))
	if err != nil {
		return fail(err)
	}
	if _, err := w.s.SetGuideMatches(ctx, r.Matches); err != nil {
		return err
	}
	channels, count := len(w.channels), w.count
	if w.keep > 0 && w.old > 0 {
		if channels, count, err = w.carryPast(ctx, at, string(kept)); err != nil {
			return fail(err)
		}
	}
	err = w.s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			UPDATE guide_channels SET kept = id IN (SELECT value FROM json_each(?)) WHERE guide_id = ?`,
			string(kept), w.guideID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			UPDATE guides SET generation = ?, read_at = ?, read_from = ?, read_error = '', listed = ?, channels = ?,
				programmes = ?
			WHERE id = ?`, w.generation, formatTime(at), from, r.Listed, channels, count, w.guideID)
		return err
	})
	if err != nil {
		return fail(err)
	}
	return w.s.deleteGuideProgrammes(ctx, w.guideID, w.generation, false)
}

// carryPast copies the old generation's past programmes that the new one
// lacks into it, as KeepPast says, and returns how many channels and
// programmes the new generation then has.
func (w *GuideReader) carryPast(ctx context.Context, now time.Time, kept string) (int, int, error) {
	_, err := w.s.db.ExecContext(ctx, `
		INSERT INTO guide_programmes (guide_id, generation, channel_id, start, stop, title, subtitle, description, category)
		SELECT o.guide_id, ?, o.channel_id, o.start, o.stop, o.title, o.subtitle, o.description, o.category
		FROM guide_programmes o
		WHERE o.guide_id = ? AND o.generation = ? AND o.stop <= ?
			AND o.channel_id IN (SELECT value FROM json_each(?))
			AND o.stop > ? - max(?, COALESCE((SELECT max(c.archive_days) FROM channel_guide_matches m
				JOIN channels c ON c.id = m.channel_id WHERE m.guide_channel_id = o.channel_id), 0) * 86400)
			AND o.stop <= COALESCE((SELECT min(n.start) FROM guide_programmes n
				WHERE n.channel_id = o.channel_id AND n.generation = ?), ?)`,
		w.generation, w.guideID, w.old, now.Unix(), kept, now.Unix(), int64(w.keep/time.Second), w.generation, now.Unix())
	if err != nil {
		return 0, 0, err
	}
	var channels, count int
	err = w.s.db.QueryRowContext(ctx, `
		SELECT count(DISTINCT channel_id), count(*) FROM guide_programmes WHERE guide_id = ? AND generation = ?`,
		w.guideID, w.generation).Scan(&channels, &count)
	return channels, count, err
}

// Abort deletes the new generation, keeps the old one, and records why the
// read failed, at the given time, from the address with the given hash.
func (w *GuideReader) Abort(ctx context.Context, at time.Time, from, why string) error {
	if err := w.s.deleteGuideProgrammes(ctx, w.guideID, w.generation, true); err != nil {
		return err
	}
	if _, err := w.s.db.ExecContext(ctx, "UPDATE guides SET read_at = ?, read_from = ?, read_error = ? WHERE id = ?",
		formatTime(at), from, why, w.guideID); err != nil {
		return fmt.Errorf("store: guide %d: %w", w.guideID, err)
	}
	return nil
}

// deleteGuideProgrammes deletes a guide's programmes, a batch at a time:
// those of the given generation if only is true, and otherwise all but
// those.
func (s *Store) deleteGuideProgrammes(ctx context.Context, guideID, generation int64, only bool) error {
	cond := "generation <> ?"
	if only {
		cond = "generation = ?"
	}
	for {
		res, err := s.db.ExecContext(ctx, `
			DELETE FROM guide_programmes WHERE id IN (
				SELECT id FROM guide_programmes WHERE guide_id = ? AND `+cond+` LIMIT ?)`,
			guideID, generation, guideBatch)
		var n int
		if err == nil {
			n, err = rowsAffected(res)
		}
		if err != nil {
			return fmt.Errorf("store: guide %d: %w", guideID, err)
		}
		if n < guideBatch {
			return nil
		}
	}
}

// guideProgrammes returns the programmes of a guide's channel that end
// after from and start before to, by start time, from the generation that
// devices see. Their IDs are below zero, so that they never are those of a
// source's programmes.
func (s *Store) guideProgrammes(ctx context.Context, guideChannelID int64, from, to time.Time) ([]Programme, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT -p.id, p.start, p.stop, p.title, p.subtitle, p.description, p.category
		FROM guide_channels gc JOIN guides g ON g.id = gc.guide_id
		JOIN guide_programmes p ON p.channel_id = gc.id AND p.generation = g.generation
		WHERE gc.id = ? AND p.start >= ? AND p.start < ? AND p.stop > ?
		ORDER BY p.start`, guideChannelID, from.Add(-longestProgramme).Unix(), to.Unix(), from.Unix())
	if err != nil {
		return nil, fmt.Errorf("store: programmes of guide channel %d: %w", guideChannelID, err)
	}
	defer rows.Close()
	var out []Programme
	for rows.Next() {
		var p Programme
		var start, stop int64
		if err := rows.Scan(&p.ID, &start, &stop, &p.Title, &p.SubTitle, &p.Description, &p.Category); err != nil {
			return nil, fmt.Errorf("store: programmes of guide channel %d: %w", guideChannelID, err)
		}
		p.Start, p.Stop = time.Unix(start, 0).UTC(), time.Unix(stop, 0).UTC()
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: programmes of guide channel %d: %w", guideChannelID, err)
	}
	return out, nil
}
