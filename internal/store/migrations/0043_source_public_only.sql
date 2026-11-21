-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 43: sources that may reach public addresses only (D121).
--
-- With public_only set, every request that Overscan makes for the source
-- connects to public addresses only: none on the server's own networks.
-- Every source starts without it, so nothing changes.

ALTER TABLE sources ADD COLUMN public_only INTEGER NOT NULL DEFAULT 0 CHECK (public_only IN (0, 1));
