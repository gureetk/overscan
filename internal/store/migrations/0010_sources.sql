-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 10: sources live in the database (D46).
--
-- The admin manages sources. A source's address can hold the credentials of
-- an account, so source_secrets keeps it sealed with the key in the key file,
-- and the admin shows only host. A source declared in the configuration file
-- is imported once, by name: config_imports remembers that it was, even after
-- the admin deletes the source.
--
-- sources is rebuilt, keeping every source's ID, for its new settings and so
-- that its type can be xtream later. Categories and channels refer to it, so
-- the rebuild runs with foreign keys off:
-- overscan: foreign keys off

CREATE TABLE sources_new (
    id                    INTEGER PRIMARY KEY,
    name                  TEXT    NOT NULL UNIQUE CHECK (name <> ''),
    type                  TEXT    NOT NULL CHECK (type IN ('m3u', 'xtream')),
    host                  TEXT    NOT NULL DEFAULT '', -- where its address points, as the admin shows it
    enabled               INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    max_connections       INTEGER NOT NULL DEFAULT 1 CHECK (max_connections >= 0), -- 0: no limit
    user_agent            TEXT    NOT NULL DEFAULT '', -- '' for Overscan's own
    refresh_seconds       INTEGER NOT NULL DEFAULT 21600 CHECK (refresh_seconds >= 900),
    numbers               TEXT    NOT NULL DEFAULT 'playlist' CHECK (numbers IN ('playlist', 'provider')),
    new_categories_hidden INTEGER NOT NULL DEFAULT 0 CHECK (new_categories_hidden IN (0, 1)),
    refreshed_at          TEXT, -- when the last refresh ended, NULL if none has
    refresh_error         TEXT    NOT NULL DEFAULT '', -- why the last refresh failed, '' if it worked
    created_at            TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
) STRICT;

INSERT INTO sources_new (id, name, type, enabled, max_connections, numbers, new_categories_hidden,
        refreshed_at, refresh_error, created_at)
    SELECT id, name, type, enabled, max_connections, numbers, new_categories_hidden,
        refreshed_at, refresh_error, created_at
    FROM sources;
DROP TABLE sources;
ALTER TABLE sources_new RENAME TO sources;

CREATE TABLE source_secrets (
    source_id INTEGER PRIMARY KEY REFERENCES sources (id) ON DELETE CASCADE,
    secret    BLOB    NOT NULL -- the address, sealed with the key file's key
) STRICT;

CREATE TABLE config_imports (
    name        TEXT PRIMARY KEY, -- the name of a source in the configuration file
    imported_at TEXT NOT NULL
) STRICT;
