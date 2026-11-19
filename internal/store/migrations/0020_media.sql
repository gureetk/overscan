-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Schema 20: what the files of films and episodes hold (D68): their
-- format, video, sound tracks and subtitles, as ffprobe read them when one
-- first played. The hub decides from it whether a box plays a file as it
-- is or through ffmpeg. A file that turns out to have changed is read
-- again.
CREATE TABLE media (
    title_id   INTEGER NOT NULL REFERENCES titles (id) ON DELETE CASCADE, -- the film, or the episode's series
    episode_id INTEGER NOT NULL DEFAULT 0, -- the episode, 0 for a film
    info       TEXT    NOT NULL CHECK (json_valid(info)),
    read_at    INTEGER NOT NULL, -- Unix seconds
    PRIMARY KEY (title_id, episode_id)
) STRICT, WITHOUT ROWID;

-- An episode's row goes with the episode.
CREATE TRIGGER media_of_episode AFTER DELETE ON episodes BEGIN
    DELETE FROM media WHERE title_id = OLD.title_id AND episode_id = OLD.id;
END;
