# Velox — Tax Calculation

Velox ships three tax providers. Each tenant picks one via
`tenant_settings.tax_provider`; the billing engine resolves the provider
at invoice build time and reuses it through finalize and credit-note
issuance. Velox does not pick a tax model — the tenant's Stripe account
holds the legal registration (OIDAR, domestic GST/VAT, US sales tax, …)
and Velox supports whatever shape that registration produces.

## Providers

### `none`

Zero-tax backend. `Calculate` returns zero per line; `Commit` and
`Reverse` are no-ops. Pick this when the tenant doesn't collect tax
(early-stage B2B, unregulated jurisdictions). An empty or unrecognised
`tax_provider` falls through to `none` so a mis-seeded tenant cannot take
billing offline.

### `manual`

Flat basis-point rate applied uniformly across every line item.
Configured per tenant via `tenant_settings.tax_rate_bp` and
`tenant_settings.tax_name` (the label that renders on the invoice —
"VAT", "Sales Tax", "GST", …). Honours tax-inclusive vs exclusive via
`Request.TaxInclusive`, exempt customers (`StatusExempt`), and
reverse-charge customers (`StatusReverseCharge`). `Commit` and `Reverse`
are no-ops (no upstream state to record).

Pick this when:

- Single jurisdiction, single legal rate.
- Tenant exempt (set `tax_rate_bp=0`).
- Tenant does not have Stripe Tax registered or wired up.

Manual is also the internal fallback used by `stripe_tax` when the
Stripe call cannot produce an answer — see *Failure handling* below.

### `stripe_tax`

Calls Stripe's Tax Calculations API at invoice build, creates a
`tax_transaction` at invoice finalize via `Commit`, and reverses against
that transaction at credit-note issue via `Reverse`. Multi-tenant: the
Stripe client is resolved per ctx (`tenant_id` + `livemode`) via the
`StripeClientResolver` interface, so each tenant's calls hit their own
Stripe account in the correct mode.

Pick this when:

- Tenant sells across multiple jurisdictions (US sales tax, EU VAT, GB
  VAT, …).
- Tenant has registered for tax collection with Stripe and needs Stripe
  Tax reporting to reflect Velox-issued invoices.
- Tenant needs durable jurisdictional records that reconcile against
  Stripe's tax reports.

## Provider interface

All three implement the same `Provider` shape:

| Method      | Called at         | Mutates upstream?    |
|-------------|-------------------|----------------------|
| `Name`      | —                 | no                   |
| `Calculate` | invoice build     | no                   |
| `Commit`    | invoice finalize  | yes (`stripe_tax`)   |
| `Reverse`   | credit-note issue | yes (`stripe_tax`)   |

`Commit` is what makes a tax decision durable upstream. Stripe Tax
*calculations* expire after 24 hours, but `tax_transaction`s are
permanent and surface in Stripe's tax reporting. The transaction ID is
persisted on `invoices.tax_transaction_id`. `Commit` uses the invoice ID
as Stripe's reference, so a retried finalize is idempotent (Stripe
enforces reference uniqueness across `tax_transaction`s in the account).

`Reverse` issues a reversal against an existing `tax_transaction` when a
credit note is issued. The credit-note ID is the reversal's reference,
so retried credit-note issuance is similarly idempotent. `none` and
`manual` return an empty `ReversalResult` so the credit-note flow can
ignore the outcome without branching on provider name.

## Failure handling: fallback vs defer

`stripe_tax` can fail for three reasons:

| Condition                                                      | Reason label         |
|----------------------------------------------------------------|----------------------|
| Customer has no country on file                                | `no_country`         |
| No Stripe client configured for the request's livemode         | `no_client_for_mode` |
| Stripe Tax API returns any error                               | `api_error`          |

What happens next depends on the per-request `OnFailure` policy:

- **default (`fallback_manual`)** — silently delegate to the internal
  `ManualProvider`. Billing continues at the tenant's configured manual
  rate. Less accurate but available.
- **`block`** — propagate the error; the engine defers the invoice to
  `tax_status=pending` and a scheduled retry re-runs `Calculate`. Used
  by tenants whose compliance posture forbids issuing an invoice at the
  wrong rate (regulated marketplaces, OIDAR-registered EU VAT, etc.).

