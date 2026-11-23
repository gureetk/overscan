-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 44: what a source brings (D135).
--
-- A source brings every category of its live channels, of its films and of
-- its series, as before, or only the categories that the administrator
-- ticks, each kind on its own. source_categories lists the categories that
-- the reads of a source found, ticked or not, with how many channels or
-- titles each held. A source brings only the ticked categories of a kind
-- while its row for that kind in source_category_choices says so. No source
-- has such a row at first, so every source brings every category, as before.

CREATE TABLE source_categories (
    source_id  INTEGER NOT NULL REFERENCES sources (id) ON DELETE CASCADE,
    kind       TEXT    NOT NULL CHECK (kind IN ('live', 'movie', 'series')),
    -- A live category's name, or the source's own ID for a category of
    -- films or of series; '' for the channels or titles without one.
    source_key TEXT    NOT NULL,
    name       TEXT    NOT NULL,
    position   INTEGER NOT NULL, -- its place in the last read that listed it
    items      INTEGER,          -- the channels or titles it held then; NULL if not known
    ticked     INTEGER NOT NULL CHECK (ticked IN (0, 1)),
    listed_at  TEXT    NOT NULL, -- when a read last listed it
    PRIMARY KEY (source_id, kind, source_key)
) STRICT;

CREATE TABLE source_category_choices (
    source_id   INTEGER NOT NULL REFERENCES sources (id) ON DELETE CASCADE,
    kind        TEXT    NOT NULL CHECK (kind IN ('live', 'movie', 'series')),
    only_ticked INTEGER NOT NULL CHECK (only_ticked IN (0, 1)), -- 0: every category, as before
    tick_new    INTEGER NOT NULL CHECK (tick_new IN (0, 1)),    -- a category that the source adds later starts ticked
    changed_at  TEXT    NOT NULL, -- when the administrator last changed the choice or a tick
    PRIMARY KEY (source_id, kind)
) STRICT;
