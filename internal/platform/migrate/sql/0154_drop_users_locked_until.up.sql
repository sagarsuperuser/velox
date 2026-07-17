-- Drop users.locked_until. It backed the old failed-login account lockout, which
-- was removed in #497 (the auto-setter went) and #498 (the interim throttle went).
-- What remained was a dormant column + a read-check that nothing wrote to — a
-- manual/operator backstop with no wired caller, and a weak one: a lock only gates
-- the login endpoint, never live sessions, so it can't actually stop a compromised
-- account (session revocation + password reset do). Removed to keep the schema
-- honest. Incident response for a compromised operator account = reset password
-- (revokes all sessions) / disable. See ADR-094.
ALTER TABLE users DROP COLUMN locked_until;
