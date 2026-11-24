// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// A channel's guide chosen by hand (D140). The choice belongs to the
// source's channel, holds in every lineup, and wins over the guides' own
// matches (D139) and over rules (D108). A channel can take another
// channel's guide, as devices see it on that channel; a channel of a guide
// of the household's own, whose match is then kept, marked by hand, beside
// the guides' own; or none at all.

// ErrNoGuideChoice is returned for a choice of a channel, or of a guide's
// channel, that does not exist, or of the channel itself.
var ErrNoGuideChoice = errors.New("store: no such channel to take a guide from")

// GuideChoice is a channel's guide chosen by hand. With neither UseChannel
// nor GuideChannelID, the channel has no guide.
type GuideChoice struct {
	ChannelID      int64
	UseChannel     int64 // another channel, whose programmes it shows
	GuideChannelID int64 // a channel of a guide of the household's own
	ChosenAt       time.Time
}

// None reports whether the choice is no guide at all.
func (c GuideChoice) None() bool { return c.UseChannel == 0 && c.GuideChannelID == 0 }

// SetGuideChoice chooses a channel's guide by hand, in place of any choice
// it had. A channel of a guide of the household's own takes effect at
// once if the guide is on; the guide keeps its programmes from its next
// read.
func (s *Store) SetGuideChoice(ctx context.Context, c GuideChoice, at time.Time) error {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var missing bool
		err := tx.QueryRowContext(ctx, "SELECT missing_since IS NOT NULL FROM channels WHERE id = ?", c.ChannelID).Scan(&missing)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		var use, guideChannel sql.NullInt64
		enabled := false
		switch {
		case c.UseChannel != 0 && c.GuideChannelID != 0:
			return ErrNoGuideChoice
		case c.UseChannel != 0:
			if c.UseChannel == c.ChannelID {
				return ErrNoGuideChoice
			}
			var one int
			if err := tx.QueryRowContext(ctx, "SELECT 1 FROM channels WHERE id = ?", c.UseChannel).Scan(&one); errors.Is(err, sql.ErrNoRows) {
				return ErrNoGuideChoice
			} else if err != nil {
				return err
			}
			use = sql.NullInt64{Int64: c.UseChannel, Valid: true}
		case c.GuideChannelID != 0:
			err := tx.QueryRowContext(ctx, `
				SELECT g.enabled FROM guide_channels gc JOIN guides g ON g.id = gc.guide_id WHERE gc.id = ?`,
				c.GuideChannelID).Scan(&enabled)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNoGuideChoice
			}
			if err != nil {
				return err
			}
			guideChannel = sql.NullInt64{Int64: c.GuideChannelID, Valid: true}
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO channel_guide_choices (channel_id, use_channel, guide_channel_id, chosen_at) VALUES (?, ?, ?, ?)
			ON CONFLICT (channel_id) DO UPDATE SET use_channel = excluded.use_channel,
				guide_channel_id = excluded.guide_channel_id, chosen_at = excluded.chosen_at`,
			c.ChannelID, use, guideChannel, formatTime(at)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM channel_guide_matches WHERE channel_id = ?", c.ChannelID); err != nil {
			return err
		}
		if guideChannel.Valid && enabled && !missing {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO channel_guide_matches (channel_id, guide_channel_id, by_name, by_hand) VALUES (?, ?, 0, 1)`,
				c.ChannelID, c.GuideChannelID); err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrNoGuideChoice) {
		return err
	}
	if err != nil {
		return fmt.Errorf("store: choose channel %d's guide: %w", c.ChannelID, err)
	}
	return nil
}

// ClearGuideChoice takes back a channel's choice by hand, so that its guide
// is found as before: its source's, or a guide of the household's own that
// matches it, once the guides' matches are worked out again. It reports
// whether there was a choice.
func (s *Store) ClearGuideChoice(ctx context.Context, channelID int64) (bool, error) {
	var had bool
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM channel_guide_choices WHERE channel_id = ?", channelID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		had = n > 0
		_, err = tx.ExecContext(ctx, "DELETE FROM channel_guide_matches WHERE channel_id = ? AND by_hand = 1", channelID)
		return err
	})
	if err != nil {
		return false, fmt.Errorf("store: take back channel %d's guide: %w", channelID, err)
	}
	return had, nil
}

