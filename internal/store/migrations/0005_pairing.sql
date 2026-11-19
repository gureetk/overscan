-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 5: pairing (D39).
--
-- A box that runs the MAG page pairs with a secret, so that a copied MAC
-- address alone is not enough. The box keeps the secret; the store keeps its
-- SHA-256 hash, in hex.
--
-- secret_hash is the secret of a paired device, and paired_at when it was
-- paired: session tokens issued before then no longer count. A request to
-- pair waits in pairing_code, pairing_hash and pairing_at until the
-- administrator approves it: the box shows the code and keeps the secret.

ALTER TABLE devices ADD COLUMN secret_hash TEXT;
ALTER TABLE devices ADD COLUMN paired_at TEXT;
ALTER TABLE devices ADD COLUMN pairing_code TEXT;
ALTER TABLE devices ADD COLUMN pairing_hash TEXT;
ALTER TABLE devices ADD COLUMN pairing_at TEXT;

CREATE UNIQUE INDEX devices_pairing_code ON devices (pairing_code);
