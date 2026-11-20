-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 38: rules that map a channel's guide and shift its times (D108).
--
-- rule_guide_id is the channel whose programmes a rule shows on an entry in
-- place of its own channel's; rule_guide_shift moves the times of the
-- programmes that the entry shows, in seconds. Both are NULL until a rule
-- says otherwise, so nothing changes.

ALTER TABLE lineup_entries ADD COLUMN rule_guide_id INTEGER REFERENCES channels (id) ON DELETE SET NULL;
ALTER TABLE lineup_entries ADD COLUMN rule_guide_shift INTEGER CHECK (rule_guide_shift <> 0);

CREATE INDEX lineup_entries_rule_guide_id ON lineup_entries (rule_guide_id);
