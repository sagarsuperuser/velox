# Velox Phase B Roadmap — AI-native primitives

**Status:** Deferred (2026-05-30) — Phase B is on hold pending
completion of the MANUAL_TEST.md sweep. The operator is running
through every FLOW in the manual test doc end-to-end to surface
regressions and stale assertions before opening any new build
track. Resume B1 design pass after the manual sweep closes.

**Context:** Phase A1 (coupons cut, ADR-039) shipped. Phase B
implements the AI-native primitives that close the gap between Velox
and the AI-native peer set (Orb / Metronome / Lago / Stripe Token
Billing). Each B-item is sized for its own design + ship session;
this doc captures sequencing so the next session has a clean starting
point.

**Demo-leverage ranking** (for when Phase B resumes):
1. B1 — LLM provider cost-table ingestion (highest; flips pitch
   from "generic usage billing" to "AI-native billing")
2. B2 — Embeddable cost dashboard + JWT (closing emotional beat;
   verify existing React component first)
3. B3 (auto-recharge half) — closing demo beat; defer commits-
   and-draw-down until enterprise DP names it
4. B4 — Price-book versioning — nice-to-have, not load-bearing

## Sequencing rationale

Ordered by **DP-demo leverage**, not engineering size. The first DP
will judge Velox on the AI-native demo (token meter → live cost →
auto-recharge → embedded dashboard). Closer-to-demo items go first.

## B1 — LLM provider cost-table ingestion + recipes evolution

**Status:** Pending
**Estimated effort:** 1-2 weeks
**Why now:** highest DP-leverage. Stripe Token Billing's killer
feature is "live model prices for OpenAI / Anthropic / Google in the
billing engine." Velox's `internal/recipe` has the `anthropic_style`
/ `openai_style` / `replicate_style` recipes but they hardcode
markup math — they don't ingest live prices.

**Scope:**
- New `internal/llmprovider/` package: HTTP adapters for Anthropic,
  OpenAI, Google model-price APIs. Periodic poll → write to a
  `llm_provider_prices` table keyed by `(provider, model, effective_at)`.
- Operator-defined markup overlay: per-tenant config carrying
  margin % per model (or a default). Applied on top of base price
  when computing usage cost.
- Evolve recipe templates: `anthropic_style` instantiates a meter
  + rule that **reads from the ingested price table** instead of
  hardcoded numbers. Same for OpenAI / Replicate.
- Settings UI: connect LLM provider → manage markup → see live
  price table.
- Industry parity: Stripe Token Billing (live sync) and Lago
  (manual model rate cards). Velox's edge: auto-sync + operator
  markup overlay.

**Dependencies:** none. Self-contained domain package.

**Done definition:**
- Operator can connect Anthropic in Settings, see model prices
  pulled live.
- Recipe install lays down a meter that cost-rates against live
  prices + tenant markup.
- Stale-data detection (>24h since last sync) surfaces an alert.

## B2 — Embeddable cost dashboard with JWT embed tokens

**Status:** Pending
**Estimated effort:** 3-5 days
**Why now:** second-highest DP-leverage. README already claims
`<VeloxCostDashboard customerId={…} />` exists — verify it's actually
shipped (not aspirational) and add the embed-token auth pattern.

**Scope:**
- Verify the React component exists in `web-v2/` or ship if missing.
- New `/v1/embed-tokens` endpoint: operator mints a short-lived JWT
  scoped to a single customer ID + dashboard-read permission only.
- Token consumed by the dashboard component for cross-domain
  embedding without exposing tenant API keys.
