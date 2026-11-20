-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 33: installable themes (D97).
--
-- A theme is a small file of named values, which Overscan checks before it
-- keeps it here. An account may have one for its boxes, and a profile one
-- of its own; without either, a box shows Overscan's own theme.

-- AUTOINCREMENT, so that a removed theme's ID never names another: a box
-- may still hold its stylesheet's address.
CREATE TABLE themes (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT    NOT NULL CHECK (name <> ''),
    data       TEXT    NOT NULL, -- the theme file, in the form Overscan keeps it
    created_at INTEGER NOT NULL  -- Unix seconds
) STRICT;

CREATE UNIQUE INDEX themes_by_name ON themes (name COLLATE NOCASE);

ALTER TABLE accounts ADD COLUMN theme_id INTEGER REFERENCES themes (id) ON DELETE SET NULL;
ALTER TABLE profiles ADD COLUMN theme_id INTEGER REFERENCES themes (id) ON DELETE SET NULL;
