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
| 2 | Dunning enrollment | Medium | FIXED — ADR-036 amendment (auto-default-first + no-policy→skip), design panel + tests | Dunning §, S-flows |
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

## Finding 2 — unpaid invoice + no dunning policy breaks billing catchup — FIXED (ADR-036 amendment)

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
Dunning/state-machine territory → wants a short design panel before building.

**RESOLVED 2026-07-05 (ADR-036 amendment).** A divergent design panel sharpened
the root cause and picked a combination that fixes both the *observed* failure
and the deeper cluster — the earlier "no policy at all" framing was incomplete:
the recipe *does* create a policy, it was just never marked default.
- **(B) money-path resilience (the floor):** `StartDunning` maps **only**
  `GetEffectivePolicyForCustomer`'s `ErrNotFound` → deliberate-skip `InvalidState`,
  the same class as a disabled policy (already swallowed by the enrollment
  adapter). A default-less/zero-policy tenant no longer errors the catchup; a real
  infra error still fails loud.
- **Auto-default-first (fixes the real root of the live incident):**
  `upsertPolicyTx` makes the **first** policy per `(tenant, livemode)` be
  `is_default=true` — so a recipe/first-create tenant resolves a working default
  with no operator step. (Chose this over "seed at bootstrap" (A) — a seeded
  default would shadow recipe policies — and over read-time "resolve-to-sole"
  which would make `is_default` lie.)
- Companion: `startDunningWithRetry` short-circuits the deliberate skip (no wasted
  retries / misleading ERROR).

Note: auto-default-first is write-side, so the pre-existing local `qa-clean`
policy (already `is_default=false`) is flipped once via `SetDefaultPolicy`; new
tenants need nothing. Tests: `TestUpsertPolicy_AutoDefaultFirst` (real-PG),
`TestStartDunning_NoPolicyConfigured`, adapter swallow, retry short-circuit.
Live re-verified: the S2 clock advance that previously hit `internal_failure`
now completes clean.


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

## Finding 6 — FLOW TZ1 coverage audit: 8 boxes CI-locked, 4 observable-only, 2 gaps — both gaps FIXED (2026-07-13)

**Surfaced:** coverage audit of FLOW TZ1 (tenant-timezone semantics), 2026-07-12, per
the "check existing CI coverage first" process — each box mapped to its durable test by
reading the asserting lines, not name-matching.

**Outcome:** 8 boxes are locked by durable Go tests → marked `[x]` with `auto:` tags
(settings validation P8, tenant-TZ billing anchor ADR-058, host-TZ independence,
org-level-no-per-sub-column ADR-077, issued-invoice zone immutability, anniversary
month-end clamp ADR-055, calendar-31 rollover, canonical-UTC wire ADR-075). 4 are
observable-only → pending `[~]` live verification (dashboard zone-abbrev display;
invoice-period inclusive-day across PDF/hosted/portal; customer-facing PDF dates in
billing TZ; public hosted-page dates). **2 have NO automated coverage:**

- **TZ1.3 (API-key expiry / list from-to filters interpret civil dates in tenant TZ) —
  FILLABLE GAP.** The load-bearing logic is the pure `startOfDayInTZ` / `endOfDayInTZ`
  in `web-v2/src/lib/dates.ts`, which have zero tests despite being trivially
  unit-testable (the repo now runs `node --test tests/*.test.ts`, added with
  `lib/effectiveNow`). The only related test, `internal/api/timefilter/timefilter_test.go`,
  asserts the backend *UTC* date-only fallback — the opposite path. **FIXED:** added
  `web-v2/tests/dates.test.ts` asserting start/end-of-day in Asia/Kolkata +
  America/Los_Angeles (and the from/to bracket excludes a UTC-May-5/IST-May-6 row).
  Needed a small `@/`-alias resolve hook for `node --test` (`web-v2/tests/support/`), since
  `dates.ts` imports the api client for its TZ fallback — reusable for future FE unit tests.
- **TZ1.14 (cancel / plan-swap credit-note period reads billing-TZ calendar days) —
  UNCOVERED.** The cancel-credit tests (`internal/billing/cancel_multidim_test.go` etc.)
  assert the description *suffix* ("canceled mid-period") but none uses a positive-offset
  zone (Asia/Tokyo) or asserts the period *date* is the billing-TZ day, not the UTC-prior
  day. **FIXED:** extracted the triplicated Sprintf into `prorationRefundDesc` (one helper
  for the cancel + both plan-swap sites, byte-identical output) and added
  `TestProrationRefundDesc_RendersBillingTZCivilDays` (Asia/Tokyo, with a UTC control that
  proves the prior-day divergence). The behavior was already correct (all three sites did
  `.In(loc)`); the test is a regression guard.

Both were test-coverage gaps, not product bugs — the behavior was correct via the shared
TZ formatters, just unasserted on these two surfaces. Now locked.
