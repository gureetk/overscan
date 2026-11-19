-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 2: devices (D23).
--
-- A device is keyed by its MAC address, stored as six upper-case hex pairs
-- separated by colons. A device that contacts Overscan for the first time is
-- pending until the administrator approves it, which links it to an account.
-- A blocked device is refused.

CREATE TABLE devices (
    id         INTEGER PRIMARY KEY,
    mac        TEXT    NOT NULL UNIQUE CHECK (mac GLOB
        '[0-9A-F][0-9A-F]:[0-9A-F][0-9A-F]:[0-9A-F][0-9A-F]:[0-9A-F][0-9A-F]:[0-9A-F][0-9A-F]:[0-9A-F][0-9A-F]'),
    state      TEXT    NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'approved', 'blocked')),
    account_id INTEGER REFERENCES accounts (id),
    label      TEXT    NOT NULL DEFAULT '', -- set by the administrator
    model      TEXT    NOT NULL DEFAULT '', -- as the device reports it
    last_seen  TEXT, -- NULL until the device first contacts Overscan
    created_at TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    CHECK (state <> 'approved' OR account_id IS NOT NULL)
) STRICT;

CREATE INDEX devices_account_id ON devices (account_id);
