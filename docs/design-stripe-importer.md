# Stripe importer — design

> **Phase 0 only.** This document defines the importer surface and pins down
> mapping decisions for `Customer`. Subsequent phases extend the same surface
> to subscriptions, products+prices, and finalized invoices. The CLI shipped
> with Phase 0 has the flags for those phases but only the `customers`
> resource is wired end-to-end.

## Why this exists

The 90-day plan slates the Stripe importer for Week 11, but the
risks-and-mitigations row says explicitly: *"Start the importer Week 7 in
parallel, not Week 11; prototype on internal test data first."* We are at
Week 9 and the importer was 0% started — Phase 0 is the
catch-up slice that gets the design pinned down and the customer importer
working end-to-end.

The strategic story Velox tells is "self-host an open-source Stripe Billing
alternative." That story collapses if migrating the existing Stripe customer
base is anything harder than a one-line CLI invocation. Phase 0 is the
foundation under that promise.

## CLI surface

```
velox-import \
  --tenant=<tenant_id>             [required]
  --api-key=<sk_live_... or sk_test_...>  [required]
  --since=<RFC3339 or YYYY-MM-DD>  [optional; default: import everything]
  --resource=<comma list>          [optional; default: customers (Phase 0)]
                                   [recognised: customers, subscriptions, products, prices, invoices]
  --output=<path>                  [optional; default: ./velox-import-<ts>.csv]
  --dry-run                        [no DB writes; report what would happen]
  --livemode-default=<true|false>  [default: derived from sk_live_/sk_test_ prefix]
```

DATABASE_URL is read from the environment, the same way `cmd/velox-bootstrap`
and `cmd/velox` do it. Migrations are NOT run by the importer — the operator
brings up Velox via the normal path first, then runs the importer against a
ready schema.

The Stripe API key passed is the **source** Stripe account's secret key. The
importer reads from it; it never writes to Stripe.

## Idempotency

Every imported row is keyed by `(tenant_id, external_id)` where `external_id`
is the Stripe id (`cus_...`, `sub_...`, etc.). The Velox `customers` table
already has `UNIQUE (tenant_id, livemode, external_id)` (migration 0020), so
upserts are safe.

For each row, the importer takes one of four actions:

| Outcome | What it means | DB write |
|---|---|---|
| `insert` | No existing row with this `external_id`. New customer created. | Yes (skipped in `--dry-run`) |
| `skip-equivalent` | Existing row matches every mapped field. No write. | No |
| `skip-divergent` | Existing row's mapped fields disagree with Stripe (after best-effort field-level normalization). **No write in Phase 0** — diff is logged in the CSV report. Phase 2 will add an `--update-on-conflict` flag for explicit overwrites. | No |
| `error` | Mapping failed, DB write failed, or another exception. Stripe id captured in the report; pipeline continues. | No |

A second run with the same source produces only `skip-equivalent` rows.

## Customer mapping (Phase 0 — implemented)

Stripe `Customer` (`stripe.Customer`) → Velox `domain.Customer` +
`domain.CustomerBillingProfile`:

| Velox field | Stripe field | Notes |
|---|---|---|
| `Customer.ExternalID` | `Customer.ID` | The `cus_...` id. Required. |
| `Customer.DisplayName` | `Customer.Name`, fallback `Customer.Description`, fallback `"(no name)"` | Velox requires non-empty `display_name`. |
| `Customer.Email` | `Customer.Email` | Stripe customers without email are valid; Velox tolerates empty email. |
| `Customer.Status` | n/a | Defaulted to `active`. Stripe doesn't have a one-to-one status field. (Deleted Stripe customers are filtered out by the source iterator using `Customer.Deleted`.) |
| `BillingProfile.LegalName` | `Customer.Name` | Same as DisplayName when absent. |
| `BillingProfile.Email` | `Customer.Email` | Mirrored. |
| `BillingProfile.Phone` | `Customer.Phone` | |
| `BillingProfile.AddressLine1..Country` | `Customer.Address.Line1..Country` | All optional. Stripe address structure maps 1:1 except country which is ISO 3166-1 alpha-2 in both. |
| `BillingProfile.Currency` | `Customer.Currency` | Stripe stores 3-letter lowercase; Velox normalizes to uppercase. |
| `BillingProfile.TaxStatus` | `Customer.TaxExempt` | `none` → `standard`; `exempt` → `exempt`; `reverse` → `reverse_charge`. |
| `BillingProfile.TaxID` + `BillingProfile.TaxIDType` | first entry of `Customer.TaxIDs.Data[]` | Stripe customers can have multiple tax IDs; Phase 0 takes the first only and notes this in the report. Phase 2 may extend the Velox model to support multiple tax IDs per customer. |
| `BillingProfile.ProfileStatus` | n/a | Set to `incomplete` when address is partial, `ready` when address+tax are present, `missing` otherwise. Mirrors Velox's existing rule. |

