-- login_throttle — the per-(client-IP-prefix × account) failed-login counter
-- (ADR-094). This is the pragmatic-lean first cut of ADR-094's target design; it
-- REPLACES the removed users.locked_until auto-lockout, which was both:
--   * FRAGMENTING — the count split across Redis + an in-process fallback that
--     diverged ~2x during a Redis flap (the two stores never merged); and
--   * WEAPONIZABLE — 5 wrong guesses on an email hard-locked THAT ACCOUNT for
--     15 minutes, so anyone who knew an operator's email could lock them out on
--     demand. That is the on-demand DoS lever OWASP explicitly cautions against.
--
-- Shape, and why each choice:
--   * Postgres, ONE authoritative store. Login already hard-depends on Postgres
--     (it reads users.password_hash), so making the throttle authoritative here
--     adds no new SPOF and there is no second store to diverge from — the
--     fragmentation class is gone by construction.
--   * `subject` is HMAC-SHA256(pepper, ip_prefix|lower(email)), NEVER plaintext.
--     The old Redis key was `velox:login_fail:<PLAINTEXT-EMAIL>` — an operator-PII
--     leak of the same class the audit #485 arc spent effort purging.
--   * Keyed on the ATTACKER dimension: the client IP-prefix (IPv4 /24, IPv6 /64)
--     combined with the target account — never a bare account. So a single source
--     hammering one account is throttled, but a remote party from a DIFFERENT
--     IP-prefix can't lock the owner out — the owner, arriving elsewhere, is
--     untouched. (This rests on TRUST_PROXY being set behind a proxy so the
--     client IP is real; a colocated attacker sharing the owner's prefix is a
--     documented residual — ADR-094.) This is what removes the lockout-as-a-weapon.
--   * One row per subject (PK) with a fixed window that self-resets, so the table
--     is bounded by distinct (IP-prefix × account) pairs, not by attempt volume.
--
-- Platform-level, like users/member_invitations: operators are platform users,
-- not tenant customers, and the check runs PRE-AUTH (no tenant context). No
-- tenant_id column, therefore no RLS fence (the rls-enumeration test fences only
-- tenant_id tables). velox_app is granted by the same default-privileges
-- mechanism that already covers every post-0001 table (verified: member_invitations
-- carries the identical grant set as users without an explicit GRANT).
--
-- DEFERRED to later ADR-094 phases (documented there, not here): the graduated
-- ladder (tarpit → CAPTCHA → step-up/MFA), aggregate credential-stuffing
-- detection, per-account sensing, and the optional Redis read-accelerator.

CREATE TABLE login_throttle (
    subject      text        NOT NULL,   -- hex HMAC-SHA256(pepper, ip_prefix || '|' || lower(trim(email)))
    window_start timestamptz NOT NULL,   -- start of the current fixed window; the row self-resets once it lapses
    failures     integer     NOT NULL,
    PRIMARY KEY (subject)
);

-- Supports the periodic retention sweep (DELETE WHERE window_start < now() - ttl),
-- which reclaims rows for sources not seen again — the table's only growth vector
-- (a distributed attack briefly mints one row per attacker prefix × account).
CREATE INDEX idx_login_throttle_window ON login_throttle (window_start);
