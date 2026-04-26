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

#### Track A → Track B handoff received (late evening 2026-04-25)

Track A resolved all five Track B asks in one pass. Summary of what was answered, with commit refs from `feat/backend-week2`:

1. **Path convention:** **hyphens** everywhere (`/v1/usage-events`, `/v1/meters/{id}/pricing-rules`, `/v1/customers/{id}/usage?event_name=...`). Design doc updated in `df80d3d`. New "Wire-contract conventions" callout block at the top of the API surface section.
2. **Quantity stance:** **`quantity` is canonical, both directions, JSON wire form is a string** for decimal precision. **No `value` alias.** Track B's `UsageEvent.value?: string` should be dropped.
3. **Ingest input field renames:** `meter_key` → `event_name`, `customer_id` → `external_customer_id` (matches Stripe Meter Events convention).
4. **Recipes RFC shipped:** `docs/design-recipes.md` (547 lines, commit `f0f0dcb`). 5 built-in recipes, atomic `POST /v1/recipes/instantiate`, embedded YAML, explicit Track B unblock section.
5. **PR #19 review** posted with 5 drift points (paths/field names/value-alias).

#### End-of-turn (Track B — Week 2 follow-up after Track A handoff)

- **Shipped (3 commits on `feat/track-b-week2` plus 1 on `feat/track-b-week1`):**
  - `cbecec3 docs: align README + blog to hyphen paths and quantity wire-contract` — on Week 1 branch. Fixed all 5 PR #19 drift points: `usage_events` → `usage-events`, `pricing_rules` → `pricing-rules`, `meter_key` → `event_name`, `customer_id` → `external_customer_id`, `value` → `quantity`. Stripe-side examples in the blog deliberately untouched (they describe Stripe's actual API). Pushed to PR #19; confirmation comment posted.
  - `f631029 refactor(web): drop value alias; quantity is canonical decimal string` — `UsageEvent.quantity: number → string`, dropped `UsageEvent.value`, renamed `CustomerUsageBreakdown.meter_key → event_name` and the corresponding query param. Display preserves trailing-zero precision by rendering the raw string; chart math coerces with `Number()` and notes that authoritative money math stays server-side.
  - `d38359f feat(web): /recipes picker + onboarding wizard wired to live recipes API` — new `/recipes` page (route added, lazy-loaded). Cards render the 5 built-in recipes with key/version/summary/creates-summary chips/installed badge. Configure dialog: overrides form rendered from `overridable_schema` (text/number/boolean), Preview button shows warnings + truncated object list, Install runs `POST /v1/recipes/instantiate` and navigates to the first created plan. Onboarding step 1 now fetches `/v1/recipes` live; cards deep-link to `/recipes`. Empty-state graceful when backend isn't deployed.
- **New API client surface:**
  - Types: `Recipe`, `RecipeDetail`, `RecipeOverrideSchema`, `RecipePreview`, `RecipeInstance`, `RecipeCreatesSummary`
  - Methods: `listRecipes`, `getRecipe`, `previewRecipe`, `instantiateRecipe`, `deleteRecipeInstance`
  - All paths hyphen-style; all field names match the RFC.