// GuideChoiceOf returns a channel's choice by hand, and whether it has one.
func (s *Store) GuideChoiceOf(ctx context.Context, channelID int64) (GuideChoice, bool, error) {
	c := GuideChoice{ChannelID: channelID}
	var use, guideChannel sql.NullInt64
	var at string
	err := s.db.QueryRowContext(ctx, `
		SELECT use_channel, guide_channel_id, chosen_at FROM channel_guide_choices WHERE channel_id = ?`,
		channelID).Scan(&use, &guideChannel, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return c, false, nil
	}
	if err == nil {
		c.UseChannel, c.GuideChannelID = use.Int64, guideChannel.Int64
		c.ChosenAt, err = parseTime(at)
	}
	if err != nil {
		return c, false, fmt.Errorf("store: channel %d's guide: %w", channelID, err)
	}
	return c, true, nil
}

// HandMatch is the match that a choice by hand of a guide's channel makes,
// with the days of archive of the channel, as the guide keeps the past of
// the channels it fills.
type HandMatch struct {
	GuideMatch
	ArchiveDays int
}

// HandMatches returns the matches that choices by hand make: those of
// channels that their sources still list with a channel of an enabled
// guide of the household's own.
func (s *Store) HandMatches(ctx context.Context) ([]HandMatch, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT hc.channel_id, hc.guide_channel_id, c.archive_days FROM channel_guide_choices hc
		JOIN channels c ON c.id = hc.channel_id
		JOIN guide_channels gc ON gc.id = hc.guide_channel_id JOIN guides g ON g.id = gc.guide_id
		WHERE g.enabled = 1 AND c.missing_since IS NULL ORDER BY hc.channel_id`)
	if err != nil {
		return nil, fmt.Errorf("store: guides chosen by hand: %w", err)
	}
	defer rows.Close()
	var out []HandMatch
	for rows.Next() {
		m := HandMatch{GuideMatch: GuideMatch{ByHand: true}}
		if err := rows.Scan(&m.ChannelID, &m.GuideChannelID, &m.ArchiveDays); err != nil {
			return nil, fmt.Errorf("store: guides chosen by hand: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: guides chosen by hand: %w", err)
	}
	return out, nil
}

// ChannelGuide says where a channel's programmes come from, before any
// lineup's rules: a choice by hand, its own source's guide, or a guide of
// the household's own that matches it.
type ChannelGuide struct {
	ChannelID int64
	Name      string // as its source gives it
	XMLTVID   string
	Category  string // its category's name at its source, "" if none
	SourceID  int64
	Source    string
	Missing   bool // its source no longer lists it
	Own       bool // its own source's guide gives it programmes

	// The channel of a guide of the household's own that fills it, by its
	// guide ID, its name, or a choice by hand; GuideChannelID is 0 if none.
	GuideChannelID int64
	GuideID        int64
	Guide          string
	GuideNames     []string
	GuideXMLTVID   string
	GuideOn        bool // the guide is on
	ByName, ByHand bool

	// A choice by hand, if Chosen: no guide, another channel's, or a
	// guide's channel, which the fields above then name.
	Chosen     bool
	UseChannel int64
	UseName    string
	UseSource  string
	ChosenAt   time.Time
}

// From says where the channel's programmes come from: "none" for no
// guide chosen by hand, "channel" for another channel's chosen by hand,
// "guide" for a guide's channel, chosen or matched, "own" for its source's
// guide, and "" if nothing gives it programmes.
func (g ChannelGuide) From() string {
	switch {
	case g.Chosen && g.UseChannel != 0:
		return "channel"
	case g.Chosen && g.GuideChannelID == 0:
		return "none"
	case g.GuideChannelID != 0:
		return "guide"
	case g.Own:
		return "own"
	}
	return ""
}

// Which channels ChannelGuides lists.
const (
	ShowWithoutGuide = "without" // nothing gives them programmes, and nobody chose
	ShowByName       = "by_name" // a guide of the household's own fills them by name
	ShowByHand       = "by_hand" // their guide was chosen by hand
	ShowAll          = "all"
)

const ownProgrammesExist = `EXISTS (
	SELECT 1 FROM programmes p JOIN source_guides sg ON sg.source_id = p.source_id AND sg.generation = p.generation
	WHERE p.channel_id = c.id)`

const channelGuideSelect = `
	SELECT c.id, c.name, c.xmltv_id, COALESCE(k.name, ''), c.source_id, s.name, c.missing_since IS NOT NULL,
		` + ownProgrammesExist + `,
		COALESCE(gc.id, 0), COALESCE(gc.guide_id, 0), COALESCE(g.name, ''), COALESCE(gc.names, ''),
		COALESCE(gc.xmltv_id, ''), COALESCE(g.enabled, 0), COALESCE(gm.by_name, 0), hc.guide_channel_id IS NOT NULL,
		hc.channel_id IS NOT NULL, COALESCE(hc.use_channel, 0), COALESCE(u.name, ''), COALESCE(us.name, ''),
		COALESCE(hc.chosen_at, '')
	FROM channels c JOIN sources s ON s.id = c.source_id LEFT JOIN categories k ON k.id = c.category_id
	LEFT JOIN channel_guide_choices hc ON hc.channel_id = c.id
	LEFT JOIN channel_guide_matches gm ON gm.channel_id = c.id AND hc.channel_id IS NULL
	LEFT JOIN guide_channels gc ON gc.id = COALESCE(hc.guide_channel_id, gm.guide_channel_id)
	LEFT JOIN guides g ON g.id = gc.guide_id
	LEFT JOIN channels u ON u.id = hc.use_channel LEFT JOIN sources us ON us.id = u.source_id`

func scanChannelGuide(row rowScanner) (ChannelGuide, error) {
	var g ChannelGuide
	var names, at string
	if err := row.Scan(&g.ChannelID, &g.Name, &g.XMLTVID, &g.Category, &g.SourceID, &g.Source, &g.Missing, &g.Own,
		&g.GuideChannelID, &g.GuideID, &g.Guide, &names, &g.GuideXMLTVID, &g.GuideOn, &g.ByName, &g.ByHand,
		&g.Chosen, &g.UseChannel, &g.UseName, &g.UseSource, &at); err != nil {
		return g, err
	}
	if names != "" {
		g.GuideNames = strings.Split(names, "\n")
	}
	if at != "" {
		t, err := parseTime(at)
		if err != nil {
			return g, err
		}
		g.ChosenAt = t
	}
	return g, nil
}

// channelGuideFilter is the condition for the channels that ChannelGuides
// lists, by show.
func channelGuideFilter(show string) (string, bool) {
	switch show {
	case ShowWithoutGuide:
		return "hc.channel_id IS NULL AND gm.channel_id IS NULL AND NOT " + ownProgrammesExist, true
	case ShowByName:
		return "gm.by_name = 1", true
	case ShowByHand:
		return "hc.channel_id IS NOT NULL", true
	case ShowAll, "":
		return "1", true
	}
	return "", false
}

// likeText returns a LIKE pattern that finds text anywhere, with its own
// wildcards escaped by a backslash.
func likeText(text string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(text) + "%"
}

// ChannelGuides returns where the programmes of the channels of enabled
// sources that their sources still list come from, those that show says,
// whose names hold find if it is not "", in their sources' order: at most
// limit of them after the first offset, and how many there are.
func (s *Store) ChannelGuides(ctx context.Context, show, find string, offset, limit int) ([]ChannelGuide, int, error) {
	cond, ok := channelGuideFilter(show)
	if !ok {
		return nil, 0, fmt.Errorf("store: channels' guides: no such list %q", show)
	}
	where := " WHERE s.enabled = 1 AND c.missing_since IS NULL AND " + cond
	args := []any{}
	if find = strings.TrimSpace(find); find != "" {
		where += ` AND c.name LIKE ? ESCAPE '\'`
		args = append(args, likeText(find))
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM channels c JOIN sources s ON s.id = c.source_id
		LEFT JOIN channel_guide_choices hc ON hc.channel_id = c.id
		LEFT JOIN channel_guide_matches gm ON gm.channel_id = c.id AND hc.channel_id IS NULL`+where,
		args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("store: channels' guides: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, channelGuideSelect+where+" ORDER BY c.source_id, c.position LIMIT ? OFFSET ?",
		append(args, max(limit, 0), max(offset, 0))...)
	if err != nil {
		return nil, 0, fmt.Errorf("store: channels' guides: %w", err)
	}
	defer rows.Close()
	var out []ChannelGuide
	for rows.Next() {
		g, err := scanChannelGuide(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("store: channels' guides: %w", err)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("store: channels' guides: %w", err)
	}
	return out, total, nil
}

// ChannelGuideCounts counts the channels of enabled sources that their
// sources still list, by the lists of ChannelGuides other than all.
func (s *Store) ChannelGuideCounts(ctx context.Context) (map[string]int, error) {
	var without, byName, byHand int
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(sum(hc.channel_id IS NULL AND gm.channel_id IS NULL AND NOT `+ownProgrammesExist+`), 0),
			COALESCE(sum(COALESCE(gm.by_name, 0)), 0), COALESCE(sum(hc.channel_id IS NOT NULL), 0)
		FROM channels c JOIN sources s ON s.id = c.source_id
		LEFT JOIN channel_guide_choices hc ON hc.channel_id = c.id
		LEFT JOIN channel_guide_matches gm ON gm.channel_id = c.id AND hc.channel_id IS NULL
		WHERE s.enabled = 1 AND c.missing_since IS NULL`).Scan(&without, &byName, &byHand)
	if err != nil {
		return nil, fmt.Errorf("store: channels' guides: %w", err)
	}
	return map[string]int{ShowWithoutGuide: without, ShowByName: byName, ShowByHand: byHand}, nil
}

