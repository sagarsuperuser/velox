-- Close the TRUNCATE hole in audit_log's append-only guarantee.
--
-- 0011 added a BEFORE UPDATE OR DELETE FOR EACH ROW trigger, but Postgres
-- never fires row-level triggers on TRUNCATE. The runtime application role
-- (velox_app) holds TRUNCATE (from the original GRANT ALL, never revoked), so
-- `TRUNCATE audit_log` erased the entire tamper-evidence log in one statement —
-- trigger-silent and RLS-bypassing — defeating the same guarantee 0011 exists
-- to provide.
--
-- A statement-level BEFORE TRUNCATE trigger reusing the existing
-- audit_log_immutable() function blocks it at the DB level for every role,
-- consistent with how UPDATE/DELETE are blocked. Legitimate retention purges
-- remain a DB-admin operation (drop the triggers, delete, recreate), exactly as
-- documented in 0011.
CREATE TRIGGER audit_log_immutable_truncate_trg
BEFORE TRUNCATE ON audit_log
FOR EACH STATEMENT EXECUTE FUNCTION audit_log_immutable();
