-- bragi:rebuild
-- An ebook and an audiobook library may share a folder (Grimmory and
-- Booklore keep both in one), so a path is unique per media type rather
-- than overall. SQLite cannot drop a column constraint, so the table is
-- rebuilt; ids are kept, so everything pointing at a library still does.
CREATE TABLE libraries_new (
    id                INTEGER PRIMARY KEY,
    media_type        TEXT NOT NULL CHECK (media_type IN ('tv', 'movie', 'music', 'book', 'audiobook')),
    name              TEXT NOT NULL,
    path              TEXT NOT NULL,
    quality_preset_id INTEGER REFERENCES quality_presets (id) ON DELETE SET NULL,
    created_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    UNIQUE (media_type, path)
);
INSERT INTO libraries_new (id, media_type, name, path, quality_preset_id, created_at)
    SELECT id, media_type, name, path, quality_preset_id, created_at FROM libraries;
DROP TABLE libraries;
ALTER TABLE libraries_new RENAME TO libraries;
