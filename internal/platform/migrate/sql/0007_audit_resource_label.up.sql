-- Add resource_label to audit_log for human-readable display
ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS resource_label TEXT DEFAULT '';
