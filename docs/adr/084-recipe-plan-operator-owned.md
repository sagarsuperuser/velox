# ADR-084: The recipe plan is operator-owned — the recipe holds no standing claim on plan money-config

**Date:** 2026-07-07
**Status:** Accepted. **Supersedes ADR-083** (recipe adoption conformance gate) for the plan.

## Context

ADR-083 (#407) added a conformance gate: when `recipe.Instantiate` found an existing plan by code, it verified the plan matched the recipe's declared spec and **refused** on divergence, to stop silently billing under a plan the operator never declared. That fixed the *silent* symptom — but it kept spawning machinery. The gate couldn't distinguish an operator's legitimately-edited *own* plan (case 3) from a foreign code collision (case 4), so it required a deferred **provenance** system (`source_recipe_key`) to tell them apart. That accretion — silent bug → gate → provenance → case-3/4 ambiguity → uninstall-keeps-objects dance — is the classic wrong-model signal (memory `feedback_complexity_accretion_smell`).

The root cause: the recipe **conflates two kinds of object it treats identically but shouldn't**:

- **Versioned reference data the recipe fully declares and that never drifts** — the `tokens` meter (incl. its aggregation), the 35 per-`{model, token_type}` rating rules, their pricing-rule bindings. Adopting these by key, and on reinstall landing rating rules as the key's *next version* (ADR-070) so live subs keep their pinned version, is correct.
- **Operator-owned business config the operator tunes** — the plan's base fee and timing. The recipe seeds a starter plan (base_amount_cents:0, in_arrears default), the operator edits it (e.g. in_advance $99), and the recipe then treats that edit as "drift from my spec." But the recipe never had a legitimate ongoing claim on the plan's money-config.

Industry confirms the split: Stripe prices are immutable (change = new price + migrate); Orb plan versions "never affect existing subscriptions — you migrate them"; Metronome rate-card edits propagate only if you configure them to. Plans/prices are **operator-owned, versioned config**, never a template-reconciled spec. Velox's rating-rule versioning already matches this; only the plan was off-model.

## Decision

**The recipe holds a standing claim only on the reference-data catalog it fully declares. It makes no claim on the plan, which is operator-owned from the moment it exists.** Remove the phantom plan spec and the drift/conform/provenance/case-3/4 class dissolves — deletion, not more guards.

- **Meters** — adopt by key. The one narrow **aggregation** conformance check (ADR-083's meter half) is **kept** on its own footing: aggregation (sum/max/count/last) is recipe-declared reference data that never drifts and is billing-consulted (engine `deferredBucket`/`mapMeterAggregation`), so a same-key meter with a divergent aggregation genuinely mis-rolls-up usage → refuse (409). This is a real reference-data conflict, not plan provenance.
- **Rating rules** — unchanged: adopt by `rule_key`, land as next version on reinstall (ADR-070), never repriced.
- **Plan** — no conformance, no refusal, no provenance.
  - **Absent** → create the starter plan, now honoring two new instantiate params `base_amount_cents` + `base_bill_timing` (defaults 0 / in_arrears preserve today's behavior). This makes the common case — a monthly in_advance $99 plan — a **single call**, removing the mandatory follow-up PATCH the $0/in_arrears-only starter forced.
  - **Present** → **leave it untouched, record its real id, and report it** in the response (`warnings`): "plan X already existed and was used as-is (base fee …, timing …)"; and if the caller supplied `base_amount_cents`/`base_bill_timing` that could not be applied, say so explicitly. An operator-edited own plan and a foreign collision get the **identical** treatment — so the distinction provenance existed to draw is irrelevant and evaporates.
- **The recipe never mutates a plan/rule/meter** under any path (creates of not-yet-existing objects only), so live-sub safety holds trivially.

**The ownership principle:** the recipe gates only what it *fully declares and that never drifts* (meter aggregation); it makes *no claim* on what the operator *tunes* (plan money-config).

### Why reuse-and-report, not refuse

Refusing (ADR-083) was a real improvement over silently shipping wrong config — but it fixes the *silent* symptom, not the wrong *operation*: reconciling an object the operator owns is the thing that shouldn't happen. Removing that operation removes the whole class. Reuse-and-report is **not** the old silent bug: the old bug returned success while *hiding* the divergence; here the response echoes the plan's *actual* config truthfully and flags "params not applied." Transparency, not silent substitution.

## What changes vs ADR-083 (#407, already on main)

- **Plan half removed:** delete `planConformanceDiff`, `effectiveTiming`, the plan set-comparison helpers, the plan refuse branch, and the `domain.Plan` reflection drift-guard test.
- **Meter half kept:** `meterConformanceDiff` (aggregation-only) survives standalone as reference-data conformance.
- **Provenance never built** — `created_objects` keeps IDs for the dashboard badge / deep-links only, never for ownership adjudication.

## Alternatives considered

- **Keep the gate + build provenance (finish ADR-083).** Rejected — adds a schema column + case-3/4 logic to *guard* the mismatch instead of removing it. More machinery.
- **Catalog-only recipe (the plan leaves the recipe entirely; operator builds plans on a shared catalog).** The purest form, but it drops the one-call ready-to-subscribe value and needs a new "plan from catalog" surface. **Deferred** with a named trigger: the first recipe that needs >1 plan, or a design partner wanting several products on one shared pricing catalog. The operator-owned-plan model doesn't block it — the single starter plan simply becomes "the first plan you build on the catalog."

## Consequences

- The plan drift/conform/provenance/case-3/4 machinery is **deleted**; the change is net-subtractive (no schema, no migration).
- One-call plan creation with base fee + timing (kills the PATCH).
- **Tradeoff (softness):** if the caller supplies base-fee params but a plan with the code already exists, the existing plan is reused and the params are reported-not-applied (a machine-readable `warnings` entry), not applied. At zero customers with a recipe-specific default code, the collision surface is tiny; the report keeps it non-silent. Stated here as the deliberate simplicity/correctness trade.
- Asymmetry (meter aggregation gated, plan not) is intentional and follows the ownership principle above — stated so it doesn't read as inconsistent.

## References
- Supersedes ADR-083; builds on ADR-070 (rating-rule reconnect by version), ADR-031 (base_bill_timing).
- Memory: `feedback_complexity_accretion_smell` (this arc is its second worked example), `feedback_no_silent_fallbacks`, `feedback_no_overengineering`.
- Industry: [Stripe prices](https://docs.stripe.com/products-prices/how-products-and-prices-work), [Orb versions & migrations](https://www.withorb.com/blog/how-we-built-plan-versions-and-migrations), [Metronome rate cards](https://metronome.com/blog/rethinking-pricing-architecture-how-the-centralized-rate-card-model-unlocks-pricing-agility).
