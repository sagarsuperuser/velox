# Changelog

All notable changes to Velox are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

**Pre-1.0 versioning:** we're on `0.MINOR.PATCH`. MINOR bumps for new
features, PATCH for fixes. The public API is stabilising but not yet
frozen; breaking changes land on MINOR until `1.0.0`.

Two surfaces mirror this file:

- `web-v2/src/pages/Changelog.tsx` — customer-facing public changelog,
  curated rollups (not every bug fix).
- `docs/design-partner-readiness-plan.md` — internal planning log, not a
  release log.

## [Unreleased]

### Added

- **Customer usage endpoint — one call answers "what did this customer use?"** (2026-04-26) —
  the second flagship developer-experience surface, alongside recipes (per
  `docs/design-customer-usage.md`, `docs/positioning.md` pillar 1.4). `GET
  /v1/customers/{id}/usage` collapses "show me what this customer used and
  owes" into one read. With no params it returns the customer's current
  billing cycle by default; `?from=&to=` (RFC 3339, both required if either
  is supplied, ≤ 1 year) overrides for historical or audit windows.
  Response includes per-meter aggregates (`total_quantity` as a precise
  decimal string, `total_amount_cents` as integer cents), per-rule
  breakdowns for multi-dim meters with `dimension_match` echoed from the
  meter pricing rule (the canonical pricing identity, not the events
  seen), per-currency `totals[]`, and a `subscriptions[]` block carrying
  plan name + cycle bounds so a dashboard renders "Plan: AI API Pro ·
  cycle Apr 1 → May 1" without follow-up calls. The endpoint is
  composition, not new aggregation logic — the priority+claim LATERAL
  JOIN already shipped in Week 2 lives in
  `usage.AggregateByPricingRules`; this surface walks the customer's
  subscribed plans → meters, calls the same store path the cycle scan
  uses, then prices each rule bucket through
  `domain.ComputeAmountCents`. Same code → dashboard math == invoice
  math. RLS-by-construction: cross-tenant customer IDs surface as 404
  via the standard `BeginTx(TxTenant, …)` lookup. Errors:
  `customer_has_no_subscription` (400, coded) when the caller passes no
  period and the customer has zero active subs; `400` for partial
  bounds, `from >= to`, or window > 1 year. Mounted at
  `/v1/customers/{id}/usage` as a sibling of `/customers` (matches the
  `/customers/{id}/coupon` precedent) behind `PermUsageRead` so a
  read-only secret key powers the dashboard without inheriting customer
  write capability. Snake-case JSON keys, struct-tag enforced; empty
  list fields emit `[]` not `null` so clients iterate without null
  guards. Composition uses three narrow interfaces local to the usage
  package (`CustomerLookup`, `SubscriptionLister`, `PricingReader`) so
  the customer-usage code owns no cross-domain state. Tests: nine unit
  cases (period resolution table-driven, single-meter flat parity,
  multi-currency totals roll-up, multi-dim dimension-match echo, flat
  meter omits `dimension_match`, customer-not-found propagation,
  blank-customer validation, aggregation-mode mapping, wire-shape
  empty-arrays regression) plus four integration cases against real
  Postgres (single-meter parity with in-cycle vs. outside-cycle event
  filtering, multi-dim parity matching `usage.AggregateByPricingRules`
  exactly, cross-tenant 404 via RLS, no-sub + explicit window). Track B
  can swap their cost-dashboard scaffold from MSW mocks to the live API
  at integration time. **Note on totals shape:** `totals` always emits
  an array even when there's only one currency (one entry per distinct
  currency), instead of the design RFC's "scalar when single currency"
  form — one consistent shape lets dashboards iterate without branching
  on cardinality.

