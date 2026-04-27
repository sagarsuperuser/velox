# Migrating from Stripe Billing to Velox

Companion to [docs/design-stripe-importer.md](./design-stripe-importer.md)
and the four importer phases shipped in PRs #47 / #51 / #52 / #53. The
design doc answers "how does the importer map Stripe objects onto
Velox?"; this doc answers "I run a SaaS today on Stripe Billing, my
customers are charged tomorrow, and I want to be on Velox by month-
end without missing an invoice — what's the playbook?"

The guide is structured as a calendar: a 14-day parallel-run window
with explicit T-14 / T-7 / T-0 / T+1 / T+14 milestones, plus a Phase F
rollback playbook for the case the cutover goes wrong. Every step is
operator-runnable with the actual `velox-import` invocation, the
actual SQL recipe, and the actual env var. No section is aspirational
— if a feature isn't built, the doc says so and points at the manual
workaround.

Velox's importer covers Stripe customers, products, prices, finalized
invoices, and subscriptions — the surface of a typical Stripe Billing
tenant. Schedules, Quotes, Promotion Codes, multi-item subscriptions,
graduated/tiered prices, metered usage type, Connect, and
draft/open invoices are out of scope today. The "Known limitations"
section enumerates each and prescribes the manual recreation path.

## Table of contents

