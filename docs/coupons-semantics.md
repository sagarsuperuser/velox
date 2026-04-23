# Coupon Semantics: Refunds, Tax, and Multi-Currency

Audience: integrators wiring Velox coupons into a checkout, finance teams
reconciling promo spend, and operators answering "why did the discount do
that?" questions.

This doc covers the three interactions that catch teams off guard most
often. API shape lives in `docs/openapi.yaml`; this doc answers the "why"
and the "what happens at the edges."

## 1. Refunds and credit notes

A coupon redemption is tied to an invoice. When that invoice gets credit-
noted, the redemption's fate depends on how much of the invoice is reversed.

### Full credit → redemption is voided

When a credit note reverses the entire invoice (credit amount ≥
invoice total):

- `CouponRedemption.voided_at` is set to the credit-note timestamp.
- `Coupon.times_redeemed` is decremented by 1 in the same transaction.
- `CouponRedemption.periods_applied` is reduced by 1 (floored at 0) so
  a `repeating` coupon recovers one period of headroom.

The customer can re-redeem the coupon (subject to the coupon's other
gates) because a voided redemption no longer counts. For a `repeating`
coupon with `duration_periods = 3`, a voided second-cycle redemption
frees up that slot — the next cycle can redeem again.

### Partial credit → redemption stands

When the credit note covers only part of the invoice (credit amount <
invoice total), the redemption is **not** voided and `times_redeemed` is
**not** decremented. The intent is: the customer still paid for some of
the invoice, so they still consumed the discount on the slice they paid.

If you need to explicitly release a redemption after a partial credit,
do it through an operator action — this is rare enough that surfacing
it as an automatic behavior would create more incorrect flips than
correct ones.

### Idempotency

Void is idempotent: re-issuing a credit note (or replaying the same
request) finds `voided_at IS NOT NULL` and skips the decrement. The
counter never double-subtracts.

### Best-effort on void failure

If the void step fails (DB contention, RLS issue, transient error), the
credit note still issues. The operator has to reconcile `times_redeemed`
manually. This trade-off was explicit: a refund the customer can see is
more important than a counter the operator can repair.

**Code references:**
`internal/creditnote/service.go` (issue + coupon-void hand-off),
`internal/coupon/postgres.go` `VoidRedemptionsForInvoice`,
`internal/creditnote/service_coupon_void_test.go`.

## 2. Tax

Velox discounts **pre-tax**. The invoice total is:

```
total = subtotal − discount + tax(subtotal − discount)
```

Tax is computed on the post-discount subtotal, so a customer paying
$80 on a $100 cart with a $20 coupon is taxed on $80, not $100. This
matches the treatment customers expect from Shopify / Stripe Billing /
Chargebee — billing the customer for tax on money they didn't pay
is the wrong default, and the one jurisdictions actively warn against.

### Tax-inclusive pricing

When `tenant_settings.tax_inclusive = true`, the displayed subtotal
already bakes the tax in. The discount still applies to the gross line
total; the tax provider re-derives the pre- and post-tax split from
the post-discount amount. Net effect for the customer is identical:
they're taxed on what they pay, not on what the pre-discount line
would have been.

### Tax provider behavior

Both providers today (`manual` flat-rate and `stripe_tax`) receive the
already-discounted subtotal as the tax base. Velox does not ask the
provider to compute tax on the gross and then subtract — the provider
sees the discounted amount directly.

**Code references:**
`internal/billing/engine.go` (order of operations: price → discount →
tax → total), `internal/coupon/service.go` `CalculateDiscount`.

## 3. Multi-currency

Velox stores currency as an ISO-4217 code on the coupon (for fixed-
amount only) and on the invoice. Redemption-time matching is strict
equality — there is no FX conversion.

### Percentage coupons

Currency is not required on the coupon. A `20%` coupon works against a
USD invoice, an EUR invoice, or a JPY invoice. The percentage is
applied to whatever the invoice's subtotal is.

### Fixed-amount coupons

Currency is **required** at creation. `$10 off` only makes sense in a
specific currency — you can't apply $10 off to a €100 invoice without
an FX rate, and Velox doesn't pick an FX rate for you.

At redeem time, the coupon's currency must match the invoice currency
(case-insensitive). A mismatch returns `422` with
`code=coupon_currency_mismatch`.

### Engine-time skip

When the billing engine walks coupons during invoice finalization, a
fixed-amount coupon whose currency doesn't match the invoice's currency
is silently skipped (with a warning log). This only happens when a
coupon was redeemed in advance and the invoice currency changes
afterwards (rare, usually an operator error). Percentage coupons are
never skipped for currency reasons.

### FX-aware discounts

If you need "€10 off for EUR carts, $10 off for USD carts," create one
coupon per currency and share the same `name` + a distinct `code` per
currency. Your checkout picks the right code based on the cart's
currency before calling `/v1/coupons/redeem`.

**Code references:**
`internal/coupon/service.go` `validateRedeem` (redeem-time gate),
`ApplyToInvoice` (engine-time skip).

## Quick reference

| Scenario | Outcome |
|---|---|
| Full credit note on invoice with redemption | Redemption voided, `times_redeemed` −1, periods_applied −1 |
| Partial credit note | Redemption stands, counters unchanged |
| Tax on discounted invoice | Tax computed on `subtotal − discount` |
| Percentage coupon against any-currency invoice | Applies |
| Fixed-amount coupon, currency mismatch | 422 `coupon_currency_mismatch` at redeem; silently skipped at billing |
