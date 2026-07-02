# ADR-070: Price-change semantics — overrides follow rule_key, periods pin at open

**Date:** 2026-07-02
**Status:** Accepted (decisions locked Day 1 per the remediation plan; implementation = P4, last in the billing lane; designed by the P4 adversarial panel — 3 lenses, all SHIP-WITH-FIXES; consolidated protocol in plan §4.4)

## Context

Two product decisions gated P4 and P10:

1. **Override keying.** Customer price overrides are stored and looked up by
   `rating_rule_version_id`, but the single-rule cycle close resolves
   `GetLatestRuleByKey` and then fetches the override by the *latest* version
   id. Publishing v2 of a rule makes every v1-keyed override unfindable: the
   negotiated customer silently reverts to list price for the whole period at
   the next close — no event, no warning (audit High #3).
2. **Repricing semantics.** Nothing defined what a mid-period rate change
   *means*. Worse, resolution is split-brain today: cycle close prices at
   latest-by-key, while preview, the threshold scan, and customer_usage price
   at the meter-pinned version — so one period can bill at two different
   rates (threshold fires at v1, close bills the residual at v2), and
   preview ≠ invoice by construction.

## Decisions

1. **Overrides are keyed by `rule_key`** — a negotiated price follows the
   rule across version publishes. `rule_key` column added and backfilled via
   the rating_rule_versions join; `UNIQUE (tenant_id, customer_id, rule_key)`
   replaces the version-id unique. The backfill collapses collisions
   deterministically (an operator who re-created an override after a publish
   holds rows against v1 AND v2): highest rule version wins, tie-break latest
   `updated_at`; losers flip `active=false` and are kept for audit, each
   demotion logged. The API keeps accepting `rating_rule_version_id` and
   resolves it to `rule_key` at the service boundary. Copy-forward
   (re-writing overrides onto each new version) is rejected: it multiplies
   writers and re-creates the detach bug on every miss.
2. **Repricing is PINNED, never retroactive: resolve-as-of-period-start.**
   The rating-rule version in force when a billing period *opened* prices
   the entire period, everywhere — cycle close (single-rule and multi-dim),
   both cancel paths, preview, threshold fires, customer_usage. A publish
   takes effect at the next period open. Already-consumed usage is never
   repriced by a catalog change. One shared resolver; the split-brain
   pinned-vs-latest mix is removed, which also makes mid-close publishes
   harmless (resolution becomes time-invariant for the period — no
   snapshot-consistency locking needed).
3. **The same effective-next-period rule binds ALL rate-state writers** —
   version publish, override upsert, override deactivate. If version
   publishes were next-period but override edits stayed
   retroactive-at-close, the spend-cap coherence argument collapses (the
   threshold scan compares `capRunning` at old rates, close bills at new).
4. **Resolver mechanics:** as-of resolution keys on an explicit
   `effective_from` stamped via `clock.Now(ctx)` — `createRatingRuleTx`
   stamps wall-clock today, which would resolve sim-time periods against
   wall-clock publish stamps under test clocks (ADR-030) — and gates on
   `lifecycle_state` (an archived or draft version must never be "latest").
5. **An override freezes PRICE, not rule semantics.** `ToRatingRule` is
   replaced by patch-the-resolved-rule: the resolved version's `ID`,
   `RuleKey`, `Currency`, `Name` survive; only pricing fields
   (mode/tiers/amounts) are swapped. This differs from Lago's full-immunity
   model (subscription immune to all plan edits) deliberately: non-pricing
   semantics of new versions still flow to overridden customers. Corollary
   guard: a publish that *changes currency* within a rule_key is rejected
   while active overrides reference that key — a rule_key-following override
   would otherwise silently reinterpret its integer cents in the new
   currency.
6. **Resolution failures are loud.** `errs.ErrNotFound` from the override
   lookup means "no override — list price". Any other error aborts the
   close/preview and is returned; today all five engine lookup sites treat
   any error as "no override" and a DB blip silently bills list price
   (violates no-silent-fallbacks).
7. **Pin-at-consumption is REJECTED**, not deferred: per-event version
   stamping is a heavier model (per-event rate joins, unbounded backfill
   semantics) that no current customer shape demands; period-start grain is
   the v1 semantics. Nothing spills to backlog from this decision.

## Consequences

- P10's `customer_usage.go:401` slice implements override lookup by
  `rule_key` with the same as-of-period-start resolution, citing this ADR.
- Recipe reinstall (`recipe/service.go:452`) allocates the next version in
  SQL instead of hardcoding `Version:1`; version allocation generally moves
  into SQL (`MAX(version)+1` with 23505 retry) so concurrent publishes stop
  409ing spuriously.
- Mid-period override deactivation (new DELETE endpoint) takes effect at the
  next period open, same as every other rate change.
- Full test matrix in plan §4.4 item 11 (detach regression both close paths,
  preview==invoice parity across a publish, fire-then-publish-then-close
  version agreement, zero-base all-override threshold fire, transient-error
  loudness, migration collision collapse).
