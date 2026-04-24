-- Stripe-parity hosted_invoice_url primitive. Generated at Service.Finalize()
-- as a 32-byte hex token (256 bits entropy) with the vlx_pinv_ prefix. Drafts
-- never carry a token; finalized/paid/voided invoices keep theirs for the
-- lifetime of the invoice so email CTAs keep working indefinitely.
--
-- Stored plaintext (not hashed): the token IS the URL — shareable by design.
-- 256-bit entropy makes guessing infeasible; the column value carries no
-- incremental sensitivity over the invoice row itself (which operators
-- already read in plaintext via the authenticated API).
--
-- Partial unique index lets pre-existing finalized invoices remain NULL
-- until a rotate-endpoint call populates them, without a duplicate-NULL
-- constraint failure.
ALTER TABLE invoices ADD COLUMN public_token TEXT;
CREATE UNIQUE INDEX idx_invoices_public_token ON invoices (public_token) WHERE public_token IS NOT NULL;
