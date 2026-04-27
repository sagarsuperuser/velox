# Why Stripe Billing's Meter API doesn't fit AI workloads

> Published 2026-04-25. By the Velox team.
> Code samples are real; the API contracts referenced are
> Stripe's [Billing Meter](https://docs.stripe.com/api/billing/meter) and
> Velox's [multi-dim meters RFC](../design-multi-dim-meters.md).

If you bill an AI product — inference, embeddings, agentic workflows, vector search — you've probably tried to model your pricing inside Stripe Billing and noticed something is wrong. You start with one Meter for "tokens." Then you add another for "output tokens" because input and output bill at different rates. Then "cached input" because Anthropic prompt caching is 90% cheaper. Then "batch" because batch pricing is half. Three models in your catalog, and you're suddenly at twenty-something Meters, each tied to its own Price and its own Subscription Item, before a single customer is on the system.

The reason isn't that Stripe shipped a bad API. The reason is that the Meter API was bolted onto a card-subscription engine designed in 2014 for SaaS seats. AI usage doesn't look like seats — it looks like a five-dimensional cube — and the Meter object simply doesn't have the shape to express that cube.

This post walks through the structural mismatch, shows what the wiring actually looks like, and describes how Velox treats dimensions as first-class on the meter so you go from N Meters back down to one.

---

## What a Stripe Meter actually is

Stripe's [Meter object](https://docs.stripe.com/api/billing/meter/object) has these fields:

```
id, object, created, status, livemode
event_name              // the string events must match
default_aggregation     // one of: sum, count, last
event_time_window       // day | hour
value_settings          // which payload key holds the numeric value
customer_mapping        // how to extract the customer id from the event
status_transitions, updated
```

Three things are pinned at meter-creation time:

