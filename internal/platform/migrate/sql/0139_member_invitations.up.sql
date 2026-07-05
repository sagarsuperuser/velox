-- Member invitation tokens for inviting teammates into a tenant (minimal
-- invite, DP-readiness cut-reinstatement #2). A row is created when a
-- member clicks "Send invite", consumed by the recipient via a tokenized
-- email link, and kept for audit afterwards.
--
-- History: an earlier member_invitations (0035) belonged to the cut
-- Members feature and was dropped with the rest of the legacy auth
-- tables in 0068. This is a fresh table against the ADR-011 auth schema
-- (users/user_tenants from 0069).
--
-- No RLS — like the sibling auth tables, the accept path runs before any
-- tenant context exists, so RLS would be circular. tenant_id on the row
-- scopes queries at the application layer (TxBypass).
--
-- Token storage mirrors password_reset_tokens: the raw token is emailed
-- once and only sha256(token) is persisted, so a DB snapshot can't be
-- replayed as an accept link.
CREATE TABLE member_invitations (
    id                  TEXT PRIMARY KEY,
    tenant_id           TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    email               TEXT NOT NULL,
    token_hash          TEXT NOT NULL UNIQUE,
    invited_by_user_id  TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role                TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('owner', 'admin', 'member')),
    expires_at          TIMESTAMPTZ NOT NULL,
    accepted_at         TIMESTAMPTZ,
    revoked_at          TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_member_invitations_tenant ON member_invitations(tenant_id);

-- At most one *pending* invitation per (tenant, email). Accepted or
-- revoked rows are excluded so re-inviting after a revoke, or re-inviting
-- a member who left, works without cleanup.
CREATE UNIQUE INDEX idx_member_invitations_pending
    ON member_invitations(tenant_id, email)
    WHERE accepted_at IS NULL AND revoked_at IS NULL;

-- user_tenants.role was CHECK (role IN ('owner')) from 0069 — the
-- bootstrap flow only ever minted owners. Invited teammates join as
-- 'member' (recorded, NOT enforced: every session still gets the full
-- permission set until role-scoped permissions land — see
-- internal/auth/permission.go). 'admin' reserved for the future split.
ALTER TABLE user_tenants DROP CONSTRAINT user_tenants_role_check;
ALTER TABLE user_tenants ADD CONSTRAINT user_tenants_role_check
    CHECK (role IN ('owner', 'admin', 'member'));

-- Invites create the first users with >1 membership. Login picks
-- tenants[0] (v1 has no tenant switcher), which was nondeterministic
-- without an order column: stamp join time so TenantsForUser can order
-- by it and login deterministically lands on the user's ORIGINAL
-- workspace. Backfill now() — pre-launch, ordering among existing
-- single-membership rows is moot.
ALTER TABLE user_tenants ADD COLUMN created_at TIMESTAMPTZ NOT NULL DEFAULT now();
