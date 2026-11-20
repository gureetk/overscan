-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 35: a box records a channel at a time that it sets, from a start
-- to an end, rather than a programme of the guide (D101). Such a recording
-- runs exactly from its start to its stop.
ALTER TABLE recordings ADD COLUMN timed INTEGER NOT NULL DEFAULT 0;
