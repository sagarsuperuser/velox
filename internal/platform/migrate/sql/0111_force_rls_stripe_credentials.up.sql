-- 0111: FORCE ROW LEVEL SECURITY on stripe_provider_credentials.
--
-- 0032 enabled RLS + a tenant_isolation policy on this table but, unlike
-- every other tenant-scoped table (the 0006 pattern), never added FORCE. A
-- table with ENABLE but not FORCE still exempts the table OWNER from RLS, so a
-- deployment whose DATABASE_URL points at the owning role (the default on
-- self-managed Postgres) silently bypasses tenant isolation on exactly this
-- table — the one holding each tenant's encrypted Stripe secret keys, the most
-- sensitive rows in the schema. Bring it in line with the rest of the schema.
ALTER TABLE stripe_provider_credentials FORCE ROW LEVEL SECURITY;
