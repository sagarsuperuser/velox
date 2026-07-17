-- Down: recreate login_throttle exactly as migration 0152 created it, so rolling
-- back this drop restores the interim throttle's schema. See 0152 and ADR-094 for
-- the full rationale of the (now-removed) design.
CREATE TABLE login_throttle (
    subject      text        NOT NULL,   -- hex HMAC-SHA256(pepper, ip_prefix || '|' || lower(trim(email)))
    window_start timestamptz NOT NULL,   -- start of the current fixed window; the row self-resets once it lapses
    failures     integer     NOT NULL,
    PRIMARY KEY (subject)
);
CREATE INDEX idx_login_throttle_window ON login_throttle (window_start);