### Deferred for Phase 0 (logged in CSV, no DB write)

- **Default payment method** (`Customer.InvoiceSettings.DefaultPaymentMethod`).
  Phase 2 imports payment methods + sets up `customer_payment_setups` rows.
  Bringing payment methods over from a separate Stripe account requires
  Connect or a dedicated migration agreement with Stripe Support.
- **Sources / cards on file** (`Customer.Sources`). Same blocker as default
  payment method. Phase 2.
- **Multiple tax IDs**. See above.
- **Subscriptions on the customer** (`Customer.Subscriptions`). Phase 1.

### `livemode` mapping

Velox carries a `livemode BOOLEAN` on every tenant-scoped row (migration
0020). Stripe customers carry an implicit livemode based on which API key
fetched them. The importer:

1. Auto-derives livemode from the API key prefix: `sk_live_` → `true`,
   `sk_test_` → `false`.
2. Allows override via `--livemode-default=true|false` for the rare case
   where the default is wrong (e.g. restricted keys that don't have the
   standard prefix).
3. Stripe `Customer.Livemode` is read but only used as a sanity check — if
   it disagrees with the configured livemode, the row is errored with a
   clear message instead of silently misclassified.

## Subscription mapping (Phase 1 — sketch only)

Velox `subscriptions` ↔ Stripe `Subscription`. Major mapping points:

| Velox field | Stripe field | Notes |
|---|---|---|
| `subscriptions.external_id` | `Subscription.ID` | |
| `subscriptions.customer_id` | resolved from `Subscription.Customer` → Velox customer (must already be imported) | Phase 1 enforces "customers must run first". |
| `subscriptions.plan_id` | resolved from `Subscription.Items.Data[0].Price.Product` → Velox plan (must already be imported via Phase 1 products+prices step) | |
| `subscriptions.status` | `Subscription.Status` | Stripe statuses largely match Velox enum; needs a small mapping table for `paused` vs Velox `paused_collection`. |
| `subscriptions.billing_period_start` / `_end` | `Subscription.CurrentPeriodStart` / `_End` | |
| `subscriptions.cancel_at_period_end` | `Subscription.CancelAtPeriodEnd` | |
| `subscriptions.canceled_at` | `Subscription.CanceledAt` | |
| `subscriptions.trial_start` / `trial_end` | `Subscription.TrialStart` / `_End` | |

**Open question (Phase 1 user-decision):** Stripe Billing supports
`Subscription.Items.Data[]` arrays with multiple line items per subscription.
Velox's current model is one-plan-per-subscription with the Add-ons living
on the subscription as `subscription_items`. The importer needs to translate
"one Stripe sub with N items" → "one Velox sub + (N-1) subscription_items
rows." Will write up the proposal explicitly before coding.

## Products + Prices mapping (Phase 1 — sketch only)

Stripe `Product` → Velox `plans`. Stripe `Price` → Velox `rating_rule_versions`.
Both are heavily lossy — Stripe's pricing model is a denormalized set of
`Price` rows whose tiered/usage/volume modes need to be re-projected onto
Velox's normalized `meter_pricing_rules` introduced by migration 0054.

This is the most engineering-heavy phase. The importer will refuse to import
prices that use Stripe-only features Velox does not yet model (e.g. graduated
tiers with "up-to" hierarchical bands beyond a depth Velox supports), with
a clear pointer to the migration guide for manual recreation.

## Invoice mapping (Phase 2 — sketch only)

Only **finalized + paid** Stripe invoices are imported, and they land in a
read-only "historical" state in Velox. Velox does not re-issue PDFs, does
not re-compute totals, does not re-charge. The point is having a unified
financial history visible in the dashboard, not reproducing Stripe's invoice
generation.

| Velox field | Stripe field | Notes |
|---|---|---|
| `invoices.external_id` | `Invoice.ID` | |
| `invoices.status` | `Invoice.Status` | Only `paid`, `uncollectible`, and `void` are imported. `draft` and `open` are skipped — they belong in source-of-truth Stripe. |
| `invoices.totals[]` | `Invoice.Total` + `Invoice.Currency` | Always-array shape. |
| `invoices.subscription_id` | resolved | Optional now (migration 0060). Imported invoices may legitimately have no Velox subscription if the source Stripe charge was one-off. |
| `invoices.created_at` | `Invoice.Created` | |
| `invoices.metadata` | `Invoice.Metadata` | merged with importer-injected `{"imported_from": "stripe", "imported_at": "<rfc3339>"}` |

