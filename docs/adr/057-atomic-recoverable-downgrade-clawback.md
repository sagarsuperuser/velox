# ADR-057: Downgrade/removal clawback credit note — created atomically, issued recoverably

**Date:** 2026-06-20
**Status:** Accepted

## Context

Hands-on testing of the subscription RemoveItem flow (2026-06-20) confirmed a money gap the atomic-proration work (ADR-030, ADR-056) had left open on the **credit** side.

On a mid-cycle **downgrade / item-removal / quantity-decrease** against a PAID source invoice, the engine claws back the unused prepayment as a **tax-reversing adjustment credit note** (ADR-048), one per funding invoice (`ClawbackPieces`). On the atomic path, the item change ran inside a coordinator-owned `*sql.Tx` (`atomicRemoveItemWithProration` / `atomicUpdateItemWithProration`), but the clawback credit note was created **and** issued **post-commit, fire-and-forget**:

```
tx: RemoveItemTx + handleItemProration(detail)   // item change + clawback DETAIL, in-tx
tx.Commit()
issueClawbackCreditNote(detail)   // CreateAndIssueAdjustment per piece — POST-COMMIT, error only logged
```

`issueClawbackCreditNote` swallowed the error (`slog.Error "…customer not yet credited, reconcile manually"`). So a `credit_notes` write failure — or a crash in the post-commit window — **removed the item but silently never credited the customer.** No rollback, no retry: money owed *to the customer*, dropped. Empirically confirmed by revoking `INSERT ON credit_notes` and running RemoveItem: it returned `204`, deleted the item, and created **no** credit note.

Asymmetry: the **charge** side (upgrades / qty-increases) was already atomic — the proration *invoice* is created in-tx. Only the **credit** side was exposed. `atomicRemoveItemWithProration`'s docstring ("a credit failure rolls back the delete") was aspirational, not true.

**Constraint.** The clawback's `Issue()` files a **Stripe Tax reversal** (an external network call) and applies the customer-balance credit — it cannot run inside the DB tx (a rollback would orphan a committed reversal). So "just move it in-tx" is impossible for the whole operation. This is the same shape the invoice tax-commit already solved: durable DB state in-tx, the Stripe call post-commit + recoverable (`RetryPendingTaxCommit`).

## Decision

Split the clawback across the transaction boundary — **create in-tx, issue post-commit, recover via a reconciler** — mirroring the tax-commit pattern.

1. **Create the credit note as a DRAFT inside the item-change tx.** `createClawbackDraftsTx` → `creditnote.CreateAdjustmentDraftTx` → `store.CreateUnderInvoiceLockTx` on the coordinator's `*sql.Tx` (no nested Begin/Commit; the per-invoice advisory lock rides the caller's tx). A create failure returns an error, so the deferred rollback **undoes the item change** — the item change and the clawback obligation are all-or-nothing. The draft is marked `issue_pending` (new `credit_notes.issue_pending`, migration 0121).
2. **Issue post-commit.** `issueClawbackDrafts` issues the recorded draft ids after the tx commits. On failure the note stays `status='draft' AND issue_pending=true` — a durable marker, not a silent loss.
3. **Reconcile the *Issue-never-ran* case.** `creditnote.RetryPendingClawbackIssue` (wired into the scheduler tick via the new `ClawbackRetrier` hook, beside `RetryPendingTaxCommit`) re-issues pending drafts cross-tenant (scoped by livemode). The scan requires `status='draft' AND issue_pending`, so it recovers exactly the case where `Issue()` **never ran** (a crash/transient error in the post-commit window before `Issue()` flipped the status). That re-issue is safe by construction — nothing has applied yet, so `Issue()` runs fresh.

`issue_pending` is the discriminator so the reconciler never auto-issues **operator-created** drafts (they default `false`). Migration 0121 is additive (constant `DEFAULT false` → metadata-only on PG 11+) plus a partial index `(tenant_id, livemode) WHERE issue_pending AND status='draft'`.

The **non-atomic fallback** path (`handleItemProration` with `tx == nil`, used when `h.db` isn't wired) is unchanged — it still create+issues inline via `issueClawbackCreditNote`.

### Known gap: the post-flip partial-issue window (NOT closed here)

`Issue()` flips `status` draft→issued in its own committed tx **before** the side-effects (Stripe Tax reversal, balance credit). If a side-effect fails *after* that flip, the row is `status='issued'` — invisible to the reconciler's `status='draft'` scan. It is **not** auto-recovered; it surfaces only via a loud `ERROR` log and needs manual reconciliation. This change does **not** claim to close that window.

Closing it requires making `Issue()` fully re-entrant — the paid-source paths already dedup (tax reversal gated on `tax_transaction_id` + stable key; balance credit deduped by `source_credit_note_id`, migration 0093), but the **unpaid-source `ApplyCreditNote`** (amount_due reduction) is **not** idempotent, so a naive re-issue would double-reduce — plus scanning `issue_pending` regardless of status. That is a **tracked follow-up**, deliberately scoped out to keep the high-blast-radius shared `Issue()` path untouched in this change.

## Consequences

- A clawback **create** failure now rolls the item change back (true atomicity) — the customer is never removed-and-uncredited because a *create* failed. An **Issue-never-ran** failure (the common post-commit-window crash) is recovered automatically by the reconciler. A **post-flip partial issue** is the one residual gap (see above): durable + loudly logged, but manual to reconcile until the tracked follow-up lands.
- One new column (0121), one reconciler, one scheduler hook. No change to the charge path or the non-atomic fallback.
- **Deferred (flagged):** (a) the post-flip re-entrancy gap above; (b) the void/uncollectible `reverseInvoiceTax`, the other post-commit, fire-and-forget member of this class (it `slog.Warn`s on a failed reversal) — both fit the same recoverable shape and are clean fast-follows, scoped out to keep this change reviewable.
- The reconciler issues on wall-clock; for a clock-pinned (test-mode, `livemode=false`) draft this can mis-lane `issued_at` (cosmetic, the 0117 concern) but does not affect the money. Not bound per-draft in v1.

Guarded by the real-Postgres `TestCreateUnderInvoiceLockTx_RollsBackWithCallerTx` (create-atomicity) and the service-level `TestRetryPendingClawbackIssue` (reconciler recovers an Issue-never-ran draft).
