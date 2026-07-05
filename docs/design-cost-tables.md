# Design RFC — Provider cost tables + margin (COGS)

**Status:** PROPOSAL (2026-07-05), pre-panel. Grounded in a 4-platform
quote-verified sweep (wf_2edadfc3: Orb 4/5, Metronome 11/11, OpenMeter 8/9,
LiteLLM/Langfuse/Helicone 14/15 claims verified) + repo census.

## Why (wedge fit)

Wedge build-list item 2/margin story: "what did serving this customer cost
me vs what did I bill them." **Verified differentiation:** Orb/Metronome/
OpenMeter have NO first-class provider-cost table and NO in-app margin
report — Metronome verifiedly pushes margin analysis to the operator's
warehouse via data export. The LLM-tooling layer (LiteLLM, Langfuse,
Helicone) all model cost as a first-class rate table. Velox sits in both
worlds: build the table the billing engines lack, in the LLM-tooling shape.

## Census facts

- LiteLLM ingestion ALREADY stamps observed provider cost on every event's
  metadata (`velox.litellm_response_cost_usd`, `velox.litellm_cost_usd` —
  dollars-as-float, mapper.go:268) — **zero readers today**. Dead data this
  feature activates.
- Events carry `properties` JSONB with canonical dims: `model`,
  `model_raw`, `provider`, `token_type` (ADR-044 roles: input / output /
  cache_read; cache_write deferred), `team_id`, `request_tags`.
- `usage_events` schema: quantity BIGINT (+ decimal quantity col later
  migrations), properties JSONB, metadata; no cost column.
- Rated revenue per (meter, rule, window) already computable
  (CustomerUsageService.rateMeter, ADR-070 resolution); the cost dashboard
  is a CUSTOMER-facing billed-spend surface (token URL) — margin is
  OPERATOR-facing and must NOT leak COGS to customers.
- ADR-045: per-unit rates = decimal.Decimal, line amounts = int64 cents.

## Verified design decisions

1. **Data model — first-class rate table, LLM-tooling shape.**
   `provider_cost_rates`: (tenant_id, provider, model, token_type,
   cost_per_token NUMERIC, currency, effective_from, created_at) —
   exact-match keys in phase 1 (Langfuse's regex match + context-size
   tiers are verified real requirements → named fast-follow).
   Tenant-scoped, RLS, livemode like everything else.
2. **Dual cost source (Langfuse-verified):** event-supplied cost WINS
   (LiteLLM's observed `response_cost` — the sender knows negotiated
   rates); rate-table inference is the default for events without it
   (direct API ingestion). Absorbs the Metronome/OpenMeter
   sender-stamped pattern too.
3. **Attach at INGEST (universal verified pattern — no peer computes at
   read time):** stamp the resolved cost on the usage-event row at write
   (new nullable `provider_cost_micros BIGINT` + `provider_cost_source
   'observed'|'table'` columns — micro-dollars: verified rates go to
   1.5e-06/token; cents too coarse; int64 micros keeps aggregate SUMs
   exact; the RATE stays decimal). One INSERT, no dual-write. Events
   ingested before rates exist stay NULL and the report counts them
   honestly ("N uncosted events") — NO retroactive recompute (verified
   norm at every peer + house no-speculative-backfill rule).
4. **Versioning:** `effective_from` on rate rows; resolution picks the
   row in force at the event timestamp. Rate edits are non-retroactive
   by default (universal verified snapshot semantics). Pre-staging a
   known provider price change = insert a future-dated row. (Effective-
   dating cost rates exceeds verified parity — flagged PLAUSIBLE,
   mechanism proven on Velox's own price side ADR-070.)
5. **Cost ≠ price, separate ledger, explicit naming.** Never overload
   invoice/charge objects (Orb's "costs"-means-charges collision is a
   verified trap). Everything is `provider_cost` / COGS in code and UI.
   Key cost by the same dims as pricing (provider/model/token_type) —
   Metronome's verified margin-consistency guidance.
6. **Sourcing: operator-authoritative CRUD** (API + dashboard).
   Optional explicit one-time import from LiteLLM's public
   community-maintained price JSON (operator-triggered, never
   auto-synced; operator rows always win — Langfuse precedence rule).
   Continuous sync into a money-adjacent table would be a silent-
   fallback violation.
7. **Margin report (in-app — the differentiator, PLAUSIBLE shape, no
   verified reference):** operator-facing, per-customer per-period:
   rated usage revenue vs provider cost vs margin %, broken down by
   model. Same-currency only (OpenMeter's verified no-FX constraint):
   currency mismatch → show cost separately, no margin %, loud note.
   Surfaces: customer detail card + a per-model table. NOT on the
   customer-facing cost dashboard (COGS must not leak).
8. **Cost-plus pricing = phase 2** (2-2 verified split — not table
   stakes; trigger: first DP ask). Stored base-cost + margin stay
   separate fields when it lands.

## Phase 1 build list

- Migration: `provider_cost_rates` table + `usage_events.
  provider_cost_micros`/`provider_cost_source` columns.
- Rate resolution at ingest (both API + LiteLLM paths; batch-cached
  lookup, no per-event query storm).
- LiteLLM observed-cost stamping (floats → micros, documented rounding).
- CRUD API + OpenAPI + dashboard page (Settings-adjacent "Provider
  costs"); import-from-JSON action.
- Margin surfaces: customer detail card + per-model breakdown
  (operator-auth only).
- Tests: rate resolution (effective_from windows, exact-match miss →
  NULL), observed-beats-table precedence, micros rounding, uncosted-count
  honesty, RLS/livemode isolation.
- ADR-079 + CHANGELOG + MANUAL_TEST flow.

## Open questions for the panel

1. Micros-int64 stamp vs NUMERIC stamp on the event row (SUM exactness vs
   schema consistency with ADR-045).
2. Rate-table lookup shape at ingest under batch load (cache invalidation
   when a rate row changes mid-batch; is per-batch resolution acceptable
   staleness?).
3. Margin "billed" side: rated usage revenue (apples-to-apples with usage
   cost) vs invoiced totals (includes base fees/credits) — phase 1 picks
   rated-usage; confirm the label copy can't be misread as GAAP margin.
4. Seed import scope: ship curated starter JSON vs fetch LiteLLM's live
   URL on operator click vs file upload only.
5. Where the uncosted-events count surfaces (report footnote vs attention
   system).
6. token_type key alignment: table rows keyed by ADR-044 canonical roles —
   what happens for non-token meters (per-request costs? phase 2?).
