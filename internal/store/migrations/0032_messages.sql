-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 32: messages from the administrator to boxes (D96).
--
-- The administrator sends a message to one box of an account, or to every
-- box of it that runs Overscan's page. Each box gets a row of its own, so
-- the boxes a message goes to are fixed when it is sent; the rows of one
-- message share its batch. A box shows its row once, and says when it was
-- read; a row that the box has not read by its expiry is never shown.

-- AUTOINCREMENT, so that a deleted message's ID never names another: a box
-- may still hold it.
CREATE TABLE messages (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    batch      INTEGER NOT NULL, -- the message as it was sent: the ID of its first row
    account_id INTEGER NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    device_id  INTEGER NOT NULL REFERENCES devices (id) ON DELETE CASCADE,
    every_box  INTEGER NOT NULL DEFAULT 0 CHECK (every_box IN (0, 1)), -- sent to every box, not to this one alone
    text       TEXT    NOT NULL CHECK (text <> ''),
    created_at INTEGER NOT NULL, -- Unix seconds
    expires_at INTEGER NOT NULL, -- Unix seconds
    read_at    INTEGER           -- Unix seconds; NULL until the box has read it
) STRICT;

CREATE INDEX messages_by_device ON messages (device_id, read_at);
CREATE INDEX messages_by_batch ON messages (account_id, batch);
