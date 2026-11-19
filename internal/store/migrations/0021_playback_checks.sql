-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 21: what each box's playback check found (D70). The page plays
-- clips that ffmpeg made and asks whether each plays; the server keeps,
-- for the device, which formats and codecs it found to play or not, and
-- the page's answers as it sent them. A device's profile is its model's
-- with these findings applied; a box never checked takes those of the last
-- box of its model that was.
CREATE TABLE playback_checks (
    device_id  INTEGER PRIMARY KEY REFERENCES devices (id) ON DELETE CASCADE,
    model      TEXT    NOT NULL, -- the device's model when it was checked
    firmware   TEXT    NOT NULL DEFAULT '', -- as the box reported it
    findings   TEXT    NOT NULL CHECK (json_valid(findings)), -- {"audio:dts": false, ...}
    results    TEXT    NOT NULL CHECK (json_valid(results)), -- the page's answers
    checked_at INTEGER NOT NULL -- Unix seconds
) STRICT;

CREATE INDEX playback_checks_by_model ON playback_checks (model, checked_at);
