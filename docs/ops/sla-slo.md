# Velox — Service Level Objectives (SLOs)

## Overview

These SLOs define the reliability targets for Velox. They inform alerting
thresholds, capacity planning, and contractual SLAs with customers.

---

## API Availability

| Metric | Target | Measurement |
|--------|--------|-------------|
| Uptime (monthly) | 99.95% | `1 - (5xx responses / total responses)` over calendar month |
| Allowed downtime | ~22 minutes/month | Unplanned outages only; scheduled maintenance excluded |
| Error budget | 0.05% of requests | Consumed by 5xx responses; alerts fire at 50% burn rate |

**Prometheus query:**
```promql
1 - (sum(rate(velox_http_requests_total{status=~"5.."}[30d])) / sum(rate(velox_http_requests_total[30d])))
```

---

## API Latency

| Percentile | Target | Applies to |
|------------|--------|------------|
| p50 | < 50ms | All endpoints |
| p95 | < 200ms | All endpoints |
| p99 | < 1s | All endpoints |
| p99 | < 5s | Invoice PDF generation |

**Prometheus query:**
```promql
histogram_quantile(0.99, rate(velox_http_request_duration_seconds_bucket[5m]))
```

---

## Billing Accuracy

| Metric | Target | Notes |
|--------|--------|-------|
| Invoice correctness | 100% | Zero tolerance for incorrect amounts |
| Tax calculation accuracy | +/- 1 cent | Per invoice, using basis-point integer math |
| Credit application accuracy | 100% | Ledger must balance exactly |
| Double-billing prevention | 0 occurrences | Enforced by idempotency constraint |

**Verification:** Reconciliation query comparing `SUM(line_items)` vs `invoice.subtotal_cents` run daily.

---

## Billing Timeliness

| Metric | Target | Measurement |
|--------|--------|-------------|
| Invoice generation latency | < 5 min from billing period end | Time between `next_billing_at` and invoice `created_at` |
| Billing cycle completion | < 30 min for full batch | End-to-end scheduler cycle |
| Payment intent creation | < 10s after invoice finalization | Auto-charge latency |

**Prometheus query:**
```promql
histogram_quantile(0.95, rate(velox_billing_cycle_duration_seconds_bucket[1h]))
```

---

## Webhook Delivery

| Metric | Target | Notes |
|--------|--------|-------|
| First delivery attempt | < 30s after event | Time from event creation to first HTTP attempt |
| Delivery success rate | > 99% | Measured over 7-day window (includes retries) |
| Maximum delivery time | 24h | After 5 retry attempts with exponential backoff |
| Retry schedule | 1m, 5m, 30m, 2h, 24h | With +/- 30s jitter |

**Prometheus query:**
```promql
rate(velox_webhook_deliveries_total{status="succeeded"}[7d]) /
(rate(velox_webhook_deliveries_total{status="succeeded"}[7d]) + rate(velox_webhook_deliveries_total{status="failed"}[7d]))
```

---

## Payment Processing

| Metric | Target | Notes |
|--------|--------|-------|
| Payment success rate | > 95% | Measured on first attempt (before dunning) |
| Payment processing time | < 10s | Stripe PaymentIntent creation + confirmation |
| Dunning recovery rate | > 30% | Percentage of failed payments recovered via retry |

---

## Data Durability & Recovery

| Metric | Target | Notes |
|--------|--------|-------|
| Recovery Point Objective (RPO) | 5 minutes | Maximum data loss window |
| Recovery Time Objective (RTO) | 1 hour | Maximum time to restore service |
| Backup frequency | Daily full + continuous WAL | Point-in-time recovery enabled |
| Backup retention | 30 days | With 7-year archive for financial records |

---

## Monitoring & Alerting

| Metric | Target | Notes |
|--------|--------|-------|
| Alert response time | < 15 min (critical) | Time from alert fire to human acknowledgment |
| Alert noise ratio | < 10% false positives | Alerts that fire but require no action |
| Mean time to detect (MTTD) | < 5 min | Time from incident start to alert firing |

---

## SLA vs SLO

- **SLOs** (this document): Internal targets that drive engineering decisions
- **SLAs** (customer contracts): Typically 1 tier below SLOs to provide buffer
  - Example: SLO = 99.95% uptime → SLA = 99.9% uptime
  - SLA breach = contractual credits to customer

---

## Review Cadence

- SLOs reviewed quarterly
- Error budget reviewed monthly
- Alerting thresholds calibrated after each incident
