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

### 4. The migration bridge — DONE (uninstalled 2026-07-14)

During the migration, `LogInTx` did NOT mark the request handled: an in-tx
emission can be rolled back after the call returns, and a secondary mutation
deep inside another route's flow must not suppress that route's own catch-all
row. Suppression was a REQUEST-scoped decision made by the route's owning
handler (`audit.MarkHandled`) after its operation committed. The catch-all
stayed installed until the END of the arc, and the uninstall shipped as one
complete change, never split.

**It happened.** The catch-all writer (`mw.AuditLog`), its heuristic
classifiers (`parseAuditPath`, `extractLabel`, `extractID`,
`canonicalResourceType`), its response buffer (`bufferedResponse`), its writer
(`writeAudit`), and `audit.MarkHandled` are DELETED. What replaced them:

- **A route-audit registry** (`internal/api/audit_routes.go`) — one table
  mapping every mutating (method, chi route pattern) to `explicit` (the route
  emits its own typed row; the note names the emitter) or `exempt(reason)`,
  where reason is a CLOSED enum and the note records the justification and any
  accepted loss. 102 routes: 92 explicit, 10 exempt. (Backfill and bootstrap were reclassified from exempt to explicit during review: an operator backdating usage, and a first-run install minting a live secret key, are both money-relevant actions that must leave evidence.)
- **A two-way-diff arch test** (`audit_routes_test.go`) — walks the LIVE chi
  route table: an undeclared mutating route fails CI, and a stale registry
  entry fails CI. This is the drift gate; a new route now forces an audit
  decision at review time. It is the *only* thing standing between a new route
  and silent un-auditing, which is why it diffs both directions.
- **A pure-observer detector** (`mw.AuditCoverage`, mounted at the ROOT, not
  on /v1 — RC1's fix). It NEVER mutates the response and NEVER buffers the
  body: a mutating 2xx that produced no audit row and is not declared exempt
  increments `velox_audit_uncovered_mutation_total{route}` and logs
  `UNCOVERED MUTATION`. It reports; it does not invent.

Two amendments this forced, both deliberate:

- **`Log`/`LogInTx` now self-mark** (the §4 rule above, inverted). That rule
  was written for the catch-all WRITER, where a stray mark cost a route its
  only row. It does not carry to an OBSERVER: a rolled-back emission means the
  handler answers non-2xx, and the detector only inspects 2xx. Self-marking was
  also *required* — `POST /v1/tenants` and both public checkout routes emit only
  in-tx and sit outside the old catch-all, so nothing else would account for
  them. Named blind spot: a route that emits nothing of its own but nests
  someone else's emission reads as covered at runtime — bounded by the
  registry's static gate.
- **`MarkSkip` survives, `MarkHandled` does not.** `MarkSkip` is now the
  detector's "this request mutated nothing" declaration, and it is load-bearing:
  four live paths return 2xx having mutated nothing (a stale-cookie logout, a
  password reset for an unknown email — the fixed 200 *is* the enumeration
  defence, a settings save that changed no field, an idempotency replay), plus
  the read-only previews, a recipe re-apply that installs nothing, and a
  credit-note issue that defers. Without the declaration each would report as an
  uncovered mutation forever, and a detector that cries wolf on a normal client
  retry is a detector nobody keeps. `MarkHandled` is gone because it was the
  exported escape hatch that let a handler ASSERT coverage it did not have; the
  only thing that may claim coverage now is a row that actually landed.

The observer wraps the response's STATUS only (chi's `middleware.WrapResponseWriter`),
never its body, so `http.Flusher` / `Hijacker` / `ReaderFrom` pass through untouched —
the streaming CSV exports and the SSE stream are unaffected by it. A regression test
pins that property.

### 5. Sim-time axis (ADR-086 amendment)

`sim_effective_at` + `test_clock_id` are promoted to nullable audit_log
columns (migration 0148, partial index on the clock slice) with a typed
`SimContext` on the emission input, because after ADR-086 teardown the audit
log is a simulation's ONLY surviving record — yet it couldn't be queried by
sim time or clock. LogInTx stamps columns AND mirrors the legacy metadata
keys the dashboard renders. Query params / UI filters ship only after
stamping reaches parity across writers (the sim-axis surfacing PR), so the
filter can never lie by omission.

### 6. Row integrity: the parts of a row a client must not be able to write

Two residual holes in the row's *evidentiary* value, both closed here.

**`request_id` was client-writable.** chi's `middleware.RequestID` uses an
inbound `X-Request-Id` header verbatim when present, generating one only when it
is absent. `audit_log.request_id` is presented in the UI as forensic correlation
evidence and is what support uses to join a customer's report back to server
logs — so a caller could pick the correlation id on their own audit rows: pin it
to a constant to make their actions unjoinable, collide it with another tenant's
traffic, or forge a value implying an action came from elsewhere. Correlation
evidence an adversary can write is not evidence; CloudTrail's `eventID`, Stripe's
request id and GCP's `insertId` are all server-minted.

`mw.RequestID` (internal/api/middleware/request_id.go) replaces chi's and ALWAYS
mints server-side (`req_` + xid), storing under chi's own `RequestIDKey` so every
existing `chimw.GetReqID` reader — the audit writer, `telemetry.ContextHandler`,
`respond.go`, `payment/stripe` — is unchanged.

