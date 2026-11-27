-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 54: what is transcoded of films and series (D150).
--
-- A title, a film or a series with its episodes, and a shelf of films or of
-- series each say whether their video is transcoded, as channels and groups
-- do (schema 53): NULL as needed, which follows the shelf and then films and
-- series as a whole; 'never'; or the name of the profile that transcodes
-- them always. Films' and series' own is a setting (vod_transcode). Every
-- title and shelf starts as needed, and so do films and series, so nothing
-- changes.

ALTER TABLE titles ADD COLUMN transcode TEXT CHECK (transcode IS NULL OR transcode <> '');

ALTER TABLE shelf_settings ADD COLUMN transcode TEXT CHECK (transcode IS NULL OR transcode <> '');
