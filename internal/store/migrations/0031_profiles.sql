-- SPDX-License-Identifier: AGPL-3.0-or-later
-- overscan: foreign keys off

-- Schema 31: profiles (D94).
--
-- An account's profiles are who watches on its devices. Each keeps its own
-- favourites and how far it got into films and episodes. An account's
-- first profile, named after the account, takes what the account kept
-- until now, so a household that adds no profile sees no change. A device
-- plays as its profile, or as its account's first profile while it has
-- none.

-- AUTOINCREMENT, so that a removed profile's ID never names another: a box
-- may still hold it.
CREATE TABLE profiles (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id INTEGER NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    name       TEXT    NOT NULL CHECK (name <> ''),
    pin_hash   TEXT    NOT NULL DEFAULT '', -- argon2id, as the parental PIN's (D55); '' when switching to it asks for none
    created_at TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
) STRICT;

CREATE UNIQUE INDEX profiles_by_name ON profiles (account_id, name COLLATE NOCASE);

INSERT INTO profiles (account_id, name) SELECT id, name FROM accounts ORDER BY id;

-- Every account has a profile from the start.
CREATE TRIGGER accounts_first_profile AFTER INSERT ON accounts
BEGIN
    INSERT INTO profiles (account_id, name) VALUES (NEW.id, NEW.name);
END;

CREATE TABLE favourites_new (
    profile_id INTEGER NOT NULL REFERENCES profiles (id) ON DELETE CASCADE,
    entry_id   INTEGER NOT NULL REFERENCES lineup_entries (id) ON DELETE CASCADE,
    added_at   TEXT    NOT NULL,
    PRIMARY KEY (profile_id, entry_id)
) STRICT, WITHOUT ROWID;

INSERT INTO favourites_new (profile_id, entry_id, added_at)
    SELECT (SELECT min(p.id) FROM profiles p WHERE p.account_id = f.account_id), f.entry_id, f.added_at
    FROM favourites f;
DROP TABLE favourites;
ALTER TABLE favourites_new RENAME TO favourites;
CREATE INDEX favourites_by_entry ON favourites (entry_id);

-- How far a profile got into a film or an episode (D65, D94). A film's row
-- has episode_id 0. When the profile finishes one, its position goes back
-- to 0 and finished is set.
CREATE TABLE progress_new (
    profile_id INTEGER NOT NULL REFERENCES profiles (id) ON DELETE CASCADE,
    title_id   INTEGER NOT NULL REFERENCES titles (id) ON DELETE CASCADE, -- the film, or the episode's series
    episode_id INTEGER NOT NULL DEFAULT 0,
    position   INTEGER NOT NULL CHECK (position >= 0), -- seconds into it
    duration   INTEGER NOT NULL CHECK (duration >= 0), -- seconds, 0 if not known
    finished   INTEGER NOT NULL DEFAULT 0 CHECK (finished IN (0, 1)),
    watched_at INTEGER NOT NULL, -- Unix seconds
    PRIMARY KEY (profile_id, title_id, episode_id)
) STRICT, WITHOUT ROWID;

INSERT INTO progress_new (profile_id, title_id, episode_id, position, duration, finished, watched_at)
    SELECT (SELECT min(p.id) FROM profiles p WHERE p.account_id = g.account_id),
        g.title_id, g.episode_id, g.position, g.duration, g.finished, g.watched_at
    FROM progress g;
DROP TABLE progress;
ALTER TABLE progress_new RENAME TO progress;
CREATE INDEX progress_by_time ON progress (profile_id, watched_at);

-- The profile a device plays as; NULL for its account's first.
ALTER TABLE devices ADD COLUMN profile_id INTEGER REFERENCES profiles (id) ON DELETE SET NULL;
