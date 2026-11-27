-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 55: HLS for an account's players (D153).
--
-- With output_hls on, players that sign in as the account, with its
-- playlist's token or its login, can ask for its live channels as HLS that
-- the hub cuts from each channel's one stream. Every account starts with it
-- off, so its players get what they got before.

ALTER TABLE accounts ADD COLUMN output_hls INTEGER NOT NULL DEFAULT 0 CHECK (output_hls IN (0, 1));
