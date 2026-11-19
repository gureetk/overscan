-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 7: the administrator and their sessions (D44).
--
-- There is one administrator. Their password is kept as an argon2id hash, in
-- the PHC string format. A session is kept as the SHA-256 hash, in hex, of the
-- token in its cookie, together with the CSRF token that its forms and API
-- writes must carry.

CREATE TABLE admin_users (
    id              INTEGER PRIMARY KEY CHECK (id = 1), -- one administrator
    username        TEXT    NOT NULL CHECK (username <> ''),
    password_hash   TEXT    NOT NULL CHECK (password_hash <> ''),
    password_set_at TEXT    NOT NULL,
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
) STRICT;

CREATE TABLE admin_sessions (
    id         INTEGER PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES admin_users (id) ON DELETE CASCADE,
    token_hash TEXT    NOT NULL UNIQUE,
    csrf_token TEXT    NOT NULL,
    address    TEXT    NOT NULL DEFAULT '', -- where the session signed in from
    user_agent TEXT    NOT NULL DEFAULT '',
    created_at TEXT    NOT NULL,
    last_seen  TEXT    NOT NULL
) STRICT;

CREATE INDEX admin_sessions_user_id ON admin_sessions (user_id);
