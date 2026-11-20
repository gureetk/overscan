-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 40: output profiles (D110).
--
-- An account's output profile says how its devices and players get live
-- channels: 'source', as their sources send them, or one that the hub's
-- ffmpeg transcodes to (internal/streamprofile names them, and the store
-- checks the names, so that more can come without rebuilding the tables).
-- A device's own, if it has one, comes before its account's: '' is its
-- account's. Every account starts with 'source' and every device with '',
-- so nothing changes.

ALTER TABLE accounts ADD COLUMN output_profile TEXT NOT NULL DEFAULT 'source';
ALTER TABLE devices ADD COLUMN output_profile TEXT NOT NULL DEFAULT '';
