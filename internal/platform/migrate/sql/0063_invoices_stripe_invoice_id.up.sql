-- Phase 3 of the Stripe importer (finalized invoices) needs an idempotency
-- anchor on invoices the same way Phase 0 used customers.external_id and
-- Phase 2 used subscription.code = stripe_subscription_id.
--
-- We can't reuse invoices.invoice_number here. That column is reserved for
-- Velox's own monotonically-increasing sequence (tenants.invoice_prefix +
-- tenants.invoice_next_seq) and carries UNIQUE (tenant_id, invoice_number).
-- An imported Stripe invoice number ("ABCD-0042") would collide with that
-- sequence the first time the operator sends a Velox-native invoice that
-- happens to land on the same generated number.
--
-- So we add a dedicated column. Stripe invoice IDs follow the in_xxx
-- pattern and are globally unique across livemode and testmode, so a
-- UNIQUE index on the column is sufficient — no per-tenant scoping needed.
-- The partial index excludes NULLs so pre-import invoices (which never had
-- a Stripe origin) aren't forced to compete with each other on a
-- single-NULL slot.
--
-- Idempotency strategy mirrors the rest of the importer:
--   1. Try to find an existing invoice with stripe_invoice_id = in_xxx.
--   2. If found, diff fields and emit skip-equivalent or skip-divergent.
--   3. If not found, insert a new invoice with stripe_invoice_id stamped.
--
-- Idempotent reruns hit step 2; the importer never overwrites.

ALTER TABLE invoices ADD COLUMN stripe_invoice_id TEXT;
CREATE UNIQUE INDEX idx_invoices_stripe_invoice_id
    ON invoices (stripe_invoice_id)
    WHERE stripe_invoice_id IS NOT NULL;
