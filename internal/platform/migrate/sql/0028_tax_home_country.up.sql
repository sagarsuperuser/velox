-- Tenant home country drives cross-border zero-rating. When set (e.g. 'IN'),
-- invoices to customers whose billing country differs from the tenant's home
-- country are zero-rated as an export (tax_rate_bp = 0, original name
-- preserved with an export marker). Required for Indian GST compliance where
-- exports are zero-rated under LUT; generally correct for VAT/GST regimes
-- that treat cross-border B2B supply as zero-rated or out-of-scope.
--
-- Empty string (default) disables the rule, preserving pre-0028 behavior
-- for tenants that haven't declared a home country yet.
ALTER TABLE tenant_settings
    ADD COLUMN tax_home_country TEXT NOT NULL DEFAULT '';
