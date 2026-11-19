-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 27: radio stations (D87). Some sources list radio stations among
-- their channels: an Xtream server's streams of type radio_streams, and a
-- playlist's channels marked radio="true". They stay channels, in the
-- lineup with the rest, and this mark tells devices which they are.
ALTER TABLE channels ADD COLUMN radio INTEGER NOT NULL DEFAULT 0 CHECK (radio IN (0, 1));
