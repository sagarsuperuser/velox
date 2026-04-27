# Velox 90-Day Plan

**Window:** 2026-04-25 → 2026-07-24
**Wedge:** AI-native billing engine you can self-host (see `docs/positioning.md`)

## North-star outcome

**Three design partners running real billing through Velox by day 90.** At least one in full production with paying customers cutting through the system.

## Leading indicators

- Time-to-first-invoice (sign-up → test invoice sent): target **under 5 minutes** by week 4
- AI-native demo path (multi-dim meter → pricing rule → invoice with cost breakdown): working end-to-end by week 3
- Self-host one-shot install (Helm or Compose, fresh VM → working tenant): under **1 hour** by week 9
- GitHub stars: 100+ by day 90 (proxy for OSS narrative resonance, not the goal itself)

## Gap-analysis alignment

This plan executes the wedge from `docs/positioning.md` — not full Stripe parity. Most Tier 1 Stripe-parity gaps are deferred. **Four Tier 1 items are in scope** because they fall naturally out of the wedge work:

| Tier 1 gap | In-plan home |
|---|---|
| Decimal quantities | Week 2 — ships with multi-dim meters |
| Aggregate usage modes (`max`, `last_during_period`, `last_ever`) | Week 2 — expressed as multi-dim meter aggregations |
| `POST /v1/invoices/create_preview` | Week 5 — the engine behind the cost dashboard's projected bill |
| Billing thresholds + Billing alerts | Week 5 — explicit deliverables |

Everything else from the gap analysis (Schedules, Quotes, Promotion Codes, `add_invoice_items`, cross-resource search, Customer Balance ledger, lookup keys, `mark_uncollectible`/`void` pause modes, plus all Tier 2 items) stays in the strategic backlog. See "Out of scope" at the bottom for the full deferred list.

---

## Phase 1 — Wedge clarity + AI-native foundation (Days 1–30)

Goal: ship the AI-native primitives that justify the positioning. Without these, the wedge is just a slide.

### Week 1 (Apr 25 – May 1) — Position & narrative
- [x] `docs/positioning.md` published (this commit)
- [x] `README.md` rewritten to lead with AI-native + self-host
- [ ] `velox.dev` hero rewritten (or stand it up if not yet live)
- [x] Outreach list: 50 candidate design partners (AI inference, vector DB, dev infra) with named contacts
- [x] First long-form post drafted: "Why Stripe Billing's Meter API doesn't fit AI workloads"

### Week 2 (May 2–8) — Multi-dimensional meters
- [x] Schema migration: `usage_events.dimensions JSONB`, GIN index on dimensions
- [x] **Decimal quantities** — `value` accepts NUMERIC, not just BIGINT (GPU-hour fractions, partial-token edge cases). _Stripe Tier 1 gap, hoisted here — maps to Stripe's `quantity_decimal`._
- [x] Service layer: aggregate over arbitrary dimension subsets (`SUM(value) GROUP BY dimensions->>'model'`)
- [x] **Aggregation modes per pricing rule** — `sum` (default), `count`, `last_during_period`, `last_ever`, `max`. _Stripe Tier 1 gap (their legacy Plan `aggregate_usage` enum), expressed as a per-rule choice instead of a per-meter one — strictly more expressive._
- [x] Handler: `POST /v1/usage_events` with `{meter, customer, dimensions, value, timestamp, idempotency_key}`
- [x] Pricing rules: `pricing_rule.dimension_match JSONB` with subset-match semantics; rule resolution at finalize
- [x] Tests: aggregation correctness, pricing rule precedence, partial-dimension match, decimal value handling, all five aggregation modes
- [x] Benchmark: 50k events/sec sustained ingest on a single tenant

