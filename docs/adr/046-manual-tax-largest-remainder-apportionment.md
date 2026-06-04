# ADR-046: Manual tax — document-level total + largest-remainder line apportionment

**Status:** Accepted
**Date:** 2026-06-03
**Supersedes:** the "dump the residual on the positionally-last line"
reconciliation in `internal/tax/manual.go` (introduced `401d990`, both
the exclusive and inclusive paths).

## Context

The `ManualProvider` applies one flat tenant rate to every line item.
Two facts about integer-cent money force a reconciliation step:

1. **Tax is rounded.** A line's tax (`base × rate`) rarely lands on a
   whole cent, so each line's tax is rounded to the nearest cent.
2. **Per-line rounding doesn't sum to the document tax.** Rounding each
   line independently and summing can drift ±1¢ (more with many lines)
   from the tax computed on the document subtotal.

Worked example — three lines `$33.33 / $33.33 / $33.34` at 7.25%:

| line | base (¢) | exact tax | naive round |
|---|---|---|---|
| 1 | 3333 | 241.6425 | 242 |
| 2 | 3333 | 241.6425 | 242 |
| 3 | 3334 | 241.7150 | 242 |
| **Σ** | **10000** | **725.00** | **726** |

The exact document tax is `10000 × 7.25% = 725¢`; naive per-line rounding
gives `726¢`. Something must reconcile the 1¢ gap so that
`Σ(line tax) == invoice tax`.

There are two independent decisions hiding in that reconciliation.

### Axis 1 — what is the authoritative document total?

- **Top-down** (compute tax on the subtotal): `725¢`. Exact; this is the
  VAT-correct figure and what an auditor recomputes as `$100.00 × 7.25%`.
- **Bottom-up** (sum the rounded per-line taxes): `726¢`. Over-collects
  the residual cent versus the true rate × base.

### Axis 2 — which line absorbs the residual?

Given a chosen total, the rounded per-line taxes must be nudged to hit
it. The **old** code added the entire residual to the line that was
**last in array order**:

```go
lines[len(lines)-1].TaxAmountCents += totalTax - lineTaxSum
```

That preserves the sum but says nothing about *placement*. When the
residual is **negative** (here −1¢, since 725 < 726) and the last line is
the largest, that line is docked below its smaller peers:
`242 / 242 / 241` — the `$33.34` line taxed *less* than two `$33.33`
lines. On the invoice this reads as a bug ("biggest line, least tax"),
and it is one: the placement is arbitrary, not principled. The flaw was
latent from the day the reconciliation was added and surfaced only on
inputs that drive the residual negative onto the largest line — earlier
examples and tests all happened to have a positive residual landing as a
round-*up* on the last line, which reads naturally.

## Decision

**Keep the total top-down (725¢, exact). Distribute the residual by
largest remainder.**

- **Axis 1 — top-down total, unchanged.** `totalTax` stays
  `round(taxableBase × rate)`. This is the exact, audit-defensible
  figure for a single flat rate, and matches the document-level rounding
  EU VAT explicitly permits. (Stripe's *default* and automatic Stripe
  Tax sum bottom-up → would yield 726¢; Stripe also offers a document /
  "invoice level" rounding mode for manual rates, which is what we do.
  Since this provider is the *manual* one, top-down is the right default
  and the contained change.)

- **Axis 2 — largest-remainder apportionment.** New helper
  `distributeLargestRemainder(total, nums, den)` in `internal/tax/manual.go`:
  each line gets `floor(nums[i] / den)`, then the leftover cents
  (`total − Σfloor`, mathematically in `[0, n]`) are handed out one at a
  time to the lines with the **largest fractional remainders**, ties
  broken by **lowest index**. Applied to both the exclusive and
  inclusive paths (the exact per-line share is `base × ppm / 1_000_000`
  exclusive, `grossBase × ppm / denom` inclusive).

Guarantees:
- `Σ(line tax) == totalTax` (the invariant the old code also held).
- Every line is within 1¢ of its exact share.
- **No base-order inversion**: a line with a larger base never carries
  smaller tax than a smaller line. (For the example: floors are
  `241 / 241 / 241`; the two leftover cents go to the largest remainders —
  the `$33.34` line at .7150 and one `$33.33` line at .6425 — giving
  `242 / 241 / 242`. The `$33.34` line is promoted, never left behind.)

`stripe_tax` is unaffected — Stripe returns per-line tax amounts and a
total directly; the engine copies them verbatim
(`internal/billing/engine.go` `ApplyTaxToLineItems`). This decision is
scoped to the internal manual provider.

## Consequences

- **Invoices never show a larger line taxed less than a smaller one.**
  The operator-visible artifact that triggered this ADR is gone.
- **Document total is unchanged.** Still `725¢` for the example; only
  *which* line absorbs the cent moved. No customer is charged a
  different total than before.
- **Method matches every reference indirect-tax engine.** See below —
  this is the convergent industry rule, not a Velox invention.
- **One small helper, two call sites.** No new dependency, no schema
  change, no API change. Pure provider-internal logic.

## Industry references

Verified 2026-06-03 across multiple platforms (not a single-source
spot-check):

- **Sovos** (enterprise indirect-tax): the residual "is split among the
  lines that have the highest remainder to the right of the truncated
  values"; on ties "added to the first line." → largest remainder,
  lowest-index tie-break. (This is the rule we implemented.)
- **Avalara AvaTax** / **Microsoft Dynamics 365**: adjust "the line that
  results in the minimum percentage change in tax amount" — the same
  minimum-distortion intent.
- **Stripe**: manual rates support both "line item level" (round per
  line, then sum → bottom-up) and "invoice level" (apply rate to
  subtotal, then round → top-down) rounding; automatic Stripe Tax always
  sums per-line then rounds. Stripe never dumps the residual on a
  positional line.

**Zero** platforms in the surveyed set distribute the residual by array
position. The old behavior was the outlier.

## Revisit trigger

- A design partner needs **bottom-up** totals (per-line tax is the
  source of truth, document tax = their sum → 726¢ for the example).
  That's a tenant-level rounding-mode setting (Stripe exposes exactly
  this for manual rates); add it as a `tenant_settings` option rather
  than changing the default. Until a DP asks, top-down is the default.
- Multi-jurisdiction manual rates (per-line different rates) — the
  apportionment would run per rate group, not across the whole document.
  Out of scope while the manual provider is single-rate.

## Related

- ADR-042 / ADR-043: tax-rate decimal precision (the `ratePPM` integer
  math this apportionment builds on).
- ADR-041: manual-fallback removal — same provider, prior cleanup.
- `feedback_verify_real_path_not_shortcut`: the old reconciliation
  passed the sum-invariant test but its *placement* was never tested;
  the new tests assert the real provider path + no-inversion.
