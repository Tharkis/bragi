-- Download client options: a category per media type and a path mapping
-- for clients that see their downloads under another path than Bragi.
-- indexers shares the shape so one code path reads both tables.

ALTER TABLE download_clients ADD COLUMN categories TEXT NOT NULL DEFAULT '{}';
ALTER TABLE download_clients ADD COLUMN remote_path TEXT NOT NULL DEFAULT '';
ALTER TABLE download_clients ADD COLUMN local_path TEXT NOT NULL DEFAULT '';

ALTER TABLE indexers ADD COLUMN categories TEXT NOT NULL DEFAULT '{}';
ALTER TABLE indexers ADD COLUMN remote_path TEXT NOT NULL DEFAULT '';
ALTER TABLE indexers ADD COLUMN local_path TEXT NOT NULL DEFAULT '';
