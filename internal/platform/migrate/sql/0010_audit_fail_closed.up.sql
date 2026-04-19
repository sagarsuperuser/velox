-- Per-tenant opt-in to treat audit log write failures as request failures.
-- Default FALSE (fail-open) preserves current behavior. Tenants bound by SOC-2
-- or similar compliance requirements set this TRUE — an audit INSERT failure
-- then causes the middleware to return 503 audit_error instead of flushing
-- the handler response, so the caller never sees a 2xx for an action that
-- wasn't recorded.
ALTER TABLE tenant_settings
    ADD COLUMN audit_fail_closed BOOLEAN NOT NULL DEFAULT FALSE;
