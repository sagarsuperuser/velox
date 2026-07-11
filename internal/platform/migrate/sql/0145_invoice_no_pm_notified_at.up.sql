-- Send-once marker for the "payment method needed" setup-link email
-- (ADR-087 follow-up). The auto-charge retry sweep visits a no-PM invoice
-- every tick; without a durable marker it either never emails (the pre-fix
-- gap: a sweep-mediated proration invoice aged into overdue with zero
-- customer contact) or would email on every tick. Finalize-time senders
-- stamp it too, so the sweep never duplicates an email the finalize path
-- already sent. NULL = not yet notified.
ALTER TABLE invoices ADD COLUMN no_pm_notified_at TIMESTAMPTZ;
