-- Initial schema. Shared by every media type; see the design doc's
-- "Media types and data model" section.

CREATE TABLE users (
    id               INTEGER PRIMARY KEY,
    username         TEXT NOT NULL UNIQUE COLLATE NOCASE,
    password_hash    TEXT,
    role             TEXT NOT NULL CHECK (role IN ('admin', 'requester')),
    api_key          TEXT NOT NULL UNIQUE,
    jellyfin_user_id TEXT UNIQUE,
    auto_approve     INTEGER NOT NULL DEFAULT 0,
    created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE TABLE sessions (
    token_hash TEXT PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);
CREATE INDEX sessions_user ON sessions (user_id);

CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE quality_presets (
    id             INTEGER PRIMARY KEY,
    name           TEXT NOT NULL UNIQUE,
    description    TEXT NOT NULL DEFAULT '',
    -- JSON array of quality names, best first, e.g. ["WEBDL-1080p","Bluray-1080p"]
    qualities      TEXT NOT NULL,
    -- size cap in MB per minute of runtime; 0 means no cap
    max_mb_per_min REAL NOT NULL DEFAULT 0,
    builtin        INTEGER NOT NULL DEFAULT 0
);

INSERT INTO quality_presets (name, description, qualities, max_mb_per_min, builtin) VALUES
    ('Best up to 1080p', 'Best available release, never above 1080p.',
     '["Bluray-1080p","WEBDL-1080p","WEBRip-1080p","HDTV-1080p","Bluray-720p","WEBDL-720p","WEBRip-720p","HDTV-720p"]', 100, 1),
    ('Up to 4K', 'Prefers 2160p, falls back to 1080p.',
     '["Bluray-2160p","WEBDL-2160p","WEBRip-2160p","Bluray-1080p","WEBDL-1080p","WEBRip-1080p","HDTV-1080p"]', 400, 1),
    ('Smallest 720p+', 'Smallest file that is at least 720p.',
     '["WEBDL-720p","WEBRip-720p","HDTV-720p","WEBDL-1080p","WEBRip-1080p","HDTV-1080p"]', 25, 1);

CREATE TABLE libraries (
    id                INTEGER PRIMARY KEY,
    media_type        TEXT NOT NULL CHECK (media_type IN ('tv', 'movie', 'music', 'book', 'audiobook')),
    name              TEXT NOT NULL,
    path              TEXT NOT NULL UNIQUE,
    quality_preset_id INTEGER REFERENCES quality_presets (id) ON DELETE SET NULL,
    created_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

-- Indexers and download clients share one shape so one code path handles both.
CREATE TABLE indexers (
    id         INTEGER PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    kind       TEXT NOT NULL,
    base_url   TEXT NOT NULL,
    api_key    TEXT NOT NULL DEFAULT '',
    username   TEXT NOT NULL DEFAULT '',
    password   TEXT NOT NULL DEFAULT '',
    category   TEXT NOT NULL DEFAULT '',
    enabled    INTEGER NOT NULL DEFAULT 1,
    priority   INTEGER NOT NULL DEFAULT 25,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE TABLE download_clients (
    id         INTEGER PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    kind       TEXT NOT NULL,
    base_url   TEXT NOT NULL,
    api_key    TEXT NOT NULL DEFAULT '',
    username   TEXT NOT NULL DEFAULT '',
    password   TEXT NOT NULL DEFAULT '',
    category   TEXT NOT NULL DEFAULT '',
    enabled    INTEGER NOT NULL DEFAULT 1,
    priority   INTEGER NOT NULL DEFAULT 25,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

-- Any node in a media tree: a Work (series), a Unit (season) or a Leaf (episode).
CREATE TABLE media_items (
    id          INTEGER PRIMARY KEY,
    media_type  TEXT NOT NULL CHECK (media_type IN ('tv', 'movie', 'music', 'book', 'audiobook')),
    parent_id   INTEGER REFERENCES media_items (id) ON DELETE CASCADE,
    level       TEXT NOT NULL CHECK (level IN ('work', 'unit', 'leaf')),
    library_id  INTEGER REFERENCES libraries (id) ON DELETE SET NULL,
    title       TEXT NOT NULL,
    year        INTEGER,
    number      INTEGER,
    air_date    TEXT,
    tmdb_id     INTEGER,
    tvdb_id     INTEGER,
    imdb_id     TEXT,
    monitored   INTEGER NOT NULL DEFAULT 1,
    status      TEXT NOT NULL DEFAULT 'wanted' CHECK (status IN
                ('requested', 'wanted', 'searching', 'downloading', 'imported', 'failed', 'upgrade_wanted')),
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);
CREATE INDEX media_items_parent ON media_items (parent_id);
CREATE INDEX media_items_status ON media_items (status);
CREATE UNIQUE INDEX media_items_tmdb_work ON media_items (media_type, tmdb_id) WHERE level = 'work';

CREATE TABLE files (
    id            INTEGER PRIMARY KEY,
    media_item_id INTEGER NOT NULL REFERENCES media_items (id) ON DELETE CASCADE,
    path          TEXT NOT NULL UNIQUE,
    size_bytes    INTEGER NOT NULL,
    quality       TEXT NOT NULL DEFAULT '',
    release_title TEXT NOT NULL DEFAULT '',
    imported_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE TABLE requests (
    id            INTEGER PRIMARY KEY,
    user_id       INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    media_type    TEXT NOT NULL,
    tmdb_id       INTEGER NOT NULL,
    title         TEXT NOT NULL,
    -- JSON, e.g. {"seasons":"all"} or {"seasons":[1,2]}
    scope         TEXT NOT NULL DEFAULT '{}',
    status        TEXT NOT NULL DEFAULT 'pending' CHECK (status IN
                  ('pending', 'approved', 'denied', 'available', 'failed')),
    reason        TEXT NOT NULL DEFAULT '',
    media_item_id INTEGER REFERENCES media_items (id) ON DELETE SET NULL,
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    updated_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);
CREATE INDEX requests_user ON requests (user_id);

CREATE TABLE downloads (
    id                 INTEGER PRIMARY KEY,
    media_item_id      INTEGER REFERENCES media_items (id) ON DELETE SET NULL,
    download_client_id INTEGER REFERENCES download_clients (id) ON DELETE SET NULL,
    client_job_id      TEXT NOT NULL DEFAULT '',
    release_title      TEXT NOT NULL,
    protocol           TEXT NOT NULL,
    indexer            TEXT NOT NULL DEFAULT '',
    size_bytes         INTEGER NOT NULL DEFAULT 0,
    status             TEXT NOT NULL DEFAULT 'queued' CHECK (status IN
                       ('queued', 'downloading', 'completed', 'importing', 'imported', 'failed')),
    error              TEXT NOT NULL DEFAULT '',
    created_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    updated_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE TABLE blocklist (
    id            INTEGER PRIMARY KEY,
    media_item_id INTEGER REFERENCES media_items (id) ON DELETE CASCADE,
    release_title TEXT NOT NULL,
    reason        TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

-- Background job queue. Survives restarts without Redis.
CREATE TABLE jobs (
    id           INTEGER PRIMARY KEY,
    kind         TEXT NOT NULL,
    payload      TEXT NOT NULL DEFAULT '{}',
    run_at       TEXT NOT NULL,
    attempts     INTEGER NOT NULL DEFAULT 0,
    last_error   TEXT NOT NULL DEFAULT '',
    locked_until TEXT,
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);
CREATE INDEX jobs_run_at ON jobs (run_at);
