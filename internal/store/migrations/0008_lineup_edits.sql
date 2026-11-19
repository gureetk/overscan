-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 8: lineup edits that survive refreshes (D47).
--
-- A lineup has groups of its own. Each category of a source goes to one
-- group of the lineup, found by the category's name, so that a category that
-- goes and comes back finds its group again; the administrator can rename a
-- group, hide it, or put several categories in one. A group that Overscan
-- made for a category goes again when nothing is in it, unless the
-- administrator has changed it.
--
-- An entry can set its channel's name, group and visibility, and the
-- administrator can pin its number: renumbering (D40) never moves a pinned
-- number, and no other channel takes it. An entry whose channel its source
-- stops listing keeps its edits; it gives up its number, unless pinned, only
-- when a listed channel takes that number, and gets one again when its
-- channel comes back. lineup_entries is rebuilt for that, keeping every
-- entry's ID.
--
-- A source can make the categories that it adds later start hidden.

CREATE TABLE lineup_groups (
    id        INTEGER PRIMARY KEY,
    lineup_id INTEGER NOT NULL REFERENCES lineups (id) ON DELETE CASCADE,
    name      TEXT    NOT NULL CHECK (name <> ''),
    hidden    INTEGER NOT NULL DEFAULT 0 CHECK (hidden IN (0, 1)),
    edited    INTEGER NOT NULL DEFAULT 0 CHECK (edited IN (0, 1)), -- made or changed by the administrator
    UNIQUE (lineup_id, name)
) STRICT;

CREATE TABLE lineup_categories (
    lineup_id INTEGER NOT NULL REFERENCES lineups (id) ON DELETE CASCADE,
    source_id INTEGER NOT NULL REFERENCES sources (id) ON DELETE CASCADE,
    category  TEXT    NOT NULL, -- the name of a live category of the source
    group_id  INTEGER NOT NULL REFERENCES lineup_groups (id) ON DELETE CASCADE,
    PRIMARY KEY (lineup_id, source_id, category)
) STRICT;

CREATE INDEX lineup_categories_group_id ON lineup_categories (group_id);

-- A group for each category name, as devices saw them, with the ID that
-- they knew as the genre's: the lowest of its categories' IDs. Lineups after
-- the first, which no version has made, get new IDs.
INSERT INTO lineup_groups (id, lineup_id, name)
    SELECT min(c.id), l.id, c.name
    FROM categories c CROSS JOIN lineups l
    WHERE l.id = (SELECT min(id) FROM lineups)
    GROUP BY l.id, c.name;
INSERT INTO lineup_groups (lineup_id, name)
    SELECT DISTINCT l.id, c.name
    FROM categories c CROSS JOIN lineups l
    WHERE l.id <> (SELECT min(id) FROM lineups);
INSERT INTO lineup_categories (lineup_id, source_id, category, group_id)
    SELECT g.lineup_id, c.source_id, c.name, g.id
    FROM categories c JOIN lineup_groups g ON g.name = c.name;

ALTER TABLE sources ADD COLUMN new_categories_hidden INTEGER NOT NULL DEFAULT 0
    CHECK (new_categories_hidden IN (0, 1));

CREATE TABLE lineup_entries_new (
    id         INTEGER PRIMARY KEY,
    lineup_id  INTEGER NOT NULL REFERENCES lineups (id) ON DELETE CASCADE,
    channel_id INTEGER NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    number     INTEGER CHECK (number > 0), -- NULL while it has none
    pinned     INTEGER NOT NULL DEFAULT 0 CHECK (pinned IN (0, 1)),
    name       TEXT CHECK (name <> ''), -- NULL: the channel's own name
    group_id   INTEGER REFERENCES lineup_groups (id) ON DELETE SET NULL, -- NULL: its category's group
    hidden     INTEGER CHECK (hidden IN (0, 1)), -- NULL: hidden if its group is
    CHECK (pinned = 0 OR number IS NOT NULL),
    UNIQUE (lineup_id, channel_id),
    UNIQUE (lineup_id, number)
) STRICT;

INSERT INTO lineup_entries_new (id, lineup_id, channel_id, number, hidden)
    SELECT id, lineup_id, channel_id, number, CASE WHEN hidden = 1 THEN 1 END
    FROM lineup_entries;
DROP TABLE lineup_entries;
ALTER TABLE lineup_entries_new RENAME TO lineup_entries;

CREATE INDEX lineup_entries_channel_id ON lineup_entries (channel_id);
CREATE INDEX lineup_entries_group_id ON lineup_entries (group_id);
