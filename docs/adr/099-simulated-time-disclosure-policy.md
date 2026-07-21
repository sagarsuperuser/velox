# ADR-099: Simulated-time disclosure — once per scope, one word, banner teaches / chip flags

**Date:** 2026-07-21
**Status:** Accepted
**Extends:** ADR-030 (simulated time on clock-pinned entities), ADR-086 §4 (FE relative-time anchors).

## Context

The clock-design review (2026-07-21) closed four surfaces that showed
simulated dates with *no* indicator. The opposite failure immediately became
visible: a clock-pinned subscription's detail page disclosed simulation
**three times** — the amber page banner, a "Test clock" chip in the header,
and another chip on *every* Activity row — all saying the same thing. The
operator asked for a rule: simple, visible, jargon-free, no redundancy.

Two failure modes, one policy needed:
- **Silence**: a simulated date shown bare reads as a real date (the gap the
  review fixed).
- **Noise**: the same flag repeated N times per page teaches operators to
  ignore amber (banner blindness) — which re-creates silence.

## Decision

**Disclose once per scope, at the highest element that covers everything
below it. The word is "Simulated". The banner teaches; the chip flags.**

1. **Scope rule.** Every surface has exactly one simulation indicator, at the
   smallest level where simulated and real data can actually mix:
   - **Detail page of one clock-pinned entity** (customer, subscription,
     invoice): the amber **page banner** is the sole indicator. Nothing below
     it on the same page repeats the flag — no header chip, no per-row chips
     on child cards. Child rows still *show* their simulated timestamps and
     the "Recorded ⟨wall⟩ · by ⟨actor⟩" provenance sublines: those are
     information, not flags.
   - **Card scoped to one customer on a page with no banner** (the Credits
     ledger card): one chip on the card header.
   - **Mixed lists**, where adjacent rows can differ (invoices, credit notes,
     subscriptions, customers, usage events, dunning runs, the dashboard
     feed, a plan's subscribers, the audit log): one chip **per row** — the
     row is the scope.
2. **Vocabulary.** One visible word everywhere: **"Simulated"**, with the
   flask icon. "Test clock" is builder vocabulary; operators think "this
   data is simulated". Tooltips explain in plain language ("Dates are
   simulated (test clock) — not real time."); link variants add "click to
   open the clock". Raw clock IDs never appear in visible copy — tooltips
   and link targets may carry them for navigation.
3. **Banner = teacher, chip = flag.** The banner keeps the full explanation,
   the View-clock link, and the deleted-clock variant. Chips never explain;
   one word plus a tooltip.

## Consequences

- Removed as redundant (their page banner already discloses): the
  subscription/customer detail header chips, the invoice detail header
  "Simulated" pill, and the per-event chip on the subscription Activity
  timeline.
- Retained: every mixed-list row chip, the Credits card-header chip, the
  audit log's per-row chip (wall-primary forensic list — the chip carries
  the simulated instant there).
- `TestClockBadge` / `SimulatedBadge` share the one-word vocabulary; the
  "Pinned to test clock vlx_tclk_…" tooltip phrasing is gone.
- MANUAL_TEST assertions that pinned the removed duplicates (B7's
  invoice-header badge leg) are truthed to the policy in the same PR.
- Trade-off accepted: a screenshot of *only* an invoice header no longer
  carries the badge — the banner sits directly above and is part of any
  honest capture; the PDF never had a badge and is unchanged.