// ChannelGuideOf returns where a channel's programmes come from, whatever
// its source's state.
func (s *Store) ChannelGuideOf(ctx context.Context, channelID int64) (ChannelGuide, error) {
	g, err := scanChannelGuide(s.db.QueryRowContext(ctx, channelGuideSelect+" WHERE c.id = ?", channelID))
	if errors.Is(err, sql.ErrNoRows) {
		return g, ErrNotFound
	}
	if err != nil {
		return g, fmt.Errorf("store: channel %d's guide: %w", channelID, err)
	}
	return g, nil
}

// GuidedChannel is a channel of an enabled source that its source still
// lists and whose own source's guide gives it programmes: one whose guide
// another channel can take.
type GuidedChannel struct {
	ID       int64
	Name     string
	XMLTVID  string
	SourceID int64
	Source   string
}

// GuidedChannels returns the channels whose guides others can take, in
// their sources' order.
func (s *Store) GuidedChannels(ctx context.Context) ([]GuidedChannel, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, c.name, c.xmltv_id, c.source_id, s.name FROM channels c JOIN sources s ON s.id = c.source_id
		WHERE s.enabled = 1 AND c.missing_since IS NULL AND `+ownProgrammesExist+`
		ORDER BY c.source_id, c.position`)
	if err != nil {
		return nil, fmt.Errorf("store: channels with a guide: %w", err)
	}
	defer rows.Close()
	var out []GuidedChannel
	for rows.Next() {
		var c GuidedChannel
		if err := rows.Scan(&c.ID, &c.Name, &c.XMLTVID, &c.SourceID, &c.Source); err != nil {
			return nil, fmt.Errorf("store: channels with a guide: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: channels with a guide: %w", err)
	}
	return out, nil
}

// ChannelProgrammes returns the programmes that a channel's own source's
// guide gives it, ending after from and starting before to, by start time,
// for the admin to check a guide by: those of the guide that devices see.
func (s *Store) ChannelProgrammes(ctx context.Context, channelID int64, from, to time.Time) ([]Programme, error) {
	return s.Programmes(ctx, channelID, from, to)
}

// GuideChannelProgrammes returns the programmes that a guide of the
// household's own holds for one of its channels, ending after from and
// starting before to, by start time: none for a channel that filled no
// channel at the guide's last read.
func (s *Store) GuideChannelProgrammes(ctx context.Context, guideChannelID int64, from, to time.Time) ([]Programme, error) {
	return s.guideProgrammes(ctx, guideChannelID, from, to)
}
