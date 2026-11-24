-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 49: a channel's guide chosen by hand (D140).
--
-- The administrator can choose where a channel's programmes come from:
-- another channel's guide (use_channel), a channel of a guide of the
-- household's own (guide_channel_id), or none at all (both NULL). The
-- choice belongs to the source's channel, so it holds in every lineup and
-- stays while the channel does, through refreshes and the week that a
-- channel its source stops listing is kept. It wins over the guides' own
-- matches and over rules. A choice whose channel goes, or whose guide's
-- channel does, goes with it, and the channel's guide is found as before.
--
-- A channel whose guide is a guide's channel chosen by hand has its match
-- in channel_guide_matches, marked by_hand, so that the guide keeps that
-- channel's programmes as it keeps those of the channels it matched. No
-- choice is made, so nothing changes.

CREATE TABLE channel_guide_choices (
    channel_id       INTEGER PRIMARY KEY REFERENCES channels (id) ON DELETE CASCADE,
    use_channel      INTEGER REFERENCES channels (id) ON DELETE CASCADE,
    guide_channel_id INTEGER REFERENCES guide_channels (id) ON DELETE CASCADE,
    chosen_at        TEXT    NOT NULL,
    CHECK (use_channel IS NULL OR guide_channel_id IS NULL),
    CHECK (use_channel IS NULL OR use_channel <> channel_id)
) STRICT;

CREATE INDEX channel_guide_choices_by_channel ON channel_guide_choices (use_channel) WHERE use_channel IS NOT NULL;
CREATE INDEX channel_guide_choices_by_guide_channel ON channel_guide_choices (guide_channel_id)
    WHERE guide_channel_id IS NOT NULL;

ALTER TABLE channel_guide_matches ADD COLUMN by_hand INTEGER NOT NULL DEFAULT 0 CHECK (by_hand IN (0, 1));
