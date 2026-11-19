-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 1: the household account and its lineup (D19).
--
-- An account holds what its devices and profiles may use: for now a lineup
-- and an optional expiry. A household starts with one account and one lineup.
-- Times are UTC in RFC 3339 form, such as 2026-11-13T01:23:45Z.

CREATE TABLE lineups (
    id         INTEGER PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE CHECK (name <> ''),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
) STRICT;

CREATE TABLE accounts (
    id         INTEGER PRIMARY KEY,
    name       TEXT    NOT NULL UNIQUE CHECK (name <> ''),
    lineup_id  INTEGER NOT NULL REFERENCES lineups (id),
    expires_at TEXT, -- NULL: never expires
    created_at TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
) STRICT;

CREATE INDEX accounts_lineup_id ON accounts (lineup_id);

INSERT INTO lineups (id, name) VALUES (1, 'Default');
INSERT INTO accounts (id, name, lineup_id) VALUES (1, 'Household', 1);
