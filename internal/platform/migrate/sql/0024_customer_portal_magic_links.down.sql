DROP TRIGGER IF EXISTS set_livemode ON customer_portal_magic_links;
DROP POLICY IF EXISTS tenant_isolation ON customer_portal_magic_links;
DROP INDEX IF EXISTS idx_customer_portal_magic_links_expires;
DROP INDEX IF EXISTS idx_customer_portal_magic_links_customer;
DROP TABLE IF EXISTS customer_portal_magic_links;
