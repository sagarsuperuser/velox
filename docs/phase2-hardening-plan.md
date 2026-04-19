# Velox Phase 2 Hardening Plan

Organized by risk × impact across 5 waves. Each item has effort size, rollout notes, tests, and dependencies.

## Legend
- **S** = 1-2 hours · **M** = half-day to 1 day · **L** = multi-day
- `[SEC]` security · `[COR]` correctness · `[RES]` reliability · `[FEAT]` feature · `[HYG]` hygiene · `[UI]` dashboard ui/ux
- `→X` = blocks X · `X→` = blocked by X

---

## Wave 0 — Schema hygiene (bundled with Wave 1) — S

One migration `0006_schema_hygiene.up.sql`. Online-safe (IF NOT EXISTS, no big-table rewrites).

| ID | Item | Notes |
|---|---|---|
| HYG-1 | `customers.email NOT NULL` | Check for NULL emails first; backfill `'unknown-{id}@placeholder.invalid'` before NOT NULL — ✅ DONE (backfilled `''` instead of placeholder, see resolution) |
| HYG-2 | Partial UNIQUE on active subscriptions | `UNIQUE(tenant_id, customer_id, plan_id) WHERE status IN ('active','trialing','past_due')` — ✅ DONE (scoped to real Velox status set, see resolution) |
| HYG-3 | `tax_rate NUMERIC(6,2)` → `tax_rate_bp BIGINT` | Dual-write → cutover → drop (2 migrations) — ✅ DONE (functional swap shipped in migration 0003; BIGINT widening in 0014) |
| HYG-4 | FK ON DELETE policies explicit | Customers→invoices RESTRICT; tenants→customers RESTRICT; audit refs PRESERVE — ✅ DONE (comprehensive sweep of all 60 NO ACTION FKs, see resolution) |
| HYG-5 | `audit_log` append-only | BEFORE UPDATE/DELETE trigger raising exception — ✅ DONE |
| HYG-6 | Schedule idempotency + payment-token cleanup | Hourly task in scheduler — ✅ DONE |

**HYG-4 resolution:** Migration `0015_fk_explicit_restrict` drops and re-adds all 60 foreign keys that previously defaulted to `NO ACTION` with explicit `ON DELETE RESTRICT`. The one intentional cascade — `feature_flag_overrides.flag_key → feature_flags.key` — is left untouched. `NO ACTION` and `RESTRICT` are semantically equivalent for non-deferrable FKs (which all of ours are — verified via `pg_constraint.condeferrable`), so the migration carries zero runtime behavior change; the value is documentation-as-code. Scoped beyond the plan's three named pairs (customers→invoices, tenants→customers, audit refs) to every FK because the risk `HYG-4` protects against — a future PR silently adding `ON DELETE CASCADE` to a money-path table and orphaning or auto-deleting billing rows — applies uniformly across the schema, and a partial fix would leave the schema lint inconsistent. Each `ALTER TABLE` combines the DROP + ADD in a single statement so no table is left without its FK during the transition. `pg_constraint` surfaced one FK my initial `information_schema` enumeration missed — `subscriptions_pending_plan_id_fkey` added in migration 0007 — a good argument for always cross-checking against `pg_constraint` when sweeping constraints. Integration test `TestFK_RestrictOnDelete` exercises three representative parent→child paths (customer→subscription, plan→subscription, tenant→customer), asserts each raises `23503 foreign_key_violation` with the expected "violates foreign key constraint" message, and verifies the cleanup path still works when children are removed first — proving RESTRICT blocks orphaning without over-blocking legitimate deletes.

**HYG-3 resolution:** The functional half of this item was already shipped: migration `0003_tax_cleanup` dropped the legacy `NUMERIC(6,2)` / `NUMERIC(5,4)` `tax_rate` columns on `tenant_settings`, `invoices`, and `invoice_line_items`, so all tax arithmetic runs through `tax_rate_bp` (integer basis points) end-to-end — no floats anywhere in the money path. That left a cosmetic gap: `tax_rate_bp` was `INT`, while every peer money column (`tax_amount_cents`, `subtotal_cents`, `total_amount_cents`, …) is `BIGINT`. INT4's ~21-million-percent ceiling is not a real overflow risk, but the schema inconsistency is the kind of "why is this one column different?" trap the HYG- items exist to remove. Migration `0014_tax_rate_bp_bigint` widens the three columns in-place (metadata-only ALTER on Postgres 12+, so safe at any table size) and Go-side `TaxRateBP` / `taxRateBP` fields + the `TaxOverrideRateBP *int` pointer flip to `int64` / `*int64` to match. No dual-write phase was needed because there was no legacy column left to co-exist with.

**HYG-2 resolution:** Migration `0013_subscriptions_active_unique` adds a partial UNIQUE index `subscriptions_one_live_per_customer_plan ON subscriptions(tenant_id, customer_id, plan_id) WHERE status IN ('active','paused')`. The plan doc referenced Stripe's `('active','trialing','past_due')` vocabulary, but Velox's actual status set is `{draft, active, paused, canceled, archived}` (see `internal/domain/subscription.go`) — `trialing` is derived in analytics from `active + trial_end_at > now()`, never stored. Scoped the partial predicate to Velox's real live statuses: `active` (obvious), `paused` (still owned by the customer-plan pair, mustn't be orphaned by a parallel active row). Excluded `draft` because multi-draft UI editing must remain legal, and terminal `{canceled, archived}` because re-subscribing after cancellation is a first-class flow. Added `postgres.UniqueViolationConstraint(err)` helper so the subscription store can disambiguate the new constraint from the pre-existing `(tenant_id, code)` one and surface a distinct `AlreadyExists("plan_id", ...)` message. Integration test exercises the seed-active → duplicate-active rejection, the paused-blocks-new path, and the cancel-frees-slot recovery path.

