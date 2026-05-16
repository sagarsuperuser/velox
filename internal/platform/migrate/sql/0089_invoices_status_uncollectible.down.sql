-- Reverse the uncollectible status. Existing 'uncollectible' rows
-- are remapped to 'voided' (safest fallback — both halt collection,
-- voided is the closer pre-amendment analog).
UPDATE invoices SET status = 'voided' WHERE status = 'uncollectible';

ALTER TABLE invoices DROP CONSTRAINT IF EXISTS invoices_status_check;

ALTER TABLE invoices
  ADD CONSTRAINT invoices_status_check
  CHECK (status = ANY (ARRAY[
    'draft'::text,
    'finalized'::text,
    'paid'::text,
    'voided'::text
  ]));
