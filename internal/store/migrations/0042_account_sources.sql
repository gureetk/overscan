-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 42: the sources that an account uses (D114).
--
-- An account uses every source, sources added later too, or only those in
-- account_sources once sources_limited is set. Its devices and players
-- then list and play only what comes from those sources. Removing a source
-- takes it off every account's list; an account left with none uses none,
-- until the administrator ticks one. Every account starts with every
-- source, so nothing changes.

ALTER TABLE accounts ADD COLUMN sources_limited INTEGER NOT NULL DEFAULT 0 CHECK (sources_limited IN (0, 1));

CREATE TABLE account_sources (
    account_id INTEGER NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    source_id  INTEGER NOT NULL REFERENCES sources (id) ON DELETE CASCADE,
    PRIMARY KEY (account_id, source_id)
) STRICT, WITHOUT ROWID;

CREATE INDEX account_sources_source_id ON account_sources (source_id);
