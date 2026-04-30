# Velox Manual Test Runbook

A practical runbook for exercising Velox end-to-end. Flows are grouped into three
tiers so you can pick the right subset for the situation you're in.

## How to use this runbook

| Tier | When | Time | What it covers |
|------|------|------|----------------|
| **Tier 1 — Smoke** | Every pre-merge + nightly | ~15 min | Infra + auth + create→bill→charge happy path |
| **Tier 2 — Full Suite** | Before a release cut | ~2–3 hrs | Every shipping domain, one flow per feature |
| **Tier 3 — Deep / Rare** | Major releases, infra changes, incident post-mortems | ~4 hrs | RLS, security headers, encryption at rest, rate limit, migrations, OTel, config validation |

Each flow has a stable FLOW ID (A1, B2, …). Reference them in bug reports and PRs.
The ID doesn't change when flows are reordered. New flows get the next free number
in their section.

Flow steps use `- [ ]` checkboxes. Copy the section into a scratch doc when running
a tier — this file stays as the canonical source, not a progress tracker.

---

## Setup

### Prerequisites

- Go 1.25+
- Docker & Docker Compose
- Node.js 22+ and npm
- [Stripe CLI](https://stripe.com/docs/stripe-cli) (`brew install stripe/stripe-cli/stripe`)
- A Stripe test account with keys from https://dashboard.stripe.com/test/apikeys
  (credentials are configured **per-tenant in the dashboard**, not via env vars)

### First-time config

```bash
cp .env.example .env
# Edit .env — the only required fields for local dev are:
#   VELOX_BOOTSTRAP_TOKEN=...         (generate with: openssl rand -hex 32)
#   VELOX_ENCRYPTION_KEY=...          (64 hex chars; openssl rand -hex 32)
```

`STRIPE_SECRET_KEY` / `STRIPE_WEBHOOK_SECRET` are **not** env vars any more —
each tenant enters their own in Settings → Payments after sign-in. The operator
never holds any tenant's Stripe secrets.

### Start everything (4 terminals)

```bash
# Terminal 1 — Infrastructure (Postgres + Redis)
make up

# Terminal 2 — Backend API (runs migrations on boot via RUN_MIGRATIONS_ON_BOOT=true)
make dev
# Log should show "migrations: applied N, current version 64"
# and "using app database connection (RLS enforced)"

# Terminal 3 — Frontend (web-v2)
cd web-v2 && npm install        # first time only
cd web-v2 && npm run dev        # http://localhost:5173

# Terminal 4 — Stripe webhooks (skip until you've connected a Stripe account in the UI)
# The endpoint_id is the vlx_spc_... returned from Settings → Payments → Connect.
stripe listen --forward-to localhost:8080/v1/webhooks/stripe/<endpoint_id>
# Copy the whsec_... secret back into Settings → Payments as the signing secret.
```

### Bootstrap (first run only)

```bash
make bootstrap                  # Creates the first tenant + secret/publishable API keys
```

Open http://localhost:5173 and paste the Secret API key printed by bootstrap into
the Sign In screen. Same key authenticates the API and the dashboard — there are
no user accounts in v1 (see ADR-007 / FLOW A1).

### Useful commands

| Command | What it does |
|---------|--------------|
| `make dev` | Start backend (auto-migrates via `RUN_MIGRATIONS_ON_BOOT=true`) |
| `make web-dev` | Start the frontend (also `cd web-v2 && npm run dev`) |
| `make test-unit` | Run all 35 test packages (short mode, `-p 1`) |
| `make test-integration` | Run full suite including integration tests |
| `make up` / `make down` | Start / stop Postgres + Redis |
| `make migrate` / `make migrate-status` | Apply migrations / show version |
| `docker compose down -v && make up` | Destroy + recreate DB (nuclear reset) |
| `make stats` | Go + TS lines, packages, test packages |

### Test cards

| Card | Behavior |
|------|----------|
| `4242 4242 4242 4242` | Always succeeds |
| `4000 0000 0000 0341` | Attaches OK, declines on charge (dunning trigger) |
| `4000 0000 0000 9995` | Always declines |

---

# Tier 1 — Smoke (~15 min)

One continuous flow: brings the stack up, signs in, exercises the core money path,
signs out. Run this before every merge to main and as a nightly canary.

## FLOW S1: End-to-end smoke

### S1.1 Stack comes up clean
- [ ] `make up` — Postgres + Redis containers start without errors
- [ ] `make dev` — backend starts, logs show `migrations: applied N, current version 64`
  and `using app database connection (RLS enforced)`
- [ ] `curl http://localhost:8080/health` → `{"status":"ok"}`
- [ ] `curl http://localhost:8080/health/ready` → 200 with `database: ok`, `scheduler: ok`
- [ ] Frontend at http://localhost:5173 loads (white page is fine — not signed in yet)

### S1.2 Bootstrap + sign in
- [ ] `make bootstrap` if no tenants exist — bootstrap prints a paired Secret Test key (`vlx_secret_test_…`) and Secret Live key (`vlx_secret_live_…`) plus a Publishable test key. Copy the Secret Test key for the smoke flow.
- [ ] Sign in via UI at `/login` by pasting the Secret Test key
- [ ] Verify: redirected to dashboard
- [ ] Verify: DevTools → Cookies → `velox_session` set with `HttpOnly: ✓`
- [ ] Verify: `localStorage` does NOT contain the API key (credential is in the httpOnly cookie, not in JS-readable storage)
- [ ] Verify: subsequent requests have no `Authorization` header but include the cookie (Network tab → request headers)

### S1.3 Tenant Stripe connection
- [ ] Settings → Payments → paste `sk_test_...` + `pk_test_...` → Connect
- [ ] Verify: `vlx_spc_...` endpoint id is displayed; copy it
- [ ] Terminal 4: `stripe listen --forward-to localhost:8080/v1/webhooks/stripe/<vlx_spc_...>`
- [ ] Paste the `whsec_...` from the CLI back into Settings → Payments
- [ ] Verify: "Connected" status and Stripe account identifier shown

### S1.4 Create the core graph
- [ ] Pricing → create rating rule: key `api_calls`, flat, $0.01/call
- [ ] Pricing → create meter: key `api_calls`, aggregation `sum`, link to rule
- [ ] Pricing → create plan: code `starter`, $29/mo, attach `api_calls` meter
- [ ] Customers → create: "Smoke Corp", `external_id=smoke_corp`, email any@any.test
- [ ] Customer detail → Billing Profile → set address + `USD` + `10%` tax rate
- [ ] Customer detail → Set Up Payment → test card `4242 4242 4242 4242`
- [ ] Customer detail → New Subscription → Starter plan, calendar billing, start today

### S1.5 Bill + charge
- [ ] Usage → ingest 1,000 events for `api_calls` on `smoke_corp`
- [ ] Trigger billing via API (UI button was removed):
  `curl -X POST -H "Authorization: Bearer $VELOX_KEY" http://localhost:8080/v1/billing/run`
  where `$VELOX_KEY` is a secret key from API Keys page (or the bootstrap key)
- [ ] Verify: exactly 1 invoice generated; auto-finalized, `payment_status = succeeded`
- [ ] Invoice detail → line items: base fee (prorated), usage ($10.00), tax (10%)
- [ ] Verify: Stripe CLI terminal shows `payment_intent.succeeded`
- [ ] Dashboard: MRR > $0, revenue chart updated, Recent Activity shows the invoice

### S1.6 Sign out
- [ ] Sidebar → Sign Out
- [ ] Verify: redirect to /login; back-button still lands you on /login
- [ ] Verify: `curl -b <stale_cookie> http://localhost:8080/v1/session/` → 401

**If all of S1 passes, the core engine is healthy.**

---

# Tier 2 — Full Suite

One flow per shipping feature, organized by domain. You do not need to run these
in order — pick the domain the change touched.

---

## Auth

The API key is the durable credential. The dashboard exchanges a pasted
key for an httpOnly `velox_session` cookie via `POST /v1/auth/exchange`;
SDK / curl callers send `Authorization: Bearer <key>` directly. There
are no user accounts, no password reset, no member invitations in v1.
See ADR-007 (revert) and ADR-008 (cookie refinement).

## FLOW A1: Dashboard sign-in (paste key → httpOnly cookie)

- [ ] `make bootstrap` prints THREE keys: a `vlx_secret_test_…`, a `vlx_secret_live_…`, and a `vlx_pub_test_…` publishable. Copy the secret in the mode you want to operate in (test for eval; live to charge real cards).
- [ ] `make dev` starts the API on `:8080`. `cd web-v2 && npm run dev` starts the dashboard on `:5173`.
- [ ] Visit `http://localhost:5173`. Login screen shows a single "Secret API key" field.
- [ ] Paste a non-vlx string → inline error `That doesn't look like a Velox key — it should start with vlx_`. No request fired.
- [ ] Paste a syntactically-valid but unknown key (e.g. `vlx_secret_test_aaaaaaa…`) → `POST /v1/auth/exchange` returns 401; UI shows `Invalid or revoked API key`.
- [ ] Paste the bootstrap secret → redirect to `/`. Dashboard loads with Customers / Invoices / Subscriptions etc. populated.
- [ ] DevTools → Application → Cookies → `velox_session` is set, `HttpOnly: ✓`, `SameSite: Lax`, `Path: /`. Value is opaque (the raw cookie value, not the API key).
- [ ] `localStorage` is empty for `velox_*` keys (the credential is *not* in localStorage anymore).
- [ ] Reload the page → still signed in (AuthContext re-runs `whoami` on mount; cookie attaches automatically).
- [ ] Verify the session row exists: `SELECT id_hash, key_id, tenant_id, expires_at FROM dashboard_sessions ORDER BY created_at DESC LIMIT 1;` — row is present, `expires_at` is ~7 days out.
- [ ] User dropdown → Sign out → `POST /v1/auth/logout` returns 204; cookie is cleared (devtools reflects); redirect to `/login`.
- [ ] Verify revocation: `SELECT revoked_at FROM dashboard_sessions WHERE id_hash = '<id_hash>';` — `revoked_at` is now NOT NULL.
- [ ] Try to use the same cookie value (e.g. paste it back into the browser via devtools) → next protected request → 401 `invalid or expired session`.

## FLOW A2: /v1/whoami contract — cookie OR Bearer

- [ ] `curl -i -c /tmp/c.txt -H 'Content-Type: application/json' -d '{"api_key":"<vlx_secret_test_...>"}' http://localhost:8080/v1/auth/exchange` → 200, response body has `{tenant_id, key_id, key_type, livemode, expires_at}`, `Set-Cookie: velox_session=...; HttpOnly` on the response.
- [ ] `curl -b /tmp/c.txt http://localhost:8080/v1/whoami` → `200 {"tenant_id":"vlx_ten_...","key_id":"vlx_key_...","key_type":"secret","livemode":false}`
- [ ] `curl -H 'Authorization: Bearer <vlx_secret_test_...>' http://localhost:8080/v1/whoami` → 200 with the same shape (Bearer fallback works for SDK callers).
- [ ] Without either credential → `401 missing credentials — sign in at /login or send Authorization: Bearer vlx_secret_...`.
- [ ] Cookie + Bearer on the same request, with **disagreeing** keys → cookie wins (verify by sending a Bearer for a different tenant + a valid cookie; whoami returns the cookie's tenant_id).
- [ ] Revoke the underlying API key (`UPDATE api_keys SET revoked_at = NOW() WHERE id = '<key_id>'`) → Bearer path 401s `api key revoked` immediately. Existing cookie still works until `dashboard_sessions.revoked_at` is also flipped — the dashboard should call `RevokeAllForKey` on key revocation (operator UI: out of scope for v1).
- [ ] Publishable key (`vlx_pub_test_…`) on `POST /v1/auth/exchange` → cookie minted; `whoami` returns `key_type:"publishable"`. Most write endpoints will 403, which is correct.

---

## API Keys

## FLOW K1: Permissions

- [ ] Secret key: full access to create/read/update on every resource
- [ ] Publishable key: read-only — POST to /v1/plans → 403
- [ ] Revoked key: any request → 401 `"api key revoked"`
- [ ] API Keys page → Create → verify raw key shown once with copy button and the
  "You won't be able to see this again" warning

## FLOW K2: Expiration

- [ ] Create key → expiration presets: No expiration, 30 days, 90 days, 1 year, Custom
- [ ] Select Custom → calendar picker uses the same branded component as rest of app
- [ ] Create a key with `expires_at = now + 1 min` — verify 200 until expiry
- [ ] `UPDATE api_keys SET expires_at = NOW() - INTERVAL '1 hour' WHERE ...` → 401 `"api key expired"`
- [ ] Keys expiring within 7 days show yellow "Expires in Xd" badge
- [ ] Expired keys grouped into a collapsed "Expired keys" section

## FLOW K3: API Keys page UX

`/api-keys` — full operator surface for create / inspect / revoke.

- [ ] Header: "API Keys" title with `· N active` count next to subtitle when any active keys exist
- [ ] Empty tenant: `EmptyState` with key icon, copy "No API keys yet", and "Create API Key" button (no list rendered)
- [ ] Each active card shows: name, masked prefix (`sk_live_xxxx--------`), key-type badge (secret = violet shield, publishable = blue eye), `Created Xago`, `Last used Xago` or "Never used", `Expires DATE` row when set
- [ ] The card matching the *current session's* localStorage key is decorated with a ring-2 ring-primary/20 outline AND a "Current session" info badge
- [ ] "Expired keys" section is collapsed by default behind chevron toggle showing count; expanding reveals one-line cards with `expired` destructive badge and a Revoke button (revoking an already-expired key is allowed and useful for cleanup)
- [ ] "Revoked keys" section is collapsed by default; expanded rows show strikethrough name, `revoked` badge, and "Revoked Xago"
- [ ] Create dialog: Name input (max 100 chars, required), Key Type 2-col selector (Secret default vs Publishable), Expiration preset row (No expiration / 30 days / 90 days / 1 year / Custom)
- [ ] Selecting `Custom` reveals a `DatePicker` constrained to `minDate = tomorrow`; submit without picking a date inline-errors "Please select an expiration date"
- [ ] Selecting any non-custom preset shows a "Key will expire on FullDate" hint below the row
- [ ] Submit success → Create dialog closes, `API Key Created` dialog opens with amber callout, full raw key in selectable monospace, Copy button (toast "Copied to clipboard"), and a single "I've saved this key" dismiss action
- [ ] Closing the created-key dialog removes the raw key from memory — refreshing the page only ever shows the masked prefix again
- [ ] Per-row Revoke → AlertDialog "Revoke API Key" with name + prefix in copy → confirm → toast "API key revoked" + list refetches; Cancel dismisses without changes
- [ ] Revoking the *current session* key triggers different copy ("Revoke Current Session Key?" / "will log you out immediately") and after confirm the next API call 401s, kicking the user back to /login
- [ ] Server validation errors (e.g. duplicate name) surface inline via `applyApiError` against `name` / `key_type` / `expires_at` fields, not as a generic toast

---

## Billing Engine

## FLOW B1: Billing model sanity (arrears + proration)

Billing is arrears — the engine bills **after** the period closes. Calendar
billing aligns to the 1st of the month; first partial period is prorated.

- [ ] New subscription starts mid-month — verify first `billing_period_end` = 1st of next month
- [ ] Run billing before the period closes → 0 invoices generated
- [ ] Advance `current_period_end` to yesterday, run billing → 1 invoice:
  - Base fee prorated (e.g., `13/30 × $29 = $12.57`)
  - Usage (full-period aggregation)
  - Tax per the tenant's basis-point rate
  - Due date = issued + net terms (from tenant settings)
  - Invoice number uses tenant prefix

## FLOW B2: Basis-point tax precision

- [ ] Settings → tax rate `7.25%` → `tax_rate_bp = 725` in DB (no float column exists)
- [ ] Run billing with a $100 subtotal → tax = $7.25 exactly
- [ ] Invoice detail page displays `Tax (7.25%)`
- [ ] 3 line items $33.33 + $33.33 + $33.34: per-line tax sums exactly to invoice total tax

## FLOW B3: Invoice idempotency

- [ ] Run billing, note the generated invoice
- [ ] Run billing again immediately (same period) → no duplicate invoice
- [ ] Server logs: `"invoice already exists for billing period (idempotent skip)"`
- [ ] ```sql
      SELECT COUNT(*) FROM invoices WHERE subscription_id = '<id>'
        AND billing_period_start = '<start>' AND billing_period_end = '<end>'
        AND status != 'voided';
      ```
  → exactly 1

## FLOW B4: Auto-charge retry

- [ ] Customer with decline-on-charge card (`4000 0000 0000 0341`)
- [ ] Ingest usage, run billing → invoice has `auto_charge_pending = true`, `payment_status = pending`
- [ ] Update the card to a working one via Stripe Checkout
- [ ] Wait for next scheduler tick (or manually trigger) → `auto_charge_pending` clears,
  `payment_status = succeeded`

## FLOW B5: Idempotency-Key header

- [ ] POST /v1/customers with `Idempotency-Key: test-123` → 201
- [ ] Repeat same body + same key → same response, only one row created
- [ ] Same key, different body → 409 (conflict — key already used with different payload)
- [ ] New key → new customer created

## FLOW B6: Subscription lifecycle

- [ ] Create subscription with `trial_days = 7` → status active, trial end date shown;
  billing during trial produces no invoice
- [ ] Pause active subscription → confirmation dialog; billing skipped; usage not metered
- [ ] Resume → active; billing runs at next period close
- [ ] Cancel → confirmation dialog; status `canceled`; no future billing

## FLOW B7: Plan change + proration

- [ ] Active sub on Starter; create Enterprise plan ($99/mo)
- [ ] Change to Enterprise "Apply immediately" → proration invoice generated, toast confirms $XX.XX
- [ ] Change back to Starter immediately → downgrade credits customer balance; toast confirms
- [ ] Change plan without "immediately" → no proration; plan changes at next period boundary

## FLOW B8: Usage caps

- [ ] Sub with `usage_cap_units = 5000`, `overage_action = block`, ingest 8,000
- [ ] Run billing → usage capped at 5,000 (proportional across meters)
- [ ] Change `overage_action = charge`, ingest another 8,000, run billing → full 8,000 billed

## FLOW B9: Customer price overrides

- [ ] POST /v1/price-overrides `{ customer_id, rating_rule_id, flat_amount_cents }`
- [ ] Ingest usage for that customer, run billing → invoice uses override price
- [ ] Same usage for another customer → invoice uses default rule price

## FLOW B10: Manual tax with cross-border zero-rating

Tests Velox's tenant-side fallback when Stripe Tax is off. See Tier 3 X7 for the
Stripe-Tax-enabled path.

- [ ] Settings → `tax_home_country = "IN"`, `tax_rate_bp = 1800`, `tax_name = "IGST"`
- [ ] Invalid country codes ("INDIA", "in ", "XX") rejected with ISO-3166 validation error
- [ ] Empty `tax_home_country` is accepted (legacy tenants)
- [ ] Domestic customer (`country = "IN"`): $100 subtotal → tax = $18, `tax_name = "IGST"`, PDF shows `IGST (18.00%)`
- [ ] Export customer (`country = "US"`): $100 subtotal → tax = $0, `tax_name = "IGST (zero-rated export)"`, PDF shows the annotation
- [ ] Customer with no country: normal 18% applies (can't prove export)
- [ ] Export customer with `tax_exempt = true`: tax = $0, `tax_name = ""` (exempt overrides export annotation)
- [ ] Clear `tax_home_country`: US customer now charged 18% (no home country → can't zero-rate)
- [ ] India B2B reverse-charge invoice PDF shows supplier GSTIN in header (e.g. `GSTIN: 27AAEPM1234C1Z5`) under the company contact line
- [ ] India reverse-charge legend reads "Tax payable on reverse charge basis: YES" (not the EU "VAT to be accounted for" wording)
- [ ] EU reverse-charge invoice PDF retains EU wording ("Reverse charge — VAT to be accounted for by the recipient.")
- [ ] **Stripe Tax `taxability_reason` round-trip (issue #4)**: trigger a no-registration jurisdiction (e.g. Texas with no Stripe Tax registration). Calc returns `tax_amount=0` with `taxability_reason=not_collecting`. After invoice finalize, line item shows the *Not collecting in this jurisdiction* badge in the dashboard, and the database `invoice_line_items.tax_reason='not_collecting'`.
- [ ] **Customer-exempt path**: customer with `tax_status='exempt'` + exemption certificate. After finalize, line item `tax_reason='customer_exempt'` and PDF footer carries the customer-exemption legend ("One or more lines are exempt from tax based on the customer's exemption certificate.") in addition to the reverse-charge legend if applicable.

## FLOW B12: Subscription activity timeline (T0-18)

Chronological feed of lifecycle events sourced from the audit log. CS reps land here for "why was my sub cancelled / plan changed" tickets.

- [ ] Create a subscription → activate → pause → resume → change plan → cancel — at least 5 mutations
- [ ] `GET /v1/subscriptions/{id}/timeline` returns `{events: [...]}` — entries in ascending timestamp order
- [ ] Each event carries `timestamp`, `source: "audit"`, `event_type`, `status`, `description`, `actor_type`, `actor_name`, `actor_id`
- [ ] Descriptions are human-readable ("Subscription activated", "Subscription paused", "Plan changed", "Subscription canceled")
- [ ] Operator-initiated cancel: `description` = "Subscription canceled" (no "by" suffix)
- [ ] Portal-initiated cancel (via customer portal /v1/me route): metadata carries `canceled_by = "customer"` → description becomes "Subscription canceled **by customer**"
- [ ] Status tags color-code: `succeeded` (emerald), `warning` (amber), `canceled` (destructive/red), `escalated` (violet), `info` (blue default)
- [ ] SPA: Subscription detail page shows an **Activity** card under the period visualization
- [ ] Card mirrors the invoice payment-activity timeline layout (colored dot, description left, timestamp right-aligned)
- [ ] When an actor is resolved (API key name, operator email), a second line shows "by {actor_name}" underneath
- [ ] Nonexistent subscription ID: **404** (not an empty events array — silent-empty masquerade is worse than a clear miss)

## FLOW B13: Multi-dimensional meters

Shipped Week 2. The wedge primitive — events carry `dimensions JSONB` and pricing
rules match by dimension subsets. Migration 0054 adds the column + GIN index;
migration 0062 makes the GIN index concurrent.

### Decimal quantities + dimensioned ingest

- [ ] `POST /v1/usage-events` with body `{"meter":"tokens","customer":"<id>","dimensions":{"model":"gpt-4o","operation":"input","cached":true,"tier":1},"value":10000.5,"timestamp":"<iso>","idempotency_key":"k1"}` → 201; `value` stored as NUMERIC (Stripe parity for `quantity_decimal`)
- [ ] Decimal value is preserved end-to-end: `SELECT value FROM usage_events ORDER BY created_at DESC LIMIT 1;` → `10000.5` (not truncated)
- [ ] Replay the same body with the same `idempotency_key` → 200 with the original event id; no duplicate row

### Dimension subset matching at finalize

- [ ] Create rating rule keyed `tokens_input` with `dimension_match = {"operation":"input"}`
- [ ] Ingest events with `{operation:"input"}` and `{operation:"output"}` for the same meter
- [ ] Run billing → only `operation=input` events priced under `tokens_input`; `operation=output` falls through to the default rule (or remains unpriced if no fallback)
- [ ] Add a second rule `tokens_input_cached` with `dimension_match = {"operation":"input","cached":true}` → it wins over `tokens_input` for cached input (more-specific match wins)

### Aggregation modes per pricing rule

All five Stripe Tier-1 modes expressed as a per-rule choice:

- [ ] `aggregation = sum` (default) — bills the cumulative quantity over the period
- [ ] `aggregation = count` — bills the number of events regardless of value
- [ ] `aggregation = max` — bills the largest single value over the period (e.g., peak concurrent VMs)
- [ ] `aggregation = last_during_period` — bills the value of the last event in the period (e.g., end-of-period gauge)
- [ ] `aggregation = last_ever` — bills the value of the last event ever (across all periods, latest snapshot)
- [ ] Switching a rule's aggregation between cycles re-bills the next cycle correctly without affecting past invoices

### Throughput

- [ ] `cmd/velox-bench` ingests 50k events/sec sustained on a single tenant against a local Postgres (matches the Week 2 plan deliverable)

## FLOW B14: Billing thresholds (per-item + per-subscription)

Shipped Week 5 (migration 0056). Stripe Tier-1 parity for
`subscription.billing_thresholds`. Crossing a threshold mid-cycle finalizes the
invoice early with `billing_reason="threshold"`.

### Per-item `usage_gte`

- [ ] PATCH a subscription with `{"billing_thresholds":[{"meter_id":"<m>","usage_gte":10000}]}` → 200
- [ ] Ingest 9,999 units → no early finalize
- [ ] Ingest 1 more unit (cumulative 10,000) → invoice auto-finalized within 1 scheduler tick; `billing_reason="threshold"` on the invoice; `billing.alert.triggered` webhook fires (if an alert is configured — see B15)
- [ ] Verify: subsequent events for the same period start a NEW billing window (the early-finalize closes the period)

### Per-subscription `amount_gte`

- [ ] PATCH a subscription with `{"billing_thresholds":{"amount_gte":50000}}` (i.e., $500) → 200
- [ ] Ingest enough usage to bring the running total to $499.99 → no finalize
- [ ] Ingest one more event that crosses $500 → early finalize, same shape as per-item

### Threshold + manual run interaction

- [ ] Cross a threshold, then immediately run `POST /v1/billing/run` → idempotent skip (the scheduler already finalized the period)

### Dashboard UI (Track B, PR #70)

Subscription detail → "Spend Thresholds" card sits between Items and Properties. Backed by `PUT/DELETE /v1/subscriptions/{id}/billing-thresholds`.

- [ ] Subscription detail with no thresholds set → empty-state copy explains the cycle scan is the only invoice-emitting path; "Set thresholds" button on the right
- [ ] Click Set / Edit → dialog opens with subtotal cap input (major units, e.g. `1000.00` for $1,000), `reset_billing_cycle` checkbox with both modes documented inline, and one row per subscription item with `usage_gte` decimal-string input
- [ ] Save with subtotal `1000.00` only → backend stores `amount_gte=100000` (cents); card shows `$1,000.00` with currency suffix and reset hint
- [ ] Save with one per-item `usage_gte=10000.5` → card shows `≥ 10000.5 units` (trailing zeros stripped from the NUMERIC(38,12) wire string per ADR-005)
- [ ] Edit and clear all fields + save → no-op (validation blocks empty submit)
- [ ] Click "Clear thresholds" (destructive button) → DELETE fires; card flips back to empty state
- [ ] On a `canceled` or `archived` subscription → Set/Edit button is hidden (backend rejects there anyway)

Shipped Week 5 (migration 0057). `POST /v1/billing/alerts` with `recurrence`
controls whether the alert fires once-ever or once-per-billing-period.

- [ ] `POST /v1/billing/alerts` `{"customer_id":"<c>","meter_id":"<m>","threshold":1000,"recurrence":"one_time"}` → 201
- [ ] Cross the threshold once → `billing.alert.triggered` webhook + dashboard notification appear
- [ ] Cross again in the same period → no second fire (one-time)
- [ ] Repeat with `recurrence:"per_period"` → fires once per billing period (resets at period boundary)
- [ ] Webhook payload includes: `customer_id`, `meter_id`, `threshold`, `current_value`, `period_start`, `period_end`, `recurrence`
- [ ] Dashboard notification: bell icon shows unread count; clicking the alert navigates to the customer detail page with the meter highlighted

### Backend-only today

The `/billing-alerts` dashboard page was cut on the lean-cut. The
backend handlers + alert engine remain — operators can manage alerts
via curl until a UI returns. To create / list / archive alerts use
the API directly (`POST /v1/billing/alerts`,
`GET /v1/billing/alerts?status=`, `POST /v1/billing/alerts/{id}/archive`).
Webhook firing + the engine logic are unchanged.

## FLOW B11: Tax-ID format validation

`UpsertBillingProfile` normalizes (trim + uppercase) and format-validates `tax_id`
against `tax_id_type`. Storage uses Stripe-canonical codes (`in_gst`, `eu_vat`,
`au_abn`); legacy Velox shorthand (`gstin`, `vat`, `abn`) is still accepted on
input and normalized to the canonical code before write. Unknown kinds pass through.

- [ ] `in_gst` + `27AAEPM1234C1Z5` → saved as `27AAEPM1234C1Z5`, `tax_id_type` stored as `in_gst`
- [ ] Legacy `gstin` input → normalized to `in_gst` on write (backend `NormalizeTaxIDType`)
- [ ] `eu_vat` + `DE123456789` → accepted
- [ ] `au_abn` + `51824753556` → accepted
- [ ] Unknown Stripe code (`za_vat` + any value) → accepted as-is
- [ ] Customer detail → Edit billing profile: `tax_id_type` Combobox shows alphabetized list of Stripe codes with country-code badges, searchable by code or label
- [ ] Malformed: `in_gst` + `27INVALID` → 422 `"invalid GSTIN format: expected 15-char code like 27AAEPM1234C1Z5"`
- [ ] Malformed: `eu_vat` + `12` → 422 `"invalid EU VAT format"`
- [ ] Malformed: `au_abn` + `123` → 422 `"invalid ABN format: expected 11 digits"`
- [ ] Unknown kind: `br_cnpj` + `12.345.678/0001-90` → accepted as-is
- [ ] Empty `tax_id` → always accepted regardless of kind

## FLOW B17: Meter Detail page UX

`/meters/:id` — operator surface for inspecting a meter, its default rating
rule, dimension-matched pricing rules (multi-dim), and which plans use it.

- [ ] Breadcrumb: `Pricing / <meter name>` — clicking "Pricing" returns to `/pricing`
- [ ] Header: meter name, ID pill with CopyButton, default-aggregation badge (e.g. `sum`)
- [ ] Properties card lists: Key (monospace), Unit, Default aggregation badge, Created datetime, ID with CopyButton
- [ ] Default pricing rule card renders the linked rating rule's *latest version for the same `rule_key`* — not the snapshot version stored on the meter (verify by editing the rule and reloading; version badge bumps)
- [ ] Mode = `flat`: 3xl price + "per unit" caption + currency uppercase tag
- [ ] Mode = `graduated`: tiers table with `First N units` / `Next M units` / `Beyond X units` labels (last tier: `up_to=0` or `-1`); right-aligned price column
- [ ] Mode = `package`: `<size>` units per package at `<price>` rendered inline
- [ ] Default rule absent (rare): card shows "No pricing rule linked"
- [ ] Dimension-matched rules table columns: Priority (mono/tabular), Dimension match (chips like `model=gpt-4` or italic "all events" when empty), Aggregation badge, Rating rule (name with `<id>` tooltip, falls back to truncated id when rule deleted), Created, trash icon
- [ ] Empty multi-dim state: "No dimension-matched rules. Every event uses the default pricing rule above." + outline "Add a rule" button
- [ ] "Add rule" dialog: Dimension match rows (key + `=` + value, trash to remove, `Add dimension` button); Aggregation Select with per-mode helper text (sum/count/last_during_period/last_ever/max); Priority numeric input ("Higher priority claims events first"); Rating-rule Select ("No rating rules — create one first" when empty)
- [ ] Dimension values: `true` / `false` coerce to bool, numeric strings coerce to number, everything else stays a string — verify by submitting `model=gpt-4` (string), `count=10` (number), `featured=true` (bool) and inspecting the persisted rule via API
- [ ] Submit with no rating rule selected → button disabled
- [ ] Submit success → toast "Pricing rule created" + table refetches with new row; rules render in priority order (higher first)
- [ ] Per-row trash → `TypedConfirmDialog` requiring typed `delete` confirmation; copy explicitly states "stops applying to new events at finalize time. Invoices already finalized are unaffected"
- [ ] Confirmed delete → toast "Pricing rule deleted" + row removed; the rule's stored events on already-finalized invoices remain on those invoices (verify by checking an in-flight upcoming invoice doesn't change after delete)
- [ ] "Used by N plans" section: lists every plan whose `meter_ids` contains this meter; columns Name (link to `/plans/:id`), Code, Interval, Base price, Status badge; row click navigates to plan detail (excluding nested button/link clicks)
- [ ] Plans-empty state: "No plans are currently using this meter"
- [ ] Multi-dim endpoint failure: page still renders (rules query swallows errors and shows empty state) — useful for tenants on pre-multi-dim builds

---

## Pricing Recipes

Five built-in YAML recipes ship with the binary (`internal/recipe/recipes/`):
`anthropic_style`, `openai_style`, `replicate_style`, `b2b_saas_pro`,
`marketplace_gmv`. Each defines products + prices + meters + dunning policy +
webhook endpoint as one atomic graph. Cuts time-to-first-invoice from "read
docs for an hour" to a single API call.

## FLOW R1: List + preview

- [ ] `GET /v1/recipes` → 5 entries; each has `key`, `name`, `description`, `tags`
- [ ] `POST /v1/recipes/{key}/preview` for each key → 200 with `products[]`,
  `prices[]`, `meters[]`, `dunning_policy`, `webhook_endpoints[]` projected (no DB writes)
- [ ] Unknown key → 404 `"recipe not found"` (no partial state, no log spam)

## FLOW R2: Instantiate end-to-end (`anthropic_style`)

- [ ] `POST /v1/recipes/anthropic_style/instantiate` with `{"livemode": false}` → 201
- [ ] Response includes IDs for every created resource
- [ ] DB now contains: 1 product (Claude family), N prices (one per model × cache tier),
  N meters (input/output/cache_read/cache_write), 1 dunning policy, 1 outbound webhook endpoint
- [ ] Pricing rules carry `dimension_match` JSONB so meters share a single product
- [ ] Audit log: one entry per resource created, all with `actor=recipe:anthropic_style`
- [ ] Repeat for `openai_style`, `replicate_style`, `b2b_saas_pro`, `marketplace_gmv` →
  each produces a clean graph in <500ms

## FLOW R3: Idempotency on (tenant_id, recipe_key)

- [ ] Instantiate `b2b_saas_pro` once → 201
- [ ] Instantiate again same tenant → 409 `"recipe already instantiated"`; no duplicate writes
- [ ] Different tenant, same recipe → 201 (key is per-tenant, not global)
- [ ] Force re-instantiate via `?force=true` is **not** supported — operator must
  `DELETE /v1/recipes/{key}/instance` first; verify that endpoint cleans up
  product+prices+meters+webhook+dunning atomically

## FLOW R4: Atomic rollback on partial failure

- [ ] Inject a failure mid-instantiate (e.g., webhook endpoint URL fails URL validation)
  → 422; verify **zero** rows created (products, prices, meters all rolled back)
- [ ] `SELECT * FROM tenant_recipe_instances WHERE tenant_id = ?` → no row
- [ ] Logs show the single failure reason — not a cascade of unrelated errors

## FLOW R5: Dashboard UI flow

- [ ] `/recipes` → grid of 5 cards with name, description, tag chips, "Preview" button
- [ ] Click Preview → side panel renders projected resources (read-only table per category)
- [ ] "Instantiate" button at panel footer; confirm dialog names the side-effects ("creates 4 products + 12 prices + 3 meters + 1 dunning policy + 1 webhook endpoint")
- [ ] Post-instantiate: redirected to `/products` with the new product IDs visible
- [ ] Recipe card on `/recipes` now shows "Instantiated 2026-04-27" with link to detail page

### Uninstall (Track B, PR #73)

Backed by `DELETE /v1/recipes/instances/{id}`. Uninstall is **no-cascade by design** — only the `recipe_instances` row drops; the plans / meters / rating rules / dunning policy / webhook endpoint stay so live subscriptions don't lose billing data.

- [ ] Recipe card flagged "Installed" → open the configure dialog → Uninstall button visible on the footer left (destructive-coloured), separate from the install/preview/cancel actions on the right
- [ ] Card NOT installed → no Uninstall button; install button enabled
- [ ] Click Uninstall → AlertDialog with copy explaining: (a) recipe link drops only, (b) plans/meters/etc. stay, (c) re-install will name-collide unless you archived/renamed the originals first
- [ ] Confirm Uninstall → toast `"Uninstalled <recipe name>"`; recipe card flips back to "not installed" without manual refresh; install button re-enables
- [ ] After uninstall, verify in `/pricing` and `/meters` that the originally-created plans + meters are still there (no cascade)
- [ ] Re-install the same recipe without renaming originals → 422 with name-collision error (this is by design)
- [ ] Re-install after archiving the originals → succeeds with fresh `recipe_instances` row

---

## Invoices

## FLOW I1: Multiple meters on one plan

- [ ] Add `storage_gb` rule ($0.10/GB) + meter (aggregation `max`), attach to plan
- [ ] Ingest 2000 API calls + 50 GB storage, run billing
- [ ] Invoice has 3 line items: base ($29) + API ($20) + storage ($5)

## FLOW I2: Negative usage (corrections)

- [ ] Ingest 1000 events, then ingest -200 (correction) for same meter
- [ ] Usage Events page shows -200 in red
- [ ] Meter breakdown shows net 800
- [ ] Run billing → billed for 800, not 1000

## FLOW I3: Manual line items

- [ ] POST /v1/invoices → create draft invoice
- [ ] Invoice detail → Add Line Item: "Setup fee", Add-On, qty 1, $250
- [ ] Add "Consulting", qty 2, $150 → total $550
- [ ] Finalize → auto-charges via Stripe

## FLOW I4: Void invoice

- [ ] Void a finalized invoice that has credits applied
- [ ] Verify: credits reversed, balance restored
- [ ] Verify: Stripe PaymentIntent canceled
- [ ] Verify: active dunning run (if any) is resolved
- [ ] Audit log shows `Voided invoice <number>`

## FLOW I5: Collect + payment timeline

- [ ] Finalized unpaid invoice for a customer with a payment method
- [ ] POST /v1/invoices/{id}/collect → Stripe PaymentIntent created, payment_status updates
- [ ] GET /v1/invoices/{id}/payment-timeline → all attempts with ts, amount, status, PI id
- [ ] For a failed-then-succeeded invoice, both attempts are shown in order

## FLOW I5b: Operator context card (issue #10)

Stripe-parity diagnostic panel that explains WHY an invoice is stuck. Renders
above the invoice document for non-terminal pending-like states; muted background
+ info icon, no destructive styling. Sources tax/payment fields from the invoice
itself and dunning state from `GET /v1/dunning/runs?invoice_id=...`.

- [ ] Pending invoice with `tax_status='pending'` → card renders with "Tax calculation deferred — `<reason>` (retry N)" diagnosis; tax-status badge + retries + deferred-at rows
- [ ] Pending invoice with `last_payment_error` populated → card shows "Payment failed: `<reason>`" diagnosis; last-payment-failure row + payment-intent ID row
- [ ] Invoice with `payment_status='unknown'` → diagnosis reads "Stripe returned an ambiguous outcome — reconciliation in progress"
- [ ] Voided invoice (Velox's stand-in for uncollectible) → diagnosis reads "Marked uncollectible — manual write-off path"
- [ ] Invoice with active dunning run → "Dunning" row shows state badge + attempt count; "Next retry" row shows scheduled timestamp
- [ ] Paid / draft invoices → card hidden entirely (no operator context surfaced)

## FLOW I6: Email + PDF preview

- [ ] Invoice detail → Email → send to any address
- [ ] Verify: email queued in `email_outbox`, PDF attached; SMTP logs (or Mailtrap) show delivery
- [ ] Invoice detail → Preview PDF → renders in overlay iframe; close via X or backdrop

### Branded HTML body (T0-16, 2026-04-24)

Every customer-facing email renders as multipart/alternative (text + HTML) with tenant chrome. Check the HTML part in Mailtrap/inbox.

- [ ] Configure tenant with `company_name`, `logo_url` (e.g. `https://via.placeholder.com/200x60`), `brand_color` (`#1f6feb`), `support_url`
- [ ] Trigger invoice-ready email → HTML body includes: tenant logo + company name in header, 2px brand-color accent bar at top, line items summary, "Amount due" amount card, **View & pay invoice** CTA button styled with `brand_color`
- [ ] CTA URL points at `{HOSTED_INVOICE_BASE_URL}/invoice/{public_token}` (or Velox defaults if unset) — copy + open it in an incognito window (covered in FLOW I10)
- [ ] Footer shows "Contact support" link + "Powered by Velox Billing" micro-credit
- [ ] Plain-text part (view source, pick `text/plain`) is still present for deliverability fallback
- [ ] Payment-receipt email after a successful charge: similar chrome, CTA labelled **View receipt**
- [ ] Dunning warning email: shows attempt N of M + next retry date, CTA **Update payment**
- [ ] Payment-update-request email: CTA uses the token URL from `PAYMENT_UPDATE_URL`, not the hosted invoice URL (different flow)
- [ ] Operator emails (portal magic link) intentionally stay **plain text** — no HTML chrome, no tenant branding, since they carry security-sensitive tokens

## FLOW I7: Zero-amount invoice

- [ ] Plan with `base_amount_cents = 0`, no meters → subscription → run billing
- [ ] Verify behavior: either no invoice generated, or $0 invoice auto-marked paid (no Stripe charge)

## FLOW I8: Currency consistency

- [ ] Default currency USD, create some invoices
- [ ] Change tenant `default_currency` to EUR → NEW invoices in EUR, existing unchanged
- [ ] Customer with `billing_profile.currency = GBP` → their invoices in GBP regardless of tenant default

## FLOW I9: Credit note on voided invoice

- [ ] Void an invoice, then try to issue a credit note → error `"cannot issue credit note on voided invoice"`
- [ ] CN is NOT created

## FLOW I11: `POST /v1/invoices/create_preview`

Shipped Week 5. Computes a draft invoice for an in-progress period without
committing to the DB. Powers the cost dashboard's "projected bill" line, the
plan-change confirmation dialog, and the operator plan-migration preview (B16).

- [ ] `POST /v1/invoices/create_preview {"subscription_id":"<sub>"}` → 200 with the same shape as a finalized invoice (line items + totals + tax) but `id` is null and no DB row exists
- [ ] Plan-change confirmation: from a sub detail page, click "Change plan" → preview dialog shows the proration line + new amount due before commit; cancelling the dialog does not write
- [ ] Cost dashboard reflects the preview: `projected_total_cents` field on the public dashboard projection (FLOW CU8) is populated when the engine returns a value

## FLOW I12: One-off invoice composer

Shipped Week 7 (migration 0060 makes `subscription_id` nullable so off-sub
invoices are supported). Operator-side 30-second flow on the customer detail
page — no leaving the page.

- [ ] Customer detail → "New invoice" → composer opens inline (no full-page navigation)
- [ ] Add 1+ line items (description, qty, unit, amount) → totals tick live
- [ ] Save as draft → invoice appears in the customer's invoice list with `status=draft`, `subscription_id=null`
- [ ] Finalize from the draft → goes through the normal Stripe PaymentIntent path
- [ ] Subscription_id is empty in the DB (`SELECT subscription_id FROM invoices WHERE id=...` → NULL) — confirms migration 0060

## FLOW I10: Hosted invoice page (public tokenized URL — T0-17)

Stripe-equivalent `hosted_invoice_url`. End customer clicks the CTA in an invoice/receipt email and lands on a branded, unauthenticated page where they can pay. Token is the sole credential.

### Token minting + dashboard affordances

- [ ] Create a customer + subscription, run billing (or create an invoice manually) → result: a **draft** invoice has no `public_token`
- [ ] Finalize the invoice → `public_token` minted (query `SELECT public_token FROM invoices WHERE id = ...` → starts with `vlx_pinv_` + 64 hex chars)
- [ ] Invoice detail page: **Copy Link** button copies `{HOSTED_INVOICE_BASE_URL}/invoice/{public_token}` (falls back to `window.location.origin` if env unset) — toast confirms
- [ ] **Rotate** button opens `TypedConfirmDialog` requiring the word `ROTATE` — confirm → new token minted, old URL stops resolving
- [ ] Buttons are **hidden** for draft invoices

### Public page render

Open the copied URL in an **incognito window** (no session cookie, no auth):

- [ ] Page loads without a login prompt
- [ ] Header: tenant logo (if `logo_url` set) + `company_name` + optional `support_url` link
- [ ] Optional 3px `brand_color` accent bar at top
- [ ] Invoice meta: invoice number (mono), amount due (large, tabular numerals), due date
- [ ] Bill-to + From columns show structured address
- [ ] Line-items table: description + qty + unit + amount, tabular numerals on numbers
- [ ] Totals card: subtotal, optional discount (−), optional tax with rate "(XX.XX%)", reverse-charge note if applicable, total, amount paid, **Amount due** bold
- [ ] Primary **Pay {amount}** button with tenant `brand_color` background (falls back to theme primary if unset)
- [ ] **Download PDF** secondary button — opens the same PDF the operator gets
- [ ] Footer: "Secured by Stripe" micro-credit + "Powered by Velox Billing"

### Pay flow (Stripe test mode)

- [ ] Click **Pay** → `POST /v1/public/invoices/{token}/checkout` → redirected to `checkout.stripe.com`
- [ ] Use test card `4242 4242 4242 4242` → complete payment → Stripe redirects back to `{baseURL}/invoice/{token}?paid=1`
- [ ] Page shows a provisional **"Processing your payment…"** banner (green with animated spinner) while the webhook catches up
- [ ] `payment_intent.succeeded` webhook arrives → invoice flips to `paid` → page auto-refetches and shows the **Paid on {date}** banner; Pay button disappears
- [ ] PDF download still works on a paid invoice

### State-gated variants

- [ ] Void a finalized invoice → visit its public URL → **Voided on {date}** banner, no Pay button, PDF still downloads (customers revisit for records)
- [ ] Visit the URL of a **draft** invoice (craft via psql or pre-finalize) → **404** (draft never leaks — belt-and-suspenders guard in `resolveInvoice`)
- [ ] Rotate the token → old URL returns **404**; new URL works

### Security checks

- [ ] Inspect the JSON response at `GET /v1/public/invoices/{token}` → **no** `tenant_id`, `subscription_id`, `tax_id`, `stripe_payment_intent_id`, or `stripe_customer_id` fields (safe-projection audit)
- [ ] Hit the public route 61+ times in a minute from the same IP → rate-limit bucket (`hostedInvoiceRL`, 60/min) kicks in with 429 + `Retry-After`
- [ ] Operator `POST /v1/invoices/{id}/rotate-public-token` requires `PermInvoiceWrite`; unauthenticated call returns 401

---

## Dunning

## FLOW D1: Retry cycle + escalation

- [ ] Customer with declining card → subscription → usage → run billing → dunning run created
- [ ] Dunning page: stat cards (Active, Escalated, Recovered, At Risk $), tab filters with counts
- [ ] Run shows state `Active`, "No retries yet", `next_action_at` scheduled
- [ ] Sidebar Dunning badge shows count
- [ ] Fast-forward `next_action_at` in DB, wait for scheduler → attempt count increments
- [ ] After max retries → state `Escalated`

## FLOW D2: Resolution modes

- [ ] Click "Resolve" on an active run → "Payment recovered" → invoice marked paid
- [ ] On another run, "Manually resolved" → run resolved without touching the invoice

## FLOW D3: Per-customer override

- [ ] Customer detail → Dunning Override → Configure → max_retries 5, grace 7 days
- [ ] Verify displayed in properties card; takes effect on next failure
- [ ] Reset to Default → override removed

## FLOW D4: Self-service payment update (token)

- [ ] Trigger a payment failure
- [ ] Server logs: `"payment update email"` with URL `http://localhost:5173/update-payment?token=vlx_pt_...`
- [ ] Open the URL in incognito (NOT logged in)
- [ ] Verify: page loads without login, shows customer name + invoice + amount; "Secured by Stripe"
- [ ] Click "Update Payment Method" → Stripe Checkout (setup mode); enter good card; complete
- [ ] Verify: redirected back; Stripe fires `checkout.session.completed`; customer PM updated
- [ ] Re-open the same URL → "Link expired or invalid" (single-use)
- [ ] Random token → same error
- [ ] No token → "No payment update token provided"
- [ ] Manually expire a token and re-open → same error

---

## Credits & Credit Notes

## FLOW C1: Credits (grant, apply, expire, deduct)

- [ ] Credits → Grant $50 to a customer, description "Welcome credit", expires 30d
- [ ] Balance = $50, ledger shows Expires column
- [ ] Ingest usage → run billing → credits applied, amount_due reduced, Stripe charged only the remainder
- [ ] Ledger: `Applied to invoice <number>` with negative amount
- [ ] Grant $500, generate $79 invoice → credits applied $79, amount_due $0, balance $421, Stripe NOT charged
- [ ] Deduct $20 → confirmation dialog → balance reduced, ledger entry created

## FLOW C2: Credit notes

- [ ] Unpaid invoice → Issue Credit → "Billing error" $20 → preview "will reduce amount due by $20"
- [ ] Issue → amount_due reduced; CN page stat cards update
- [ ] Paid invoice → Issue Credit → $15 type "Credit to balance" → customer credit balance +$15;
  invoice detail shows CN in "Post-payment adjustments"
- [ ] Paid invoice → Issue Credit → $10 type "Refund to payment method" → Stripe refund processed;
  CN badge "Refunded"; credit balance unchanged
- [ ] CN > amount_due on unpaid → error
- [ ] CN > amount_paid on paid → error
- [ ] CN page: stat cards (Total Credited, Refunded, Applied to Balance, Issued); tab filters with counts;
  search by number/customer/reason; draft CNs show Issue + Void, issued CNs don't

## FLOW C3: Coupons + plan restrictions

- [ ] Create `PRO20`, 20% off, restricted to Enterprise
- [ ] Redeem for Starter sub → error `"coupon is not valid for this plan"`
- [ ] Redeem for Enterprise sub → discount applied
- [ ] Copy code button works; toast "Code copied"

### Edit dialog (Track B, PR #72)

Coupon detail page → Edit button between Duplicate and Archive. Backed by `PATCH /v1/coupons/{id}`. Covers the Stripe-parity mutable subset: name, max_redemptions, expires_at, restrictions (min_amount / first_time_only / max_per_customer). Discount type/value/currency, duration, stackability, and plan/customer scope are write-once — the dialog deliberately doesn't expose them; duplicate-and-archive is the path there.

- [ ] On a non-archived coupon detail → Edit button visible between Duplicate and Archive
- [ ] On an archived coupon → Edit button hidden, Restore visible (existing behaviour preserved)
- [ ] Click Edit → dialog opens pre-populated with the coupon's current name / max_redemptions / expires_at / restrictions
- [ ] Change name + save → header h1 updates without manual refresh; audit log records `coupon.updated` with `changed_fields=[name]`
- [ ] Clear `expires_at` and save → backend receives `expires_at: null`; header "Expires" tile flips to "No expiry"
- [ ] Set `expires_at` to a future date and save → "Expires" tile shows the date with `<ExpiryBadge warningDays={7}>` if within the warning window
- [ ] Clear `max_redemptions` and save → backend receives `max_redemptions: null`; header "Redemptions" tile reads "N redeemed" without the `/cap` suffix
- [ ] Set `min_amount` only + uncheck first_time + clear max_per_customer → Restrictions card collapses to a single row (Minimum purchase)
- [ ] Clear all three restriction fields and save → Restrictions card disappears entirely (full-overwrite to `{}` clears the block)
- [ ] Submit a `min_amount` of `-50` → server-side 422 surfaces inline on the Minimum purchase field, not as a global toast

---

## Webhooks

## FLOW W1: Stripe signature verification

- [ ] Valid webhook payload + signature within 300s → 200, event processed
- [ ] Replay the same payload 5+ min later → rejected (timestamp tolerance exceeded)
- [ ] Modify payload but keep original signature → rejected (signature mismatch)

## FLOW W2: Outbound webhook secret rotation (72h grace period — T0-19)

Stripe-parity dual-signing window. Outbound events are signed with BOTH the new and previous secrets for 72 hours so partner verifiers can stage a deploy without a production outage.

- [ ] Webhooks → Endpoints → Rotate Secret on an endpoint → modal shows the new `whsec_...` **and** a green card: *"Previous secret valid until {timestamp}"* with "during this window, both secrets sign outbound webhooks — deploy at your own pace" copy
- [ ] API response on `POST /v1/webhook-endpoints/{id}/rotate-secret`: body includes `secret` + `secondary_valid_until` (ISO 8601, ~72h in the future)
- [ ] Endpoints table: row shows a subtle *"Dual-signing until {timestamp}"* hint under the URL
- [ ] Trigger any outbound event (finalize an invoice, etc.) while the grace window is open → `Velox-Signature` header carries **two** `v1=` entries: `t=<ts>,v1=<newSig>,v1=<oldSig>`
- [ ] Verify with new secret: valid ✓
- [ ] Verify with old secret: **still valid** ✓ (this is the grace-window guarantee)
- [ ] Simulate expiry: manually set `secondary_secret_expires_at` in the past via psql → trigger another event → header now carries **one** `v1=` entry, only the new secret verifies
- [ ] Hard-replace path: `RotateEndpointSecret(..., gracePeriod=0)` skips the secondary entirely (not exposed via UI; library-level test only)

## FLOW W4: Live webhook event stream + replay (Week 6)

Server-sent events at `GET /v1/webhook_events/stream` (auth required, secret
key with `PermAPIKeyRead` scope). The Webhooks → Events page in the dashboard
streams new deliveries in real time, lets you replay a delivery, and shows the
payload diff between retries.

- [ ] Webhooks → Events → page shows recent deliveries with state dot (succeeded/failed/pending)
- [ ] Trigger any outbound webhook (finalize an invoice, etc.) → new row streams in within ~1 second without a manual refresh (SSE-driven)
- [ ] Click a delivery → side panel shows request URL, response status, headers, body
- [ ] Click "Replay" on a failed delivery → fresh attempt fires; new row appears for the replay; original row preserved
- [ ] Multiple retries on the same event → "Diff" tab shows the payload diff between attempts (useful when the receiver changed shape between retries)
- [ ] Disconnect Redis or stop the dispatcher → readiness goes degraded; UI still loads but stops streaming new rows (graceful)

## FLOW W3: Delivery stats

- [ ] Webhooks → Endpoints → Success Rate column
- [ ] Green ≥95%, amber 70–94%, red <70%
- [ ] Replay a failed event → success rate updates

---

## Customers & Portal

## FLOW CU1: Settings + billing profile

- [ ] Settings: change company name → save → "Saved" indicator; navigating away with unsaved changes prompts
- [ ] Change currency → NEW invoices use it; existing invoices unchanged
- [ ] Customer detail → edit billing profile (address, tax ID) → PDF reflects updated bill-to

## FLOW CU2: Operator-facing customer portal API

Operator view (API-key auth, `PermCustomerRead`). This is what the dashboard hits to render
a customer's portal-eye view; it is NOT what end customers use — see CU5 for that.

- [ ] GET /v1/customer-portal/{customer_id}/overview → active subs, recent invoices, credit balance
- [ ] GET /v1/customer-portal/{customer_id}/subscriptions → only that customer's subs
- [ ] GET /v1/customer-portal/{customer_id}/invoices → only that customer's invoices

## FLOW CU3: GDPR export + erasure

- [ ] GET /v1/customers/{id}/export → includes customer, profile, invoices, subs, credit ledger, balance
- [ ] Stripe IDs redacted (last 4 visible); payment method details redacted
- [ ] Try delete on customer with active subs → `"customer has active subscriptions; cancel them before deletion"`
- [ ] Cancel sub, POST /v1/customers/{id}/delete-data → display_name → "Deleted Customer", email cleared,
  profile PII anonymized, status `archived`, invoices preserved, audit log entry created
- [ ] Export endpoint for deleted customer returns anonymized data

## FLOW CU4: Archival cascade

- [ ] Customer detail → Archive → confirmation dialog → amber banner "…data is read-only"
- [ ] All action buttons hidden (Edit, Set Up Billing, Configure, Set Up Payment, Add)
- [ ] "Restore Customer" visible in the banner; customer badge `archived`
- [ ] Run billing → no invoices for the archived customer's subs; existing invoices still readable;
  credit balance still visible
- [ ] Restore → banner disappears, actions reappear, badge `active`
- [ ] Customers list → Archived tab → shows archived rows (or empty + Clear filter)

## FLOW CU5: Customer-facing self-service portal (`/v1/me/*`)

End-customer surface added in T0-8. Bearer-token auth (`vlx_cps_...`) via customer portal
session. UI lives at `web-v2/src/pages/CustomerPortal.tsx` with tabs: Invoices, Subscriptions,
Payment Methods.

Endpoints (all bearer-auth, scoped to the session's customer):
- `GET /v1/me/invoices` — list
- `GET /v1/me/invoices/{id}/pdf` — download (blob fetch; cannot use `<a href>` because endpoint is bearer-protected)
- `GET /v1/me/subscriptions` — list
- `POST /v1/me/subscriptions/{id}/cancel` — cancel
- `GET /v1/me/branding` — tenant branding (logo, company name, support URL, brand color)
- `GET /v1/me/payment-methods` — list + update

### Magic-link flow
- [ ] Operator mints a portal session: `POST /v1/customer-portal-sessions {"customer_id":"..."}` → returns bearer token
- [ ] Public magic-link request/consume at `/v1/public/customer-portal/*` — untested end-to-end in this runbook; verify token expiry and single-use
- [ ] Load `CustomerPortal` page with the token → header shows partner logo + company name + support URL (from `/me/branding`)

### Self-service
- [ ] Invoices tab → list renders newest first; drafts filtered out
- [ ] Click PDF → blob download triggers (not a direct link); filename matches invoice number
- [ ] Subscriptions tab → only the session customer's subs appear
- [ ] Cancel a subscription → `TypedConfirmDialog` requires typing `CANCEL` (case-insensitive)
- [ ] Webhook emitted: `subscription.canceled` with `canceled_by: customer` in payload
- [ ] Payment Methods tab → attach / detach via Stripe SetupIntent
- [ ] Cross-customer probe: swap the bearer token for one scoped to a different customer; hitting the first customer's invoice ID → **404** (not 403 — avoids enumeration)

## FLOW CU8: Embeddable cost-dashboard widget

Per-customer iframe-able URL with token auth. Migration 0064 adds
`customers.cost_dashboard_token` (partial unique, 256-bit `vlx_pcd_` prefix).
Operator mints the token via API; the tenant embeds the
`<VeloxCostDashboard>` React component (or hits the public JSON endpoint
directly) into their own product. The in-dashboard preview page was
deleted on the lean-cut; this flow exercises the API + the embeddable
React wrapper.

### Mint + read

- [ ] `POST /v1/customers/{id}/rotate-cost-dashboard-token` (API-key auth) returns `{token, public_url, customer_id}` — token starts with `vlx_pcd_` + 64 hex chars (72 chars total)
- [ ] `GET /v1/public/cost-dashboard/{token}` (no auth) returns the sanitised projection: `customer_id`, `tenant_id`, `billing_period {from, to, source}`, `subscriptions[]`, `usage[{meter_id, meter_key, meter_name, unit, currency, total_quantity, total_amount_cents, rules[]}]`, `totals[{currency, amount_cents}]`, `thresholds[]` (reserved), `warnings[]`, `projected_total_cents`
- [ ] Confirm absent: `email`, `display_name`, `external_id`, `metadata`, `billing_profile` (sanitisation contract)
- [ ] Customer with no active subscription → empty arrays + `billing_period.source = no_subscription`, NOT a 5xx
- [ ] Hard-refreshing the public URL stays under the 60/min/IP `hostedInvoiceRL` rate limit; 61+ requests/min/IP from the same source → 429

### Rotation invalidates

- [ ] Rotate the token (call the rotate endpoint again) → previous URL returns 404 immediately; new URL works
- [ ] Audit log records the rotation with `previous_token_was_unset` flag; plaintext token is **never** in the audit log

### Typed React wrapper

- [ ] `web-v2/src/components/embeds/VeloxCostDashboard.tsx` renders the iframe via `<VeloxCostDashboard token={t} baseUrl={u} theme="dark" accent="#10b981" />`
- [ ] Tenant app importing the component compiles cleanly with `tsc --noEmit`
- [ ] `?theme=light` / `?theme=dark` query params switch theme on the iframe
- [ ] `?accent=#10b981` repaints accent; invalid hex silently ignored

## FLOW CU7: Email bounce capture + badge (T0-20 — 🟡 pipeline only)

Pipeline is complete, UI is ready, webhook event defined — but synchronous SMTP 5xx detection covers only a minority of real-world bounces because most providers emit bounces as async NDRs, not synchronous `RCPT TO` failures. Test the pipeline end-to-end with the psql shortcut below; real bounce detection for most partner traffic ships with T1-8 (SES/SendGrid/Postmark webhook handlers) plugging into the same `customer.MarkEmailBounced` seam.

### Setup a deliberately-bouncing address

Easiest path: use Mailtrap with a rule that 5xx's specific addresses, or point `SMTP_HOST` at a fake SMTP that rejects `RCPT TO: <bounce@example.invalid>` with `550 5.1.1 User unknown`.

Alternative psql-based manual test (for quick verification without infra):
```sql
-- Simulate a bounce by calling the service method directly through the
-- public customer_svc.MarkEmailBounced path (see TestCustomerService tests).
UPDATE customers SET email_status = 'bounced',
    email_last_bounced_at = NOW(),
    email_bounce_reason = '550 5.1.1 User unknown'
WHERE id = '<customer_id>';
```

### Capture path (preferred: real SMTP)

- [ ] Create a customer with email `bounce@example.invalid`
- [ ] Trigger an invoice email send to that customer
- [ ] Server logs show: `send email failed ... error="550 5.1.1 User unknown"`
- [ ] Within ~5 seconds: `customers.email_status` flips to `bounced`, `email_last_bounced_at` populated, `email_bounce_reason` captured
- [ ] `VELOX_EMAIL_BIDX_KEY` must be set — without the blinder, bounces are logged but NOT persisted (graceful degradation; the dashboard stays "unknown")

### Dashboard badge

- [ ] Customer detail page top metrics: email displays a small red **Bounced** badge next to the address
- [ ] Details card: email row shows `Bounced · {formatDate(email_last_bounced_at)}` badge
- [ ] Hover the badge → `title` attribute surfaces the `email_bounce_reason`
- [ ] Customers with `email_status` ∈ `{unknown, ok, complained}` show **no** badge

### Webhook event

- [ ] Register a webhook endpoint subscribed to `customer.email_bounced`
- [ ] Trigger a bounce → `webhook_outbox` gets a row; dispatcher delivers
- [ ] Delivery payload: `{customer_id, reason}` + the standard envelope
- [ ] `webhook_deliveries` log records a 2xx from the receiver

### Heuristic boundaries

- [ ] 4xx transient error (`421 try again later`) does NOT flip status — email outbox handles the retry
- [ ] Error string containing "5xx-like-digits" in unrelated context (zip code 95014) does NOT flip — the parser anchors on word boundaries
- [ ] Deliberately-deferred surfaces (tracked in T1-8): async NDR parsing, SES/SendGrid provider webhooks, auto-suppression on subsequent sends, complaint-vs-bounce differentiation. All plug into the same `customer.MarkEmailBounced` seam.

## FLOW CU6: Brand color + logo URL (tenant settings)

Shipped in T0-12. URL-only logo (no upload infra); brand accent color applied to invoice PDF.

- [ ] Settings → Business tab → Logo URL field accepts public HTTPS URL (example hosts in help text: Cloudinary, S3 public object, CDN)
- [ ] Paste `https://via.placeholder.com/200x60` → live `LogoPreview` thumbnail renders inline
- [ ] Paste an invalid / non-HTTPS URL → thumbnail shows "Couldn't load image"
- [ ] Brand color field: native `<input type="color">` + hex text input (lowercased on save) + Clear button
- [ ] Invalid hex (`#zzz`, `#12345`, missing `#`, uppercase `#FF00AA`): client rejects on save with `"Must be a 7-character hex like #1f6feb"`; server validates the same pattern `^#[0-9a-f]{6}$`
- [ ] Save → generate an invoice PDF → company name tinted in the brand color, thin 2px accent bar under the header block
- [ ] Clear the brand color → save → new PDF has neutral palette (no accent bar); output is byte-identical to the pre-migration look
- [ ] Branded email (T0-16, 2026-04-24): trigger any customer-facing email with `brand_color` set → HTML body renders the 2px accent bar at top, CTA button background uses the brand color, logo + company name in header. See FLOW I6 for the full checklist.

---

## Platform

## FLOW P1: Feature flags

- [ ] GET /v1/feature-flags → seeded flags returned: `billing.auto_charge`, `billing.tax_basis_points`,
  `webhooks.enabled`, `dunning.enabled`, `credits.auto_apply`, `billing.stripe_tax`
  (each with key / enabled / description / timestamps)
- [ ] `billing.stripe_tax` is **legacy** — tax provider selection is now authoritative at
  `tenant_settings.tax_provider` (`none` / `manual` / `stripe_tax`, migration 0031). The flag
  is still seeded for backward compat but per-tenant settings override it.
- [ ] PUT /v1/feature-flags/webhooks.enabled `{"enabled":false}` → flag disabled globally;
  trigger an event → NOT delivered; re-enable → delivery resumes
- [ ] PUT /v1/feature-flags/dunning.enabled/overrides/{tenant_id} `{"enabled":false}` → disabled for tenant only
- [ ] DELETE .../overrides/{tenant_id} → tenant falls back to global
- [ ] Cache TTL: toggles reflect within 30s

## FLOW P2: Audit log

- [ ] Perform several actions (create customer, grant credits, void invoice, change plan)
- [ ] Audit Log page: all logged
- [ ] Stat cards: Total, Today, Unique Actors, Destructive Actions
- [ ] Destructive actions have red left border
- [ ] Expand a row → metadata (amounts, IDs); "View" link navigates to the resource
- [ ] Filters: resource type, action, date range (server-side)
- [ ] Export CSV → all entries exported

## FLOW P3: Usage summary API

- [ ] Ingest events for multiple meters for a customer
- [ ] GET /v1/usage-summary/{customer_id}?from=YYYY-MM-DD&to=YYYY-MM-DD
- [ ] Aggregated totals per meter; quantities match ingestion

## FLOW P4: Empty billing cycle

- [ ] No subs due (all already billed, or none exist)
- [ ] Trigger billing → "0 invoice(s) generated", clean exit, no errors, dashboard stats unchanged

## FLOW P5: Health checks

- [ ] GET /health → 200 `{"status":"ok"}`
- [ ] GET /health/ready → 200 with checks `{api, database, scheduler: ok}`
- [ ] Stop Postgres → GET /health/ready → 503 `degraded` with `database: error:...`;
  GET /health still 200 (liveness ≠ readiness)
- [ ] Scheduler stalled (kill its goroutine or wait past 2× interval) → readiness shows scheduler degraded

## FLOW P6: Tax fallback metrics

Counter `velox_tax_fallback_total{reason}` increments every time `StripeCalculator`
falls through to `ManualCalculator`. Operators alert on sustained non-zero values.

- [ ] `curl -H "Authorization: Bearer $METRICS_TOKEN" http://localhost:8080/metrics | grep velox_tax_fallback_total`
  → counter registered (HELP + TYPE lines)
- [ ] Reason `no_country`: billing.stripe_tax on + customer with no country → counter `reason="no_country"` +1
- [ ] Reason `no_client_for_mode`: connected tenant in one mode only, bill in the other mode → +1
- [ ] Reason `api_error`: invalid Stripe key + fully-addressed customer → +1; restore key
- [ ] Happy path: valid key + addressed customer → counter unchanged

---

## UI / UX

## FLOW U0: Quickstart wizard / TTFI

`/onboarding` — 5-step setup wizard. Goal: time-to-first-invoice (TTFI) under
5 minutes for a new tenant. Each step persists progress so a refresh resumes.

- [ ] Fresh tenant lands on `/onboarding` automatically post-bootstrap
- [ ] **Step 1 — Template**: 5 recipe cards (anthropic, openai, replicate,
  b2b_saas_pro, marketplace_gmv). Pick one → preview panel renders → "Use this template" instantiates (FLOW R2)
- [ ] **Step 2 — Stripe**: connect tenant Stripe key (S1.3 minus the manual UI dance — wizard inlines it). Failure → step gates; success → green tick + masked key
- [ ] **Step 3 — Tax**: pick `stripe_tax` vs `manual` (default `manual`); tax registration tax-id field with FLOW B11 validation inline
- [ ] **Step 4 — Branding**: brand color picker + logo URL (FLOW CU6 inputs); preview chip updates live
- [ ] **Step 5 — First test invoice**: "Create demo invoice" button → spawns 1 demo customer + 1 active subscription on the recipe's primary plan, ingests one usage event, runs `/v1/billing/run`, opens hosted invoice URL in new tab — total elapsed shown in the success card (target: under 30 seconds end-to-end)
- [ ] On finish: TTFI telemetry recorded; `GET /v1/billing/dashboard` shows `time_to_first_invoice_seconds` for that tenant
- [ ] Refresh mid-wizard → resumes on the last incomplete step (state in `tenant_onboarding_state` row)
- [ ] "Skip wizard" link → marks `onboarding_skipped_at`, lands on `/dashboard` empty state with the same Get Started checklist (U1)
- [ ] Re-running the wizard after skip is allowed via `/onboarding?force=true`; does **not** create duplicate demo customer (idempotent on `tenant_id`)

## FLOW U1: Dashboard

- [ ] 4 KPI cards: MRR (sparkline + trend %), Active Customers, Failed Payments (red if >0), Revenue 30d
- [ ] Revenue bar chart (compact, no axes)
- [ ] Recent Activity: last 5 invoices with status dot, badge, amount, relative time — clicking navigates to detail
- [ ] Get Started checklist: **6 steps** — Connect Stripe, Create first plan, Add first customer, Create subscription, Set up webhook endpoint, Complete company profile. Each auto-tracks against server state (no manual checkoff). Self-hides at 100%.
- [ ] Dismiss button persists per-tenant in localStorage (`velox:getstarted-dismissed:${tenantID}`) — dismissing in Tenant A does not hide it in Tenant B
- [ ] "Trigger Billing" button is **not** on the dashboard — use `POST /v1/billing/run` via API (see S1.5)

## FLOW U3: Usage Events page

- [ ] Stat cards: Total Events, Total Units, Active Meters, Active Customers
- [ ] Meter breakdown with horizontal bars
- [ ] Filter by customer → breakdown updates
- [ ] Filter by date range
- [ ] Export CSV
- [ ] Stat cards stay constant when paging (page=1 → page=2 → page=3) — `total_events` reflects all filtered rows, NOT the 25-event page (issue #7)
- [ ] Filter by customer → all four stat cards AND breakdown bars recompute server-side; `Total Units` reflects every matching event in scope, not the visible page
- [ ] Filter by meter → breakdown collapses to a single row; `Active Meters` shows 1
- [ ] Decimal precision: ingest three events with quantities `0.5`, `0.5`, `0.0001` for one customer/meter → `Total Units` displays `1.0001` (no precision loss, no toLocaleString rounding)

## FLOW U5: Dark mode

- [ ] Toggle in sidebar footer → UI switches (sidebar, cards, tables, modals, forms, charts)
- [ ] Badges and status colors still distinguishable
- [ ] Refresh → persists (localStorage `velox-theme`)
- [ ] Toggle back → clean switch
- [ ] Delete `velox-theme` → follows system preference

## FLOW U6: Responsive

- [ ] Tablet width (768px): tables scroll horizontally with fade indicator
- [ ] Sidebar collapses to hamburger; open/close via Menu/X
- [ ] Stat cards stack to 2-col grid
- [ ] Modals don't overflow

## FLOW U7: Edge cases

| Case | Expected |
|------|----------|
| Zero usage | Base fee only invoice |
| Meter without rating rule | Usage silently skipped |
| Duplicate idempotency key (same body) | Cached response, one row |
| Duplicate idempotency key (different body) | 409 Conflict |
| Invalid `external_customer_id` on ingest | `"customer not found"` error |
| Invalid `event_name` on ingest | `"meter not found"` error |
| Void already voided invoice | Error message |
| Finalize non-draft invoice | Error message |
| Duplicate subscription code | Humanized error |
| Cancel canceled subscription | Error message |
| Revoke current session's API key | Warning dialog about logout |
| Create subscription for archived customer | Allowed (backend permits) |
| Esc from modal with form data | "Unsaved changes" confirmation |

## FLOW U8: Error toasts carry the Request ID

Every error toast (via `showApiError` from `lib/formErrors.ts`) surfaces the server-assigned
`Velox-Request-Id` so you can correlate to server logs. Every successful request also records
the latest Request-Id via `lib/lastRequestId.ts` for the Report-an-issue flow (U11).

- [ ] Force any API error (e.g. create a customer with a duplicate external_id) → toast shows
  `Request ID: req_...` as the bottom line; click to copy
- [ ] Trigger an error even when the response envelope fails to parse — the Request-Id from the
  `Velox-Request-Id` response header should still appear in the toast
- [ ] Run `grep "req_abc..." server.log` → matches the toast's trace handle

## FLOW U9: Typed destructive confirmations (`TypedConfirmDialog`)

High-blast-radius actions require typing a specific word before the confirm button enables.
Match is case-insensitive. Used on:

- [ ] Void invoice (type `VOID`) — `InvoiceDetail.tsx`
- [ ] Void credit note (type `VOID`) — `CreditNotes.tsx`
- [ ] Cancel subscription from operator UI (type `CANCEL`) — `SubscriptionDetail.tsx`
- [ ] Cancel subscription from customer portal (type `CANCEL`) — `CustomerPortal.tsx`
- [ ] Delete webhook endpoint (type `DELETE`) — `Webhooks.tsx`
- [ ] Typing the wrong word leaves the confirm button disabled
- [ ] Cancel button always closes the dialog

## FLOW U10: Public pages (no auth required)

Most public pages were cut on the lean-cut (Status, Privacy, Terms,
DPA, Security, the embedded `/docs` site, in-app Changelog). The
remaining unauthenticated surface is invoice + payment-update flows —
the only public pages a real customer touches.

- [ ] `/invoice/:token` — `HostedInvoice.tsx`. Sign out, hit the URL with a valid `public_token` → invoice renders, Pay button works (full FLOW I10 below covers it).
- [ ] `/update-payment` — `UpdatePayment.tsx`. Token-authenticated card-update flow (FLOW D4 covers it).
- [ ] `/checkout/success` — Stripe Checkout return URL. Lands a session with a success message; no auth needed.
- [ ] `/login` — paste-API-key form (FLOW A1). Loads without auth.

## FLOW U11: In-app support channel (Report-an-issue mailto)

Added in T0-10. Two entry points; both build the mailto body at click-time so the
freshest trace context is included.

- [ ] Signed-in: account menu → "Report an issue" → opens mail client with:
  - `To:` the configured support address
  - Body includes `tenant_id`, current URL, user agent, and the most recent `Velox-Request-Id`
    from `lib/lastRequestId.ts` (set after any API call — success or error)
- [ ] Trigger a failing API request, then click "Report an issue" → the Request ID in the
  mailto body matches the one from the error toast
- [ ] Sign out, open `/login`, click "Trouble signing in? Contact support" → same mailto scaffold
  with URL + user agent (no Request-Id in pre-auth mode)

---

# Tier 3 — Deep / Rare

Run before major releases, after infra changes (RLS, encryption, rate limiter,
migrations), or when investigating an incident. These flows exercise properties
that are easy to miss in day-to-day work.

## FLOW X1: Multi-tenant RLS isolation

- [ ] Bootstrap Tenant A with API key A; create customer "Alpha Corp" with key A
- [ ] Bootstrap Tenant B with API key B; list customers with key B → Alpha Corp NOT visible
- [ ] GET /v1/customers/{alpha_id} with key B → 404
- [ ] Create "Beta Corp" with key B; list with key A → Beta Corp NOT visible
- [ ] Repeat for invoices, subscriptions, credits — cross-tenant reads must 404

## FLOW X2: Bootstrap lockdown

- [ ] Start server without `VELOX_BOOTSTRAP_TOKEN` → POST /v1/bootstrap → 403
  `"bootstrap disabled — set VELOX_BOOTSTRAP_TOKEN env var to enable"`
- [ ] Start with `VELOX_BOOTSTRAP_TOKEN=my-secret`, POST `{"token":"wrong"}` → 403 `"invalid bootstrap token"`
- [ ] Correct token after tenants already exist → 409 `"bootstrap already completed — tenants exist"`

`make bootstrap` (CLI) always works and creates additional tenants — only the
HTTP endpoint is guarded.

## FLOW X3: Rate limiting

Rate limiter runs AFTER auth middleware — per-tenant GCRA buckets in Redis at
100 req/min. Unauthenticated + public endpoints (`/health`, `/metrics`,
`/v1/bootstrap`) are NOT rate limited.

- [ ] Send 100+ concurrent requests (Go test or parallel curl; sequential curl is too slow)
- [ ] First 100 → 200, rest → 429 with `Retry-After`; headers include `X-RateLimit-Limit/Remaining/Reset`
- [ ] Wait ~10s, send 20 more → ~16 allowed (GCRA smooth refill at 1.67/sec)
- [ ] Exhaust Tenant A → Tenant B still succeeds (separate buckets keyed by tenant_id)
- [ ] Stop Redis → requests still succeed; logs `"rate_limiter: redis error, failing open"`
  (in `APP_ENV=production`, fails closed instead)
- [ ] Restart Redis → rate limiting resumes
- [ ] Under rate limit: `/health`, `/health/ready`, `/metrics` still return 200

## FLOW X4: Security headers + metrics auth

- [ ] `curl -I http://localhost:8080/v1/customers`
  - `X-Content-Type-Options: nosniff`
  - `X-Frame-Options: DENY`
  - `Cache-Control: no-store`
  - `Referrer-Policy: strict-origin-when-cross-origin`
- [ ] In staging/prod (`APP_ENV != local`): `Strict-Transport-Security` present
- [ ] Set `METRICS_TOKEN=secret123`, restart → GET /metrics → 401
- [ ] `curl -H "Authorization: Bearer secret123" /metrics` → 200, Prometheus output
- [ ] Unset `METRICS_TOKEN`, restart → /metrics accessible unauth (dev mode only)

## FLOW X5: PII encryption at rest

- [ ] Set `VELOX_ENCRYPTION_KEY` (64 hex chars), restart
- [ ] Create customer with email + display_name
- [ ] `SELECT display_name, email FROM customers ORDER BY created_at DESC LIMIT 1;`
  → values prefixed `enc:`; API responses show decrypted plaintext
- [ ] Create billing profile with legal_name, email, phone, tax_id
- [ ] `SELECT legal_name, email, phone, tax_id FROM customer_billing_profiles ORDER BY created_at DESC LIMIT 1;`
  → all 4 fields prefixed `enc:`; API responses show decrypted values
- [ ] Pre-encryption plaintext rows still read correctly (no `enc:` prefix → returned as-is)

## FLOW X6: Webhook replay attack

- [ ] Capture a real Stripe webhook payload + `Stripe-Signature` from `stripe listen` logs
- [ ] Replay via curl 5+ min later → rejected (timestamp tolerance >300s)
- [ ] Replay with modified payload + same signature → rejected (signature mismatch)

## FLOW X7: Stripe Tax integration

Requires a Stripe account home country that supports `V1TaxCalculations.Create`
(US/GB/EU/AU/…). India-registered accounts are account-level blocked (not
key-level) and return `"Stripe Tax isn't yet supported for your country"` — use
FLOW B10 for those tenants.

- [ ] PUT /v1/feature-flags/billing.stripe_tax `{"enabled": true}`
- [ ] Customer with full address (country, state, postal code) in billing profile
- [ ] Run billing → invoice tax calculated by Stripe; `tax_name` shows jurisdiction
  (e.g. "CA Sales Tax"); per-line-item tax amounts populated
- [ ] Set invalid Stripe key → billing still generates invoice with $0 tax (graceful fallback);
  logs warn "tax calculation failed"; counter `velox_tax_fallback_total{reason="api_error"}` +1
- [ ] Restore key; flip customer `tax_exempt = true` → $0 tax regardless of setting

## FLOW X8: Migration rollback

- [ ] `make migrate-status` → note version N
- [ ] `go run ./cmd/velox migrate rollback` → version N-1
- [ ] `make migrate` → back to N
- [ ] `docker compose down -v && make up && make dev` → fresh DB migrates to latest version;
  `make migrate-status` matches `ls internal/platform/migrate/sql/*.up.sql | wc -l`

## FLOW X9: Config validation

Direct `go run ./cmd/velox` lets you control env vars individually. The
validator emits warnings for non-fatal issues and errors for fatal ones.

- [ ] No `VELOX_ENCRYPTION_KEY` in production: fatal — `"VELOX_ENCRYPTION_KEY is required in production"`
- [ ] `VELOX_ENCRYPTION_KEY` not exactly 64 hex chars: fatal — exact length in message
- [ ] `VELOX_ENCRYPTION_KEY` not valid hex: fatal — `"not valid hex"`
- [ ] `APP_ENV=production` without `REDIS_URL`: warning — `"REDIS_URL is not set — rate limiting will fail open"`
- [ ] `APP_ENV="foo"` (unrecognized): warning listing expected values
- [ ] `PORT="not-a-port"`: warning
- [ ] `DB_MAX_IDLE_CONNS > DB_MAX_OPEN_CONNS`: warning
- [ ] All valid (`make dev` with a good `.env`): zero WARN-level config logs

Stripe is no longer validated at boot (per-tenant credentials in DB).

## FLOW X10: OpenTelemetry tracing

```bash
docker run -d --name jaeger -p 16686:16686 -p 4318:4318 jaegertracing/jaeger:2
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 go run ./cmd/velox
```

- [ ] Hit several endpoints (create customer, ingest usage, run billing)
- [ ] Jaeger UI at http://localhost:16686, service `velox`
- [ ] HTTP spans: method + path (e.g. `POST /v1/customers`)
- [ ] `billing.RunCycle` span with `batch_size` attribute
- [ ] `billing.BillSubscription` spans with `subscription_id`, `tenant_id`
- [ ] Parent-child relationship: HTTP span → billing span

## FLOW X11: Large batch usage ingestion

- [ ] POST /v1/usage-events/batch with 1,000 events
- [ ] Response `{accepted: 1000, rejected: 0}`; Usage Events page total matches
- [ ] Include duplicate idempotency keys → duplicates rejected, rest accepted
- [ ] Run billing → aggregated correctly

## FLOW X12: Operator CLI (`velox-cli`)

Cobra-based binary at `./cmd/velox-cli`. Two subcommands today: `sub list` and
`invoice send`. Reads `VELOX_API_URL` + `VELOX_API_KEY` from env (or `--api-url`
/ `--api-key` flags). Wire shape mirrors server respond.List `{data, total}`
exactly so `--output json` is byte-identical to the HTTP response.

- [ ] `go build -o /tmp/velox-cli ./cmd/velox-cli && /tmp/velox-cli --help` lists `sub`, `invoice`, `--api-url`, `--api-key`
- [ ] `velox-cli sub list` → table with columns ID / CUSTOMER / STATUS / PLAN / PERIOD_END
- [ ] `velox-cli sub list --status=active --limit=10 --output=json` → JSON `{data:[…], total:N}`
- [ ] `velox-cli sub list --customer=cus_xxx` → only that customer's subs
- [ ] `velox-cli sub list --plan=plan_xxx` → only that plan's subs
- [ ] Multi-item subscriptions: PLAN column joins plan IDs with `,`; JSON output preserves the array
- [ ] Missing API key → exit code 1, stderr `"VELOX_API_KEY (or --api-key) is required"`
- [ ] Wrong API key → exit code 1, server's 401 message surfaced verbatim
- [ ] `velox-cli invoice send <inv_id>` → 202 → "Invoice queued for delivery"; `email_outbox` row appears
- [ ] `velox-cli invoice send <unknown>` → exit code 1, server's 404 surfaced
- [ ] `velox-cli invoice send <inv_id> --output=json` → `{queued_at, recipient}` JSON

## FLOW X14: Self-host (Compose)

Compose-on-single-VM is the supported v1 deployment shape. Helm /
Terraform / multi-replica HA paths are explicitly deferred — see
`docs/self-host.md` and the lean-cut entry in CHANGELOG for the
rationale.

- [ ] `docker compose up -d postgres redis mailpit` brings the three sidecars up healthy
- [ ] `make bootstrap` creates a tenant + secret/publishable test keys; banner points at `/login` to paste the secret key
- [ ] `make dev` starts the API on `:8080`; `/health` and `/health/ready` both return `{"status":"ok"}`
- [ ] `cd web-v2 && npm run dev` brings the dashboard up on `:5173`; pasting the secret key loads `/`
- [ ] On first dev startup, `RUN_MIGRATIONS_ON_BOOT=true` (default for `make dev`) applies all forward migrations idempotently
- [ ] Outbound transactional mail catches in mailpit at `http://localhost:8025` (no real SMTP needed for local eval)

---

# Diagnostics

Common failure modes and where to look first.

## Server won't start

- `VELOX_ENCRYPTION_KEY` rejected → see FLOW X9; must be exactly 64 hex chars
- Postgres unreachable → `docker compose ps`; `docker compose logs postgres`; `make up`
- Port 8080 in use → `lsof -i :8080` → kill the stale process
- Migration dirty → `SELECT * FROM schema_migrations;` — `dirty=true` means a prior
  run failed partway. Resolve the underlying SQL error, then
  `go run ./cmd/velox migrate force <version>` to clear the dirty flag before
  re-running `make migrate`

## Dashboard sign-in fails

- Pasted key doesn't start with `vlx_` → frontend rejects before sending; no request fired.
- 401 on `POST /v1/auth/exchange` → key is wrong, revoked, or expired. Re-run `make bootstrap` for a fresh test key.
- CORS: `CORS_ALLOWED_ORIGINS` must include the frontend origin (`http://localhost:5173` for local dev). Browser console shows the cross-origin block. The cookie won't attach without the right CORS preflight either.
- Cookie not set after exchange → check `Set-Cookie` on the exchange response; `Secure` flag in dev should be off (we set it only when `APP_ENV` is `staging`/`production`). If it's on in dev, the browser drops the cookie over plain HTTP.
- `velox_session` cookie present but every request 401s → `dashboard_sessions.expires_at` may have passed, or `revoked_at` is set. Check the row.

## Invoice didn't generate

- Subscription not due → billing period end is in the future. Advance it in DB for testing
- Already billed → see FLOW B3; logs say `"invoice already exists for billing period"`
- Subscription paused / customer archived → no billing
- Trial active → no billing until trial ends (FLOW B6)
- Feature flag `billing.auto_charge` off → invoice generated but not charged

## Stripe Tax returning errors

- Unsupported home country → FLOW X7 disclaimer; fall back to FLOW B10 manual tax
- Missing customer address → counter `velox_tax_fallback_total{reason="no_country"}` +1 (FLOW P6)
- Tenant in mode without connected Stripe → `{reason="no_client_for_mode"}`
- Invalid/expired Stripe key → `{reason="api_error"}`

## Rate limit not triggering

- Redis not connected → `redis-cli ping`; check `REDIS_URL`; server logs
  `"rate_limiter: redis error, failing open"`
- Testing with sequential curl — GCRA refills too fast; use parallel (FLOW X3)
- Endpoint is public (`/health`, `/metrics`, `/v1/bootstrap`) — intentionally not limited

## PII not encrypted in DB

- `VELOX_ENCRYPTION_KEY` wasn't set when the row was created → pre-encryption rows
  stay plaintext (backward compat, FLOW X5). New rows post-key-set are encrypted
- Wrong field — only customer display_name/email and billing profile
  legal_name/email/phone/tax_id are encrypted (see `cipher.EncryptString` call sites)

## Webhook signature fails

- Wrong `whsec_...` pasted into Settings → Payments after `stripe listen` restarted
  (CLI rotates the secret each run)
- Clock skew > 5 min between Stripe and local → webhook rejected (FLOW X6)
- Using `/v1/webhooks/stripe` (no endpoint id) — must be `/v1/webhooks/stripe/<vlx_spc_...>`
