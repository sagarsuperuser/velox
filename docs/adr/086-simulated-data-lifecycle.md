# ADR-086: Simulated-data lifecycle — one durable discriminator + clock-delete teardown

**Status:** Accepted (2026-07-09). Phase 1 shipped (#417); Phases 2–4 pending.
**Supersedes:** ADR-016 (test-clock soft-delete + cancel-subs + detach-customers → **hard-delete cascade**).
**Extends:** ADR-029 (fully-disjoint test-clock flows — now *enforced* by a durable discriminator + an arch-test, not case-by-case predicates).
**Reaffirms:** ADR-030 (simulated time on clock-pinned entities stays; `created_at` is domain time) **and its audit exception** (the audit log is always wall-clock).

## Context

In **test mode** (`livemode=false`), two time domains share one dataset: **wall-clock** (real-test data + every cron sweep on `time.Now()`) and **simulated** (test-clock-pinned entities whose billing timestamps are stamped at the clock's `frozen_time`). An adversarial audit found **24 defects** where a wall-clock plane (auto-charge, dunning, credit-expiry, usage-billing, analytics, ~8 FE surfaces) processes a *simulated* record against wall-clock time. All are test-mode-only (no live-money impact), but they violate ADR-029 invariants, corrupt demos/QA, and lie in the UI.

## Root cause — one defect, 24 symptoms

Every plane decides *"is this simulated?"* by reading the **mutable pin** (`test_clock_id` on the customer/subscription), not a **durable fact**. Deleting a clock nulls/orphans the pin while the simulated timestamps remain, so the "skip me, I'm simulated" signal vanishes and the wall-clock planes grab the rows. Aggravating: the durable flag is **inconsistently present** — `invoices` and `credit_notes` carry `is_simulated`, but `usage_events`, the credit ledger, `subscriptions`, and `invoice_dunning_runs` do not.

This ADR fixes the **root**, not the 24 leaves.

## Decision — the model

1. **Immutable birth-stamp.** Every sim-bearing row carries `sim_clock_id` (written **once** at creation, auto-stamped from a session var via a column `DEFAULT`, exactly as `livemode` is auto-set today — writers never name it). `is_simulated` becomes a **generated column** `GENERATED ALWAYS AS (sim_clock_id IS NOT NULL) STORED` — one source of truth, no dual-write, no drift, un-nullable.

2. **The pin is demoted.** `customers.test_clock_id` / `subscriptions.test_clock_id` become pure *"which plane does this entity's **next** write land in"* pointers with **zero operational readers**. Because treatment keys on the immutable stamp, **detaching the pin can never change how an existing row is treated.**

3. **Every operational plane keys on `is_simulated`, uniformly.** Wall-clock plane processes `is_simulated = false`; the catchup/Advance plane processes `is_simulated = true AND sim_clock_id = $clock` against that clock's `frozen_time`. The money sweeps run under `TxBypass` (which skips RLS), so they use **explicit predicates** — an RLS-only design would falsely claim safety over the exact paths where money moves.

4. **Clock-delete = teardown.** A single guarded `DELETE FROM test_clocks`; foreign keys do the rest. `sim_clock_id … REFERENCES test_clocks(id) ON DELETE CASCADE` on every sim-bearing table evaporates all of that clock's simulated entities, **including the customer** (`customers.test_clock_id … ON DELETE CASCADE` — the customer was born into the sandbox and goes with it; the Stripe test-clock model). The cascade is keyed on **`sim_clock_id`, which is `NULL` on every live row**, so live financial rows are structurally unreachable and migration-0015's `RESTRICT` guards stay intact. The old soft-delete + cancel-subs + detach dance (ADR-016) is gone; `canceled_at = NULL` phantom-MRR dissolves because the subs are deleted, not canceled.

5. **No physical/domain time split.** `created_at` / `updated_at` stay simulated (`clock.Now`) on clock-pinned entities — **reaffirming ADR-030.** We deliberately do **not** rewrite the ~148 `clock.Now` write sites, because:
   - The **wall-clock forensic layer already exists**: the audit log stamps `created_at = time.Now().UTC()` unconditionally (ADR-030 audit exception, `internal/audit/audit.go`), carrying the simulated instant as `sim_effective_at` metadata. "When did this *really* happen?" is answered there.
   - Investigation confirmed the only **wall-clock-semantic** readers of entity `created_at` are (a) `analytics/overview.go` count metrics — gated on `is_simulated` in Phase 2 — and (b) the pricing as-of resolution (see Known limitations). Everything else is display, stable-cursor, or ordering, where sim-time is harmless. In production there are no clocks, so `created_at` is already wall-clock everywhere.

## The anti-regression guards — why this dissolves the class instead of playing whack-a-mole

The model alone isn't enough; a future engineer must be *unable* to reintroduce the defect. Mechanized, not documented:

- **Sweep-filter arch-test** (sibling to `internal/arch/boundaries_test.go`): any store/sweep `SELECT` over a sim-bearing table must carry an `is_simulated` predicate, with an explicit allowlist for the catchup `…ForClock` paths (filter the other way) and single-row `GET`s (plane-neutral). Honest caveat: "is this a wall sweep" isn't perfectly decidable, so this is an allowlist-backed gate, not airtight — escalation is the RLS third-axis, deferred below.
- **Write-chokepoint** — sim-capable writes funnel through one `WithSimulation(clockID)` / `OpenCustomerWriteTx(customerID)` site that resolves the pin and sets the session var; an arch-test asserts every creation door goes through it, **including the LiteLLM ingest door** (the #406 lint-scope lesson).
- **Schema-completeness test**: every sim-bearing table has `sim_clock_id` + generated `is_simulated` + `ON DELETE CASCADE`.
- **FE**: the relative-time helper takes a **required** anchor param, so a simulated row's badge *cannot* fall back to `Date.now()` (no default to fall back to).

## Planes — the complete list (nothing hidden in a follow-up)

| Plane | Today | Target predicate | Phase |
|---|---|---|---|
| auto-charge `ListAutoChargePending` | pin-join | `is_simulated = false` | 1 ✅ |
| dunning `ListDueRuns`, `ListFailedWithoutDunningRun` | pin-join | `is_simulated = false` | 1 ✅ |
| tax `ListPendingTaxRetry` / `ListPendingTaxCommit` | pin-join | `is_simulated = false` | 2 |
| credit `ListExpiredGrants` + drain aggregation | customer-pin | `is_simulated = false` (needs `sim_clock_id` on the ledger) | 2 |
| usage `AggregateForBillingPeriod` family | none | match the aggregating entity's discriminator (needs `sim_clock_id` on `usage_events`) | 2 |
| analytics `overview.go` (`new_customers`, failed-invoice, dunning counts) | none | `is_simulated = false` | 2 |
| **audit — entity creation** (currently only update/rotate emit rows) | not audited | emit a wall-clock create audit row | 2 |
| FE relative-time / Due / Expiry / cycle badges | mutable pin → wall-clock fallback | `is_simulated` + server `effective_now` | 4 |

## Phased build

- **Phase 1 — SHIPPED (#417).** Auto-charge + dunning sweep gates on `is_simulated`. No schema; independently mergeable; closed the ADR-029 one-off leak.
- **Phase 2.** `sim_clock_id` + generated `is_simulated` + auto-stamp `DEFAULT` + `CHECK (NOT (is_simulated AND livemode))` + partial indexes on the four tables that lack it; convert `invoices`/`credit_notes` bools to generated; flip tax/credit/usage/analytics to the uniform rule; audit entity creation; the write-chokepoint + the arch-tests. Migration (next number, verified across branches; pre-launch → reset the `livemode=false` dataset, no backfill).
- **Phase 3.** `ON DELETE CASCADE` on `sim_clock_id`, `ON DELETE CASCADE` on `customers.test_clock_id`; replace `testclock.Delete` with the guarded hard `DELETE`; schema-completeness + sweep arch-tests. Makes the entire *deleted-clock* half structurally impossible.
- **Phase 4.** API ships `is_simulated` + `effective_now` per row; the FE relative-time helper takes a required anchor.

## Known limitations & deferred (each with a trigger)

- **No `created_at` time-split** (§5) — trigger to revisit: a compliance/debugging need for wall-clock timestamps on *test* data beyond what the audit log provides.
- **Pricing `GetRuleByKeyAsOf`** compares a rule's wall-clock `created_at` against a sim `asOf` (`pricing/postgres.go:126`). Correct for forward-advanced clocks and single-version rules (≈all usage) and it never errors (fallback to the earliest active version); a clock anchored *behind* a version's real creation time, combined with a *multi-version* rule, prices with an older version. **Documented, not fixed** — trigger: an operator hits it on a real simulation.
- **RLS third axis** — promote `is_simulated` to a fail-closed RLS conjunct on the `TxTenant` read/analytics plane if a forgotten-filter leak ever recurs *after* the arch-test ships. The `TxBypass` sweeps stay on explicit filters regardless (RLS can't reach them).
- **Post-teardown forensic grouping** — once rows cascade away, "which clock produced this" is unanswerable; acceptable by design. Trigger: a named audit need → capture a summary audit row at teardown before the cascade.

## Implementation refinements (as built, 2026-07-09)

- **Stamp mechanism: explicit at the writer, not a session-var `DEFAULT`.** §1 favored auto-stamping `sim_clock_id` from an `app.sim_clock_id` session var (the `livemode` pattern). In practice `sim_clock_id` depends on the *customer's* clock, resolved per-write, and the clock id isn't reliably on `ctx` at write time (the invoice service already avoids `clock.IsSimulated(ctx)` for this reason and looks the customer up). So each writer stamps `sim_clock_id` explicitly — for `customers` it's the `test_clock_id` supplied at creation; for customer-scoped rows (usage, credit) it's a shared resolver of the customer's pin, mirroring the proven `customerOnTestClock` → `is_simulated` write. The stamping discipline is guarded by the schema-completeness + write-chokepoint arch-tests (a new sim-bearing table/writer that forgets the stamp fails the build).
- **FK deferred to Phase 3.** `sim_clock_id` is a plain `text` column in Phase 2 (the durable discriminator only). The `REFERENCES test_clocks(id) ON DELETE CASCADE` lands in Phase 3, where the teardown cascade needs it — so the Phase-2 migrations stay additive and cleanly reversible.
- **`customers` joins the sim-bearing set.** The plane table's tables were the billing rows; `customers` also needs the durable flag, because the analytics `new_customers` / `active_customers` metrics must keep excluding a simulated customer *after* its clock is deleted (the pin is nulled then). Migration 0143 adds it.
- **Analytics gated per-plane-slice.** Customer metrics gate on `customers.is_simulated` (0143); invoice metrics (revenue, paid/failed/open counts) gate on the existing `invoices.is_simulated`; the subscription + dunning-recovery metrics gate once those tables gain the discriminator — each in its own slice.

## References

- ADR-016 (superseded), ADR-029 (extended), ADR-030 (reaffirmed, incl. audit exception), ADR-070 (rule-version as-of), ADR-027 (customer-level pin at creation).
- The 24-finding clock-delete-detach audit and the four-candidate design panel (2026-07-08/09) that this consolidates.
