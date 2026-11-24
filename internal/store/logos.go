// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Logos (D141). A lineup entry's logo is, in this order: one set by hand
// on it, or none at all set by hand; a rule's (D107); else, automatically,
// its channel's own from its source, if it loads, and else its guide's, if
// it loads; a guide set to give its logos first comes before the
// channel's own. Its guide's is the icon of the channel of a guide of the
// household's own that fills it (D139, D140), else that of the channel of
// its source's guide that matched it. Whether a logo loads is what the
// background check last found (logo_checks); one never checked counts as
// loading, as before.

// The ways a guide's logos are used: never, where a channel's own is
// missing or does not load, or before it.
const (
	GuideLogosNone  = "none"
	GuideLogosFill  = "fill"
	GuideLogosFirst = "first"
)

// ValidGuideLogos reports whether a way of using a guide's logos exists.
func ValidGuideLogos(v string) bool {
	return v == GuideLogosNone || v == GuideLogosFill || v == GuideLogosFirst
}

// logoFails is true of a logo's address that the check found not to load.
func logoFails(address string) string {
	return `EXISTS (SELECT 1 FROM logo_checks lc WHERE lc.address = ` + address + ` AND lc.loads = 0)`
}

// The parts of an entry e's logo, for its channel c of its source s.
const (
	// addedIconSQL is the icon of the channel of an enabled guide of the
	// household's own that fills c, by a match or by hand, if the guide's
	// logos are used and it loads.
	addedIconSQL = `(SELECT gc.icon FROM channel_guide_matches lm
		JOIN guide_channels gc ON gc.id = lm.guide_channel_id JOIN guides lg ON lg.id = gc.guide_id
		WHERE lm.channel_id = c.id AND lg.enabled = 1 AND lg.logos <> 'none' AND gc.icon <> ''
			AND NOT EXISTS (SELECT 1 FROM logo_checks lc WHERE lc.address = gc.icon AND lc.loads = 0))`
	// addedFirstSQL is true if that guide gives its logos first.
	addedFirstSQL = `EXISTS (SELECT 1 FROM channel_guide_matches lm
		JOIN guide_channels gc ON gc.id = lm.guide_channel_id JOIN guides lg ON lg.id = gc.guide_id
		WHERE lm.channel_id = c.id AND lg.enabled = 1 AND lg.logos = 'first' AND gc.icon <> '')`
	// sourceIconSQL is the icon of the channel of c's source's guide that
	// matched it, if the source's guide's logos are used and it loads.
	sourceIconSQL = `(CASE WHEN s.guide_logos <> 'none' AND c.guide_icon <> ''
		AND NOT EXISTS (SELECT 1 FROM logo_checks lc WHERE lc.address = c.guide_icon AND lc.loads = 0) THEN c.guide_icon END)`
	// ownLogoSQL is c's own logo, if it has one that loads.
	ownLogoSQL = `(CASE WHEN c.logo <> '' AND NOT EXISTS (SELECT 1 FROM logo_checks lc WHERE lc.address = c.logo AND lc.loads = 0)
		THEN c.logo END)`
	guideIconSQL  = `COALESCE(` + addedIconSQL + `, ` + sourceIconSQL + `)`
	guideFirstSQL = `(` + addedFirstSQL + ` OR s.guide_logos = 'first' AND c.guide_icon <> '')`
	autoLogoSQL   = `(CASE WHEN ` + guideFirstSQL + ` THEN COALESCE(` + guideIconSQL + `, ` + ownLogoSQL + `)
		ELSE COALESCE(` + ownLogoSQL + `, ` + guideIconSQL + `) END)`
)

// Where an entry's logo comes from.
const (
	LogoFromHand        = "hand"         // set by hand
	LogoFromNone        = "none"         // none, set by hand
	LogoFromRule        = "rule"         // a rule's
	LogoFromSource      = "source"       // its channel's own, from its source
	LogoFromGuide       = "guide"        // a guide's of the household's own
	LogoFromSourceGuide = "source_guide" // its source's guide's
	LogoFromNothing     = ""             // nothing gives it one
)

