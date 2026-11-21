-- Books and audiobooks. A book is a media_items row at level 'work' with no
-- children; its metadata comes from Open Library, not TMDB.

-- Open Library work key, e.g. "OL27448W".
ALTER TABLE media_items ADD COLUMN foreign_id TEXT;
ALTER TABLE media_items ADD COLUMN author TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX media_items_foreign_work ON media_items (media_type, foreign_id)
    WHERE level = 'work' AND foreign_id IS NOT NULL;

-- Requests for books name the Open Library work instead of a TMDB id
-- (tmdb_id is 0 for them).
ALTER TABLE requests ADD COLUMN foreign_id TEXT NOT NULL DEFAULT '';
ALTER TABLE requests ADD COLUMN author TEXT NOT NULL DEFAULT '';