### Week 3 (May 9–15) — Pricing recipes
- [x] Recipe definition format (YAML in `internal/recipe/recipes/`)
- [x] Built-in recipes: `anthropic_style`, `openai_style`, `replicate_style`, `b2b_saas_pro`, `marketplace_gmv`
- [x] `POST /v1/recipes/instantiate` — creates products + prices + meters + dunning + webhooks atomically
- [x] Dashboard UI: pick recipe → preview generated objects → instantiate
- [x] Recipes documented at `/docs/recipes` with copy-pasteable curl

### Week 4 (May 16–22) — Quickstart wizard + 5-min path
- [x] Onboarding flow: pick template → connect Stripe (test) → tax mode → branding → send first test invoice
- [x] Sample data: each recipe seeds 1 demo customer + 1 active subscription
- [x] Telemetry on time-to-first-invoice (audit-log driven) — `TimeToFirstInvoiceSeconds` on `GET /v1/billing/dashboard` computed via `MetricsTTFIReader` (audit-log scan of `invoice.finalize` minus `tenants.created_at`); see `internal/billing/ttfi_postgres.go` (PR #57)
- [ ] **Demo recording** — 5-minute screen recording walking through wizard → invoice in inbox

---

## Phase 2 — Operator UX + design-partner outreach (Days 31–60)

Goal: punch above weight on operator UX so demo calls land. Start outreach.

### Week 5 (May 23–29) — Cost dashboard + the engine behind it
The wedge's sticky feature, plus the two Stripe Tier 1 gaps it depends on. Dense week — if slip happens, the engine + thresholds/alerts ship first (independently useful), the dashboard component bleeds into Week 6.

- [x] **`POST /v1/invoices/create_preview`** — computes a draft invoice for an in-progress period without committing. Powers (a) the cost dashboard's "projected bill" line, (b) plan-change confirmation dialogs, (c) the operator plan-migration preview in Week 6. _Stripe Tier 1 gap, hoisted because the dashboard depends on it._
- [x] **Billing thresholds** — `subscription.billing_thresholds.usage_gte` (per-item) + `amount_gte` (per-subscription). Crossing a threshold finalizes the invoice early with `billing_reason="threshold"`. _Stripe Tier 1 gap, hoisted — it's the "stop-the-bleeding" surface AI-usage buyers expect._
- [x] **Billing alerts** — `POST /v1/billing/alerts` with `recurrence` (`one_time` / `per_period`); fires `billing.alert.triggered` webhook + dashboard notification. _Stripe Tier 1 gap._
- [ ] React component: `<VeloxCostDashboard tenantKey customerId />` — current period usage by dimension, projected bill (powered by `create_preview`), top usage drivers, alert threshold visualization
- [x] Public iframe-able URL with secure token (reuses public-token pattern from hosted invoice)
- [x] Theming via CSS variables; dark mode by default — `?theme=light|dark` (default dark) and `?accent=#RRGGBB` (override `--primary` + `--ring`); see `web-v2/src/lib/embedTheme.ts` and the docs at `/docs/embeds/cost-dashboard`
- [x] Documented embed snippet at `/docs/embeds/cost-dashboard`

### Week 6 (May 30 – Jun 5) — Live event stream + plan migration preview
- [x] Real-time webhook event UI (server-sent events, replay button, payload diff for retries)
- [x] Plan migration tool: pick old plan → new plan → preview impact across N customers (per-customer before/after invoice) → one-click commit with audit trail

### Week 7 (Jun 6–12) — Bulk operations + one-off invoice composer
- [x] Bulk: apply coupon to N customers, schedule cancel for cohort, plan migrate cohort
- [x] One-off invoice composer (target 30-second flow, no leaving customer page)
- [x] Operator CLI: `velox sub list`, `velox invoice send`, `velox import-from-stripe`

### Week 8 (Jun 13–19) — Outreach intensifies
- [ ] 50 cold emails sent (week 1 list)
- [ ] 10+ demo calls scheduled
- [ ] **3 design partners with signed LOI** (12 months free, co-branded case study, weekly check-in)
- [x] Onboarding playbook drafted (`docs/design-partner-onboarding.md`) — partner-facing lifecycle from LOI through Day-90 case-study draft

---

## Phase 3 — Self-host story + first production cutover (Days 61–90)

Goal: prove the self-host pillar works end-to-end. Get one partner to production.

### Week 9 (Jun 20–26) — Self-host playbook
- [x] Helm chart for Kubernetes (`charts/velox/`)
- [x] Docker Compose for single-VM (`deploy/compose/`)
- [x] Postgres backup + restore guide (pg_basebackup + WAL archive)
- [x] Terraform module for AWS VPC deploy (`deploy/terraform/aws/`)
- [x] Self-host docs page at `/docs/self-host`
- [ ] **Cold-install test:** non-Velox engineer follows docs from scratch, reports friction

### Week 10 (Jun 27 – Jul 3) — Compliance + audit posture
- [x] Encryption-at-rest verification (webhook secrets, API keys, customer email/PII): single doc tying it together
- [x] Audit log retention guide (recommended retention windows, S3 archival pattern)
- [x] SOC2 control mapping doc (`docs/compliance/soc2-mapping.md`)
- [x] GDPR data export + deletion verified end-to-end against the multi-dim usage_events table

### Week 11 (Jul 4–10) — Migration FROM Stripe
- [x] Importer: customers, subscriptions, products, prices, finalized invoice history
- [x] CLI: `velox import-from-stripe --api-key=... --since=2024-01-01`
- [x] Migration guide with cutover playbook (parallel-run window, webhook redirection, reconciliation)
- [ ] Test against a real Stripe test account end-to-end

### Week 12 (Jul 11–17) — First production cutover
- [ ] At least 1 design partner cuts over to Velox in production
- [ ] Daily standup with that partner during cutover week
- [ ] Incident runbook tested (failover, rollback to Stripe Billing, billing reconciliation)

### Week 13 (Jul 18–24) — Stabilize + retro
- [ ] Bug-fix sprint based on production findings
- [ ] **Public retro post** — "What we learned shipping Velox to our first production tenant"
- [ ] Plan next 90 days based on what production actually exposed

---

## Risks & mitigations

| Risk | Mitigation |
|---|---|
| Design partners don't sign | 50-deep outreach pipeline; offer 12 months free; co-brand case study; weekly office hours |
| Multi-dim meter performance regression | Benchmark against current `usage_records` baseline before merging; partition `usage_events` by `(tenant_id, month)` from day one |
| Migration FROM Stripe is harder than estimated | Start the importer Week 7 in parallel, not Week 11; prototype on internal test data first |
| Self-host docs underdeliver | Cold-install test with non-Velox engineer in Week 9; budget 2 days of doc fixes after that |
| First-cutover incident causes partner churn | Parallel-run window (both Stripe Billing + Velox running, Velox dark) for 2 weeks before flipping primary |

## Out of scope (deferred to next 90 days)

These are real Stripe-parity gaps but they don't serve the wedge. (For the four Tier 1 items that **are** in scope, see "Gap-analysis alignment" near the top.) Each below is a quarter-long initiative on its own; revisit after design-partner traction is real.

- Subscription Schedules / Phases
- Quotes
- Connect / multi-party
- Multi-PSP (Razorpay, Paystack, Adyen)
- Revenue Recognition (ASC 606)
- Sigma / SQL reporting
- Promotion Codes (vs the existing Coupon API)
- Multi-currency
- 50+ payment method types
- Pricing Tables / Embedded Checkout components
- Smart Retries (ML-driven dunning)
- Automatic card updater

## Open questions

- Lead vertical: AI inference, vector DB, or dev infra? **Proposed:** all three in week-1 outreach, double down on whichever segment converts first
- Cloud pricing model: defer until 2 production tenants exist
- Whether to lean on a hosted demo tenant for OSS visitors to "try without installing": likely yes, decide week 4

---

**Cadence:** weekly review every Saturday (current week's deliverables + next week's plan). Major milestones (week 4, 8, 12) trigger a short public update post.
