# Stripe Tier 2 Gap Analysis

> Time-boxed (≤90 min) hardening pass. No code changes — output is this doc.
> Author: Track A (worktree `agent-a18dc8bf`). Date: 2026-04-26.
> Velox HEAD at start of pass: `7adf96b` (post-Week-5c/5d merges).

## Purpose

Tier 1 Stripe parity (customer-usage endpoint, `create_preview`, billing thresholds, billing alerts) closed today. The 90-day plan is silent on Tier 2. Before design-partner outreach (Week 8) and the first production cutover (Phase 3), we need a brutally honest map of:

1. What Stripe Billing's **Tier 2** surface actually contains (verified, not from memory).
2. Where Velox **already** matches it, where it **partially** matches, and where it's **absent** — with file-level citations.
3. Which gaps actually matter for the AI-native + self-host wedge, vs. which are generic-SaaS noise we should defer.

The output of section 3 is a Phase-3 inclusion shortlist. It is **not** a v2 roadmap.

## Conventions

- **Stripe citation** = a `WebFetch` result on the canonical doc URL. URLs that 404'd at fetch time are noted; fall-back URLs were tried.
- **Velox citation** = `package/file.go:func/type`, verified via `Grep` or `Read`.
- **State**:
  - `PRESENT` — functional, may want polish but not a parity gap.
  - `PARTIAL` — surface exists, gaps remain.
  - `ABSENT` — no surface.
- **Effort**: S (≤1 wk), M (1–3 wks), L (>3 wks).
- **Blast radius**: HIGH (engine + schema), MED (one domain), LOW (isolated).
- **Wedge fit**: HIGH (matters for AI-native pricing buyer), MED, LOW (generic SaaS only).

---

## Section 1 — Stripe Tier 2 surface enumeration

### 1.1 Subscription Schedules / Phases
- Source: `https://docs.stripe.com/api/subscription_schedules` + `https://docs.stripe.com/billing/subscriptions/subscription-schedules`.
- A schedule predefines a sequence of **phases**, up to 10 current/future. Each phase carries: `items`, `start_date`/`end_date` (or `duration`), `proration_behavior` (`create_prorations` / `none` / `always_invoice`), `billing_thresholds`, `collection_method`, `default_payment_method`, `trial_end`, `coupons`/`discounts`, `metadata`, `billing_cycle_anchor`.
- `default_settings` carries phase-level defaults that apply unless overridden. `end_behavior` is `release` (schedule terminates, subscription continues) or `cancel`.
- API surface: `POST /v1/subscription_schedules`, `POST .../release`, `POST .../cancel`, plus retrieve/list/update. Also `from_subscription` to wrap an existing sub.
- Use cases pulled directly from Stripe docs: future-dated launches, retroactive backdates, scheduled mid-contract price changes, ramp pricing ("50% off first 3 months → standard"), per-phase tax-rate variations, installment plans (`iterations`).

### 1.2 Billing-cycle anchor + proration_behavior
- Source: `https://docs.stripe.com/billing/subscriptions/billing-cycle`.
- `billing_cycle_anchor` (Unix ts) or `billing_cycle_anchor_config` (recommended) sets the renewal anchor.
- `proration_behavior` is the lever that controls cycle-change accounting:
  - `create_prorations` — auto-creates a prorated invoice between subscription creation and the first full invoice.
  - `none` — initial period is free; nothing invoiced until first cycle.
  - `always_invoice` — bill immediately for any remaining time owed.
- On plan change: anchor resets to "now" when switching to a price with a different `recurring.interval`; same-interval keeps the anchor.

### 1.3 Upgrade/downgrade flow
- Source: `https://docs.stripe.com/billing/subscriptions/upgrade-downgrade`.
- Mid-cycle upgrade: pass `items[id]` + `items[price]` (omitting `items[id]` adds rather than replaces).
- Proration is automatic; `proration_behavior: always_invoice` bills immediately.
- For end-of-period transitions, Stripe explicitly recommends Subscription Schedules ("to manage the transition safely and avoid overwrites").
- Downgrades issue credit prorations.
- Preview: Stripe references previewing prorations through `invoices.create_preview`.

