-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 30: the folder that a recording's file was made in (D89), so
-- that a recording made before recordings_dir changed can still be found
-- and deleted. Empty for one made before this column, in the recordings
-- folder of its time.
ALTER TABLE recordings ADD COLUMN dir TEXT NOT NULL DEFAULT '';
