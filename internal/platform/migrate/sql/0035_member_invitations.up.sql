-- Member invitation tokens for inviting teammates into a tenant. A row is
-- created when an owner clicks "Invite", consumed by the recipient via a
-- tokenized email link, and kept for audit afterwards.
--
-- No RLS — like the other auth tables, invitations run before tenant
-- context is fully established on the accept path, so RLS would be circular.
-- tenant_id on the row scopes queries at the application layer (TxBypass).
--
-- Token storage mirrors password_reset_tokens: the raw token is emailed
-- once and we only persist sha256(token) so a DB snapshot can't be replayed
-- as an accept-link.
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

-- At most one *pending* invitation per (tenant, email). Accepted or revoked
-- rows are excluded so re-inviting after revoke, or re-inviting an existing
-- member who left, is allowed without cleanup.
CREATE UNIQUE INDEX idx_member_invitations_pending
    ON member_invitations(tenant_id, email)
    WHERE accepted_at IS NULL AND revoked_at IS NULL;