1. **The event name.** A meter listens for one and only one `event_name`. Events sent under any other name simply do not aggregate against it.
2. **The aggregation formula.** [`default_aggregation.formula`](https://docs.stripe.com/api/billing/meter/object) is one of `sum`, `count`, or `last`. It's a property of the meter, not the price, and there's no way to apply two different aggregations to the same event stream.
3. **Where the numeric value lives.** `value_settings.event_payload_key` names a single key inside `payload` that holds the number to aggregate. One key per meter.

Meter Events are posted to [`POST /v1/billing/meter_events`](https://docs.stripe.com/api/billing/meter-events/create) with a payload of the form:

```json
{
  "event_name": "tokens",
  "timestamp": 1714000000,
  "payload": {
    "stripe_customer_id": "cus_abc",
    "value": "1234"
  }
}
```

The payload is opaque to billing. You can put extra metadata in there, but Stripe Billing doesn't read it. Aggregation is `SUM(payload.value) WHERE event_name = $meter.event_name AND payload.stripe_customer_id = $customer_id`. That's it. You don't get to filter by other payload keys.

A Price is then created with `recurring.usage_type = metered` and `recurring.meter = mtr_xyz`, and a Subscription Item attaches that Price to a customer. The Subscription Item is the unit that gets billed. **Each rate cell in your pricing matrix wants its own Price, which wants its own Meter, which wants its own Subscription Item.**

That's the load-bearing fact. The rest follows from it.

---

## What this means for AI pricing

[Anthropic's published pricing](https://docs.anthropic.com/en/docs/about-claude/pricing) for a single model splits across at least four dimensions:

- **Model** — Opus 4, Sonnet 4, Haiku 4 (and prior generations still in customer catalogs)
- **Operation** — input tokens vs. output tokens
- **Cache state** — base input, prompt-cache write, prompt-cache read
- **Batch mode** — real-time vs. batch (50% discount)

Multiplied through for one model: 1 × 2 × 3 × 2 = **12 distinct rate cells**. For a catalog of three live models that's **36 cells**. OpenAI's catalog has similar shape. Most billing teams I've talked to who serve other AI customers run between 16 and 60 cells, depending on how many models they expose and whether they pass through cache and batch tiers.

Now wire that on Stripe Billing. Each cell needs:

- One **Meter** with a unique `event_name` (e.g., `tokens.opus4.input.uncached.realtime`) and a fixed `sum` aggregation
- One **Price** referencing that meter at the cell's rate
- One **Subscription Item** on every customer's subscription pinning that price

If you have 36 cells and 5,000 active customers, that's:

- 36 meters in your account
- 36 prices in your catalog
- **180,000 subscription items** that have to exist before any usage gets billed correctly

Your ingest path also has to know which `event_name` to send. The dimension structure that lives naturally in your application — `{model: "opus4", operation: "input", cached: false, batch: false}` — has to be flattened into a string like `tokens.opus4.input.uncached.realtime` before crossing the Stripe boundary, then is unrecoverable on the other side. You can't ask Stripe "how many tokens did this customer use across all Opus models?" — that aggregation doesn't exist as a first-class object. You'd have to fetch each meter individually, or maintain the same data in your own warehouse.

If you ever change the rate-card structure — collapse a cell, split a cell, add a new model, sunset an old one — every customer's subscription item set has to migrate. Stripe gives you [Subscription Schedules](https://docs.stripe.com/api/subscription_schedules) to phase that, but the operational cost is real.

This is what people mean when they say "Stripe Billing wasn't built for AI." The API is fine for what it is — a clean, well-documented, financially correct usage-bill primitive — but its unit of composition is a meter+price+subscription-item triple, and AI pricing wants the unit of composition to be a single dimension on a single event.

---

## What changes when dimensions are first-class

Velox's [multi-dim meters design](../design-multi-dim-meters.md) takes the inverse position. There is **one meter per usage type** — `tokens`, `requests`, `gb_hours` — and events carry dimensions inline:

```json
POST /v1/usage-events
{
  "event_name": "tokens",
  "external_customer_id": "cus_acme",
  "quantity": "12450",
  "dimensions": {
    "model": "opus4",
    "operation": "input",
    "cached": false,
    "batch": false
  },
  "timestamp": "2026-04-25T12:34:56Z"
}
```

`dimensions` is JSONB. No schema enforcement at v1; whatever your application emits, Velox stores. The `value` is `NUMERIC(38, 12)`, so you can bill fractional GPU-hours and partial tokens without precision games.

Pricing is then expressed as **rules attached to the meter**, each with a `dimension_match` and an `aggregation_mode`:

```json
POST /v1/meters/mtr_tokens/pricing-rules
{
  "rating_rule_version_id": "rrv_opus4_input_uncached",
  "dimension_match": {
    "model": "opus4",
    "operation": "input",
    "cached": false,
    "batch": false
  },
  "aggregation_mode": "sum",
  "priority": 100
}
```

`dimension_match` uses subset semantics — `{model: "opus4"}` matches every event tagged with that model regardless of other dimensions, and a more specific rule (`{model: "opus4", cached: true}`) at higher priority claims its events first. Each event is claimed by exactly one rule per (customer, period) so there's no double-counting. The rating rule version (`rrv_…`) holds the actual rate, and Velox's existing flat / graduated / package logic is unchanged — only the resolution layer is new.

This collapses the 36 cells from the Stripe wiring into:

- **1 meter** (`tokens`)
- **36 pricing rules** on that meter
- **1 subscription item** per customer

The dimension structure that lives in your application gets sent across the wire intact. You can ask "how many tokens did this customer use across all Opus models in April?" with a single grouping query — `GET /v1/customers/{id}/usage?event_name=tokens&group_by=model`. You can introduce a new model by adding one rule, not 12. You can deprecate a cell by removing one rule, not migrating every customer's subscription items.

---

## The aggregation-mode mismatch

There's a second, smaller structural mismatch worth flagging. Stripe Billing supports three aggregation formulas — `sum`, `count`, `last` — and pins the choice at the meter. Their legacy `Plan.aggregate_usage` enum (still surfacing on older subscriptions) supports five — `sum`, `last_during_period`, `last_ever`, `max`, `most_recent` — and also pins it.

The right shape is: **aggregation is a property of the rule, not the meter.** The same `tokens` meter might want `sum` for prepaid bundles, `max` for "peak concurrent usage" tiers, and `last_ever` for a seat-count-style usage that bills by current state regardless of when the event was emitted. Velox carries all five (`sum`, `count`, `last_during_period`, `last_ever`, `max`) per pricing rule.

This is squarely on Stripe's [Tier 1 gap list](https://docs.stripe.com/billing/migration/migrate-subscriptions-to-prices-and-meters#aggregation-modes) of "things the new Meter API doesn't yet match from the old Plan model." It's fixable in their model, but it isn't fixed today, and the per-rule placement is strictly more expressive even if Stripe ships the missing modes.

---

## What this gives you operationally

The shape change isn't only an API ergonomics win. Three operational benefits fall out of it that the Stripe Meter API can't provide cleanly:

**1. A real cost dashboard.** Once events carry dimensions, your application can render "you've used $4.31 of Opus today, $0.18 of Sonnet, $11.62 of Haiku" without running a separate analytics pipeline. Velox ships an embeddable React component (`<VeloxCostDashboard customerId={…} />`) that queries `/v1/customers/{id}/usage?group_by=model` and shows per-dimension burn against the current period's projected bill. This is the surface AI buyers are starting to expect — Anthropic, OpenAI, and Replicate all expose some form of it; building it on Stripe Meter requires you to mirror your own usage data into a warehouse and join it back.

**2. Mid-period rate changes without subscription-item churn.** Updating a rate on Stripe means swapping the Price on the Subscription Item, with all the proration handling that implies. In Velox you create a new `rating_rule_version`, point the relevant pricing rule at it, and set an effective-from date — the subscription itself never changes shape.

**3. Auditable dimension lineage.** Every event in `usage_events` keeps its full dimension payload. When a customer asks "why did I get billed $X for cached input?", you can answer them by re-running the aggregation query, not by reconstructing event names from logs.

---

## When Stripe Billing's Meter API is still the right tool

Anti-positioning matters. If your pricing is genuinely one-dimensional — per-seat SaaS, per-API-request without tiering, single-rate metered — Stripe Meter is fine and you should not be reading this post. Their implementation is solid, their docs are excellent, and the tax/regulatory and PCI scope you'd otherwise own goes away. The Meter API's structural limits don't matter if your pricing only has one dimension to begin with.

The decision point is the cardinality of your rate matrix. Below ~6 cells, Stripe is comfortable. Above ~12, you're fighting the API. Between, it depends on how often the matrix changes — if it's stable for years, the wiring tax is one-time; if you're iterating, it compounds.

---

## What we're building

Velox's multi-dim meter implementation lands in `v0.x` on **May 8, 2026**. The schema, API surface, aggregation semantics, and test plan are public in the [design doc](../design-multi-dim-meters.md). The endpoints in this post are the published contract — you can scaffold against them today.

If you run an AI product where the rate matrix is starting to outgrow Stripe Billing, or where regulatory pressure (EU GDPR-strict, India RBI data localization, healthcare-adjacent SaaS) means you can't keep customer billing data on Stripe's servers in the first place — we're looking for design partners. Twelve months free hosted access in exchange for a weekly check-in and a co-branded case study. Email `partners@velox.dev` or open an issue on [the repo](https://github.com/sagarsuperuser/velox).

Velox is open source under MIT. The wedge is small and clear: AI-native, self-host first, the layer above PaymentIntent.
