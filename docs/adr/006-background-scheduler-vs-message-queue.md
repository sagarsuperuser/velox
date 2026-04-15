# ADR-006: Background Scheduler vs. Message Queue

## Status
Accepted

## Date
2026-04-14

## Context
Velox needs to run periodic background work: billing cycle execution (find due subscriptions, generate invoices, charge payments), dunning retry processing (retry failed payments on schedule), and outbound webhook delivery (retry failed deliveries). These workloads are periodic, not event-driven — they poll for work on a timer.

The standard enterprise answer is a message queue (RabbitMQ, SQS) or a workflow engine (Temporal). These provide durability, retry semantics, dead-letter queues, and horizontal scaling. They also add operational dependencies, deployment complexity, and infrastructure cost that may not be justified for a v1 product.

## Decision
Velox v1 uses a simple `billing.Scheduler` — a goroutine with a `time.Ticker` that runs in the same process as the API server. Every tick, it calls `Engine.RunCycle()` for billing and `DunningService.ProcessDueRuns()` for each tenant.

Work distribution and concurrency safety come from PostgreSQL, not the scheduler:

- `GetDueBilling()` uses `SELECT ... WHERE next_billing_at <= $1 ORDER BY next_billing_at LIMIT $2 FOR UPDATE SKIP LOCKED` — multiple scheduler instances (in a horizontal scaling scenario) will not process the same subscription twice
- `ListDueRuns()` uses the same `FOR UPDATE SKIP LOCKED` pattern for dunning runs
- Credit ledger writes use `SELECT ... FOR UPDATE` to serialize concurrent balance mutations

The scheduler is stateless. If the process crashes, the next tick picks up where it left off — `FOR UPDATE SKIP LOCKED` ensures no double-processing, and idempotent PaymentIntent creation (keyed on invoice ID) prevents duplicate charges.

## Consequences

### Positive
- Zero additional infrastructure: no Redis, no RabbitMQ, no Temporal cluster to operate
- Single binary deployment: `go build` produces one artifact that handles HTTP, billing, dunning, and webhooks
- PostgreSQL is already required — using it as a job queue adds no new failure modes
- `FOR UPDATE SKIP LOCKED` provides safe horizontal scaling without a distributed lock service

### Negative
- Billing cycle throughput is limited to what one goroutine can process per tick (mitigated by batch processing and the SKIP LOCKED pattern allowing multiple replicas)
- No built-in dead-letter queue or retry backoff for the scheduler itself (individual operations like payment and webhook delivery have their own retry logic)
- Observability requires structured logging rather than queue-native dashboards
- No fan-out: a single slow subscription blocks the rest of the batch within that tick

### Trade-offs
- We trade scalability ceiling for operational simplicity. A single Velox instance processing 50 subscriptions per tick at 5-minute intervals handles ~600 invoices/hour — sufficient for thousands of customers. When this becomes a bottleneck, adding a second replica with SKIP LOCKED doubles throughput without architecture changes.

## Alternatives Considered
- **Temporal**: Provides durable workflows, automatic retries, and visibility. But it requires a Temporal server cluster (3+ pods), adds ~500ms latency per workflow step, and introduces a significant operational dependency. Rejected for v1 — the complexity is not justified when PostgreSQL SKIP LOCKED provides the core guarantee we need.
- **Redis + worker pool (e.g., Asynq, Faktory)**: Adds Redis as a dependency. Provides faster polling than PostgreSQL but introduces a new failure mode (Redis unavailability). Since our job source-of-truth is already PostgreSQL (subscriptions, dunning runs), adding Redis as an intermediary creates data consistency concerns. Rejected.
- **SQS/PubSub**: Cloud-native but creates vendor lock-in and requires event-driven refactoring. Billing cycles are naturally periodic (run every hour), not event-driven (react to each subscription change). Rejected for poor fit.
