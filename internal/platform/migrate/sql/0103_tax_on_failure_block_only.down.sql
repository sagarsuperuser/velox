-- Reverse migration 0103 — restore the (block, fallback_manual) CHECK.
-- The post-revert state leaves existing rows at 'block'; operators who
-- want fallback_manual back must UPDATE the rows themselves.

ALTER TABLE tenant_settings
    DROP CONSTRAINT IF EXISTS tenant_settings_tax_on_failure_check;

ALTER TABLE tenant_settings
    ADD CONSTRAINT tenant_settings_tax_on_failure_check
        CHECK (tax_on_failure IN ('block', 'fallback_manual'));
