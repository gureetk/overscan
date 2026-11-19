-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 9: the channels' stream addresses, encrypted (D46).
--
-- A stream address can hold the credentials of its source's account, so the
-- database keeps it sealed with the key in the key file, which it never
-- holds: stream_secret, made and read by the store. stream_url keeps the
-- addresses that older versions stored as they were, until Overscan seals
-- them when it starts with its key; it is then empty.

ALTER TABLE channels ADD COLUMN stream_secret BLOB;
