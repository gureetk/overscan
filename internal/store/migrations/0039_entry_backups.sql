-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 39: backup streams for a lineup's channels (D109).
--
-- A lineup entry plays its own channel's stream. Its backups are other
-- channels, of any source, in order, whose streams the hub tries in turn
-- when the entry's own cannot start: its source is at its limit, gives no
-- address for it, or does not send it. No entry has a backup yet, so
-- nothing changes.

CREATE TABLE lineup_entry_backups (
    entry_id   INTEGER NOT NULL REFERENCES lineup_entries (id) ON DELETE CASCADE,
    position   INTEGER NOT NULL CHECK (position > 0),
    channel_id INTEGER NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    PRIMARY KEY (entry_id, position),
    UNIQUE (entry_id, channel_id)
) STRICT;

CREATE INDEX lineup_entry_backups_channel_id ON lineup_entry_backups (channel_id);
