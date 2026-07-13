# ADR-090: In-transaction audit emission (LogInTx) and the declared-coverage model

Date: 2026-07-13
Status: Accepted
Relates: ADR-089 (fail-closed retirement — the interim step this supersedes structurally), ADR-030 (wall-clock audit timestamps — unchanged), ADR-086 (sim-data lifecycle — amended: sim-axis columns ship now)

## Context

The 2026-07-13 audit-subsystem e2e audit (68 verified findings, 3-judge
panel, 5-lens adversarial design panel) identified three root causes in the
audit log's write model:

- **RC1 — coverage anchored to transport.** The HTTP middleware catch-all
  guaranteed "every /v1 mutation gets a row," so background/engine writers
  (dunning-driven cancels, engine voids, reconciler credit notes), routes
  mounted outside the /v1 block, and GET-shaped data egress silently got
  nothing. The perceived safety net bred the gaps.
- **RC2 — row content derived by heuristics.** parseAuditPath (a hardcoded
  verb list with 5 dead entries and ~25 live segments missing;
  resource_id = parts[1] unconditionally) and response-body sniffing
  fabricated FALSE permanent records in an append-only compliance log —
  e.g. deleting a meter's pricing rule recorded as "deleted meter X".
- **RC3 — audit as an out-of-band observer.** Each row rode its own tx, so
  phantom rows (logout logged before revoke), lost rows (client
  disconnect), and the fail-open/fail-closed response swap (whose
  idempotency interaction was the audit's worst finding; ADR-089) were all
  structural.

## Decision

### 1. Emission joins the business transaction (shared fate, no savepoints)

`audit.Logger.LogInTx(ctx, tx, Entry)` writes the audit row on the CALLER's
transaction: the mutation and its evidence commit or roll back together.
"Committed but unrecorded" and "recorded but rolled back" become
unrepresentable on covered paths — this is real fail-closed, for every
tenant, with no post-commit window to police and no response swapping.

The panel explicitly REJECTED a savepoint variant (absorb audit-INSERT
failure for non-strict tenants): its only trigger class is deterministic
code bugs, which totality (below) plus CI kill at build time — and absorbing
the failure means silently committing money state that diverges from the
compliance record, which is RC3 again. Savepoint-per-mutation also carries a
known subtransaction-overflow pathology at scale.

Shared fate cuts both ways on external-effect flows (Stripe charge executed,
persisting tx then aborts on an audit bug): the state is recovered by the
same webhook/reconciler machinery that already owns effect-then-crash, and
the recovered persist emits the audit row with recovery provenance. A
deterministic emission bug on such a path is an OUTAGE class (Stripe retries
loop until a deploy) — accepted as strictly better than silently-unrecorded
money mutations, and gated by the totality tests.

### 2. LogInTx is total

An input a caller can construct must never abort a money tx on telemetry
grounds:

- `tenant_id` comes from the transaction's own `app.tenant_id` GUC via
  `NULLIF(current_setting(…), '')` — it cannot disagree with RLS, and a
  GUC-less transaction violates NOT NULL loudly. The NULLIF matters: on a
  pooled connection a reverted `is_local` GUC reads back as EMPTY STRING,
  not NULL (the dominant case in a warm pool), and without the fold the
  loud failure would silently depend on the incidental tenants FK — which
  retention-driven partitioning could remove later.
- `livemode` is trigger-stamped from the same tx session (0021) — the row
  lands in the plane of the transaction it rides in, by construction.
- Unmarshalable metadata degrades to `{"marshal_error": …}` instead of a
  nil that would violate the JSONB NOT NULL.
- Action/resource wire strings are the existing constants, FROZEN — FE
  filter vocabulary, badge maps, and historical rows key on them. A
  vocabulary round-trip integration test INSERTs every declared value so a
  schema-rejected constant cannot ship (the enum+CHECK drift class).

### 3. Ownership rule for the migration (store-interface surgery)

The SERVICE owns audit-row content — it builds the typed `audit.Entry` where
intent is known. The layer that OWNS the transaction exposes it: tx-owning
store methods gain `…Audited(…, emit func(tx, out) error)` variants that run
the service-built emission before commit. Handlers stop writing audit rows.
Background/engine/webhook writers pass their BUSINESS tx — never an
audit-only tx.

First batch (this PR): credit grant/adjust. The handler's post-hoc `Log`
calls are gone; emissions ride the ledger tx (`AppendEntryAudited`,
`AdjustAtomicAudited`). Each subsequent domain migrates in its own PR under
the money-path playbook gates.

Declared scope note: because the emission attaches inside `Service.Grant`
(own-tx path), every caller of that path is audited — the operator/API
routes AND the proration-fallback + credit-note-bridge flows that reach
`Grant` from other requests. That widening is deliberate: a grant is a
grant, and the actor attribution (the enclosing request's identity) is
exactly ADR-090's D16 rule for operator-triggered synchronous effects. Amended in PR4: `GrantTx` (and therefore `GrantForCreditNoteTx`, which
routes through it) now emits on the caller's transaction, closing the
own-tx/caller-tx divergence — a credit-note Issue tx deliberately carries
BOTH its `credit_note.issued` row and the grant's `grant` row.
`GrantCommitForInvoiceTx` stays unaudited by design: it exists only inside
invoice finalize, whose own `finalize` row is the canonical evidence.

### 4. The migration bridge

`LogInTx` does NOT mark the request handled: an in-tx emission can be
rolled back after the call returns, and a secondary mutation deep inside
another route's flow must not suppress that route's own catch-all row.
Suppression is a REQUEST-scoped decision: the route's owning handler calls
`audit.MarkHandled` after its operation commits. The catch-all (still
installed), the heuristic classifiers, and `MarkSkip`/`MarkHandled` are
deleted only at the END of the arc (registry + pure-observer detector PR),
after a route-walk arch test proves declared coverage parity — uninstall is
one complete change, never split.

### 5. Sim-time axis (ADR-086 amendment)

`sim_effective_at` + `test_clock_id` are promoted to nullable audit_log
columns (migration 0148, partial index on the clock slice) with a typed
`SimContext` on the emission input, because after ADR-086 teardown the audit
log is a simulation's ONLY surviving record — yet it couldn't be queried by
sim time or clock. LogInTx stamps columns AND mirrors the legacy metadata
keys the dashboard renders. Query params / UI filters ship only after
stamping reaches parity across writers (the sim-axis surfacing PR), so the
filter can never lie by omission.

## Consequences

- Explicit emissions gain a hard error contract: callers PROPAGATE LogInTx
  errors (aborting their tx). The `_ =` discard pattern dies with the
  own-tx writer as domains migrate.
- Email sends remain a documented best-effort lossy audit class (SMTP has
  no query-back; post-effect emission with WithoutCancel).
- WAL/latency cost of one extra INSERT inside money txs: single-digit
  percent on real flows (priced by the panel; the duplicate-index drop in
  0147 pre-paid most of it).
- Old heuristic rows are kept as-is (append-only forbids rewriting);
  mixed-era vocabulary in a long-lived dev DB is cosmetic.
- `velox_audit_write_errors_total` narrows to residual own-tx callers as
  migration proceeds; covered paths ride the mutation's own error SLO.
