-- Rolling back excludes any 'customer'-tagged rows from the CHECK.
-- The down migration is intentionally lossy: rows already tagged
-- 'customer' would violate the narrower constraint, so we coerce
-- them to 'system' before re-applying the old check. Lossless rollback
-- is impossible without keeping a parallel actor_type column.

UPDATE audit_log SET actor_type = 'system' WHERE actor_type = 'customer';
ALTER TABLE audit_log DROP CONSTRAINT IF EXISTS audit_log_actor_type_check;
ALTER TABLE audit_log
    ADD CONSTRAINT audit_log_actor_type_check
    CHECK (actor_type IN ('api_key', 'user', 'system'));
