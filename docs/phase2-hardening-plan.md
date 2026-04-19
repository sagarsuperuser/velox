# Velox Phase 2 Hardening Plan

Organized by risk × impact across 5 waves. Each item has effort size, rollout notes, tests, and dependencies.

## Legend
- **S** = 1-2 hours · **M** = half-day to 1 day · **L** = multi-day
- `[SEC]` security · `[COR]` correctness · `[RES]` reliability · `[FEAT]` feature · `[HYG]` hygiene
- `→X` = blocks X · `X→` = blocked by X

---

## Wave 0 — Schema hygiene (bundled with Wave 1) — S

One migration `0006_schema_hygiene.up.sql`. Online-safe (IF NOT EXISTS, no big-table rewrites).

| ID | Item | Notes |
|---|---|---|
| HYG-1 | `customers.email NOT NULL` | Check for NULL emails first; backfill `'unknown-{id}@placeholder.invalid'` before NOT NULL |
| HYG-2 | Partial UNIQUE on active subscriptions | `UNIQUE(tenant_id, customer_id, plan_id) WHERE status IN ('active','trialing','past_due')` |
| HYG-3 | `tax_rate NUMERIC(6,2)` → `tax_rate_bp BIGINT` | Dual-write → cutover → drop (2 migrations) |
| HYG-4 | FK ON DELETE policies explicit | Customers→invoices RESTRICT; tenants→customers RESTRICT; audit refs PRESERVE |
| HYG-5 | `audit_log` append-only | BEFORE UPDATE/DELETE trigger raising exception |
| HYG-6 | Schedule idempotency + payment-token cleanup | Hourly task in scheduler |

---

## Wave 1 — Security

### [SEC-1] Close RLS bypass on 3 tables — M → unblocks W2

**Problem:** `tenant_settings`, `idempotency_keys`, `payment_update_tokens` have no RLS; code queries via `db.Pool` directly. Cross-tenant leakage if pool role widens.

**Target:**
- Migration: `ENABLE ROW LEVEL SECURITY + FORCE + POLICY USING (tenant_id = current_setting('app.tenant_id'))` for all three tables.
- Refactor callers to `db.BeginTx(ctx, postgres.TxTenant, tenantID)`:
  - `tenant/settings.go` Get/Upsert/NextInvoiceNumber/NextCreditNoteNumber
  - `middleware/idempotency.go` Get/Put queries
  - `payment/token.go` Create/Validate/MarkUsed/Cleanup
- `ListTenantIDs` is legitimately cross-tenant (dunning scheduler) → keep on `TxBypass` with an explicit comment.

**Tests:** unit test that TxTenant(A) can't read tenant B's rows; integration test end-to-end.

**Rollout:** single commit; migration takes <1s (just ALTER + CREATE POLICY).

### [SEC-2] Hash webhook endpoint secrets — L

**Problem:** `webhook_endpoints.secret TEXT` stored plaintext (schema:542). DB dump → attacker forges HMAC signatures against customer receivers.

**Target (two-phase rollout):**
- **Phase A:** migration adds `secret_hash, secret_salt, secret_last4`; dual-write on Create/Rotate; read still from plaintext.
- **Phase B:** read from hash; plaintext column still present.
- **Phase C:** drop plaintext column.
- Create/Rotate returns raw secret once; subsequent GET shows only `last4`.
- Constant-time verification in webhook signer.

**Tests:** rotate returns raw; GET returns only last4; incorrect secret fails verify; migration dual-write preserves existing signatures.

**Rollout:** 3 commits over 1-2 weeks to allow revert window.

---

## Wave 2 — Correctness (silent money bugs)

### [COR-1] Wire coupons → `invoice.discount_cents` — M → COR-2

**Problem:** Coupon redemptions written; `invoice.discount_cents` never populated.

**Target:**
- New method: `coupon.Service.ApplyToInvoice(ctx, tenantID, invoiceID, subscriptionID) (discountCents, err)` — pure: consults active redemptions, returns the amount.
- Invoice finalize + billing engine call it before computing totals.
- Order: line_items → subtotal → apply discount → tax → total (Stripe-style).
- Line-item level vs invoice level: start at invoice-level (simpler), upgrade to line-item in FEAT-6.

**Schema:** no change.

**Tests:** percentage coupon on simple invoice; fixed-amount coupon; coupon exceeds subtotal (clamp to subtotal); expired coupon ignored; proration invoice with coupon.

### [COR-2] Wire tax at finalize & proration — M — COR-1→

**Problem:** `tax.Calculator` exists; never called. `invoice.tax_amount_cents` always 0.

