-- Stripe-Dashboard-style secret key display.
--
-- Stripe's own dashboard shows a secret key as "sk_live_51ab••••••••wxyz" —
-- the key-type prefix (sk_live_ / sk_test_ / rk_live_ / rk_test_) plus a few
-- account-identifying chars, then bullets, then last4. The prefix is how
-- tenants tell "live vs test" and "standard vs restricted" at a glance —
-- suppressing it the way we previously did ("•••• wxyz") forces them to
-- trust our mode label without a visual cross-check against their Stripe
-- dashboard.
--
-- Storing first 12 chars of the plaintext at write time is safe: the leading
-- 8 chars are the public type prefix, the next 4 are not sensitive on their
-- own (Stripe keys require the full remainder to be usable) and they mirror
-- what's shown in the tenant's Stripe dashboard.
ALTER TABLE stripe_provider_credentials
    ADD COLUMN secret_key_prefix TEXT NOT NULL DEFAULT '';
