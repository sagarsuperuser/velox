# ADR-078: Prepaid commits + drawdown, phase 1

**Status:** Accepted (2026-07-05)
**Context:** `docs/design-prepaid-commits.md` (the locked design — peer-swept
across Metronome/Lago/Stripe/AI-infra with adversarial quote-verification,
then a 6-lens adversarial design panel; this ADR records the decisions, the
design doc holds the full evidence and census).

## Decision

Ship the prepaid-commit primitive on the existing credit-block ledger:

1. **Discriminator, not a new entry type.** `grant_kind`
   (`'commit' | 'promotional' | NULL`) + `source_invoice_id` on
   `customer_credit_ledger` (migration 0136), with a partial-unique
   fund-once index predicated on `grant_kind='commit'`. Legacy `NULL`
   grants are money-derived liabilities and drain in the **paid class**;
   `promotional` is explicit operator input only. Zero behavior change
   until a tenant uses either.
2. **Commit line = dedicated nullable columns** on `invoice_line_items`
   (`commit_granted_cents`, `commit_expires_at`) — not metadata JSONB
   (unvalidatable money config), not a side table (orphan lifecycle).
   Granted amount is independent of the line price (discounted commits:
   pay $9k, get $10k). Manual invoices only, `add_on` lines only, at most
   one per invoice — line-local rules in `buildLineItem` (shared by BOTH
   line-creation paths), invoice-level rules re-asserted in the store
   under the invoice row lock.
3. **Fund on ISSUE, in the finalize tx.** `FinalizeWithDates` grants the
   block via an injected `CommitFunder` (satisfied by `credit.Service`)
   inside the finalize coordinator tx — status flip and grant are
   both-or-neither. A funder error fails finalize: operator-synchronous,
   loud, retryable. The **payment gate** (pending-until-paid) and the
   markPaid funder hook are **phase 2** (trigger: first DP
   pending-until-paid ask, or the self-serve/auto-top-up build which
   hard-defaults to gated). Grant-on-issue is the verified negotiated-B2B
   default (Metronome term-start-at-issue; Orb `require_successful_payment`
   defaults off).
4. **Retire on VOID only.** Voiding a commit funding invoice retires the
   grant's remaining balance in the void tx (`RetireCommitGrantForInvoiceTx`
   — ExpireGrantAtomic's locked shape on the caller's tx, entry stamped at
   void-time, `entry_type='adjustment'` with
   `metadata.reason='commit_void_retire'`). The `consumed_cents` CAS makes
   the legal uncollectible→void sequence retire exactly once. Consumed
   stays consumed. **Uncollectible / dunning pause / cancel do NOT
   retire** — the block stays live as a collections stance (consistent
   with the other dunning terminals), which also makes the
   uncollectible→paid recovery correct with no restore leg (voided→paid
   is rejected, so a retired grant can never be paid for).
5. **Commit invoices are cash instruments.**
   - Credit balance never pays a commit funding invoice —
     `ApplyToInvoiceAtomic` no-ops on invoices carrying a commit line
     (in-tx re-read; covers the auto-charge sweep and dunning retry —
     otherwise a card-less customer's commit settles with its own
     just-granted credits: revenue booked on zero cash).
   - Credit notes are **blocked** on commit invoices (typed error at CN
     create, shared core so operator + automated paths agree). Unwind:
     unpaid → void (retires); paid → phase-2 CN-retire leg (trigger:
     first DP commit-refund ask).
6. **Status-flip CAS (prerequisite prod fix).** `FinalizeWithDates` gains
   `AND status='draft'`; `updateStatusInTx` gains an in-SQL allowed-source
   set (finalized←draft; voided←draft/finalized/uncollectible, never paid;
   uncollectible←finalized). Service guards read stale snapshots — the
   moment money hooks ride these flips, the CAS is what prevents
   pay-vs-void retiring paid-for credits and void-vs-finalize minting a
   grant on an annulled invoice.
7. **One global lock order:** invoice-row → customer advisory → ledger
   rows. `ApplyToInvoiceAtomic` now locks the invoice row FIRST and
   re-reads status/amount_due in-tx (also fixing the stale-due race:
   a drain racing a settle no longer burns credits into a paid invoice),
   then takes the advisory lock; `AdjustAtomic` takes the advisory lock
   before its row locks. This removed a reachable 3-way deadlock cycle
   (drain: ledger→invoice; expiry: advisory→ledger; new hooks:
   invoice→advisory) and is what makes alert crossings well-ordered.
8. **Drawdown order:**
   `ORDER BY (grant_kind IS NOT DISTINCT FROM 'promotional') DESC,
   expires_at NULLS LAST, created_at, id` — promotional (zero-cost-basis)
   first, then the paid class by the existing tiebreak. `IS NOT DISTINCT
   FROM` is load-bearing: a bare `=` sorts NULL first under DESC,
   draining legacy money-derived credits before free promo (verified
   against live Postgres). Applies to both drainPositiveBlocks callers
   (drawdown AND clawback attribution). No operator priority field —
   Metronome/Lago/Stripe numeric priority is the named phase-2 escape
   hatch.
9. **Balance alerts** (green field; the 0083-deleted subsystem's
   replacement): `credit.balance_low` (per-tenant threshold,
   `tenant_settings.credit_balance_low_threshold_cents`),
   `credit.balance_depleted` (>0→0), `credit.balance_recovered` (0→>0 —
   kept as depleted's state-machine complement, not for parity).
   Crossings computed on SUM(amount_cents) before/after inside each
   ledger-writing tx via one shared helper at the 3 insert sites,
   enqueued on the same tx (transactional outbox; credit store gained a
   `SetOutboxEnqueuer` mirror of invoice's). Payloads carry
   `customer_id` + `balance_cents` (+ `threshold_cents`). Known lag:
   expired-but-unswept blocks cross when the sweep retires them.
10. **Expiry default: never.** `commit_expires_at` optional; term-aligned
    defaults arrive with recipe-carried commits (phase 2) — a manual
    invoice has no term to align to, and inventing one is the banned
    silent-fallback class.

## Consequences

- New API surface: `commit_granted_cents`/`commit_expires_at` on line
  items; `grant_kind` (promotional only) + `expires_at` on
  POST /v1/credits/grant; threshold on tenant settings; 3 outbound event
  types.
- `VELOX_BILLING_ALERTS_INTERVAL` phantom removed from compose (nothing
  read it).
- Accepted, documented limitations (phase-2 triggers in the design doc):
  ReverseForInvoice re-grants drawn credits as NULL-kind/never-expiring
  (pre-existing class, needs per-block attribution); reporting stays
  face-value-aggregate; uncollectible/pause/cancel leave commit blocks
  live; the $1M per-grant cap applies to commits (split larger commits).
- Deferred: payment gate + paid-hook, auto-recharge, CN-retire leg,
  recipe commits + term-aligned expiry, drain-priority override,
  per-customer thresholds, pending-block visibility, true-up.
