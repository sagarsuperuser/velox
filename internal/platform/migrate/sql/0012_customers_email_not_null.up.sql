-- customers.email is intentionally optional at the API layer (see
-- internal/customer/service.go), but the DB column was nullable which means
-- "missing email" has two on-disk representations: NULL and ''. Two
-- representations for one concept means every WHERE clause and every JOIN
-- has to remember to COALESCE or gets silently different results. Worse,
-- NULL leaks through the encryption layer — an email that was never
-- encrypted (NULL) is indistinguishable from a cleartext empty string
-- once read, complicating GDPR-erase semantics and PII sweeps.
--
-- Collapse to a single representation: NOT NULL DEFAULT ''. Empty string
-- means "no email on file"; that's the same meaning the application layer
-- already assigns (service.go treats TrimSpace(email) == "" as optional
-- and COALESCE(email,'') on every SELECT already erases the NULL/empty
-- distinction on the read path). Deliberately not using the plan-doc's
-- 'unknown-{id}@placeholder.invalid' sentinel: fake addresses risk
-- leaking into outbound sends, Stripe customer metadata, and operator
-- reports, and the audit trail is already preserved by customer.id.
UPDATE customers SET email = '' WHERE email IS NULL;
ALTER TABLE customers ALTER COLUMN email SET DEFAULT '';
ALTER TABLE customers ALTER COLUMN email SET NOT NULL;