**HYG-1 resolution:** Migration `0012_customers_email_not_null` backfills NULL → `''`, sets `DEFAULT ''`, then adds `NOT NULL`. Writes in `internal/customer/postgres.go` stop passing `email` through `postgres.NullableString` so an empty string now inserts as `''` rather than NULL. Deviated from the plan's stated `'unknown-{id}@placeholder.invalid'` backfill: email is intentionally optional at the API layer (`service.go` guards with `if input.Email != ""`), and `COALESCE(email,'')` on every SELECT already erases the NULL/empty distinction on the read path — empty string is the existing semantic for "missing email", so collapsing the two on-disk representations to just `''` is the cleaner long-term fix. Fake placeholder addresses would risk leaking into outbound email, Stripe customer metadata, and operator reports. Integration test `TestCustomersEmail_NotNull` covers both the empty-string round-trip and the raw-SQL NULL rejection path.

**HYG-5 resolution:** Migration `0011_audit_append_only` adds a `BEFORE UPDATE OR DELETE … FOR EACH ROW` trigger on `audit_log` backed by a `plpgsql` function that unconditionally `RAISE EXCEPTION 'audit_log is append-only; % is not permitted'` with a `HINT` pointing operators at the retention-purge procedure (drop trigger → delete → recreate). The trigger fires regardless of RLS bypass, so a compromised application path or stray admin tool can't silently rewrite or erase evidence — RLS is the first line of defence, this trigger is the belt to the braces, and the RES-5 fail-closed writer ensures rows reach the table in the first place. Deliberately no session-variable escape hatch: retention cleanup is a DB-admin DDL event, and that DDL is already captured by Postgres' statement log, preserving the audit chain above the row level. Integration test `TestAuditLog_AppendOnly` verifies both operations are blocked and the underlying row survives unchanged.

**HYG-6 resolution:** `middleware.CleanExpired` now returns `(int, error)` and is exposed via a thin `middleware.IdempotencyCleaner` adapter matching the scheduler's `Cleanup(ctx) (int, error)` interface shape. The billing scheduler gains a sixth step in `runOnce` that deletes idempotency keys past their `expires_at`, mirroring the existing token-cleanup step. A `velox_scheduled_cleanup_rows_total{table}` counter (labeled per table) replaces ad-hoc logging-only cleanup visibility so operators can alert on surges per table. Wired in `cmd/velox/main.go` alongside the existing `TokenCleaner`.

---

## Wave 1 — Security

### [SEC-1] Close RLS bypass on 3 tables — M → unblocks W2 — ✅ DONE

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

**Resolution (pre-existing, verified 2026-04-19):** Migration `0006_close_rls_bypass.up.sql` enables RLS + FORCE + standard `tenant_id = current_setting('app.tenant_id', true)` policy on all three tables. Callers routed through `db.BeginTx(ctx, postgres.TxTenant, tenantID)`: `tenant/settings.go:62,83,115,174,201`; `middleware/idempotency.go:152,180`; `payment/token.go:48,109`. `dunning.ListTenantIDs` intentionally remains on `TxBypass` with an explanatory comment.

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

### [COR-1] Wire coupons → `invoice.discount_cents` — M → COR-2 — ✅ DONE

**Problem:** Coupon redemptions written; `invoice.discount_cents` never populated.

**Target:**
- New method: `coupon.Service.ApplyToInvoice(ctx, tenantID, invoiceID, subscriptionID) (discountCents, err)` — pure: consults active redemptions, returns the amount.
- Invoice finalize + billing engine call it before computing totals.
- Order: line_items → subtotal → apply discount → tax → total (Stripe-style).
- Line-item level vs invoice level: start at invoice-level (simpler), upgrade to line-item in FEAT-6.

**Schema:** no change.

**Tests:** percentage coupon on simple invoice; fixed-amount coupon; coupon exceeds subtotal (clamp to subtotal); expired coupon ignored; proration invoice with coupon.

**Resolution (pre-existing, verified 2026-04-19):** `coupon.Service.ApplyToInvoice(ctx, tenantID, subscriptionID, planID, subtotalCents)` at `coupon/service.go:194`. Billing engine calls it at `engine.go:606` before computing totals, clamping the discount to the subtotal. Percentage + fixed-amount + clamp-at-subtotal + expiry + inactive-coupon paths are all covered.

### [COR-2] Wire tax at finalize & proration — M — COR-1→ — ✅ DONE

**Problem:** `tax.Calculator` exists; never called. `invoice.tax_amount_cents` always 0.

**Target:**
- At finalize: `tax.Calculate(billing_address, subtotal_after_discount, currency) → (amount, rate_bp, name)` → write to invoice before total.
- Same on proration invoice creation (fixes the "proration is tax-free" bug).
- Add `tenant_settings.tax_inclusive BOOLEAN DEFAULT false`; if true, interpret prices as tax-inclusive (subtotal = sticker / (1+rate), tax = sticker - subtotal).
- `customer_billing_profiles.tax_id TEXT` for VAT number (B2B reverse-charge cases).

**Tests:** exclusive pricing; inclusive pricing; 0% jurisdiction; proration with VAT; VAT reverse-charge path (future).

**Resolution (pre-existing, verified 2026-04-19):** `tax.Calculator` wired in `engine.go:42` and called via `ApplyTaxToLineItems` at `engine.go:614`, after coupon discount and before total. Migration `0008` added `tenant_settings.tax_inclusive`; tax-inclusive mode back-calculates net subtotal/discount from gross inputs (see `engine.go:615-619`). `customer_billing_profiles.tax_id` exists in schema. VAT reverse-charge path is the "future" bullet and remains deferred.

### [COR-3] Plan change "at period end" becomes real — M — ✅ DONE

**Problem:** `subscription/service.go:226-238`: `EffectiveAt` set to period end but `sub.PlanID = NewPlanID` is written immediately — API lies.