- **Recipes picker UI — discoverable pricing installation** (2026-04-26) —
  the dashboard surface for the recipes feature (Track A backend
  entry below). New `/recipes` page (`web-v2/src/pages/Recipes.tsx`)
  lists the five built-ins as cards with a creates-summary chip
  strip (`{meters, rating_rules, pricing_rules, plans,
  dunning_policies, webhook_endpoints}`); each card opens a
  configure dialog that renders the overrides form from
  `recipe.overridable[]` (string / number / boolean fields with
  `enum`, `max_length`, and `pattern` honored). Preview button
  posts `/v1/recipes/{key}/preview` and renders the would-be graph
  inline (truncated past 5 items per object class) plus any
  warnings; Install posts `/v1/recipes/instantiate` and navigates
  to the first `created_objects.plan_ids` so the new catalog is
  one click away. Sidebar entry added under Configuration
  (`Sparkles` icon) and the onboarding wizard's step 1
  ("Pick a pricing template") now fetches `/v1/recipes` live and
  deep-links into the picker. New TS types in
  `web-v2/src/lib/api.ts`: `Recipe`, `RecipeDetail`,
  `RecipeOverrideSchema` (`{key, type, default?, enum?, max_length?,
  pattern?}` — collapsed from the design's `string[]` +
  `Record<key, schema>` split into a single self-describing array,
  matches PR #25's actual wire shape), `RecipePreview`,
  `RecipeInstance`, `RecipeCreatesSummary`. Falls back to an empty
  list when the backend endpoint isn't reachable so the page stays
  usable on pre-recipes builds.

- **Multi-dimensional meter dashboard surfaces** (2026-04-26) — the
  operator-side complement to the Week 2 multi-dim meters engine
  (Track A entry below). `web-v2/src/pages/MeterDetail.tsx` gets a
  "Dimension-matched rules" card: chips-table over each rule's
  `dimension_match` keys, the aggregation mode (one of `sum`,
  `count`, `last_during_period`, `last_ever`, `max`), priority,
  and rating-rule reference. "Add rule" dialog walks the operator
  through a dynamic key/value dimension builder + select for the
  five aggregation modes + rating-rule selector + priority input;
  delete is gated through the `TypedConfirmDialog` (type
  `delete` to confirm — same pattern as void-invoice). The
  default-rule card was renamed "Default pricing rule" with a
  fallback-explainer subtitle so operators understand the
  priority+claim relationship. `web-v2/src/pages/UsageEvents.tsx`
  gets a conditional `Dimensions` column (only shown when at least
  one event in view carries them, or a filter is active), a
  `key=value` text filter that flows through to the
  `dimensions=` query param, and a CSV export that now carries the
  dimensions JSON column. The decimal-precision `quantity` field
  is now read as a string-encoded NUMERIC end-to-end —
  `eventQuantity()` coerces to `number` only at chart math; raw
  display preserves trailing-zero precision (`1234.567890123456`).
  TS types added: `MeterPricingRule`, `MeterAggregationMode` union,
  plus `api.{listMeterPricingRules, createMeterPricingRule,
  deleteMeterPricingRule}` client methods (all hyphen paths,
  matching the rest of `/v1/*`).

- **Pricing recipes — one-call billing setup** (2026-04-26) — the
  developer-experience flagship that turns Week 2's multi-dim meter
  engine into a 30-second quickstart (per `docs/design-recipes.md`,
  `docs/positioning.md` pillar 1.3). `POST
  /v1/recipes/{key}/instantiate` atomically builds the full graph
  (rating rules → meters → multi-dim pricing rules → plan → optional
  dunning policy → optional webhook endpoint → instance row) under a
  single tenant-scoped transaction; partial state never reaches the
  tenant. Five built-ins ship in v1 — `anthropic_style` (4 Claude
  models × input/output, cached input via priority=200 rule),
  `openai_style` (14 rules across GPT-4 / GPT-4o / 3.5-turbo /
  embeddings), `replicate_style` (per-second GPU billing across
  a100/a40/t4/cpu), `b2b_saas_pro` (seats-with-included-tier +
  storage), `marketplace_gmv` (package-billing GMV take rate +
  per-transaction fee). Recipes live as embedded YAML under
  `internal/recipe/recipes/*.yaml` (`embed.FS`, loaded at boot — no
  DB recipe table); per-instantiation overrides (`currency`,
  `plan_name`, `plan_code`, plus recipe-specific knobs like
  `included_seats`) flow through `text/template` with
  `Option("missingkey=error")` so a typo fails preview rather than
  silently drops. `POST /{key}/preview` renders the would-be graph
  with zero DB writes — cheap enough to call on every override-form
  keystroke and powering the dashboard's review dialog. Idempotency
  is enforced two ways: `(tenant_id, recipe_key)` UNIQUE in postgres,
  plus a Service-layer pre-check inside the same tx that returns
  `ErrAlreadyExists` with the existing instance ID surfaced through
  the WithCode error. Force re-instantiation is reserved for v2 — v1
  accepts the `force` field and returns `InvalidState` with a clear
  message, keeping the API contract stable when force support lands.
  Uninstall removes the recipe_instance row only; the resources the
  recipe created (plans, meters, dunning policy, webhook endpoint)
  stay — operators own them once they exist, and silent cascade
  could lose live billing data. Cross-domain transactional
  composition is built on `*Tx` writers added to
  `pricing.Service` (CreateRatingRuleTx, CreateMeterTx, CreatePlanTx,
  UpsertMeterPricingRuleTx), `dunning.Service` (UpsertPolicyTx), and
  `webhook.Service` (CreateEndpointTx); `recipe.Service` defines
  narrow `PricingWriter` / `DunningWriter` / `WebhookWriter`
  interfaces it threads the same `*sql.Tx` through. Six integration
  tests verify the contract end-to-end against real Postgres: full
  graph build (counts match the design doc — 1 meter / 9 rating
  rules / 9 pricing rules / 1 plan / 1 dunning policy / 1 webhook
  endpoint for `anthropic_style`), idempotency, mid-graph rollback
  (synthetic failure injected via a `failingPricingWriter` wrapper —
  zero rows survive in every cross-domain table), RLS isolation
  (tenant B never sees tenant A's instance), preview/instantiate
  parity, and uninstall-keeps-resources. Mounted at `/v1/recipes`
  behind `PermPricingWrite`. Dashboard surface is Track B's next
  slice.

- **Multi-dimensional meters foundation — AI-native wedge** (2026-04-25) —
  the runtime engine for Velox's positioning bet (per
  `docs/positioning.md`): one meter receives events with arbitrary
  dimensions, many pricing rules pick out subsets at different rates.
  Migration `0054_multi_dim_meters` widens `usage_events.quantity` to
  `NUMERIC(38,12)` (Stripe `quantity_decimal` parity), adds a GIN index
  over `properties` for JSONB-superset dispatch, and introduces
  `meter_pricing_rules (dimension_match, aggregation_mode, priority)` —
  N rules per meter, claim-based, no double-count. Per-rule
  `aggregation_mode` adds `count`, `last_during_period`, `last_ever`,
  `max` to the existing `sum` (Stripe Tier 1 gap closed). Ingest
  accepts a `dimensions` field (alias for `properties`) capped at 16
  scalar keys, matching the rule-side `dimension_match` cap. The
  priority+claim resolution query lives in
  `usage.Store.AggregateByPricingRules`: rules walked in `priority
  DESC, created_at ASC` order, each in-period event claimed by its
  top-priority match via `LATERAL JOIN`, per-mode aggregation
  dispatched in SQL via a `CASE` over the per-group constant mode;
  `last_ever` runs a separate query that ignores period bounds for
  "current state" billing (e.g. seat counts). Unclaimed events fall
  through to the meter's default `rating_rule_version_id` with
  `meters.aggregation` — backward-compatible for tenants without rules.
  Local benchmark harness (`cmd/velox-bench`) baselines ~2.5k
  events/sec on dev hardware; the design doc's 50k/sec target requires
  cloud-grade Postgres + batched INSERTs, both follow-up work. HTTP
  endpoints for `meter_pricing_rules` CRUD will land in a follow-up PR
  (engine first, surface second). See
  `docs/design-multi-dim-meters.md` for the full algorithm.

- **Trial extension — Stripe parity** (2026-04-25) — operators can now
  push a trialing subscription's `trial_end_at` later via `POST
  /v1/subscriptions/{id}/extend-trial` with `{trial_end:<RFC3339>}`. The
  store atomic enforces `status='trialing'` (closing the race against
  the cycle-scan auto-flip), and the service guards against past
  timestamps and shrinks (use `end-trial` to shorten — `extend-trial`
  is extension-only by design). Fires `subscription.trial_extended`
  with `triggered_by:"operator"`. Dashboard `SubscriptionDetail`
  surfaces an "Extend trial" button next to "End trial now" on
  trialing subs; the dialog seeds with current + 7 days.

- **Trial state machine — Stripe parity** (2026-04-25) — subscriptions
  with `trial_days > 0` now enter a real `status='trialing'` state on
  `Create` (previously they went straight to `active` and the engine
  inferred trial-skip from `trial_end_at` alone). New status added to the
  `subscriptions.status` CHECK constraint and to `domain.SubscriptionStatus`.
  Billing engine runs a three-branch state machine on each cycle visit:
  (a) trialing AND `now < trial_end_at` → skip billing, advance the cycle;
  (b) trialing AND `now >= trial_end_at` → atomically flip to `active`,
  stamp `activated_at`, fire `subscription.trial_ended`
  (`triggered_by:"schedule"`), then bill normally; (c) any other status →
  bill normally. `GetDueBilling` now sweeps `IN ('active', 'trialing')` so
  the cycle scan actually visits trialing subs. New endpoint `POST
  /v1/subscriptions/{id}/end-trial` lets sales/ops end a trial early; it
  fires `subscription.trial_ended` with `triggered_by:"operator"` so
  analytics can split scheduled trial-ends from manual ones. Dashboard
  `SubscriptionDetail` shows an "End trial now" button on trialing subs.
  The atomic `'trialing' → 'active'` UPDATE-WHERE-status closes the race
  between scheduler auto-flip and operator early-end.

- **Pause collection — Stripe parity** (2026-04-25) — distinct from a hard
  pause: the cycle keeps running, but invoices generate as drafts and skip
  finalize/charge/dunning until resumed. New nullable composite
  `pause_collection = {behavior, resumes_at}` on `subscriptions`. Two new
  endpoints: `PUT /v1/subscriptions/{id}/pause-collection` accepts
  `{behavior:"keep_as_draft", resumes_at?:<RFC3339>}`; `DELETE
  /v1/subscriptions/{id}/pause-collection` clears the pause. v1 only
  supports `keep_as_draft` (the `mark_uncollectible` and `void` Stripe
  modes need an `uncollectible` invoice status that doesn't exist yet).
  Billing engine forces invoice status to draft, skips
  `credits.ApplyToInvoice` and auto-charge, and auto-resumes when
  `resumes_at` passes (cycle scan checks at cycle time, fires
  `subscription.collection_resumed` with `triggered_by:"schedule"` so
  analytics can distinguish from operator-triggered resume). Webhook
  events: `subscription.collection_paused` /
  `subscription.collection_resumed`. Dashboard `SubscriptionDetail` gets a
  blue "Collection paused" banner with one-click Resume and a Stripe-style
  choice dialog ("Pause subscription" hard freeze vs "Pause collection
  only") on the Pause action.

- **Scheduled subscription cancellation — Stripe parity** (2026-04-25) —
  `cancel_at_period_end` (soft, reversible) and `cancel_at` (timestamp
  schedule) on `subscriptions`. Two new endpoints: `POST /v1/subscriptions/
  {id}/schedule-cancel` accepts `{at_period_end:true}` xor
  `{cancel_at:<RFC3339>}`; `DELETE /v1/subscriptions/{id}/scheduled-cancel`
  clears any prior schedule. Billing engine fires the cancel atomically at
  the period boundary after the final invoice generates, mirrors test-clock
  time for `canceled_at`, and emits `subscription.cancel_scheduled` /
  `subscription.cancel_cleared` / `subscription.canceled` (with
  `triggered_by:"schedule"`). v1 only accepts `cancel_at` >= current period
  end; mid-period proration is a follow-up. Dashboard `SubscriptionDetail`
  gets a "Cancellation scheduled" banner with one-click Undo and a Stripe-
  style choice dialog ("at period end" vs "immediately") on the Cancel
  action.

- **Phase 2 Addendum shipped — pre-invite design-partner readiness** (2026-04-24)
  - **Hosted invoice page** (T0-17) — Stripe-equivalent `hosted_invoice_url`.
    `invoices.public_token` + three public routes at `/v1/public/invoices/*`
    (view, checkout via Stripe Checkout, PDF). Mobile-first React page at
    `/invoice/:token` with tenant branding. Operator rotate-public-token
    endpoint. Dashboard "Copy Link" + "Rotate" actions on invoice detail.
  - **Branded HTML emails** (T0-16) — 6 customer-facing emails converted
    to multipart/alternative with tenant logo, brand color, support link,
    CTA to hosted invoice page. Operator emails (password reset, member
    invite, portal magic link) stay plain text.
  - **Webhook secret rotation grace period** (T0-19) — 72h dual-signing
    window via Stripe-style multi-v1 `Velox-Signature` header. Dashboard
    shows "Dual-signing until {time}" during the window.
  - **Subscription activity timeline** (T0-18) — `GET /v1/subscriptions/
    {id}/timeline` + SPA Activity panel. CS reps get a chronological feed
    of lifecycle events (create, activate, pause, resume, cancel, item
    changes) sourced from the audit log.
  - **SMTP bounce capture** (T0-20, **pipeline only**) — schema,
    webhook event, and UI badge ready for bounce signal; synchronous
    SMTP 5xx detection catches a minority of real-world bounces because
    most MX providers emit bounces as async NDRs, not synchronous `RCPT
    TO` rejections. Full coverage ships with T1-8 SES / SendGrid /
    Postmark webhook handlers, which plug into the same
    `customer.MarkEmailBounced` seam. Deployments without
    `VELOX_EMAIL_BIDX_KEY` get graceful degradation — bounces are
    logged but `email_status` stays `unknown`.

### Changed

- **Manual test runbook** updated for the Phase 2 Addendum surfaces.
  Existing flows I6 (emails), W2 (webhook rotation), CU6 (brand color)
  expanded with branded-HTML / grace-period / email-brand checks. New
  flows: I10 hosted invoice page (token mint + public render + Stripe
  Checkout + state-gated variants + security audit), B12 subscription
  activity timeline, CU7 email bounce capture + badge.
- **API shape:** 5 customer-facing email interfaces (`invoice.EmailSender`,
  `dunning.EmailNotifier`, `payment.EmailReceipt`, `email.EmailDeliverer`)
  gained a trailing `publicToken` parameter. All callers updated
  atomically — breaking change for any out-of-tree email implementations.
- **Webhook Store interface:** `UpdateEndpointSecret` replaced by
  `RotateEndpointSecret(tenantID, id, newSecret, gracePeriod)`.
  Hard-replace semantics preserved via `gracePeriod=0`.
- **Customer JSON** now surfaces `email_status`, `email_last_bounced_at`,
  `email_bounce_reason` when populated.
- **Webhook endpoint JSON** now surfaces `secondary_secret_last4` and
  `secondary_secret_expires_at` during a rotation's grace window.

### Fixed

- **Recipes API wire shape — snake_case + creates summary + preview wrapper**
  (2026-04-26) — three drifts between `docs/design-recipes.md` and the
  Week 3 implementation surfaced during Track B's first integration
  pass and are now fixed. `domain.Recipe` was missing `json:"…"` tags
  on its top-level fields, so `GET /v1/recipes`, `GET
  /v1/recipes/{key}`, and `POST /v1/recipes/{key}/preview` were
  emitting PascalCase keys (`Key`, `Version`, `Meters`,
  `RatingRules`, `DunningPolicy`, …) inconsistent with the rest of
  `/v1/*`; tags added with `omitempty` on `description` /
  `dunning_policy` / `webhook` to keep wire output tight when
  recipes don't declare those sections. `SampleData` is now `json:"-"`
  — it's a seed hint for `seed_sample_data=true`, not part of the
  public API surface. `RecipeListItem` and the new
  `RecipeDetail` wrapper carry a `creates: {meters, rating_rules,
  pricing_rules, plans, dunning_policies, webhook_endpoints}` count
  object so the picker UI renders summary chips ("1 meter · 9
  pricing rules · monthly billing") without a follow-up preview
  call. `Service.Preview` now returns
  `PreviewResult{key, version, objects: {…}, warnings: []}` per the
  design spec — previously it inlined every object array at the top
  level. `objects.dunning_policies` and `objects.webhook_endpoints`
  are 0-or-1-length slices for uniform iteration, all object slices
  default to non-nil so JSON emits `[]` not `null`, and `warnings`
  is the same shape recipes.preview spec'd for non-fatal conditions
  (currency-vs-Stripe-account mismatch, placeholder webhook URLs) —
  empty array in v1, slot in place. New `TestWireShape_SnakeCase`
  unit test pins all three contracts so future regressions trip CI
  before reaching the dashboard. No behavior change to
  `Instantiate` / `Uninstall`; data shape only.

- **Hosted invoice Checkout metadata** — `velox_invoice_id` is now
  propagated to both the Checkout Session and the underlying
  PaymentIntent, so `payment_intent.succeeded` webhooks route hosted-
  invoice payments to the right invoice. Caught during T0-17.3 review.

### Migrations

- `0048_invoice_public_token` — adds `invoices.public_token` with a
  partial unique index. Existing finalized invoices stay NULL until
  rotated; drafts never get a token.
- `0049_webhook_secondary_secret` — adds
  `webhook_endpoints.secondary_secret_encrypted` +
  `secondary_secret_last4` + `secondary_secret_expires_at`.
- `0050_customer_email_status` — adds `customers.email_status` (NOT NULL,
  default `unknown`) + `email_last_bounced_at` + `email_bounce_reason`.
- `0051_subscription_scheduled_cancel` — adds
  `subscriptions.cancel_at_period_end` (NOT NULL, default false) +
  `cancel_at` (nullable timestamptz, partial index for the cycle scan).
- `0055_recipe_instances` — adds `recipe_instances` (one row per
  installed recipe per tenant) with `UNIQUE (tenant_id, recipe_key)`
  for idempotency and a JSONB `created_objects` blob recording the
  IDs the instantiation created. RLS-enforced via
  `current_setting('app.current_tenant_id')`.

---

Historical entries (pre-Addendum) are summarised in
`web-v2/src/pages/Changelog.tsx`. When the next release is cut, the
contents of `[Unreleased]` above will move under a new
`## [0.X.0] - YYYY-MM-DD` heading here, and a matching entry will be
curated into the public changelog page.
