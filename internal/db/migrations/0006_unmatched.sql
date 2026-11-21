-- Folders and files a library scan could not match. They stay listed until
-- they are matched by hand, ignored, or disappear from disk. The nightly
-- scan skips them; a scan started by hand tries them again.
CREATE TABLE unmatched (
    id         INTEGER PRIMARY KEY,
    library_id INTEGER NOT NULL REFERENCES libraries (id) ON DELETE CASCADE,
    -- Path relative to the library root: a folder for TV, movies and
    -- audiobooks, a file for ebooks and single-file audiobooks.
    path       TEXT NOT NULL,
    reason     TEXT NOT NULL DEFAULT '',
    -- What the scan read from the path or the file's own tags, to prefill
    -- the search.
    guess      TEXT NOT NULL DEFAULT '',
    ignored    INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    UNIQUE (library_id, path)
);