**Target:**
- Migration `0008`: add `subscriptions.pending_plan_id TEXT, pending_plan_effective_at TIMESTAMPTZ`.
- `ChangePlan(immediate=false)`: write pending fields, do NOT touch `PlanID`.
- Billing engine cycle boundary: if `now >= pending_plan_effective_at`, apply (PlanID=pending, clear pending, record history, no proration).
- New endpoint: `DELETE /subscriptions/{id}/pending-change` to cancel a scheduled change.
- Idempotent: changing pending plan twice just overwrites.

**Tests:** scheduled downgrade applies on next cycle; cancel-pending restores state; immediate change still works; mid-period cancel clears pending.

**Resolution (pre-existing, verified 2026-04-19):** Migration `0007` adds `subscriptions.pending_plan_id` + `pending_plan_effective_at`. `subscription/service.go:228` writes pending fields when `immediate=false` without touching `PlanID`. Cycle-boundary apply is `engine.ApplyPendingPlanAtomic` at `engine.go:407`. Cancel endpoint `DELETE /subscriptions/{id}/pending-change` wired at `subscription/handler.go:149`. Idempotent.

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

### [COR-5] Fix truncating division in unit-amount + Stripe tax rate — S — ✅ DONE

**Problem:** `billing/engine.go:322-324` and `tax/stripe.go:98` use int division — cents lost.

**Target:** switch to `money.RoundHalfToEven`. Already shared after `0c3f4e0`.

**Tests:** 3 × $0.33 = $0.99 not $1.01; boundary cases.

**Resolution (2026-04-19):** `billing/engine.go:316` (tax per-line) and `engine.go:561` (unit-amount blended display) were already on `money.RoundHalfToEven`; `tax/stripe.go:102` (effective-rate derivation) too. The residual truncation was in the preview path — `billing/preview.go:105` still used `amount / quantity` for display. Fixed to `money.RoundHalfToEven(amount, quantity)` matching the finalize-side math, so the preview never under-displays the blended unit price for graduated/tiered plans (a systematic downward bias that would otherwise diverge from the actual invoice).

### [COR-6] Idempotency middleware caches 4xx & 5xx — S — ✅ DONE

**Problem:** `middleware/idempotency.go:109` only stores 2xx; transient 500 retry re-runs side effects.

**Target:** cache all responses except 409/422 (those signal "this isn't the real first response"). 24h TTL already in `expires_at`. Matches Stripe semantics.

**Tests:** retry after 500 replays 500; body fingerprint mismatch returns 422.

**Resolution (pre-existing, verified 2026-04-19):** `api/middleware/idempotency.go:119-124` caches every response except `409 Conflict` and `422 Unprocessable Entity` (the two codes that signal "this isn't the real first response"). 24h TTL enforced via `expires_at`. Plan doc line reference (109) was from an earlier revision.

---

## Wave 3 — Reliability

### [RES-1] Transactional outbox for outbound webhooks — L → unblocks RES-2, RES-6 — ✅ DONE

**Problem:** `payment/stripe.go:144-150` dispatches in `go func() { _ = ... }()`. Crash loses event.

**Target:**
- Table `webhook_outbox(id, tenant_id, event_type, payload, status, attempts, next_attempt_at, last_error, created_at, dispatched_at)` with partial index `WHERE status='pending'`.
- Producer writes to outbox in SAME tx as the state change (e.g., MarkPaid + outbox insert atomic).
- Dispatcher worker (leader-gated via RES-2): `SELECT ... FOR UPDATE SKIP LOCKED LIMIT 10 WHERE status='pending' AND next_attempt_at <= now()`; dispatch; update status/attempts/next_attempt_at with exponential backoff (1s, 5s, 30s, 5m, 1h, 4h, 12h × retry up to 72h).
- DLQ: after 15 attempts → `status='failed'`, metric emitted.
- Receiver dedups on `event_id` (Velox guarantees uniqueness).

**Rollout:** shadow mode first (write to outbox AND fire-and-forget) for 1 week to compare metrics; then cut over; then remove fire-and-forget.

**Tests:** crash between commit and dispatch → eventually delivered; 500 response → retry with backoff; 410 Gone → DLQ immediately; duplicate delivery on receiver side → idempotent.

**✅ Resolution (2026-04-19):**

Shipped infrastructure + full producer cutover, gated by `VELOX_WEBHOOK_OUTBOX_ENABLED` (default `true`; set to `false` for emergency rollback to the legacy direct-dispatch path).

- `internal/platform/migrate/sql/0016_webhook_outbox.{up,down}.sql` — table with partial index on `(next_attempt_at) WHERE status='pending'`, tenant+status index for operator UI, RLS policy matching every other tenant table.
- `internal/webhook/outbox.go` — `OutboxStore` with `Enqueue(ctx, tx, …)` (tx-coupled, lets producers persist the event in the same tx as their state change) and `EnqueueStandalone(…)` (self-tx wrapper for callers without a tx in scope). `ProcessBatch` claims with `FOR UPDATE SKIP LOCKED`, nil-handler → `dispatched`, error → retry-with-backoff or DLQ after `MaxOutboxAttempts = 15` (~72h total ramp).
- `internal/webhook/dispatcher.go` — background worker (`DispatcherConfig{Interval:2s, BatchSize:25, BatchTimeout:30s}`) that drains the outbox by calling `Service.Dispatch` for each claimed row. Registered alongside the existing billing scheduler + webhook retry worker in `cmd/velox/main.go` with graceful shutdown.
- `internal/webhook/outbox_dispatcher.go` — adapter implementing `domain.EventDispatcher`. Swapped in at wiring time for all three producer sites (payment/stripe, dunning/service, invoice/handler) via a single `eventDispatcher` variable in `internal/api/router.go`. The old pattern `go func() { _ = events.Dispatch(…) }()` was not just lossy on crash — it also captured the HTTP request ctx, which chi may cancel shortly after the handler returns; errors were silently swallowed. The synchronous rewrite (under each `fireEvent`) adds slog on failure and gives callers a persist-before-return guarantee.
- Tests: `internal/webhook/outbox_integration_test.go` — six cases covering standalone enqueue, tx rollback-vs-commit atomicity, successful dispatch, retry-with-backoff (second immediate pass sees 0 rows due to `next_attempt_at` future), DLQ transition after `MaxOutboxAttempts`, and terminal-state respect (DLQ rows never re-claimed), and accurate `PendingCount`/`FailedCount`. All six pass; full test suite green.

