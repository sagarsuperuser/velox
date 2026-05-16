-- Reverse the enum expansion. Re-maps mark_uncollectible →
-- write_off_later so the rows pass the older constraint. Rows with
-- final_action='cancel_subscription' have no pre-amendment equivalent;
-- those are remapped to manual_review on revert (safest fallback —
-- operator-attended terminal state).

ALTER TABLE dunning_policies DROP CONSTRAINT IF EXISTS dunning_policies_final_action_check;

UPDATE dunning_policies SET final_action = 'write_off_later' WHERE final_action = 'mark_uncollectible';
UPDATE dunning_policies SET final_action = 'manual_review' WHERE final_action = 'cancel_subscription';

ALTER TABLE dunning_policies
  ADD CONSTRAINT dunning_policies_final_action_check
  CHECK (final_action = ANY (ARRAY[
    'manual_review'::text,
    'pause'::text,
    'write_off_later'::text
  ]));
