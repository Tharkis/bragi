-- TV slice: metadata, search scheduling and download tracking.

ALTER TABLE media_items ADD COLUMN overview TEXT NOT NULL DEFAULT '';
ALTER TABLE media_items ADD COLUMN poster_path TEXT NOT NULL DEFAULT '';
-- Minutes per episode, used for the quality preset's size cap.
ALTER TABLE media_items ADD COLUMN runtime INTEGER NOT NULL DEFAULT 0;
-- Folder of a series inside its library, e.g. "Severance (2022)".
ALTER TABLE media_items ADD COLUMN folder TEXT NOT NULL DEFAULT '';
-- When the scheduler should next search for this item. NULL means not queued.
ALTER TABLE media_items ADD COLUMN next_search_at TEXT;
-- Plain-words result of the last search, e.g. "No releases found".
ALTER TABLE media_items ADD COLUMN search_note TEXT NOT NULL DEFAULT '';
ALTER TABLE media_items ADD COLUMN refreshed_at TEXT;
CREATE INDEX media_items_next_search ON media_items (next_search_at) WHERE next_search_at IS NOT NULL;

ALTER TABLE downloads ADD COLUMN progress REAL NOT NULL DEFAULT 0;
ALTER TABLE downloads ADD COLUMN output_path TEXT NOT NULL DEFAULT '';
ALTER TABLE downloads ADD COLUMN quality TEXT NOT NULL DEFAULT '';
CREATE INDEX downloads_status ON downloads (status);

-- Shown on request cards before the show is in the library.
ALTER TABLE requests ADD COLUMN year INTEGER NOT NULL DEFAULT 0;
ALTER TABLE requests ADD COLUMN poster_path TEXT NOT NULL DEFAULT '';
