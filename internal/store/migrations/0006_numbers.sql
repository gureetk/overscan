-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 6: how each source's channels are numbered (D40).
--
-- numbers is playlist, for the playlist's order, or provider, for the numbers
-- the playlist gives its channels (tvg-chno). Sources numbered before this
-- version used their own numbers where they had any, so they start as
-- provider; a configuration that asks for playlist numbers then numbers their
-- channels again.

ALTER TABLE sources ADD COLUMN numbers TEXT NOT NULL DEFAULT 'provider'
    CHECK (numbers IN ('playlist', 'provider'));
