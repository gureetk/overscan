-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 15: stream repair (D57).
--
-- A channel's stream can go through ffmpeg, which copies it into a new
-- MPEG-TS stream with its timestamps rebuilt. The switch belongs to the
-- channel, the stream a source gives, so it holds in every lineup that has
-- the channel, and it survives the source's refreshes like the lineup's
-- edits.

ALTER TABLE channels ADD COLUMN repair INTEGER NOT NULL DEFAULT 0 CHECK (repair IN (0, 1));
