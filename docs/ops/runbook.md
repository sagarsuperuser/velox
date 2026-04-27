# Velox — Operational Runbook

Companion to [sla-slo.md](./sla-slo.md). SLOs define the targets; this runbook
describes the metrics that measure them, the alerts that fire when they slip,
and the triage steps to get them back on track.

## Table of contents

- [Severity definitions](#severity-definitions)
- [Metrics inventory](#metrics-inventory)
- [Alert catalog](#alert-catalog)
- [Dashboards](#dashboards)
- [Incident playbooks](#incident-playbooks)
- [Communication](#communication)
- [Rollback procedures](#rollback-procedures)
- [Compliance](#compliance)
- [Post-mortem template](#post-mortem-template)

---

## Severity definitions

| Severity | Customer impact | Response time | Examples |
|----------|-----------------|---------------|----------|
| **SEV-1 (page)** | Revenue-impacting or data-at-risk. Multiple tenants affected, or any tenant with active financial transactions failing. | Acknowledge within 5 min, status page update within 15 min. | API 5xx burn, Stripe breaker open, audit writes failing for fail-closed tenant, data loss or corruption. |
| **SEV-2 (ticket)** | Degradation. Users experience errors or slowness but core flows still complete. Single-tenant scope, or a non-revenue surface. | Triage within 1 hour during business hours, next-morning otherwise. | p99 latency breach, billing cycle slow, payment success rate dipped, webhook delivery failure rate elevated. |
| **SEV-3 (info)** | No user impact. A signal worth watching before it becomes SEV-2. | Triage during the next business day. | In-flight request saturation, backup size anomaly, invoice throughput dropped during business hours. |

Ambiguity rule: when unsure, over-classify for the first 30 minutes, then downgrade. An over-classified SEV-2 costs an extra Slack ping; an under-classified SEV-1 costs trust.

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
| `velox_stripe_breaker_state` | gauge | — | Global circuit breaker for Stripe calls: `0` closed, `1` half-open, `2` open. |

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
# page — Stripe breaker is open (payments blocked for every tenant)
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

# ticket — PaymentIntent failure rate > 5% on the 15-min window
# Distinct from VeloxPaymentSuccessRateLow (30m/<90%); this is the shorter,
# sharper signal that something changed in the last 15 min.
ALERT VeloxPaymentIntentFailureRate
  EXPR sum(rate(velox_payment_charges_total{result="failed"}[15m]))
     / sum(rate(velox_payment_charges_total[15m])) > 0.05
  FOR 15m
  LABELS { severity = "ticket" }
```

### Scheduler

```promql
# page — scheduler hasn't completed a run in 2x its interval
# Scheduler heartbeat is surfaced via /health/ready response, not as a
# Prometheus gauge (yet). Alert via blackbox_exporter probing /health/ready.
# Blackbox config: probe_success{job="velox",instance="https://api.velox.dev/health/ready"}
ALERT VeloxSchedulerStale
  EXPR probe_success{job="velox",instance=~".*/health/ready"} == 0
  FOR 2m
  LABELS { severity = "page" }
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
2. Stripe breaker state (global gauge).
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

**What's happening:** The global Stripe circuit breaker has opened. All
payment attempts are being rejected fast without hitting the API — Stripe
is almost certainly having an incident.

**Triage:**

1. Check status.stripe.com for an active incident.
2. Check recent Stripe API error codes in logs (5xx/timeout pattern).
3. If Stripe reports healthy but our breaker is open, the root cause is
   likely in our network/egress — check outbound latency and DNS.

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

### Playbook: `VeloxPaymentIntentFailureRate`

**What's happening:** PaymentIntent creation failure rate exceeds 5% over 15
minutes. Distinct from `VeloxStripeBreakerOpen` — the breaker trips on API
unreachability; this alert fires when Stripe *responds* but rejects at a rate
worth investigating (expired cards, 3DS friction, declined, risk-blocked).

**Triage:**

1. Break down by Stripe error code — high `card_declined` is usually customer-
   side (dunning should pick up); high `authentication_required` suggests 3DS
   friction the customer hasn't completed; `api_key_expired` / `connection_error`
   point to Velox-side credential or network issues.
   ```promql
   sum by (result) (rate(velox_payment_charges_total[15m]))
   ```
   For per-code drill-down, check logs: `grep "stripe charge failed" logs | tail -50`.
2. If a single tenant dominates, check that tenant's Stripe connection
   credentials are still valid (`stripe_provider_credentials` rows for that
   tenant+livemode) and their Stripe account isn't restricted.
3. If spread across tenants, suspect a Velox-side regression: PaymentIntent
   params we're sending are wrong, idempotency-key collision, or a recent
   dunning/billing change introduced a bad retry pattern.

**Common causes:**

- A new card-brand restriction landed for a tenant (Stripe notified them,
  not us) — their success rate craters until they update their Stripe config.
- Our auto-charge retry loop is hammering a known-bad card — check
  `velox_auto_charge_retries_total{result="failed"}` rate.
- The idempotency key we send collides with a prior PI attempt; Stripe returns
  the old failed status. Fix is in code, not in ops.

**Resolution:**

Tenant-scoped issues resolve when the customer updates their payment method —
dunning emails should already be firing. Velox-side issues require a rollback
of the offending deploy.

### Playbook: `VeloxSchedulerStale`

**What's happening:** The billing/dunning scheduler has not reported a run
within 2× its configured interval. Invoices may not be generating; dunning
retries are not running.

**Triage:**

1. Hit `GET /health/ready` on each API pod — the response body includes
   `scheduler.last_run_ago_seconds`. If all pods report stale, the scheduler
   is not running anywhere.
2. The scheduler is leader-gated via advisory lock (`LockKeyBillingScheduler`,
   `LockKeyDunningScheduler`). Check that *some* pod holds the lock:
   ```sql
   SELECT locktype, classid, objid, granted, pid
   FROM pg_locks WHERE locktype = 'advisory';
   ```
   Zero rows means no leader. A row with `granted=false` means contention — a
   dead pod may have left the lock pinned; restart live pods so one can
   re-acquire cleanly.
3. Check logs for panics in `internal/billing/scheduler.go` — a panic in one
   tick can prevent the next from starting if recovery is missing.

**Common causes:**

- The previous leader pod was OOM-killed mid-run. Advisory locks release on
  connection close, but if the connection is pooled the lock may linger until
  a health check reaps it. Typically self-heals within ~60s.
- A long-running billing cycle exceeded 2× the interval. Not a bug — check
  `velox_billing_cycle_duration_seconds` to confirm it's *running*, not stuck.
- Database is under load and the scheduler can't acquire the lock in a
  reasonable time. Check DB CPU and active connections.

**Alerting note (current gap):** The scheduler heartbeat is surfaced via
`/health/ready` response body, *not* as a Prometheus gauge. Alert via a
blackbox-exporter probe against `/health/ready` with status-code check, or
instrument `velox_scheduler_last_run_timestamp_seconds` (tracked TODO).

### Playbook: `VeloxDBPoolSaturated`

**What's happening:** The Postgres connection pool is at or near capacity.
Handlers block waiting for a connection, latency climbs, and in extreme cases
the liveness probe fails because it can't acquire a connection either.

**Triage:**

1. Check `pg_stat_activity` for stuck sessions:
   ```sql
   SELECT pid, state, wait_event_type, wait_event, query_start,
          now() - query_start AS age, substring(query, 1, 80) AS q
   FROM pg_stat_activity
   WHERE datname = 'velox' AND state <> 'idle'
   ORDER BY query_start
   LIMIT 30;
   ```
2. `idle in transaction` rows older than ~1 minute indicate a handler that
   opened a transaction and failed to commit/rollback. Kill them with
   `SELECT pg_cancel_backend(pid)` (soft) or `pg_terminate_backend(pid)` (hard).
3. If every connection is running a legitimate query, this is load, not a leak.
   Scale the API pod count first (so each pod's share of the pool drops) before
   enlarging `max_connections` — unbounded connection counts are how Postgres
   servers fall over.

**Common causes:**

- A handler path that forgot to `defer tx.Rollback()` (the idiom is
  `defer tx.Rollback()` before `tx.Commit()`; rollback is a no-op on a
  committed tx but safety-net on panic).
- A blocking wait on an external service inside a transaction. Policy: no
  network I/O between `BeginTx` and `Commit`. Audit against this rule.
- Runaway usage ingestion under the same tenant holding ROW EXCLUSIVE locks
  on `usage_events`. Consider backpressure at the ingest edge.

**Alerting note (current gap):** `sql.DBStats` is not yet exported as
Prometheus metrics. Until then, alert via `pg_stat_activity` count via the
`postgres_exporter` (`pg_stat_database_numbackends{datname="velox"}` against
your `max_connections` value). Instrumenting `velox_db_pool_*` gauges from
`*sql.DB.Stats()` is a tracked TODO.

### Playbook: `VeloxOutboxBacklog`

**What's happening:** The webhook outbox table has a growing backlog of
unpublished rows. Customers will eventually receive events, but delivery is
lagging far beyond the expected cadence.

**Triage:**

1. Count the backlog by state:
   ```sql
   SELECT status, count(*) FROM webhook_outbox
   WHERE tenant_id IS NOT NULL
   GROUP BY status;
   ```
   Healthy: `pending` in low tens, `failed` only for known-bad destinations.
   Unhealthy: `pending` in thousands, or steady growth over an hour.
2. Check whether the outbox worker is running. It's launched in `cmd/velox`
   main.go when `VELOX_WEBHOOK_OUTBOX_ENABLED` is truthy (default: on). Absent
   the worker, the backlog grows forever.
3. Check whether worker is stuck on a specific row — look at the oldest
   `pending` row's `locked_until` and `attempts` columns. A perpetually locked
   row indicates the worker acquired it, crashed, and the lock has not yet
   expired.

**Common causes:**

- `VELOX_WEBHOOK_OUTBOX_ENABLED` toggled off by accident during a deploy.
- A deploy introduced a bug in the dispatcher that panics and silently drops
  the worker goroutine. Pod logs for `outbox dispatcher` lines will show the
  panic.
- A slow customer endpoint that holds workers hostage because retries are
  in-flight. Lowering the per-destination concurrency cap is the fix.

**Alerting note (current gap):** Outbox backlog size is not yet exported as a
Prometheus gauge. Until then, alert via a `postgres_exporter` custom query
(`SELECT count(*) FROM webhook_outbox WHERE status = 'pending'`) or a cron
that scrapes it. Instrumenting `velox_webhook_outbox_backlog` as a periodic
gauge is a tracked TODO.

---

## Communication

Every SEV-1 and SEV-2 incident triggers three communications in parallel:
status page, partner Slack/email, and internal Slack (`#incidents`). Drafts
below; adapt to the specific scenario.

### Status page update (SEV-1, initial)

```
[INVESTIGATING · {{ component }}]

We are investigating reports of {{ brief user-visible symptom }}.
Customers using {{ feature }} may experience {{ impact }}.

We'll post an update by {{ now + 30 min, UTC }}.
```

### Status page update (SEV-1, ongoing)

```
[IDENTIFIED · {{ component }}]

The cause appears to be {{ root cause in one sentence }}. We are
{{ mitigation in progress }}. Current impact: {{ impact, e.g.,
"~3% of PaymentIntent requests returning 500" }}.

Next update in 30 minutes.
```

### Status page update (resolution)

```
[RESOLVED · {{ component }}]

{{ Component }} has been fully restored as of {{ time, UTC }}. The
root cause was {{ one-sentence summary }}. No customer data was lost.

A post-mortem will be published within 5 business days.
```

### Partner notification (Slack Connect / email)

```
Hi {{ partner contact }},

We had a {{ severity }} incident from {{ start, UTC }} to {{ end, UTC }}
affecting {{ specific surface — e.g., "invoice finalization in test mode" }}.

Impact on {{ partner company }}: {{ specific, measured — e.g., "3 invoices
failed to finalize and were retried successfully once service was restored.
No data was lost." }}

Root cause: {{ one sentence, honest }}.

What we're doing to prevent recurrence: {{ concrete, not "improved monitoring" }}.

Full post-mortem by {{ date }}. Questions → reply here or security@velox.dev.

— Velox oncall
```

Rule: never "we're sorry for any inconvenience this may have caused."
Partners want to know *what* happened, *what it cost them*, and *what
prevents it next time*. Generic apology language reads as evasion.

### Internal Slack (`#incidents`)

```
🚨 SEV-{{ 1 | 2 | 3 }} · {{ alert name }} · {{ start time UTC }}

Impact: {{ facts: which tenants, which endpoints, error rate }}
Suspected cause: {{ one line }}
Owner: {{ your name }}
Doc: {{ thread link }}

Next: {{ specific action with ETA }}
```

Keep internal updates every 15 minutes while the incident is open, even if
"no progress yet" — the absence of an update looks like abandonment.

---

## Rollback procedures

### Application rollback (Kubernetes / Helm)

1. Identify the previous healthy revision:
   ```bash
   kubectl rollout history deployment/velox -n velox
   helm history velox -n velox
   ```
2. Roll back:
   ```bash
   kubectl rollout undo deployment/velox -n velox
   # or
   helm rollback velox <previous-revision> -n velox
   ```
3. Watch pods return to Ready:
   ```bash
   kubectl rollout status deployment/velox -n velox --timeout=5m
   ```
4. Confirm 5xx rate returns to baseline and the alert clears.

**Rollback criterion:** if a deploy landed in the last 30 minutes and the
alert jumped in correlation, roll back first — investigate after. The cost
of an unnecessary rollback is one extra deploy; the cost of debugging a live
incident is customer trust.

### Migration rollback

Migrations are applied by `goose` from `migrations/postgres/`. Rolling *back*
a schema migration is rarely safe in production — forward-only is the
default. If the forward migration was destructive (dropped a column, dropped
a table), a code rollback without a schema rollback will fail because the old
binary expects the old schema.

Approach:

1. **If the forward migration was additive** (added columns with defaults,
   added tables, added indexes): no schema rollback needed. Roll back the
   app; the new columns sit unused until the next forward deploy.
2. **If the forward migration was destructive**: do not run `goose down`.
   Instead, roll forward with a *compensating* migration that recreates the
   missing structure from backup or from the old column (if kept). Then roll
   the app to a build that handles the new state. Plan this *before* the
   destructive change lands.
3. **In all cases**: snapshot the DB before any destructive migration. A
   WAL-G base backup plus WAL replay gives a PITR option even if the
   forward migration was unrecoverable. See
   [backup-recovery.md](./backup-recovery.md).

If you must restore to a pre-migration state, follow the PITR procedure in
`backup-recovery.md` §5.2, targeting a timestamp just *before* the migration
ran (check `schema_migrations.applied_at` if the table survived).

### Feature-flag rollback

Faster than a deploy rollback when the failing behavior is behind a flag.
Flags live in `tenant_settings.feature_flags` (per-tenant) and, for global
flags, in environment variables. Flip the flag via the settings API or
restart pods with the env var unset. Confirm traffic patterns recover before
concluding.

---

## Compliance

Compliance posture has its own set of operator docs. They cover what
evidence Velox produces, how long to retain it, and the regime-specific
reasoning behind each retention window.

- [audit-log-retention.md](./audit-log-retention.md) — what the audit
  log captures, recommended retention by compliance regime (SOC 2 / GDPR
  / PCI-DSS / HIPAA / SOX), the prune-and-archive pattern (batched
  DELETE that doesn't lock the hot table, S3 lifecycle to Glacier with
  optional Object Lock), and how to restore a window from archive into
  a side query table. Pairs with the `VeloxAuditWriteErrors` alert and
  the `velox_audit_write_errors_total{tenant_id}` metric above.
- [encryption-at-rest.md](./encryption-at-rest.md) — what Velox encrypts
  at the application layer (customer PII, webhook signing secrets,
  per-tenant Stripe credentials via AES-256-GCM under
  `VELOX_ENCRYPTION_KEY`), what it hashes (API keys / passwords /
  sessions / portal tokens / payment-update tokens), the email blind
  index for magic-link lookup under `VELOX_EMAIL_BIDX_KEY`, copy-pasteable
  SQL recipes that prove encryption is in effect on a running install,
  the honest disclosure that key rotation is **not implemented today**
  with the operational symptoms when either key is rotated, and the
  SOC 2 / PCI-DSS / GDPR / HIPAA control mapping. Read alongside
  [secrets-management.md](./secrets-management.md) for the env-var
  delivery story.
- [`docs/compliance/soc2-mapping.md`](../compliance/soc2-mapping.md) —
  SOC 2 Trust Services Criteria control mapping. Maps CC1-CC9 plus
  the optional Availability / Confidentiality / Processing Integrity /
  Privacy categories onto the Velox surface, with file-and-line
  evidence pointers, an honest gap list ranked by audit impact (key
  rotation tooling, SECURITY.md, MFA, govulncheck-blocking, SAST,
  CODE_OF_CONDUCT, CODEOWNERS, status page, image signing — in
  priority order before a Type 1), and a flat evidence index an
  auditor can walk straight through. Pre-launch / pre-audit posture:
  this is audit-prep input, not an attestation.
- GDPR data export + deletion guide — landing in the rest of
  Week 10 of the [90-day plan](../90-day-plan.md).

---

## Post-mortem template

Published within 5 business days of any SEV-1 or multi-tenant SEV-2. Save
as `docs/postmortems/YYYY-MM-DD-short-slug.md`.

```markdown
# Post-mortem: {{ one-line title }}

**Date:** {{ YYYY-MM-DD }}
**Duration:** {{ start time UTC }} → {{ end time UTC }} ({{ minutes }} min)
**Severity:** SEV-{{ 1 | 2 }}
**Author:** {{ name }}
**Status:** {{ draft | reviewed | published }}

## Summary

One paragraph. What happened, from the customer's perspective. Avoid
internal jargon — this section will be lifted into the partner
notification.

## Impact

- Tenants affected: {{ count, or "all" }}
- Requests affected: {{ count or percentage }}
- Revenue at risk: {{ $ range, or "none" }}
- Data loss: {{ yes/no; if yes, scope + recovery status }}

## Timeline (UTC)

- `HH:MM` — Deploy of {{ commit SHA }} lands.
- `HH:MM` — First alert fires: {{ alert name }}.
- `HH:MM` — Oncall acknowledges.
- `HH:MM` — Root cause identified.
- `HH:MM` — Mitigation applied.
- `HH:MM` — Customer impact ends.
- `HH:MM` — All alerts clear.

## Root cause

What specifically went wrong. Be technical. Include the offending code or
config in a snippet if relevant.

## Why it wasn't caught earlier

Walk the defenses: unit test, integration test, code review, staging
canary, production monitoring. Which layer should have caught it, and why
didn't it?

## Resolution

What we changed to end the incident. Distinct from prevention (below).

## Prevention

- [ ] {{ concrete action, owner, due date }}
- [ ] {{ … }}

Each action must be specific and assignable. "Improve monitoring" is not
an action; "Add `velox_webhook_outbox_backlog` gauge, owner @sagar, due
YYYY-MM-DD" is.

## What went well

At least one item. Bias the team toward reinforcement of good behaviors.

## What we're grateful for

Optional, brief. Credit specific people for specific actions during the
incident. Written in the first person from the incident commander.
```

**Review process:** draft posted to `#postmortems` within 3 business days,
reviewed by at least one other engineer, then published to the team. For
incidents with partner-visible impact, a sanitized version goes to the
partner by day 5.