The inbound value is DROPPED, not preserved under a second key. Nothing consumed
it (no `X-Request-Id` reference exists in the repo, web-v2 or docs; the published
contract is the `Velox-Request-Id` RESPONSE header); cross-service trace
continuity is already carried properly by W3C Trace Context via `mw.Tracing()`;
and recording an unverified client string anywhere on the row — even under an
honestly-named key — is precisely the "unverified input in a permanent record"
class this redesign exists to kill. **Accepted loss, written down:** rows written
BEFORE this change may carry a client-supplied `request_id`, and append-only
forbids rewriting them. A `req_` prefix marks the server-minted era; a
`request_id` without it is not trustworthy as provenance.

**The append-only triggers were the only barrier.** `audit_log` is immutable via
BEFORE UPDATE/DELETE (0011) and BEFORE TRUNCATE (0115) triggers that hold even
for an RLS-bypassing role — but `velox_app` still HELD `UPDATE`/`DELETE`/
`TRUNCATE` from 0001's blanket `GRANT ALL` (0115's own header admits it was never
revoked). One dropped or disabled trigger and the tamper-evidence log was
writable by the role that serves customer traffic. Migration 0150 revokes those
three privileges from `velox_app` (INSERT/SELECT retained — the writer only ever
appends). The privilege system and the triggers now fail independently:
`velox_app` is refused at the permission check (42501) before a trigger runs; the
owner/superuser is still refused by the trigger (P0001). Retention purges are
unaffected — they remain an owner-role operation, as 0011 documented.
### 7. Read egress: bulk exports are audited, fail-closed, emit-before-stream

RC1 named it and this closes it: a full-tenant EXPORT produced zero audit rows.
The catch-all only ever considered mutating methods, and the export handlers
emitted nothing — so copying every customer's PII, every invoice, every
subscription, or a year of usage events left the log silent. A tamper-evidence
system that cannot show who copied the evidence has a hole in its chain of
custody; Stripe and AWS CloudTrail both log data-export/read events.

- **`action=export`** (`domain.AuditActionExport`) is a NEW top-level wire string
  — the first in the vocabulary that records a READ. `resource_type` is the
  exported resource; `resource_id` is EMPTY (a bulk export has no single
  subject); metadata carries the filters and the exact filename delivered.
- **Emit BEFORE the first byte, fail closed.** The row is written before the
  stream opens, and a failed write means 500 with nothing streamed. The ordering
  is the decision: a row written at stream COMPLETION is defeated by killing the
  connection mid-stream, so pages of PII would egress with nothing recorded.
  Emit-then-stream can only OVER-record (a row for a file that later aborted —
  reconcilable against the EXPORT_INCOMPLETE marker); stream-then-emit
  UNDER-records, and in an append-only log that is unrecoverable.
- **Own-tx, detached.** An export is a read: there is no business transaction for
  LogInTx to ride. It uses `Logger.Log` (own tx, `context.WithoutCancel`), so a
  client that hangs up the moment the export starts still leaves the row.
- **No row count on the row.** It is unknowable before the stream starts, and a
  count we cannot honour is a lie in a permanent record.
- **The audit log exports itself** (`GET /v1/exports/audit-log.csv`, same
  permission as the read route). It replaces a dashboard Export button that paged
  the API in the browser and stopped at 50,000 rows — a silent truncation of the
  compliance evidence itself. `Logger.Stream` applies no cap. Because the row is
  written first, the exported file CONTAINS the record of its own export.
- **Declared, not inferred** — a small read-egress registry
  (`auditEgressRegistry`) with its own two-way-diff arch test over the live GET
  routes under `/v1/exports`. Deliberately NOT a general GET axis in the mutating
  registry: five routes do not justify declaring an audit story for every read
  endpoint, and a registry nobody can keep current is a registry that lies. The
  runtime detector is unchanged (it observes mutations only).
- **ACCEPTED LOSS, written down:** ordinary paginated list reads are NOT audited.
  An operator can still copy the tenant by walking `GET /v1/customers?limit=…`,
  and `audit_log` will not show it. What it CAN answer is "did anyone take the
  one-click bulk export, and when" — the question an auditor asks and the action a
  departing employee takes. A row per list call would put audit_log on the
  dashboard's hottest read path, and that noise is what makes an audit log
  unreadable (same volume argument as the ingest exemption). CLOSURE TRIGGER: a DP
  or auditor asking for PII-read evidence (SOC 2 CC6.x) → an access-log-derived
  read trail, aggregated per session/actor, not one row per GET.

Adjacent hardening shipped with it: **CSV formula injection**. A cell beginning
with `= + - @` (or TAB/CR) executes as a formula in Excel/Sheets/LibreOffice, and
customer display names flow into these files — including the audit log's own
`resource_label`. Both builders neutralize (Go free-text columns; the browser-side
builder), because the CSV is the artifact an operator hands an AUDITOR, and a file
that runs code when opened is not evidence. The client-side neutralizer exempts
cells that ARE numbers, or every negative amount in a finance export becomes text
and `SUM()` breaks.

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
