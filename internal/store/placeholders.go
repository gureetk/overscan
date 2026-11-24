// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Placeholder programmes (D142): where the setting asks for them, the
// guides that devices and players read give a channel a programme named
// after it in each block of time, of the length that the setting says,
// in which it has no programme at all. Blocks start at the server's
// midnight, and only those that have not ended, and start within
// placeholdersAhead, have them, so that a placeholder is never in the
// past and never plays from an archive. Placeholders are made as the
// guide is read, and never stored: recordings, catch-up and the search
// of programmes see only real ones.

// settingPlaceholders is the length of placeholder programmes' blocks, in
// minutes, or "0" for none; none until it is set.
const settingPlaceholders = "guide_placeholders"

// PlaceholderBlocks are the lengths that placeholder programmes' blocks
// can have. Each divides a day.
var PlaceholderBlocks = []time.Duration{30 * time.Minute, time.Hour, 2 * time.Hour, 3 * time.Hour}

// placeholdersAhead is how far ahead of now placeholders start: the
// next two days, enough for players that read the guide once a day.
const placeholdersAhead = 48 * time.Hour

// placeholderIDs is where the IDs of placeholder programmes start, below
// zero, far below those of the guides of the household's own: a
// placeholder's ID is minus this plus its start in whole minutes since
// 1970, so that the box's page tells a channel's placeholders apart
// (D142). It stays within what JavaScript's numbers hold exactly.
const placeholderIDs = 1 << 50

// Placeholders returns the length of placeholder programmes' blocks, or
// 0 if channels have none (D142), as until it is set.
func (s *Store) Placeholders(ctx context.Context) (time.Duration, error) {
	v, _, err := s.setting(ctx, settingPlaceholders)
	if err != nil {
		return 0, fmt.Errorf("store: placeholder programmes: %w", err)
	}
	n, _ := strconv.Atoi(v)
	block := time.Duration(n) * time.Minute
	if !slices.Contains(PlaceholderBlocks, block) {
		return 0, nil
	}
	return block, nil
}

// SetPlaceholders sets the length of placeholder programmes' blocks, one
// of PlaceholderBlocks, or 0 for none.
func (s *Store) SetPlaceholders(ctx context.Context, block time.Duration) error {
	if block != 0 && !slices.Contains(PlaceholderBlocks, block) {
		return fmt.Errorf("store: placeholder programmes: %v is not a length that their blocks can have", block)
	}
	return s.setSetting(ctx, settingPlaceholders, strconv.Itoa(int(block/time.Minute)))
}

// placeholders makes placeholder programmes: blocks of length block from
// the midnights of zone, at now.
type placeholders struct {
	block time.Duration
	zone  *time.Location
	now   time.Time
}

// placeholdersAt returns the placeholders that the setting asks for at
// now, and whether it asks for any.
func (s *Store) placeholdersAt(ctx context.Context, now time.Time) (placeholders, bool, error) {
	block, err := s.Placeholders(ctx)
	if err != nil || block == 0 {
		return placeholders{}, false, err
	}
	zone := s.zone
	if zone == nil {
		zone = time.Local
	}
	return placeholders{block: block, zone: zone, now: now}, true, nil
}

// span is a block of time, from start to stop.
type span struct{ start, stop time.Time }

// blockAt returns the block that holds t: the one of length f.block,
// counted from midnight in f.zone, that starts at t or before it, cut
// short at the next midnight, which a day of 23 or 25 hours moves.
func (f placeholders) blockAt(t time.Time) span {
	local := t.In(f.zone)
	y, m, d := local.Date()
	midnight := time.Date(y, m, d, 0, 0, 0, 0, f.zone)
	next := time.Date(y, m, d+1, 0, 0, 0, 0, f.zone)
	if midnight.After(t) || !next.After(midnight) {
		// A zone whose clocks skip or repeat midnight: blocks of the hour
		// in UTC, for that day.
		start := t.Truncate(f.block)
		return span{start, start.Add(f.block)}
	}
	start := midnight.Add(t.Sub(midnight) / f.block * f.block)
	return span{start, earliest(start.Add(f.block), next)}
}

