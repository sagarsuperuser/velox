-- Audit rows must be immutable after insert. A BEFORE UPDATE OR DELETE
-- trigger blocks both operations at the DB level so no compromised code
-- path, stray ORM call, or admin tool can silently rewrite or erase
-- evidence. Append-only is a SOC-2 / GDPR expectation and complements the
-- RES-5 fail-closed write path (which ensures rows get written at all).
--
-- Legitimate retention cleanup is a DB-admin operation: temporarily DROP
-- the trigger, delete, recreate. That DDL is itself logged by the
-- Postgres statement log, preserving the audit chain above the row level.
CREATE OR REPLACE FUNCTION audit_log_immutable()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION
        'audit_log is append-only; % is not permitted', TG_OP
        USING HINT = 'To purge rows for retention, drop the trigger temporarily at the DB-admin level.';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER audit_log_immutable_trg
BEFORE UPDATE OR DELETE ON audit_log
FOR EACH ROW EXECUTE FUNCTION audit_log_immutable();