**Target:**
- At finalize: `tax.Calculate(billing_address, subtotal_after_discount, currency) → (amount, rate_bp, name)` → write to invoice before total.
- Same on proration invoice creation (fixes the "proration is tax-free" bug).
- Add `tenant_settings.tax_inclusive BOOLEAN DEFAULT false`; if true, interpret prices as tax-inclusive (subtotal = sticker / (1+rate), tax = sticker - subtotal).
- `customer_billing_profiles.tax_id TEXT` for VAT number (B2B reverse-charge cases).

**Tests:** exclusive pricing; inclusive pricing; 0% jurisdiction; proration with VAT; VAT reverse-charge path (future).

### [COR-3] Plan change "at period end" becomes real — M

**Problem:** `subscription/service.go:226-238`: `EffectiveAt` set to period end but `sub.PlanID = NewPlanID` is written immediately — API lies.

**Target:**
- Migration `0008`: add `subscriptions.pending_plan_id TEXT, pending_plan_effective_at TIMESTAMPTZ`.
- `ChangePlan(immediate=false)`: write pending fields, do NOT touch `PlanID`.
- Billing engine cycle boundary: if `now >= pending_plan_effective_at`, apply (PlanID=pending, clear pending, record history, no proration).
- New endpoint: `DELETE /subscriptions/{id}/pending-change` to cancel a scheduled change.
- Idempotent: changing pending plan twice just overwrites.

**Tests:** scheduled downgrade applies on next cycle; cancel-pending restores state; immediate change still works; mid-period cancel clears pending.

### [COR-4] Three concurrency races (3 commits × S each)

**Problem:** Read-check-write patterns without locks in:
- `invoice/service.go:182-202` (AddLineItem + UpdateTotals not atomic)
- `credit/service.go:174-204` (Adjust balance-check + append not locked)
- `subscription/service.go` Cancel/Pause/Resume (state-check + write not guarded)

**Targets:**
- **Invoice totals**: wrap CreateLineItem + ListLineItems + UpdateTotals in one tx; roll back line item on any failure.
- **Credit Adjust**: wrap balance read + append in tx with `SELECT ... FOR UPDATE` on ledger rows (same pattern `ApplyToInvoiceAtomic` uses).
- **Subscription state transitions**: replace read-check-write with conditional UPDATE — `UPDATE subscriptions SET status='canceled' WHERE id=$1 AND status IN ('active','paused') RETURNING *`; 0 rows → re-fetch and return idempotent current state or conflict error.

**Tests (each):** two concurrent goroutines → exactly one state transition, one event.

### [COR-5] Fix truncating division in unit-amount + Stripe tax rate — S

**Problem:** `billing/engine.go:322-324` and `tax/stripe.go:98` use int division — cents lost.

**Target:** switch to `money.RoundHalfToEven`. Already shared after `0c3f4e0`.

**Tests:** 3 × $0.33 = $0.99 not $1.01; boundary cases.

### [COR-6] Idempotency middleware caches 4xx & 5xx — S

**Problem:** `middleware/idempotency.go:109` only stores 2xx; transient 500 retry re-runs side effects.

**Target:** cache all responses except 409/422 (those signal "this isn't the real first response"). 24h TTL already in `expires_at`. Matches Stripe semantics.

**Tests:** retry after 500 replays 500; body fingerprint mismatch returns 422.

---

## Wave 3 — Reliability

### [RES-1] Transactional outbox for outbound webhooks — L → unblocks RES-2, RES-6

**Problem:** `payment/stripe.go:144-150` dispatches in `go func() { _ = ... }()`. Crash loses event.

**Target:**
- Table `webhook_outbox(id, tenant_id, event_type, payload, status, attempts, next_attempt_at, last_error, created_at, dispatched_at)` with partial index `WHERE status='pending'`.
- Producer writes to outbox in SAME tx as the state change (e.g., MarkPaid + outbox insert atomic).
- Dispatcher worker (leader-gated via RES-2): `SELECT ... FOR UPDATE SKIP LOCKED LIMIT 10 WHERE status='pending' AND next_attempt_at <= now()`; dispatch; update status/attempts/next_attempt_at with exponential backoff (1s, 5s, 30s, 5m, 1h, 4h, 12h × retry up to 72h).
- DLQ: after 15 attempts → `status='failed'`, metric emitted.
- Receiver dedups on `event_id` (Velox guarantees uniqueness).

**Rollout:** shadow mode first (write to outbox AND fire-and-forget) for 1 week to compare metrics; then cut over; then remove fire-and-forget.

