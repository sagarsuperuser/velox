-- Backfill missing names on legacy dunning_policies rows (ADR-036
-- followup). Pre-ADR-036 the policy was a singleton-per-tenant and
-- the name column was often left empty by recipe-instantiation /
-- bootstrap paths — operators never had a UI to set it. The
-- campaigns-model assignment dropdown now renders the policy name,
-- so empty names fall through to the underlying value (the policy
-- id) and operators see a vlx_dpol_... string in the trigger label.
--
-- This is a one-time data fix; no schema change. Future inserts go
-- through the policy CRUD endpoints which require a name.
UPDATE dunning_policies
   SET name = 'Default',
       updated_at = now()
 WHERE name IS NULL OR name = '';
