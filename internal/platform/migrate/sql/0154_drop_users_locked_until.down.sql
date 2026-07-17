-- Down: re-add locked_until exactly as migration 0069 created it (nullable
-- timestamptz, no default). Restores the column shape; nothing writes to it.
ALTER TABLE users ADD COLUMN locked_until TIMESTAMPTZ;
