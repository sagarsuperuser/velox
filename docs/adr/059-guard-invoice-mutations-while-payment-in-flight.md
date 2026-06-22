# ADR-059: Guard invoice mutations while a payment is in flight

**Date:** 2026-06-22
**Status:** Accepted

## Context

An end-to-end audit asked: *which paths mutate an invoice while its payment is "in flight"?* — where **in flight** = `payment_status ∈ {processing, unknown}`: the charge is open at the provider (processing — e.g. off-session SCA `requires_action`) or its outcome is genuinely ambiguous (unknown — a 5xx/timeout that may or may not have captured). In both, **the captured amount is not yet known.** The audit found two distinct classes of bug, neither guarded.

**Class 1 — status-strand (operator-reachable today).** Three status writers flipped status + reversed tax with **no** `payment_status` check:

- `invoice.Service.Void` — voiding an invoice whose charge later succeeds strands captured money on a voided invoice and reverses tax on a sale that completed.
- `invoice.Service.MarkUncollectible` — `MarkPaid` *is* allowed from `uncollectible` (the "wrote it off but paid after all" recovery), so reversing tax + flipping to uncollectible, then the charge settling, leaves the tenant **under-remitting** output tax on a real collected sale.
- dunning `resolveRun` (`manually_resolved`) — called the **raw store** `UpdateStatus(Voided)`, bypassing `service.Void` entirely: a *second, less-guarded void writer* that reversed no tax and emitted no `invoice.voided` event. Plus `RecordOfflinePayment` guarded only `processing`, not `unknown` (double-collect window).

**Class 2 — amount-drift ("Part B").** The automated ADR-050 unpaid-source clawback (subscription cancel/downgrade/qty-decrease/item-removal) selects a finalized funding invoice via `FindFundingInvoicesForPeriod`, which filters only `status` (not `payment_status`). An in-flight source is classified *unpaid* and its `amount_due` is **reduced immediately**. Then at settle, `MarkPaid` records `amount_paid = amount_due` (the now-lowered figure) → **under-record**, undersized refund cap. Naively *skipping* the relief instead trades that for a silent **over-charge** (the customer paid the full amount and never gets the unused portion back). You cannot correctly size the clawback until the charge reaches a terminal state.

**Reachability (honest).** On **cards** — Velox's default (`Confirm+OffSession`) — the in-flight window is *seconds*: the charge settles inline or fails. So a mutation colliding with it is rare. The window becomes *days* — and the collision routine — under **asynchronous bank methods (ACH / SEPA Direct Debit)**, which legitimately sit `processing` for 1–5 business days and are the standard way B2B annual contracts pay. So Class 2 is *"rare on cards today, regular the moment ACH/SEPA is enabled."* Stripe enforces the same rule it implies: *"When an invoice has open or paid payments, you can't void, edit, or mark it uncollectible,"* and *"open invoices can't have a credit note with a pending payment_intent."*

## Decision

One canonical predicate, in-flight guards on the status writers, a single void writer, and — for the automated clawback — **defer the issue, not the create**.

