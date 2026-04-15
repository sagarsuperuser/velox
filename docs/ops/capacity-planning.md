# Velox — Capacity Planning Guide

## Quick Reference

| Scale | Customers | Subscriptions | API RPS | Instances | Postgres | Redis |
|-------|-----------|---------------|---------|-----------|----------|-------|
| Starter | < 1K | < 1K | < 50 | 1 | 2 vCPU, 4GB | 1 node |
| Growth | 1K–10K | 1K–10K | 50–200 | 2–3 | 4 vCPU, 8GB | 1 node |
| Scale | 10K–100K | 10K–50K | 200–1K | 3–5 | 8 vCPU, 32GB | 3-node cluster |
| Enterprise | 100K+ | 50K+ | 1K+ | 5+ | 16+ vCPU, 64GB+ | 3-node cluster |

---

## Component Sizing

### Velox API Instances

Each instance is a stateless Go binary. Scale horizontally.

| Resource | Starter | Production |
|----------|---------|------------|
| CPU | 0.25 vCPU | 0.5–1 vCPU |
| Memory | 128 MB | 256–512 MB |
| Replicas | 1 | 2+ (HA) |

**When to scale:** Add instances when p99 latency exceeds 500ms or CPU > 70%.

### PostgreSQL

Velox is database-bound. Postgres sizing matters most.

| Metric | Starter | Production |
|--------|---------|------------|
| CPU | 2 vCPU | 4–16 vCPU |
| Memory | 4 GB | 8–64 GB |
| Storage | 20 GB | 100 GB+ (SSD/NVMe) |
| IOPS | 3K | 10K–50K |
| Connections | 20 | 50–200 |

**Connection pooling formula:**
```
max_connections = (instances * DB_MAX_OPEN_CONNS) + 10 (admin/monitoring)
```
Default: 3 instances * 20 conns = 60. Set Postgres `max_connections = 100`.

**When to scale:**
- CPU > 60% sustained → upgrade instance
- Storage > 70% → expand or enable auto-scaling
- Connection wait time > 100ms → increase pool size or add read replica

### Redis

Used for rate limiting only (GCRA algorithm). Very low resource needs.

| Resource | Starter | Production |
|----------|---------|------------|
| Memory | 64 MB | 256 MB |
| Instance | 1 node | 3-node cluster (HA) |

**Estimated memory per rate limit key:** ~100 bytes. 100K tenants = ~10 MB.

---

## Billing Scheduler Throughput

The scheduler processes subscriptions using `FOR UPDATE SKIP LOCKED` for safe horizontal distribution.

| Config | Throughput | Latency |
|--------|-----------|---------|
| 1 instance, batch=50, 1h interval | 50 subs/hour | Suitable for < 1K subs |
| 2 instances, batch=50, 1h interval | 100 subs/hour | Suitable for < 2.5K subs |
| 3 instances, batch=100, 30m interval | 600 subs/hour | Suitable for < 15K subs |
| 5 instances, batch=200, 15m interval | 4K subs/hour | Suitable for < 100K subs |

**Formula:**
```
throughput = instances * batch_size * (60 / interval_minutes)
```

**When to scale the scheduler:**
- If invoices are generated more than 30 minutes after `next_billing_at`
- Monitor with: `histogram_quantile(0.95, rate(velox_billing_cycle_duration_seconds_bucket[1h]))`

**Beyond 100K subscriptions:** Consider migrating from the PostgreSQL scheduler to a queue-based system (Temporal, SQS). ADR-006 documents this planned upgrade path.

---

## Database Growth Estimates

| Table | Row size (avg) | Rows/year (10K customers) | Storage/year |
|-------|---------------|--------------------------|-------------|
| invoices | 500 bytes | 120K (monthly billing) | 60 MB |
| invoice_line_items | 300 bytes | 360K (3 lines/invoice avg) | 108 MB |
| usage_events | 200 bytes | 50M (high-usage SaaS) | 10 GB |
| credit_ledger | 200 bytes | 240K | 48 MB |
| audit_log | 400 bytes | 2M | 800 MB |
| webhook_deliveries | 500 bytes | 500K | 250 MB |

**Total estimated storage (10K customers, 1 year):** ~12 GB

**Usage events dominate storage.** Consider a retention policy:
- Raw events: 90 days
- Aggregated summaries: 7 years (for invoicing audit trail)

---

## Key Metrics to Monitor

### Scaling Triggers

| Metric | Threshold | Action |
|--------|-----------|--------|
| API p99 latency | > 1s for 5 min | Add API instances |
| DB CPU | > 60% sustained | Upgrade Postgres |
| DB connections waiting | > 0 for 5 min | Increase pool or add read replica |
| Billing cycle duration | > 50% of interval | Reduce interval or add instances |
| Invoice generation lag | > 30 min | Scale scheduler |
| Redis memory | > 75% | Upgrade or flush stale keys |

### Prometheus Queries

```promql
# API saturation
sum(velox_http_requests_in_flight) / count(up{job="velox"})

# DB connection pool usage (via pg_stat_activity)
pg_stat_activity_count{datname="velox", state="active"}

# Billing throughput
rate(velox_invoices_generated_total[1h])

# Invoice generation lag (seconds since last billing cycle)
time() - max(velox_billing_cycles_total)
```

---

## Cost Estimates (AWS, monthly)

| Scale | Compute | Postgres (RDS) | Redis (ElastiCache) | Total |
|-------|---------|----------------|---------------------|-------|
| Starter | $15 (1x t3.small) | $30 (db.t3.small) | $15 (cache.t3.micro) | ~$60 |
| Growth | $90 (3x t3.medium) | $200 (db.r6g.large) | $30 (cache.t3.small) | ~$320 |
| Scale | $250 (5x t3.large) | $800 (db.r6g.xlarge) | $150 (3x cache.r6g.large) | ~$1,200 |

Does not include: backup storage, data transfer, monitoring tools.
