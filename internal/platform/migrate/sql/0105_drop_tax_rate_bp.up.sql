-- Migration 0105: drop tax_rate_bp columns (ADR-043).
--
-- Migration 0104 (ADR-042) added tax_rate NUMERIC(7,4) alongside the
-- legacy tax_rate_bp bigint with the standard "add + backfill + drop
-- later" pattern that production systems use to support staged
-- rollouts. Velox is pre-launch (single deployment, single DB), so
-- the dual-column transition window adds belt-and-suspenders code
-- complexity without the staged-rollout benefit it's meant to
-- protect. Per feedback_no_belt_and_suspenders and
-- feedback_pre_launch_scoping, drop the legacy columns now while
-- there are zero deployments to coordinate.
--
-- After this migration: tax_rate NUMERIC(7,4) is the only rate storage.
-- Code paths that wrote both columns collapse to one. Schema matches
-- industry-grade precision shape (Stripe Tax / Lago / Chargebee /
-- Recurly all use decimal types).

ALTER TABLE invoices            DROP COLUMN tax_rate_bp;
ALTER TABLE invoice_line_items  DROP COLUMN tax_rate_bp;
ALTER TABLE tenant_settings     DROP COLUMN tax_rate_bp;
