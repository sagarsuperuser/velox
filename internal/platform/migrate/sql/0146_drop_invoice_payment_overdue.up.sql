-- Drop the dead invoices.payment_overdue column. It was created with a
-- DEFAULT false in schema 0001 and never written by any code path — the
-- only reader was the invoice-attention classifier's overdue arm, which
-- therefore could never fire. The live "Past due" signal is computed from
-- due_at at query time (invoice list `?overdue=`), not from this column,
-- so nothing observable changes.
ALTER TABLE invoices DROP COLUMN payment_overdue;
