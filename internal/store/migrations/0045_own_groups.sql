-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 45: lineups of the household's own groups (D136).
--
-- A lineup with own_groups set holds only the channels of the categories
-- that its groups take, from any source, and those moved into a group by
-- hand: a source's refresh adds no group for a category of its own. Such a
-- lineup starts empty. Every lineup that exists takes every category, as
-- before.

ALTER TABLE lineups ADD COLUMN own_groups INTEGER NOT NULL DEFAULT 0 CHECK (own_groups IN (0, 1));
