-- Reverse the constraint expansion. Any rows with the now-
-- removed value get NULL'd first so the constraint check
-- doesn't reject them on re-add. Test-mode-only data, so
-- losing the typed code is acceptable for a rollback.

UPDATE invoices SET tax_error_code = NULL WHERE tax_error_code = 'provider_not_configured';

ALTER TABLE invoices DROP CONSTRAINT invoices_tax_error_code_check;

ALTER TABLE invoices ADD CONSTRAINT invoices_tax_error_code_check
    CHECK (tax_error_code IS NULL OR tax_error_code IN (
        'customer_data_invalid',
        'jurisdiction_unsupported',
        'provider_outage',
        'provider_auth',
        'unknown'
    ));
