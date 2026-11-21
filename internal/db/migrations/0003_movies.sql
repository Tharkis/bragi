-- Movies: a movie is a single 'work' row in media_items with its file
-- attached directly. air_date holds the date Bragi starts searching (the
-- digital or physical release, else the theatrical one); in_cinemas is the
-- theatrical date, shown in the UI.
ALTER TABLE media_items ADD COLUMN in_cinemas TEXT;
