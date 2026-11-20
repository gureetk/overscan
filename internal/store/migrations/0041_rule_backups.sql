-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 41: backups that a lineup's rules give its channels (D113).
--
-- A merge rule finds the copies of a channel that a source lists more than
-- once, by guide ID or by name. It shows the first and hides the others,
-- whose streams the hub tries in turn when the first's cannot start, after
-- the backups picked by hand (D109). The rules work these out again each
-- time they run, as they do the rest of what they do (D107). No lineup has
-- a merge rule yet, so nothing changes.

CREATE TABLE lineup_entry_rule_backups (
    entry_id   INTEGER NOT NULL REFERENCES lineup_entries (id) ON DELETE CASCADE,
    position   INTEGER NOT NULL CHECK (position > 0),
    channel_id INTEGER NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    PRIMARY KEY (entry_id, position),
    UNIQUE (entry_id, channel_id)
) STRICT;

CREATE INDEX lineup_entry_rule_backups_channel_id ON lineup_entry_rule_backups (channel_id);
