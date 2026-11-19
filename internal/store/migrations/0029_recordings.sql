-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 29: recordings made on the server (D89). A box schedules a
-- programme from the guide; at its start the server plays the channel,
-- as a device of its own would, and writes the stream to a file in the
-- recordings folder until its end.
CREATE TABLE recordings (
	id INTEGER PRIMARY KEY,
	account_id INTEGER NOT NULL DEFAULT 0, -- the household whose box asked
	device_id INTEGER NOT NULL DEFAULT 0,  -- the box that asked
	entry_id INTEGER NOT NULL,             -- the lineup entry it records
	channel TEXT NOT NULL,                 -- the channel's name, as the box saw it
	title TEXT NOT NULL,                   -- the programme's
	start INTEGER NOT NULL,                -- Unix seconds
	stop INTEGER NOT NULL,
	state TEXT NOT NULL CHECK (state IN ('scheduled', 'recording', 'done', 'failed', 'cancelled')),
	file TEXT NOT NULL DEFAULT '',         -- its file's name in the recordings folder
	size INTEGER NOT NULL DEFAULT 0,       -- bytes
	error TEXT NOT NULL DEFAULT '',        -- why it failed
	created_at INTEGER NOT NULL,
	UNIQUE (entry_id, start)
);
CREATE INDEX recordings_by_state ON recordings (state, start);
