-- Recreate the column with its original schema-0001 definition so a
-- rollback restores the exact prior shape (it stays unwritten/always-false).
ALTER TABLE invoices ADD COLUMN payment_overdue BOOLEAN NOT NULL DEFAULT false;
