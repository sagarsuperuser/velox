-- Recreate the column exactly as 0010 defined it (BOOLEAN NOT NULL DEFAULT
-- FALSE), so a down-migration restores the schema shape 0010 established.
--
-- Restoring the column does NOT restore the behavior: no code reads it (the
-- response swap it controlled was deleted in ADR-089, and the middleware that
-- performed the swap no longer exists). Every row comes back FALSE — which was
-- the effective value for every tenant anyway once the swap was retired.
ALTER TABLE tenant_settings
    ADD COLUMN audit_fail_closed BOOLEAN NOT NULL DEFAULT FALSE;
