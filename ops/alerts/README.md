# Velox alert rules

Prometheus alerting rules for the Velox API, ready to import into:

- **Self-hosted Prometheus** — drop into `rule_files`, reload Prometheus.
- **Grafana Cloud / Mimir** — paste into a managed alert rule group, or sync
  via `cortex-tool` / the Grafana Terraform provider.
- **Datadog** — rules are in Prometheus format; port the expressions manually
  (Datadog monitors use their own query syntax, but the thresholds and
  grouping translate directly).

Each rule file targets one subsystem so they can be enabled piecemeal.

## Files

| File | Subsystem | Alerts |
|------|-----------|--------|
| `api.yaml` | HTTP surface | `VeloxAPIErrorBudgetBurn` (page), `VeloxAPIHighLatency` (ticket), `VeloxAPIHighConcurrency` (info) |
| `billing.yaml` | Billing engine | `VeloxBillingCycleFailing` (page), `VeloxBillingCycleSlow` (ticket), `VeloxNoInvoicesGenerated` (info) |
| `payments.yaml` | Stripe + PaymentIntents | `VeloxStripeBreakerOpen` (page), `VeloxPaymentSuccessRateLow` (ticket), `VeloxPaymentIntentFailureRate` (ticket) |
| `webhooks.yaml` | Outbound delivery | `VeloxWebhookFailureRate` (ticket) |
| `audit.yaml` | Audit log | `VeloxAuditWriteErrors` (page) |
| `scheduler.yaml` | Billing/dunning scheduler | `VeloxSchedulerStale` (page) — via `blackbox_exporter` probe |

## Severity labels

Every rule carries a `severity:` label consumed by Alertmanager routing:

- `page` — SEV-1. Route to PagerDuty / OpsGenie.
- `ticket` — SEV-2. Route to the oncall queue (Slack `#oncall`).
- `info` — SEV-3. Route to a low-noise channel (`#velox-signals`).

Severity semantics and response-time expectations live in
[`docs/ops/runbook.md`](../../docs/ops/runbook.md#severity-definitions).

## Importing

### Self-hosted Prometheus

```yaml
# prometheus.yml
rule_files:
  - /etc/prometheus/rules/velox/*.yaml
```

```bash
# Copy the rules and reload
cp ops/alerts/*.yaml /etc/prometheus/rules/velox/
curl -X POST http://localhost:9090/-/reload
```

### Grafana Cloud / Mimir

Use `mimirtool rules load` or `cortextool rules sync` with a `namespace` per
file:

```bash
mimirtool rules load \
  --address=https://prometheus-prod-XX.grafana.net/api/prom \
  --id=<stack_id> \
  --key=<api_key> \
  ops/alerts/*.yaml
```

Or in Terraform, one `grafana_rule_group` resource per file.

## `blackbox_exporter` config for `VeloxSchedulerStale`

The scheduler alert requires a `blackbox_exporter` probe hitting
`/health/ready` because the last-run timestamp is currently surfaced only in
the health response body, not as a Prometheus metric.

```yaml
# prometheus.yml (scrape_config)
- job_name: velox_ready
  metrics_path: /probe
  params:
    module: [http_2xx]
  static_configs:
    - targets:
        - https://api.velox.dev/health/ready
  relabel_configs:
    - source_labels: [__address__]
      target_label:  __param_target
    - source_labels: [__param_target]
      target_label:  instance
    - target_label:  __address__
      replacement:   blackbox-exporter:9115
```

`/health/ready` returns non-2xx when the scheduler has not reported a run
within 2× its configured interval, so `probe_success == 0` catches staleness
without extra logic.

## Instrumentation gaps (tracked)

Three alerts mentioned in the readiness plan are **not** shippable today
because their underlying signal is not yet exported as a Prometheus metric.
Each playbook in the runbook documents a `postgres_exporter`/blackbox-based
workaround usable until the gauge lands.

| Gap | Current workaround | Proper fix |
|-----|--------------------|------------|
| Scheduler heartbeat as a gauge | `blackbox_exporter` probe of `/health/ready` (shipped in this file). | Export `velox_scheduler_last_run_timestamp_seconds` from `internal/api/router.go`'s `RecordSchedulerRun()`. |
| DB connection pool saturation | `postgres_exporter`'s `pg_stat_database_numbackends{datname="velox"}` against `max_connections`. | Instrument `*sql.DB.Stats()` into `velox_db_pool_{open,in_use,idle,wait_count,wait_duration}` gauges. |
| Outbox backlog size | `postgres_exporter` custom query: `SELECT count(*) FROM webhook_outbox WHERE status='pending'`. | Periodic gauge `velox_webhook_outbox_backlog` exported by the outbox dispatcher. |

None of the three gaps is blocking design-partner readiness — the workarounds
are functional. Close them during Phase 4/Tier 1 so oncall is not juggling
two observability backends.

## Validation

Before importing, lint the rules locally with `promtool`:

```bash
promtool check rules ops/alerts/*.yaml
```

Expected output: `SUCCESS: <N> rules found`. A syntax error prints the file
and line.
