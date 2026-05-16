-- ADR-036 amendment — invoice status 'uncollectible' is the
-- counterpart to the new dunning final_action 'mark_uncollectible'.
-- Stripe-standard semantics: operator (or dunning) acknowledged the
-- receivable is uncollectible; invoice stays in financial reporting
-- but no further collection is attempted. Distinct from 'voided'
-- which annuls the invoice as if it never existed.
--
-- Adds 'uncollectible' to the invoices_status_check CHECK constraint.
-- No backfill — no existing rows can be in this state.
ALTER TABLE invoices DROP CONSTRAINT IF EXISTS invoices_status_check;

ALTER TABLE invoices
  ADD CONSTRAINT invoices_status_check
  CHECK (status = ANY (ARRAY[
    'draft'::text,
    'finalized'::text,
    'paid'::text,
    'voided'::text,
    'uncollectible'::text
  ]));
