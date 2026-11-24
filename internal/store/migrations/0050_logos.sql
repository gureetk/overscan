-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 50: logos (D141).
--
-- A channel's logo is, in this order: one set by hand on its lineup entry
-- (logo: NULL for none set, '' for no logo at all, else an address); a
-- rule's (rule_logo, D107); else its source's, if it loads; else its
-- guide's, if it loads; else none, and devices show its name. Its guide's
-- is the icon of the channel of a guide of the household's own that fills
-- it, or of the channel of its source's guide that its source's guide
-- matched it with (guide_icon). Each guide of the household's own says
-- whether its logos are used (logos: 'fill' where a channel's own is
-- missing or does not load, 'first' before it, 'none' never), and so does
-- each source of its own guide's (guide_logos), never at first, so that
-- an upgrade keeps every logo as it was. Whether a logo loads is what
-- fetching it last found (logo_checks), checked in the background and
-- never as a device asks.

ALTER TABLE lineup_entries ADD COLUMN logo TEXT;

ALTER TABLE channels ADD COLUMN guide_icon TEXT NOT NULL DEFAULT '';

ALTER TABLE sources ADD COLUMN guide_logos TEXT NOT NULL DEFAULT 'none' CHECK (guide_logos IN ('none', 'fill', 'first'));

ALTER TABLE guides ADD COLUMN logos TEXT NOT NULL DEFAULT 'fill' CHECK (logos IN ('none', 'fill', 'first'));

CREATE TABLE logo_checks (
    address    TEXT    PRIMARY KEY,
    loads      INTEGER NOT NULL CHECK (loads IN (0, 1)),
    checked_at TEXT    NOT NULL
) STRICT, WITHOUT ROWID;
