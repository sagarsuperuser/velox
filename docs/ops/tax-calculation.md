# Velox — Tax Calculation

Velox supports two tax calculators. The active one per tenant depends on
whether Stripe Tax credentials are configured.

## Calculators

### `ManualCalculator`

A flat basis-point rate applied uniformly to every line item. Configured per
tenant via settings (`tax_rate_bp`, `tax_name`).

Use it when:

- The tenant has a single jurisdiction and a single legal rate.
- The tenant is exempt (set `tax_rate_bp: 0`).
- The tenant does not have Stripe Tax enabled on their Stripe account.

### `StripeCalculator`

Calls the Stripe [Tax Calculations API](https://stripe.com/docs/api/tax/calculations)
per invoice finalization. Returns jurisdiction-aware tax based on the customer's
billing address and the Stripe Tax product catalog mapping.

Use it when:

- The tenant sells across multiple jurisdictions (US states, EU VAT, GB VAT).
- The tenant has registered for tax collection with Stripe.
- The tenant needs tax-inclusive reporting or marketplace/connect reconciliation.

The calculator holds **both** a live and a test Stripe client and selects based
on the request's livemode. A test-mode invoice will never call the live Tax
API, and vice versa.

## Fallback behavior

`StripeCalculator` falls back to `ManualCalculator` in every case where it
cannot produce a Stripe-sourced answer:

| Condition | Behavior | Log level |
|-----------|----------|-----------|
| Customer has no country on file | Falls back to manual | `warn` |
| No Stripe client configured for the current mode (e.g. test-mode invoice, no test key) | Falls back to manual | `warn` |
| Stripe Tax Calculations API returns any error | Falls back to manual | `warn` |
| Line-item list is empty | Returns zero tax (no fallback call) | — |

The fallback is **always** available — it's a required constructor argument, so
a misconfiguration that nils it out is caught at startup, not at invoice time.

### Why fallback, not fail?

An invoice that cannot calculate tax still must be issued — a billing engine
that blocks on a third-party outage will miss a billing cycle, which is a
customer-visible revenue event. Manual tax is less accurate but correct-ish,
and the tenant's configured rate is the reasonable default.

If a tenant requires strict jurisdiction-sourced tax (e.g. regulated industry,
marketplace), they should run a reconciliation job against Stripe's reports
and issue credit notes for the delta. Velox does not block invoice issuance
on Stripe Tax availability.

## What a fallback looks like in logs

```
WARN stripe tax API error, falling back to manual error=<stripe error>
WARN stripe tax: no customer country, falling back to manual
WARN stripe tax: no client configured for mode, falling back to manual livemode=true
```

These are `warn`, not `error`, because the system continues correctly — it's
the tenant's configured manual rate that applies. If you're alerting on them,
alert on `warn` rate, not presence.

## When to investigate a fallback

- **Single tenant, repeated:** their Stripe Tax setup is broken or their
  customers are missing country data. Action: contact the tenant.
- **Many tenants, correlated in time:** Stripe API incident. Check
  [status.stripe.com](https://status.stripe.com). Action: none, fallback is
  correct.
- **Single tenant, sudden start:** their Stripe API key was rotated or
  revoked. Action: ask them to update credentials.

## Mode-split semantics

A test-mode invoice uses the test Stripe Tax key. A live-mode invoice uses the
live key. This split matters because:

1. Test-mode keys are rate-limited differently — a test-mode load test should
   not exhaust a tenant's live-mode Stripe quota.
2. Tax Calculations on the wrong mode would charge against the wrong Stripe
   account, which is incorrect accounting even though no state mutation
   occurs.

If only one mode has a key configured, the other mode silently falls back to
manual. Tenants who want both modes operational must configure both keys in
their tenant settings.

## Audit trail

Tax calculations are **not** persisted in an audit table — they are part of
the invoice line items, which are themselves the durable record. The effective
rate and source (manual vs Stripe) are reflected in:

- `invoice_line_items.tax_rate_bp` — rate applied to this line.
- `invoice_line_items.tax_name` — e.g. `"sales_tax"`, `"vat"`, or the tenant's
  configured `tax_name` for manual.

If an operator needs to prove "this invoice used Stripe Tax," the `tax_name`
and `tax_country` on the invoice are the primary evidence; correlate with the
log line emitted at finalization time to distinguish Stripe-sourced values
from a manual calculation that happened to name itself similarly.

## Related

- [runbook.md](./runbook.md) — alerts that page on billing cycle failures,
  including tax calculation errors that slip through both calculators.
- [sla-slo.md](./sla-slo.md) — SLO targets for billing cycle success rate.
