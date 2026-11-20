-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 34: a box records a channel now, until the end of the programme
-- that the guide shows on it, or until it is stopped (D98). A recording
-- that runs until it is stopped has, as its stop, the latest it may run.
ALTER TABLE recordings ADD COLUMN until_stopped INTEGER NOT NULL DEFAULT 0;
