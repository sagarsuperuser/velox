-- Mark a credit note as an AUTO-ISSUE clawback draft awaiting issuance by the
-- reconciler. Set true when a subscription downgrade / item-removal /
-- qty-decrease clawback creates the credit note IN-TRANSACTION with the item
-- mutation (so the item change + the clawback obligation commit atomically),
-- then issues it post-commit. If that post-commit Issue() fails — the Stripe
-- Tax reversal or the ledger credit errors — the credit note stays
-- status='draft' with issue_pending=true, and RetryPendingClawbackIssue
-- re-issues it. Re-issue is safe: the tax reversal is gated on
-- tax_transaction_id + a stable idempotency key (no double-reverse), and the
-- ledger credit is deduped by source_credit_note_id (migration 0093).
--
-- Pre-fix, the clawback credit note was created+issued POST-COMMIT and
-- fire-and-forget (subscription handler issueClawbackCreditNote): a failure
-- left the item removed but the customer silently un-credited (only an ERROR
-- log "reconcile manually"). This column is the durable marker that lets the
-- reconciler distinguish an auto-clawback draft (re-issue it) from an OPERATOR
-- draft (leave it alone — operators issue/void their own drafts).
--
-- DEFAULT false: operator-created drafts and all existing rows are NOT
-- auto-issued. Only the engine's in-tx clawback create sets it true. The
-- column is NOT cleared on issue — the reconciler scan ANDs status='draft'
-- (below + RetryPendingClawbackIssue), so a successfully-issued CN drops out
-- because its status leaves 'draft', not because the flag clears. (A failed
-- post-CAS Issue — status already flipped to 'issued', side-effect un-applied —
-- is therefore NOT auto-recovered here; it surfaces via a loud ERROR log for
-- manual reconciliation. Auto-recovering that window — re-issue keyed on
-- issue_pending regardless of status + an idempotent ApplyCreditNote — is a
-- tracked follow-up, ADR-057.)
ALTER TABLE credit_notes ADD COLUMN issue_pending BOOLEAN NOT NULL DEFAULT false;

-- Reconciler hot-path: find the small set of un-issued auto-clawback drafts.
CREATE INDEX IF NOT EXISTS idx_credit_notes_issue_pending
    ON credit_notes (tenant_id, livemode)
    WHERE issue_pending AND status = 'draft';
