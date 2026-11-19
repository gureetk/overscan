-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 24: what the administrator sets of the films and series that
-- devices see (D79). A shelf can be hidden, renamed, and put in a place of
-- its own among the others; a title can be hidden. Settings outlast the
-- reads of their sources, as the lineup's do.

-- A title the administrator hid: devices do not see it.
ALTER TABLE titles ADD COLUMN hidden INTEGER NOT NULL DEFAULT 0 CHECK (hidden IN (0, 1));

-- What the administrator set of a shelf of films or of series. A shelf that
-- merges the categories of the same name from every source is name:<its
-- name>; a Stalker portal's category, a shelf of its own (D77), is
-- category:<its ID>.
CREATE TABLE shelf_settings (
    kind     TEXT    NOT NULL CHECK (kind IN ('movie', 'series')),
    shelf    TEXT    NOT NULL CHECK (shelf LIKE 'name:_%' OR shelf LIKE 'category:_%'),
    hidden   INTEGER NOT NULL DEFAULT 0 CHECK (hidden IN (0, 1)),
    name     TEXT    CHECK (name IS NULL OR name <> ''), -- the name devices see; NULL for the category's own
    position INTEGER CHECK (position IS NULL OR position >= 1), -- its place among the shelves; NULL for the sources' order
    PRIMARY KEY (kind, shelf)
) STRICT, WITHOUT ROWID;
