-- Revert to ENABLE-only (the 0032 state): owner connections bypass RLS again.
ALTER TABLE stripe_provider_credentials NO FORCE ROW LEVEL SECURITY;
