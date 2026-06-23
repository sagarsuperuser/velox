# ADR-061: Credit-note `Issue()` atomicity (atomic internal effects + transactional-outbox external effects)

## Status

Accepted (design-only) — 2026-06-24. **Build deferred** to a named trigger
(see Scope). This ADR is the settled blueprint so the eventual build
doesn't re-derive it, and so a maintainer who touches `Issue()` first
doesn't reach for the convenient (second-practice) fix.

## Context

The liveness audit (2026-06-23) found a sink in `creditnote.Service.Issue()`:
the `draft→issued` compare-and-swap (CAS) commits in its **own**
transaction, and the side effects run **afterward** in separate calls. If a
side effect fails, the credit note is durably `issued` while its effect
never happened — most visibly, an `issued` credit-channel note whose
`GrantForCreditNote` failed leaves the customer **shown a credit they never
received**, with no automatic recovery (re-Issue refused: `status!=draft`;
Void refused: issued is final).

The first instinct was a reconciler that re-runs the missing grant. The
project principle (see `feedback_first_good_practice_default`) corrects
that: **go for the first good practice by default; the second practice
needs a really strong reason.** A reconciler is the *second* practice; it
over-applies eventual-consistency to a side effect that could simply be
atomic.

`Issue()` does three kinds of work, and the right tool differs by kind:

1. **State transition** — `draft→issued` (the CAS). Internal.
2. **Internal side effects** — `GrantForCreditNote` (credit ledger) for a
   paid invoice's credit note, or `ApplyCreditNote` (reduce `amount_due`)
   for an unpaid invoice. Both are DB writes on the same pool.
3. **External side effects** — the Stripe refund (refund-channel note) and
   the **tax reversal** at the provider. Network calls; they cannot share a
   DB transaction.

Today (1) is its own transaction and (2) and (3) are separate, unguarded
calls afterward — a classic dual-write.

## Decision

Make `Issue()` first-practice end-to-end, by kind of effect.

### Internal effects → one transaction (atomic)

The CAS and the internal side effect commit together in a **coordinator
`*sql.Tx`** threaded through the creditnote, credit, and invoice stores —
the ADR-056 pattern Velox already uses for proration. The CAS provides both
concurrency safety (exactly one caller proceeds) **and** atomicity: a grant
or `amount_due` failure rolls the issue back, the note stays `draft`, and
the caller retries cleanly. The issued-but-ungranted orphan **cannot
exist** — there is nothing to reconcile.

Shape:

- New tx-accepting store variants: `creditnote.TransitionStatusTx`,
  `credit.GrantForCreditNoteTx`, `invoice.ApplyCreditNoteTx` — each
  operating on a passed `*sql.Tx`.
- `Issue()` opens the coordinator tx (RLS: `BeginTx(ctx, TxTenant,
  tenantID)`), performs CAS + the internal effect on that tx, and commits.

### External effects → transactional outbox (first-practice for a network boundary)

The Stripe refund and tax reversal **cannot** be in the DB transaction —
that is the genuine strong reason, and it is the *only* place the second
tier is justified here. But the first practice for a network boundary is
**not** fire-and-forget + an after-the-fact reconciler scan; it is the
**transactional outbox** (ADR-040): enqueue the *obligation*
("reverse tax for invoice X", "refund PaymentIntent Y") **inside the
coordinator tx**, so the intent commits atomically with the issue and
**cannot be lost**. A dispatcher then executes it with Stripe idempotency
keys; a backstop sweep handles dead-letter rows.

So: the obligation is atomic; only its execution is asynchronous.

### Reconciler → backstop only

A reconciler/marker-scan survives in exactly one role: the dead-letter
sweep for stuck outbox rows. It is never the primary mechanism for a
correctness-bearing effect.

### Net invariant

After this change, for any committed `issued` credit note: its internal
effect is committed (atomic), and its external obligations are durably
enqueued (outbox). Neither an ungranted orphan nor a lost tax-reversal can
occur. The 0093 credit-ledger dedup index and Stripe idempotency keys
become belts, not the mechanism.

## Consequences

- One coherent flow, no special-case recovery paths for the internal grant.
- The external effects move from inline to enqueue-in-tx + async dispatch —
  a sequencing change that must preserve correctness (do not reverse tax or
  refund before the issue is durably committed; the outbox enqueue is what
  guarantees the ordering).
- Moderate refactor: three tx-variant store methods + the coordinator in
  `Issue()` + moving two external effects onto the outbox. The patterns
  (ADR-056 coordinator, ADR-040 outbox) already exist; this is composition,
  not invention.
- Tests: atomic-rollback (grant fails → note stays `draft`, no ledger
  entry); obligation-durability (Stripe down at issue → outbox row present,
  dispatched later, exactly once); no double-grant under concurrent Issue.

## Alternatives considered

- **Reconciler-first (re-run the grant via the 0093 dedup index).**
  Rejected as the *primary* — it is the second practice applied to an
  internal effect that can be atomic; it leaves a window and needs a
  scan/marker. Retained only as the deferred-build fallback if the atomic
  refactor itself is postponed.
- **A persisted `grant_settled` / `issue_pending` marker column to bound a
  reconciler scan.** Rejected — solves a scale problem we don't have and
  cements the second practice with a migration.
- **Outbox for the *internal* effects too.** Rejected — overkill; an
  in-process DB write belongs in the transaction, not on an async queue.
- **Do nothing.** The current state is the deferred gap; acceptable only
  because it is pre-launch with no victim (see Scope).

## Scope and sequencing

**Deferred build**, trigger = the first design partner issuing
account-credit credit notes (the feature carries real money). When built,
do it as **one pass covering the whole flow**, not piecemeal — and **fold
in the sibling gap**: `reverseInvoiceTax` can also fail post-issue today
(an external effect), which wants the identical outbox treatment. So the
unit of work is: internal effects (grant, `amount_due`) atomic in the
coordinator tx **+** external effects (tax reversal, refund) on the
transactional outbox — grant + reduce + reverse + refund, together.

Until then, a grant failure here is an operator-visible error plus a manual
fix; the marker comment at the grant site records this.

## Related

- `feedback_first_good_practice_default` — the principle this ADR applies.
- ADR-040 — transactional outbox (the external-effect first-practice tool).
- ADR-056 (#287) — coordinator `*sql.Tx` (the internal-effect first-practice
  tool, proration).
- ADR-057 (#290) — clawback CN created in the item-change tx; documented the
  post-CAS partial-issue gap this ADR closes generally.
- ADR-059 (#296) — in-flight clawback deferred via a reconciler citing the
  zero-cross-peer-import rule. **Open question for the broader design:**
  that rule is not a strong reason against an *outbox* (a shared generic
  table needs no cross-peer import), so the clawback path may warrant the
  same outbox treatment for consistency. Tracked by the product-wide
  first-vs-second-practice audit (2026-06-24).