- **Build verification:**
  - `tsc -b` passes (only the two pre-existing errors on `HostedInvoice.tsx:512` and `Webhooks.tsx:243` remain — predate this branch)
  - `vite build` ~600ms; new lazy chunk for `/recipes`
  - Visual verification still deferred to next session (needs Postgres + Track A's PR #20 backend)
- **Branch state:**
  - `feat/track-b-week1` (PR #19): 5 commits, last is `cbecec3` (drift fix). Ready to merge.
  - `feat/track-b-week2`: 7 commits, stacked on `feat/track-b-week1`. Will rebase to `main` after #19 merges. No PR opened yet — Week 2 arc continues with cost-dashboard scaffold next session.
- **Blocking Track A on:** nothing. All earlier asks resolved.
- **Next session (Track B):** with PR #19 + Track A's PR #20 merged, the natural next pieces are (a) embeddable cost-dashboard scaffold (Week 5; API contract in `docs/design-multi-dim-meters.md` as `GET /v1/customers/{id}/usage`) and (b) sidebar nav links for `/recipes` + `/onboarding` once they're polished. Also: changelog discipline — when multi-dim and recipes both land on `main`, both `CHANGELOG.md` and `web-v2/src/pages/Changelog.tsx` need entries (Track A on the engineering log; me on the customer-facing rollup).

**Wall-clock duration (extended day):** initial Week 1 + Week 2 = 21:09 → 23:45 IST (≈ 2h 36m). Track A handoff + this round = 23:50 → 00:01 IST 2026-04-26 (≈ 11m active editing time, plus design-doc / RFC reading). Cumulative day total ≈ 2h 50m.

---

## 2026-04-26 (Sun) — post-PR-#20-merge

### Track B — rebase + live smoke test

Track A merged PR #20 (multi-dim backend, 12 commits) into `main` at 18:25:30Z. Cleanup pass on Track B's stacked branches plus first live API verification.

- **Rebased both branches onto new main:**
  - `feat/track-b-week1` rebased clean (handoff-log conflict resolved by keeping both Track A's "second update" and my Track B kickoff/end-of-turn — they describe the same date from two perspectives). Force-pushed to PR #19.
  - `feat/track-b-week2` rebased via `git rebase --onto feat/track-b-week1 cbecec3 feat/track-b-week2` (skip the 6 Week-1 commits, replay only the 7 Week-2 unique commits). Force-pushed.
- **Heads-up: Track B's "Week 3 picker UI scaffold" is already shipped.** Done in commit `0c719bd feat(web): /recipes picker + onboarding wizard wired to live recipes API` (and 6 sibling commits) on `feat/track-b-week2`. Per Track A's handoff message expecting Track B to "start" the picker — we got ahead of it Saturday night.
- **Live smoke test against the merged backend (binary built from `main` HEAD on port :8090, fresh tenant + secret key):**
  - `GET /v1/meters` ✓ — empty data, route works
  - `POST /v1/meters` ✓ — created `vlx_mtr_d7mgm93majdkeceoujpg`
  - `POST /v1/customers` ✓ — created `vlx_cus_...` with `external_id="acme"`
  - `POST /v1/usage-events` with `{external_customer_id, event_name, quantity:"1234.567890123456", dimensions:{model,operation,cached}}` ✓ — accepted; response carries `quantity:"1234.567890123456"` (string-encoded decimal, matches my TS type) and the JSONB column round-trips intact.
  - **Two real gaps surfaced:**
    1. **`/v1/meters/{id}/pricing-rules` returns 404 on the chi router.** The store + service for `meter_pricing_rules` shipped in PR #20, but the HTTP handler / route registration did not. My UI's "Add rule" dialog and rules list will show empty state until Track A adds the handler. Caught by my UI's try/catch fallback so the page stays usable, but the feature is non-functional. Suggested follow-up commit: register `r.Post/Get/Delete` for `pricing-rules` under the existing `/v1/meters/{id}` subtree, calling `pricing.Service.UpsertMeterPricingRule` etc.
    2. **Response-side returns `properties`, not `dimensions`.** Ingest accepts both (`dimensions` wins, per the apiEvent doc on `internal/usage/handler.go:74`), but the response keeps the legacy `properties` JSON field. My TS type expected `dimensions`. Patched in commit `af44730` — `UsageEvent.properties` added as optional alongside `dimensions`, new `eventDimensions()` helper reads whichever is present. Cleanest long-term fix: have Track A serialize as `dimensions` on responses too. Until then, my UI works against both.
- **Recipes endpoints (`/v1/recipes*`):** correctly 404 — recipes are Track A's Week 3 work, not in PR #20. My `/recipes` page falls back to empty-state cleanly.
- **Build:** `tsc -b` clean (only pre-existing errors); `vite build` ~600–740ms.
- **Branch state pushed to origin:**
  - `feat/track-b-week1`: 6 commits, ready to merge (PR #19 is up-to-date)
  - `feat/track-b-week2`: 8 commits (7 from before + `af44730` patch), stacked on Week 1
- **Messages for Track A (next-task copy):**
  1. **Add the HTTP handler + routes for meter pricing rules.** Service already exists. Routes I'm calling: `POST/GET /v1/meters/{id}/pricing-rules`, `DELETE /v1/meters/{id}/pricing-rules/{rule_id}`. The Track B UI is already wired against these.
  2. **Response-side wire alignment for `properties` vs `dimensions`.** Either rename to `dimensions` on the wire (cleanest) or leave as-is and document the alias in the wire-contract block. Track B side defensively reads both; Track A's choice.
  3. **`GET /v1/customers/{id}/usage` (the breakdown endpoint).** Same gap as #1 — needed for the Week 5 cost dashboard. If the route doesn't exist yet, please add when adding pricing-rules routes (same handler file).
  4. **Recipe RFC has 7 open questions** (per Track A's earlier handoff). When picking up Week 3 implementation, if any of those answers shift the API shape, ping Track B before changing the contract — my picker UI is already built against the doc-as-it-stands.
- **Blocking Track A on:** nothing. Above are next-task asks, not blockers.
- **Next session (Track B):**
  - PR #19 hasn't merged yet — once it does, retire the Week 1 branch and rebase Week 2 directly onto main as the new base.
  - Then either (a) embeddable cost-dashboard scaffold (Week 5, depends on `/v1/customers/{id}/usage` from Track A), or (b) sidebar nav entries + polish on `/recipes` + `/onboarding`. (b) is unblocked today.

**Wall-clock for this round:** 00:05 → 00:30 IST 2026-04-26 (≈ 25m).

---

## 2026-04-26 (Sun) — Track A end-of-Week-3, recipes shipped

### Track A
- **Shipped (Week 3 pricing recipes — full backend slice):**
  - Migration `0055_recipe_instances` — thin index of `(tenant_id, recipe_key)` instantiations + `created_objects` JSONB; UNIQUE for idempotency, RLS-enforced.
  - Domain types: `domain.Recipe`, `domain.RecipeInstance`, `domain.CreatedObjects`, plus the recipe-shape types (`RecipeMeter`, `RecipeRatingRule`, `RecipePricingRule`, `RecipePlan`, `RecipeDunningPolicy`, `RecipeWebhook`, `RecipeOverridable`).
  - `internal/recipe` package: `embed.FS` registry, YAML parser with enum validation (e.g. `dunning.policy.final_action` checked against `domain.DunningFinalAction`), `text/template` renderer with `Option("missingkey=error")`, in-memory `Registry` API.
  - Postgres `Store` + interface — `GetByKey`, `GetByID`, `List`, `CreateTx`, `GetByKeyTx`, `DeleteByIDTx`, `DeleteByKeyTx` (Tx variants for cross-domain composition).
  - `Service` — `ListRecipes` (registry × tenant install state), `GetRecipe`, `Preview` (no DB), `Instantiate` (atomic graph build under one tx), `Uninstall` (instance row only — operator owns downstream resources).
  - `*Tx` variants on cross-domain writers: `pricing.Service` (CreateRatingRuleTx, CreateMeterTx, CreatePlanTx, UpsertMeterPricingRuleTx), `dunning.Service` (UpsertPolicyTx), `webhook.Service` (CreateEndpointTx). `recipe.Service` defines its own narrow `PricingWriter`/`DunningWriter`/`WebhookWriter` interfaces and threads a single `*sql.Tx` through.
  - Five built-in recipes under `internal/recipe/recipes/`: `anthropic_style.yaml` (1 multi-dim meter, 9 rating rules, 9 pricing rules — cached input via priority=200 rule), `openai_style.yaml` (14 pricing rules across GPT-4 / GPT-4o / 4o-mini / 3.5-turbo + 3 embedding models), `replicate_style.yaml` (per-second GPU billing for a100/a40/t4/cpu, `last_during_period`), `b2b_saas_pro.yaml` (graduated seat tiers + storage GB add-on), `marketplace_gmv.yaml` (package-billing GMV take rate + per-transaction fee).
  - HTTP handlers + router wiring at `/v1/recipes` behind `PermPricingWrite`: `GET /`, `GET /{key}`, `POST /{key}/preview`, `POST /{key}/instantiate`, `GET /instances`, `DELETE /instances/{id}`. Registry loads once at server boot via `embed.FS`; load failure panics (TestLoad gates malformed YAMLs in CI before they ship).
  - Six integration tests against real Postgres (`service_integration_test.go`): full graph build (counts match the design doc — 1 meter / 9 rating rules / 9 pricing rules / 1 plan / 1 dunning policy / 1 webhook endpoint for `anthropic_style`); idempotency (second instantiate returns ErrAlreadyExists with existing instance ID, no new rows); atomicity rollback (synthetic mid-graph failure injected via a `failingPricingWriter` wrapper — zero rows survive in every cross-domain table); RLS isolation (tenant B can't see tenant A's instance via store or `ListRecipes`); preview/instantiate parity (same logical graph, modulo IDs); uninstall-keeps-resources (instance row removed, plans/meters/rules survive). Plus 12 unit tests covering Preview / GetRecipe / ListRecipes / force rejection / helper conversions.
  - CHANGELOG entries (both surfaces, per `feedback_changelog_discipline`): `CHANGELOG.md` Keep-a-Changelog Unreleased + Migrations 0055, plus `web-v2/src/pages/Changelog.tsx` Linear-style entry dated 2026-04-26 with 6 bullets.
- **Commits (this branch, in order):**
  - `4143eab feat(recipe): migration 0055 — recipe_instances index table`
  - `0d51e95 feat(domain): Recipe, RecipeInstance, CreatedObjects`
  - `9d57c3c feat(recipe): package skeleton — embed.FS, parser, template renderer`
  - `e672be1 feat(recipe): Store interface + Postgres impl`
  - `a707597 fix(recipe): drop Products — Velox has no separate Products table`
  - `92f62a8 feat(recipes): add *Tx variants on cross-domain Create methods`
  - `c63fc2f feat(recipe): Service — Preview, Instantiate, Uninstall`
  - `cb5d66f feat(recipe): 4 built-in recipes — openai_style, replicate_style, b2b_saas_pro, marketplace_gmv`
  - `cfd36bd feat(recipe): HTTP handlers + router wiring`
  - `5e7a0bd test(recipe): integration tests for Instantiate / Uninstall`
  - + (pending) CHANGELOG + handoff commit, then PR.
- **Decisions made inline (per `feedback_feat8_autonomy`):**
  - Force re-instantiation deferred to v2: API accepts the field today and returns `InvalidState` rather than silently dropping it — keeps the contract stable when force support lands. Operators uninstall first.
  - Uninstall removes the recipe-instance row only; downstream resources (plans, meters, dunning policy, webhook endpoint) stay. Reasoning: those resources may have live subscriptions and silent cascade would lose billing data.
  - Sample data + subscriptions deferred from v1 instantiate. The recipe defines a `sample_data` block but `Instantiate` doesn't materialise customers/subscriptions — the recipe's job is the pricing graph, not seed data (matches `feedback_no_seed_data_shortcut`).
  - Final-action enum validation lifted to the parser layer so future recipes fail at boot rather than at first instantiate. Caught a stale `final_action: void` in `anthropic_style.yaml` and corrected to `pause`.
  - Registry-load failure at server boot: panic rather than bubbling through `NewServer`'s signature — `TestLoad` gates malformed YAML in CI before it ships, so this path is unreachable in production.
- **Blocking Track B on:** nothing.
- **Track B can start:**
  - **Recipe picker UI** — the API surface is now real and behind `PermPricingWrite`. Routes:
    - `GET /v1/recipes` returns `[{...recipe, instantiated: {id, created_at, ...} | null}]` — picker grid.
    - `GET /v1/recipes/{key}` returns the full recipe definition for the detail drawer.
    - `POST /v1/recipes/{key}/preview` body `{overrides: {...}}` returns the rendered recipe (no DB writes) — call on every override-form keystroke.
    - `POST /v1/recipes/{key}/instantiate` body `{overrides: {...}, force?: false}` returns the `RecipeInstance` (created_objects has the IDs).
    - `GET /v1/recipes/instances` returns the tenant's installed instances; `DELETE /v1/recipes/instances/{id}` uninstalls.
    - All bodies are JSON; auth via existing API-key flow (PermPricingWrite required).
  - Multi-dim meter dashboard surfaces (MeterDetail / UsageEvents) once Week 2 PR lands — independent of recipes.
- **Open for human review:**
  - PR (this work, `feat/backend-week3-recipes`) — to be opened next.
  - The 7 open questions in `docs/design-recipes.md` — most settled by the implementation; worth a short doc pass to reflect the actual v1 decisions before closing the design as "shipped".
- **Next session (Track A):** open the PR for review, then either (a) drive it to merge + start Week 4 (`create_preview` design / billing-cycle hardening per 90-day plan), or (b) wait for Track B to ship the recipe picker so we can iterate on API ergonomics with real UI feedback.

---

## 2026-04-26 (Sun) — Track A response: recipes wire-shape fix + customer-usage RFC pre-stage

### Track A
- **Picked up after merges:** Track B reported three wire-shape drifts during fresh smoke testing of the merged recipes backend (PascalCase JSON keys, missing `creates` summary on list/detail, missing `objects` wrapper + `warnings` on preview). All three fixed in this slice — data shape only, no behavior change to `Instantiate` / `Uninstall`.
- **Shipped:**
  - **JSON tags on `domain.Recipe`** — top-level fields (`Key`, `Version`, `Name`, `Summary`, `Description`, `Overridable`, `Meters`, `RatingRules`, `PricingRules`, `Plans`, `DunningPolicy`, `Webhook`, `SampleData`) were missing tags entirely, so the encoder fell back to PascalCase. Added snake_case tags with `omitempty` on `description` / `dunning_policy` / `webhook`. `SampleData` is now `json:"-"` — it's a seed hint, not part of the public API surface.
  - **`creates` summary on list + detail.** New `RecipeCreates` struct (`{meters, rating_rules, pricing_rules, plans, dunning_policies, webhook_endpoints}` integer counts), `countCreates(domain.Recipe)` helper, `Creates` field added to `RecipeListItem`, and a new `RecipeDetail` wrapper for `GetRecipe` so detail also carries the summary. Picker UI renders "1 meter · 9 pricing rules · monthly billing" chips without a follow-up preview call.
  - **`PreviewResult` wrapper** — `Service.Preview` now returns `{key, version, objects: {meters, rating_rules, pricing_rules, plans, dunning_policies, webhook_endpoints}, warnings: []}` per the design spec. `dunning_policies` and `webhook_endpoints` are 0-or-1-length slices for uniform iteration. All object slices default to non-nil so the JSON encoder emits `[]` not `null`. `warnings` is empty in v1 — slot in place for the design's non-fatal-condition shape (currency-vs-Stripe mismatch, placeholder webhook URLs).
  - **Regression test** — new `TestWireShape_SnakeCase` (3 sub-tests) marshals real responses from `ListRecipes`, `GetRecipe`, and `Preview`, then asserts every required snake_case key is present and no PascalCase key leaks. Drift here trips CI before reaching the dashboard.
  - **`docs/design-customer-usage.md`** pre-staged (per Track B's standing ask). Same RFC structure as `design-recipes.md`: motivation grounded in cost-transparency wedge, goals + non-goals, today's-surface map (composition over invention — `usage.AggregateForBillingPeriod`, `customer.Store.Get`, `subscription.Store.List`, `pricing.ComputeAmountCents` all already exist), wire-contract example responses for default-cycle and explicit-window, internals sketch, integration test list (multi-dim parity, RLS, plan transitions, closed-cycle parity), 8 open questions with proposed answers, Track B unblock section with mockable contract + dashboard layout suggestions.
  - CHANGELOG entries on both surfaces (per `feedback_changelog_discipline`): `CHANGELOG.md` Unreleased > Fixed entry describing all three drifts + regression test, plus `web-v2/src/pages/Changelog.tsx` Linear-style fix entry dated 2026-04-26.
- **Decisions made inline (per `feedback_feat8_autonomy`):**
  - Kept the heavy meters/rating_rules/etc. arrays at top level on `GET /v1/recipes` rather than removing them. Track B's report flagged the missing `creates` and accepted "either re-add the creates summary or update the doc" — adding `creates` is the lower-risk path; removing the arrays would force Track B to re-architect the picker if it's already consuming them. Can revisit in v2 if the response weight becomes a real concern.
  - `omitempty` on `description` / `dunning_policy` / `webhook` (recipe-level) but **not** on `objects.meters` / `objects.dunning_policies` / etc. (preview-level). Reason: recipe-level optionals are sometimes-absent fields the client can reasonably skip; preview-level slices are always-present-but-possibly-empty arrays the picker iterates without null guards. Different semantics, different convention.
  - For multi-currency plans (rare today), the design-customer-usage RFC's `totals` becomes an array of `{currency, amount_cents}`. Single-currency cases keep the scalar shape. Documented in the open-question section so it surfaces in review rather than getting buried.
- **Blocking Track B on:** nothing. Picker UI types should match the spec now; smoke-test against the new shape and ping if anything else drifts.
- **Track B can start:**
  - Re-run the picker smoke test against the fixed contract; flag any further mismatches.
  - **Cost-dashboard scaffold (Week 5)** — `docs/design-customer-usage.md` is the contract to mock against. Same parallel-work pattern as recipes (MSW handlers seeded from the example response, then swap to real API at Track A integration time). Design's "Track B unblock" section has the dashboard layout suggestions.
- **Open for human review:**
  - PR for the recipes wire-shape fix (to be opened next).
  - `docs/design-customer-usage.md` open questions (8) — most have proposed answers; please flag any you want resolved before Week 5 implementation begins.

---

## 2026-04-26 (Sun) — Track A: customer-usage backend (Week 5)

### Track A
- **Picked up after the recipes wire-shape fix + customer-usage RFC pre-stage:** with Track B already mockable against `docs/design-customer-usage.md`, took the highest-leverage next slice — actually shipping the backend behind the contract so Track B can swap MSW for the real API on its next session.
- **Shipped (full Week 5 backend slice for `GET /v1/customers/{id}/usage`):**
  - **`internal/usage/customer_usage.go`** — new `CustomerUsageService` composing the existing `usage.Service` with three narrow per-domain interfaces (`CustomerLookup`, `SubscriptionLister`, `PricingReader`) defined inside the usage package. Composition over extension: existing `usage.Service` callers (ingest, billing, recipe) are not touched. Result types (`CustomerUsageResult`, `CustomerUsageSubscription`, `CustomerUsageMeter`, `CustomerUsageRule`, `CustomerUsageTotal`) carry snake_case JSON tags; nil slices are normalised to `[]` so the encoder doesn't emit `null`.
  - **Period resolution (`resolvePeriod`):** default → customer's active subscription cycle (`current_period_start`/`current_period_end_at`). Explicit `?from=&to=` (RFC 3339) overrides. Partial bounds (only one side) anchor on the cycle for the missing side. No-cycle + no-bounds returns `errs.Invalid("period", "explicit from/to required: customer has no active subscription").WithCode("customer_has_no_subscription")` so the dashboard knows to prompt for a window. Window cap: `MaxCustomerUsageWindow = 365 * 24 * time.Hour`.
  - **Per-meter aggregation:** delegates to `usage.AggregateByPricingRules` (the same path the cycle scan uses to bill — dashboard math IS invoice math, by construction). For each `RuleAggregation`, looks up the matching `MeterPricingRule` by `RuleID` and echoes the rule's canonical `dimension_match` map (not the observed event values — that's the bucket the cycle scan would price into). Unclaimed bucket (`RuleID == ""`) falls back to the meter's `RatingRuleVersionID` and emits `dimension_match: null`; warns if neither rule nor meter has a default rating rule.
  - **Multi-currency totals:** always-array `totals: [{currency, cents}]` shape (deviation from RFC's scalar-when-single — chose uniformity for cleaner client iteration; documented in CHANGELOG). Per-currency sum across all rules; rating-rule currency mismatch within a meter emits a warning.
  - **Multi-item subscriptions:** `buildSubscriptionSummaries` emits one entry per `(sub, item)` pair so multi-line subs surface every plan in the response.
  - **`internal/usage/customer_usage_handler.go`** — thin handler. `parseUsagePeriodQuery` reads `?from=&to=` and surfaces parse failures via `errs.Invalid("from"|"to", "must be RFC 3339 (e.g. 2026-04-01T00:00:00Z)")`. `tenantID` comes from `auth.TenantID(ctx)`; `customerID` from `chi.URLParam(r, "id")` after sibling-mount.
  - **`internal/api/router.go` wiring:** sibling-mount `r.Mount("/customers/{id}/usage", customerUsageH.CustomerUsageRoutes(auth.Require(auth.PermUsageRead)))` — same precedent as `/customers/{id}/coupon`. Cross-tenant lookups RLS-isolate naturally at the customer fetch (404, no leak through usage scan).
  - **9 unit tests (`customer_usage_test.go`) with fakes:** `TestResolvePeriod` (table-driven across default/explicit/partial/no-sub/window-cap), 5× `TestCustomerUsageService_Get_*` (single-rule path, multi-rule dimension echo, unclaimed fallback, currency-mismatch warning, customer-not-found pass-through), `TestMapMeterAggregation`, `TestCustomerUsageResult_EmptyArraysOnWire` (regression on `[]` vs `null`).
  - **4 integration tests (`customer_usage_integration_test.go`) against real Postgres:** `TestCustomerUsage_SingleMeterFlatParity` (100 events × qty=10 × 1¢ = 1000c; out-of-cycle event excluded), `TestCustomerUsage_MultiDimDimensionMatchEcho` (1000 input @3¢ + 100 output @5¢ = 3500c, both rules echoed), `TestCustomerUsage_CrossTenantIsolation` (tenant B's key vs tenant A's customer → 404), `TestCustomerUsage_NoSubscriptionRequiresExplicitWindow` (`customer_has_no_subscription` code first, then explicit window succeeds).
  - **CHANGELOG entries on both surfaces** (per `feedback_changelog_discipline`): `CHANGELOG.md` Unreleased > Added entry with full implementation summary + error codes + the always-array totals deviation note; `web-v2/src/pages/Changelog.tsx` Linear-style feature entry dated 2026-04-26 with 6 bullets.
- **Decisions made inline (per `feedback_feat8_autonomy`):**
  - **Always-array `totals` shape** (vs RFC's scalar-when-single): single-currency tenants pay one extra array index; multi-currency tenants get the same iteration shape. Cleaner client contract, no branch in the dashboard. Documented in CHANGELOG so the deviation is visible.
  - **`dimension_match` echoes the rule's canonical match expression**, not the observed event values. Reason: the rule is the pricing identity — what the cycle scan would charge against — and that's what the dashboard should show. Observed values would also fan out per-event-shape and lose the canonical bucketing.
  - **Per-`(sub, item)` flattening for multi-item subscriptions:** one row per plan keeps the response's mental model "every plan you're paying on" rather than "every subscription wrapper" — closer to what the customer-facing breakdown actually answers.
  - **Service composition over extending `usage.Service`:** new `CustomerUsageService` keeps the cross-domain wiring (customer + subscription + pricing) out of the existing single-domain service. Mirrors `recipe.Service`'s pattern of narrow per-domain writer interfaces.
- **Blocking Track B on:** nothing. Endpoint is live behind `PermUsageRead`; Track B can swap the MSW handler for the real API.
- **Track B can pick up:**
  - **Swap MSW → real API in the cost-dashboard scaffold.** Wire shape matches `docs/design-customer-usage.md` modulo the always-array `totals` shape (handle `totals[0]` as the single-currency case rather than `totals.cents`).
  - Once the dashboard is wired, ping if any field rendering is awkward — `dimension_match` echo and the warnings array are both new surfaces and may want UI polish iterations.
- **Open for human review:**
  - PR for the customer-usage backend (`feat/backend-week5-customer-usage`) — to be opened next.
  - The always-array `totals` deviation from the RFC — flag if you'd rather match the spec's scalar-when-single shape.
- **Next session (Track A):** open the PR, drive to merge, then either (a) start Week 5's other half (`create_preview` invoice preview design + implementation) per 90-day plan, or (b) Week 6 dunning hardening if human prefers; both unblocked.

---

## 2026-04-26 (Sun) — third update, Track B Week 5 prep

### Track B
- **Branch:** `feat/track-b-week5-prep` (off `main` post-PR-#24/#23 merge), pushed.
- **Re-smoke of recipes API (PR #25):** snake_case ✓, `creates` summary ✓, `{key, version, objects, warnings}` preview wrapper ✓, `instantiated` field present (null on fresh tenant) ✓. Two contract suggestions raised in PR review and addressed Track-B-side this slice (see commits below).
- **Shipped (2 commits):**
  - `34dfc83 refactor(web): adapt recipes TS to PR #25 wire shape` — collapsed `Recipe.overridable: string[]` + `RecipeDetail.overridable_schema: Record<key, schema>` into a single `RecipeOverrideSchema[]` with `key` embedded (matches PR #25's actual wire shape — strictly better than the original RFC). `OverrideField` reads `schema.key`; dropped the redundant `fieldKey` prop. Added `max_length` and `pattern` to the schema type since the live response carries them.
  - `c6bcafb feat(web): cost-dashboard scaffold against design-customer-usage RFC` — new `<CostDashboard customerId />` component, new `api.customerUsage(customerId, {from?, to?})` method, new `CustomerUsage*` types. Replaces the old quantity-only "Usage This Period" card on `CustomerDetail.tsx` with the multi-dim cost view (cycle progress, totals, per-meter expand-to-rules, warnings). Self-contained so the same component drops into a future public iframe-able route once token-based access lands.
- **Two flags for Track A on PR #25 + RFC:**
  1. **PR #25 / `docs/design-recipes.md` should reflect the new `overridable` shape.** Doc says `overridable: string[]` + separate `overridable_schema: dict`. Implementation collapses both into `overridable: [{key, type, default, enum?, max_length?, pattern?}, ...]` — better, single source of truth, my TS already matches. Doc update needed for downstream consumers.
  2. **`docs/design-customer-usage.md` `totals` field — request to make it always-array.** RFC currently spec'd `totals: {amount_cents, currency}` for single-currency and `totals: [...]` polymorphic for multi-currency. My TS models it as `totals: [{currency, amount_cents}]` unconditionally (single-currency tenants get a length-1 array). Removes the polymorphic discriminator on the client. If Track A keeps the polymorphic shape on the wire, I'll patch in a thin adapter — but the always-array shape is strictly cheaper for clients and costs nothing on the wire. **Asking for confirmation before Track A starts implementation.**
- **8 open questions on `docs/design-customer-usage.md`:** all of Track A's proposed answers I'd accept (no objections). Cosmetic note: example response shows `meter_id: "vlx_mtr_tokens"` (semantic) but real meter IDs are random hex; flagging so doc readers don't assume IDs encode meter keys.
- **Build verification:** `tsc -b` clean (only pre-existing errors); `vite build` ~565ms with new lazy chunk for the customer-usage component.
- **Blocking Track A on:** confirmation of the always-array `totals` shape, ideally before Week 5 implementation kicks off so I don't have to write an adapter post-hoc.
- **Track A can pick up from this work:** Week 5 implementation against the RFC (with `totals` shape decision), then `create_preview` for the projected-bill line on the dashboard (currently absent — UI math will extrapolate from elapsed-vs-total cycle once we have it).
- **Next session (Track B):**
  - Once `GET /v1/customers/{id}/usage` is live, click-test the dashboard end-to-end against a real `anthropic_style` tenant.
  - Add a "projected bill" line to the dashboard when `create_preview` ships (Week 5 explicit deliverable).
  - The standalone iframe-able route (`/cost-dashboard?customer_id=…&token=…`) lands once Track A wires public-token access.

**Wall-clock:** 00:30 → 01:25 IST 2026-04-26 (≈ 55m).

---

## 2026-04-26 (Sun) — Track A: create_preview backend (Week 5b)

### Track A
- **Branch:** `feat/backend-week5b-create-preview` (off `main` post-customer-usage merge).
- **Goal:** close the Stripe Tier 1 parity gap (`POST /v1/invoices/create_preview`, formerly `Invoice.upcoming`) and route the existing in-app debug preview path through the same multi-dim-aware aggregator (`usage.AggregateByPricingRules`) so preview math == invoice math by construction across every meter shape.
- **Shipped:**
  - **`docs/design-create-preview.md`** — RFC with wire shape, period resolution, subscription resolution rules, error code matrix, no-writes guarantee, route mounting precedence (more-specific before catch-all under chi), 8-item implementation checklist.
  - **`internal/billing/preview.go` refactor** — preview now walks every active subscription's pricing rules (one `usage.AggregateByPricingRules` call per meter, then `domain.ComputeAmountCents` per rule). New per-line shape echoes the rule's canonical `dimension_match`, quantity is decimal `string` on the wire (shopspring `MarshalJSON`), totals are always-array `[{currency, cents}]` (single-currency tenants get a length-1 array — same precedent as customer-usage so TS clients iterate uniformly).
  - **`internal/billing/preview_create.go`** — `PreviewService` composing the engine with two narrow per-domain interfaces (`CustomerLookup`, `SubscriptionLister`). `CreatePreview()`: customer existence → subscription resolution (explicit ID with cross-customer rejection vs implicit primary pick: active/trialing, latest cycle start) → period resolution (default → sub's cycle, explicit RFC 3339 bounds override, partial bounds rejected) → `engine.previewWithWindow`. Coded `customer_has_no_subscription` error so the dashboard can prompt for an explicit window.
  - **`internal/billing/create_preview_handler.go`** — `CreatePreviewHandler` with `Routes()` returning chi router; `decodeCreatePreviewRequest` accepts empty body (defaults to primary sub + cycle), rejects malformed JSON; period parsed via `parseWirePeriod`. Error mapping via `respond.FromError`; `ErrNotFound` surfaces with "customer or subscription" label.
  - **`internal/api/router.go` wiring** — `createPreviewH := billing.NewCreatePreviewHandler(billing.NewPreviewService(engine, customerStore, subStore))`. Mount precedence: `r.With(auth.Require(auth.PermInvoiceRead)).Mount("/invoices/create_preview", createPreviewH.Routes())` BEFORE `Mount("/invoices", invoiceH.Routes())` — chi catches "create_preview" as `{id}` otherwise.
  - **`internal/billing/preview_wire_shape_test.go`** — `TestWireShape_SnakeCase` is the merge gate. "FullyPopulated" subtest asserts all 9 top-level snake_case keys, no PascalCase leaks, lines/totals/warnings as arrays, quantity as JSON string `"1234567.891234"` (chose meaningful digits — shopspring trims trailing zeros, so test value must survive normalization), `dimension_match` as object. "EmptyResultSlicesAreArrays" subtest asserts empty slices marshal as `[]` not `null`.
  - **16 unit tests (`preview_create_test.go`):** `TestResolveCreatePreviewPeriod` (6 cases), `TestPickPrimarySubscription` (6 cases), `TestPreviewService_ResolveSubscription` (5 cases), `TestCreatePreview_BlankCustomerID`, `TestCreatePreview_CustomerNotFound`, `TestDecodeCreatePreviewRequest` (7 cases), `TestComputePreviewTotals` (4 cases — single-currency, multi-currency split, empty totals, mixed-zero totals).
  - **7 integration tests (`preview_integration_test.go`) against real Postgres:** `TestCreatePreview_SingleMeterFlatParity` (100 events × qty=10 × 1¢ = 1000c), `TestCreatePreview_MultiDimDimensionMatchEcho` (1000 input @3¢ + 100 output @5¢ = 3500c, both rules echoed), `TestCreatePreview_NoWrites` (count `invoices` + `invoice_lines` rows before/after — zero diff guarantee), `TestCreatePreview_CrossTenantIsolation` (tenant B's key vs tenant A's customer → 404 via RLS), `TestCreatePreview_CustomerHasNoSubscription` (coded error returned), `TestCreatePreview_ExplicitSubscriptionWrongCustomer` (cross-customer subscription ID rejected with 404). All 7 pass in 3.88s.
  - **TS consumer updates (`web-v2/src/lib/api.ts`)** — replaced old `InvoicePreview` interface with new shape: dropped `subtotal_cents` + top-level `currency`; added `totals[]`, `warnings[]`; broke out `InvoicePreviewLine` and `InvoicePreviewTotal` types; `quantity` is now `string`. Added `createInvoicePreview` API method calling `POST /invoices/create_preview`. **`web-v2/src/pages/SubscriptionDetail.tsx`** updated to use `preview.totals[]` per-row and `Number(line.quantity).toLocaleString()` for the decimal-string field.
  - **CHANGELOG.md** comprehensive entry at top of [Unreleased]/Added (Stripe Tier 1 parity, multi-dim parity guarantee, no-writes property, error codes, route mounting order, test coverage, in-app debug route shape change with TS consumer updates).
  - **`web-v2/src/pages/Changelog.tsx`** Linear-style feature entry dated 2026-04-26 with 6 bullets (no-writes, period resolution, subscription resolution, always-array totals, route ordering, test-coverage summary).
- **Decisions made inline (per `feedback_feat8_autonomy`):**
  - **Compose `PreviewService` against `Engine` (not reimplement per-meter walk):** the create_preview surface and the existing `/v1/billing/preview/{id}` debug route share one code path. Preview math == invoice math by construction. Three narrow interfaces (`CustomerLookup`, `SubscriptionLister`, plus existing engine seams) keep `PreviewService` cross-domain-clean.
  - **Mount `/invoices/create_preview` before `/invoices`:** chi-router pattern ordering — without this, the `/invoices/{id}` catch-all captures "create_preview" as an invoice ID and 404s. Tested explicitly in handler test.
  - **Update existing TS `InvoicePreview` rather than introduce a separate type:** both `/v1/billing/preview/{id}` (debug) and `/v1/invoices/create_preview` (Tier 1) now return the same shape. One type, one rendering path on the dashboard.
  - **Test fixture quantity `1234567.891234` (not `1000000.000000000000`):** discovered shopspring `decimal.MarshalJSON` trims trailing zeros, so the assertion needs digits that survive normalization. Documented in test comment.
- **Test status:**
  - `go test ./internal/billing/... -count=1`: pass (16 unit + handler tests).
  - `go test -p 1 ./internal/billing/... -short=false -count=1`: pass (7 integration tests in 3.88s).
  - Full short-mode suite: pass.
- **Blocking Track B on:** nothing.
- **Track B can pick up:**
  - **"Projected bill" line on the cost dashboard** — Week 5 explicit deliverable, now unblocked. Call `api.createInvoicePreview({customer_id})` and render `preview.totals[0].cents` next to the cycle-progress meter; multi-currency tenants get the length-N array.
  - **Subscription detail preview already wired** — uses the new shape via `SubscriptionDetail.tsx` updates above. Click-test against real tenant after merge.
- **Open for human review:**
  - PR to be opened on `feat/backend-week5b-create-preview` once final commit lands.
- **Next session (Track A):** drive PR to green, self-merge per authorization (CI green AND `TestWireShape_SnakeCase` in suite — both conditions met), then start Week 5/6 next blocker per 90-day plan.
