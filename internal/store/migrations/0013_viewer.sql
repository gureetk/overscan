-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 13: the viewer's own lists and settings (D53).
--
-- Favourites belong to an account, so that its boxes and clients share
-- them. What a device has watched, and how Overscan's page looks on it,
-- belong to the device.

CREATE TABLE favourites (
    account_id INTEGER NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    entry_id   INTEGER NOT NULL REFERENCES lineup_entries (id) ON DELETE CASCADE,
    added_at   TEXT    NOT NULL,
    PRIMARY KEY (account_id, entry_id)
) STRICT, WITHOUT ROWID;

CREATE INDEX favourites_by_entry ON favourites (entry_id);

-- The channels a device played last, at most 30 of them.
CREATE TABLE watched (
    device_id  INTEGER NOT NULL REFERENCES devices (id) ON DELETE CASCADE,
    entry_id   INTEGER NOT NULL REFERENCES lineup_entries (id) ON DELETE CASCADE,
    watched_at INTEGER NOT NULL, -- Unix seconds
    PRIMARY KEY (device_id, entry_id)
) STRICT, WITHOUT ROWID;

CREATE INDEX watched_by_entry ON watched (entry_id);

-- The page's settings on a device, as a JSON object that the device API
-- checks.
CREATE TABLE device_settings (
    device_id INTEGER PRIMARY KEY REFERENCES devices (id) ON DELETE CASCADE,
    settings  TEXT NOT NULL CHECK (json_valid(settings))
) STRICT;
