-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 4: sources, the channel catalog and lineup entries.
--
-- Until the admin exists, sources are declared in the configuration file and
-- matched to these rows by name; their addresses stay in the file. Channels
-- keep their IDs across refreshes through an identity key that each kind of
-- source defines. A channel a refresh no longer lists is kept, marked
-- missing, for a grace period, so that it keeps its ID and number if it
-- comes back.

CREATE TABLE sources (
    id              INTEGER PRIMARY KEY,
    name            TEXT    NOT NULL UNIQUE CHECK (name <> ''),
    type            TEXT    NOT NULL CHECK (type IN ('m3u')),
    enabled         INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    max_connections INTEGER NOT NULL DEFAULT 1 CHECK (max_connections >= 0), -- 0: no limit
    refreshed_at    TEXT, -- when the last refresh ended, NULL if none has
    refresh_error   TEXT    NOT NULL DEFAULT '', -- why the last refresh failed, '' if it worked
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
) STRICT;

CREATE TABLE categories (
    id        INTEGER PRIMARY KEY,
    source_id INTEGER NOT NULL REFERENCES sources (id) ON DELETE CASCADE,
    kind      TEXT    NOT NULL CHECK (kind IN ('live')),
    name      TEXT    NOT NULL CHECK (name <> ''),
    position  INTEGER NOT NULL, -- order of first appearance in the source
    UNIQUE (source_id, kind, name)
) STRICT;

CREATE TABLE channels (
    id            INTEGER PRIMARY KEY,
    source_id     INTEGER NOT NULL REFERENCES sources (id) ON DELETE CASCADE,
    identity_key  TEXT    NOT NULL,
    name          TEXT    NOT NULL,
    number        INTEGER NOT NULL DEFAULT 0, -- the source's number, 0 if none
    logo          TEXT    NOT NULL DEFAULT '', -- the source's logo address
    category_id   INTEGER REFERENCES categories (id) ON DELETE SET NULL,
    xmltv_id      TEXT    NOT NULL DEFAULT '',
    stream_url    TEXT    NOT NULL, -- can carry credentials; never sent to devices
    archive_days  INTEGER NOT NULL DEFAULT 0,
    position      INTEGER NOT NULL, -- order in the source
    listed_at     TEXT    NOT NULL, -- when a refresh last listed it
    missing_since TEXT, -- set when a refresh stops listing it
    UNIQUE (source_id, identity_key)
) STRICT;

CREATE INDEX channels_category_id ON channels (category_id);

-- What a lineup shows: one entry per channel, with the number devices see.
-- Merged channels (one entry, several streams) arrive with the hub stage.
CREATE TABLE lineup_entries (
    id         INTEGER PRIMARY KEY,
    lineup_id  INTEGER NOT NULL REFERENCES lineups (id) ON DELETE CASCADE,
    channel_id INTEGER NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    number     INTEGER NOT NULL CHECK (number > 0),
    hidden     INTEGER NOT NULL DEFAULT 0 CHECK (hidden IN (0, 1)),
    UNIQUE (lineup_id, channel_id),
    UNIQUE (lineup_id, number)
) STRICT;

CREATE INDEX lineup_entries_channel_id ON lineup_entries (channel_id);