### 1.4 Customer Balance (free-floating credit/debit ledger)
- Source: `https://docs.stripe.com/billing/customer/balance` + `https://docs.stripe.com/api/customer_balance_transactions` + Customer object docs.
- Two related fields: `Customer.balance` (single-currency, default-currency) and `Customer.invoice_credit_balance` (multi-currency map).
- Negative = credit, positive = debit. Balance auto-applies to the **next** invoice on finalize. Cannot target a specific invoice or skip future invoices.
- Customer Balance Transactions: an immutable ledger of debit/credit events. Endpoints: `POST /v1/customers/:id/balance_transactions`, `GET .../:id`, `GET .../...`, `POST .../:id` (update).
- Distinct from prepaid usage credits / Credit Grants — Customer Balance is "money the customer owes / money owed back to the customer that we'll roll into the next invoice."

### 1.5 Promotion Codes (vs raw Coupons)
- Source: `https://docs.stripe.com/billing/subscriptions/coupons` + `https://docs.stripe.com/api/promotion_codes`.
- **Coupon** = the discount definition (percent_off / amount_off, duration once|forever|repeating, max_redemptions, redeem_by, applies_to, currency_options).
- **PromotionCode** = a customer-facing code mapped to a coupon, with extra controls: `customer` (restrict to one customer), `expires_at`, `max_redemptions` (separate from coupon's), `restrictions.first_time_transaction`, `restrictions.minimum_amount`, `restrictions.minimum_amount_currency`, `active`. One coupon ↔ many codes.
- Endpoints: `POST /v1/promotion_codes`, retrieve/update/list. Customer Portal can self-redeem when configured.
- Stack: up to 20 entries in `discounts[]` per subscription/invoice.

### 1.6 Subscription cancel semantics
- Source: `https://docs.stripe.com/billing/subscriptions/cancel`.
- Modes: immediate (default), `cancel_at_period_end=true`, scheduled via `cancel_at` (Unix ts).
- `prorate=true`: emit credit proration on mid-period cancel.
- Webhook events: `customer.subscription.updated` (when `cancel_at_period_end` flips), `customer.subscription.deleted` (on actual cancel).
- Reactivation: set `cancel_at_period_end=false` before the period ends.

### 1.7 pause_collection
- Source: `https://docs.stripe.com/billing/subscriptions/pause-payment`.
- Three behaviors:
  - `keep_as_draft` — invoices still generated but `auto_advance=false`; collected manually later.
  - `mark_uncollectible` — invoices generated and marked uncollectible; existing customer balance still applies.
  - `void` — invoices auto-voided; effectively "free during pause."
- Optional `resumes_at` for auto-unpause.

### 1.8 Payment Links
- Source: `https://docs.stripe.com/payment-links/api` + `https://docs.stripe.com/api/payment_links/payment_links`.
- Shareable URL → hosted Stripe-hosted payment page → spawns a Checkout Session per visit. Reusable, multi-purchase.
- Fields referenced (full list partly cut off in the public doc): `line_items`, `price_data`, `subscription_data`, `payment_method_types`, `after_completion`, `allow_promotion_codes`, `automatic_tax`, `billing_address_collection`, `customer_creation`.
- Endpoints: `POST /v1/payment_links`, `POST .../:id`, `GET .../:id`, `GET .../`, `GET .../:id/line_items`.
- Distinct from Hosted Invoice URL: Payment Link is a **product/SKU surface** (one URL for many checkouts), Hosted Invoice URL is a **single-invoice** surface for a specific debt.

### 1.9 Quotes (sales-led contract billing)
- Source: `https://docs.stripe.com/api/quotes` + `https://docs.stripe.com/quotes`.
- Workflow: `draft` → `open` (finalized, awaiting customer) → `accepted` (auto-creates invoice/sub/sub-schedule) → optional `canceled`.
- Endpoints: `POST /v1/quotes`, `.../finalize`, `.../accept`, `.../cancel`, `.../pdf`. Webhooks: `quote.finalized`, `quote.accepted`, `quote.canceled`.
- Quote number format `QT-[prefix]-[sequence]-[revision]`.
- Used for sales-led renegotiation, multi-year contracts, ramp pricing.

### 1.10 Revenue Recognition (ASC 606)
- Source: `https://docs.stripe.com/revenue-recognition`.
- Accrual accounting automation: deferred revenue schedules, GL account mapping, multi-currency revenue, contract modeling, marketplace/Connect handling, CSV exports.
- Distinct accounting layer **on top of** transaction data.

### 1.11 Tax Rates (manual catalogue)
- Source: `https://docs.stripe.com/billing/taxes/tax-rates`.
- Manual catalogue of `{display_name, percentage, jurisdiction, inclusive, country, tax_type}`. Up to 10 per line; line-level overrides invoice-level defaults.
- Distinct from Stripe Tax (auto-calc by jurisdiction) — manual rates are for tenants who own their own tax determination.

### 1.12 Add-lines / `add_invoice_items` to draft invoices
- Source: `https://docs.stripe.com/api/invoices/add_lines` + `https://docs.stripe.com/api/invoiceitems`.
- `POST /v1/invoices/{INVOICE_ID}/add_lines` — append one-off lines to a **draft** invoice. Supports negative amounts (credits), descriptions, tax_rates, price_data.
- Standalone `InvoiceItem` (`POST /v1/invoiceitems`): an unattached line that auto-flows into the customer's next finalized invoice. Distinct from `add_lines` (which is bulk-append to a specific draft).
- Subscription update path: `add_invoice_items` parameter — schedule one-off lines to land on the next subscription invoice.

### 1.13 Invoice collection_method = send_invoice
- Source: `https://docs.stripe.com/billing/invoices/sending` + `https://docs.stripe.com/billing/invoices/overview`.
- `collection_method=send_invoice` (vs `charge_automatically`): Stripe emails the invoice with payment instructions; customer self-serves the payment.
- `days_until_due` controls due-date.
- `auto_advance=false` keeps the invoice editable past finalize timing.
- Plus Tier 2 invoice-status verbs: `void`, `mark_uncollectible`, `paid_out_of_band`.

### 1.14 Customer Portal
- Cited via Customer Portal mentions in promotion-code and pause-collection docs (no dedicated WebFetch performed since wedge fit is low).
- Self-serve: update payment method, change plan, cancel, redeem promotion code, download invoices.

---

## Section 2 — Velox state per feature

### 2.1 Subscription Schedules / Phases — **PARTIAL**
- Velox has **item-scoped scheduled plan changes** but no schedule-of-phases primitive.
- `internal/subscription/service.go:UpdateItem` (`func` around L289) writes `pending_plan_id` + `effective_at` on a single subscription_item when `Immediate=false`. Cleared via `internal/subscription/service.go:ClearPendingItemChange` (around L367) and `DELETE /v1/subscriptions/{id}/items/{itemID}/pending-change` (`internal/subscription/handler.go` L210, L673). Events `subscription.pending_change_scheduled` and `subscription.pending_change_canceled` fire (`internal/subscription/handler_test.go:587, 673`).
- `internal/subscription/handler.go:scheduleCancel` (L348) supports `cancel_at` (RFC3339) or `at_period_end` for sub-level cancel.
- Migration `0029_subscription_item_changes`, `0027_item_change_proration`, `0051_subscription_scheduled_cancel`, `0052_subscription_pause_collection` show item-level + sub-level scheduling.
- **Gap vs Stripe**: no multi-phase model, no `default_settings`, no `end_behavior`, no per-phase `proration_behavior`/`coupons`/`tax_rates`/`billing_thresholds`, no installment-plan `iterations`. A "phase" in Velox today = one pending change on one item.
- **Surprise**: Velox's per-item granularity is actually **finer** than Stripe's per-phase model for the multi-item case. The composition is different — Stripe builds phases over time; Velox lets items diverge in their own pending plans. For ramp pricing across several phases ("year 1 50%, year 2 75%, year 3 100%") Velox would need either a phase primitive or chained item-pending-change auto-apply.

### 2.2 Billing-cycle anchor + proration_behavior — **PARTIAL**
- Velox **does** prorate plan changes (full proration apparatus in `internal/subscription/handler.go`: `ProrationInvoiceCreator`, `ProrationCreditGranter`, `ProrationCouponApplier`, `ProrationTaxApplier`; tested at `internal/subscription/handler_test.go:539, 587, 778, 835`). Proration credits use the source-tuple dedup index from migration `0027_item_change_proration` and `0005_proration_dedup`.
- Velox **does not** expose Stripe's three-valued `proration_behavior` knob. The default is implicitly `create_prorations` (the only mode); there is no `none` (skip proration on plan change) and no `always_invoice` (force-bill the proration immediately rather than letting it ride to next cycle). Verified: `Grep proration_behavior|ProrationBehavior` across the entire repo returns 0 matches.
- Velox **does not** expose a `billing_cycle_anchor` knob. `BillingTime` is a per-subscription `calendar | anniversary` (`internal/subscription/service.go:BillingTime`) — not a configurable anchor timestamp.
- **Gap vs Stripe**: missing `proration_behavior=none` + `=always_invoice`; missing arbitrary anchor-date control.

### 2.3 Upgrade/downgrade flow — **PRESENT (with caveats)**
- `Service.UpdateItem` (`internal/subscription/service.go` ~L289) handles plan change immediate + scheduled, with full proration credit/invoice generation. Tests: `TestUpdateItem_PlanChange` (`internal/subscription/service_test.go:578`), end-to-end change-plan tests in `internal/subscription/handler_test.go`.
- Preview: `internal/billing/preview.go` + `internal/billing/create_preview_handler.go` shipped Week 5b — covers projected-bill and plan-change preview.
- **Caveat**: Velox doesn't model "upgrade vs downgrade" explicitly the way Stripe does (Stripe distinguishes via proration sign and may emit `customer.subscription.updated` with different fields); Velox treats both as `domain.ItemChangeTypePlan` (`internal/subscription/handler_test.go:843`) and the proration tax/coupon engine handles the sign. Functionally fine; semantically lighter.

### 2.4 Customer Balance (free-floating ledger) — **PARTIAL**
- Velox has `internal/credit/` — a prepaid event-sourced credit ledger (ADR-004 — `docs/adr/004-event-sourced-credit-ledger.md`). Endpoints: `GET /v1/credits/balances`, `GET /v1/credits/balance/{customer_id}` (`internal/credit/handler.go:35-36`), `POST /v1/credits/grant` (`internal/credit/service.go:Grant`).
- Credits **debit on apply** to a finalized invoice atomically (`internal/credit/service.go:ApplyToInvoice`, `internal/credit/postgres.go:ApplyToInvoiceAtomic`). Reversal: `Service.ReverseForInvoice`.
- This is the **prepaid usage credit / Credit Grants pattern** — closer to Stripe's Billing Credits than to Customer Balance.
- **Gap vs Stripe Customer Balance**: no debit transactions (balance can only go down via apply or up via grant — no "customer owes us extra" debit row), no auto-application to the **next** invoice on finalize as a standing thing (Velox applies on demand inside `billSubscription`), and no `Customer.invoice_credit_balance` multi-currency map.
- `Grep customer_balance|balance_transaction|BalanceTransaction` returns 0 matches in `internal/`.
- **Surprise**: the prepaid-credits ledger is more sophisticated than Stripe Customer Balance for AI usage credits ("here's $1k of GPT-4 quota"). It's the right primitive for the wedge. The "free-floating credit/debit that auto-applies to next invoice" pattern is not in Velox at all.

### 2.5 Promotion Codes — **PARTIAL**
- Velox has `internal/coupon/` — full coupon CRUD, duration `once|forever|repeating` (`internal/coupon/postgres_filters_integration_test.go`), restrictions object with `first_time_customer_only`, `min_amount_cents`, `max_redemptions_per_customer` (`internal/coupon/service.go:147, 150, 258, 261, 513`), customer-scoped coupons (`customer_id` field), plan-scoped coupons (`plan_ids`), stackable flag (`internal/coupon/service.go:64`).
- Velox **does not** have the Coupon-vs-PromotionCode split. The `code` is the coupon's primary user-facing identifier and there's no separate "many promo codes mapping to one underlying coupon" surface. Verified: `Grep PromotionCode|promotion_code|promo_code` returns 0 matches in `internal/`.
- **Gap vs Stripe**: no per-code expiry separate from coupon expiry, no per-code customer restriction (Stripe lets you bind one promo code to one customer while leaving the underlying coupon open), no `restrictions.minimum_amount_currency`.
- **Wedge note**: AI-native buyers use coupons rarely; this is a generic-SaaS gap.

### 2.6 Subscription cancel semantics — **PRESENT**
- `internal/subscription/handler.go:scheduleCancel` (L348, route at L193) supports `at_period_end:true` or `cancel_at:<RFC3339>`. Migration `0051_subscription_scheduled_cancel`. Stripe-equivalent webhook events fire: `cancel_at_period_end` round-trips through the API (`internal/subscription/handler.go:373, 381, 1422`). Reactivation via clear-cancel.
- Proration on cancel: same machinery as upgrade/downgrade.
- **Status**: matches Stripe Tier 2 cancel semantics.

### 2.7 pause_collection — **PARTIAL**
- Migration `0052_subscription_pause_collection` exists (referenced from the `mark_uncollectible/void` comment at line 5). The migration explicitly notes: **"v1 supports only behavior='keep_as_draft'; mark_uncollectible/void"** — i.e., only one of Stripe's three behaviors is wired today.
- **Gap vs Stripe**: missing `mark_uncollectible` and `void` behaviors. `resumes_at` may or may not exist — would need a deeper migration read. The wire shape almost certainly uses `keep_as_draft` as the implicit default.

### 2.8 Payment Links — **ABSENT**
- `Grep payment_link|PaymentLink` returns 0 matches in `internal/`.
- Velox has the **Hosted Invoice URL** equivalent (`internal/hostedinvoice/handler.go` — Stripe's `hosted_invoice_url` per the package comment at L2). That's a **single-invoice** surface, not a reusable multi-purchase payment link.
- A "share this URL → customers buy plan X" Payment Links workflow has no surface today. Recipes (`internal/recipe/`) instantiate the catalog; nothing exposes "checkout a plan via shareable URL" beyond the operator dashboard's create-subscription flow.

### 2.9 Quotes — **ABSENT**
- `Grep -i quote` in `internal/` returns only RFC ETag matches and unrelated identifiers. No Quote object, no quote handler, no quote.finalized webhook.
- The 90-day plan and `docs/positioning.md` both explicitly state "No Quotes — sales-led contract billing should pick Recurly or Maxio." Confirmed not in v1 scope by design.

### 2.10 Revenue Recognition — **ABSENT (deliberate)**
- No `internal/revenue/`, no ASC 606 surface. `docs/positioning.md`: "No Revenue Recognition / Sigma — bring your own warehouse + dbt."
- Confirmed deliberate per `feedback_no_overengineering`. Don't ship it.

### 2.11 Tax Rates (manual catalogue) — **PARTIAL**
- `internal/tax/manual.go` — single tenant-level flat rate. `NewManualProvider(rateBP int64, taxName string)` returns one rate, applied uniformly across every line. No catalogue. Comment at `internal/tax/manual.go:9` admits: "Deliberately simple: no jurisdiction lookup, no per-product tax codes, no cross-border auto zero-rating. Tenants who need jurisdictional accuracy pick stripe_tax."
- `internal/tax/stripe.go` — full Stripe Tax integration with multi-jurisdiction breakdowns (`Result.Breakdowns`).
- **Gap vs Stripe Tax Rates**: no manual multi-rate catalogue. Stripe tenants can define and reuse N tax rates without going to Stripe Tax; Velox forces "one flat manual rate OR full Stripe Tax."
- **Wedge note**: per `project_tax_neutrality`, Velox's stance is "tenant's Stripe Tax handles the registration." So the manual catalogue is a small generic-SaaS gap, not a wedge gap.

### 2.12 add_lines / add_invoice_items — **PARTIAL**
- Velox **does** support adding line items to a draft invoice: `internal/invoice/postgres.go:AddLineItemAtomic` (L533, L537), `internal/invoice/service.go:AddLineItem` (L246), exposed at `internal/invoice/handler.go` (L234). Tests: `internal/invoice/postgres_add_line_item_test.go` (concurrent adds + non-draft rejection).
- Velox **does not** have a standalone `InvoiceItem` resource (an unattached line that auto-flows into the next finalized invoice for a customer). `Grep InvoiceItem` returns no `/v1/invoiceitems` surface.
- Velox **does not** have a subscription-update `add_invoice_items` parameter.
- **Gap vs Stripe**: no orphan-item-then-attach-on-finalize flow; only direct add to an existing draft.

### 2.13 collection_method = send_invoice — **ABSENT**
- `Grep collection_method|send_invoice|charge_automatically|days_until_due` in `internal/` returns 0 matches.
- Velox today is implicitly all `charge_automatically`: every invoice flows through `internal/billing/engine.go` charging-with-PaymentIntent path, then `internal/dunning/` if the charge fails.
- Hosted Invoice URL exists (`internal/hostedinvoice/`) so the **page** for a customer to self-pay an open invoice is there. But the **collection-method semantic** — finalize-and-don't-charge, due-in-N-days, dunning around the customer-paying-themselves rather than auto-debit — is not modeled. There's also no `paid_out_of_band` action surfaced (`Grep paid_out_of_band|MarkPaidOutOfBand` returns 0 matches).
- Velox has `void` (`internal/invoice/handler.go:201` — `POST /v1/invoices/{id}/void`) but **no `mark_uncollectible`** (`Grep MarkUncollectible|mark_uncollectible` returns 0 matches in `internal/`).

### 2.14 Customer Portal — **PRESENT**
- Full `internal/customerportal/` package: handler, magic-link auth (`magiclink.go`), session store, public surface. `internal/portalapi/` for the embeddable side. The wedge already accounts for this — customers self-serve via portal.
- **Note**: portal coverage of new Tier 2 features (pause behaviors, schedules, promotion codes) lags whatever the operator API gains. Not a Tier 2 gap per se — a downstream surface to refresh whenever Tier 2 lands.

---

## Section 3 — Gap prioritization

For each PARTIAL or ABSENT row, score effort / blast / wedge, then bucket Phase-3 inclusion.

| # | Feature | State | Effort | Blast | Wedge fit | Phase-3 inclusion | One-line reason |
|---|---|---|---|---|---|---|---|
| 2.1 | Subscription Schedules / Phases | PARTIAL | L | HIGH | LOW | **defer-to-v2** | Phases are sales-led + ramp-pricing primitives; AI-native usage buyers don't need them. Item-scoped pending changes already cover the common upgrade-at-renewal case. Needs RFC if revisited. |
| 2.2a | `proration_behavior=none` | PARTIAL | S | LOW | MED | **yes (Phase 3)** | One-line knob; production cutovers from Stripe will set it on cohorts where finance doesn't want mid-cycle deltas. Cheap to ship. |
| 2.2b | `proration_behavior=always_invoice` | PARTIAL | S | MED | MED | **yes (Phase 3)** | Required for "bill the upgrade now" UX (operator dashboard plan-migration tool, Week 6). Already de facto needed by the Week 6 plan-migration preview. |
| 2.2c | Arbitrary `billing_cycle_anchor` | PARTIAL | M | MED | LOW | **defer-to-v2** | `calendar` vs `anniversary` covers ~95% of buyers. Arbitrary anchor only matters for Stripe-migration cohorts where the existing anchor must be preserved — handle in the Week 11 importer instead. |
| 2.4 | Customer Balance (debit/credit ledger that auto-applies to next invoice) | PARTIAL | M | HIGH | LOW | **defer-to-v2** | Velox's prepaid `credit` ledger covers the AI-native usage-credit case better than Stripe Customer Balance. The "free-floating debit that rolls into next invoice" pattern is generic-SaaS A/R. Needs RFC if a design partner asks for it. |
| 2.5 | Promotion Codes (vs raw Coupons) | PARTIAL | M | MED | LOW | **defer-to-v2** | Coupons in Velox already do customer-scoped, plan-scoped, first-time-only, min-amount, repeating duration. The Coupon↔PromotionCode split is generic-SaaS marketing infrastructure; AI buyers don't run promo campaigns. |
| 2.7 | pause_collection — `mark_uncollectible` and `void` behaviors | PARTIAL | S | LOW | MED | **yes (Phase 3)** | Migration `0052` already wired the column with comment acknowledging the gap; finishing the two missing behaviors is small, schema-bounded, and finance teams expect both during Stripe migrations. |
| 2.8 | Payment Links | ABSENT | M | MED | MED | **yes — Week 8 stretch / Phase 3** | Design-partner self-onboarding ("share this URL to get someone on the AI Pro plan in 60 seconds") is a clean Phase-3 win. Recipes already define the catalog; Payment Links is the front door. |
| 2.9 | Quotes | ABSENT | L | HIGH | LOW | **defer-to-v2** | Already an explicit `docs/positioning.md` non-goal: "No Quotes — sales-led contract billing should pick Recurly or Maxio." Don't reverse here. |
| 2.10 | Revenue Recognition | ABSENT | L | HIGH | LOW | **defer-to-v2** | Explicit positioning non-goal. Bring-your-own-warehouse + dbt is correct. |
| 2.11 | Tax Rates manual catalogue | PARTIAL | S | LOW | LOW | **defer-to-v2** | One flat rate or full Stripe Tax covers ~all v1 buyers. Multi-rate catalogue without jurisdiction logic is a footgun (per `project_tax_neutrality`). |
| 2.12a | Bulk `add_lines` to draft | PRESENT | — | — | — | n/a | Already shipped (`AddLineItemAtomic`). Polish only. |
| 2.12b | Standalone `InvoiceItem` resource | ABSENT | M | MED | LOW | **defer-to-v2** | "Park a charge on the customer for the next invoice" is a generic-SaaS pattern. Operators today can wait for the draft and add the line. Not wedge-relevant. |
| 2.13a | `collection_method=send_invoice` + `days_until_due` | ABSENT | M | HIGH | MED | **yes (Phase 3) — design partners will need it** | Many AI buyers' larger customers (especially B2B and EU GDPR-strict tenants) explicitly require "invoice me, don't auto-debit" with NET-30 terms. Migration FROM Stripe (Week 11) will hit this on day one. Production-blocking for non-self-serve cohorts. |
| 2.13b | `mark_uncollectible` and `paid_out_of_band` invoice actions | ABSENT | S | LOW | MED | **yes (Phase 3)** | A/R hygiene basics. Once `send_invoice` lands, these are the natural triage verbs. Cheap on top of existing void/finalize state machine. |

### Tiering

#### Must-fix before design-partner outreach (Week 8)
Anything HIGH wedge fit + LOW/MED effort that buyers will ask about in the demo call:
- **Payment Links — stretch goal for Week 8.** AI buyers ask "can I send a URL to my customer to subscribe?" in the first demo. Velox's Recipes define the catalog — Payment Links is the front door. If Phase 2's Week 7 (bulk operations + invoice composer) finishes early, slot Payment Links there.
  - If it doesn't fit Week 7, push to early Phase 3 (Week 9 alongside Helm/Compose, since both touch operator-facing self-serve).
- **Nothing else from Tier 2 is HIGH wedge fit + cheap.** The wedge is multi-dim meters, recipes, cost dashboards, thresholds, alerts — all already shipped or in plan. Tier 2 features are mostly migration-blockers, not demo-wow factors.

#### Must-fix before production cutover (Phase 3, Weeks 9–12)
Production-blocking gaps regardless of wedge fit. These are the "design-partner #1 wants to flip the live switch and Stripe parity is missing":
1. **`proration_behavior` knob (2.2a + 2.2b)** — ~1 wk total. Required by the Week 6 plan-migration preview to surface "always_invoice now" as an option, and required during Week 11/12 cutover when finance teams say "don't proration this cohort." Combined RFC + impl + operator dashboard exposure.
2. **`pause_collection` full behavior set (2.7)** — ~3 days. Migration `0052` already concedes the gap. Add `mark_uncollectible` and `void` behaviors. Cheap, schema-bounded, A/R-hygiene check-mark on a feature that already half-exists.
3. **`collection_method=send_invoice` + `days_until_due` (2.13a)** — ~2 wks. **Production-blocking for B2B AI cohorts.** Most >$10k/month enterprise customers of design partners will require NET-30 invoicing, not auto-charge. Touches: subscription model (per-sub collection method), invoice finalize path (auto-charge gate), dunning (reminder cadence vs. card-retry), hosted-invoice page (already paid-link-out via Stripe Checkout — fine). Needs RFC.
4. **`mark_uncollectible` and `paid_out_of_band` invoice verbs (2.13b)** — ~3 days. Natural follow-on to (3); A/R triage verbs every accounts team expects.

Total Phase-3 inclusion budget (rough): **3–4 weeks of backend** for items 1–4, slottable across Weeks 9–12 alongside the planned self-host + migration + cutover work.

#### Defer to v2 (with rationale)
- **Subscription Schedules / Phases (2.1)** — sales-led contract primitive, doesn't serve the wedge. Item-scoped pending changes are sufficient for AI-native upgrade flows. Revisit only if a design partner specifically asks for ramp pricing across phases. Needs RFC then.
- **Arbitrary `billing_cycle_anchor` (2.2c)** — handle in the Week 11 Stripe importer for migrated cohorts; don't add it as a top-level subscription knob.
- **Customer Balance free-floating ledger (2.4)** — prepaid `credit` ledger covers the AI-native pattern; the "auto-apply debit/credit to next invoice" pattern is generic A/R. Needs RFC if asked for.
- **Promotion Codes (2.5)** — coupon system is already richer than most teams need. The Coupon↔Code split is marketing infrastructure for B2C, not the AI-native buyer.
- **Quotes (2.9)** — explicit positioning non-goal. Don't reverse.
- **Revenue Recognition (2.10)** — explicit positioning non-goal. BYO warehouse.
- **Tax Rates manual catalogue (2.11)** — would be a footgun without jurisdiction logic. Tenants who need accuracy pick `stripe_tax`; tenants who need flat-rate get the existing `manual` provider.
- **Standalone `InvoiceItem` resource (2.12b)** — `AddLineItemAtomic` to draft covers ~all real usage; orphan-item-then-attach is a power-feature with low demand.

---

## RFC-fork findings (escalation surface)

Three places where a Phase-3 inclusion item warrants its own design doc rather than inline implementation:

1. **`docs/design-collection-method.md`** (item 2.13a) — `send_invoice` + `days_until_due` + dunning-reminder-vs-retry split + `paid_out_of_band` verb. Touches subscription, invoice, dunning, and hosted-invoice. The single biggest Phase-3 inclusion item.
2. **`docs/design-proration-behavior.md`** (items 2.2a/2.2b) — small RFC, mostly to lock the wire shape before threading it through `Service.UpdateItem`, the operator dashboard, and the importer-defaults. ~1 page.
3. **`docs/design-payment-links.md`** (item 2.8) — if Week 8 stretch is taken. How does it relate to Recipes? (Likely: "a Payment Link is a thin shell over a Recipe instance.") How does it relate to Hosted Invoice URL? (Different surface — Payment Link is product/SKU front door, Hosted Invoice URL is single-invoice debt page.)

---

## Findings to surface to the user

1. **Velox's prepaid `credit` ledger is more sophisticated than Stripe Customer Balance for the AI-native usage-credit pattern.** Don't reflexively port Customer Balance — it's a generic-SaaS primitive that doesn't serve the wedge, and the existing ledger covers "$1k of GPT-4 quota" cleanly.
2. **Item-scoped pending plan changes are *finer*-grained than Stripe Phases for the multi-item case** — but coarser than Phases for ramp pricing across multiple stages. The composition gap is the Schedules gap, but it's wedge-low.
3. **`pause_collection` migration `0052` already documents its own gap** in a code comment (`v1 supports only behavior='keep_as_draft'; mark_uncollectible/void` …). The next pass on pause should finish the comment.
4. **`AddLineItemAtomic` already ships** — the `add_lines` endpoint is half there. Polish to match Stripe's `POST /v1/invoices/{id}/add_lines` wire shape would close that part of the gap cheaply.
5. **`collection_method=send_invoice` is the single largest production-cutover risk.** B2B AI buyers' enterprise customers will require NET-30 invoicing, not auto-charge. This is a Phase-3 must, not a defer.
6. **No bug found.** Investigation surfaced one piece of self-documenting incomplete migration (item 3) but no incorrect behavior in shipped code.

---

## Verification notes

- All Velox citations verified via `Grep` or `Read` against the worktree at `7adf96b`. Where a function or type was named, file path + approximate line is given.
- All Stripe claims verified via `WebFetch`. URLs that 404'd:
  - `https://docs.stripe.com/billing/quotes` (used `https://docs.stripe.com/quotes` + `https://docs.stripe.com/api/quotes` instead).
  - `https://docs.stripe.com/api/payment_links` (used `https://docs.stripe.com/payment-links/api` + `https://docs.stripe.com/api/payment_links/payment_links` instead).
  - `https://docs.stripe.com/billing/customer/credit-grants` (404; `Customer.balance` covered via the customer object docs and customer balance docs which did fetch).
  - `https://docs.stripe.com/billing/subscriptions/pause` (404; covered via `pause-payment` URL).
  - `https://docs.stripe.com/api/credit_grants` (404; not load-bearing for v1).
  - `https://docs.stripe.com/billing/invoices/connect` (404; `send_invoice` covered via `billing/invoices/sending`).
- Where Stripe's docs partially answered (e.g., the truncated PaymentLinks fields list), the field set listed is what the doc surfaced — not extrapolation from memory.
