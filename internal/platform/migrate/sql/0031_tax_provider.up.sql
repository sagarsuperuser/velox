-- tax_provider selects the tenant's tax-calculation backend. Replaces the
-- earlier billing.stripe_tax feature flag with a per-tenant, user-facing
-- configuration choice.
--
-- Allowed values:
--   'none'        — skip tax entirely (zero lines, no provider calls)
--   'manual'      — flat rate from tenant_settings.tax_rate_bp (default)
--   'stripe_tax'  — Stripe Tax API for multi-jurisdiction calculations
ALTER TABLE tenant_settings
  ADD COLUMN tax_provider TEXT NOT NULL DEFAULT 'manual'
  CHECK (tax_provider IN ('none', 'manual', 'stripe_tax'));
