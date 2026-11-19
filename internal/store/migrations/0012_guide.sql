-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 12: the guide (D51).
--
-- Each source's guide is read now and then, as a whole, into programmes. A
-- read writes a new generation of the source's programmes, a batch at a
-- time, then makes that generation the one devices see and deletes the old
-- one. So a read of a large guide takes little memory and never holds the
-- database for long, and devices see the whole old guide or the whole new
-- one, never a mix.

CREATE TABLE source_guides (
    source_id  INTEGER PRIMARY KEY REFERENCES sources (id) ON DELETE CASCADE,
    listed_url BLOB, -- the guide's address that the source's playlist names (url-tvg), sealed; NULL if none
    generation INTEGER NOT NULL DEFAULT 0, -- the programmes that devices see
    read_at    TEXT, -- when the last read ended, NULL if none has
    read_from  TEXT    NOT NULL DEFAULT '', -- a hash of the address the last read used, to notice a new one
    read_error TEXT    NOT NULL DEFAULT '', -- why the last read failed, '' if it worked
    channels   INTEGER NOT NULL DEFAULT 0 CHECK (channels >= 0), -- channels that it found programmes for
    programmes INTEGER NOT NULL DEFAULT 0 CHECK (programmes >= 0)
) STRICT;

CREATE TABLE programmes (
    id          INTEGER PRIMARY KEY,
    source_id   INTEGER NOT NULL REFERENCES sources (id) ON DELETE CASCADE,
    generation  INTEGER NOT NULL,
    channel_id  INTEGER NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    start       INTEGER NOT NULL, -- Unix seconds
    stop        INTEGER NOT NULL CHECK (stop > start),
    title       TEXT    NOT NULL CHECK (title <> ''),
    subtitle    TEXT    NOT NULL DEFAULT '',
    description TEXT    NOT NULL DEFAULT '',
    category    TEXT    NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX programmes_by_channel ON programmes (channel_id, generation, start);
CREATE INDEX programmes_by_source ON programmes (source_id, generation);