// EntryLogoInfo says what logo an entry shows and why.
type EntryLogoInfo struct {
	Address string // what devices see a copy of, "" if none
	From    string // a LogoFrom
	ByHand  *string
	// Own is its channel's own logo, and OwnFails whether it does not
	// load; Guide names the guide of the household's own whose icon it
	// would take, if any.
	Own      string
	OwnFails bool
	Guide    string
}

// EntryLogoInfo returns what logo a lineup entry shows, and why.
func (s *Store) EntryLogoInfo(ctx context.Context, lineupID, entryID int64) (EntryLogoInfo, error) {
	var info EntryLogoInfo
	var byHand, rule sql.NullString
	var added, addedGuide, sourceIcon sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT `+entryLogo+`, e.logo, e.rule_logo, c.logo, `+logoFails("c.logo")+`,
			`+addedIconSQL+`, (SELECT lg.name FROM channel_guide_matches lm JOIN guide_channels gc ON gc.id = lm.guide_channel_id
				JOIN guides lg ON lg.id = gc.guide_id WHERE lm.channel_id = c.id AND lg.enabled = 1),
			`+sourceIconSQL+`
		FROM lineup_entries e JOIN channels c ON c.id = e.channel_id JOIN sources s ON s.id = c.source_id
		WHERE e.id = ? AND e.lineup_id = ?`, entryID, lineupID).Scan(&info.Address, &byHand, &rule, &info.Own, &info.OwnFails,
		&added, &addedGuide, &sourceIcon)
	if errors.Is(err, sql.ErrNoRows) {
		return info, ErrNotFound
	}
	if err != nil {
		return info, fmt.Errorf("store: lineup entry %d's logo: %w", entryID, err)
	}
	if byHand.Valid {
		info.ByHand = &byHand.String
	}
	info.Guide = addedGuide.String
	switch {
	case byHand.Valid && byHand.String == "":
		info.From = LogoFromNone
	case byHand.Valid:
		info.From = LogoFromHand
	case rule.Valid:
		info.From = LogoFromRule
	case info.Address == "":
		info.From = LogoFromNothing
	case added.Valid && info.Address == added.String:
		info.From = LogoFromGuide
	case info.Address == info.Own:
		info.From = LogoFromSource
	case sourceIcon.Valid && info.Address == sourceIcon.String:
		info.From = LogoFromSourceGuide
	}
	return info, nil
}

// LogoFetch is a logo to fetch, with what to fetch it as: the user agent
// and the choice of public addresses only of the source, or of the guide of
// the household's own, that gives it (D121).
type LogoFetch struct {
	Address    string
	UserAgent  string
	PublicOnly bool
}

// LogosToCheck returns logos to check whether they load: those that
// entries' logos are chosen between, their channels' own and their guides',
// for the channels that have a guide's logo to fall back on or to take
// first; that were never checked, or that did not load more than recheck
// ago, or that loaded more than reload ago. At most limit of them.
func (s *Store) LogosToCheck(ctx context.Context, now time.Time, recheck, reload time.Duration, limit int) ([]LogoFetch, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH guided AS (
			SELECT c.id AS channel_id, c.logo AS own, s.user_agent AS own_agent, s.public_only AS own_public,
				gc.icon AS icon, lg.user_agent AS agent, lg.public_only AS public
			FROM channel_guide_matches lm JOIN channels c ON c.id = lm.channel_id JOIN sources s ON s.id = c.source_id
				JOIN guide_channels gc ON gc.id = lm.guide_channel_id JOIN guides lg ON lg.id = gc.guide_id
			WHERE lg.enabled = 1 AND lg.logos <> 'none' AND gc.icon <> '' AND s.enabled = 1 AND c.missing_since IS NULL
			UNION ALL
			SELECT c.id, c.logo, s.user_agent, s.public_only, c.guide_icon, s.user_agent, s.public_only
			FROM channels c JOIN sources s ON s.id = c.source_id
			WHERE s.guide_logos <> 'none' AND c.guide_icon <> '' AND s.enabled = 1 AND c.missing_since IS NULL
		), wanted AS (
			SELECT icon AS address, agent, public FROM guided
			UNION
			SELECT own, own_agent, own_public FROM guided WHERE own <> ''
		)
		SELECT w.address, max(w.agent), max(w.public) FROM wanted w
		LEFT JOIN logo_checks lc ON lc.address = w.address
		WHERE lc.address IS NULL OR lc.loads = 0 AND lc.checked_at < ? OR lc.loads = 1 AND lc.checked_at < ?
		GROUP BY w.address ORDER BY lc.checked_at IS NOT NULL, lc.checked_at LIMIT ?`,
		formatTime(now.Add(-recheck)), formatTime(now.Add(-reload)), limit)
	if err != nil {
		return nil, fmt.Errorf("store: logos to check: %w", err)
	}
	defer rows.Close()
	var out []LogoFetch
	for rows.Next() {
		var f LogoFetch
		if err := rows.Scan(&f.Address, &f.UserAgent, &f.PublicOnly); err != nil {
			return nil, fmt.Errorf("store: logos to check: %w", err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: logos to check: %w", err)
	}
	return out, nil
}

// SetLogoCheck records whether a logo loads.
func (s *Store) SetLogoCheck(ctx context.Context, address string, loads bool, at time.Time) error {
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO logo_checks (address, loads, checked_at) VALUES (?, ?, ?)
		ON CONFLICT (address) DO UPDATE SET loads = excluded.loads, checked_at = excluded.checked_at`,
		address, loads, formatTime(at)); err != nil {
		return fmt.Errorf("store: a logo's check: %w", err)
	}
	return nil
}

// ForgetLogoChecks forgets the checks of logos checked before a time, so
// that the table keeps only what is still in use.
func (s *Store) ForgetLogoChecks(ctx context.Context, before time.Time) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM logo_checks WHERE checked_at < ?", formatTime(before)); err != nil {
		return fmt.Errorf("store: logos' checks: %w", err)
	}
	return nil
}

// LogoCounts counts the logos checked, and those of them that do not load.
func (s *Store) LogoCounts(ctx context.Context) (checked, failing int, err error) {
	if err := s.db.QueryRowContext(ctx, "SELECT count(*), COALESCE(sum(loads = 0), 0) FROM logo_checks").Scan(&checked, &failing); err != nil {
		return 0, 0, fmt.Errorf("store: logos' checks: %w", err)
	}
	return checked, failing, nil
}

// SetSourceGuideLogos sets how a source's guide's logos are used.
func (s *Store) SetSourceGuideLogos(ctx context.Context, sourceID int64, how string) error {
	if !ValidGuideLogos(how) {
		return fmt.Errorf("store: no such way to use a guide's logos: %q", how)
	}
	res, err := s.db.ExecContext(ctx, "UPDATE sources SET guide_logos = ? WHERE id = ?", how, sourceID)
	if err != nil {
		return fmt.Errorf("store: source %d's guide's logos: %w", sourceID, err)
	}
	return mustAffect(res)
}

// SetGuideLogos sets how a guide of the household's own's logos are used.
func (s *Store) SetGuideLogos(ctx context.Context, guideID int64, how string) error {
	if !ValidGuideLogos(how) {
		return fmt.Errorf("store: no such way to use a guide's logos: %q", how)
	}
	res, err := s.db.ExecContext(ctx, "UPDATE guides SET logos = ? WHERE id = ?", how, guideID)
	if err != nil {
		return fmt.Errorf("store: guide %d's logos: %w", guideID, err)
	}
	return mustAffect(res)
}