Line items are imported one-for-one; Velox's `invoice_line_items.line_type`
defaults to `"imported"` to distinguish them from native Velox items.

## Failure handling

- **Per-row failures don't abort the run.** Errors are logged to the CSV
  report with the Stripe id and a human-readable error. The summary line at
  the end reports `imported / skipped / errored / total` counts. A run with
  any non-zero `errored` count exits with status 1 so CI / cron treats it as
  a failure even though some rows succeeded.
- **API-level failures (network, 429s)** are retried with exponential
  backoff up to 5 attempts using stripe-go's built-in retry middleware. A
  permanent API failure (auth, 404 on the resource list endpoint) aborts
  the run before any rows are processed.
- **DB-level failures during a row write** (constraint violation, RLS error)
  are logged at the row level. Connection-pool exhaustion or `context.Canceled`
  aborts the run.

## CSV report format

```
stripe_id,resource,action,velox_id,error
cus_NfJG2N4m6X,customer,insert,cust_01H...,
cus_NaJ12P,customer,skip-equivalent,cust_01H...,
cus_NbR99,customer,skip-divergent,cust_01H...,email Stripe="alice@example.com" Velox="alice@old.example.com"
cus_NcZ87,customer,error,,map: empty stripe id
```

Columns:

- `stripe_id` — the source `cus_...` / `sub_...` / etc.
- `resource` — `customer`, `subscription`, `product`, `price`, `invoice`
- `action` — `insert`, `skip-equivalent`, `skip-divergent`, `error`
- `velox_id` — Velox internal id when one exists, else empty
- `error` — human-readable description for `error` and `skip-divergent`
  rows; empty otherwise

## Cutover playbook (Phase 4 — outline only)

The full migration cutover lives in a separate doc once Phase 3 ships.
The shape:

1. **Parallel-run** — the source Stripe account stays authoritative for
   billing for N days. Velox imports nightly + reports drift.
2. **Webhook redirect** — Stripe webhooks for the source account are
   re-pointed to Velox's `POST /v1/webhooks/stripe`. Velox's webhook handler
   already does this.
3. **DNS / API cutover** — partner traffic moves from Stripe Billing
   endpoints to Velox endpoints. Velox starts being authoritative for new
   subscriptions, plan changes, invoice generation.
4. **Reconciliation window** — a tail of Stripe-source invoices may finalize
   after cutover. Reconcile by running the importer on a 14-day moving
   window in `--dry-run --resource=invoices` mode and reviewing the diff.
5. **Stop the importer.** Source Stripe account becomes archive-only.

## Testing strategy

- **Unit**: pure mapping function tests with realistic fixture JSON
  copy-pasted from Stripe's API docs sample responses (under
  `internal/importstripe/testdata/`).
- **Integration**: a real Postgres test database (the standard
  `-short=false` path) with a mock Stripe API client implemented as a
  fake `Source` interface. The fake yields canned customers; the importer
  walks them and writes to Velox. The test asserts:
  - First run: every customer is inserted.
  - Second run (no source changes): every customer is `skip-equivalent`.
  - Second run with one Stripe-side change: the changed customer reports
    `skip-divergent` with the diff captured in the CSV.
  - `--dry-run` mode produces the same CSV but zero DB rows.
- **Smoke**: a manual recipe (in this doc, see "Running locally") for
  testing against a real Stripe test account.

## Running locally

```bash
# 1. Bring up Velox normally (creates a tenant via bootstrap).
docker compose up -d postgres
DATABASE_URL="postgres://velox:velox@localhost:5432/velox?sslmode=disable" \
  go run ./cmd/velox-bootstrap

# 2. Run the importer in dry-run mode against a Stripe test account.
DATABASE_URL="postgres://velox:velox@localhost:5432/velox?sslmode=disable" \
  go run ./cmd/velox-import \
    --tenant=ten_xxxxxxxx \
    --api-key=sk_test_xxxxxxxxxxxx \
    --resource=customers \
    --dry-run

# 3. Inspect the CSV at ./velox-import-<timestamp>.csv. Re-run without
#    --dry-run when satisfied.
```

## Open questions / explicit non-goals

- **Multi-tenant Stripe accounts.** A single Stripe Connect platform may
  have many connected accounts. Phase 0 imports from one Stripe account at
  a time. Multi-account loops belong in operator scripts on top of this CLI.
- **Schema migration.** The importer does not run `migrate.Up()` itself.
  Operators bring up Velox first.
- **Reverse direction.** Velox-to-Stripe export is out of scope for this
  90-day plan.
- **Webhook backfill.** Past Stripe webhook deliveries are not replayed.
  The migration guide will explicitly call this out.
