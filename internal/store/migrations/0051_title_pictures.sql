-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 51: pictures from TMDB for the films and series of Xtream servers
-- and Stalker portals (D143). Where the administrator turns them on, a
-- title whose own picture is missing, does not load, or is one that many
-- of its source's titles share gets TMDB's poster (poster_fill), and its
-- wide picture where it has none of its own (backdrop_fill). Both stay
-- empty until then, so an upgrade changes no picture. A picture of a
-- source that did not load for a device, or for the check that looks at
-- it again a day later, is recorded in picture_checks until it loads.

ALTER TABLE titles ADD COLUMN poster_fill TEXT NOT NULL DEFAULT '';

ALTER TABLE titles ADD COLUMN backdrop_fill TEXT NOT NULL DEFAULT '';

CREATE INDEX titles_by_poster ON titles (source_id, kind, poster);

CREATE TABLE picture_checks (
    source_id  INTEGER NOT NULL REFERENCES sources (id) ON DELETE CASCADE,
    address    TEXT    NOT NULL, -- the source's address of a title's picture, which did not load
    checked_at TEXT    NOT NULL,
    PRIMARY KEY (source_id, address)
) STRICT, WITHOUT ROWID;
