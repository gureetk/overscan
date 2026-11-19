-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 22: a film or an episode keeps the source's path of its file
-- where the source's ID for it is not enough to play it: on a Plex server,
-- the path of the file's part (D72). Other sources leave it ''.

ALTER TABLE titles ADD COLUMN file TEXT NOT NULL DEFAULT '';
ALTER TABLE episodes ADD COLUMN file TEXT NOT NULL DEFAULT '';