**Scope carried forward (not blocking RES-2):** The current pilot uses `EnqueueStandalone` from each `fireEvent` site — the insert is synchronous, but it is not tx-coupled to the business-op state change. True atomic enqueue (plan step 2) requires each business-op store method (`invoices.MarkPaid`, `dunning.StartDunning`, etc.) to accept an optional `*sql.Tx` the outbox insert can ride on. That refactor is per-producer and can land incrementally without touching the outbox contract. Shadow-mode comparison in the rollout plan is skipped: the interface-swap approach makes the cutover reversible via env var without shipping duplicate code paths.

### [RES-2] Scheduler advisory lock — M — ✅ DONE

**Problem:** Two app replicas both run `engine.RunCycle`.

**Target:**
- Wrap each tick's RunCycle / dunning sweep / outbox dispatch in `SELECT pg_try_advisory_lock($key)`; skip if false; release at tick end.
- Separate keys for `billing-scheduler`, `dunning-scheduler`, `outbox-dispatcher` → each can run on different leaders if we want.
- Outbox drain uses `FOR UPDATE SKIP LOCKED` (parallelism at row level, not leader level).
- Matches the pattern already in `platform/migrate/migrate.go`.

**Tests:** two scheduler instances → only one runs per tick; leader crash → lock auto-released, other picks up.

**✅ Resolution (2026-04-19):**

