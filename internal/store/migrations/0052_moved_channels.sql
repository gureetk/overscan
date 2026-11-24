-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 52: moving a source's channels to other sources (D144). Once the
-- administrator confirms that another source's channel stands for one of
-- this source's, every lineup's entry of the old channel goes to the new
-- one, with its edits, and the old channel records where it went
-- (moved_to), so that the old source's reads give it no entry again while
-- that channel is there. No channel has moved before, so nothing changes.

ALTER TABLE channels ADD COLUMN moved_to INTEGER REFERENCES channels (id) ON DELETE SET NULL;

CREATE INDEX channels_moved_to ON channels (moved_to);
