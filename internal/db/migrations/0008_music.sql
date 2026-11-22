-- Music. An artist is a media_items 'work', an album (a MusicBrainz release
-- group) a 'unit' under it, and a track a 'leaf' under the album. Files
-- belong to tracks. foreign_id holds the MusicBrainz id at every level: the
-- artist, the release group, the recording.

-- Albums: "Album", "EP", "Single", "Live", "Compilation" or "Other".
ALTER TABLE media_items ADD COLUMN album_type TEXT NOT NULL DEFAULT '';
-- Albums: the MusicBrainz release whose track list Bragi uses.
ALTER TABLE media_items ADD COLUMN release_id TEXT NOT NULL DEFAULT '';
-- Tracks: the disc number (1 for single-disc albums).
ALTER TABLE media_items ADD COLUMN disc INTEGER NOT NULL DEFAULT 0;
-- Artists: monitor albums that come out after the artist was added.
ALTER TABLE media_items ADD COLUMN monitor_new INTEGER NOT NULL DEFAULT 0;
CREATE INDEX media_items_foreign ON media_items (media_type, level, foreign_id) WHERE foreign_id IS NOT NULL;

-- Quality presets are for video unless they say otherwise.
ALTER TABLE quality_presets ADD COLUMN media_type TEXT NOT NULL DEFAULT 'video';
INSERT INTO quality_presets (name, description, qualities, max_mb_per_min, builtin, media_type) VALUES
    ('Lossless first', 'FLAC when it can be found, else the best MP3. Keeps looking for FLAC.',
     '["FLAC","MP3-320","MP3-V0","MP3-256","AAC-256"]', 0, 1, 'music'),
    ('MP3 320', 'MP3 at 320 kbps or V0. Smaller files, never lossless.',
     '["MP3-320","MP3-V0","MP3-256"]', 0, 1, 'music');

-- MusicBrainz asks for one request a second, so lookups are kept a while.
CREATE TABLE metadata_cache (
    key        TEXT PRIMARY KEY,
    body       TEXT NOT NULL,
    fetched_at TEXT NOT NULL
);
