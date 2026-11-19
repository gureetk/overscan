-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 18: a source can be a Jellyfin server (D66), read for its films
-- and series only. Its sealed secret holds the server's address, the API
-- key as the password and, as the username, the user whose libraries boxes
-- see.
--
-- sources is rebuilt, keeping every source's ID and setting, so that its
-- type can be jellyfin. Other tables refer to it, so the rebuild runs with
-- foreign keys off:
-- overscan: foreign keys off

CREATE TABLE sources_new (
    id                    INTEGER PRIMARY KEY,
    name                  TEXT    NOT NULL UNIQUE CHECK (name <> ''),
    type                  TEXT    NOT NULL CHECK (type IN ('m3u', 'xtream', 'jellyfin')),
    host                  TEXT    NOT NULL DEFAULT '', -- where its address points, as the admin shows it
    enabled               INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    max_connections       INTEGER NOT NULL DEFAULT 1 CHECK (max_connections >= 0), -- 0: no limit
    user_agent            TEXT    NOT NULL DEFAULT '', -- '' for Overscan's own
    refresh_seconds       INTEGER NOT NULL DEFAULT 21600 CHECK (refresh_seconds >= 900),
    numbers               TEXT    NOT NULL DEFAULT 'playlist' CHECK (numbers IN ('playlist', 'provider')),
    new_categories_hidden INTEGER NOT NULL DEFAULT 0 CHECK (new_categories_hidden IN (0, 1)),
    refreshed_at          TEXT, -- when the last refresh ended, NULL if none has
    refresh_error         TEXT    NOT NULL DEFAULT '', -- why the last refresh failed, '' if it worked
    created_at            TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    account_status        TEXT    NOT NULL DEFAULT '', -- '' until a server says
    account_expires       TEXT, -- NULL if it does not expire, or is not known
    account_connections   INTEGER NOT NULL DEFAULT 0 CHECK (account_connections >= 0), -- 0: not known
    account_time_zone     TEXT    NOT NULL DEFAULT '' -- the server's IANA time zone, '' if not known
) STRICT;

INSERT INTO sources_new (id, name, type, host, enabled, max_connections, user_agent, refresh_seconds, numbers,
        new_categories_hidden, refreshed_at, refresh_error, created_at, account_status, account_expires,
        account_connections, account_time_zone)
    SELECT id, name, type, host, enabled, max_connections, user_agent, refresh_seconds, numbers,
        new_categories_hidden, refreshed_at, refresh_error, created_at, account_status, account_expires,
        account_connections, account_time_zone
    FROM sources;
DROP TABLE sources;
ALTER TABLE sources_new RENAME TO sources;
