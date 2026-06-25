# ADR-062: Async obligation backbone — extend the outbox, not Temporal/River/pgx (build deferred)

**Date:** 2026-06-25
**Status:** Decided (design + build-vs-buy). **Build deferred** to a named trigger.

This ADR records a decision we are deliberately **not building yet**, so the
next session inherits the verdict instead of re-litigating Temporal vs River vs
a `database/sql`→pgx migration. ADR-061 forward-references it; this is that doc.

## Context

Velox runs **four bespoke re-drive sweeps** that each recover a post-commit
async side-effect that failed: tax-commit (#267 `RetryPendingTaxCommit`),
tax-reversal (#310 `RetryPendingTaxReversal`), clawback-issue (ADR-057
`RetryPendingClawbackIssue`), and CN-tax-reversal (PR2/ADR-061
`RetryPendingCreditNoteTaxReversal`). Each is the same machinery re-implemented:
a scheduler tick + leader gate + a `ListPendingX` scan + a `RetryX` loop + a
status/marker column + its own retry/aging semantics. That duplication grows
linearly with each new async effect.

The recognised industry consolidation is a **single durable, in-DB-transaction-
enqueued obligation queue** (verified 2026-06-25, deep-research wa8o61214:
River/Oban/Solid Queue are mainstream; the queue COMPLEMENTS — never replaces —
idempotency + reconciliation). The question: do we adopt a product (River /
Temporal), and do we migrate `database/sql`→pgx-native to unlock one?

Velox facts that decide it: `database/sql` over the `pgx/v5/stdlib` driver
(`*sql.DB`/`*sql.Tx`; the RLS tenant-isolation core is built on the `BeginTx`
wrapper — 60 non-test files / 385 query sites). We **already operate** a
transactional outbox (`webhook_outbox`, ADR-040): in-tx `Enqueue(ctx, tx, …)`,
`FOR UPDATE SKIP LOCKED` draining, DLQ (15 attempts / ~72h backoff),
advisory-lock leader. It is wired as a **customer-notification fan-out**
(`dispatcher → svc.Dispatch → matchesEvent` against `'*'`/prefix subscriptions).

## Decision

**When we consolidate, generalise the existing `webhook_outbox` into the
obligation queue** — add an internal/external **discriminator** + an
obligation handler-registry so internal obligations (tax reversal, refund
retry, …) drain to in-process handlers instead of fanning out to `'*'`-
subscribed customer endpoints. Then migrate the four sweeps onto it.

Decided **against**:

- **Temporal / DBOS (durable execution).** Its enqueue is an **RPC to a separate
  datastore**, so it is NOT in our Postgres transaction → reintroduces the exact
  dual-write the queue eliminates. It is also a stateful cluster (or SaaS that
  breaks the self-host wedge) solving a **bigger problem** — long-running,
  multi-step, stateful *workflows* — than we have (single-step idempotent jobs).
- **River (Go/Postgres) — for now.** Right shape, but its transactional
  `InsertTx` takes a **pgx-native `pgx.Tx`**; we are on `database/sql`, so its
  headline in-tx-enqueue property would not compose with our coordinator
  `*sql.Tx` without migrating the whole data layer. Buying the library and
  losing its one advantage is the worst of both.
- **`database/sql` → pgx-native migration, now.** Evaluated explicitly because it
  is the gate to River. We already use pgx as the *driver*; pgx-**native** would
  add binary-protocol perf, rich types, `LISTEN/NOTIFY`, and `COPY` — but our
  workload exercises **none** of them today (transactional billing CRUD at
  moderate QPS; money is `int64` cents; JSONB is already hand-marshalled). The
  one real future fit is **`COPY` for high-volume usage-event ingestion** (the
  AI-native metering wedge). "Do it now while the codebase is small" does not win
  because (a) the **risk is size-independent** — it touches the RLS security
  boundary and every money query, where subtle type/NULL/query-mode differences
  hide money bugs and RLS leaks; (b) **zero payoff today** at pre-launch; and
  (c) the migration is **incremental and path-local** — pgx-native and
  `database/sql` coexist in one binary (two pools, same Postgres), so the future
  cost is "add a pgx pool on the ingestion path," not a big-bang refactor. So the
  "it only gets bigger later" premise does not hold.

## Status: build deferred

The **design** is decided; the **build** is not scheduled. Reasons: at low/mid
scale (0 customers, 1 dev) the four bespoke sweeps are **correct** and the
consolidation is a *maintainability* win, not a correctness need — adopting any
queue does not make money more correct (exactly-once effects = at-least-once
delivery + idempotent consumers + reconciliation, which the sweeps already are).
Per `feedback_pre_launch_scoping` / `feedback_no_overengineering`, a generic
async backbone is a solution to a problem (many heterogeneous effects + an ops
team) we do not have yet.

**Triggers to build the obligation queue:** a durable async obligation that is
NOT webhook-notification-shaped AND for which a targeted reconciler is
insufficient, OR the count of distinct async effects outgrows "one outbox + a
few bespoke sweeps" (rule of thumb: ~6+), OR a `cmd/velox-worker` process split
(ADR-040 anticipates it).

**Triggers to revisit pgx-native (and then River):** usage-event ingestion
becomes a *measured* bottleneck → stand up a pgx-native pool on **just the
ingestion path**, prove the `COPY` gain against a target, expand only if
justified. River falls out for free on whatever ends up pgx-native — the clean
way to arrive, not a speculative rewrite.

## Consequences

- We keep four small, proven, individually-correct sweeps for now; the
  duplication is real but bounded and not yet painful.
- The CN-tax-reversal recovery (PR2) deliberately did **not** route onto
  `webhook_outbox` (the fan-out leak) and used a per-CN marker + structural
  sweep instead — that decision stands until this queue exists.
- ADR-061's "committed PR3" wording is clarified: the *design* is committed
  (this ADR), the *build* is trigger-gated.

## Related

- ADR-040 — the transactional outbox this would generalise (`webhook_outbox`).
- ADR-056 — coordinator-tx (the in-tx-enqueue primitive the outbox already uses).
- ADR-061 (#313) — atomic+recoverable `creditnote.Issue()`; names this ADR as the
  home for the build-vs-buy + pgx rationale.
- `reference_billing_reliability_industry_grounding` (memory) — the verified
  River/Oban/Solid-Queue/Temporal + sync-vs-async grounding.
