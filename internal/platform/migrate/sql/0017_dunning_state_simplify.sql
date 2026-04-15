-- Simplify dunning run states from 8 to 4
UPDATE invoice_dunning_runs SET state = 'active' WHERE state IN ('scheduled', 'retry_due', 'awaiting_payment_setup', 'awaiting_retry_result');
UPDATE invoice_dunning_runs SET state = 'escalated' WHERE state = 'exhausted';

-- Simplify resolutions from 5 to 3
UPDATE invoice_dunning_runs SET resolution = 'payment_recovered' WHERE resolution = 'payment_succeeded';
UPDATE invoice_dunning_runs SET resolution = 'manually_resolved' WHERE resolution IN ('operator_resolved', 'invoice_not_collectible');
UPDATE invoice_dunning_runs SET resolution = 'retries_exhausted' WHERE resolution IN ('retries_exhausted', 'subscription_paused');

-- Update event history too
UPDATE invoice_dunning_events SET state = 'active' WHERE state IN ('scheduled', 'retry_due', 'awaiting_payment_setup', 'awaiting_retry_result');
UPDATE invoice_dunning_events SET state = 'escalated' WHERE state = 'exhausted';