**Tests:** crash between commit and dispatch → eventually delivered; 500 response → retry with backoff; 410 Gone → DLQ immediately; duplicate delivery on receiver side → idempotent.

### [RES-2] Scheduler advisory lock — M — RES-1→

**Problem:** Two app replicas both run `engine.RunCycle`.

**Target:**
- Wrap each tick's RunCycle / dunning sweep / outbox dispatch in `SELECT pg_try_advisory_lock($key)`; skip if false; release at tick end.
- Separate keys for `billing-scheduler`, `dunning-scheduler`, `outbox-dispatcher` → each can run on different leaders if we want.
- Outbox drain uses `FOR UPDATE SKIP LOCKED` (parallelism at row level, not leader level).
- Matches the pattern already in `platform/migrate/migrate.go`.

**Tests:** two scheduler instances → only one runs per tick; leader crash → lock auto-released, other picks up.

### [RES-3] Safe handling of unknown Stripe results — M

**Problem:** `payment/stripe.go:179-207` marks `PaymentFailed` on any error — including 500/timeout where Stripe may have succeeded server-side.

**Target:**
- Distinguish explicit failure (Stripe 4xx + decline_code) from unknown (5xx, timeout, conn reset).
- Explicit failure → `PaymentFailed` with decline_code.
- Unknown → new enum `PaymentPending` → `PaymentUnknown`; store `stripe_payment_intent_id` (if returned) + `attempted_at`.
- Reconciler worker: for `PaymentUnknown` rows older than 60s, query Stripe by idempotency key or PI ID → resolve to succeeded/failed.
- Idempotency key on Stripe call: `inv_{invoice_id}_{attempt_number}` — Stripe returns same result on retry.

**Tests:** Stripe 500 → status=unknown, reconciler resolves; 402 card_declined → failed with code; network timeout w/ PI created → reconciler finds it.

### [RES-4] Inbound Stripe webhook dedup tightened — S

**Problem:** `payment/stripe.go:212-219`: reads dedup then acts; concurrent delivery can race the first handler's in-flight processing.

**Target:**
- `INSERT INTO stripe_webhook_events (...) ON CONFLICT (tenant_id, stripe_event_id) DO NOTHING RETURNING id` → row returned means "we own it"; no row means "duplicate, return 200 with no-op".
- Process handler after dedup commits (or inside same tx — both safe).

**Tests:** concurrent delivery → exactly one handler run; handler crash after dedup INSERT → replay safe via downstream idempotency.

### [RES-5] Audit log fail-closed opt-in — S

**Problem:** `middleware/audit.go:195-236`: all failures log-and-swallow.

**Target:**
- `tenant_settings.audit_fail_closed BOOLEAN DEFAULT false`.
- If true, audit INSERT failure → 503 `audit_error` returned to caller.
- Always emit `audit_write_errors_total{tenant_id}` metric.
- SOC-2-bound tenants opt in.

**Tests:** fail-open default → request succeeds, metric incremented; fail-closed → 503.

### [RES-6] Email delivery outbox + DLQ — M — RES-1→

**Target:** reuse outbox pattern with `email_outbox` table. Backoff, DLQ, metrics.

### [RES-7] Dunning circuit breaker + payment-retry timeout — S

**Target:** per-tenant breaker around Stripe calls; opens after N consecutive 5xx in window, rejects for cooldown. 15s timeout on `RetryPayment`. Manual reset endpoint. Metric `stripe_breaker_state{tenant_id}`.

### [RES-8] PII scrubbing for logs + error-column storage — S

**Target:** `errs/scrub.go` regex redactor for card fragments, emails, customer IDs in Stripe error messages. Applied at persistence + log boundaries.

**Tests:** "Card ending in 4242" → "Card ending in ****".

---

## Wave 4 — Feature completeness

### [FEAT-1] Credit notes reduce `invoice.amount_due` — S

On `Issue`, call `invoice.ApplyCreditNote` (method exists). Clamp at invoice total.

### [FEAT-2] Direct refund endpoint — M

`POST /invoices/{id}/refund` → Stripe refund + credit note + invoice update. Partial refunds. Reason enum (duplicate, fraudulent, requested_by_customer, other).

### [FEAT-3] Customer payment-method self-service — M

`/v1/me/payment-methods` via portal session. Stripe SetupIntent for 3DS. Endpoints: list, set default, remove. Wire to `web-v2`.

### [FEAT-4] Price overrides consumed at rating — S

`billing.engine` rating helper: look up `customer_price_overrides` first, fall back to plan default. Unit test with override + without.

### [FEAT-5] Multi-item subscriptions — L (Phase 3)

