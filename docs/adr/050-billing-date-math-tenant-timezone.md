# ADR-050: Billing calendar date-math is anchored in the tenant timezone

- Status: Accepted
- Date: 2026-06-09
- Relates: ADR-042 (integer day-ratio proration), ADR-030 (simulated time / clock-pinned), the period-creation + proration paths

## Context

Subscription period boundaries and the proration denominator are computed with `time.Time.AddDate` (advance one month/year). `AddDate` is **timezone-sensitive**: it operates on the *wall-clock calendar date* in the value's `Location`. Two facts made this silently wrong:

1. **pgx/v5 stdlib scans `timestamptz` as `time.Local`** (the host process timezone), not UTC. So a `period_start` stored as `2026-05-31T18:30:00Z` reads back as `2026-06-01 00:00 +05:30` on an `Asia/Kolkata` host — calendar date **June 1**, not May 31.
2. **The advance helpers ran `AddDate` on the value's ambient Location**, not the tenant's billing timezone:
   - `NextBillingPeriodEnd` (period boundaries) handled this correctly in its **calendar** branch (it added the month `In(loc)`), but the **anniversary** and **yearly** branches did a bare `AddDate` — ignoring the `loc` they were handed.
   - `AddBillingInterval` (the proration denominator, via `fullBillingCycleDays`/`advanceBillingPeriod`) took **no `loc` at all**.

**Net effect for any tenant in a non-UTC timezone:** every month/year advance lands one calendar day off whenever the UTC instant maps to a different calendar date in the tenant zone (month-end + offset). The audit found this is **systematic across the whole billing surface**, not a one-off.

### The motivating case (verified against the live DB + empirical replication)

An operator started an anniversary-monthly sub on **June 1** (IST). Stored: `period_start = 2026-05-31T18:30:00Z`. The anniversary branch did `AddDate(0,1,0)` on the **UTC** date *May 31* → overflowed the non-existent June 31 → **July 1 18:30 UTC** = a **31-day** first period (`Jun 1 → Jul 2` in IST), when the correct June anniversary is **30 days** (`Jun 1 → Jul 1`). A mid-cycle upgrade then divided `remaining/30` (denominator computed in IST via `time.Local`) against a numerator measured to the 31-day stored boundary, overcharging ~3.3%.

Worse cases verified: a Jan-31 anniversary on an east-of-UTC tenant lands 3 days off; calendar Jan-31 silently *skips February*; yearly leap anchors land a day early.

### Two distinct defects

- **Root A/B (the class, ~13 sites):** calendar `AddDate` run on a non-tenant-anchored `time.Time` — the same defect whether the value was freshly built in UTC or DB-scanned as `time.Local`. `NextBillingPeriodEnd` anniversary/yearly branches + `AddBillingInterval` and all its proration-denominator callers.
- **Root C (independent, 1 site):** `NextBillingPeriodEnd`'s calendar branch added the month *before* snapping to the 1st, so a day-29/30/31 anchor overflowed a short month and skipped a whole calendar month (`Jan 31 + 1mo = Mar 3` in Go's normalization → snapped to `Mar 1`, losing February). TZ-agnostic.

**Explicitly NOT a bug, do not "fix" it:** Go's `AddDate` overflows `Jan 31 + 1mo → Mar 3` in *every* timezone; there is no month-end clamp to Feb-28 and we are not adding one (anniversary month-end behavior is Go's, consistently). The divergence we fix is purely the timezone offset and the calendar snap-order.

## Decision

**All month/year billing advances are anchored in the tenant timezone**, via one shared helper, so the result depends only on the tenant `loc` — never on the input's ambient `Location` or the host `time.Local`.

1. New `domain.addIntervalIn(t, interval, loc)`: re-projects `t` into `loc`, `AddDate`s, returns a UTC instant. `NextBillingPeriodEnd` (all branches) and `AddBillingInterval` route through it. (Root A/B.)
2. `AddBillingInterval` gains a `loc` parameter; threaded through `advanceBillingPeriod` (engine) and `fullBillingCycleDays` (subscription proration). The subscription handler gets a `TenantLocator` dependency (`*billing.Engine.TenantLocation`) so its proration denominator anchors in the same zone the engine writes boundaries in.
3. `NextBillingPeriodEnd`'s calendar branch snaps to first-of-month **before** advancing: `BeginningOfMonthIn(periodEnd, loc).AddDate(0,1,0)`. (Root C.)

Day-grade adds (`AddDate(0,0,N)` for trial end, due dates, net-payment terms) are TZ-invariant — a 24h×N shift on an instant — and are deliberately **not** routed through the helper.

When the tenant timezone is unconfigured the helpers fall back to UTC (the prior behavior for UTC tenants; only offset-TZ tenants change).

## Why not the alternatives

- **Pin everything to UTC** — would make the proration agree with the *buggy* 31-day period and contradict the operator's intent (they picked June 1 in their calendar). The boundaries themselves must be tenant-anchored; UTC was the wrong direction.
- **Use the stored period length as the denominator** instead of recomputing the cycle — breaks the deliberate stub-period full-cycle denominator (ADR-042): a mid-cycle signup's current period is shorter than a cycle, and dividing by it over-charges. Rejected.
- **Normalize all DB-read `timestamptz` to UTC at the scan layer** — addresses provenance but not the core requirement that the *advance* happen in the tenant zone; the helper makes results provenance-independent anyway, so the scan-normalization would be redundant belt-and-suspenders. Not added.

## Consequences

- Period boundaries, renewal anchors, and the proration denominator are now **host-TZ-independent** and mutually consistent — the same sub prorates identically on a UTC CI runner and an IST production host.
- Fixes a systematic small-cents over/under-charge on every mid-cycle upgrade/downgrade/cancel/swap for offset-TZ tenants, plus day-drifted renewal anchors and the Feb-skip calendar bug.
- One existing test (`TestPeriod_DayGradeSnap`) asserted the *buggy* UTC-computed anniversary end; corrected to the tenant-anchored value.
- New regression tests assert **provenance-independence** (same instant, UTC- vs Local-located, yields the same tenant-anchored result) and the month-end / leap cases — the existing suite built every input in UTC and was blind to the class.

## Follow-up: inclusive-last-day display convention (shipped)

Industry standard (verified across Stripe/Zuora/Recurly/Chargebee/Lago) is to *show* the **inclusive last covered day** ("Jun 1 – Jun 30") at the render layer, while storing the half-open `[start, end)` boundary. Velox previously showed the exclusive end ("Jun 1 – Jul 1") — which is what made this very off-by-one hard to spot on the invoice.

Shipped for the **invoice** period: `domain.FormatInclusivePeriod(start, end, loc)` renders the inclusive last day, date-only, in the tenant TZ (snap end to civil midnight in `loc`, then step back one CALENDAR day — never a 24h instant subtraction, the same trap as the period math above). The invoice read path sets the computed `billing_period_display` string; the PDF / hosted / portal paths (which fetch via `GetByPublicToken`, bypassing the read decorator) author it from the *same* helper — so PDF, hosted, dashboard, and list all show one identical string, no cross-runtime drift. Raw half-open `billing_period_start/end` stay unchanged (SDK contract). One-off / no-period invoices omit the period.

**Still deferred:** the *subscription* "current period" displays (SubscriptionDetail, CostDashboard, CustomerDetail, PlanDetail, Portal) are TS-only and still render the exclusive end via `formatDate`; a follow-up will route them through a shared `dates.ts` inclusive-end helper. Lower stakes (not the billing document).
