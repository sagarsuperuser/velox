-- Drop login_throttle. The interim per-(client-IP-prefix × account) failed-login
-- counter added in 0152 (#497, ADR-094) is removed: v1 ships NO automatic login
-- brute-force throttle. The old users.locked_until auto-lockout stays removed
-- too; login brute-force protection (a non-weaponizable throttle + MFA + a
-- login-time breached-password check) is documented in ADR-094 and deferred as
-- one unit, to be built to the target design rather than grown from this stub.
-- The idx_login_throttle_window index is dropped implicitly with the table.
DROP TABLE IF EXISTS login_throttle;