- Industry parity: Metronome's React/iframe embed
  (https://docs.metronome.com/guides/customers-billing/optimize-customer-experience/customer-dashboards-and-reporting).

**Dependencies:** B1 ideally lands first so the embedded dashboard
reads live cost data, but the embed mechanism itself is independent.

**Done definition:**
- Operator demo: copy embed snippet → paste into a sandbox React app
  → cost dashboard renders with the operator's customer's live spend.

## B3 — Commits + draw-down + auto-recharge

**Status:** Pending
**Estimated effort:** 1-2 weeks
**Why now:** enterprise-track for AI infra. Series A-B DPs increasingly
sell prepaid commits ("$10K committed for the year") and want the
billing system to track draw-down + alert on thresholds.

**Scope:**
- New `internal/commit/` package. Event-sourced like `internal/credit`.
- Commit object = enterprise prepaid balance with contract terms
  (rollover, expiry, scope filter).
- Auto-draw against usage at cycle close.
- Auto-recharge: when balance falls below threshold, fire a Stripe
  PaymentIntent to top up; operator-configurable threshold + amount.
- Industry parity: Metronome's enterprise commits
  (https://metronome.com/blog/a-practical-guide-to-enterprise-commit-contracts).

**Dependencies:** Builds on the credit ledger primitive; ideally
lands after B1 so commits can be denominated in token-cost terms.

**Done definition:**
- Operator can create a commit on a customer.
- Cycle close auto-draws from commit before charging.
- Threshold-triggered Stripe top-up fires + lands a credit row.

## B4 — Price-book versioning + simulation

**Status:** Pending
**Estimated effort:** 1 week
**Why now:** Orb's killer demo feature is "preview a price change
against the last 30 days of usage before committing." Velox's
pricing is mutable in-place — operators can't see the impact of a
change before shipping it.

**Scope:**
- Add `effective_at` + `superseded_at` columns on `plans` /
  `pricing_rules`.
- New `/v1/pricing/preview` endpoint: given a draft price set + a
  customer + a usage window, return what the invoice WOULD have been.
- Dashboard "Compare" UI: side-by-side current vs draft pricing
  against the last 30/60/90 days.
- Industry parity: Orb's price-book versions
  (https://www.withorb.com/blog/make-price-changes-easily-with-versions-and-migrations).

**Dependencies:** none.

**Done definition:**
- Operator can draft a price change.
- Preview shows MRR impact + per-customer delta.
- Apply commits the new price; old price stays read-readable for
  audit.

## B5 — Stripe Customer lazy-create UX surface

**Status:** Deferred (2026-05-31)
**Estimated effort:** ~30 min, single PR
**Why now:** Velox creates the Stripe Customer object lazily on first
payment action (matches Lago / Orb / Metronome — verified 2026-05-31
research). The architecture is correct for the AI-native peer set,
but operators hit "I created a customer, why isn't it in Stripe?"
surprise on day one. Architecture stays as-is — this is a UX gap.

**Scope (UX only, no architectural change):**
- `CustomerDetail.tsx`: when `stripe_customer_id` is empty, render
  *"Not yet created — created on first payment action"* with a
  "View in Stripe" disabled state. When populated, show the `cus_xxx`
  with an active "View in Stripe" external link.
- Add a "Create in Stripe now" action button to Customer Detail.
  Backend: thin wrapper handler that fires the existing
  `paymentmethods.StripeAdapter.EnsureStripeCustomer` (already
  idempotent — short-circuits if `stripe_customer_id` is set).
- README / dashboard tooltip line documenting the lazy-create
  pattern + the AI-native peer-set rationale, so future operators
  understand it isn't a bug.

**Dependencies:** none.

**Done definition:**
- Operator creates customer in Velox → sees explicit "not yet
  created" state on Customer Detail.
- Operator clicks "Create in Stripe now" → within ~1s, Stripe
  Customer is created with full fields (email, name, address,
  tax_id) per the Phase 1 sync; `stripe_customer_id` populated;
  field updates to show `cus_xxx`.
- No regression on the lazy-on-first-PM-action path.

**Industry references:**
- Lago: lazy + opt-in (https://getlago.com/docs/integrations/payments/stripe-integration)
- Orb: lazy + explicit mapping (https://docs.withorb.com/integrations-and-exports/stripe)
- Metronome: lazy + billing-config (https://docs.metronome.com/integrations/invoice-integrations/stripe)
- Chargebee/Recurly use eager — different category (traditional SaaS, not AI-native)

## What this roadmap explicitly defers

- **Multi-PSP** (Razorpay / Adyen): defer until a paying tenant asks.
  README already says this.
- **Quote / contract object** (sales-led flows): explicit defer per
  README. Recurly / Maxio own that lane.
- **Revenue Recognition / Sigma**: explicit defer per README; bring
  your own warehouse + dbt.
- **Multi-recipient email** (Chargebee shape): additive when a DP asks.
- **Trial reshape** (free-credits-only): leave as-is; revisit if a
  DP wants pure free-credits.

## Re-prioritization triggers

This roadmap holds unless:
- A signed DP names a specific gap not in B1-B4 → that gap jumps to
  the top.
- AI-native peer convergence shifts (e.g. Stripe Token Billing
  unlocks a feature Velox doesn't have) → re-research.
- A B-item turns out to be longer than estimated → reassess B-order.
