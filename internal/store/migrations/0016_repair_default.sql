-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 16: a default for stream repair (D58).
--
-- One setting for the whole server says whether channels are repaired
-- unless they say otherwise. A channel's own switch can now leave it to
-- that default (NULL), as well as say always (1) or never (0). A channel
-- set to be repaired stays so, and the others follow the default, which
-- starts off, so nothing plays differently after the upgrade.

-- Settings for the whole server, one value per key.
CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) STRICT, WITHOUT ROWID;

ALTER TABLE channels ADD COLUMN repair_own INTEGER CHECK (repair_own IN (0, 1));
UPDATE channels SET repair_own = 1 WHERE repair = 1;
ALTER TABLE channels DROP COLUMN repair;
ALTER TABLE channels RENAME COLUMN repair_own TO repair;
