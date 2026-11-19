-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 17: films and series (D63), and how far an account got into them
-- (D65).
--
-- A source's films and series are read when a device first asks for them,
-- not on the source's schedule: its categories and its whole list of each,
-- then again when a device asks and the list is a day old. A title's
-- details, and a series' episodes, are read when a device opens it.
-- Titles and episodes keep their IDs across reads through the source's own
-- IDs for them; one that a source stops listing is kept for a week, marked
-- missing, so that it keeps its ID, and where the household got to in it,
-- if it comes back.

-- What the last read of a source's list of films or of series did. A read
-- that works counts one more generation, which the rows it lists carry.
CREATE TABLE library_reads (
    source_id  INTEGER NOT NULL REFERENCES sources (id) ON DELETE CASCADE,
    kind       TEXT    NOT NULL CHECK (kind IN ('movie', 'series')),
    generation INTEGER NOT NULL DEFAULT 0,
    read_at    TEXT, -- when the last read that worked ended, NULL if none has
    tried_at   TEXT, -- when the last read ended, whether it worked or not
    read_error TEXT    NOT NULL DEFAULT '', -- why the last read failed, '' if it worked
    titles     INTEGER NOT NULL DEFAULT 0 CHECK (titles >= 0), -- what the last read that worked listed
    PRIMARY KEY (source_id, kind)
) STRICT;

CREATE TABLE library_categories (
    id         INTEGER PRIMARY KEY,
    source_id  INTEGER NOT NULL REFERENCES sources (id) ON DELETE CASCADE,
    kind       TEXT    NOT NULL CHECK (kind IN ('movie', 'series')),
    source_key TEXT    NOT NULL, -- the source's ID for the category
    name       TEXT    NOT NULL CHECK (name <> ''),
    position   INTEGER NOT NULL, -- the source's order
    generation INTEGER NOT NULL, -- the read that last listed it
    UNIQUE (source_id, kind, source_key)
) STRICT;

CREATE INDEX library_categories_by_name ON library_categories (kind, name);

CREATE TABLE titles (
    id            INTEGER PRIMARY KEY,
    source_id     INTEGER NOT NULL REFERENCES sources (id) ON DELETE CASCADE,
    kind          TEXT    NOT NULL CHECK (kind IN ('movie', 'series')),
    source_key    TEXT    NOT NULL, -- the source's ID: a film's stream ID, a series' ID
    category_id   INTEGER REFERENCES library_categories (id) ON DELETE SET NULL,
    name          TEXT    NOT NULL CHECK (name <> ''),
    year          INTEGER NOT NULL DEFAULT 0, -- 0 if not known
    rating        INTEGER NOT NULL DEFAULT 0 CHECK (rating BETWEEN 0 AND 100), -- out of 100, 0 if not known
    poster        TEXT    NOT NULL DEFAULT '', -- the source's address of its picture
    container     TEXT    NOT NULL DEFAULT '', -- a film's file extension, such as mkv; '' if not known
    added_at      INTEGER NOT NULL DEFAULT 0, -- when the source added or changed it, Unix seconds; 0 if not known
    summary       TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(summary)), -- what the list says beyond this
    position      INTEGER NOT NULL, -- the source's order
    generation    INTEGER NOT NULL, -- the read that last listed it
    missing_since TEXT, -- set when a read stops listing it
    details       TEXT CHECK (details IS NULL OR json_valid(details)), -- read when a device opens it
    details_at    TEXT, -- when they were read
    UNIQUE (source_id, kind, source_key)
) STRICT;

CREATE INDEX titles_by_category ON titles (category_id, position);
CREATE INDEX titles_by_added ON titles (kind, added_at);

CREATE TABLE episodes (
    id            INTEGER PRIMARY KEY,
    title_id      INTEGER NOT NULL REFERENCES titles (id) ON DELETE CASCADE,
    source_key    TEXT    NOT NULL, -- the source's ID for the episode
    season        INTEGER NOT NULL CHECK (season >= 0),
    number        INTEGER NOT NULL CHECK (number >= 0),
    name          TEXT    NOT NULL DEFAULT '',
    container     TEXT    NOT NULL DEFAULT '',
    duration      INTEGER NOT NULL DEFAULT 0 CHECK (duration >= 0), -- seconds, 0 if not known
    plot          TEXT    NOT NULL DEFAULT '',
    picture       TEXT    NOT NULL DEFAULT '', -- the source's address of a picture from it
    position      INTEGER NOT NULL, -- the source's order
    missing_since TEXT, -- set when the series' details stop listing it
    UNIQUE (title_id, source_key)
) STRICT;

-- How far an account got into a film or an episode (D65). A film's row has
-- episode_id 0. When the account finishes one, its position goes back to 0
-- and finished is set.
CREATE TABLE progress (
    account_id INTEGER NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    title_id   INTEGER NOT NULL REFERENCES titles (id) ON DELETE CASCADE, -- the film, or the episode's series
    episode_id INTEGER NOT NULL DEFAULT 0,
    position   INTEGER NOT NULL CHECK (position >= 0), -- seconds into it
    duration   INTEGER NOT NULL CHECK (duration >= 0), -- seconds, 0 if not known
    finished   INTEGER NOT NULL DEFAULT 0 CHECK (finished IN (0, 1)),
    watched_at INTEGER NOT NULL, -- Unix seconds
    PRIMARY KEY (account_id, title_id, episode_id)
) STRICT, WITHOUT ROWID;

CREATE INDEX progress_by_time ON progress (account_id, watched_at);