// blocks returns the blocks that can hold placeholders and overlap from
// to to: those that end after now and start within placeholdersAhead of
// it.
func (f placeholders) blocks(from, to time.Time) []span {
	from = latest(from, f.now)
	to = earliest(to, f.now.Add(placeholdersAhead))
	var out []span
	for t := from; t.Before(to); {
		b := f.blockAt(t)
		if !b.stop.After(t) {
			break // never, but never loop
		}
		out = append(out, b)
		t = b.stop
	}
	return out
}

// window returns the window to read a channel's programmes over, so that
// fill knows of every programme in the blocks that overlap from to to.
func (f placeholders) window(from, to time.Time) (time.Time, time.Time) {
	bs := f.blocks(from, to)
	if len(bs) == 0 {
		return from, to
	}
	return earliest(from, bs[0].start), latest(to, bs[len(bs)-1].stop)
}

// earliest and latest return the earlier and the later of two times.
func earliest(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func latest(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// overlaps reports whether a programme ends after from and starts before
// to, as the queries of programmes choose them.
func overlaps(p Programme, from, to time.Time) bool {
	return !p.Start.Before(from.Add(-longestProgramme)) && p.Start.Before(to) && p.Stop.After(from)
}

// fill returns a channel's programmes that overlap from to to, with
// placeholders named name in the blocks that overlap it where ps, the
// channel's programmes by start over window's span, has none at all, by
// start time.
func (f placeholders) fill(ps []Programme, name string, from, to time.Time) []Programme {
	bs := f.blocks(from, to)
	out := make([]Programme, 0, len(ps)+len(bs))
	// ps by start: the programmes that start before a block ends, j of
	// them, reach until the latest stop among them.
	j := 0
	var reach time.Time
	i := 0 // the next of ps to put out
	for _, b := range bs {
		for ; j < len(ps) && ps[j].Start.Before(b.stop); j++ {
			reach = latest(reach, ps[j].Stop)
		}
		if j > 0 && reach.After(b.start) {
			continue
		}
		for ; i < len(ps) && ps[i].Start.Before(b.start); i++ {
			if overlaps(ps[i], from, to) {
				out = append(out, ps[i])
			}
		}
		out = append(out, Programme{
			ID:          -(placeholderIDs + b.start.Unix()/60),
			Start:       b.start.UTC(),
			Stop:        b.stop.UTC(),
			Title:       name,
			Placeholder: true,
		})
	}
	for ; i < len(ps); i++ {
		if overlaps(ps[i], from, to) {
			out = append(out, ps[i])
		}
	}
	return out
}

// entryShownName returns the name of a lineup entry's channel as devices
// see it, or ErrNotFound for an entry that devices of the lineup do not
// see.
func (s *Store) entryShownName(ctx context.Context, lineupID, entryID int64) (string, error) {
	var name string
	err := s.db.QueryRowContext(ctx, `SELECT `+entryName+lineupEntryJoins+`
		WHERE e.id = ? AND e.lineup_id = ? AND `+shownEntry, entryID, lineupID).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("store: lineup entry %d: %w", entryID, err)
	}
	return name, nil
}

// shownNames returns the names of the channels that a lineup shows, or of
// the entries given among them, by entry ID, as devices see them.
func (s *Store) shownNames(ctx context.Context, lineupID int64, entryIDs ...int64) (map[int64]string, error) {
	args := []any{lineupID}
	only := ""
	if len(entryIDs) > 0 {
		only = " AND e.id IN (?" + strings.Repeat(", ?", len(entryIDs)-1) + ")"
		for _, id := range entryIDs {
			args = append(args, id)
		}
	}
	out := map[int64]string{}
	err := eachRow(ctx, s.db, `SELECT e.id, `+entryName+lineupEntryJoins+`
		WHERE e.lineup_id = ? AND `+shownEntry+only, args, func(rows *sql.Rows) error {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return err
		}
		out[id] = name
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("store: channels of lineup %d: %w", lineupID, err)
	}
	return out, nil
}

// EntryListings returns what the guides that devices and players read
// list on a lineup entry's channel between from and to, at now: its
// programmes, as EntryProgrammes returns them, with placeholders where
// the setting asks for them (D142). It returns ErrNotFound for an entry
// that devices of the lineup do not see.
func (s *Store) EntryListings(ctx context.Context, lineupID, entryID int64, from, to, now time.Time) ([]Programme, error) {
	f, on, err := s.placeholdersAt(ctx, now)
	if err != nil {
		return nil, err
	}
	if !on {
		return s.EntryProgrammes(ctx, lineupID, entryID, from, to)
	}
	name, err := s.entryShownName(ctx, lineupID, entryID)
	if err != nil {
		return nil, err
	}
	wfrom, wto := f.window(from, to)
	ps, err := s.EntryProgrammes(ctx, lineupID, entryID, wfrom, wto)
	if err != nil {
		return nil, err
	}
	return f.fill(ps, name, from, to), nil
}

// LineupListings returns, by lineup entry, what the guides that devices
// and players read list on every channel that a lineup shows, or on the
// entries given, between from and to, at now: their programmes, as
// LineupProgrammes returns them, with placeholders where the setting
// asks for them (D142). Entries without any are left out.
func (s *Store) LineupListings(ctx context.Context, lineupID int64, from, to, now time.Time, entryIDs ...int64) (map[int64][]Programme, error) {
	f, on, err := s.placeholdersAt(ctx, now)
	if err != nil {
		return nil, err
	}
	if !on {
		return s.LineupProgrammes(ctx, lineupID, from, to, entryIDs...)
	}
	names, err := s.shownNames(ctx, lineupID, entryIDs...)
	if err != nil {
		return nil, err
	}
	wfrom, wto := f.window(from, to)
	byEntry, err := s.LineupProgrammes(ctx, lineupID, wfrom, wto, entryIDs...)
	if err != nil {
		return nil, err
	}
	out := make(map[int64][]Programme, len(names))
	for id, name := range names {
		if ps := f.fill(byEntry[id], name, from, to); len(ps) > 0 {
			out[id] = ps
		}
	}
	return out, nil
}

// EachLineupListing calls fn with what the guides that devices and
// players read list on every channel that a lineup shows between from and
// to, at now, as EachLineupProgramme does: their programmes, with
// placeholders where the setting asks for them (D142). It holds one
// channel's programmes at a time, and stops at fn's first error, which it
// returns.
func (s *Store) EachLineupListing(ctx context.Context, lineupID int64, from, to, now time.Time, fn func(entryID int64, p Programme) error) error {
	f, on, err := s.placeholdersAt(ctx, now)
	if err != nil {
		return err
	}
	if !on {
		return s.EachLineupProgramme(ctx, lineupID, from, to, fn)
	}
	names, err := s.shownNames(ctx, lineupID)
	if err != nil {
		return err
	}
	// Each channel's programmes come together, from one guide: hold
	// them until the next channel's come, then put them out with its
	// placeholders.
	done := make(map[int64]bool, len(names))
	var held []Programme
	var heldID int64
	flush := func() error {
		if heldID == 0 {
			return nil
		}
		id, ps := heldID, held
		heldID, held = 0, held[:0]
		name, shown := names[id]
		if done[id] || !shown {
			// Its programmes came apart, or it came after the names
			// were read, as a read of a guide or a source between two
			// queries can make them: put them out as they are.
			for _, p := range ps {
				if overlaps(p, from, to) {
					if err := fn(id, p); err != nil {
						return err
					}
				}
			}
			return nil
		}
		done[id] = true
		for _, p := range f.fill(ps, name, from, to) {
			if err := fn(id, p); err != nil {
				return err
			}
		}
		return nil
	}
	wfrom, wto := f.window(from, to)
	var fnErr error
	err = s.EachLineupProgramme(ctx, lineupID, wfrom, wto, func(entryID int64, p Programme) error {
		if entryID != heldID {
			if fnErr = flush(); fnErr != nil {
				return fnErr
			}
			heldID = entryID
		}
		held = append(held, p)
		return nil
	})
	if fnErr != nil {
		return fnErr
	}
	if err != nil {
		return err
	}
	if err := flush(); err != nil {
		return err
	}
	ids := make([]int64, 0, len(names))
	for id := range names {
		if !done[id] {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		for _, p := range f.fill(nil, names[id], from, to) {
			if err := fn(id, p); err != nil {
				return err
			}
		}
	}
	return nil
}
