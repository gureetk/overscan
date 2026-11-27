-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 53: what is transcoded, and the profiles that the administrator
-- makes (D147).
--
-- A profile that the administrator makes, beside the three fixed ones
-- (D110, internal/streamprofile), holds a codec, a largest size in lines (0
-- for the source's), a quality, a highest bit rate in kbit/s (0 for none),
-- a frame rate, whether it deinterlaces and makes HDR SDR, and its sound.
-- Accounts', devices', channels' and groups' settings name it, as they name
-- the fixed ones.
--
-- A channel, at its source and so in every lineup, and a lineup's group
-- each say whether their streams are transcoded: NULL as needed, which
-- follows the group and then live TV; 'never'; or the name of the profile
-- that transcodes them always. Live TV's own is a setting (live_transcode).
-- Every channel and group starts as needed, and so does live TV, so nothing
-- changes.

CREATE TABLE transcode_profiles (
    id          INTEGER PRIMARY KEY,
    name        TEXT    NOT NULL UNIQUE CHECK (name <> ''),
    codec       TEXT    NOT NULL CHECK (codec IN ('h264', 'hevc')),
    max_lines   INTEGER NOT NULL CHECK (max_lines IN (0, 576, 720, 1080)),
    quality     TEXT    NOT NULL CHECK (quality IN ('high', 'medium', 'low')),
    max_kbps    INTEGER NOT NULL CHECK (max_kbps = 0 OR max_kbps BETWEEN 500 AND 40000),
    frame_rate  TEXT    NOT NULL CHECK (frame_rate IN ('source', '30')),
    deinterlace INTEGER NOT NULL CHECK (deinterlace IN (0, 1)),
    tone_map    INTEGER NOT NULL CHECK (tone_map IN (0, 1)),
    sound       TEXT    NOT NULL CHECK (sound IN ('aac', 'ac3', 'copy')),
    made_at     TEXT    NOT NULL
) STRICT;

ALTER TABLE channels ADD COLUMN transcode TEXT CHECK (transcode IS NULL OR transcode <> '');

ALTER TABLE lineup_groups ADD COLUMN transcode TEXT CHECK (transcode IS NULL OR transcode <> '');
