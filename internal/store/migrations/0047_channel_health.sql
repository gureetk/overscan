-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 47: channel health, from what happens as people watch (D134,
-- D138).
--
-- A channel's row says when its stream last failed to start, how many
-- times in a row since it last played, when it last played, whether the
-- administrator hid it until it plays again, and what its stream held when
-- the hub last read it: the video's codec, picture size, scan, bit depth,
-- frame rate and dynamic range, and the audio's codecs. Overscan never
-- opens a channel to fill it in. No channel has a row yet, so nothing
-- changes until people watch.

CREATE TABLE channel_health (
    channel_id    INTEGER PRIMARY KEY REFERENCES channels (id) ON DELETE CASCADE,
    failed_at     TEXT,
    failures      INTEGER NOT NULL DEFAULT 0 CHECK (failures >= 0),
    played_at     TEXT,
    held          INTEGER NOT NULL DEFAULT 0 CHECK (held IN (0, 1)),
    video         TEXT    NOT NULL DEFAULT '',
    width         INTEGER NOT NULL DEFAULT 0,
    height        INTEGER NOT NULL DEFAULT 0,
    interlaced    INTEGER NOT NULL DEFAULT 0 CHECK (interlaced IN (0, 1)),
    bit_depth     INTEGER NOT NULL DEFAULT 0,
    frame_rate    REAL    NOT NULL DEFAULT 0,
    dynamic_range TEXT    NOT NULL DEFAULT '',
    audio         TEXT    NOT NULL DEFAULT '',
    seen_at       TEXT
) STRICT;

CREATE INDEX channel_health_failed ON channel_health (failed_at) WHERE failures > 0;
