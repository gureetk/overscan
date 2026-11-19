-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 3: Stalker API sessions and the capture log (D13).

-- The token a device got from its last handshake. A new handshake replaces
-- it. Only a SHA-256 hash of the token is kept.
CREATE TABLE stalker_tokens (
    device_id  INTEGER PRIMARY KEY REFERENCES devices (id) ON DELETE CASCADE,
    token_hash TEXT    NOT NULL UNIQUE,
    issued_at  TEXT    NOT NULL
) STRICT;

-- Requests Overscan does not implement, one row per module, action and set
-- of parameter names, with a redacted example and a count.
CREATE TABLE capture_log (
    id         INTEGER PRIMARY KEY,
    module     TEXT    NOT NULL, -- the request's type parameter
    action     TEXT    NOT NULL,
    params     TEXT    NOT NULL, -- parameter names, sorted, comma-separated
    example    TEXT    NOT NULL, -- the last such request's query, redacted
    user_agent TEXT    NOT NULL,
    device_id  INTEGER REFERENCES devices (id) ON DELETE SET NULL,
    count      INTEGER NOT NULL DEFAULT 1,
    first_seen TEXT    NOT NULL,
    last_seen  TEXT    NOT NULL,
    UNIQUE (module, action, params)
) STRICT;

CREATE INDEX capture_log_device_id ON capture_log (device_id);
