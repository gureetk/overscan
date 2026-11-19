-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 11: what an Xtream server says about the account it is read with
-- (D50). The refresher records it after each read. A source's own limit of
-- streams at once is capped by the account's.

ALTER TABLE sources ADD COLUMN account_status TEXT NOT NULL DEFAULT ''; -- '' until a server says
ALTER TABLE sources ADD COLUMN account_expires TEXT; -- NULL if it does not expire, or is not known
ALTER TABLE sources ADD COLUMN account_connections INTEGER NOT NULL DEFAULT 0 CHECK (account_connections >= 0); -- 0: not known
ALTER TABLE sources ADD COLUMN account_time_zone TEXT NOT NULL DEFAULT ''; -- the server's IANA time zone, '' if not known
