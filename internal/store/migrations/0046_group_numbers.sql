-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 46: a group's first number (D137).
--
-- numbers_from, when set, numbers the group's channels from it, and gives
-- a channel that joins the group the next free number after the group's
-- last. numbered_in names the group whose numbering gave an entry its
-- number, so that a channel that comes into a group from elsewhere is
-- told apart from one that the group numbered. Every group that exists
-- has no first number, and every channel keeps its number, as before.

ALTER TABLE lineup_groups ADD COLUMN numbers_from INTEGER CHECK (numbers_from BETWEEN 1 AND 999999);

ALTER TABLE lineup_entries ADD COLUMN numbered_in INTEGER REFERENCES lineup_groups (id) ON DELETE SET NULL;
