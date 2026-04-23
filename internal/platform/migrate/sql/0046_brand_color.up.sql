-- Tenant brand accent color used on invoice PDFs (company name + accent bar)
-- and, eventually, HTML email headers. Stored as a 7-char hex string like
-- #1f6feb; parsed into RGB at render time. NULL/empty means "use the default
-- neutral palette" so existing tenants render unchanged until they set it.
ALTER TABLE tenant_settings ADD COLUMN brand_color TEXT;
