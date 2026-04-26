# Billing Alerts — Technical Design

> **Status:** Draft v1
> **Owner:** Track A
> **Last revised:** 2026-04-26
> **Implementation window:** Week 5d of `docs/90-day-plan.md`
> **Related:** `docs/design-create-preview.md` (sibling — same composition + always-array conventions), `docs/design-customer-usage.md` (the read surface alerts evaluate against), `docs/design-multi-dim-meters.md` (Week 2 dependency — `usage.AggregateByPricingRules` is the trigger evaluator's engine), `docs/positioning.md` pillar 1.4 (cost transparency)

## Motivation

Stripe ships [Billing Alerts](https://stripe.com/docs/billing/alerts) as a Tier 1 surface — operators set "alert me when this customer's bill exceeds $X this cycle" and the system fires a webhook + dashboard notification when usage crosses the threshold. AI tenants in particular ask for this constantly because per-token spend can scale unexpectedly and a slow Slack alert prevents an unbounded bill before the cycle closes. Velox already has the read surfaces (`/v1/customers/{id}/usage`, `/v1/invoices/create_preview`) and the durable webhook outbox; alerts are the orchestration layer that turns them into proactive notifications.

This slice ships:

1. **Alerts CRUD** — `POST /v1/billing/alerts` to create, `GET /v1/billing/alerts/{id}` to read one, `GET /v1/billing/alerts` to list with filters, `POST /v1/billing/alerts/{id}/archive` to soft-disable.
2. **Trigger evaluator** — a background goroutine that scans active alerts, evaluates each against `usage.AggregateByPricingRules` for the customer's current cycle, and emits `billing.alert.triggered` via the webhook outbox once the threshold crosses.
3. **Recurrence semantics** — `one_time` alerts fire once per alert lifetime; `per_period` alerts fire once per billing cycle and reset on the cycle boundary so the operator gets a fresh signal each month.
4. **Atomic emission** — the alert's state mutation (status flip + trigger row insert) and the outbox enqueue happen in the same Postgres tx so a partial commit can't leave a webhook with no audit trail or vice versa.

The dashboard consumer is the existing CostDashboard's notifications panel; it polls `GET /v1/billing/alerts?customer_id=…&status=triggered` to surface unread alerts inline. Track B will follow up with a "Set alert" button on the cost dashboard that mints the alert via this endpoint.

## Goals

- **Stripe Tier 1 parity** for the simple "alert me at $X" use case. Threshold is either dollar-cents (`amount_gte`) or raw quantity (`usage_gte`) — both are common ("alert at $50 spent" vs "alert at 1M tokens consumed").
- **Customer-scoped first**. v1 alerts are always tied to a `customer_id`; tenant-wide ("alert me when ANY customer crosses $1000 this cycle") is a separate, more-complex surface — defer.
- **Optional meter scope**. An alert can target one specific meter (`filter.meter_id`) or aggregate across all meters on the customer's plan (omit `filter.meter_id`). Single-meter alerts evaluate against that meter's rule resolution; cross-meter alerts evaluate against the customer's total amount across the cycle (same number `customer-usage` reports).
- **Optional dimension filter**. Multi-dim meters (Week 2) let pricing rules carry `dimension_match` — alerts can narrow further: "alert me when GPT-4 input token spend on customer X exceeds $50". Implemented as a strict-superset match on rule `dimension_match`.
- **Reliable emission via outbox**. Trigger evaluation reads, decides to fire, mutates state and enqueues the webhook all inside one tx. If the tx rolls back, no webhook fires; if it commits, the dispatcher will eventually deliver. Same atomicity contract every other event-emitting domain uses.
- **Idempotent recurrence**. `per_period` alerts emit at most one event per (alert, cycle) — even across crashes / replica failovers / replays of the evaluator. The trigger row's UNIQUE constraint on `(alert_id, period_from)` is the source of truth.
- **Always-array / always-object wire shape**. Same conventions as customer-usage and create_preview — `alerts[]` is a JSON array even when empty; `dimension_match` is a JSON object even when empty (the rule's "match anything" identity); decimals serialize as JSON strings.

## Non-goals (deferred)

- **Tenant-wide alerts.** Stripe has them; we don't (yet). The use case is real but the evaluator topology is different (one alert vs N customers; needs cross-customer LATERAL JOINs); ship customer-scoped first to validate the contract.
- **Multi-recipient alerts / Slack / SMS.** v1 emits a webhook + a dashboard notification. The tenant wires their own Slack via the outbound webhook (their server consumes `billing.alert.triggered` and posts to whatever notification channel they want). v2 may add direct-Slack as a sugar layer.
- **Threshold ramps / multi-tier alerts.** "Alert me at $50, then again at $100, then again at $200" is one alert with multiple thresholds in Stripe's surface. v1 is one alert = one threshold; an operator who wants three thresholds creates three alerts. Cleaner shape, fewer footguns.
- **Forecasting alerts.** "Alert me when projected end-of-cycle bill > $X" requires the full `create_preview` path; v1 only fires on observed usage. Deferred to v2 once create_preview is dashboard-stable. Same data, different trigger condition — a one-line evaluator change when we get there.
- **Backfill triggers.** When an alert is created mid-cycle and the customer is already past the threshold, v1 fires immediately on the next evaluator tick (≤ 5 min). It does NOT replay historical "would have fired N hours ago" events — operators creating an alert mid-cycle accept the as-of-now interpretation.
- **Per-alert rate limiting.** A `per_period` alert only fires once per cycle by construction, so spam isn't possible. We don't need a separate rate-limit knob.

## API surface

> **Wire-contract conventions** (consistent with `/v1/*`, see `docs/design-customer-usage.md`):
>
> - **Snake-case JSON keys**, struct-tag enforced.
> - **Customer/meter/alert identity = ID** (`vlx_cus_…`, `vlx_mtr_…`, `vlx_alrt_…`).
> - **Empty arrays are `[]`**, never `null`.
> - **Empty objects are `{}`**, never `null` (e.g. `dimensions`).
> - **Decimals are JSON strings** per ADR-005 (`"1234567.000000000000"`).
> - **Cents are integers**.
> - **Recurrence enum:** `one_time` | `per_period`.
> - **Status enum:** `active` (armed, awaiting threshold) | `triggered` (one_time, fired and done) | `triggered_for_period` (per_period, fired this cycle, will rearm next cycle) | `archived` (soft-disabled by operator).
> - **Threshold mode:** exactly one of `amount_gte` (integer cents) or `usage_gte` (decimal-string quantity). Both null → 422; both non-null → 422.

### `POST /v1/billing/alerts`

Creates an alert.

```http
POST /v1/billing/alerts
Authorization: Bearer <secret_key>
Content-Type: application/json

{
  "title": "GPT-4 cost cap for ACME",
  "customer_id": "vlx_cus_abc123",
  "filter": {
    "meter_id": "vlx_mtr_tokens",
    "dimensions": { "model": "gpt-4", "operation": "input" }
  },
  "threshold": {
    "amount_gte": 50000
  },
  "recurrence": "per_period"
}
```

Request body:
- `title` (string, required, ≤ 200 chars) — display name for the dashboard.
- `customer_id` (string, required) — RLS-scoped; cross-tenant IDs surface as 404.
- `filter` (object, optional):
  - `filter.meter_id` (string, optional) — when set, the alert evaluates only that meter's resolved spend. When omitted, the alert evaluates against the customer's total cycle spend (sum across all meters).
  - `filter.dimensions` (object, optional) — strict-superset match against rule `dimension_match`. Only meaningful when `filter.meter_id` is set (cross-meter dimension filtering doesn't have a well-defined evaluator). Empty object `{}` is equivalent to omitted.
- `threshold` (object, required) — exactly one of:
  - `threshold.amount_gte` (integer cents) — fires when observed amount-cents ≥ this. Currency is implicit from the customer's billing profile (or first rated rule's currency for cross-meter alerts).
  - `threshold.usage_gte` (decimal-string quantity) — fires when observed quantity ≥ this. Only meaningful with a `filter.meter_id`; rejected with 422 otherwise (cross-meter quantity sums are not well-defined when meters have different units).
- `recurrence` (string, required) — `one_time` or `per_period`.

Response `201`:

```json
{
  "id": "vlx_alrt_xyz789",
  "title": "GPT-4 cost cap for ACME",
  "customer_id": "vlx_cus_abc123",
  "filter": {
    "meter_id": "vlx_mtr_tokens",
    "dimensions": { "model": "gpt-4", "operation": "input" }
  },
  "threshold": {
    "amount_gte": 50000,
    "usage_gte": null
  },
  "recurrence": "per_period",
  "status": "active",
  "last_triggered_at": null,
  "last_period_start": null,
  "created_at": "2026-04-26T12:00:00Z",
  "updated_at": "2026-04-26T12:00:00Z"
}
```

Notes on the response:
- `threshold.amount_gte` and `threshold.usage_gte` are both always present (one is null) — the always-object idiom lets clients read both without conditional indexing.
- `filter.dimensions` is always an object (`{}` when no filter) per always-object idiom.
- `last_triggered_at` and `last_period_start` are null until the alert fires for the first time.

### `GET /v1/billing/alerts/{id}`

Reads a single alert. Same response shape as create. 404 if not found (RLS hides cross-tenant IDs by construction).

### `GET /v1/billing/alerts`

Lists alerts. Optional query params:

- `customer_id` (string) — filter to one customer.
- `status` (string) — filter to one status (`active`, `triggered`, `triggered_for_period`, `archived`).
- `limit` (int, default 50, max 200) — pagination cap.
- `offset` (int, default 0).

Response `200`:

```json
{
  "data": [
    { ...alert object... },
    { ...alert object... }
  ],
  "total": 7
}
```

Standard list-envelope shape (`data` + `total`) used by every paginated `/v1/*` surface. Empty result is `data: []` not null.

### `POST /v1/billing/alerts/{id}/archive`

Soft-disables an alert. The alert's `status` flips to `archived`; the evaluator skips archived alerts on every subsequent tick. Idempotent — archiving an already-archived alert returns the same shape with no error.

Response `200` is the updated alert object.

### Error shapes

- `400 invalid_request` — body unparseable, malformed JSON.
- `422 validation_error` (with `param`) — required field missing (`title`, `customer_id`, `recurrence`, `threshold`), threshold has both / neither of (`amount_gte`, `usage_gte`), `usage_gte` set without `filter.meter_id`, unknown `recurrence` value, `dimensions` value not a scalar.
- `404 not_found` — `customer_id` doesn't exist for this tenant; `meter_id` doesn't exist; alert ID on read/archive doesn't exist.
- `409 already_exists` — not used in v1; alerts have no unique business key.

## Webhook payload

When the evaluator fires, it enqueues a `billing.alert.triggered` event into `webhook_outbox` inside the same tx that flips the alert's status:

```json
{
  "id": "evt_…",
  "type": "billing.alert.triggered",
  "data": {
    "alert_id": "vlx_alrt_xyz789",
    "customer_id": "vlx_cus_abc123",
    "title": "GPT-4 cost cap for ACME",
    "threshold": {
      "amount_gte": 50000,
      "usage_gte": null
    },
    "observed": {
      "amount_cents": 51234,
      "quantity": "1234567.000000000000"
    },
    "currency": "usd",
    "triggered_at": "2026-04-26T14:23:01Z",
    "period": {
      "from": "2026-04-01T00:00:00Z",
      "to":   "2026-05-01T00:00:00Z",
      "source": "current_billing_cycle"
    },
    "filter": {
      "meter_id": "vlx_mtr_tokens",
      "dimensions": { "model": "gpt-4", "operation": "input" }
    },
    "recurrence": "per_period"
  }
}
```

Snake-case throughout. `observed.amount_cents` and `observed.quantity` are both populated — the receiver doesn't need to know which threshold the alert was set on. `currency` is lower-case (matches Stripe convention and the rest of Velox's emitted events).

`period.source` mirrors the customer-usage convention: `current_billing_cycle` when derived from the customer's primary active subscription. `filter.dimensions` is always an object (`{}` when no dimension filter) per always-object idiom.

## Schema

```sql
CREATE TABLE billing_alerts (
    id                   TEXT PRIMARY KEY DEFAULT 'vlx_alrt_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id            TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    customer_id          TEXT NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    title                TEXT NOT NULL,
    meter_id             TEXT REFERENCES meters(id) ON DELETE SET NULL,
    dimensions           JSONB NOT NULL DEFAULT '{}'::jsonb,
    threshold_amount_cents BIGINT,
    threshold_quantity   NUMERIC(38,12),
    recurrence           TEXT NOT NULL CHECK (recurrence IN ('one_time','per_period')),
    status               TEXT NOT NULL DEFAULT 'active'
                              CHECK (status IN ('active','triggered','triggered_for_period','archived')),
    last_triggered_at    TIMESTAMPTZ,
    last_period_start    TIMESTAMPTZ,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- exactly one threshold field set
    CHECK ( (threshold_amount_cents IS NOT NULL)::int + (threshold_quantity IS NOT NULL)::int = 1 )
);

CREATE INDEX idx_billing_alerts_tenant_customer
    ON billing_alerts (tenant_id, customer_id, status);
CREATE INDEX idx_billing_alerts_evaluator
    ON billing_alerts (status)
    WHERE status IN ('active','triggered_for_period');

CREATE TABLE billing_alert_triggers (
    id                  TEXT PRIMARY KEY DEFAULT 'vlx_atrg_' || encode(gen_random_bytes(12), 'hex'),
    tenant_id           TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    alert_id            TEXT NOT NULL REFERENCES billing_alerts(id) ON DELETE CASCADE,
    period_from         TIMESTAMPTZ NOT NULL,
    period_to           TIMESTAMPTZ NOT NULL,
    observed_amount_cents BIGINT NOT NULL,
    observed_quantity   NUMERIC(38,12) NOT NULL DEFAULT 0,
    currency            TEXT NOT NULL,
    triggered_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (alert_id, period_from)
);
```

Both tables enable RLS via the standard pattern (`current_setting('app.bypass_rls', …) = 'on' OR tenant_id = current_setting('app.tenant_id', …)`).

The `UNIQUE (alert_id, period_from)` constraint on `billing_alert_triggers` is the per-period idempotency contract — two replicas racing the evaluator can both attempt to insert; one wins, the other gets a unique-violation that the evaluator catches and treats as "already fired this period, skip".

The partial index `idx_billing_alerts_evaluator` keeps the evaluator's hot scan tight: archived alerts and one_time alerts that already fired are excluded by predicate, so the evaluator only walks rows that could plausibly fire.

## Trigger evaluation

The evaluator runs on a 5-minute tick (configurable via `VELOX_BILLING_ALERTS_INTERVAL`, default `5m` in local / `1m` in production for fast operator feedback). Each tick:

1. **Acquire advisory lock** (`LockKeyBillingAlertEvaluator`). One leader runs the evaluator across the cluster; followers skip silently.
2. **Per livemode** (live, then test): fan out so the evaluator sees both partitions correctly under RLS.
3. **List candidate alerts**: `SELECT … FROM billing_alerts WHERE status IN ('active','triggered_for_period')`. For `triggered_for_period` rows, the evaluator additionally checks if the customer's current cycle has rolled past `last_period_start` — if so, transition back to `active` (rearm) and re-evaluate this tick.
4. **Per alert**: open a tenant-scoped tx, evaluate the threshold, decide whether to fire.
5. **Evaluate**: resolve the customer's current cycle (primary active subscription), call `usage.AggregateByPricingRules` for the meter (or all meters in the plan if no `meter_id`), filter by `dimensions` (strict-superset on rule `dimension_match`), sum `amount_cents` and `quantity`. Compare against threshold.
6. **Fire (in tx)**: insert the trigger row (UNIQUE (alert_id, period_from) handles idempotency), flip the alert status (`triggered` for one_time / `triggered_for_period` for per_period), set `last_triggered_at` + `last_period_start`, enqueue `billing.alert.triggered` in `webhook_outbox`. Commit.
7. **No-fire**: continue to next alert; nothing persists. The evaluator is read-only on the no-fire path.
8. **Errors per alert**: log + continue. One bad alert (e.g. customer was deleted concurrently, sub has no cycle) doesn't block the rest.

### Recurrence semantics

- **`one_time`**: status `active` → `triggered` on first fire. Stays `triggered` forever (until archived). The evaluator's hot scan excludes `triggered` rows via the partial index, so a one_time alert that fired is invisible to the evaluator on subsequent ticks.
- **`per_period`**: status `active` → `triggered_for_period` on first fire of the cycle. On the next evaluator tick where the customer's `current_billing_period_start > alerts.last_period_start`, the evaluator transitions the alert back to `active` and immediately re-evaluates against the new cycle (so the operator gets a fresh signal as soon as the threshold is crossed in the new cycle). The cycle-rollover detection is part of the evaluator, not a scheduled cron, so it works correctly even when ticks are sparse or skipped.

### Atomicity contract

Within the per-alert tx (step 6 above), three operations happen:
1. INSERT `billing_alert_triggers` row.
2. UPDATE `billing_alerts` (status, last_triggered_at, last_period_start, updated_at).
3. INSERT `webhook_outbox` row via `OutboxStore.Enqueue(ctx, tx, …)`.

Either all three commit together or none commit. A crash mid-tx rolls back everything; the next tick re-evaluates and re-fires (the UNIQUE constraint guarantees we don't double-emit on retry). A crash AFTER commit but before the outbox dispatcher delivers leaves the row pending in `webhook_outbox` — the dispatcher retries until delivery succeeds (or DLQs after 15 attempts).

### Multi-replica safety

Two safeguards prevent double-fire:
- **Advisory lock at the evaluator level** (`LockKeyBillingAlertEvaluator`). One leader wins the tick; followers skip. Same pattern as the billing scheduler.
- **UNIQUE constraint at the trigger-row level**. If two replicas somehow race past the lock (e.g. lock contention edge case during failover), the second INSERT into `billing_alert_triggers` violates UNIQUE (alert_id, period_from), the tx rolls back, and no double-emission occurs.

### Edge cases

- **Customer deleted**: the FK is `ON DELETE CASCADE`, so customer deletion drops the alert and its trigger rows. No orphan emission.
- **Subscription canceled**: the customer has no current cycle. The evaluator logs a warning and skips. Alert stays `active` indefinitely until either the customer re-subscribes (alert wakes up) or the operator archives it.
- **Multi-currency customer**: rare today but real for cross-region tenants. The evaluator computes per-currency totals and compares the threshold against the FIRST currency seen (typically the customer's billing profile currency). v1 doesn't try to convert; "alert me at $50" on a customer with a EUR sub will compare 50 cents to the EUR amount as if they're the same unit. Documented limitation; refine in v2 once a real consumer asks.
- **Alert created mid-cycle, customer already past threshold**: fires on next tick (≤ 5 min after creation). No replay; the as-of-now observed amount is what gets emitted.
- **Pricing rules change mid-cycle**: the alert evaluates against current rule resolution. Historical events get retroactively re-rated under the new rules (this is how `AggregateByPricingRules` works); the alert reflects whatever the dashboard reflects.
- **Test-mode alerts**: live and test alerts are independent (RLS-partitioned via `tenant_id` plus livemode tagged on the outbox row). Test-mode subscriptions can fire alerts that emit to test-mode webhook endpoints.

## Internals

### Composition

```go
// internal/billingalert/service.go (sketch)
type Service struct {
    store Store
}

func (s *Service) Create(ctx context.Context, tenantID string, req CreateRequest) (domain.BillingAlert, error) {
    // validate, RLS-write, emit billing.alert.created? (not v1; create is a quiet op)
}

func (s *Service) Archive(ctx context.Context, tenantID, id string) (domain.BillingAlert, error) {
    // tenant-scoped tx, flip status to archived
}
```

```go
// internal/billingalert/evaluator.go (sketch)
type Evaluator struct {
    store         Store
    customers     CustomerLookup       // narrow interface
    subscriptions SubscriptionLister   // narrow interface
    pricing       PricingReader        // narrow interface
    usage         *usage.Service       // for AggregateByPricingRules
    outbox        OutboxEnqueuer       // narrow interface — Enqueue(ctx, tx, ...)
    locker        Locker
    interval      time.Duration
    clock         clock.Clock
}

func (e *Evaluator) RunOnce(ctx context.Context) {
    // acquire advisory lock; per livemode fan-out; list candidate alerts;
    // per alert, open tx, evaluate, fire-or-skip, commit.
}
```

The narrow-interface pattern mirrors customer-usage and create-preview. No direct cross-domain imports — `billingalert` depends only on:
- `internal/domain` for shared types
- `internal/errs` for error envelope
- `internal/usage.Service` for `AggregateByPricingRules` (peer-package okay; usage is the canonical aggregation engine)
- narrow interfaces (`CustomerLookup`, `SubscriptionLister`, `PricingReader`, `OutboxEnqueuer`) satisfied by other packages without cross-importing them.

### RLS

Every store method opens a tenant-scoped tx via `db.BeginTx(ctx, postgres.TxTenant, tenantID)`. The evaluator's per-alert tx is also tenant-scoped — it reads the alert under the alert's tenant context, so cross-tenant ID confusion (an evaluator pulling alert A's customer for alert B's tenant) is impossible by construction.

Cross-tenant 404s surface naturally: the customer lookup inside `Service.Create` opens a TxTenant scoped to the calling tenant, so a request body specifying a customer ID that belongs to a different tenant returns ErrNotFound. The handler maps that to 404.

### Wire shape

The handler decoupling is identical to create-preview: a wire-shape struct with snake-case JSON tags, a service-shape struct that exposes Go-typed fields, and a `decode*Request` helper that translates one to the other. This keeps the JSON contract pinned by struct tags and the service signature ergonomic.

The wire-shape regression test (`TestWireShape_SnakeCase`) marshals a fully-populated `domain.BillingAlert` plus a fully-populated webhook payload and asserts:
- All snake_case keys present.
- No PascalCase leaks.
- `dimensions` marshals as JSON object even when empty.
- `threshold.amount_gte` and `threshold.usage_gte` are both present (one null).
- `data` arrays in list responses are `[]` not null when empty.

## Tests

### Unit tests

- `Service.Create` — table-driven: missing title / customer_id / recurrence → 422 with field; both thresholds set → 422; usage_gte without meter_id → 422; happy path returns alert with `status='active'`.
- `Service.Archive` — already-archived returns same shape (idempotent); not-found returns 404; cross-tenant returns 404.
- `Service.List` — filter by customer_id; filter by status; pagination clamps.
- `Evaluator.evaluateAmount` — table-driven against fake usage data: below threshold no fire; at threshold fire; above threshold fire; multi-currency mismatch logs warning.
- `Evaluator.dimensionsMatch` — strict-superset semantics: rule's match must contain all alert filter keys with equal values; missing key fails; extra key on rule passes.
- `Evaluator.shouldRearm` — per_period alerts whose `last_period_start` is older than the customer's current `current_billing_period_start` flip back to `active`.
- **`TestWireShape_SnakeCase` (the merge gate)**: marshal a fully-populated `domain.BillingAlert` and webhook payload; assert all snake_case keys present; no PascalCase leaks; `dimensions` is `{}` not null when empty.

### Integration tests (real Postgres)

- `TestCreateAlert_Persist` — create via service; read via store; assert all fields round-trip including `dimensions` JSONB.
- `TestCreateAlert_RLS` — tenant B can't read tenant A's alert (404).
- `TestEvaluator_FiresOnceForOneTime` — seed customer + sub + plan + meter + events past threshold; run evaluator; assert one trigger row, status=triggered, one outbox row with `billing.alert.triggered`. Run evaluator again; assert no new rows.
- `TestEvaluator_FiresPerPeriodAndRearms` — seed; fire once; advance test clock past cycle boundary; assert evaluator transitions alert back to active and re-fires; assert two trigger rows with different `period_from`; two outbox rows.
- `TestEvaluator_DoubleFireIdempotent` — manually insert a trigger row for the current period; run evaluator; assert no new outbox row (UNIQUE constraint catches the duplicate).
- `TestEvaluator_ArchivedSkipped` — archive a triggered alert; advance clock; assert evaluator never re-fires it.
- `TestEvaluator_DimensionFilter` — multi-dim meter; alert filters to `{model:"gpt-4"}`; events tagged gpt-3.5 don't count toward threshold; events tagged gpt-4 do.
- `TestEvaluator_AtomicityOnRollback` — inject a webhook outbox failure (e.g. extreme payload size); assert both the trigger row insert AND the alert status update roll back together (no half-commit).
- `TestEvaluator_NoSubscription` — customer has no active sub; assert evaluator logs warning and skips; alert stays active.
- `TestEvaluator_MultiTenantIsolation` — two tenants, one alert each; assert each alert evaluates against its own tenant's events.

### Wire-shape merge gate

`TestWireShape_SnakeCase` lives in `internal/billingalert/wire_shape_test.go`. Same pattern as the create_preview merge gate — must pass before merge, blocks PR otherwise.

## Migrations

`0057_billing_alerts.up.sql` / `0057_billing_alerts.down.sql`. Number is sibling-aware: 0056 is reserved for the parallel billing-thresholds slice; 0057 is the next free slot for this work. **Do not** renumber locally — pick from `origin/main`'s latest, never the local branch.

## Performance

- Evaluator hot scan: indexed by partial index `idx_billing_alerts_evaluator` on `status IN ('active','triggered_for_period')`. Tenant with 1000 alerts (heavy) → 1000-row scan, tens of ms.
- Per-alert evaluation: 1 customer lookup + 1 sub lookup + 1 `AggregateByPricingRules` call. Same cost envelope as customer-usage / create_preview. For typical sub (10K events / cycle): ~50ms. Tenant with 100 active alerts: ~5s end-to-end. 5-min tick is comfortable.
- The advisory lock prevents two replicas duplicating the work. Lock contention is graceful: followers log Debug and return.
- The outbox enqueue is one extra INSERT per fire; the dispatcher amortizes delivery cost across many events on its own tick. No extra round-trips on the evaluator's hot path.

## Open questions

1. **Should we ship a `GET /v1/billing/alerts/{id}/triggers` endpoint to list past trigger events?** Useful for an audit pane on the alert detail page. **Proposal: defer.** v1 emits via webhook + dashboard notification; the trigger history can be reconstructed from the customer's webhook delivery log if needed. Add when a real consumer asks (post-launch).
2. **Should `filter.dimensions` be allowed without `filter.meter_id`?** Cross-meter dimension filtering ("alert when ANY meter spend with `region=us-east` exceeds $X") has a real use case but the evaluator topology is more complex (per-meter rule resolution, then per-meter dimension filter, then aggregate). **Proposal: reject in v1** with a clear error; ship if a real consumer asks.
3. **Should we surface a "muted" status separate from "archived"?** Mute = pause for one cycle, then auto-rearm. Archive = soft-disable forever. Stripe has both. **Proposal: defer.** Operators can archive + recreate to mute; the audit trail (the trigger rows) is preserved across recreates. Add muted as a v2 nicety.
4. **Should `recurrence: per_period` reset on the customer's cycle or on a calendar boundary (e.g. monthly UTC)?** **Proposal: customer's cycle.** A subscription anchored to the 15th of each month should fire alerts that follow that cycle, not the calendar month. This matches Stripe's semantics and the rest of the billing engine.
5. **Should the evaluator interval be per-tenant configurable?** A high-spend tenant might want 1-min ticks; a low-volume one is fine with 30-min. **Proposal: defer.** Single global interval (5m local / 1m production) covers v1; per-tenant tuning is a v2 lever once we have data on usage patterns.
6. **Should we persist the trigger row's outbox `event_id` for cross-reference?** Useful for debugging "did this alert's webhook get delivered". **Proposal: defer.** The webhook outbox is queryable on its own; cross-referencing via `event_type='billing.alert.triggered'` and matching `data.alert_id` is sufficient. Add a column if a real ops use case emerges.
7. **Should we expose a way to test-fire an alert without waiting for real usage?** Useful for the operator to verify their webhook receiver is wired correctly. **Proposal: defer to v2** behind a separate `POST /v1/billing/alerts/{id}/test_fire` route gated to test-mode keys only. v1 operators can verify via Stripe's standard webhook-event replay UI on the dashboard.

## Implementation checklist (Week 5d)

- [ ] `docs/design-billing-alerts.md` — this RFC.
- [ ] `internal/domain/billing_alert.go` — domain types (`BillingAlert`, `BillingAlertStatus`, `BillingAlertRecurrence`, `BillingAlertTrigger`, threshold helpers).
- [ ] `internal/platform/migrate/sql/0057_billing_alerts.up.sql` + `.down.sql` — schema.
- [ ] `internal/billingalert/store.go` — `Store` interface + `ListFilter`.
- [ ] `internal/billingalert/postgres.go` — Postgres implementation.
- [ ] `internal/billingalert/service.go` — Create / Get / List / Archive.
- [ ] `internal/billingalert/handler.go` — chi router for the four endpoints.
- [ ] `internal/billingalert/evaluator.go` — background evaluator with advisory lock.
- [ ] `internal/billingalert/wire_shape_test.go` — `TestWireShape_SnakeCase` merge gate.
- [ ] `internal/billingalert/service_test.go` — unit tests with fakes.
- [ ] `internal/billingalert/evaluator_test.go` — unit tests for evaluator helpers.
- [ ] `internal/billingalert/integration_test.go` — full-stack integration with real Postgres + outbox.
- [ ] Add `EventBillingAlertTriggered = "billing.alert.triggered"` to `internal/domain/webhook_outbound.go`.
- [ ] Add `LockKeyBillingAlertEvaluator int64 = 76540005` to `internal/platform/postgres/advisory_lock.go`.
- [ ] Wire routes + evaluator goroutine in `internal/api/router.go` and `cmd/velox/main.go`.
- [ ] Update `web-v2/src/lib/api.ts` — `BillingAlert` types + endpoint methods.
- [ ] Update `CHANGELOG.md` (Track A) + `web-v2/src/pages/Changelog.tsx` (Track B).
- [ ] Append entry to `docs/parallel-handoff.md`.

## Track B unblock

Track B can scaffold a "Set alert" CTA on the cost dashboard against the contract in this RFC, mocking the API client until the backend lands. The CTA opens a modal with title / threshold / recurrence inputs; on submit calls `POST /v1/billing/alerts`. The notifications panel polls `GET /v1/billing/alerts?customer_id=…&status=triggered_for_period,triggered` to surface any unread alerts inline.

Same parallel-work pattern as recipes / customer-usage / create-preview: Track B mocks against this RFC, swaps to the real API at integration time.

## Review status

- **Track A author:** drafted 2026-04-26 alongside the implementation.
- **Track B review:** pending — Track B can scaffold the alert-creation CTA against this design without waiting for further iteration.
- **Human review:** pending — flag any open question to revisit.
