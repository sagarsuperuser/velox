-- Revert: restore the original 'manual_review' default. Existing
-- rows are not touched (they kept whatever they had).

ALTER TABLE dunning_policies ALTER COLUMN final_action SET DEFAULT 'manual_review';
