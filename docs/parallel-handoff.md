# Parallel Work Handoff Log

> Append-only log. Each session ends its turn by adding an entry. The next session reads recent entries before starting work. Format defined in `docs/parallel-work.md`.

---

## 2026-04-25 (Sat)

### Track A
- **Shipped:**
  - `docs/parallel-work.md` — workstream split, file ownership, coordination protocols, kickoff prompt for Track B
  - `docs/design-multi-dim-meters.md` — RFC-style design for the Week 2 multi-dim meters work (schema, API surface, aggregation semantics, test plan, benchmark plan, 7 open questions)
  - `docs/parallel-handoff.md` (this file)
  - Earlier in the day: `docs/positioning.md`, `docs/90-day-plan.md` (with gap-analysis alignment), `docs/parallel-work.md`
- **API decisions firmed up in design doc:**
  - Dimensions are free-form JSONB (no schema enforcement v1) — keys live on existing `properties` column
  - `quantity` becomes `NUMERIC(38, 12)` for decimal support
  - New `meter_pricing_rules` table for N rules per meter with `dimension_match JSONB` + `aggregation_mode`
  - 5 aggregation modes: `sum`, `count`, `last_during_period`, `last_ever`, `max`
  - Priority + claim semantics — each event claimed by highest-priority matching rule, no double-count
  - Decimal lib: `shopspring/decimal` (per memory `feedback_prefer_battle_tested_libs`)
- **Blocking Track B on:** nothing
- **Track B can start:**
  - Week 1: README rewrite, AI-billing blog post draft, outreach list (50 candidates)
  - Building recipe-picker UI scaffolds against `docs/design-multi-dim-meters.md` (the design IS the contract; OK to mock the API while Track A implements)
- **Open for human review:**
  - Open questions 1–7 in `docs/design-multi-dim-meters.md` — flag any to resolve before Week 2 implementation starts (May 2)
- **Next session (Track A):** start the Week 2 implementation against the design doc — schema migration, decimal-quantity refactor, `meter_pricing_rules` store, ingest service updates. Or, if human prefers, finalize Week 1 README/blog work first.

