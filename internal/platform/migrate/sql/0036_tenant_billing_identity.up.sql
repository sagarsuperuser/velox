-- Seller-side fields that appear on every invoice this tenant issues. These
-- are distinct from the buyer-side equivalents on customer/billing_profile
-- (e.g. customer.tax_id is the BUYER's VAT; tenant_settings.tax_id below is
-- the SELLER's). All three are optional — older tenants keep working with
-- NULLs and the invoice renderer just omits the line.
--
-- tax_id           — seller's VAT/EIN/GSTIN/ABN. Legally required on B2B
--                    invoices in EU, UK, AU, IN, and many other jurisdictions.
-- support_url      — link printed in the invoice footer so customers can
--                    reach support without email round-trips.
-- invoice_footer   — default free-form footer text (thank-you copy, legal
--                    boilerplate, remittance instructions). Per-invoice
--                    override via invoices.footer is already supported.
ALTER TABLE tenant_settings
  ADD COLUMN tax_id         TEXT,
  ADD COLUMN support_url    TEXT,
  ADD COLUMN invoice_footer TEXT;