- [1. Pre-migration checklist](#1-pre-migration-checklist)
- [2. The five importer phases](#2-the-five-importer-phases)
- [3. Rehearsal run (test mode)](#3-rehearsal-run-test-mode)
- [4. Production parallel-run cutover playbook](#4-production-parallel-run-cutover-playbook)
- [5. Reconciliation toolkit](#5-reconciliation-toolkit)
- [6. Webhook redirection strategy](#6-webhook-redirection-strategy)
- [7. Known limitations on import](#7-known-limitations-on-import)
- [8. FAQ](#8-faq)

---

## 1. Pre-migration checklist

Run this list before T-14. Anything unchecked is a reason to extend
the parallel-run window or defer the cutover.

### Velox tenant provisioned

A production Velox deployment with a tenant created via
`cmd/velox-bootstrap` and the first user signed in. Verify:

```bash
# Velox API is up and the bootstrap finished.
curl -fsS "$VELOX_BASE_URL/health/ready" | grep -q '"status":"ok"'

# A tenant exists. Replace ten_… with the id printed by velox-bootstrap.
psql "$DATABASE_URL" -c "SELECT id, name, created_at FROM tenants;"
```

The Velox tenant id (`ten_…`) is the `--tenant` flag for every
`velox-import` invocation below.

### Stripe credentials

Two scenarios:

- **Production cutover (recommended path).** A Stripe **restricted
  key** with read-only scope on Customers, Products, Prices,
  Subscriptions, and Invoices. Stripe Dashboard → Developers → API
  keys → Create restricted key. The importer reads only — never
  writes — but using a restricted key over a full secret key is the
  defensive default. Pass it as `--api-key=rk_live_…`. The importer
  recognises the `rk_live_` / `rk_test_` prefix and derives livemode
  from it (see `cmd/velox-import/main.go::resolveLivemode`).
- **Rehearsal / parallel-run prep.** A `sk_test_…` from the same
  Stripe account. Test-mode customers, products, prices, subscriptions,
  and invoices import the same way as live, isolated at the database
  layer by Velox's `livemode` column on every tenant-scoped row
  (migration `0020_test_mode`).

The importer reads the `Customer.Livemode` flag on every row and
errors out on a livemode mismatch — so a `sk_live_…` key cannot
silently land test-mode rows under a livemode=true Velox tenant, and
vice versa.

### Postgres + encryption keys

Velox encrypts customer PII, webhook signing secrets, and per-tenant
Stripe credentials at rest under
[`VELOX_ENCRYPTION_KEY`](./ops/encryption-at-rest.md#whats-encrypted)
(AES-256-GCM, 32 bytes hex). The customer email blind index runs
under [`VELOX_EMAIL_BIDX_KEY`](./ops/encryption-at-rest.md#whats-encrypted)
(HMAC-SHA256, also 32 bytes hex). In production, `APP_ENV=production`
makes a missing `VELOX_ENCRYPTION_KEY` fatal at startup — verify
both keys are wired before any customer is imported, otherwise the
imported PII lands in plaintext on disk.

```bash
# 1. Both keys present and 64 hex chars each (32 bytes).
test "$(printf '%s' "$VELOX_ENCRYPTION_KEY" | wc -c | tr -d ' ')" = "64"
test "$(printf '%s' "$VELOX_EMAIL_BIDX_KEY" | wc -c | tr -d ' ')" = "64"

# 2. Migrations applied. CheckSchemaReady runs at startup; this is
#    the same check, against the live DB.
psql "$DATABASE_URL" -c "
  SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1;
"
# Expected: at least 0063 (Phase 3's stripe_invoice_id migration).

# 3. Encryption is in effect — no plaintext PII rows.
psql "$DATABASE_URL" -c "
  SELECT count(*) AS plaintext_email_rows
  FROM customers
  WHERE email <> '' AND email NOT LIKE 'enc:%';
"
# Expected: 0 (no rows imported yet, so trivially zero).
```

If either key is missing or malformed, fix that **before** running
the importer. There is no built-in re-encryption command —
[`docs/ops/encryption-at-rest.md` → Key rotation: NOT IMPLEMENTED](./ops/encryption-at-rest.md#key-rotation-not-implemented-today)
documents the gap. A row written under a noop encryptor stays in
plaintext until manually re-encrypted.

### Customer + price catalog complexity assessment

The importer is deliberately conservative — anything Velox does not
faithfully model surfaces as an `error` row in the CSV report rather
than a silent partial import. Run this assessment **before** T-14 and
maintain a manual checklist of everything that needs human attention:

| Stripe feature | Importer behaviour | Manual action |
|---|---|---|
| Tiered / graduated / volume pricing (`billing_scheme=tiered`) | Phase 1 errors the price row (`tiered billing_scheme not supported in Phase 1; recreate the rating rule manually`) | Recreate as Velox `meter_pricing_rules` with `dimension_match` + per-rule rates, or as separate flat plans. See [`docs/design-multi-dim-meters.md`](./design-multi-dim-meters.md). |
| Metered usage type (`usage_type=metered`) | Phase 1 errors the price row | Use Velox's multi-dimensional meters + `pricing_rule.aggregation_mode` (sum / count / max / last_during_period / last_ever) |
| Multi-item subscriptions (`Subscription.Items.Data[]` length > 1) | Phase 2 errors the subscription row (`Phase 2 supports single-item subs only; recreate multi-item subscription manually`) | Split into multiple single-item Velox subs, or use Velox's subscription items + add-on model |
| One-time prices, day/week intervals (`type=one_time`, `interval=day`/`week`, `interval_count != 1`) | Phase 1 errors the price row | Create a flat-amount Velox plan or one-off invoice |
| Schedules (`Subscription.Schedule`) | Phase 2 emits a CSV note; no schedule object created in Velox | Recreate manually if used |
| Promotion Codes | Out of scope | Migrate the underlying Coupon to Velox's coupon model, ignore the public-code surface |
| Quotes | Out of scope | Stays in Stripe for audit |
| Connect / multi-party | Out of scope (single-tenant per Velox deployment) | Re-architecture; flag explicitly to your account rep |
| `pause_collection` | Phase 2 emits a CSV note; only `paused` status maps | Recreate behaviour via Velox's `pause-collection` endpoint after import |
| Multiple tax IDs per customer | Phase 0 imports the first; emits a CSV note | Manually add the rest via the API |
| `Subscription.Items.PriceData` (inline ad-hoc pricing) | No matching Velox price; Phase 2 errors out | Create the price up front in Stripe (so Phase 1 can import it), or recreate in Velox |

A `--dry-run` rehearsal (Section 3) is the cheapest way to enumerate
which rows fall into each bucket. Sum the `error` count from the CSV
across all five resources — that's the size of the manual-recreation
backlog.

### Webhook receiver inventory

List every downstream system subscribed to your Stripe account's
webhooks today. Stripe Dashboard → Developers → Webhooks. Catalog:

- Each endpoint URL and the events it subscribes to.
- Whether the consumer is internal (your own services) or external
  (a partner integration).
- The signature-verification mechanism each consumer uses (Stripe's
  `stripe-signature` header HMAC, or their own).

This list is the input to Section 6 (webhook redirection): every
endpoint subscribed to subscription / invoice / charge events on
Stripe will need to be repointed at Velox's outbound webhooks at T-0,
or kept on Stripe for events Velox is not authoritative for
(payment-related events still flow through Stripe).

### Timeline expectation

A **2-week parallel-run window** is the
recommended cutover shape — both Stripe Billing and Velox running,
Velox dark) for 2 weeks before flipping primary"). Plan on:

| Phase | Window | What's running |
|---|---|---|
| Phase A — Prepare | T-14 → T-7 | Velox installed, dry-run rehearsal complete |
| Phase B — Initial backfill | T-7 → T-0 | Real import, reconciliation green |
| Phase C — Parallel run (dark) | T-7 → T-0 | Both Stripe Billing and Velox observe; Stripe Billing remains authoritative for invoice generation |
| Phase D — Cutover | T-0 | Velox takes over invoice generation; webhook receivers repointed |
| Phase E — Stabilize | T-0 → T+14 | Daily reconciliation, customer-support escalation playbook |
| Phase F — Rollback | any time | If invoice cycle breaks within the first cycle post-cutover |

If your tenant is over 10k customers, or if the rehearsal CSV shows
> 1% error rows, extend the parallel-run window to 4 weeks. The cost
of the extra two weeks is much cheaper than the cost of an incident
on the primary cycle.

---

## 2. The five importer phases

The importer is a single binary, `cmd/velox-import`, with one CLI
flag (`--resource=...`) selecting which phases to run. Resources
execute in **strict dependency order regardless of CLI input order**:
customers → products → prices → subscriptions → invoices. A second
run with the same source is idempotent at every layer — every row
resolves to one of `insert`, `skip-equivalent`, `skip-divergent`, or
`error` (see `internal/importstripe/types.go::Action`).

The four phase PRs:

- **Phase 0 — Customers** ([PR #47](https://github.com/sagarsuperuser/velox/pull/47))
- **Phase 1 — Products + Prices** ([PR #51](https://github.com/sagarsuperuser/velox/pull/51))
- **Phase 2 — Subscriptions** ([PR #52](https://github.com/sagarsuperuser/velox/pull/52))
- **Phase 3 — Finalized Invoices** ([PR #53](https://github.com/sagarsuperuser/velox/pull/53))

### Phase 0 — Customers

What it does: maps Stripe `Customer` rows onto Velox `customers` +
`customer_billing_profiles` rows. Idempotency anchors on
`customers.external_id = stripe.customer.id` (Velox's existing
`UNIQUE (tenant_id, livemode, external_id)` from migration `0020`).

```bash
DATABASE_URL="$DATABASE_URL" velox-import \
  --tenant="$VELOX_TENANT_ID" \
  --api-key="$STRIPE_API_KEY" \
  --resource=customers \
  --dry-run
```

What it skips with CSV `error` rows:
- Customers without an `id` field (defensive belt-and-braces against
  Stripe API anomalies).
- Customers whose `livemode` field disagrees with the configured
  `--livemode-default` or the api-key prefix (caught by the iterator,
  surfaced as an explicit error rather than silently misclassifying).

What it skips with CSV notes:
- Multiple tax IDs (only the first is imported; rest noted).
- Default payment method + sources (Phase 0 doesn't carry payment
  methods over — that requires Stripe Connect or a dedicated
  migration agreement).

Expected output: one CSV row per Stripe customer, with a summary at
the end (`inserted / skip-equivalent / skip-divergent / errored /
total`).

### Phase 1 — Products + Prices

What it does: maps Stripe `Product` rows onto Velox `plans` (plan
`code` = Stripe Product ID for idempotent lookup; plan name +
description copied verbatim) and Stripe `Price` rows onto Velox
`rating_rule_versions` (rule_key = Stripe Price ID; mode = flat;
flat_amount_cents = Stripe `unit_amount`).

```bash
DATABASE_URL="$DATABASE_URL" velox-import \
  --tenant="$VELOX_TENANT_ID" \
  --api-key="$STRIPE_API_KEY" \
  --resource=products,prices \
  --dry-run
```

What it skips with CSV `error` rows:
- Tiered / graduated / volume billing scheme (`billing_scheme=tiered`)
- One-time prices (`type=one_time`)
- Metered usage type (`usage_type=metered`)
- Day / week intervals or `interval_count != 1`
- Prices that reference a product not yet imported (`plan with code
  prod_xxx not found; run --resource=products first`)

What it skips with CSV notes:
- Currency / billing-interval disagreements between a price and its
  parent plan (the importer surfaces the mismatch but doesn't
  overwrite the plan's currency or interval — those are immutable in
  `pricing.Service.UpdatePlan`).

Expected output: one CSV row per Stripe Product, then one CSV row per
Stripe Price.

### Phase 2 — Subscriptions

What it does: maps Stripe `Subscription` rows onto Velox
`subscriptions` rows with `subscription.code = stripe_subscription_id`
as the import-stable key (so reruns are idempotent). Customer
resolution via `customers.external_id = stripe.customer.id`; price →
plan resolution via `item.price.product.id` matched against `plan.code`.

```bash
DATABASE_URL="$DATABASE_URL" velox-import \
  --tenant="$VELOX_TENANT_ID" \
  --api-key="$STRIPE_API_KEY" \
  --resource=subscriptions \
  --dry-run
```

What it skips with CSV `error` rows:
- Multi-item subscriptions (`Subscription.Items.Data[]` length > 1)
  — `Phase 2 supports single-item subs only; recreate multi-item
  subscription manually`.
- Subscriptions whose customer hasn't been imported yet.
- Subscriptions whose price's product hasn't been imported yet.

Status mapping (with notes for non-direct mappings):
- `active`, `trialing`, `canceled`, `paused` → direct.
- `past_due` → `active` (Velox tracks dunning on invoices, not
  subscriptions — the past-due state lives in the invoice +
  payment-attempt machinery).
- `unpaid`, `incomplete`, `incomplete_expired` → `canceled` (terminal
  in Velox).
- Unknown future Stripe states → `canceled` with a CSV note so
  operators can spot them in the report.

What it skips with CSV notes:
- Discounts (use Velox's coupon model after import).
- `subscription_schedule` (Velox doesn't model schedules — captured
  in note for operator follow-up).
- `pause_collection.behavior` + `resumes_at` (Velox's `paused`
  status doesn't carry the same metadata).
- `default_tax_rates`, `billing_thresholds` (out of scope today).

Expected output: one CSV row per Stripe subscription, with notes for
any lossy mappings.

### Phase 3 — Finalized Invoices

What it does: maps Stripe `Invoice` rows in terminal status (`paid`,
`void`, `uncollectible`) onto Velox `invoices` + 1..N
`invoice_line_items` rows, inserted atomically via
`CreateWithLineItems`. Idempotency anchors on
`invoices.stripe_invoice_id` with a partial unique index (`WHERE
stripe_invoice_id IS NOT NULL`) added by migration
`0063_invoices_stripe_invoice_id` — Velox-native invoices keep using
`invoice_number` (the per-tenant sequential allocator) and never
collide.

```bash
DATABASE_URL="$DATABASE_URL" velox-import \
  --tenant="$VELOX_TENANT_ID" \
  --api-key="$STRIPE_API_KEY" \
  --resource=invoices \
  --dry-run
```

What it skips with CSV `error` rows:
- Drafts and open invoices (`status=draft` or `status=open`) — they
  belong in Stripe until terminal. Operators settle / void /
  write-off in Stripe and re-run the importer.
- Invoices whose customer hasn't been imported.
- Invoices whose subscription (when present) hasn't been imported.

Status mapping:
- `paid` → Velox `paid` + `payment_status=succeeded`.
- `void` → Velox `voided` + `payment_status=failed`.
- `uncollectible` → Velox `voided` + `payment_status=failed` with a
  CSV note (Velox lacks an `uncollectible` status; voided is the
  closest terminal state).

Billing-reason mapping:
- `subscription_create`, `subscription_cycle`, `subscription_threshold`,
  `manual` → direct.
- `subscription_update` → `manual` (Velox doesn't model the update
  reason; manual is the most accurate fallback).
- Legacy `subscription`, `quote_accept`,
  `automatic_pending_invoice_item_invoice`, `upcoming`, empty/unknown
  → `manual` with notes preserving the original Stripe distinction.

What it skips with CSV notes:
- Discounts, `amount_shipping`, `default_tax_rates`, multi-rate tax
  splits, hosted-invoice URL — each captured in the note rather than
  silently dropped or fabricated.

Expected output: one CSV row per Stripe invoice, with `inserted`
counts dominating on a fresh tenant and `skip-equivalent` dominating
on reruns.

### Running all phases at once

```bash
DATABASE_URL="$DATABASE_URL" velox-import \
  --tenant="$VELOX_TENANT_ID" \
  --api-key="$STRIPE_API_KEY" \
  --resource=customers,products,prices,subscriptions,invoices \
  --dry-run
```

Resources execute in dependency order regardless of input order. The
single CSV report aggregates rows across all five resources, with the
final summary line showing the cross-resource totals.

---

## 3. Rehearsal run (test mode)

Run the importer end-to-end against a Stripe **test** account before
attempting any production data. The rehearsal is the cheapest way to
exercise the full import + map + lookup + CSV-emission path without
risking a single production row.

### Step 1 — Dry-run all five resources

```bash
DATABASE_URL="$DATABASE_URL" velox-import \
  --tenant="$VELOX_TENANT_ID" \
  --api-key="sk_test_…" \
  --resource=customers,products,prices,subscriptions,invoices \
  --dry-run \
  --output=./rehearsal-dryrun.csv
```

The `--api-key=sk_test_…` prefix derives livemode=false automatically;
the importer tags every row written under `livemode=false` so test
data never bleeds into the live tenant scope (RLS at the Postgres
layer enforces this independently of the application's livemode
tagging — see migration `0020_test_mode`).

### Step 2 — Inspect the CSV

```bash
# Summary by action across all phases.
awk -F',' 'NR>1 {a[$3]++} END {for (k in a) print k, a[k]}' rehearsal-dryrun.csv

# Expected output shape:
#   insert 1234
#   skip-equivalent 0       (first run — nothing exists yet)
#   skip-divergent 0        (first run)
#   error 12

# Drill into errors specifically — these are the manual-recreation backlog.
grep ',error,' rehearsal-dryrun.csv | awk -F',' '{print $2 "\t" $5}' | sort | uniq -c | sort -rn
```

A non-zero `error` count is **expected** on most real Stripe accounts
— the catalog above (Section 1) lists exactly which Stripe features
generate errors. The rehearsal is how you size that backlog before
putting calendar pressure on the cutover.

### Step 3 — Resolve out-of-scope items

For each error category surfaced in step 2, decide:

- **Recreate manually in Velox** — the most common path for tiered
  prices, multi-item subs, schedules, promotion codes, etc. Use the
  Velox API or dashboard.
- **Defer in Velox, keep in Stripe** — for one-offs that aren't
  worth the recreation cost (an old promotion code on a single
  customer, say).
- **Block the cutover** — for systemic issues like "every
  subscription has > 1 item" that signal Velox isn't yet a fit.

Document each decision in your cutover runbook (a separate
operator-side doc, not Velox's).

### Step 4 — Real run (no `--dry-run`)

```bash
DATABASE_URL="$DATABASE_URL" velox-import \
  --tenant="$VELOX_TENANT_ID" \
  --api-key="sk_test_…" \
  --resource=customers,products,prices,subscriptions,invoices \
  --output=./rehearsal-real.csv
```

Compare row counts between `rehearsal-dryrun.csv` and
`rehearsal-real.csv`:

```bash
diff <(awk -F',' 'NR>1 {print $1, $2, $3}' rehearsal-dryrun.csv | sort) \
     <(awk -F',' 'NR>1 {print $1, $2, $3}' rehearsal-real.csv | sort)
```

The diff should be empty — `--dry-run` walks the same mapping +
lookup pipeline as the real run; only the DB-write step is suppressed.
Any divergence between the two CSVs is a bug to investigate before
the production import.

### Step 5 — Idempotency check

Run the real import a second time without changing anything in
Stripe:

```bash
DATABASE_URL="$DATABASE_URL" velox-import \
  --tenant="$VELOX_TENANT_ID" \
  --api-key="sk_test_…" \
  --resource=customers,products,prices,subscriptions,invoices \
  --output=./rehearsal-rerun.csv
```

Every row should resolve to `skip-equivalent`. A non-zero `insert` or
`skip-divergent` count on a no-change rerun signals either (a) the
importer's mapper is non-deterministic for some field (file a bug),
or (b) Stripe's API is returning different data on consecutive calls
(rare; usually pagination cursors or expand-defaults). Investigate
before the production run.

---

## 4. Production parallel-run cutover playbook

The core of this guide. Walks the 14-day window from initial backfill
through cutover and stabilization, with explicit rollback at the end
for the case the cutover doesn't hold.

### Phase A — Prepare (T-14 days)

Goal: Velox is provisioned, the dry-run is green, and every error
row has a documented manual-recreation plan.

1. **Bootstrap a Velox tenant for production.** `cmd/velox-bootstrap`
   creates the first tenant + first user. Verify with:
   ```bash
   psql "$DATABASE_URL" -c "
     SELECT t.id, t.name, t.created_at, count(u.id) AS user_count
     FROM tenants t LEFT JOIN users u ON u.tenant_id = t.id
     GROUP BY t.id, t.name, t.created_at;
   "
   ```
2. **Run the importer against the LIVE Stripe account in `--dry-run`
   mode.** Use a restricted (read-only) `rk_live_…` key, not a full
   `sk_live_…`. The importer never writes to Stripe, but defense in
   depth is cheap:
   ```bash
   DATABASE_URL="$DATABASE_URL" velox-import \
     --tenant="$VELOX_TENANT_ID" \
     --api-key="rk_live_…" \
     --resource=customers,products,prices,subscriptions,invoices \
     --dry-run \
     --output=./prod-tminus14-dryrun.csv
   ```
3. **Resolve every error row.** Section 1's complexity assessment
   table maps each error to its manual-recreation path. Track each
   resolution in your cutover runbook. **Do not move past T-7 with
   unresolved errors** — the parallel-run window's whole purpose is
   to surface and fix these before they affect billing.
4. **Set up monitoring.** Velox metrics scrape to Prometheus at
   `GET /metrics`; alerts wire to PagerDuty / Slack per
   [`docs/ops/runbook.md` → Alert catalog](./ops/runbook.md#alert-catalog).
   At minimum, the following must be wired before T-7:
   - `velox_billing_cycle_errors_total` — page on first non-zero
     value post-cutover.
   - `velox_invoices_generated_total` — alert on a 6-hour gap during
     business hours.
   - `velox_payment_charges_total{result="failed"}` — alert on
     elevated failure rate vs the rolling baseline.
   - `velox_webhook_deliveries_total{status="failed"}` — alert on
     elevated failure rate.
   - `velox_audit_write_errors_total` — page (audit fail-closed
     posture means rejections cascade into customer-facing 503s).

### Phase B — Initial backfill (T-7 days)

Goal: every Stripe customer / product / price / subscription /
invoice has a corresponding row in Velox. Reconciliation is green
across the board.

1. **Run the importer for real (no `--dry-run`).** Same command as
   Phase A step 2 minus the `--dry-run` flag:
   ```bash
   DATABASE_URL="$DATABASE_URL" velox-import \
     --tenant="$VELOX_TENANT_ID" \
     --api-key="rk_live_…" \
     --resource=customers,products,prices,subscriptions,invoices \
     --output=./prod-tminus7-real.csv
   ```
2. **Verify customer count match.** Count Stripe customers via the
   API and compare against Velox:
   ```bash
   # Stripe side — count customers using the dashboard or API.
   #   curl https://api.stripe.com/v1/customers/search \
   #     -u "rk_live_…:" -d "query=created>'1970-01-01'"
   # The total_count field in the response is authoritative.

   # Velox side.
   psql "$DATABASE_URL" -c "
     SELECT count(*) AS velox_customers
     FROM customers
     WHERE livemode = true;
   "
   ```
   The numbers should match within the size of any rows that errored
   in step 1 (i.e. `velox_customers = stripe_customers - error_rows`).
3. **Verify subscription active count match.**
   ```bash
   # Velox side — active subscriptions imported from Stripe.
   psql "$DATABASE_URL" -c "
     SELECT count(*) AS velox_active_subs
     FROM subscriptions
     WHERE status = 'active'
       AND code LIKE 'sub_%'
       AND livemode = true;
   "
   ```
   Compare against Stripe's count of `status=active` subscriptions
   for the same Stripe account. The `code LIKE 'sub_%'` predicate
   filters to importer-created rows (Velox-native subs use
   `subscription.code` formats that don't start with `sub_`).
4. **Verify last-90-days invoice count match.**
   ```bash
   # Velox side — finalized invoices imported from Stripe in the last 90d.
   psql "$DATABASE_URL" -c "
     SELECT count(*) AS velox_recent_invoices
     FROM invoices
     WHERE stripe_invoice_id IS NOT NULL
       AND created_at >= now() - INTERVAL '90 days'
       AND livemode = true;
   "
   ```
   Compare against Stripe's `Invoice.list` filtered by
   `created>=<epoch_90d_ago>` and `status=paid` ∪ `status=void` ∪
   `status=uncollectible`. The `stripe_invoice_id IS NOT NULL`
   predicate filters to importer-created rows; Velox-native invoices
   have a NULL `stripe_invoice_id` (the partial unique index from
   migration `0063` only enforces uniqueness when the column is
   non-null).
5. **Daily reconciliation kicks in.** From T-7 onwards, run the
   importer with `--since` for a 24-hour window every day at the
   same wall-clock time:
   ```bash
   YESTERDAY="$(date -u -v-1d '+%Y-%m-%d')"   # macOS / BSD
   # YESTERDAY="$(date -u -d '1 day ago' '+%Y-%m-%d')"  # GNU coreutils
   DATABASE_URL="$DATABASE_URL" velox-import \
     --tenant="$VELOX_TENANT_ID" \
     --api-key="rk_live_…" \
     --resource=customers,subscriptions,invoices \
     --since="$YESTERDAY" \
     --output="./prod-recon-$(date -u +%Y%m%d).csv"
   ```
   The expected outcome on a healthy parallel run is a CSV with
   `insert` rows for new customers / subscriptions / invoices Stripe
   created in the last 24h, and zero `skip-divergent` rows. Any
   `skip-divergent` row is a discrepancy to investigate **before**
   the cutover — see [Section 5](#5-reconciliation-toolkit) for the
   reconciliation toolkit.

### Phase C — Parallel run (T-14 to T-0)

Goal: Velox runs **dark** alongside Stripe Billing for ~2 weeks.
Stripe Billing remains authoritative. Velox observes, doesn't act.

The parallel-run posture:

- **Both systems receive new state.** Customers, subscriptions, and
  usage events created on the operator's app go to both Stripe (via
  whatever existing wiring) and Velox (via fresh API calls from the
  app, or via the daily importer).
- **Velox does not charge cards.** During parallel run, Velox's
  billing scheduler runs its normal cycle and writes Velox-native
  invoices for *new* subscriptions created post-T-7, but the
  PaymentIntent confirmation still flows through Stripe Billing's
  existing wiring on the operator's end. Velox's billing engine sees
  the imported subscriptions as having `next_billing_at` in the past
  on day one — finalize them as **dry runs** by leaving the Stripe
  Billing flow authoritative, or pause Velox's scheduler entirely
  for the parallel-run window (see Phase F's pause mechanism).
  The defensive default is to point Velox at a *separate* Stripe
  Connect credential that has no live charge capability during
  parallel run, so even if a billing-cycle finalize accidentally
  triggers a PaymentIntent, the charge fails closed.
- **Daily reconciliation.** The Phase B step 5 cron runs daily.
  Confirm zero `skip-divergent` rows. If reconciliation diverges
  (e.g. Stripe reports 1042 active subs but Velox reports 1039 on
  the same day), do not cut over — extend the parallel run and
  investigate. The most common divergence sources are (a) a Stripe
  Billing event that never made it to Velox's app integration, and
  (b) timezone/clock skew between Stripe's `created` field and
  Velox's `created_at`.
- **Webhook redirection prep.** Stripe webhooks still hit the
  legacy receiver. Velox's outbound webhooks fire to a **shadow
  endpoint** that logs but doesn't act. Configure each downstream
  consumer with two webhook subscriptions during this window: their
  existing Stripe subscription (still authoritative) and a Velox
  shadow subscription (used to validate payload shape and signature
  verification before cutover). See [Section 6](#6-webhook-redirection-strategy)
  for the full mechanics.

**Failure mode.** If reconciliation diverges in unexpected ways —
new categories of error rows, customer counts drifting day over day,
unknown Stripe subscription statuses — the parallel run is failing
and the cutover should slip. Do not cut over with known divergence.
The cost of an extra week is much smaller than the cost of an
incident on the primary cycle. The 90-day plan's Week-12 row
explicitly anticipates this: "First-cutover incident causes partner
churn — Parallel-run window... for 2 weeks before flipping primary."

### Phase D — Cutover (T-0)

Goal: at the end of this phase, Velox is authoritative for invoice
generation and customer-facing billing emails, while Stripe still
processes the actual card charges via PaymentIntent.

Run this phase during a **maintenance window** announced to customers
in advance. Plan on 2-4 hours; the actual switching is fast (minutes)
but the verification afterwards needs a calm pace.

1. **Disable Stripe Billing's automatic invoice generation.** The
   exact mechanism depends on your Stripe Billing setup:
   - If using Stripe Billing Subscriptions, set
     `pause_collection.behavior = "void"` on every active Stripe
     subscription, or update each subscription with
     `cancel_at_period_end = true` and let it ride out one final
     Stripe-issued invoice before Velox takes over.
   - If using Stripe Invoices via `Invoice.create`, simply stop the
     code path that calls `Invoice.create` on cycle.
   The defensive choice is to pick one of these mechanisms in
   advance and document the exact API call in the cutover runbook —
   not improvise on the day.
2. **Velox takes over invoice generation + finalization.** Velox's
   billing scheduler runs every hour by default in production
   (`billingInterval := 1 * time.Hour` in `cmd/velox/main.go:117`).
   On the first tick post-cutover, it generates invoices for every
   subscription whose `next_billing_at` has passed. The first cycle
   is the highest-risk moment of the entire migration — staff
   on-call for the duration.
3. **Webhook receivers' Stripe subscription is paused; Velox
   webhooks now hit production endpoints.** Section 6 walks the full
   mechanics. The short version: each downstream consumer's Stripe
   webhook subscription is set to `disabled = true` (Stripe Dashboard
   → Webhooks → toggle), and their Velox shadow subscription is
   promoted from "log only" to "act on payload." The signing-secret
   primary + secondary rotation grace built into Velox
   ([migration `0049_webhook_secondary_secret`](../internal/platform/migrate/sql/0049_webhook_secondary_secret.up.sql))
   gives a 72h overlap window where consumers can verify both old
   and new secrets — set the primary at T-7 and let it stabilise
   before promoting.
4. **Inbound Stripe PaymentIntent webhooks still arrive at Velox.**
   The Stripe webhook for `payment_intent.succeeded` /
   `.failed` / `.processing` continues to flow into Velox's
   `POST /v1/webhooks/stripe` endpoint — this is the path that
   confirms a charge after a card-present moment. Velox handles
   inbound Stripe webhooks via per-tenant credentials in
   `stripe_provider_credentials` (`internal/tenantstripe/postgres.go`),
   so the cutover doesn't change the inbound flow. Verify the
   tenant's Stripe webhook signing secret is registered in Velox via
   the dashboard or the API, and that
   `stripe_provider_credentials.webhook_secret_encrypted` is non-NULL:
   ```bash
   psql "$DATABASE_URL" -c "
     SELECT tenant_id, livemode, stripe_account_id,
            (webhook_secret_encrypted IS NOT NULL) AS has_webhook_secret,
            verified_at
     FROM stripe_provider_credentials
     WHERE livemode = true;
   "
   ```
5. **Customer-facing emails (invoice ready, payment failed,
   dunning) now flow from Velox.** Verify Velox's email provider is
   configured (`SMTP_*` env vars or whatever delivery integration
   was set during initial install) and that test emails arrive.
   Velox's branded multipart email templates are documented in the
   self-host README; the wire on a live tenant is the same path
   exercised during initial bootstrap. **If your downstream comms
   stack still emits Stripe-Billing-themed emails (e.g. via
   Stripe's hosted invoice email), disable that path before T-0 to
   avoid double-sending.**
6. **Audit log entry on the cutover event.** Velox's catch-all
   audit middleware doesn't write rows for read-only health-check
   traffic; the cutover itself should be marked explicitly via an
   operator-issued POST that lands in `audit_log`. The cleanest
   pattern is to update a `tenant_settings.metadata` JSONB key with
   a cutover timestamp:
   ```bash
   curl -X PATCH "$VELOX_BASE_URL/v1/tenant/settings" \
     -H "Authorization: Bearer $VELOX_SECRET_KEY" \
     -H "Content-Type: application/json" \
     -d '{
       "metadata": {
         "cutover_from_stripe_billing_at": "2026-05-01T00:00:00Z",
         "cutover_runbook_version": "1"
       }
     }'
   ```
   The PATCH writes an `audit_log` row via the catch-all middleware
   with `action=update`, `resource_type=tenant_settings`, and the
   request body in `metadata.path` — that's the auditable evidence
   of the cutover instant.

### Phase E — Stabilize (T+1 to T+14)

Goal: 14 days of clean post-cutover operation. By T+14, the migration
is done.

1. **Daily metric checks.** A simple dashboard with the following
   Prometheus queries provides the high-signal view:
   ```promql
   # Invoice generation cadence (a 6-hour silence during business
   # hours signals a stalled scheduler).
   sum(rate(velox_invoices_generated_total[1h]))

   # Payment success rate.
   sum(rate(velox_payment_charges_total{result="succeeded"}[5m]))
     / sum(rate(velox_payment_charges_total[5m]))

   # Webhook delivery rate.
   sum(rate(velox_webhook_deliveries_total{status="succeeded"}[5m]))
     / sum(rate(velox_webhook_deliveries_total[5m]))

   # Stripe breaker state (1=half-open, 2=open — non-zero is bad).
   velox_stripe_breaker_state

   # Audit-write errors (any non-zero is a SEV-1 for fail-closed tenants).
   sum(rate(velox_audit_write_errors_total[5m])) by (tenant_id)
   ```
   The full metric inventory lives in
   [`docs/ops/runbook.md` → Metrics inventory](./ops/runbook.md#metrics-inventory).
2. **Customer-support escalation playbook.** Any invoice-related
   customer ticket during T+1 to T+14 is escalated to the
   migration-on-call engineer **immediately**, bypassing the normal
   support tier ladder. The diagnostic path:
   - Pull the customer's Velox invoice ID from the ticket.
   - Cross-check against Stripe (using
     `invoices.stripe_invoice_id` for imported invoices, or by
     checking what Stripe Billing would have invoiced this customer
     for in the same period for net-new invoices).
   - If divergent, manually reconcile (issue a credit note in
     Velox, or a refund through Stripe, depending on the direction
     of the error).
3. **Daily reconciliation continues.** The Phase B step 5 cron keeps
   running through T+14. The expected output is now mostly
   `skip-equivalent` rows for customers / subscriptions / invoices
   that already migrated, plus `insert` rows for any net-new state
   created post-cutover that's still being mirrored to Stripe (some
   integrations keep both sides in sync indefinitely).
4. **If incident**: Phase F.

### Phase F — Rollback playbook

Specifically: if Velox's billing run produces a wrong invoice in the
**first cycle post-cutover**, what do you do?

The honest framing: rollback is expensive and operationally messy.
The whole point of the parallel-run window is to make rollback
unnecessary. But it's the responsible thing to have rehearsed before
T-0, so:

1. **Pause Velox's billing scheduler.** Velox does not ship a
   built-in "pause scheduler" toggle in v1 — the scheduler is wired
   directly in `cmd/velox/main.go:160` and starts unconditionally
   alongside the API server. The operational pause is to **stop the
   binary**:
   ```bash
   # Helm
   kubectl scale deployment/velox --replicas=0 -n velox

   # Compose
   docker compose stop velox-api

   # Bare process
   systemctl stop velox
   ```
   This is heavy-handed (the API stops responding too), but it
   guarantees no further invoice cycles run while you triage. If
   your deployment requires continued API availability during
   rollback (for read traffic), the lighter alternative is to deploy
   a build with `cmd/velox/main.go:160` commented out — keep that
   "scheduler-disabled" image tag in your registry as a known-good
   rollback artifact pre-cutover. *Future Velox work tracked: a
   `VELOX_SCHEDULER_DISABLED=true` env var that gates the
   `scheduler.Start(ctx)` line — not built today.*
2. **Void the offending invoices in Velox.**
   ```bash
   # Identify the wrong invoices (last hour, post-cutover).
   psql "$DATABASE_URL" -c "
     SELECT id, customer_id, total_cents, currency, payment_status, created_at
     FROM invoices
     WHERE livemode = true
       AND stripe_invoice_id IS NULL
       AND created_at >= '<cutover_time>'
     ORDER BY created_at DESC;
   "

   # Void each one via the API (writes an audit_log row with action=void).
   for INV_ID in inv_…; do
     curl -X POST "$VELOX_BASE_URL/v1/invoices/$INV_ID/void" \
       -H "Authorization: Bearer $VELOX_SECRET_KEY" \
       -H "Content-Type: application/json" \
       -d '{"reason": "rollback from migration cutover"}'
   done
   ```
3. **Re-enable Stripe Billing's automatic invoice generation.**
   The reverse of Phase D step 1 — flip the same Stripe-side
   mechanism back on. Stripe Billing resumes authoritative invoice
   generation on the next cycle.
4. **Reconcile customer-side.**
   - If a Velox-issued invoice **overcharged** (the customer's card
     was actually charged before you noticed): issue a refund
     through Stripe Dashboard or the Stripe API. The refund event
     fires a `charge.refunded` webhook back to Velox, which records
     it on `payments` → `refunds` and writes a credit-note audit
     trail.
   - If a Velox-issued invoice **undercharged** (a customer was
     billed less than they should have been): top up via a separate
     Stripe Billing one-off invoice for the difference, with a
     human-readable description. Do **not** retry the Velox invoice
     during rollback — that risks a second wrong charge.
   - If an invoice was issued but not yet charged (PaymentIntent
     was created but the customer hadn't completed 3DS or similar):
     void the PaymentIntent in Stripe alongside the Velox void in
     step 2.
5. **Post-mortem before retrying cutover.** Minimum 30-day cooling
   period. Use the post-mortem template in
   [`docs/ops/runbook.md` → Post-mortem template](./ops/runbook.md#post-mortem-template).
   Specifically:
   - What was the root cause? (Almost certainly: a category of
     Stripe state the parallel-run reconciliation didn't catch.)
   - Why didn't reconciliation catch it? (The reconciliation
     toolkit in [Section 5](#5-reconciliation-toolkit) needs new
     queries.)
   - What changes need to land before the retry? (Often: a new
     CSV note in the importer, or a new daily-reconciliation
     check.)
   - When is the retry? (No earlier than 30 days after the
     incident; longer if the root cause is in Velox itself rather
     than tenant-specific state.)

The rollback playbook is the single most operator-critical part of
this guide. Rehearse it on a staging environment before T-0. If you
can't rollback in a rehearsal, you can't rollback in production.

---

## 5. Reconciliation toolkit

SQL + curl recipes for ongoing reconciliation. Run during the
parallel-run window (T-14 to T-0) and again during stabilisation
(T+1 to T+14). Each recipe answers one specific question with
side-by-side counts.

### Are all Stripe customers in Velox?

```bash
# Stripe side — count total customers (use Stripe Dashboard or API).
# Authoritative figure: count of Customer rows that aren't deleted.
curl -sS "https://api.stripe.com/v1/customers?limit=1" \
  -u "rk_live_…:" | python3 -c "import sys,json; d=json.load(sys.stdin); print('approximate via has_more pagination — see total via Sigma or batched count')"
# Stripe doesn't expose a total_count on /customers; for a precise
# count run a Sigma query: SELECT COUNT(*) FROM customers WHERE deleted = FALSE;
# or paginate the list endpoint and count locally.
```

```sql
-- Velox side — count of imported live-mode customers.
-- The external_id != '' predicate filters to importer-created rows;
-- Velox-native customers may have an empty external_id.
SELECT count(*) AS velox_imported_customers
FROM customers
WHERE livemode = true
  AND external_id <> ''
  AND external_id LIKE 'cus_%';
```

The two numbers should match within the size of any rows that
errored during import (track via the CSV report's `errored` count).

### Do active subscriptions match?

```bash
# Stripe side — count of status=active subscriptions in livemode.
curl -sS "https://api.stripe.com/v1/subscriptions?status=active&limit=1" \
  -u "rk_live_…:" | python3 -c "import sys,json; d=json.load(sys.stdin); print('total via Sigma: SELECT COUNT(*) FROM subscriptions WHERE status = active AND livemode = true;')"
```

```sql
-- Velox side — active subscriptions imported from Stripe.
SELECT count(*) AS velox_active_subs
FROM subscriptions
WHERE status = 'active'
  AND code LIKE 'sub_%'
  AND livemode = true;
```

The `code LIKE 'sub_%'` predicate filters to importer-created subs
(Phase 2 sets `subscription.code = stripe_subscription_id`). Net-new
subscriptions created in Velox post-cutover use a different code
format and are excluded from this count.

### Are the last 30 days of paid invoices accounted for?

```bash
# Stripe side — paid invoices in the last 30 days.
SINCE_EPOCH=$(date -u -v-30d +%s)   # macOS/BSD
# SINCE_EPOCH=$(date -u -d '30 days ago' +%s)  # GNU coreutils
curl -sS "https://api.stripe.com/v1/invoices?status=paid&created[gte]=${SINCE_EPOCH}&limit=1" \
  -u "rk_live_…:" | python3 -c "import sys,json; d=json.load(sys.stdin); print('total via Sigma or paginated count')"
```

```sql
-- Velox side — imported paid invoices in the last 30 days.
SELECT count(*) AS velox_paid_invoices_30d
FROM invoices
WHERE stripe_invoice_id IS NOT NULL
  AND payment_status = 'succeeded'
  AND created_at >= now() - INTERVAL '30 days'
  AND livemode = true;
```

The `stripe_invoice_id IS NOT NULL` predicate filters to imported
rows. The same query without that predicate (and with
`payment_status = 'succeeded'`) gives total Velox-paid revenue
including post-cutover net-new invoices.

### Total revenue match?

```sql
-- Velox side — sum of paid invoice totals imported from Stripe in the
-- last 30 days, broken out by currency. Stripe's totals are denominated
-- in the smallest unit (cents/satoshi/etc); compare like for like.
SELECT
  currency,
  count(*) AS invoice_count,
  sum((totals->>'total')::bigint) AS total_minor_units
FROM invoices
WHERE stripe_invoice_id IS NOT NULL
  AND payment_status = 'succeeded'
  AND created_at >= now() - INTERVAL '30 days'
  AND livemode = true
GROUP BY currency
ORDER BY total_minor_units DESC;
```

The Stripe-side equivalent is a Sigma query (or a paginated
list-and-sum):

```sql
-- Stripe Sigma equivalent (run in Stripe Dashboard → Sigma).
SELECT currency, COUNT(*) AS invoice_count, SUM(amount_paid) AS total_minor_units
FROM invoices
WHERE status = 'paid'
  AND livemode = true
  AND created >= datetime_sub(current_timestamp(), interval 30 day)
GROUP BY currency
ORDER BY total_minor_units DESC;
```

Expect penny-level match between the two within rounding tolerance.
A divergence of more than one cent per invoice signals one of:

- **Tax computation drift.** The importer carries Stripe's
  `tax_amount_cents` verbatim; if the post-cutover Velox path
  computes tax differently, totals diverge. See
  [`docs/ops/tax-calculation.md`](./ops/tax-calculation.md).
- **Currency-conversion drift.** Velox stores per-row currency; if
  Stripe and Velox disagree on a transaction's currency (rare but
  possible for Connect or multi-currency tenants), totals
  diverge.
- **Discount drift.** Phase 2 / 3 emit CSV notes for discounts but
  don't carry them. If a customer had an active coupon in Stripe
  that hasn't been recreated in Velox, the post-cutover Velox
  invoice is over-charged by the discount amount.

### What changed between yesterday and today?

```bash
# Daily delta — every Stripe customer / subscription / invoice
# created or updated in the last 24h, run after the daily importer
# cron. The CSV captures every action (insert / skip-equivalent /
# skip-divergent / error); a non-zero skip-divergent count is the
# headline reconciliation signal during parallel run.
YESTERDAY="$(date -u -v-1d '+%Y-%m-%d')"
DATABASE_URL="$DATABASE_URL" velox-import \
  --tenant="$VELOX_TENANT_ID" \
  --api-key="rk_live_…" \
  --resource=customers,subscriptions,invoices \
  --since="$YESTERDAY" \
  --output="./recon-$(date -u +%Y%m%d).csv"

# Triage: any skip-divergent rows are pre-cutover concerns.
grep ',skip-divergent,' "./recon-$(date -u +%Y%m%d).csv" \
  | awk -F',' '{print $1, $5}'
```

A clean parallel-run day looks like: `insert` rows for net-new
state, `skip-equivalent` for stable state, zero `skip-divergent`,
zero `error`. Anything else is a triage item.

---

## 6. Webhook redirection strategy

Three flows to think about: per-tenant Stripe credentials, inbound
Stripe webhooks, and outbound Velox webhooks.

### Per-tenant Stripe credentials

Velox stores a tenant's Stripe credentials in
`stripe_provider_credentials` ([migration `0032_tenant_stripe_credentials`](../internal/platform/migrate/sql/0032_tenant_stripe_credentials.up.sql)):

```sql
CREATE TABLE stripe_provider_credentials (
    id                        TEXT PRIMARY KEY,
    tenant_id                 TEXT NOT NULL,
    livemode                  BOOLEAN NOT NULL,
    stripe_account_id         TEXT,
    stripe_account_name       TEXT,
    secret_key_encrypted      TEXT NOT NULL,        -- AES-256-GCM under VELOX_ENCRYPTION_KEY
    secret_key_last4          TEXT NOT NULL,
    publishable_key           TEXT NOT NULL,
    webhook_secret_encrypted  TEXT,                 -- nullable; set after webhook registration
    webhook_secret_last4      TEXT,
    verified_at               TIMESTAMPTZ,
    last_verified_error       TEXT,
    UNIQUE (tenant_id, livemode)
);
```

One row per `(tenant_id, livemode)`. Configure via the dashboard
(`/settings/stripe` if present in your build) or directly via the
API:

```bash
curl -X POST "$VELOX_BASE_URL/v1/tenant/stripe-credentials" \
  -H "Authorization: Bearer $VELOX_PLATFORM_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "livemode": true,
    "secret_key": "sk_live_…",
    "publishable_key": "pk_live_…",
    "webhook_secret": "whsec_…"
  }'
```

The secret_key and webhook_secret land encrypted on disk; only the
`*_last4` and `publishable_key` are stored in plaintext for the UI
to identify which key is connected. See
[`docs/ops/encryption-at-rest.md`](./ops/encryption-at-rest.md#whats-encrypted)
for the encryption surface.

### Inbound Stripe webhooks

Velox already accepts payment-related webhooks at
`POST /v1/webhooks/stripe` (registered in `internal/api/router.go`).
The handler verifies the signature against the per-tenant
`webhook_secret_encrypted` and records the event in
`stripe_webhook_events` for replay protection ([migration
`0058_webhook_event_replay`](../internal/platform/migrate/sql/0058_webhook_event_replay.up.sql)).

The cutover does **not** change which Stripe events arrive at this
endpoint. The PaymentIntent flow (the part Velox owns —
`payment_intent.succeeded`, `.failed`, `.processing`,
`charge.refunded`) continues to fire from Stripe to
`/v1/webhooks/stripe` exactly as before. The only thing that changes
at T-0 is **who acts on subscription / invoice events**: Stripe
Billing was authoritative; Velox becomes authoritative.

If your Stripe Billing setup subscribed downstream consumers to
`customer.subscription.*` and `invoice.*` events, those subscriptions
need to be migrated to Velox's outbound webhook system below. The
inbound Stripe webhook configuration is unchanged.

### Outbound Velox webhooks

Velox's outbound webhook system (`internal/webhook/`) signs every
delivery with HMAC-SHA256 under a per-endpoint signing secret stored
encrypted in `webhook_endpoints.secret_encrypted`. Migration
`0049_webhook_secondary_secret` introduced a `secondary_secret_encrypted`
column for rotation grace: when a secret rotates, the old value is
stashed in `secondary_secret_encrypted` for `secondary_secret_expires_at`
(default 72h) so consumers verifying with either secret accept the
delivery.

The cutover sequence for each downstream consumer:

1. **T-7 (during parallel run).** Register a new outbound webhook
   endpoint pointing at the consumer's existing receiver URL. At
   first, the consumer's code path treats Velox-signed events as
   "shadow" — log only, don't act. This validates the wire-shape
   and signature mechanics without affecting production behaviour.
   ```bash
   curl -X POST "$VELOX_BASE_URL/v1/webhook-endpoints" \
     -H "Authorization: Bearer $VELOX_SECRET_KEY" \
     -H "Content-Type: application/json" \
     -d '{
       "url": "https://consumer.example.com/webhooks/velox",
       "events": [
         "customer.created", "customer.updated",
         "subscription.created", "subscription.updated",
         "subscription.canceled",
         "invoice.finalized", "invoice.paid", "invoice.voided"
       ],
       "active": true
     }'
   ```
2. **T-7 to T-1.** Consumer-side, verify Velox's signing secret +
   payload shape match expectations. Velox's signature format and
   verification recipe are documented in the per-endpoint settings
   page; it's standard HMAC-SHA256 with a `Velox-Signature` header
   identical in shape to Stripe's `Stripe-Signature`.
3. **T-0 (cutover moment).** Disable the consumer's Stripe webhook
   subscription for `customer.subscription.*` and `invoice.*`
   events (Stripe Dashboard → Webhooks → toggle the endpoint to
   `disabled`). At the same moment, flip the consumer-side flag
   that made Velox's deliveries "shadow" so they become
   authoritative.
4. **T+0 to T+72h.** During this window Velox's secondary secret is
   still valid, so a consumer using the old (Stripe) signing secret
   pattern won't reject deliveries. If the consumer's signature-
   verification code rejects Velox's signatures, the delivery
   retries with backoff (`internal/webhook/service.go::StartRetryWorker`)
   — investigate before the retry budget exhausts.
5. **T+72h.** Secondary secret expires. Consumer must be on the
   primary signing secret by now.

The "register at T-7, flip at T-0" pattern keeps the rollback
window short: if cutover fails at T-0, the consumer-side flag flips
back to "shadow Velox, act on Stripe" in seconds, no Velox-side
config change needed.

---

## 7. Known limitations on import

The importer is conservative: anything Velox doesn't faithfully model
surfaces as a CSV `error` row or a CSV note rather than a silent
partial import. The full list:

| Stripe feature | Importer behaviour | What to do about it |
|---|---|---|
| **Subscription Schedules** (`Subscription.Schedule`, `subscription_schedules`) | Phase 2 emits a CSV note; no schedule object created in Velox | Recreate manually via Velox's subscription change-plan endpoint with `effective_at` for future-dated transitions. Out of scope for v1. |
| **Quotes** | Out of scope; not iterated by the importer | Legacy quotes stay in Stripe for audit. Velox has no Quotes object today. |
| **Promotion Codes** (vs basic Coupon) | Out of scope | Velox has Coupons, not Promotion Codes. Map your underlying coupon definitions to Velox's coupon model; ignore the public-code surface. |
| **Connect / multi-party** | Out of scope | Velox is single-tenant per deployment. Connect platforms are not a v1 target. |
| **Tiered / graduated / volume pricing** (`billing_scheme=tiered`) | Phase 1 errors the price row | Recreate as Velox `meter_pricing_rules` with `dimension_match` + per-rule rates, or as separate flat-amount plans. See [`docs/design-multi-dim-meters.md`](./design-multi-dim-meters.md). |
| **Multiple subscription items** (`Subscription.Items.Data[]` length > 1) | Phase 2 errors the subscription row | Split into multiple single-item Velox subs, or use Velox's subscription items + add-on model. |
| **Metered usage type** (`usage_type=metered`) | Phase 1 errors the price row | Use Velox's multi-dimensional meters + `pricing_rule.aggregation_mode`. |
| **Open / draft invoices** (`status=open`/`draft`) | Phase 3 errors the invoice row | Let Stripe finalize them, then re-run the importer once they're in `paid` / `void` / `uncollectible`. |
| **Refunds, disputes, chargebacks** (history) | Out of scope; not iterated | Stripe remains the audit-of-record for past payment activity. Velox tracks refunds going forward via `payments` → `refunds`. |
| **Subscription `pause_collection`** | Phase 2 emits a CSV note; only `paused` status maps directly | Recreate behaviour via `POST /v1/subscriptions/{id}/pause-collection` after import. |
| **Multiple tax IDs per customer** | Phase 0 imports the first; emits a CSV note | Manually add the rest via the API after import. |
| **Stripe Tax** (`automatic_tax`, `tax_rates`) | Phase 3 carries `tax_amount_cents` verbatim; per-rate breakdown is not modelled | Velox respects each tenant's Stripe Tax registration; tax handling lives in the tenant's Stripe account post-cutover. See [`docs/ops/tax-calculation.md`](./ops/tax-calculation.md). |
| **`Customer.balance` / customer credit ledger** | Out of scope; Stripe's `customer.balance` does not auto-import | Velox has its own credit ledger (`internal/credit/`). Manual reconciliation: read each customer's `customer.balance` from Stripe, issue a `POST /v1/credits/grant` in Velox for non-zero balances. |
| **Stripe Sources / Cards on file** | Out of scope (Phase 0 design doc § "Deferred for Phase 0") | Migrating saved cards across Stripe accounts requires Connect or a dedicated migration agreement with Stripe Support. The standard cutover path is to issue a "update your payment method" email to every customer via Velox's customer portal magic links. |
| **Past Stripe webhook deliveries** | Out of scope; not replayed | Past webhooks live in Stripe Events and stay in Stripe. Velox's outbound webhook history starts at T-0. |
| **Stripe `Invoice.hosted_invoice_url`** | Phase 3 emits a CSV note; Velox generates its own hosted URL via `public_token` at finalize | Customer-facing hosted-invoice links generated by Stripe pre-cutover stay valid in Stripe; post-cutover Velox issues new ones. |

---

## 8. FAQ

### Do I need to migrate all-at-once?

No. The parallel-run window is exactly the surface that lets you
migrate gradually. Three patterns:

- **Per-customer cohort.** Import every customer; cut over Velox
  authority for a 10% cohort first; widen weekly. Requires
  application-level routing (your billing-flow code knows whether
  customer X is "on Velox" or "on Stripe Billing" for the current
  cycle).
- **Per-product line.** If you bill multiple product lines (a SaaS
  subscription + a usage line item), cut over the simpler line
  first while Stripe Billing handles the complex one. Requires
  per-product Stripe-vs-Velox routing.
- **Hard cutover at T-0.** The default pattern in this guide. All
  customers move in one moment. Higher risk, simpler operationally.

### What about historical invoices older than 12 months?

Phase 3 imports all finalized invoices regardless of age. There is
no built-in age filter beyond `--since` (which limits the *Stripe
list endpoint* to invoices created on/after a date — useful for
cron-style daily reconciliation, not for "skip everything older
than 12 months"). If you only want the recent window, run the import
twice: once with `--since="$(date -u -v-12m '+%Y-%m-%d')"` to
catch the recent year, and again without `--since` later if you
decide you want the longer history.

### What if my Stripe account uses Stripe Tax?

Velox respects each tenant's Stripe Tax registration. The importer
carries `tax_exempt` and the first `tax_id` over via Phase 0 (Stripe
`Customer.TaxExempt` → Velox `BillingProfile.TaxStatus`,
Stripe `Customer.TaxIDs.Data[0]` → Velox `BillingProfile.TaxID`).
Continued tax handling lives in your Stripe account post-cutover —
Velox calls Stripe Tax's calculate API at finalize time when the
tenant's tax provider is configured. See
[`docs/ops/tax-calculation.md`](./ops/tax-calculation.md) for the
configuration matrix.

The tax-neutrality posture is documented in the project memory: Velox
doesn't pick tax models. The tenant's Stripe account holds the
registration (OIDAR / domestic GST / VAT / US sales tax / etc.), and
Velox supports all of them via the per-tenant Stripe credential
flow.

### What about per-customer balance / customer credit ledger?

Out of scope for the importer. Velox has its own event-sourced
credit ledger (`internal/credit/`); Stripe's `customer.balance` does
not auto-import. Manual reconciliation:

```bash
# 1. List Stripe customers with non-zero balance.
curl -sS "https://api.stripe.com/v1/customers?limit=100" \
  -u "rk_live_…:" | jq -r '.data[] | select(.balance != 0) | "\(.id)\t\(.balance)"'

# 2. For each, mint an equivalent grant in Velox.
curl -X POST "$VELOX_BASE_URL/v1/credits/grant" \
  -H "Authorization: Bearer $VELOX_SECRET_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "external_customer_id": "cus_…",
    "amount_cents": 1234,
    "currency": "usd",
    "reason": "stripe customer.balance migration",
    "expires_at": null
  }'
```

The reverse case — a customer with a *negative* Stripe balance
(meaning they owe the operator money) — is rarer; handle as a
one-off invoice in Velox post-cutover.

### How do I test the cutover without affecting customers?

The Section 3 rehearsal pattern, plus a separate Velox tenant in
your **test** mode pointing at your **test** Stripe account, lets
you exercise the full T-14 → T+14 timeline without a single live
charge. The recommended posture: run the rehearsal cutover one full
calendar week before T-14 of the production cutover, with
synthetic time pressure (a "T-0" you've actually picked) so the
team's runbook reflects real wall-clock pacing.

### What if a downstream consumer can't be migrated to Velox webhooks at T-0?

Use the parallel-webhook posture from
[Section 6](#6-webhook-redirection-strategy) indefinitely. The
consumer keeps its existing Stripe webhook subscription, and Velox's
outbound webhook fires too — the consumer's code path picks one to
act on (the Stripe one) and logs the other (Velox shadow). Migrate
when the consumer is ready. The cost is a duplicate event source
during the hand-off; the benefit is no hard dependency on the
consumer's migration timeline.

### Can I use the importer to keep Velox in sync with Stripe forever?

The daily `--since` cron pattern is fine for the parallel-run window
and the immediate stabilisation period (T-14 to T+14). Beyond that,
the right answer is to make Velox authoritative and stop running the
importer — running both as primary indefinitely is the recipe for
divergence. If your architecture genuinely needs both as primary
(rare), the design conversation is "two sources of truth means N²
reconciliation paths" — out of scope for this guide.

---

## Related docs

- [`docs/design-stripe-importer.md`](./design-stripe-importer.md) —
  importer design rationale, mapping tables per resource, idempotency
  + failure handling. Read this for the "why" behind each phase's
  behaviour.
- [`docs/ops/runbook.md`](./ops/runbook.md) — operational runbook.
  Phase E's metric checks reference the metrics inventory, and the
  Phase F rollback playbook references the rollback-procedures
  section. Cross-link from the Migration section added at the bottom.
- [`docs/ops/encryption-at-rest.md`](./ops/encryption-at-rest.md) —
  PII encryption posture. Pre-migration checklist's "VELOX_ENCRYPTION_KEY
  + VELOX_EMAIL_BIDX_KEY" requirement maps to this doc.
- [`docs/ops/audit-log-retention.md`](./ops/audit-log-retention.md)
  — audit log evidence retention. The cutover audit-log entry
  pattern in Phase D step 6 is captured under the `update` action
  on `tenant_settings` for SOC 2 evidence.
- [`docs/compliance/soc2-mapping.md`](./compliance/soc2-mapping.md)
  — SOC 2 control mapping. CC9.2 (vendor risk management) names
  Stripe explicitly; the migration from Stripe Billing is a
  dependency-reduction move that affects the carve-out scope of any
  future SOC 2 audit.
- [`docs/ops/tax-calculation.md`](./ops/tax-calculation.md) —
  per-tenant tax configuration and Stripe Tax compatibility, cited
  from FAQ.
- [`docs/self-host.md`](./self-host.md) — self-host install paths.
  Migrating from Stripe Billing assumes a Velox install is already
  up; the install paths there are the prerequisites.