- `internal/platform/postgres/advisory_lock.go` — `DB.TryAdvisoryLock(ctx, key)` pins a `sql.Conn`, runs `pg_try_advisory_lock($1)`, and returns an `*AdvisoryLock` whose `Release()` calls `pg_advisory_unlock($1)` and closes the conn. Release uses `context.Background()` so shutdown-triggered ctx cancellation cannot strand a held lock.
- Session-scoped (not `pg_try_advisory_xact_lock`) because scheduler ticks span many independent txns — a tx-scoped lock would drop the moment the first query committed. Crash safety comes from TCP session death: Postgres auto-releases when the connection dies.
- Three key constants (`LockKeyBillingScheduler`, `LockKeyDunningScheduler`, `LockKeyOutboxDispatcher`) namespaced ≥ 76_540_000 to avoid colliding with the hash-derived keys golang-migrate picks.
- `internal/billing/scheduler.go` split into `runBillingHalf` (reconcile + auto-charge retry + RunCycle + credit expiry + reminders + token/idempotency cleanup) and `runDunningHalf`. Each half optionally gated by its own key via `Scheduler.SetLocker(locker, billingKey, dunningKey)`. Splitting lets two replicas divide roles — one takes billing, the other dunning.
- `internal/billing/lock_adapter.go` — `postgresLocker` implementing `billing.Locker` over `*postgres.DB`; keeps scheduler tests decoupled from the real DB via a fake locker.
- `internal/webhook/dispatcher.go` — optional `DispatchLocker`. `OutboxStore.TryDispatcherLock(ctx)` implements it. Row-level `FOR UPDATE SKIP LOCKED` still governs correctness; the lock just prevents two dispatchers from both polling every 2s when one suffices.
- `cmd/velox/main.go` wires `billing.NewPostgresLocker(db)` into the scheduler and `server.OutboxStore` into the dispatcher.
- Tests: `internal/platform/postgres/advisory_lock_test.go` (integration — exclusive acquire, distinct keys independent, idempotent Release) + `internal/billing/scheduler_lock_test.go` (unit — leader gate blocks follower's billing half, dunning half runs when free, skips when held, propagates infra errors, nil locker preserves single-replica mode). All pass.

### [RES-3] Safe handling of unknown Stripe results — M — ✅ DONE

**Problem:** `payment/stripe.go:179-207` marks `PaymentFailed` on any error — including 500/timeout where Stripe may have succeeded server-side.

**Target:**
- Distinguish explicit failure (Stripe 4xx + decline_code) from unknown (5xx, timeout, conn reset).
- Explicit failure → `PaymentFailed` with decline_code.
- Unknown → new enum `PaymentPending` → `PaymentUnknown`; store `stripe_payment_intent_id` (if returned) + `attempted_at`.
- Reconciler worker: for `PaymentUnknown` rows older than 60s, query Stripe by idempotency key or PI ID → resolve to succeeded/failed.
- Idempotency key on Stripe call: `inv_{invoice_id}_{attempt_number}` — Stripe returns same result on retry.

**Resolution:**
- `domain.PaymentUnknown` + migration 0009 widened CHECK constraint.
- Typed `*payment.PaymentError` returned by `LiveStripeClient.CreatePaymentIntent`; `classifyStripeError` maps stripe-go `ErrorType` to `Unknown` bool. Card / invalid-request / idempotency errors → definite failure; API (5xx) errors and any untyped error (context cancel, DNS, etc.) → Unknown. PI ID preserved from `stripe.Error.PaymentIntent`.
- `ChargeInvoice` branches: Unknown → `PaymentUnknown` + stash PI ID; definite → `PaymentFailed` (unchanged).
- `payment.Reconciler` runs from scheduler step 0a (before auto-charge retries). Calls `paymentintent.Get` per unresolved unknown older than 60s; maps `succeeded`→MarkPaid, `canceled`/`requires_payment_method`→PaymentFailed, `processing`/`requires_action`/etc.→leave for next tick. Cool-off lets webhooks resolve first (cheaper path).
- No idempotency-key-based recovery path yet. Invoices without a PI ID are marked failed (we can't query Stripe). This is acceptable because our current idempotency key is deterministic (`velox_inv_{invoice_id}_{pm_id}`) — a subsequent manual retry with same key returns the same server-side result.

**Tests:** 3 ChargeInvoice branches (definite/unknown/untyped-fail-safe); 5 reconciler scenarios (succeeded, canceled, still-in-flight, no-PI, stripe-5xx-during-reconcile).

### [RES-4] Inbound Stripe webhook dedup tightened — S — ✅ DONE

**Problem:** `payment/stripe.go:212-219`: reads dedup then acts; concurrent delivery can race the first handler's in-flight processing.

**Target:**
- `INSERT INTO stripe_webhook_events (...) ON CONFLICT (tenant_id, stripe_event_id) DO NOTHING RETURNING id` → row returned means "we own it"; no row means "duplicate, return 200 with no-op".
- Process handler after dedup commits (or inside same tx — both safe).

**Resolution:** `payment/webhook_store.go:23-76` already implements the atomic insert — ON CONFLICT DO NOTHING RETURNING id inside TxTenant, `sql.ErrNoRows` → `isNew=false`. `stripe.go:210-219` acts only on `isNew=true`. UNIQUE (tenant_id, stripe_event_id) exists at `migrate/sql/0001_schema.up.sql:597`. Added concurrent-delivery integration test (`webhook_store_integration_test.go`) — 16 racers, exactly one insert wins, N-1 see `isNew=false`, no errors, one row persisted. Stable under `-race`.

### [RES-5] Audit log fail-closed opt-in — S — ✅ DONE

**Problem:** `middleware/audit.go:195-236`: all failures log-and-swallow.

**Target:**
- `tenant_settings.audit_fail_closed BOOLEAN DEFAULT false`.
- If true, audit INSERT failure → 503 `audit_error` returned to caller.
- Always emit `audit_write_errors_total{tenant_id}` metric.
- SOC-2-bound tenants opt in.

**Tests:** fail-open default → request succeeds, metric incremented; fail-closed → 503.

**Resolution:** Migration 0010 adds `tenant_settings.audit_fail_closed` (default FALSE). `domain.TenantSettings.AuditFailClosed` wired through `SettingsStore.Get`/`Upsert`. New `SettingsStore.IsAuditFailClosed` hot-path lookup used by the audit middleware. Middleware rewritten to buffer the handler response (new `bufferedResponse`); on audit write failure it emits `velox_audit_write_errors_total{tenant_id}` (replaces the unlabeled `velox_audit_failures_total`) and branches on `IsAuditFailClosed`:
- fail-open: logs + flushes handler response (availability preserved).
- fail-closed: returns `503 {"error":{"code":"audit_error"...}}`; handler headers/body dropped. A settings-lookup error fails **safe to closed** so a broken lookup can't silently downgrade a SOC-2 tenant. Write path refactored behind a narrow `auditWriter` interface for unit-testability; `postgresAuditWriter` is the production impl. Also consolidated the duplicate counter in `internal/audit/audit.go` onto the same labeled metric. Tests (`audit_test.go`) cover success, fail-open + metric, fail-closed → 503, settings-lookup-error fails-safe-closed, non-2xx skip, GET bypass. Full unit suite green.

### [RES-6] Email delivery outbox + DLQ — M — RES-1→

**Target:** reuse outbox pattern with `email_outbox` table. Backoff, DLQ, metrics.

### [RES-7] Dunning circuit breaker + payment-retry timeout — S — ✅ DONE

**Target:** per-tenant breaker around Stripe calls; opens after N consecutive 5xx in window, rejects for cooldown. 15s timeout on `RetryPayment`. Manual reset endpoint. Metric `stripe_breaker_state{tenant_id}`.

**Resolution:** Thin wrapper in `internal/payment/breaker/` over `sony/gobreaker/v2` keyed per-tenant. `FailureThreshold=5` consecutive Unknown outcomes (5xx / timeout / network) trip; `Cooldown=30s` before half-open probe; `Interval=60s` clears counts in the closed state. Card declines and validation errors are routed through gobreaker's `IsExcluded` so merchant-side problems don't open the breaker. `payment.IsUnknownPaymentFailure` is the shared classifier. `Stripe.ChargeInvoice` and `Reconciler.reconcileOne` both route Stripe calls through `Breaker.Execute`; on `breaker.ErrOpen` they return `payment.ErrPaymentTransient` without mutating invoice state. `paymentRetrierAdapter.RetryPayment` wraps the Stripe call in a 15s `context.WithTimeout` and translates `ErrPaymentTransient` to `dunning.ErrTransientSkip`, which `dunning.processRun` detects to decrement `AttemptCount` and return nil (the tick didn't really happen). `velox_stripe_breaker_state{tenant_id}` gauge is driven by the breaker's `OnStateChange` hook (0=closed, 1=half_open, 2=open). Admin endpoints: `GET /v1/integrations/stripe/breaker` returns current state; `POST /v1/integrations/stripe/breaker/reset` forces-closed for the caller's tenant (scoped by auth context — no cross-tenant reset). 9 unit tests in `breaker_test.go` cover opens-after-N, card-declines-excluded, cooldown → half-open probe, half-open-failure reopens, manual reset, tenant isolation. The preference for battle-tested libs over custom implementations came out of this item and is now a durable user preference.

### [RES-8] PII scrubbing for logs + error-column storage — S — ✅ DONE

**Target:** `errs/scrub.go` regex redactor for card fragments, emails, customer IDs in Stripe error messages. Applied at persistence + log boundaries.

**Tests:** "Card ending in 4242" → "Card ending in ****".

**Resolution:** `internal/errs/scrub.go` exposes a single idempotent `Scrub(s string) string` that redacts card last4 embedded in free text (`"ending in 4242"` → `"ending in ****"`), raw 13–19 digit PAN-like runs (`→ "[REDACTED_CARD]"`), and RFC-5322-ish emails (`→ "[REDACTED_EMAIL]"`). Stripe correlation IDs (`pi_*`, `cus_*`, `pm_*`, `ch_*`) and decline codes are deliberately preserved — they're not PII and operators rely on them for triage. Scrub runs at ingress, not at each downstream boundary: `stripe_client.stripeErrorMessage()` and both bare-`PaymentError` constructors pipe their output through `Scrub`, and `handler.handleStripeWebhook` scrubs `obj.LastError.Message` before building the `StripeWebhookEvent`. Every downstream surface — `invoices.last_payment_error`, `stripe_webhook_events.failure_message`, slog `"error"` fields, HTTP error bodies — is therefore automatically clean without each callsite having to remember. 19 unit test cases in `scrub_test.go` cover last4 variants, raw card numbers, emails with `+` tags and parenthetical wrapping, combined PII messages, and confirm Stripe IDs survive untouched. Idempotence asserted separately.

---

## Wave 4 — Feature completeness

### [FEAT-1] Credit notes reduce `invoice.amount_due` — S — ✅ DONE

On `Issue`, call `invoice.ApplyCreditNote` (method exists). Clamp at invoice total.

**Resolution:** `creditnote/service.go:274` calls `invoices.ApplyCreditNote(ctx, tenantID, cn.InvoiceID, cn.TotalCents)` during Issue for unpaid invoices. `invoice/postgres.go:275-303` clamps via `GREATEST(amount_due_cents - $1, 0)`. Test assertion added in `creditnote/service_test.go` to verify amount_due drops after Issue.

### [FEAT-2] Direct refund endpoint — M

`POST /invoices/{id}/refund` → Stripe refund + credit note + invoice update. Partial refunds. Reason enum (duplicate, fraudulent, requested_by_customer, other).

### [FEAT-3] Customer payment-method self-service — M

`/v1/me/payment-methods` via portal session. Stripe SetupIntent for 3DS. Endpoints: list, set default, remove. Wire to `web-v2`.

### [FEAT-4] Price overrides consumed at rating — S — ✅ DONE

`billing.engine` rating helper: look up `customer_price_overrides` first, fall back to plan default. Unit test with override + without.

**Resolution (pre-existing, verified 2026-04-19):** `e.pricing.GetOverride(ctx, tenantID, customerID, ratingRuleVersionID)` is called at `billing/engine.go:544` (finalize path) and `engine.go:94` (preview path) before `ComputeAmountCents`. When an active override is found, `override.ToRatingRule()` replaces the default rule; otherwise the default is used. `Pricing` interface defined at `engine.go:99` requires the `GetOverride` method.

### [FEAT-5] Multi-item subscriptions — L (Phase 3)

Schema: `subscription_items(id, subscription_id, plan_id, quantity, price_override_id)`. Subscription PlanID deprecated → set of items. Rating/billing iterates. Proration per item. Plan change = item change. Portal updated.

**Deferred:** 3-day initiative; not a Phase 2 blocker.

### [FEAT-6] Coupon duration + stacking — M — COR-1→

Add `coupons.duration` enum (once/repeating/forever), `coupons.duration_periods INT` (for repeating). Stacking rules per coupon. `coupon_redemptions` tracks periods applied.

### [FEAT-7] Usage backfill API — S — ✅ DONE

`POST /usage-events/backfill` accepting past timestamps, tagged `origin='backfill'` for audit.

**✅ Resolution (2026-04-19):**

- Migration `0017_usage_events_origin` adds `origin TEXT NOT NULL DEFAULT 'api' CHECK (origin IN ('api','backfill'))` to `usage_events`. No index on origin — aggregation doesn't filter by it, and an index would cost writes without benefit until a future operator UI needs it.
- `domain.UsageEvent.Origin` + `UsageOriginAPI` / `UsageOriginBackfill` constants; the existing `Ingest` path defaults to `api`, `Service.Backfill` tags `backfill`.
- `Service.Backfill(ctx, tenantID, input)` requires a non-nil timestamp and rejects future timestamps. Past / now / near-now accepted — strict-past was trialled but made tests brittle against clock drift, and the origin tag already distinguishes rows for audit.
- `Handler.Backfill` mounted at `POST /v1/usage-events/backfill` behind `PermUsageWrite` (ahead of the `/usage-events` subtree so the more-specific route wins).
- Billing interaction: backfilled events participate in `AggregateForBillingPeriod` the same as live events. Finalized invoices are immutable (they reference `billed_entries`, not live aggregations), so backfill into a closed period updates the audit ledger without rewriting history.
- Tests: unit (`TestBackfill` + extended `TestIngest`) cover missing / future timestamps, origin tagging, and default-origin-on-live-ingest; integration (`TestBackfill_PersistsOriginAndAggregates`) confirms the SQL round-trips `origin` correctly and aggregation sums `api` + `backfill` quantities together.

### [FEAT-8] Test mode / sandbox — M

New key namespace `sk_test_*` / `pk_test_*`. Stripe calls replaced with stub in test mode. Isolated invoices marked `livemode=false`. Stripe convention.

---

## Wave 5 — Dashboard UI/UX (design-partner bar)

Audit of `web-v2/` against Stripe/Linear/Vercel produced 38 items across 6 themes; only the 6 P0s (blocks a design-partner demo) are planned for Phase 2. P1 polish and P2 nice-to-haves are deferred to Phase 3.

**Governing principles (from CLAUDE.md + prior feedback):**
- All actions visible from first render — no hover-to-reveal.
- No low-contrast text on money, status, or IDs.
- Destructive actions require explicit confirmation (AlertDialog is already consistent — keep it that way).
- Match existing dark-mode quality (oklch palette in `web-v2/src/index.css:101-148`) — the UI bar already set by Dashboard/Analytics skeletons + CMD+K palette.

### [UI-1] Skeleton table rows across list pages — S

**Problem:** List pages (`Customers.tsx:114-120`, `Invoices.tsx:79-82`, `Subscriptions.tsx`, `ApiKeys.tsx:104-107`, `Coupons.tsx`, `Credits.tsx`, `CreditNotes.tsx`, `Pricing.tsx`, `Dunning.tsx`, `Webhooks.tsx`, `AuditLog.tsx`) render a centered `<Loader2 animate-spin />` while the initial fetch is pending. Dashboard + Analytics already use `CardSkeleton.tsx`. Inconsistent loading UX + layout shift when rows arrive.

**Target:**
- New component `web-v2/src/components/ui/TableSkeleton.tsx` — N ghost rows with column-shaped pulse bars, parameterized by column count.
- Replace spinner in every list page with `<TableSkeleton columns={...} rows={10} />` during the initial load.
- Keep spinner for explicit action states (form submit, CSV export) — not for initial data.

**Tests:** visual regression via Playwright screenshot on each list page's loading state (deferred — add to test strategy when Playwright lands).

### [UI-2] Empty-state screens with primary CTA — M

**Problem:** Zero list pages render a "no results" screen. If the tenant has no invoices, `Invoices.tsx:114` just shows an empty `<TableBody>`. Worse: `ApiKeys.tsx:135-140` *hides* the "Create API Key" button when the list is empty — exactly the inverse of what the user needs. `Coupons.tsx:92`, `Subscriptions.tsx:139`, `Customers.tsx:150` have the same no-content-no-guidance issue.

**Target:**
- New component `web-v2/src/components/EmptyState.tsx` — icon + heading + sub-copy + primary CTA. Model on the styled empty card in `UpdatePayment.tsx:70-77`.
- Render on every list page when `items.length === 0 && !loading`. Each page supplies its own heading/copy/CTA.
- **Fix `ApiKeys.tsx:135`**: always show the "Create API Key" button. Empty state becomes "No keys yet — create your first one" + CTA.
- Apply consistently: Customers, Invoices, Subscriptions, Coupons, Credits, CreditNotes, Plans, Meters, Webhooks (Endpoints tab), UsageEvents.

**Tests:** snapshot test per list page with empty and non-empty data sets.

### [UI-3] Persist filter/sort/page to URL query params — M

**Problem:** `Customers.tsx:106-112`, `Invoices.tsx:71-77`, `Subscriptions.tsx:116-122`, `UsageEvents.tsx`, `AuditLog.tsx` all build query params for their fetch calls but never push them into `location.search`. Refresh loses everything; URLs aren't shareable; back button doesn't restore table state. Stripe / Linear all persist.

**Target:**
- New hook `web-v2/src/hooks/useQueryState.ts` — typed wrapper over `useSearchParams()` that round-trips `{ page, sort, order, status, q, dateRange }` to the URL and back into state.
- Convert every list page to the hook. Page state becomes `const [q, setQ] = useQueryState({ defaults })`.
- URL shape: `/invoices?status=paid&sort=created_at&order=desc&page=2&q=acme`.
- Debounce the search-box write (300ms) to avoid URL thrashing on every keystroke.

**Tests:** e2e: apply filter → reload → filter still applied; share URL between two tabs → same results.

### [UI-4] Inject API errors into form fields — M

**Problem:** On create/edit, API errors (e.g., `409 Conflict: plan code already exists`, `422: coupon percent must be <= 100`) are surfaced only as `toast.error()`. The user sees a red toast but no hint *which* field is wrong. `Pricing.tsx` plan/meter/rule forms are worst offenders; `Customers.tsx`, `Subscriptions.tsx`, `Coupons.tsx` also affected.

**Target:**
- New util `web-v2/src/lib/formErrors.ts` — parses a Velox API error envelope (`{ error: { code, message, field? } }`) and returns a map `{ [fieldName]: message }`.
- New hook or util `applyApiErrors(form, error)` that calls `form.setError(field, { message })` for each field the API flagged.
- Every form's `onSubmit` handler: on error, call `applyApiErrors` first; only fall through to `toast.error()` for errors without a `field`.
- **Server side, if missing**: audit Velox API responses to ensure conflict/validation errors include the `field` pointer. Add where absent. (This is an API contract cleanup — may spawn a small backend commit.)

**Tests:** submit Pricing form with duplicate code → error appears under the code input, not a toast; submit Coupon with percent=120 → error appears under percent input.

### [UI-5] Extend expiry-urgency badge pattern — S

**Problem:** `ApiKeys.tsx:82-88` has a well-crafted "expires in N days" urgency badge that turns amber at ≤7 days. The same pattern is absent on:
- Invoices past due / approaching due (`InvoiceDetail.tsx`)
- Subscriptions with trial ending soon (`SubscriptionDetail.tsx`, Subscriptions list)
- Coupons near expiry (`Coupons.tsx` list)

**Target:**
- Extract `ApiKeys.tsx:82-88` into `web-v2/src/components/ExpiryBadge.tsx` — takes `expiresAt: Date | null` and an optional `warningDays` threshold (default 7).
- Apply to: invoice due dates (`Invoices.tsx` row, `InvoiceDetail.tsx` header), subscription trial end, coupon expiry in list + detail views.
- Consistent color scale: grey = no expiry, green = >30d, amber = ≤7d, red = expired.

**Tests:** component test for each threshold boundary (31d, 30d, 8d, 7d, 1d, 0d, -1d).

### [UI-6] Extract shared UI primitives — S — (UI-1, UI-2 →)

**Problem:** Three pages reimplement copy-to-clipboard inline (`CustomerDetail.tsx:35-45`, `InvoiceDetail.tsx:35-45`, `ApiKeys.tsx:337-340`). Breadcrumb back-links are hand-rolled on every detail page. `CustomerDetail.tsx:275-300` has five same-weight buttons with no visible primary.

**Target:**
- `web-v2/src/components/ui/CopyButton.tsx` — icon + toast feedback + timeout-reset check icon. Replace all three inline implementations.
- `web-v2/src/components/ui/BackLink.tsx` — arrow + label + href. Use on all detail pages (`CustomerDetail`, `InvoiceDetail`, `SubscriptionDetail`, `PlanDetail`, `MeterDetail`).
- `CustomerDetail.tsx:275-300`: reduce to one primary button (`Create Subscription`), demote the other four to secondary/ghost variants for clear visual hierarchy.

**Tests:** component tests for CopyButton (clipboard mock) and BackLink (href rendering).

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

UI-1..5 independent of backend; can run in parallel with Wave 2/3
UI-6 ← UI-1, UI-2 (uses primitives those waves create)
```

---

## Recommended commit order

1. **SEC-1 + HYG bundle** — security wave, one migration ✅
2. **COR-4** — three concurrency fixes (3 commits, quick wins) ✅
3. **COR-5** — rounding (trivial) ✅
4. **COR-6** — idempotency 4xx/5xx caching ✅
5. **UI-1** — skeleton table rows (fast win, visible)
6. **UI-2** — empty states with CTAs (fixes ApiKeys inversion)
7. **COR-1** — coupons → invoice discount ✅
8. **COR-2** — tax at finalize + proration ✅
9. **UI-3** — URL state persistence
10. **COR-3** — real at-period-end plan change ✅
11. **UI-4** — form API error injection (+ any backend error-envelope cleanup)
12. **FEAT-1** — credit note → amount_due ✅
13. **UI-5** — expiry-urgency badge extended
14. **UI-6** — shared primitives (CopyButton, BackLink, CustomerDetail hierarchy)
15. **SEC-2 Phase A** — webhook secret dual-write
16. **RES-4** — inbound dedup tightened ✅
17. **RES-3** — ChargeInvoice unknown-state ✅
18. **RES-5** — audit fail-closed opt-in ✅
19. **RES-7** — dunning breaker ✅
20. **RES-8** — PII scrubbing ✅
21. **HYG-6** — scheduled cleanup ✅
22. **HYG-5** — audit_log append-only trigger ✅
23. **HYG-1** — customers.email NOT NULL ✅
24. **HYG-2** — partial UNIQUE on live subscriptions ✅
25. **HYG-3** — tax_rate_bp widened to BIGINT ✅
26. **HYG-4** — explicit FK ON DELETE RESTRICT across schema ✅
27. **RES-1** — transactional outbox (L) ✅
28. **RES-2** — scheduler advisory lock ✅
29. **RES-6** — email outbox
30. **SEC-2 Phase B/C** — cutover + drop plaintext
31. **FEAT-2, FEAT-3, FEAT-4 ✅, FEAT-7 ✅, FEAT-8** — feature completeness
32. **FEAT-6** — coupon stacking
33. **FEAT-5** — multi-item subs (Phase 3)

**UI parallelization note:** UI-1..5 are independent of backend correctness work. A frontend-focused contributor can ship UI-1 → UI-2 → UI-3 → UI-5 → UI-6 in a single thread while backend commits land. UI-4 has a soft dependency on the backend error-envelope audit (small item) — slot it after COR-2 so any API fixes can be bundled.

---

## Test strategy

- **Wave 0/1**: migration up/down tests; RLS violation assertions; secret rotation e2e
- **Wave 2**: invariant tests (`invoice.total = subtotal + tax - discount` always); race tests (goroutines); plan-change scheduled integration; rounding boundary tables
- **Wave 3**: failure-injection (crash after commit, Stripe 500/timeout), webhook replay, lock contention, breaker state transitions
- **Wave 4**: feature e2e per endpoint
- **Wave 5**: component tests for new primitives (TableSkeleton, EmptyState, CopyButton, BackLink, ExpiryBadge); hook test for `useQueryState` (round-trip URL ↔ state); visual-regression snapshots per list page (loading + empty + populated); form-error injection test across representative forms (Customers, Pricing plan, Coupon). Playwright e2e added here if not already present.

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
| 5 | 4-5d | 6 UI/UX P0s (design-partner bar) |

**Total (Phase 2 = Waves 0-5 excl. FEAT-5): ~3 weeks of focused work.** UI wave can run in parallel with Waves 2/3 by a second contributor — elapsed-time closer to 2 weeks with two threads.

## Deferred UI items (Phase 3)

Audit produced 38 items total; 6 P0s are in Wave 5 above. The remaining **22 P1** and **10 P2** items form the Phase 3 polish pass. Highlights of what's deferred:

- **Accessibility pass** — ARIA labels on icon buttons (Coupons.tsx:556, table row actions), skip-to-content link, focus-ring audit, modal Esc-key verification, color-blind simulator check on status badges.
- **Mobile responsiveness** — list pages' tables on <640px (stacked row view vs. side-scroll), modal viewport tests, hamburger keyboard nav. `UpdatePayment.tsx` is already mobile-polished; other pages are not.
- **Keyboard shortcut expansion** — J/K next/prev row, E export, F filter. CMD+K palette exists (`CommandPalette.tsx:50-150`) but has hardcoded 10-result limits and no "search all".
- **Bulk actions on table rows** — Invoices bulk-void/email, Credits bulk-grant. Requires selection state + action bar.
- **Per-page detail 404s** — `main.tsx:14-50` has an app-level ErrorBoundary but detail pages crash on invalid IDs rather than showing a "not found" card.
- **CustomerDetail action hierarchy** (`CustomerDetail.tsx:275-300`) — partial fix in UI-6; full visual overhaul deferred.
- **Archive flow consistency** (`CustomerDetail.tsx:250-264` uses a Card banner for archive; should be an AlertDialog like other destructive actions).
- **Tooltip-everywhere pass** — icon-only buttons without labels (Coupons toggle, table-row icons).
- **Advanced command-palette syntax** — `status:failed`, `customer:acme`.
- **Status legend / glossary page** — help new users understand "finalized" vs. "paid" vs. "processing".

See the web-v2 audit in conversation for full file/line references.