Both outcomes are counted on `velox_tax_outcome_total{outcome, reason}`:

- `outcome=fallback` — silent rate drift; investigate before it compounds.
- `outcome=deferred` — blocked billing cycle; needs operator action if
  retries do not clear.

Happy-path calculations are not counted on this metric — it is a pure
failure-mode signal, suitable for `rate(...) > 0` alerting.

### Why fallback, not fail (default)?

A billing engine that blocks on a third-party outage misses billing
cycles, which is a customer-visible revenue event. Manual tax at the
tenant's configured rate is the reasonable default — correct-ish, and
the divergence is observable on `velox_tax_outcome_total{outcome="fallback"}`.

### Why offer `block` then?

Some tenants — regulated marketplaces, OIDAR-registered EU VAT — are
legally exposed if they issue an invoice at the wrong rate. For them, a
deferred invoice with a retry is correct; a fallback is not. `block`
trades availability for correctness, and the deferred pile is itself an
alert.

### Log lines

```
WARN stripe tax failed, falling back to manual reason=api_error error=… livemode=true
WARN stripe tax failed, falling back to manual reason=no_country
WARN stripe tax failed, falling back to manual reason=no_client_for_mode livemode=true
WARN stripe tax failed, deferring per block policy reason=api_error error=… livemode=true
```

These are `warn`, not `error`, because the system continues correctly —
either a tenant-configured rate applied or the invoice was deferred to
retry. Alert on the metric, not on log presence.

## When to investigate a fallback or defer

- **Single tenant, repeated** — Stripe Tax setup is broken on the
  tenant side, or their customers are missing country data. Action:
  contact the tenant.
- **Many tenants, correlated in time** — Stripe API incident. Check
  [status.stripe.com](https://status.stripe.com). Action: none for
  fallback (correct by design); for `deferred` invoices, ensure the
  retry scheduler clears them once Stripe recovers.
- **Single tenant, sudden start** — Stripe API key rotated or revoked.
  Action: ask the tenant to update credentials.

## Mode-split semantics

A test-mode invoice resolves to the test Stripe client, a live-mode
invoice to the live client. This matters because:

1. Test-mode keys are rate-limited differently — a test-mode load test
   should not exhaust a tenant's live-mode Stripe quota.
2. Calling Tax on the wrong Stripe account is incorrect accounting even
   though no state mutation occurs (a calculation creates no upstream
   state, but `Commit` does).

If only one mode has a key configured, the other mode falls back per the
configured `OnFailure` policy (recorded as reason `no_client_for_mode`).
Tenants who want both modes operational must configure both keys in
their tenant settings.

## Persistence

Tax decisions are recorded on the invoice itself, not in a separate
audit table — the invoice is the durable record:

| Column                              | Source                                                  |
|-------------------------------------|---------------------------------------------------------|
| `invoices.tax_provider`             | provider that produced this invoice                     |
| `invoices.tax_calculation_id`       | Stripe Tax `calc_xxx` (`stripe_tax` only)               |
| `invoices.tax_transaction_id`       | Stripe Tax `tx_xxx`, set at `Commit` (`stripe_tax` only) |
| `invoices.tax_reverse_charge`       | reverse-charge flag                                     |
| `invoices.tax_exempt_reason`        | exempt-customer reason                                  |
| `invoice_line_items.tax_rate_bp`    | rate applied to this line                               |
| `invoice_line_items.tax_name`       | label rendered on the invoice                           |

The line-level fields are what the customer sees on the PDF. The
invoice-level fields are the upstream linkage that lets `Reverse` find
the original `tax_transaction` when a credit note is issued, and let
operators reconcile a Velox invoice against Stripe Tax reports without
joining log lines.

## Tax IDs

`internal/tax/taxid.go` validates customer tax IDs (VAT numbers, GST
numbers, etc.) and surfaces the validated ID on the invoice so the PDF
renders the correct legal text. Format validation is local; existence
validation against tax authorities is out of scope.

## Related

- [runbook.md](./runbook.md) — alerts that page on billing-cycle
  failures, including tax outcomes that exceed thresholds.
- [sla-slo.md](./sla-slo.md) — SLO targets for billing-cycle success
  rate.
