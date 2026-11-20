-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 37: a lineup's rules (D107).
--
-- A rule is a JSON object that internal/rules checks: what it matches and
-- what it does. Position orders a lineup's rules, from 1. What the rules do
-- to an entry is kept apart from the administrator's own edits (D47),
-- which win over it, and is written again whenever the rules run: after
-- every refresh, and whenever a rule changes. rule_pinned marks a number
-- that a rule gave, which numbering by the source (D40) leaves alone, as it
-- does a number that the administrator set. No lineup has rules yet, so
-- nothing changes.

CREATE TABLE lineup_rules (
    id        INTEGER PRIMARY KEY,
    lineup_id INTEGER NOT NULL REFERENCES lineups (id) ON DELETE CASCADE,
    position  INTEGER NOT NULL CHECK (position > 0),
    spec      TEXT    NOT NULL CHECK (json_valid(spec)),
    UNIQUE (lineup_id, position)
) STRICT;

ALTER TABLE lineup_entries ADD COLUMN rule_hidden INTEGER CHECK (rule_hidden IN (0, 1)); -- NULL: no rule says
ALTER TABLE lineup_entries ADD COLUMN rule_name TEXT CHECK (rule_name <> '');
ALTER TABLE lineup_entries ADD COLUMN rule_group_id INTEGER REFERENCES lineup_groups (id) ON DELETE SET NULL;
ALTER TABLE lineup_entries ADD COLUMN rule_logo TEXT CHECK (rule_logo <> '');
ALTER TABLE lineup_entries ADD COLUMN rule_pinned INTEGER NOT NULL DEFAULT 0 CHECK (rule_pinned IN (0, 1));

CREATE INDEX lineup_entries_rule_group_id ON lineup_entries (rule_group_id);
