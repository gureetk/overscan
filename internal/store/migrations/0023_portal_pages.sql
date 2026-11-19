-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 23: the films and series of Stalker portals (D77). A portal lists
-- them 14 to a page, and reading a whole list would take thousands of
-- requests, so Overscan reads a portal's categories when a device first asks
-- for films or series, and a category's pages as devices browse it. Each of
-- a portal's categories is a shelf of its own, and a title read from one of
-- its pages keeps its place in the category's order as its position; 0 is a
-- title that a page read again no longer lists where it was.

-- How many items the portal last said that one of its categories holds;
-- NULL for another source's category, and for a portal's until a page of it
-- has been read.
ALTER TABLE library_categories ADD COLUMN total INTEGER CHECK (total IS NULL OR total >= 0);

-- When each page of a portal's category was last read.
CREATE TABLE portal_pages (
    category_id INTEGER NOT NULL REFERENCES library_categories (id) ON DELETE CASCADE,
    page        INTEGER NOT NULL CHECK (page >= 1),
    read_at     TEXT    NOT NULL,
    PRIMARY KEY (category_id, page)
) STRICT, WITHOUT ROWID;
