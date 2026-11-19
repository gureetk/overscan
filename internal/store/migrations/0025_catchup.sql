-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 25: catch-up (D81, D82). A channel that its source keeps an
-- archive of plays a past programme from it. An Xtream server's archive
-- address is made from the source's login, as its live addresses are; a
-- playlist describes its own with catch-up attributes, which can hold the
-- provider's login, so they are sealed as stream addresses are (D46).

-- How a playlist's channel plays its archive: its mode, catch-up source and
-- correction, as JSON, sealed; NULL for a channel without one.
ALTER TABLE channels ADD COLUMN catchup_secret BLOB;

-- The time zone of the dates and times in the source's catch-up addresses:
-- '' for the server's own, which an Xtream server gives (UTC where it gives
-- none, and for a playlist), or an IANA name such as UTC or Europe/London.
ALTER TABLE sources ADD COLUMN catchup_zone TEXT NOT NULL DEFAULT '';
