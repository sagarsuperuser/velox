# Velox Positioning

> Strategic positioning doc. Living. Last revised 2026-04-25.

## One line

**The open-source billing engine for AI and usage-heavy SaaS, that runs in your own VPC.**

## The wedge

Two market truths that Stripe Billing structurally cannot serve:

1. **AI/LLM apps need multi-dimensional pricing.** Real model pricing today is `model × operation × cached × tier × context-window`. Stripe's Meter API was bolted onto a card-subscription engine and forces one Meter per dimension combination — six Meters and ugly subscription wiring just to model GPT-4 input/output/cached/uncached. Velox treats dimensions as first-class on the meter.
2. **Regulated tenants cannot put customer billing data on Stripe's servers.** EU GDPR-strict, India RBI data-localization, healthcare-adjacent SaaS, government procurement. Stripe can never offer this; their whole model is "send us the data."

Velox owns the layer above PaymentIntent. Stripe still does cards + tax computation under the hood. The 0.5% Stripe Billing fee disappears.

## Target customer

- **Stage:** Series A–C, $1M–$50M ARR
- **Product shape:** usage-heavy (AI inference, vector DBs, dev infra, observability, API platforms)
- **Trigger:** outgrowing Stripe Billing's modeling limits, **or** compliance review blocked Stripe Billing, **or** monthly Stripe Billing fees crossed $5K
- **Buyer:** VP Eng or staff/principal engineer. Technical decision, not procurement-led.

## Anti-positioning (what Velox is NOT)

State this loudly so the wrong customers self-select out:

- **Not for vanilla card-first SaaS** with simple per-seat pricing — Stripe Billing is fine for them
- **Not multi-PSP today** — Stripe is the only payment processor; Razorpay/Paystack/Adyen come when a paying tenant asks
- **Not for marketplaces / Connect** — single-tenant per Velox deployment
- **No Revenue Recognition / Sigma** — bring your own warehouse + dbt
- **No Quotes / Subscription Schedules yet** — sales-led contract billing should pick Recurly or Maxio
- **No 50+ payment method types** — cards via Stripe + send-invoice. ACH/SEPA/etc. as the wedge expands

## Three pillars

### 1. AI/usage-native
- **Multi-dimensional meters** — events carry `{meter, customer, dimensions: {model, operation, cached, tier}, value, timestamp}`; pricing rules match dimension subsets
- **High-throughput Postgres ingest** — partitioned `usage_events`, target sustained 100k events/sec on commodity hardware
- **Pricing recipes** — one-call instantiation: `recipe="anthropic_style"` creates products + prices + meters + dunning
- **Embeddable cost dashboards** — your customers can show their end users "you've used $4.31 of GPT-4 today" without building it from scratch

### 2. Self-host first
- **Helm chart + Docker Compose + Terraform examples** — runs in your VPC in ≤1 hour
- **Data sovereignty** — customer billing data never leaves your infrastructure
- **Append-only audit log** — DB-trigger enforced tamper-evidence
- **RLS multi-tenancy** — one Velox deployment can serve N internal business units cleanly

### 3. Stripe-grade primitives
Already shipped, do not rebuild:
- Test clocks · idempotency · hosted invoice page · branded multipart emails · dunning with breaker · transactional outbox · webhook signing with 72h rotation grace · trial state machine with atomic flips · scheduled cancellation · pause-collection

## Why now (2026)

- AI infrastructure spend hit $20B+ in 2026 with usage-based pricing the dominant model; existing billing tools weren't designed for it
- Lago raised $19M Series A on generic OSS billing; the wedge underneath them (AI-native + sovereign) is empty
- EU AI Act, India RBI data-localization, and stricter HIPAA/SOC2 audits are all pushing buyers toward self-host
- Mid-market SaaS doing $5M+ ARR are starting to feel Stripe Billing's 0.5% fee as a real line item

## Competitive frame

| | Velox | Stripe Billing | Lago | Orb / Metronome | OpenMeter |
|---|---|---|---|---|---|
| OSS / self-host | ✅ | ❌ | ✅ | ❌ | ✅ |
| AI-native pricing | ✅ | ❌ | ⚠️ generic | ⚠️ closed source | ⚠️ metering only |
| Full billing engine | ✅ | ✅ | ✅ | ✅ | ❌ |
| Stripe-grade primitives | ✅ | ✅ | ⚠️ | ✅ | ❌ |
| Pricing | OSS / cloud TBD | 0.5% of GMV | OSS / cloud | $30K+/yr | OSS |
| Data sovereignty | ✅ first-class | ❌ | ⚠️ underdocumented | ❌ | ✅ but no billing |

Velox lives in the empty cell: **OSS + self-host + AI-native + full billing engine**.

## Buyer triggers (when does someone reach for Velox?)

- Stripe Billing fees crossed $5K/month
- Pricing model the product team wants doesn't fit Stripe's Meter API (multi-dimensional, percentile-based, hierarchical credits)
- Compliance review blocked Stripe Billing (data residency, audit retention, PHI proximity)
- Need to white-label billing for marketplace tenants without going Connect
- Engineering team wants to own the billing layer the way they own auth or storage

## Go-to-market sequence

1. **OSS launch** on GitHub with strong README + 5-minute quickstart + AI billing recipe library
2. **Design partner program** — 3 SaaS in production by day 90, 12 months free, co-branded case study
3. **Content** — "AI billing patterns" blog series, self-host playbooks, "migrating from Stripe Billing" guide
4. **Conferences** — KubeCon (self-host angle), AI Summit / NeurIPS-adjacent (AI-native angle), Strangeloop, Postgres Conference
5. **Cloud** — defer until 2 design partners are in production; OSS first, hosted later

## Open positioning questions

- Brand voice: developer-technical and anti-marketing-speak (proposed)
- Pricing for hosted: usage-based itself? Fixed tier? Defer until OSS traction is real (proposed)
- Lead vertical for outreach: AI inference platforms first, vector DBs second, dev infra third (proposed)
- Whether to publicly compare to Lago by name in marketing copy (proposed: no — let buyers triangulate)
