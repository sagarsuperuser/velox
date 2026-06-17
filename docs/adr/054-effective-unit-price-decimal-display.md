# ADR-054: Display the per-unit price at full precision — derive it on read, don't store it

**Date:** 2026-06-17
**Status:** Accepted

## Context

A usage invoice line for a sub-cent rate rendered **Unit Price $0.00** while the **Amount** was correct (e.g. 1,000 units billed $3.00 — a $0.003/unit rate). The line amount is computed from the full-precision decimal rate (ADR-045) and is exact; but the invoice line stores the per-unit price only as `unit_amount_cents` (int64 whole cents), **back-derived** from the rounded amount: `round(300 ÷ 1000) = round(0.3) = 0`. Both the dashboard and the PDF then print that `0` at two decimals → `$0.00`. The result looks internally inconsistent (1,000 × $0.00 ≠ $3.00) and is wrong for the AI-token / usage case Velox targets, where sub-cent rates are the norm.

Verified industry behavior (research, 2026-06-17) is a **two-track precision model**: the per-unit price is shown at full/native decimal precision, and rounding to currency precision (whole cents) is applied **only** to the derived line amount and totals — never to the displayed unit price.

- **Stripe** — `unit_amount_decimal`, up to 12 decimal places; *"rounding occurs on the line item level … after multiplying the quantity by the decimal amount"* ([products-prices](https://docs.stripe.com/products-prices/manage-prices)).
- **Chargebee** — up to 20 dp; *"invoices … display the product price and quantity in multiple decimal places … line item amounts are rounded to the currency's precision"* ([multi-decimal](https://www.chargebee.com/docs/2.0/multi-decimal-support.html)).
- **Recurly** — up to 9 dp; *"the unit_amount_decimal value is displayed on the invoice … line item totals are rounded"* ([decimal-pricing](https://docs.recurly.com/recurly-subscriptions/docs/decimal-pricing)).
- **Metronome / Lago** — carry the rate in fractional minor units / configurable precision; rounding is opt-in at the amount level.

## Decision

Surface a full-precision per-unit price, **derived on read**, never stored.

1. **One computation** — `domain.InvoiceLineItem.EffectiveUnitAmountDecimal()` returns `amount_cents ÷ quantity` in decimal cents, capped at 12 dp (Stripe parity), guarding `quantity == 0`. It uses `QuantityDecimal` when set, else the integer `Quantity`.
2. **Effective, not nominal** — the displayed unit price is the *effective* rate (amount ÷ quantity), so it always **reconciles** with the rounded line amount and stays well-defined for blended/tiered/multi-dimensional lines that have no single nominal rate. (The nominal rule rate lives upstream and is undefined for aggregated lines; surfacing it would not reconcile with the amount.)
3. **Plumbed through the API, computed once** — `InvoiceLineItem.MarshalJSON` injects `unit_amount_decimal` (decimal-cents string) into the dashboard wire form; the hosted-invoice DTO and the PDF call the same method. No frontend arithmetic (`feedback_no_heuristic_proxies`): the backend serves the value, the renderers display it via the existing decimal-aware `formatRate` (which never collapses a sub-cent rate to `$0.00`). `unit_amount_cents` is retained unchanged for back-compat.

**No new column, no migration.** The unit price is a *derived* quantity; persisting it would be denormalization that can drift from `amount_cents` — exactly the dual-write class this codebase has been bitten by. Deriving on read from the authoritative `amount_cents` + `quantity` is the normalized, drift-proof design and also fixes every existing row with no backfill.

## Why this design

`amount_cents` and `quantity` are the authoritative persisted facts; the unit price is `amount ÷ quantity` by definition. Computing it in one domain method and serving it (rather than storing it, or re-deriving it in each of the dashboard / PDF / hosted renderers) gives a single source of truth that cannot disagree with the amount and cannot drift across surfaces (`project_tax_field_propagation_drift` is the cautionary precedent). Line amounts and totals stay whole-cent int64 (ADR-045 unchanged); only the per-unit *display* gains precision — the two-track model the peers converge on.

## Alternatives considered

- **Store a `unit_amount_decimal` column populated at line construction.** Rejected: a migration plus ~16 construction sites to populate, a denormalized field that can drift from `amount_cents`, and a backfill question for existing rows — all to persist a value that is trivially derivable on read.
- **Plumb the nominal rule rate onto the line.** Rejected: undefined for tiered / multi-dimensional / package lines (no single rate), and `quantity × nominal` need not equal the rounded `amount_cents`, so the displayed unit price wouldn't reconcile with the charged amount.
- **Compute `amount ÷ quantity` in the frontend.** Rejected per `feedback_no_heuristic_proxies` — the backend knows the value authoritatively, so it plumbs it through the API; the dashboard and hosted page render the served field rather than each re-implementing the arithmetic.

## Consequences

### Positive
- Sub-cent rates display correctly ($0.003, $0.0000030) on the dashboard, the PDF, and the public hosted page; `quantity × unit ≈ amount` reconciles.
- Zero schema change, zero migration, zero backfill — every existing line is fixed on read.
- Single computation (`EffectiveUnitAmountDecimal`) shared by all three render surfaces; no per-renderer drift.

### Risks / open items
- **Credit-note line items** (`credit_note_line_items`) carry the same whole-cent `unit_amount_cents` and would show the same `$0.00` for a sub-cent gross unit. Deferred — same pattern (a parallel method + the CN render surfaces); credit notes for sub-cent usage are rare and the reported surface is the invoice. Trigger: first sub-cent credit-note display complaint.
- The effective rate can differ from the *nominal* configured rate by the amount's rounding at very small quantities (e.g. one unit of a sub-cent item rounds the amount itself); this is correct invoice behavior (Stripe rounds the line amount too) and reconciles with what's charged.

## References
- ADR-045 (decimal per-unit pricing rates — line amounts stay int64), ADR-053 (single source of truth posture).
- Memory: `project_decimal_pricing_rates`, `feedback_no_heuristic_proxies`, `project_tax_field_propagation_drift`, `feedback_verify_stripe_parity_claims`.
- [Stripe prices](https://docs.stripe.com/products-prices/manage-prices), [Chargebee multi-decimal](https://www.chargebee.com/docs/2.0/multi-decimal-support.html), [Recurly decimal pricing](https://docs.recurly.com/recurly-subscriptions/docs/decimal-pricing).
