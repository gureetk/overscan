-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 48: guides of the household's own (D139).
--
-- A guide of its own is an XMLTV guide that the administrator adds, at an
-- http or https address or in a file on the server. Its address is sealed,
-- as a source's is (D46), and host is what the admin shows of it. Each
-- read records the channels that the guide lists, and, as a source's
-- guide does (D51), writes a new generation of programmes a batch at a
-- time, which then replaces the old one in a single step. It keeps the
-- programmes of only the guide's channels that a source's channel uses: a
-- channel whose own source gives it no programme, matched by guide ID or
-- by name. The matches are worked out again as sources and guides change,
-- and kept in channel_guide_matches. No guide is added, so nothing
-- changes.

CREATE TABLE guides (
    id          INTEGER PRIMARY KEY,
    name        TEXT    NOT NULL UNIQUE,
    address     BLOB    NOT NULL, -- sealed: an http or https address, or the absolute path of a file
    host        TEXT    NOT NULL DEFAULT '', -- what the admin shows of the address
    user_agent  TEXT    NOT NULL DEFAULT '', -- '' for Overscan's own
    public_only INTEGER NOT NULL DEFAULT 0 CHECK (public_only IN (0, 1)), -- D121
    enabled     INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    position    INTEGER NOT NULL, -- the order in which guides match, the first first
    added_at    TEXT    NOT NULL,
    generation  INTEGER NOT NULL DEFAULT 0, -- the programmes that devices see
    read_at     TEXT, -- when the last read ended, NULL if none has
    read_from   TEXT    NOT NULL DEFAULT '', -- a hash of the address the last read used, to notice a new one
    read_error  TEXT    NOT NULL DEFAULT '', -- why the last read failed, '' if it worked
    listed      INTEGER NOT NULL DEFAULT 0 CHECK (listed >= 0), -- channels that the last read listed
    channels    INTEGER NOT NULL DEFAULT 0 CHECK (channels >= 0), -- of those, the ones it found programmes for
    programmes  INTEGER NOT NULL DEFAULT 0 CHECK (programmes >= 0)
) STRICT;

-- The channels that a guide lists, by the guide's own ID for them, with
-- their display names, one to a line, and the address of their icon. One
-- that a read no longer lists is kept a while, as a source's channel is.
-- kept says whether the guide's programmes hold it: whether a channel used
-- it when the guide was last read.
CREATE TABLE guide_channels (
    id            INTEGER PRIMARY KEY,
    guide_id      INTEGER NOT NULL REFERENCES guides (id) ON DELETE CASCADE,
    xmltv_id      TEXT    NOT NULL,
    names         TEXT    NOT NULL,
    icon          TEXT    NOT NULL DEFAULT '',
    listed_at     TEXT    NOT NULL,
    missing_since TEXT,
    kept          INTEGER NOT NULL DEFAULT 0 CHECK (kept IN (0, 1)),
    UNIQUE (guide_id, xmltv_id)
) STRICT;

CREATE TABLE guide_programmes (
    id          INTEGER PRIMARY KEY,
    guide_id    INTEGER NOT NULL REFERENCES guides (id) ON DELETE CASCADE,
    generation  INTEGER NOT NULL,
    channel_id  INTEGER NOT NULL REFERENCES guide_channels (id) ON DELETE CASCADE,
    start       INTEGER NOT NULL, -- Unix seconds
    stop        INTEGER NOT NULL CHECK (stop > start),
    title       TEXT    NOT NULL CHECK (title <> ''),
    subtitle    TEXT    NOT NULL DEFAULT '',
    description TEXT    NOT NULL DEFAULT '',
    category    TEXT    NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX guide_programmes_by_channel ON guide_programmes (channel_id, generation, start);
CREATE INDEX guide_programmes_by_guide ON guide_programmes (guide_id, generation);

-- Which guide's channel gives a source's channel its programmes, and
-- whether its name matched rather than its guide ID.
CREATE TABLE channel_guide_matches (
    channel_id       INTEGER PRIMARY KEY REFERENCES channels (id) ON DELETE CASCADE,
    guide_channel_id INTEGER NOT NULL REFERENCES guide_channels (id) ON DELETE CASCADE,
    by_name          INTEGER NOT NULL DEFAULT 0 CHECK (by_name IN (0, 1))
) STRICT;

CREATE INDEX channel_guide_matches_guide_channel ON channel_guide_matches (guide_channel_id);
