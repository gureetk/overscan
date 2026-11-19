-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 26: parental control for films and series, and adult marks
-- (D85, D86). The administrator can lock a shelf of films or of series, or
-- a title, as groups and channels are locked (D55). A source can mark a
-- channel or a title as one for adults; the adult_marks setting says what
-- a mark does: nothing until it is set, lock or hide.

-- A title the administrator locked: once the account has a PIN, devices
-- need it to play the title.
ALTER TABLE titles ADD COLUMN locked INTEGER NOT NULL DEFAULT 0 CHECK (locked IN (0, 1));

-- A shelf the administrator locked, which locks every title on it.
ALTER TABLE shelf_settings ADD COLUMN locked INTEGER NOT NULL DEFAULT 0 CHECK (locked IN (0, 1));

-- What a source marks as for adults, as each read of it says.
ALTER TABLE titles ADD COLUMN adult INTEGER NOT NULL DEFAULT 0 CHECK (adult IN (0, 1));
ALTER TABLE channels ADD COLUMN adult INTEGER NOT NULL DEFAULT 0 CHECK (adult IN (0, 1));