Schema: `subscription_items(id, subscription_id, plan_id, quantity, price_override_id)`. Subscription PlanID deprecated → set of items. Rating/billing iterates. Proration per item. Plan change = item change. Portal updated.

**Deferred:** 3-day initiative; not a Phase 2 blocker.

### [FEAT-6] Coupon duration + stacking — M — COR-1→

Add `coupons.duration` enum (once/repeating/forever), `coupons.duration_periods INT` (for repeating). Stacking rules per coupon. `coupon_redemptions` tracks periods applied.

### [FEAT-7] Usage backfill API — S

`POST /usage-events/backfill` accepting past timestamps, tagged `origin='backfill'` for audit.

### [FEAT-8] Test mode / sandbox — M

New key namespace `sk_test_*` / `pk_test_*`. Stripe calls replaced with stub in test mode. Isolated invoices marked `livemode=false`. Stripe convention.

---

## Non-goals (explicit)
- Tax engine integration (Avalara/TaxJar) — after COR-2 lands
- Revenue recognition / ASC 606 — Phase 3+
- ACH / SEPA / wire rails — after core is stable
- Usage bucketing rollups (hourly/daily) — perf concern, not correctness
- Custom HTML invoice templates per tenant — after RES-6

---

## Dependency map

```
Wave 0 (HYG) ──┐
SEC-1 (RLS) ───┼──> W2 correctness work
SEC-2 (secrets) ── independent

COR-1 ──> COR-2 ──> [proration revisit benefits]
COR-3 (real end-of-period) independent
COR-4 (races) independent — smallest, ship first
COR-5, COR-6 trivial, ship first

RES-1 (outbox) ──┬─> RES-2 (scheduler lock)
                 └─> RES-6 (email outbox reuse)
RES-3 (unknown Stripe) independent
RES-4 (inbound dedup) independent

FEAT-1 ← COR-1/2 (amounts finalized)
FEAT-6 ← COR-1
FEAT-5 is Phase 3
```

---

## Recommended commit order

1. **SEC-1 + HYG bundle** — security wave, one migration
2. **COR-4** — three concurrency fixes (3 commits, quick wins)
3. **COR-5** — rounding (trivial)
4. **COR-6** — idempotency 4xx/5xx caching
5. **COR-1** — coupons → invoice discount
6. **COR-2** — tax at finalize + proration
7. **COR-3** — real at-period-end plan change
8. **FEAT-1** — credit note → amount_due
9. **SEC-2 Phase A** — webhook secret dual-write
10. **RES-4** — inbound dedup tightened
11. **RES-3** — ChargeInvoice unknown-state
12. **RES-5** — audit fail-closed opt-in
13. **RES-7** — dunning breaker
14. **RES-8** — PII scrubbing
15. **HYG-6** — scheduled cleanup
16. **RES-1** — transactional outbox (L)
17. **RES-2** — scheduler advisory lock
18. **RES-6** — email outbox
19. **SEC-2 Phase B/C** — cutover + drop plaintext
20. **FEAT-2, FEAT-3, FEAT-4, FEAT-7, FEAT-8** — feature completeness
21. **FEAT-6** — coupon stacking
22. **FEAT-5** — multi-item subs (Phase 3)

---

## Test strategy

- **Wave 0/1**: migration up/down tests; RLS violation assertions; secret rotation e2e
- **Wave 2**: invariant tests (`invoice.total = subtotal + tax - discount` always); race tests (goroutines); plan-change scheduled integration; rounding boundary tables
- **Wave 3**: failure-injection (crash after commit, Stripe 500/timeout), webhook replay, lock contention, breaker state transitions
- **Wave 4**: feature e2e per endpoint

## Rollback discipline

- Migrations MUST be online-safe: NULL→backfill→NOT NULL in separate migrations; no ALTER TABLE rewrites on hot tables
- Every `*.up.sql` has a matching `*.down.sql` that preserves data where possible
- Wave 3 (outbox) runs in shadow mode before cutover
- Feature flags gate RES-1 / RES-6 / SEC-2 until verified

## Sizing

| Wave | Effort | Items |
|---|---|---|
| 0 | 0.5d | 6 hygiene items |
| 1 | 1-2d | SEC-1 (M), SEC-2 (L, staged) |
| 2 | 3-4d | 6 correctness items |
| 3 | 5-7d | 8 resilience items, incl. L outbox |
| 4 excl. FEAT-5 | 3d | 7 features |
| 4 FEAT-5 | 3d | Multi-item subs (Phase 3) |

**Total (Phase 2 = Waves 0-4 excl. FEAT-5): ~2.5 weeks of focused work.**
