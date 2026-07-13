-- Restore the grants 0001 handed velox_app, so the roundtrip (down -> up) lands
-- back on the pre-0150 privilege set. This re-opens the hole 0150 closed: the
-- append-only triggers become the sole barrier again.
GRANT UPDATE, DELETE, TRUNCATE ON audit_log TO velox_app;
