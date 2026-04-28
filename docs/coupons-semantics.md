# Coupon Semantics: Refunds, Tax, and Multi-Currency

Audience: integrators wiring Velox coupons into a checkout, finance teams
reconciling promo spend, and operators answering "why did the discount do
that?" questions.

This doc covers the three interactions that catch teams off guard most
often. API shape lives in `api/openapi.yaml`; this doc answers the "why"
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

## 4. Applying a coupon to an already-issued draft invoice

The billing engine applies coupons automatically during `RunCycle` — the
subscription-scope coupon wins; the customer-scope coupon falls back; tax
recomputes against `subtotal − discount`. For the less common case where
an operator needs to apply a code to an already-issued draft (e.g., a
retention gesture on an invoice that generated before the coupon was
attached), use `POST /v1/invoices/{id}/apply-coupon`.

### Gate conditions

The endpoint rejects before any redemption commits when:

- the invoice is not `draft` (finalized/paid/voided invoices are
  immutable — void and re-issue if needed),
- `discount_cents > 0` (stacking two coupons is intentionally disallowed
  — different coupons have different redeem semantics, and combining
  them silently is a reconciliation landmine),
- `tax_transaction_id != ""` (the tenant's Stripe Tax account already
  committed the calculation upstream; recomputing tax here would
  desync our record from Stripe's),
- `subtotal_cents <= 0` (nothing to discount).

### Orchestration

The engine runs the same sequence as an automatic redemption:

1. Redeem (same gates: code, plan, currency, usage, dates, customer
   history). PlanIDs check is any-one-of across the subscription's
   plans, so multi-item subscriptions don't fail the gate just because
   one item is on an unrestricted plan.
2. Recompute tax against `subtotal − discount` via the tenant's
   configured tax provider.
3. Atomically write the new discount, new tax amount, and repriced
   line items in one transaction.
4. Advance `periods_applied` on the committed redemption — same
   counter that `once` and `repeating` coupons burn during normal
   billing.

If step 3 fails after step 1 committed a fresh redemption, the engine
compensates by voiding the redemption so `times_redeemed` stays
honest. Replays (same `Idempotency-Key`) skip the compensation
because the first call already owns the side effects.

### Idempotency

Pass `Idempotency-Key` to make retries safe. The underlying coupon
redeem uses the same key-scoping as `/v1/coupons/redeem`, so a
repeated request returns the original redemption without burning
another slot.

### Emitted effects

- `invoice.coupon.applied` webhook event carries the updated invoice.
- Audit log entry action `apply_coupon` references the invoice and
  redemption.

**Code references:**
`internal/billing/engine.go` `ApplyCouponToInvoice`,
`internal/invoice/service.go` `ApplyCoupon`,
`internal/invoice/handler.go` `applyCoupon`.

## Quick reference

| Scenario | Outcome |
|---|---|
| Full credit note on invoice with redemption | Redemption voided, `times_redeemed` −1, periods_applied −1 |
| Partial credit note | Redemption stands, counters unchanged |
| Tax on discounted invoice | Tax computed on `subtotal − discount` |
| Percentage coupon against any-currency invoice | Applies |
| Fixed-amount coupon, currency mismatch | 422 `coupon_currency_mismatch` at redeem; silently skipped at billing |
| Apply coupon to finalized/voided/paid invoice | Rejected at gate; no redemption committed |
| Apply coupon to already-discounted invoice | Rejected at gate; stacking disallowed |
| Apply to draft after tax_transaction committed | Rejected at gate; must void invoice and re-issue |
