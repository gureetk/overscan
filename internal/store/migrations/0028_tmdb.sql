-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 28: details from TMDB for the titles in folders (D88).

-- Settings that are secrets, sealed as sources' secrets are (D46): TMDB's
-- key, which the administrator gives.
CREATE TABLE sealed_settings (
	key TEXT PRIMARY KEY,
	secret BLOB NOT NULL
);

-- What TMDB says of a folder's film or series, found by its name and year
-- or by the TMDB ID in its name. tmdb_id is 0 when TMDB had no match.
-- details holds what Overscan uses of it, as JSON.
CREATE TABLE tmdb_titles (
	source_id INTEGER NOT NULL REFERENCES sources (id) ON DELETE CASCADE,
	kind TEXT NOT NULL CHECK (kind IN ('movie', 'series')),
	key TEXT NOT NULL,
	tmdb_id INTEGER NOT NULL DEFAULT 0,
	details TEXT NOT NULL DEFAULT '{}',
	checked_at INTEGER NOT NULL,
	PRIMARY KEY (source_id, kind, key)
);
