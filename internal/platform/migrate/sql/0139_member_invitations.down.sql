DROP TABLE IF EXISTS member_invitations;

-- Restore the owner-only role constraint. Any non-owner rows created
-- while 0139 was live would violate it, so coerce them first — down
-- migrations run pre-launch only (no real multi-member tenants).
UPDATE user_tenants SET role = 'owner' WHERE role NOT IN ('owner');
ALTER TABLE user_tenants DROP CONSTRAINT user_tenants_role_check;
ALTER TABLE user_tenants ADD CONSTRAINT user_tenants_role_check
    CHECK (role IN ('owner'));
ALTER TABLE user_tenants DROP COLUMN IF EXISTS created_at;
