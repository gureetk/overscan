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
)

// The sources that an account uses (D114). An account uses every source, as
// every account does at first, or only some: its devices and players then
// list and play only what comes from those. A lineup's channel shows when
// one of its streams, its own or a backup's (D109, D113), comes from them,
// and plays from those streams alone; its past programmes come from its own
// source's archive, so a channel whose own source the account does not use
// has none. Films and series show when their source is one of them.

// Errors about the sources that an account uses.
var (
	// ErrNoSources is a choice of only some sources with none in it.
	ErrNoSources = errors.New("store: an account that uses only some sources needs one at least")
	// ErrNoSuchSource is a choice that names a source that does not exist.
	ErrNoSuchSource = errors.New("store: no such source")
)

// Sources are the sources that an account uses. The zero value is every
// source, those added later too.
type Sources struct {
	// Only says that the account uses only the sources in IDs, and none if
	// there are none: those it used were removed.
	Only bool
	IDs  []int64 // in order, when Only is set
}

// EverySource is the choice of every source, as every account has at first.
var EverySource = Sources{}

// Uses reports whether the sources hold the source with the given ID.
func (u Sources) Uses(sourceID int64) bool {
	return !u.Only || slices.Contains(u.IDs, sourceID)
}

// For returns a lineup's entry as an account that uses these sources plays
// it: with only those of its streams that come from them, its own first if
// it does, and the others as its backups, in order. It returns false when
// none does.
func (u Sources) For(p PlayableEntry) (PlayableEntry, bool) {
	if !u.Only {
		return p, true
	}
	var streams []PlayableEntry
	if u.Uses(p.SourceID) {
		own := p
		own.Backups = nil
		streams = append(streams, own)
	}
	for _, b := range p.Backups {
		if u.Uses(b.SourceID) {
			streams = append(streams, b)
		}
	}
	if len(streams) == 0 {
		return PlayableEntry{}, false
	}
	first := streams[0]
	first.Backups = slices.Clone(streams[1:])
	return first, true
}

// where returns SQL that, after AND, holds for a column of source IDs when
// the sources hold it, and its arguments.
func (u Sources) where(column string) (string, []any) {
	if !u.Only {
		return "1", nil
	}
	if len(u.IDs) == 0 {
		return "0", nil
	}
	args := make([]any, len(u.IDs))
	for i, id := range u.IDs {
		args[i] = id
	}
	return column + " IN (" + strings.TrimSuffix(strings.Repeat("?, ", len(u.IDs)), ", ") + ")", args
}

// parseIDs parses IDs that SQLite's group_concat joined with commas, in
// order.
func parseIDs(text string) ([]int64, error) {
	if text == "" {
		return nil, nil
	}
	parts := strings.Split(text, ",")
	ids := make([]int64, 0, len(parts))
	for _, p := range parts {
		id, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("source ID %q: %w", p, err)
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids, nil
}

// SetAccountSources sets the sources that an account uses. Only some
// sources need one at least (ErrNoSources), and each must exist
// (ErrNoSuchSource). It returns ErrNotFound for an account that does not
// exist.
func (s *Store) SetAccountSources(ctx context.Context, accountID int64, u Sources) error {
	ids := slices.Clone(u.IDs)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if u.Only && len(ids) == 0 {
		return ErrNoSources
	}
	if !u.Only {
		ids = nil
	}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "UPDATE accounts SET sources_limited = ? WHERE id = ?", u.Only, accountID)
		if err != nil {
			return err
		}
		if err := mustAffect(res); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM account_sources WHERE account_id = ?", accountID); err != nil {
			return err
		}
		for _, id := range ids {
			var exists bool
			if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM sources WHERE id = ?)", id).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return ErrNoSuchSource
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO account_sources (account_id, source_id) VALUES (?, ?)",
				accountID, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrNoSuchSource) {
		err = fmt.Errorf("store: sources of account %d: %w", accountID, err)
	}
	return err
}

// LimitedAccounts counts the accounts that use only some sources.
func (s *Store) LimitedAccounts(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM accounts WHERE sources_limited = 1").Scan(&n); err != nil {
		return 0, fmt.Errorf("store: accounts that use only some sources: %w", err)
	}
	return n, nil
}

// AccountChannels returns the channels that an account's devices and
// players see: those its lineup shows, or, when it uses only some sources,
// those of them with a stream from one, their own or a backup's. A channel
// whose own source it does not use has no past programmes for it.
func (s *Store) AccountChannels(ctx context.Context, acct Account) ([]LineupChannel, error) {
	all, err := s.LineupChannels(ctx, acct.LineupID)
	if err != nil || !acct.Sources.Only {
		return all, err
	}
	backups, err := s.backupSources(ctx, acct.LineupID)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, c := range all {
		switch {
		case acct.Sources.Uses(c.SourceID):
			out = append(out, c)
		case slices.ContainsFunc(backups[c.EntryID], acct.Sources.Uses):
			c.ArchiveDays = 0
			out = append(out, c)
		}
	}
	return out, nil
}

// AccountEntries returns the IDs of the lineup entries that an account's
// devices and players see, as AccountChannels lists them.
func (s *Store) AccountEntries(ctx context.Context, acct Account) (map[int64]bool, error) {
	list, err := s.AccountChannels(ctx, acct)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]bool, len(list))
	for _, c := range list {
		out[c.EntryID] = true
	}
	return out, nil
}

// AccountSees reports whether an account's devices and players see a
// lineup's entry, as AccountChannels lists it, and returns ErrNotFound if
// they do not.
func (s *Store) AccountSees(ctx context.Context, acct Account, entryID int64) error {
	if err := s.EntryShown(ctx, acct.LineupID, entryID); err != nil || !acct.Sources.Only {
		return err
	}
	p, err := s.PlayableEntry(ctx, entryID)
	if err != nil {
		return err
	}
	if _, ok := acct.Sources.For(p); !ok {
		return ErrNotFound
	}
	return nil
}

// backupSources returns the sources of the backups of a lineup's entries,
// those picked by hand and those that its rules give, by entry, for the
// backups that can play.
func (s *Store) backupSources(ctx context.Context, lineupID int64) (map[int64][]int64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT b.entry_id, c.source_id
		FROM (SELECT entry_id, channel_id FROM lineup_entry_backups
			UNION ALL SELECT entry_id, channel_id FROM lineup_entry_rule_backups) b
		JOIN lineup_entries e ON e.id = b.entry_id AND e.lineup_id = ?
		JOIN channels c ON c.id = b.channel_id
		JOIN sources s ON s.id = c.source_id
		WHERE s.enabled = 1 AND c.missing_since IS NULL`, lineupID)
	if err != nil {
		return nil, fmt.Errorf("store: sources of lineup %d's backups: %w", lineupID, err)
	}
	defer rows.Close()
	out := map[int64][]int64{}
	for rows.Next() {
		var entry, source int64
		if err := rows.Scan(&entry, &source); err != nil {
			return nil, fmt.Errorf("store: sources of lineup %d's backups: %w", lineupID, err)
		}
		out[entry] = append(out[entry], source)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: sources of lineup %d's backups: %w", lineupID, err)
	}
	return out, nil
}
