-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 14: the parental PIN (D55).
--
-- An account may have a PIN, kept as an argon2id hash. A lineup's groups
-- and entries may be locked: a locked group locks every channel in it. Overscan's
-- page asks for the PIN before it plays a locked channel.

ALTER TABLE accounts ADD COLUMN pin_hash TEXT; -- NULL for no PIN

ALTER TABLE lineup_groups ADD COLUMN locked INTEGER NOT NULL DEFAULT 0 CHECK (locked IN (0, 1));

ALTER TABLE lineup_entries ADD COLUMN locked INTEGER NOT NULL DEFAULT 0 CHECK (locked IN (0, 1));