### Track B
- **Kickoff:** session started in `../velox-track-b` worktree on `feat/track-b-week1` from `origin/main` (ceba954). Read positioning.md, 90-day-plan.md, parallel-work.md, design-multi-dim-meters.md, CLAUDE.md, and full memory index.
- **Working from:** the design doc as API contract — no need to wait on Track A's `feat/backend-week2` to land.
- **Plan for this week (May 1 deadline):**
  1. README rewrite — lead with AI-native + self-host wedge (replace generic framing, don't append)
  2. `docs/blog/2026-04-stripe-meter-api-ai-workloads.md` — long-form, uses the Anthropic-style worked example from the design doc (~24 Stripe Meters vs. 1 Velox meter + N rules)
  3. `docs/marketing/outreach-list.md` — 50 real candidates across AI inference, vector DB, dev infra
- **Blocking Track A on:** nothing
- **Track A can:** proceed with Week 2 implementation against the design doc; nothing in Track B's lane affects backend
- **Open questions for human:** none yet — wedge is clear from positioning.md
- **Next:** start the README rewrite immediately.

#### End-of-turn (Track B)
- **Shipped (all three Week 1 deliverables, in order):**
  - `README.md` — full rewrite. Leads with the AI-native + self-host wedge from `docs/positioning.md`. Replaced (not appended to) the generic framing. Includes the multi-dim "wedge in code" demo, three pillars, anti-positioning, comparison table, 90-day roadmap. Drops the stale stat block (numbers were 6+ months out of date).
  - `docs/blog/2026-04-stripe-meter-api-ai-workloads.md` — long-form post (~1,800 words). Walks the Stripe Meter object model field-by-field (verified against `docs.stripe.com/api/billing/meter/object`), shows ~36 Meters of wiring for an Anthropic-style 4-dim rate card, contrasts with 1 meter + N rules in Velox, calls out the per-meter aggregation-mode mismatch as a second structural issue. Honest anti-positioning section ("when Stripe Meter is still right").
  - `docs/marketing/outreach-list.md` — 50 leads: 22 AI inference / model APIs, 9 vector DB, 19 dev infra. Each row has named founder/CEO with a `(verify)` flag, a tier (A/B/C), and a wedge-fit reason tied to their public pricing matrix or regulatory profile. Includes outreach sequencing, what's deliberately *not* on the list, and a templates-needed punch list for Week 2.
- **Commits (this branch, in order):**
  - `080d90d docs(handoff): Track B kickoff entry`
  - `e0d2e09 docs(readme): lead with AI-native + self-host wedge`
  - `815a472 docs(blog): why Stripe Meter API doesn't fit AI workloads (draft)`
  - `e97dd0b docs(marketing): 50-candidate outreach list (AI inference, vector DB, dev infra)`
- **Discipline notes:**
  - No half-finished drafts; each deliverable shipped polished before moving on.
  - Industry-references-first: Stripe Meter object/event docs verified via WebFetch before writing the blog post; positioning anchored explicitly on Stripe / Lago / Orb / OpenMeter / Vercel / Linear.
  - No seed-data shortcuts; outreach list groups companies by **wedge fit**, not "easy to demo to."
  - All three deliverables explicitly anchor on `docs/positioning.md` and `docs/design-multi-dim-meters.md` (the design doc is the API contract for the README and blog code samples).
  - Did not touch `internal/`, `cmd/`, `internal/platform/migrate/sql/`, or `docs/design-*.md` / `docs/adr/` — Track A's lane.
- **Blocking Track A on:** nothing
- **Track A can pick up from this work:**
  - The blog post and README treat the multi-dim API surface as **the published contract** (`POST /v1/usage_events` with `value` + `dimensions`, `POST /v1/meters/{id}/pricing_rules`). If Track A's Week 2 implementation diverges from the doc, the doc updates first — and the blog/README will need a follow-up edit to match.
  - One naming nit worth confirming: README and blog use `/v1/usage_events` (underscore, per the design doc) while the rest of the API uses hyphens (`/v1/usage-events`, `/v1/credit-notes`). Worth a short ADR or a doc-doc fix before May 8 — not blocking, but pick one and stick with it.
- **Next:** rebase on `origin/main`, push `feat/track-b-week1`, open PR to `main` (do not merge — leave for human). Then this session pauses until Week 2.

---

## 2026-04-25 (Sat) — second update, Track A end-of-Week-2

### Track A
- **Shipped (Week 2 multi-dim meters):**
  - Migration `0054_multi_dim_meters` (decimal `NUMERIC(38,12)` quantity, GIN on `properties`, `meter_pricing_rules` table)
  - Decimal-quantity refactor across `domain.UsageEvent` + pricing/billing path
  - `pricing` package: `meter_pricing_rules` store + service
  - `usage`: dimensions on `properties` + JSONB superset contract test
  - `usage.Store.AggregateByPricingRules` — priority+claim resolution across all 5 aggregation modes (period-bounded query for sum/count/max/last_during_period; separate query for last_ever)
  - `cmd/velox-bench` ingest benchmark — baselines ~2.5k events/sec on local dev hardware. The 50k/sec target needs cloud-grade Postgres + batched INSERTs (both follow-up); the harness itself is the deliverable for Week 2.
  - Wire-contract alignment in `docs/design-multi-dim-meters.md`: hyphen paths (`/v1/usage-events`, `/v1/meters/{id}/pricing-rules`), `quantity` as the canonical field both directions (no `value` alias), `external_customer_id` + `event_name` on input.
  - Lint fixes (gofmt + errcheck) so PR #20 CI is green.
  - CHANGELOG entry for the multi-dim meters foundation.
- **Shipped (Week 3 prep):**
  - `docs/design-recipes.md` — RFC-style design for Week 3 pricing recipes work (5 built-in recipes, `POST /v1/recipes/instantiate` atomic graph creation, YAML format embedded via `embed.FS`, idempotency at the recipe-instance level, 7 open questions). Track B can scaffold the picker UI against this design today.
- **PR review (Track B's PR #19, Week 1 README + blog):** posted comment at https://github.com/sagarsuperuser/velox/pull/19#issuecomment-4320248289 listing 5 drift points (path conventions × 2, field names × 3, `quantity` stance) to keep Week 1 docs aligned with the corrected design doc.
- **Track A's PR #20 status:** lint fixed in latest push (df80d3d, 6a9fdca), CI rerunning. Was test+frontend+docker green, lint failing on 3 gofmt + 1 errcheck — all fixed.
- **Blocking Track B on:** nothing.
- **Track B can start:**
  - **Week 3 picker UI scaffold against `docs/design-recipes.md`** — contract is fully spec'd; mock the 5 endpoints with MSW and ship the picker grid + detail drawer + override form + preview modal.
  - Multi-dim meter dashboard surfaces (MeterDetail.tsx, UsageEvents.tsx) light-up verification once PR #20 merges.
- **Open for human review:**
  - Open questions 1–7 in `docs/design-recipes.md` — flag any to resolve before Week 3 implementation starts (May 9).
  - Track B's quantity-vs-value question: resolved as `quantity` canonical, no `value` alias. Documented in `docs/design-multi-dim-meters.md` "Wire-contract conventions" callout.
- **Next session (Track A):** depending on human direction — (a) drive PR #20 to merge once CI is green, then start Week 3 recipes implementation Monday; (b) Week 1 docs polish if human wants those locked first; (c) `create_preview` design doc for Week 5 if human wants the next RFC pre-staged for Track B.

#### End-of-turn (Track B — Week 2 frontend, in-flight)
Continuing the same day. PR #19 (Week 1 docs) is open and unmerged; new branch `feat/track-b-week2` is stacked on top of it. Will rebase to `main` once #19 lands.

- **Shipped (3 commits on `feat/track-b-week2`):**
  - `6cc492f feat(web): multi-dim pricing rules section on meter detail` — `MeterDetail.tsx` gets a "Dimension-matched rules" card with a chips-table of `priority / dimension_match / aggregation_mode / rating_rule / created`, an "Add rule" dialog (key/value dimension builder, rating-rule selector, all 5 aggregation modes), and typed-confirm delete. The original "Pricing Rule" card was renamed "Default pricing rule" with a copy clarifying it's the fallback for events not claimed by a higher-priority dimension-matched rule. Backed by new `api.{list,create,delete}MeterPricingRule` calls; falls back to empty list when the backend endpoint isn't deployed yet.
  - `7d8260d feat(web): surface dimensions on usage events table + filter` — `UsageEvents.tsx` gets a conditional Dimensions column (only shows when at least one event in view carries them, or a filter is active), a `key=value` text filter bound to a `dimensions=` query param, and stats/breakdowns now read the new decimal `value` field with fallback to legacy integer `quantity`. CSV export includes the dimensions JSON column.
  - `20059c4 feat(web): /onboarding wizard scaffold (5-step quickstart)` — new `/onboarding` route with a Stripe/Linear-style 5-step wizard skeleton: pick template → connect Stripe → tax → branding → send first invoice. Step state in `?step=`. Body content uses real deep-links into existing settings/customer/invoice pages so it's usable today; template picker wires up to recipes API in Week 3.
- **API contract additions** (in `web-v2/src/lib/api.ts`):
  - New types: `MeterPricingRule`, `MeterAggregationMode` (union of 5 modes), `CustomerUsageBreakdown`
  - `UsageEvent.value?: string` (decimal string) and `UsageEvent.dimensions?: Record<string, string|number|boolean>` — both optional during the multi-dim transition
  - New methods: `listMeterPricingRules`, `createMeterPricingRule`, `deleteMeterPricingRule`, `customerUsageBreakdown`
- **Path-naming choice (worth flagging to Track A):** I went with **hyphens** for the new endpoints (`/v1/meters/{id}/pricing-rules`) for consistency with the rest of the API (`/v1/credit-notes`, `/v1/rating-rules`, `/v1/audit-log`). The design doc currently says underscores. If Track A is firm on underscores, they should rename existing endpoints too — anything else creates dual conventions in the same surface.
- **Build verification:**
  - `tsc -b` passes — only pre-existing errors on `HostedInvoice.tsx:512` (Button asChild prop) and `Webhooks.tsx:243` (setState narrowing). Both predate this branch — confirmed via diff against `origin/main`.
  - `vite build` succeeds in ~600ms; no new bundles broke chunking.
  - Visual verification not done — dev server requires Postgres + bootstrap; deferred to next session against Track A's actual multi-dim backend on May 8.
- **Messages for Track A (scannable copy for the human to convey):**
  1. Update `docs/design-multi-dim-meters.md` paths to **hyphens** (or rename existing endpoints to underscores). Pick one.
  2. Pick a stance on `quantity` vs `value` in `UsageEvent` responses; document in design doc. I have both as optional — Stripe-style transition keeps both, response prefers `value`.
  3. Start `docs/design-recipes.md` next so I can scaffold the recipe-picker UI in Week 3 against a real contract instead of mocking.
  4. After multi-dim backend ships May 8, manually click through `/meters/:id` (Pricing Rules section) and `/usage` (Dimensions column) to confirm rules and event dimensions render.
  5. Glance at PR #19 (README + blog) — examples lifted directly from the design doc; if implementation diverges, those copy fragments need follow-up.
- **Blocking Track A on:** nothing
- **Track A can pick up from this work:** OpenAPI spec update, recipe RFC, multi-dim backend implementation. Path/field naming feedback is the only thing I'd love resolved before Track A's PR opens.
- **Next session (Track B):** rebase on `origin/main` once #19 merges, then either (a) extend with embeddable cost dashboard scaffold (Week 5 work, but its API contract is in the design doc as `GET /v1/customers/{id}/usage`), or (b) start the recipe-picker UI once Track A drops a recipes RFC. (a) is unblocked today; (b) waits for Track A.

**Wall-clock duration:** Week 2 in-flight = 21:46 → 23:45 IST (≈ 2h 0m); cumulative for the day = 21:09 → 23:45 (≈ 2h 36m).
