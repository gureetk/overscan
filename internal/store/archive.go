// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"overscan.gureet.ca/internal/catchup"
	"overscan.gureet.ca/internal/source"
)

// Catch-up (D81, D82): a channel whose source keeps an archive plays its
// past programmes from it. An Xtream server's channel plays its archive at
// the server's timeshift address, made from the source's login as its live
// address is; a playlist's, as the playlist describes it.

// ErrNoArchive is returned for a channel whose past programmes devices
// cannot play: its source keeps no archive of it, or Overscan cannot play
// the archive it keeps.
var ErrNoArchive = errors.New("store: the channel has no archive that plays")

// ArchiveEntry is what the stream hub needs to play a lineup entry's past
// programmes from its source's archive. Its Archive holds the source's
// login, so it never goes to a device.
type ArchiveEntry struct {
	PlayableEntry
	// Days is how many days back the archive reaches.
	Days    int
	Archive catchup.Archive
}

// PlayableArchive returns the lineup entry with the given ID, if a device
// may play it, with what plays its archive. It returns ErrNotFound for an
// entry that devices do not see, and ErrNoArchive for one without an
// archive that plays.
func (s *Store) PlayableArchive(ctx context.Context, entryID int64) (ArchiveEntry, error) {
	var a ArchiveEntry
	var ref string
	var sealed, catchupSealed, sourceSealed []byte
	var sourceType, setZone, serverZone string
	err := s.db.QueryRowContext(ctx, `
		SELECT e.id, e.lineup_id, c.id, c.source_id, COALESCE(e.name, c.name), c.stream_url, c.stream_secret,
			c.catchup_secret, s.type, s.catchup_zone, s.account_time_zone,
			(SELECT secret FROM source_secrets WHERE source_id = s.id), `+lockedEntry+`,
			COALESCE(c.repair, `+repairDefaultSQL+`), `+repairHLSSQL+`, `+archiveDaysSQL+
		lineupEntryJoins+`
		WHERE e.id = ? AND `+shownEntry,
		entryID).Scan(&a.EntryID, &a.LineupID, &a.ChannelID, &a.SourceID, &a.Name, &ref, &sealed,
		&catchupSealed, &sourceType, &setZone, &serverZone, &sourceSealed, &a.Locked, &a.Repair, &a.RepairHLS, &a.Days)
	if errors.Is(err, sql.ErrNoRows) {
		return ArchiveEntry{}, ErrNotFound
	}
	if err == nil && a.Days <= 0 {
		return ArchiveEntry{}, ErrNoArchive
	}
	if err == nil && sealed != nil {
		ref, err = s.open(streamContext(a.SourceID), sealed)
	}
	if err != nil {
		return ArchiveEntry{}, fmt.Errorf("store: archive of lineup entry %d: %w", entryID, err)
	}
	fail := func(err error) (ArchiveEntry, error) {
		return ArchiveEntry{}, fmt.Errorf("store: archive of lineup entry %d: %w", entryID, err)
	}
	a.StreamURL = ref
	switch {
	case sourceType == source.TypeXtream:
		id, ext, ok := catchup.XtreamRef(ref)
		if !ok {
			return ArchiveEntry{}, ErrNoArchive
		}
		if sourceSealed == nil {
			return fail(ErrNoAddress)
		}
		secret, err := s.openSource(a.SourceID, sourceSealed)
		if err != nil {
			return fail(err)
		}
		a.Archive.Xtream = &catchup.XtreamStream{Server: secret.URL, Username: secret.Username,
			Password: secret.Password, ID: id, Ext: ext}
		settings := source.Settings{Type: sourceType, URL: secret.URL, Username: secret.Username, Password: secret.Password}
		if a.StreamURL, err = settings.StreamURL(ref); err != nil {
			return fail(err)
		}
		a.Archive.Zone = archiveZone(setZone, serverZone)
	case catchupSealed != nil:
		plain, err := s.open(catchupContext(a.SourceID), catchupSealed)
		if err != nil {
			return fail(err)
		}
		if err := json.Unmarshal([]byte(plain), &a.Archive.Playlist); err != nil {
			return fail(ErrUnreadable)
		}
		a.Archive.Live = ref
		a.Archive.Zone = archiveZone(setZone, "")
	default:
		return ArchiveEntry{}, ErrNoArchive
	}
	return a, nil
}

// archiveZone returns the time zone of a source's catch-up addresses: the
// one its setting names, or else the one its server gives, or else UTC.
func archiveZone(setting, server string) *time.Location {
	for _, name := range []string{setting, server} {
		if name == "" {
			continue
		}
		if loc, err := time.LoadLocation(name); err == nil {
			return loc
		}
	}
	return time.UTC
}

// ProgrammeAt returns the programme of a lineup entry's channel that starts
// at start, from the generation that devices see. It returns ErrNotFound
// for an entry that devices of the lineup do not see, or a time when no
// programme starts.
func (s *Store) ProgrammeAt(ctx context.Context, lineupID, entryID int64, start time.Time) (Programme, error) {
	return s.ProgrammeStarting(ctx, lineupID, entryID, start, start.Add(time.Second))
}

// ProgrammeStarting returns the first programme of a lineup entry's channel
// that starts at from or later, before to, from the generation that devices
// see. It returns ErrNotFound for an entry that devices of the lineup do
// not see, or when no programme starts then.
func (s *Store) ProgrammeStarting(ctx context.Context, lineupID, entryID int64, from, to time.Time) (Programme, error) {
	var p Programme
	var start, stop int64
	err := s.db.QueryRowContext(ctx, `
		SELECT p.id, p.start, p.stop, p.title, p.subtitle, p.description, p.category`+lineupEntryJoins+`
		JOIN programmes p ON p.channel_id = c.id
		JOIN source_guides sg ON sg.source_id = p.source_id AND sg.generation = p.generation
		WHERE e.id = ? AND e.lineup_id = ? AND `+shownEntry+` AND p.start >= ? AND p.start < ?
		ORDER BY p.start, p.id LIMIT 1`,
		entryID, lineupID, from.Unix(), to.Unix()).Scan(&p.ID, &start, &stop, &p.Title, &p.SubTitle, &p.Description, &p.Category)
	if errors.Is(err, sql.ErrNoRows) {
		return Programme{}, ErrNotFound
	}
	if err != nil {
		return Programme{}, fmt.Errorf("store: programme of lineup entry %d: %w", entryID, err)
	}
	p.Start, p.Stop = time.Unix(start, 0).UTC(), time.Unix(stop, 0).UTC()
	return p, nil
}

// EntryArchiveDays returns how many days back devices can play the past
// programmes of a lineup entry's channel from its source's archive (D81):
// 0 for a channel without such an archive, or an entry that devices of the
// lineup do not see.
func (s *Store) EntryArchiveDays(ctx context.Context, lineupID, entryID int64) (int, error) {
	var days int
	err := s.db.QueryRowContext(ctx, `SELECT `+archiveDaysSQL+lineupEntryJoins+`
		WHERE e.id = ? AND e.lineup_id = ? AND `+shownEntry, entryID, lineupID).Scan(&days)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("store: archive of lineup entry %d: %w", entryID, err)
	}
	return days, nil
}

// InArchive reports whether a programme has ended by now and started
// within the days that an archive reaches back, so that it plays from it.
func InArchive(p Programme, days int, now time.Time) bool {
	return days > 0 && !p.Stop.After(now) && p.Start.After(now.Add(-time.Duration(days)*24*time.Hour))
}