**Canonical predicate.** `domain.InvoicePaymentStatus.IsInFlight()` = `processing || unknown`. Every guard routes through it (including the prior operator credit-note gate, #295), so the definition can't drift across call sites — the drift this audit found.

**Class 1 — guards (block, Stripe-parity).**
1. `Void` and `MarkUncollectible` reject in-flight with `InvalidState` (→409), placed **before** `reverseInvoiceTax` so no reversal fires on an in-flight invoice. The reconciler resolves the charge; the operator retries.
2. `RecordOfflinePayment` widened from `== processing` to `IsInFlight()` (closes the `unknown` double-collect window).
3. **Single void writer.** Dunning `resolveRun` (`manually_resolved`) now voids through `invoice.Service.Void` (a narrow `InvoiceVoider` dep, wired post-construction like `SetInvoiceUncollectibleMarker`), inheriting the status guards, the in-flight guard, the tax reversal, and the single-writer `invoice.voided` event. Its inline credit-reversal + PI-cancel now run **only after the void succeeds** — otherwise an in-flight invoice (whose void the service refuses) would still get its live PI canceled, defeating the guard.

**Class 2 — defer-until-settle (the automated clawback).** The automated path cannot 409 a cancel/downgrade back to a human, so it **defers** rather than reduces. The key insight: `Issue()` already branches on the source's `payment_status` *at issue time* (succeeded → credit/refund channel; not-succeeded → reduce `amount_due`). So we defer *when `Issue()` runs*, not the create:

1. **`Issue()` is the single chokepoint.** Before claiming the draft→issued CAS it reads the source invoice and:
   - if the source is **voided/uncollectible** (annulled after the draft was created) → **voids the draft** (pure status flip) instead of issuing — the void already reversed the tax and zeroed the receivable, so issuing would double-reverse tax.
   - if the source is **in flight** → **no-op**, leaving the draft `status='draft' AND issue_pending`. Every issue trigger (engine `CreateAndIssueAdjustment`, the post-commit `issueClawbackDrafts`, the reconciler) defers identically — so no clawback create-site needs its own gate.
2. **`CreateAndIssueAdjustment` marks the draft `issue_pending`** so a deferred (or post-commit-failed) issue is recoverable by the reconciler.
3. **Reconciler issues once the source settles.** `RetryPendingClawbackIssue`'s scan (`ListPendingClawbackDrafts`) gains a correlated `NOT EXISTS` that skips drafts whose source `payment_status IN ('processing','unknown')`, and **drops the prior 24h `updated_at` window** — an ACH/SEPA source can sit `processing` for days, so a fixed horizon would age a deferred draft out of the scan and silently drop the relief. The source-terminal gate is now the only eligibility predicate; a settled source makes the draft eligible regardless of age.
4. **Engine void-skip.** `relieveUnpaidPrebill`'s fully-unused **void** branch is skipped for an in-flight source (it falls through to the deferring `CreateAndIssueAdjustment` for the full amount). At settle the draft issues down the right channel: paid → full credit; failed → `amount_due → 0` (the same end-state the void produced).

**No migration.** The draft row + `issue_pending` (migration 0121) *is* the durable deferred record; the source invoice's `payment_status` is the trigger signal. A dedicated table was rejected (a dual-write reconciliation surface — the recurring money-bug shape). Source-exclusion in `FindFundingInvoicesForPeriod` was rejected (it hard-fails a single-funding-source downgrade and over-credits survivors via redistribution).

**Reconciler-only, not a settle-hook.** A hook from `payment` into `creditnote`/`billing` would violate the zero-cross-peer-import rule, and the draft→issued CAS already guarantees exactly-once however many triggers race. Latency is up to one scheduler tick (~1h prod) *after* a charge that already sat in flight for days — negligible. Matches the 0121 precedent (scheduler tick, no settle hook).

## Consequences

- **Parts A + B closed for full capture.** The operator vector was gated (Part A, #295); the automated vector now defers → `amount_due` stays full while in-flight → `MarkPaid` records the full captured amount → the reconciler credits the unused share. No under-record, no over-charge.
- **Part C (partial capture) is not reachable in Velox.** `MarkPaid` records `amount_paid = amount_due`. That equals the captured amount under Velox's PaymentIntent-only **full-capture** model — Velox exposes no partial/manual-capture flow. If one is ever added, recording `amount_paid` from the PI's `amount_received` is the correct source of truth; until then this is a theoretical vector, not a live gap.
- **Genuinely-wedged payment → deferred draft waits (observability follow-up).** If a charge *never* reaches terminal (e.g. a `requires_action` PI nobody authenticates or cancels), the deferred draft sits unissued. It is **not lost** — it is durably captured and auto-issues the instant the payment settles, and a wedged payment is independently visible (the invoice stuck in `processing`; the tenant unpaid). A dedicated **stale-deferred-draft alarm** (surface a draft deferred > N days so an operator can cancel/await the wedged PI, which then auto-resolves the clawback) is a deferred *observability* follow-up — it does not affect whether the books are correct or the customer is eventually made whole.
- **Industry parity.** Operator block matches Stripe exactly. The automated defer-then-act-on-settled-state matches Lago's terminate-subscription rule (*"the unused paid amount is refunded; any unpaid unused amount is credited back"*) — Velox just sequences it after settlement instead of guessing the channel mid-flight.

## Deferred / follow-ups

| Item | Affects correctness? | Trigger |
|------|----------------------|---------|
| Stale-deferred-draft alarm (surface a clawback draft wedged on a never-settling payment) | No — obligation is durably captured and auto-issues on settle; wedged payments are independently visible | operability hardening / first ACH-SEPA design partner |
| Part C — record `amount_paid` from the PI's `amount_received` | No (not reachable: full-capture only) | partial-capture support |
| `reverseInvoiceTax` failure recovery (ADR-057 §Deferred b) | Yes, on a transient reversal failure | first real tax-filing customer |
| ADR-057 partial-issue window (non-idempotent `ApplyCreditNote` after the status flip) — Part B **inherits, does not widen** it (the reconciler only re-drives `status='draft'`) | Yes, on a side-effect failure after the CAS | making `ApplyCreditNote` idempotent (separate PR) |
