-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 36: accounts with logins (D102).
--
-- An account may have a login for the outputs that players use: a username,
-- which no other account has whatever its case, and a password kept as an
-- argon2id hash. Its playlist token names it in the addresses of its
-- playlist and guide: the store keeps the token's SHA-256 to find the
-- account by, and the token sealed with the key file, so that the admin can
-- show the addresses again. max_streams caps the streams that its devices
-- and players play at once, 0 for no limit. The outputs that it may use
-- start off. Every account starts without a login, the household's too, so
-- nothing changes until the administrator sets one.

ALTER TABLE accounts ADD COLUMN username TEXT CHECK (username <> '');
CREATE UNIQUE INDEX accounts_by_username ON accounts (username COLLATE NOCASE);
ALTER TABLE accounts ADD COLUMN password_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE accounts ADD COLUMN token_hash TEXT CHECK (token_hash <> '');
CREATE UNIQUE INDEX accounts_by_token ON accounts (token_hash);
ALTER TABLE accounts ADD COLUMN token_sealed BLOB;
ALTER TABLE accounts ADD COLUMN max_streams INTEGER NOT NULL DEFAULT 0 CHECK (max_streams >= 0);
ALTER TABLE accounts ADD COLUMN output_m3u INTEGER NOT NULL DEFAULT 0 CHECK (output_m3u IN (0, 1));
ALTER TABLE accounts ADD COLUMN output_xtream INTEGER NOT NULL DEFAULT 0 CHECK (output_xtream IN (0, 1));
