# Manual-test findings

The single home for issues surfaced while executing
[`MANUAL_TEST.md`](../../MANUAL_TEST.md). **`MANUAL_TEST.md` stays a
checklist** (does each flow pass?); the detail and disposition of any
failure live here.

## How this works

- **Fix-now** (clear, small, safe): fix on the branch — the commit + its
  test is the record. The finding here just points at the commit; the
  MANUAL_TEST box goes `[x]`.
- **Open** (needs a decision, a design fork, or can't be done yet):
  status stays `OPEN` until resolved. The MANUAL_TEST box goes `[~]`
  with a one-line pointer here.
- A finding that grows into real backlog can graduate to a GitHub issue;
  until then this doc is the tracker (mirrors how the 2026-07-02 audit
  report serves as the audit backlog tracker).

Status: **FIXED** · **OPEN** (needs decision) · **WONTFIX** (by design).

| # | Area | Severity | Status | MANUAL_TEST flow |
|---|---|---|---|---|
| 1 | Subscription create | Medium | FIXED — re-applied on `qa/manual-test-pass` (conflict-resolved vs ADR-074) | B6 / U1 |
| 2 | Dunning enrollment | Medium | OPEN — needs decision | Dunning §, S-flows |
| 3 | Docs (B5) | Low | FIXED (doc) | B5 |
| 4 | Docs (S1.4) | Low | FIXED (doc) | S1.4 |
| 5 | Auto-charge retry comment | Low | FIXED (comment) | B4 |

---

## Finding 1 — `start_now` subscription lands with `activated_at = NULL` — FIXED

**Surfaced:** manual pass 2026-07-03, subscription-create + MRR-movement flows.
**Fix:** commit `1d7e132` on `fix/qa-manual-findings` (held for review).

A `start_now` subscription was created `active` but with `activated_at = NULL`.
Two layers both dropped it: the service's `StartNow` branch never set it
(unlike `Activate` / `ActivateAfterTrial`), **and** `CreateWithBill`'s INSERT
omitted the column entirely, so it was dropped even when set. Consequence: the
sub counts in headline MRR (`currentMRR` keys on `status='active'`) but is
invisible to MRR movement / point-in-time / churn (all key on `activated_at`
as the MRR-start event), so the dashboard's New/Net never reconciled with the
MRR delta for the most common create path.

Provenance: **pre-existing latent bug**, not a sprint regression — INSERT
traces to `7c8a2ca` (pre-sprint test-clock feature), column to `620e22e`
(original schema). P2b-b (#345) added `activated_at` to the *other* activation
writers but never touched the create-time path.

Fix: service stamps `activated_at = now` in the StartNow branch; store INSERT
adds the column + binds `sub.ActivatedAt`. Trialing subs still activate at
trial-end. Unit assertion (`TestCreate`) + real-Postgres round-trip
(`TestCreate_StartNow_PersistsActivatedAt`), both mutation-verified.
Live-reconfirmed: post-fix sub → `activated_at` set, movement new/net reconcile.

Residual (flagged, not built — no speculative backfill at 0 customers): active
subs created before the fix keep `activated_at = NULL`. If real data existed:
`UPDATE subscriptions SET activated_at = COALESCE(activated_at, started_at, created_at) WHERE status='active' AND activated_at IS NULL`.

---

## Finding 2 — unpaid invoice + no dunning policy breaks billing catchup — OPEN (needs decision)

**Surfaced:** manual pass 2026-07-03, test-clock cycle-billing flow.

A cycle invoice generated UNPAID (no Stripe / no payment method) → catchup tried
to enroll it in dunning → **`get effective dunning policy: not found`** → the
whole billing catchup reported `had_errors:true` (test-clock status
`internal_failure`; in the real scheduler this is a logged billing-cycle error
every tick). Confirmed by contrast: a fully-credit-paid invoice had
`had_errors:false` — only the unpaid invoice triggered enrollment.

Root cause: `dunning.GetEffectivePolicyForCustomer` (service.go:158) falls
through to `store.GetDefaultPolicy(tenantID)`, which errors when the tenant has
**no default dunning policy**. **Bootstrap seeds `tenant_settings` but not a
dunning policy** — only recipes create one — so any tenant set up manually (no
recipe) that bills an unpaid invoice errors the billing cycle. The design
comment at service.go:156 assumes a tenant default always exists; after a plain
bootstrap it doesn't.

Provenance: **pre-existing gap**, not a sprint regression.

Decision needed (two legit fixes):
- **(A) Seed a default dunning policy at bootstrap** — matches the "default
  always exists" intent + the `tenant_settings`-seed precedent (ties into P7's
  RunBootstrap). Open q: seeded enabled or disabled?
- **(B) Make enrollment resilient** — no default policy ⇒ skip enrollment (log,
  don't error), so an unconfigured optional feature can't break the core money
  path (same principle as "SMTP unset doesn't break billing"). More defensive;
  keeps dunning genuinely opt-in.

Recommendation: **(B)** as the floor (billing must not fail because dunning is
unconfigured), optionally + **(A)** if the product wants dunning on-by-default.
Dunning/state-machine territory → wants a short design panel before building
(deferred: Fable credits out + review-first).


## Finding 3 — MANUAL_TEST B5 said 409 for idempotency-key reuse; correct is 422 — FIXED (doc)

**Surfaced:** manual pass 2026-07-03, B5 (Idempotency-Key header).

Same idempotency key + different body returns **422 `idempotency_error`** with
Stripe's exact wording, not 409. This is correct (Stripe parity) and CI-locked
by `internal/api/middleware/idempotency_cache_test.go`; the doc was wrong.
Fixed the MANUAL_TEST B5 checkbox (409 → 422 + note). Code unchanged — doc-only
drift.


## Finding 4 - MANUAL_TEST S1.4 "Set Up Payment -> type card" is a removed flow - FIXED (doc)

Surfaced: manual pass 2026-07-03, S1.4 card attach.

The inline "Set Up Payment -> enter card" button (/checkout/setup) was removed in
the unified-PM-paths cleanup (web-v2/src/lib/api.ts:251). Cards now attach ONLY via a
Stripe-hosted Checkout setup-session: operator mints a link via
POST /v1/customers/{id}/payment-methods/setup-session -> {url} (dashboard: Payment
Methods -> "Copy setup link" / "Send setup email"); customer enters the card on
Stripe's page; checkout.session.completed / setup_intent.succeeded webhook attaches
the PM. MANUAL_TEST S1.4 still described the removed button. Doc corrected; code
unchanged (removal was intentional). No product bug.

---

## Finding 5 — auto-charge-retry decline comment claims a non-existent inline dunning start — FIXED

**Surfaced:** manual pass (B4 adversarial dig), 2026-07-04.
**Fix:** comment correction in `internal/billing/engine.go` `processAutoCharge` (this branch).

`processAutoCharge`'s card-decline branch asserted *"ChargeInvoice already … fired
inline StartDunning (closes the Phase 3 → Phase 5 webhook race per
stripe.go:401-426)."* That is false and self-contradictory: `stripe.go`'s charge path
has **no** `StartDunning` call, and `chargeInvoice` explicitly documents *"Dunning is
NOT started here"* — dunning is started by the `payment_intent.payment_failed`
**webhook** (`handlePaymentFailed`), backstopped by the `EnrollFailedWithoutDunning`
reconciler. `stripe.go:401-426` is just the function's guard clauses, not a dunning start.

Not a functional bug (the invoice still reaches dunning via webhook/backfill), but a
misleading comment on a money path that hides a real operational dependency
(auto-charge-retry declines rely on webhook delivery, else a ≥10-min backfill delay).
Corrected the comment to describe the actual mechanism. The exactly-once and
terminal-sink properties of the retry path were verified sound in the same dig
(Unknown → `payment_status='unknown'` excludes re-listing; the paid-invoice predicate
backstops a stale `auto_charge_pending`).
