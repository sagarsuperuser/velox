# Velox — Operational Runbook

Companion to [sla-slo.md](./sla-slo.md). SLOs define the targets; this runbook
describes the metrics that measure them, the alerts that fire when they slip,
and the triage steps to get them back on track.

## Table of contents

- [Metrics inventory](#metrics-inventory)
- [Alert catalog](#alert-catalog)
- [Dashboards](#dashboards)
- [Incident playbooks](#incident-playbooks)

---

## Metrics inventory

All metrics are exported at `GET /metrics` in Prometheus exposition format. The
endpoint is unauthenticated — scrape it from a trusted network only, or front
it with an ingress that restricts access to the monitoring backend.

### HTTP surface

| Metric | Type | Labels | Meaning |
|--------|------|--------|---------|
| `velox_http_requests_total` | counter | `method`, `path`, `status` | Every request served by the API. `path` is normalized (IDs collapsed to `:id`) to keep cardinality bounded. |
| `velox_http_request_duration_seconds` | histogram | `method`, `path`, `status` | Request latency. Buckets go from 1ms to 5s. |
| `velox_http_requests_in_flight` | gauge | — | Concurrent requests currently being processed. Saturation signal. |

### Billing engine

| Metric | Type | Labels | Meaning |
|--------|------|--------|---------|
| `velox_billing_cycles_total` | counter | — | Billing runs completed (successful or not). |
| `velox_billing_cycle_errors_total` | counter | — | Billing runs that returned an error. |
| `velox_billing_cycle_duration_seconds` | histogram | — | Wall-clock duration of a billing run. Buckets up to 5min. |
| `velox_invoices_generated_total` | counter | — | Invoices written by the billing engine. Reflects throughput, not correctness. |
| `velox_usage_events_ingested_total` | counter | — | Usage events successfully persisted. Aggregated; not labeled by meter. |

### Payments

| Metric | Type | Labels | Meaning |
|--------|------|--------|---------|
| `velox_payment_charges_total` | counter | `result` | `result` ∈ {`succeeded`, `failed`}. Per-attempt, not per-invoice. |
| `velox_auto_charge_retries_total` | counter | `result` | Retries triggered by the dunning loop. |
| `velox_stripe_breaker_state` | gauge | `tenant_id` | Per-tenant circuit breaker: `0` closed, `1` half-open, `2` open. |

### Webhooks

| Metric | Type | Labels | Meaning |
|--------|------|--------|---------|
| `velox_webhook_deliveries_total` | counter | `status` | `status` ∈ {`succeeded`, `failed`, `pending`}. Outbound deliveries only. |

### Dunning & credit

| Metric | Type | Labels | Meaning |
|--------|------|--------|---------|
| `velox_dunning_runs_processed_total` | counter | — | Dunning attempts executed (per policy step). |
| `velox_credit_operations_total` | counter | `type` | `type` ∈ {`grant`, `usage`, `expiry`, `adjustment`}. |

### Audit & compliance

| Metric | Type | Labels | Meaning |
|--------|------|--------|---------|
| `velox_audit_write_errors_total` | counter | `tenant_id` | Audit log INSERT failures. Per-tenant because SOC-2 posture is per-tenant. |

### Background cleanup

| Metric | Type | Labels | Meaning |
|--------|------|--------|---------|
| `velox_scheduled_cleanup_rows_total` | counter | `table` | Rows deleted by retention jobs. Label is the target table. |

---

## Alert catalog

Each alert ties back to an SLO in [sla-slo.md](./sla-slo.md). Severities:

- **page** — wake someone up. User impact is happening now.
- **ticket** — open a ticket in the oncall queue. Degradation, not outage.
- **info** — Slack notification. Something to watch, no action required yet.

### API availability & latency

```promql
# page — error budget burning fast (SLO: 99.95% monthly)
ALERT VeloxAPIErrorBudgetBurn
  EXPR sum(rate(velox_http_requests_total{status=~"5.."}[5m]))
     / sum(rate(velox_http_requests_total[5m])) > 0.005
  FOR 5m
  LABELS { severity = "page" }

# ticket — p99 latency exceeds SLO
ALERT VeloxAPIHighLatency
  EXPR histogram_quantile(0.99, rate(velox_http_request_duration_seconds_bucket[5m])) > 1
  FOR 10m
  LABELS { severity = "ticket" }

# info — saturation warning
ALERT VeloxAPIHighConcurrency
  EXPR velox_http_requests_in_flight > 200
  FOR 5m
  LABELS { severity = "info" }
```

### Billing engine

```promql
# page — billing cycles are failing
ALERT VeloxBillingCycleFailing
  EXPR rate(velox_billing_cycle_errors_total[15m]) > 0
  FOR 15m
  LABELS { severity = "page" }

# ticket — billing runs are running long
ALERT VeloxBillingCycleSlow
  EXPR histogram_quantile(0.95, rate(velox_billing_cycle_duration_seconds_bucket[1h])) > 60
  FOR 30m
  LABELS { severity = "ticket" }

# info — invoice throughput dropped to zero during business hours
ALERT VeloxNoInvoicesGenerated
  EXPR rate(velox_invoices_generated_total[1h]) == 0
  FOR 2h
  LABELS { severity = "info" }
```

### Payments

```promql
# page — Stripe breaker is open for any tenant (payments blocked)
ALERT VeloxStripeBreakerOpen
  EXPR velox_stripe_breaker_state == 2
  FOR 5m
  LABELS { severity = "page" }

# ticket — payment success rate < 90% (SLO: 95%)
ALERT VeloxPaymentSuccessRateLow
  EXPR sum(rate(velox_payment_charges_total{result="succeeded"}[30m]))
     / sum(rate(velox_payment_charges_total[30m])) < 0.9
  FOR 30m
  LABELS { severity = "ticket" }
```

### Webhooks

```promql
# ticket — outbound webhook delivery failure rate > 10%
ALERT VeloxWebhookFailureRate
  EXPR sum(rate(velox_webhook_deliveries_total{status="failed"}[15m]))
     / sum(rate(velox_webhook_deliveries_total[15m])) > 0.1
  FOR 15m
  LABELS { severity = "ticket" }
```

### Audit (fail-closed tenants)

```promql
# page — audit writes failing for any tenant on SOC-2 fail-closed policy
ALERT VeloxAuditWriteErrors
  EXPR increase(velox_audit_write_errors_total[5m]) > 0
  FOR 1m
  LABELS { severity = "page" }
```

Audit write failures page immediately because fail-closed tenants see 503s and
fail-open tenants have a compliance gap accumulating. See
[api-key-rotation.md](./api-key-rotation.md) for related key-lifecycle alerts.

---

## Dashboards

Suggested Grafana layout. One screen per audience.

### API health (for oncall)

1. Requests/sec by status family (2xx / 4xx / 5xx) — single stacked area.
2. p50/p95/p99 latency (single graph, three lines).
3. In-flight requests gauge.
4. Top 10 endpoints by error rate, last 15min.

### Billing engine (for billing team)

1. Invoices generated per hour.
2. Billing cycle duration p95.
3. Billing cycle error rate.
4. Usage events/sec.

### Payments (for revenue team)

1. Payment success rate (24h rolling).
2. Per-tenant breaker state as a status table.
3. Auto-charge retry volume by result.
4. Credit operations by type (grant vs usage vs expiry).

---

## Incident playbooks

### Playbook: `VeloxAPIErrorBudgetBurn`

**What's happening:** 5xx rate exceeds 0.5% of traffic over 5 minutes. Monthly
SLO of 99.95% consumes 22 minutes of full error budget; this alert catches a
burn rate that exhausts the month's budget in hours.

**Triage:**

1. Check the top-offending endpoints:
   ```promql
   topk(10, sum by (path, status) (rate(velox_http_requests_total{status=~"5.."}[5m])))
   ```
2. If a single endpoint dominates, check application logs for stack traces on
   that handler.
3. If spread evenly across endpoints, suspect infrastructure: database,
   external services, or a deployment that just went out.
4. Rollback criterion: if a deploy landed in the last 30 minutes and error rate
   jumped in correlation, rollback first — investigate after.

**Common causes:**

- PostgreSQL connection pool exhausted — check `pg_stat_activity` for idle-in-
  transaction connections.
- Stripe outage — check `velox_stripe_breaker_state` and status.stripe.com.
- A migration that dropped or renamed a column without a code deploy first.

### Playbook: `VeloxBillingCycleFailing`

**What's happening:** Billing runs are returning errors. Invoices may not be
generating. Each failed run is visible in `velox_billing_cycle_errors_total`.

**Triage:**

1. Check logs for the most recent billing cycle:
   ```
   grep "billing cycle" logs | tail -50
   ```
2. Determine scope: single tenant vs. all tenants. Per-tenant failures are
   usually data problems (bad subscription state, missing rating rule). All-
   tenant failures are usually infrastructure.
3. If a single tenant is the source, pause that tenant's billing and continue
   others. Tenant isolation means one bad subscription should not poison the
   queue — but verify.

**Common causes:**

- A rating rule version referenced by a live subscription was deleted.
- Currency mismatch between plan and customer.
- Tax calculator failure — should fall back to manual (see
  [tax-calculation.md](./tax-calculation.md)). If the fallback also fails, the
  manual calculator config is probably broken.

### Playbook: `VeloxStripeBreakerOpen`

**What's happening:** The circuit breaker for a tenant's Stripe account has
opened. Payments for that tenant are being rejected fast without hitting the
API.

**Triage:**

1. Identify the tenant from the alert labels.
2. Check recent Stripe API error codes in logs for that tenant.
3. Common distinction:
   - Account-specific: invalid API key, account suspended, keys rotated on
     Stripe side — the breaker is working correctly, resolve with the customer.
   - Platform-wide: if multiple tenants' breakers opened within minutes of each
     other, it's a Stripe outage — check status.stripe.com.

**Resolution:**

The breaker closes automatically after its cooldown window when probes succeed.
No manual reset required. If the underlying cause is resolved but the breaker
stays open, restart the API pod — the breaker state is in-memory.

### Playbook: `VeloxAuditWriteErrors`

**What's happening:** `audit_log` INSERTs are failing for at least one tenant.
This is a compliance-grade alert: SOC-2 fail-closed tenants are returning 503s
to callers, and fail-open tenants are accumulating an accepted gap.

**Triage:**

1. Identify the tenant from the alert label.
2. Check whether the tenant has `audit_fail_closed: true` — that determines
   whether customer-visible impact is happening now.
3. Check `audit_log` table health: row count, recent INSERTs, any locks.

**Common causes:**

- Partition for the current month not created — check
  `migrations/postgres/*audit_log*`.
- Disk full on the primary — `pg_database_size('velox')` and underlying volume.
- A unique-constraint collision from a replay — the audit primary key is
  `vlx_aud_*` random, so collisions are essentially impossible in practice.
  If this is the cause, something is wrong with ID generation.

**Resolution:**

Audit writes use a detached timeout context, so client disconnects don't
interrupt them. If the DB is healthy and writes still fail, roll the API back
to the previous version — recent middleware or schema change is the first
suspect.

### Playbook: `VeloxWebhookFailureRate`

**What's happening:** Outbound webhook deliveries are failing at >10%. Customers
who rely on webhooks for downstream workflows are seeing breakage.

**Triage:**

1. Check the `webhook_outbox` table for entries with high retry counts.
2. Break down by destination host — a single customer endpoint that's down
   skews the global metric but has no shared cause.
3. If spread across customers, look for a Velox-side bug: malformed payload,
   signature mismatch, or the outbox worker not draining.

**Resolution:**

The outbox worker retries with exponential backoff — transient failures
self-heal. Persistent per-customer failures should result in an email to the
tenant admin (not paging Velox oncall).
