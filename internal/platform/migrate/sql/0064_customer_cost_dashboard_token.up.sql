-- Cost-dashboard tokens turn on a future embeddable customer-facing
-- usage view (similar in spirit to Stripe's hosted_invoice_url, but
-- bound to a customer rather than to one invoice). The token in the
-- URL is the sole credential: no API key, no session cookie. Operators
-- mint a token explicitly, hand the resulting URL to their customer
-- (typically rendered as an iframe inside the operator's own product),
-- and rotate when they need to invalidate.
--
-- Why a per-customer column rather than a separate table?
--   - Exactly one live token per customer at any time. A side table
--     would need its own UNIQUE (customer_id) anyway.
--   - Rotation is the only mutation; no history is kept (mirrors how
--     invoices.public_token is treated).
--   - Lookup is cross-tenant under TxBypass — adding a join would
--     buy nothing.
--
-- The token format is vlx_pcd_<64 hex chars> (256 bits of entropy,
-- random — see internal/customer/cost_dashboard_token.go). The partial
-- UNIQUE index excludes NULLs so customers without a minted token
-- don't compete for a single-NULL slot.
ALTER TABLE customers ADD COLUMN cost_dashboard_token TEXT;
CREATE UNIQUE INDEX idx_customers_cost_dashboard_token
    ON customers (cost_dashboard_token)
    WHERE cost_dashboard_token IS NOT NULL;
