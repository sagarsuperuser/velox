ALTER TABLE tenant_settings ALTER COLUMN tax_rate_bp TYPE INT;
ALTER TABLE invoices ALTER COLUMN tax_rate_bp TYPE INT;
ALTER TABLE invoice_line_items ALTER COLUMN tax_rate_bp TYPE INT;
