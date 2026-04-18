# Velox Manual E2E Test Plan

## Prerequisites

### What you need installed
- Go 1.25+
- Docker & Docker Compose
- Node.js 22+ and npm
- [Stripe CLI](https://stripe.com/docs/stripe-cli) (`brew install stripe/stripe-cli/stripe`)
- Stripe test API keys from https://dashboard.stripe.com/test/apikeys

### First-time setup
```bash
cp .env.example .env
# Edit .env — fill in:
#   STRIPE_SECRET_KEY=sk_test_...
#   STRIPE_WEBHOOK_SECRET=whsec_...  (from `stripe listen` output below)
```

### Start everything (4 terminals)
```bash
# Terminal 1 — Infrastructure
make up                             # Starts Postgres + Redis

# Terminal 2 — Backend API
make dev                            # Runs migrations (2 total), starts server on :8080

# Terminal 3 — Frontend (web-v2 with shadcn/ui)
cd web-v2 && npm install            # First time only
cd web-v2 && npm run dev            # Starts dev server on :5173

# Terminal 4 — Stripe webhooks
stripe listen --forward-to localhost:8080/v1/webhooks/stripe
# Copy the "whsec_..." signing secret into your .env file
```

### Bootstrap (first run only)
```bash
make bootstrap                      # Creates tenant + prints API key
```
Then open http://localhost:5173, paste the API key, sign in.

### Useful commands
| Command | What it does |
|---------|-------------|
| `make dev` | Start backend (auto-migrates) |
| `cd web-v2 && npm run dev` | Start frontend (shadcn/ui) |
| `make test-unit` | Run all 26 test packages |
| `make up` / `make down` | Start/stop Postgres + Redis |
| `make migrate-status` | Show current migration version |
| `docker compose down -v && make up` | Fresh DB (destroy + recreate) |
| `make stats` | Show project stats |

### Test Cards
| Card | Behavior |
|------|----------|
| `4242 4242 4242 4242` | Always succeeds |
| `4000 0000 0000 0341` | Attaches OK, declines on charge |
| `4000 0000 0000 9995` | Always declines |

---

## Phase 1: Infrastructure & Config

---

## FLOW 1: Config Validation

> **Note:** These tests use direct commands (not `make dev`) because we need to control specific env vars.
### 1.1 Missing Stripe Key
```bash
DATABASE_URL="postgres://velox:velox@localhost:5432/velox?sslmode=disable" \
  go run ./cmd/velox
```
- [ ] Verify: JSON log with `"level":"WARN","msg":"config validation","warning":"STRIPE_SECRET_KEY is not set — payment processing will fail"`
- [ ] Verify: also warns about STRIPE_WEBHOOK_SECRET
- [ ] Verify: server still starts (warnings, not fatal)

### 1.2 Invalid Stripe Key Prefix
```bash
DATABASE_URL="postgres://velox:velox@localhost:5432/velox?sslmode=disable" \
  STRIPE_SECRET_KEY="bad_key_123" \
  go run ./cmd/velox
```
- [ ] Verify: warns "does not start with 'sk_' — may be invalid"

### 1.3 Production Without Redis
```bash
DATABASE_URL="postgres://velox:velox@localhost:5432/velox?sslmode=disable" \
  STRIPE_SECRET_KEY="sk_test_fake" \
  STRIPE_WEBHOOK_SECRET="whsec_fake" \
  APP_ENV="production" \
  go run ./cmd/velox
```
- [ ] Verify: warns "REDIS_URL is not set — rate limiting will fail open"
- [ ] Verify: warns about VELOX_ENCRYPTION_KEY in production

### 1.4 Invalid Encryption Key
```bash
DATABASE_URL="postgres://velox:velox@localhost:5432/velox?sslmode=disable" \
  VELOX_ENCRYPTION_KEY="tooshort" \
  go run ./cmd/velox
```
- [ ] Verify: warns "VELOX_ENCRYPTION_KEY must be exactly 64 hex characters"

### 1.5 All Valid (no warnings)
```bash
make dev
```
- [ ] Verify: no WARN-level config validation logs (all env vars set correctly via .env)
- [ ] Verify: server starts cleanly with INFO logs only

---

## FLOW 2: Health Checks

### 2.1 Liveness
- [ ] `GET /health` > verify: `{"status": "ok"}`

### 2.2 Readiness (Healthy)
- [ ] `GET /health/ready` > verify: `{"status": "ok", "checks": {"api": "ok", "database": "ok", "scheduler": "ok"}}`

### 2.3 Database Down
- [ ] Stop postgres
- [ ] `GET /health/ready` > verify: 503, `{"status": "degraded", "checks": {"database": "error: ..."}}`
- [ ] `GET /health` > verify: still returns 200 (liveness != readiness)
- [ ] Restart postgres

### 2.4 Scheduler Stalled
- [ ] Wait for 2x scheduler interval with scheduler stopped
- [ ] `GET /health/ready` > verify: scheduler check shows "degraded" or "no run within expected interval"

---

## FLOW 3: Migration Management (CLI)

> **Note:** Use `make` commands (they load `.env` automatically). Direct `go run` requires passing `DATABASE_URL` manually.

### 3.1 Status
```bash
make migrate-status
```
- [ ] Verify: shows `version: 2, dirty: false` (2 consolidated migrations)

### 3.2 Rollback (Staging Only — careful!)
```bash
make migrate-status                                     # Note: version 2
DATABASE_URL="postgres://velox:velox@localhost:5432/velox?sslmode=disable" \
  go run ./cmd/velox migrate rollback
make migrate-status                                     # Verify: version 1
make migrate                                            # Re-apply
make migrate-status                                     # Verify: version 2 again
```
- [ ] Verify: rollback reverts one migration
- [ ] Verify: re-running `make migrate` re-applies cleanly

### 3.3 Fresh DB
```bash
docker compose down -v && make up
make dev                                                # Migrations run on boot
make migrate-status                                     # Verify: version 2
```
- [ ] Verify: clean start with all tables created

---

## Phase 2: Auth & Security

---

## FLOW 4: API Key Permissions

- [ ] Secret key: full access to all endpoints
- [ ] Publishable key: read-only (can't create plans, can read customers)
- [ ] Revoked key: 401 on any request
- [ ] Create key > verify raw key shown once with copy button
- [ ] Verify: key type description shown in modal

---

## FLOW 5: Expired API Key Rejection

- [ ] Create an API key with `expires_at` set to 1 minute from now
- [ ] Use the key immediately > verify 200 OK
- [ ] Wait for expiry (or manually update DB: `UPDATE api_keys SET expires_at = NOW() - INTERVAL '1 hour' WHERE ...`)
- [ ] Use the expired key > verify 401 Unauthorized
- [ ] Verify error message says "api key expired"

---

## FLOW 6: Multi-tenant RLS Isolation

- [ ] Bootstrap creates Tenant A with API key A
- [ ] Create a customer "Alpha Corp" using key A
- [ ] Create a second tenant (Tenant B) with API key B
- [ ] Using key B, list customers > verify Alpha Corp is NOT visible
- [ ] Using key B, try `GET /v1/customers/{alpha_corp_id}` > verify 404
- [ ] Using key B, create customer "Beta Corp"
- [ ] Using key A, list customers > verify Beta Corp is NOT visible
- [ ] Repeat for invoices, subscriptions, credits — cross-tenant reads must fail

---

## FLOW 7: Bootstrap Lockdown

- [ ] After initial bootstrap, POST /v1/bootstrap again
- [ ] Verify: 403 Forbidden (bootstrap only works when no tenants exist)
- [ ] Verify: error message "bootstrap not available: tenants already exist" or similar

---

## FLOW 8: Rate Limiting (Redis)

### 8.1 Basic Rate Limit
- [ ] Send 100+ requests in rapid succession (use `for i in {1..110}; do curl -s ...; done`)
- [ ] Verify: first 100 return 200, remaining return 429
- [ ] Verify: response headers present: `X-RateLimit-Limit`, `X-RateLimit-Remaining`, `X-RateLimit-Reset`
- [ ] Verify: `Retry-After` header on 429 responses

### 8.2 Per-Tenant Isolation
- [ ] Exhaust rate limit for Tenant A
- [ ] Verify: Tenant B requests still succeed (separate buckets)

### 8.3 Fail-Open
- [ ] Stop Redis (`docker compose stop redis`)
- [ ] Verify: API requests still succeed (no 429s, rate limiting disabled)
- [ ] Check logs for "rate_limiter: redis error, failing open"
- [ ] Restart Redis > verify: rate limiting resumes

### 8.4 Health/Metrics Bypass
- [ ] Verify: `/health`, `/health/ready`, `/metrics` are never rate-limited

---

## FLOW 9: Security Headers + Metrics Auth

### 9.1 Security Headers
- [ ] `curl -I http://localhost:8080/v1/customers`
- [ ] Verify: `X-Content-Type-Options: nosniff`
- [ ] Verify: `X-Frame-Options: DENY`
- [ ] Verify: `Cache-Control: no-store`
- [ ] Verify: `Referrer-Policy: strict-origin-when-cross-origin`
- [ ] In staging/production (APP_ENV != local): verify `Strict-Transport-Security` header present

### 9.2 Metrics Auth
- [ ] Set `METRICS_TOKEN=secret123` env var, restart
- [ ] `curl http://localhost:8080/metrics` > verify: 401 Unauthorized
- [ ] `curl -H "Authorization: Bearer secret123" http://localhost:8080/metrics` > verify: 200 with Prometheus metrics
- [ ] Unset `METRICS_TOKEN`, restart > verify: `/metrics` accessible without auth (dev mode)

---

## FLOW 10: PII Encryption at Rest

### 10.1 Enable Encryption
- [ ] Set `VELOX_ENCRYPTION_KEY` (64 hex chars) and restart
- [ ] Create a new customer with email and display name

### 10.2 Verify Encryption
```sql
SELECT display_name, email FROM customers ORDER BY created_at DESC LIMIT 1;
```
- [ ] Verify: values start with `enc:` prefix (encrypted in DB)
- [ ] Verify: API response returns decrypted plaintext (transparent to clients)

### 10.3 Billing Profile Encryption
- [ ] Create billing profile with legal_name, email, phone, tax_id
```sql
SELECT legal_name, email, phone, tax_identifier FROM customer_billing_profiles ORDER BY created_at DESC LIMIT 1;
```
- [ ] Verify: all PII fields start with `enc:` prefix in DB
- [ ] Verify: API returns decrypted values

### 10.4 Backward Compatibility
- [ ] Existing plaintext records (created before encryption key was set) should still read correctly
- [ ] Verify: no `enc:` prefix > returned as-is (no decryption attempted)

---

## FLOW 11: Webhook Replay Attack

- [ ] Capture a valid Stripe webhook payload + signature from stripe listen logs
- [ ] Replay it 5 minutes later using curl with the same signature
- [ ] Verify: rejected due to timestamp tolerance (>300s)
- [ ] Replay with a modified payload but same signature > verify rejected (signature mismatch)

---

## Phase 3: Core Happy Path

---

## FLOW 12: Complete Happy Path

### 12.1 Setup
- [ ] Settings: company "Demo Corp", prefix "DEMO", net terms 15, USD, tax rate 10% GST
- [ ] Create rating rule: key `api_calls`, flat, $0.01/call
- [ ] Create meter: key `api_calls`, aggregation sum, link rule
- [ ] Create plan: code `starter`, $29/mo, attach api_calls meter
- [ ] Create customer: "Alpha Inc", external_id `alpha_inc`, email
- [ ] Set up billing profile with address
- [ ] Set up payment method (4242 card)
- [ ] Create subscription: calendar billing, start immediately

### 12.2 Usage + Billing
- [ ] Ingest 10,000 API calls
- [ ] Click "Run Billing"
- [ ] Verify: invoice auto-finalized, auto-charged via Stripe webhook
- [ ] Verify: subtotal = $29 + $100 = $129, tax = $12.90, total = $141.90
- [ ] Verify: PDF correct (FROM/BILL TO/amounts/tax)
- [ ] Verify: invoice number uses "DEMO" prefix
- [ ] Verify: due date = issued + 15 days
- [ ] Verify: sidebar badge updates (Invoices count changes)

### 12.3 Billing Cycle
- [ ] Subscription detail: billing period advanced to next month
- [ ] Dashboard: MRR shows $29, revenue chart updated, 1 paid invoice

---

## FLOW 13: Basis-Point Tax Precision

### 13.1 Fractional Tax Rate
- [ ] Settings: set tax rate to 7.25% (725 basis points)
- [ ] Run billing for a $100 subtotal
- [ ] Verify: tax = $7.25 exactly (725 cents)
- [ ] Verify: `tax_rate_bp = 725` in the invoice record

### 13.2 Per-Line Rounding
- [ ] Run billing with 3 line items: $33.33, $33.33, $33.34
- [ ] Verify: per-line taxes sum exactly to invoice total tax
- [ ] Verify: no off-by-one-cent discrepancy

---

## FLOW 14: Invoice Idempotency

### 14.1 Double Billing Prevention
- [ ] Run billing for a subscription
- [ ] Note the billing period (start/end) on the generated invoice
- [ ] Run billing again immediately (same period)
- [ ] Verify: NO duplicate invoice created
- [ ] Verify: logs show "invoice already exists for billing period (idempotent skip)"

### 14.2 Verify Unique Constraint
```sql
SELECT COUNT(*) FROM invoices
WHERE subscription_id = '{sub_id}'
  AND billing_period_start = '{start}'
  AND billing_period_end = '{end}'
  AND status != 'voided';
```
- [ ] Verify: exactly 1 row

---

## FLOW 15: Auto-Charge Retry

### 15.1 Setup
- [ ] Create customer with a card that declines on charge (4000 0000 0000 0341)
- [ ] Create subscription, ingest usage, run billing

### 15.2 Verify Pending Flag
```sql
SELECT id, auto_charge_pending, payment_status FROM invoices ORDER BY created_at DESC LIMIT 1;
```
- [ ] Verify: `auto_charge_pending = true`, `payment_status = 'pending'`

### 15.3 Retry on Next Scheduler Tick
- [ ] Update customer's card to a working one (via Stripe dashboard or checkout)
- [ ] Wait for next scheduler tick (or trigger manually)
- [ ] Verify: `auto_charge_pending` cleared to `false` after successful charge
- [ ] Verify: invoice `payment_status = 'succeeded'`

---

## FLOW 16: Idempotency Key Behavior

### 16.1 Duplicate Prevention
- [ ] `POST /v1/customers` with `Idempotency-Key: test-123` header
- [ ] Repeat exact same request with same `Idempotency-Key: test-123`
- [ ] Verify: second response is identical to first (cached)
- [ ] Verify: only one customer actually created

### 16.2 Different Key = Different Request
- [ ] `POST /v1/customers` with `Idempotency-Key: test-456` and different body
- [ ] Verify: new customer created (different idempotency key)

---

## Phase 4: Subscription Lifecycle

---

## FLOW 17: Subscription Lifecycle

### 17.1 Trial
- [ ] Create subscription with trial_days = 7
- [ ] Verify: status active, trial end date shown
- [ ] Run billing > verify: no invoice (trial active)

### 17.2 Pause with Confirmation
- [ ] Pause active subscription
- [ ] Verify: confirmation dialog shown (billing stops, usage not metered)
- [ ] Verify: status = paused
- [ ] Run billing > verify: no invoice generated

### 17.3 Resume + Cancel
- [ ] Resume > verify active
- [ ] Cancel > confirmation dialog > verify canceled

---

## FLOW 18: Plan Change + Proration

### 18.1 Upgrade (immediate)
- [ ] Create second plan: "Enterprise", $99/mo
- [ ] Active subscription > Change Plan > Enterprise > "Apply immediately"
- [ ] Verify: toast shows "Proration invoice created for $XX.XX"
- [ ] Verify: proration invoice appears in Invoices list
- [ ] Verify: subscription now on Enterprise plan

### 18.2 Downgrade (immediate)
- [ ] Change plan back to Starter, immediate
- [ ] Verify: toast shows "$XX.XX credited to customer balance"
- [ ] Verify: credit balance increased

### 18.3 Change at Period End
- [ ] Change plan without "immediately" checked
- [ ] Verify: no proration, plan changes at next billing

---

## FLOW 19: Usage Caps

### 19.1 Setup
- [ ] Create subscription with usage_cap_units = 5000, overage_action = "block"
- [ ] Ingest 8000 events

### 19.2 Billing with Cap
- [ ] Run billing
- [ ] Verify: usage capped at 5000 (proportionally scaled across meters)
- [ ] Verify: invoice reflects capped usage, not full 8000

### 19.3 Overage = Charge
- [ ] Change subscription overage_action to "charge"
- [ ] Ingest 8000 more, run billing
- [ ] Verify: full 8000 billed (cap not enforced for "charge" mode)

---

## FLOW 20: Customer Price Overrides

### 20.1 Create Override
- [ ] `POST /v1/price-overrides` with customer_id, rating_rule_id, custom flat_amount_cents
- [ ] Verify: override saved

### 20.2 Billing with Override
- [ ] Ingest usage for the customer with the override
- [ ] Run billing
- [ ] Verify: invoice uses the override price, NOT the default rule price
- [ ] Verify: line item shows custom amount

### 20.3 Second Customer (No Override)
- [ ] Ingest same usage for a different customer
- [ ] Run billing
- [ ] Verify: invoice uses default rule price

---

## Phase 5: Billing Features

---

## FLOW 21: Credits (Grant, Apply, Expire)

### 21.1 Grant with Expiry
- [ ] Credits page > Grant Credits to Alpha Inc
- [ ] Amount: $50, description: "Welcome credit", expires in 30 days
- [ ] Verify: balance = $50
- [ ] Verify: ledger shows "Expires" column with date

### 21.2 Apply Credits to Invoice
- [ ] Ingest 5,000 API calls, run billing
- [ ] Verify: credits applied, amount_due reduced
- [ ] Verify: Stripe charged only the remaining amount
- [ ] Verify: ledger shows "Applied to invoice DEMO-XXXX" with negative amount

### 21.3 Credits > Invoice Amount
- [ ] Grant $500 credits
- [ ] Generate invoice for $79
- [ ] Verify: credits applied = $79, amount_due = $0, balance = $421
- [ ] Verify: Stripe NOT charged

### 21.4 Credit Deduction
- [ ] Credits page > Deduct $20 from Alpha Inc
- [ ] Verify: confirmation dialog shown
- [ ] Verify: balance reduced, ledger shows deduction entry

---

## FLOW 22: Credit Notes

### 22.1 On Unpaid Invoice (reduces amount due)
- [ ] Open finalized unpaid invoice > "Issue Credit"
- [ ] Quick-pick "Billing error", amount $20
- [ ] Verify: blue preview banner shows "Invoice amount due will be reduced by $20.00"
- [ ] Issue > verify amount_due reduced
- [ ] Verify: CN page stat cards update

### 22.2 On Paid Invoice -- Credit type
- [ ] Open paid invoice > "Issue Credit"
- [ ] Amount $15, reason "Service disruption", type "Credit to balance"
- [ ] Verify: preview shows "will be added to customer's credit balance"
- [ ] Verify: customer credit balance increased by $15
- [ ] Verify: invoice detail shows CN in "Post-payment adjustments"

### 22.3 On Paid Invoice -- Refund type
- [ ] Same paid invoice > "Issue Credit"
- [ ] Amount $10, type "Refund to payment method"
- [ ] Verify: Stripe refund processed (check Stripe CLI)
- [ ] Verify: CN listing shows "Refunded" badge
- [ ] Verify: credit balance NOT changed

### 22.4 CN Exceeding Limits
- [ ] Try CN for more than amount_due on unpaid invoice > verify error
- [ ] Try CN for more than amount_paid on paid invoice > verify error

### 22.5 CN Page Quality
- [ ] Verify: stat cards (Total Credited, Refunded to Card, Applied to Balance, Issued)
- [ ] Verify: tab filters (All/Draft/Issued/Voided) with counts
- [ ] Verify: search works (by number, customer, reason)
- [ ] Verify: draft CNs show Issue + Void buttons, issued CNs do not

---

## FLOW 23: Coupons + Plan Restrictions

### 23.1 Create Restricted Coupon
- [ ] Coupons > Create > code "PRO20", 20% off, restrict to Enterprise plan only
- [ ] Verify: plan checkboxes shown, Enterprise checked

### 23.2 Redeem
- [ ] Via API: redeem PRO20 for a Starter subscription
- [ ] Verify: error "coupon is not valid for this plan"
- [ ] Redeem PRO20 for Enterprise subscription > verify: discount applied

### 23.3 Copy Code
- [ ] Verify: copy button on coupon code works
- [ ] Verify: toast "Code copied"

---

## FLOW 24: Stripe Tax Integration

### 24.1 Enable Stripe Tax
- [ ] Enable feature flag: `PUT /v1/feature-flags/billing.stripe_tax` with `{"enabled": true}`
- [ ] Ensure customer has a billing profile with full address (country, state, postal code)

### 24.2 Billing with Stripe Tax
- [ ] Ingest usage, run billing
- [ ] Verify: invoice tax is calculated by Stripe (check `tax_name` field -- should show jurisdiction name like "CA Sales Tax")
- [ ] Verify: per-line-item tax amounts populated

### 24.3 Fallback on Stripe Error
- [ ] Temporarily set an invalid Stripe key
- [ ] Run billing > verify: invoice still generated with zero tax (graceful fallback)
- [ ] Check logs for "tax calculation failed" warning
- [ ] Restore valid key

### 24.4 Tax-Exempt Customer
- [ ] Set customer billing profile `tax_exempt: true`
- [ ] Run billing > verify: zero tax regardless of Stripe Tax or manual rate

---

## FLOW 25: Multiple Meters on One Plan

- [ ] Create second rule: `storage_gb`, $0.10/GB
- [ ] Create second meter: `storage_gb`, aggregation max
- [ ] Attach to plan
- [ ] Ingest: 2000 API calls + 50 GB storage
- [ ] Run billing
- [ ] Verify 3 line items: base ($29) + API (2000 x $0.02) + storage (50 x $0.10)

---

## FLOW 26: Negative Usage (Corrections)

- [ ] Ingest 1000 events for a meter
- [ ] Ingest -200 events (correction) for same meter
- [ ] Verify: UsageEvents page shows -200 in red text
- [ ] Verify: meter breakdown shows net 800
- [ ] Run billing > verify: billed for 800, not 1000

---

## FLOW 27: Manual Line Items

- [ ] Create a draft invoice (via API: POST /v1/invoices)
- [ ] Invoice detail > "Add Line Item"
- [ ] Description: "Setup fee", type: Add-On, qty: 1, price: $250
- [ ] Verify: line item added, invoice total updated
- [ ] Add another: "Consulting", qty: 2, price: $150
- [ ] Verify: total = $250 + $300 = $550
- [ ] Finalize > verify auto-charges

---

## FLOW 28: Large Batch Usage Ingestion

- [ ] POST /v1/usage-events/batch with 1000 events (script or curl)
- [ ] Verify: response shows `accepted: 1000, rejected: 0`
- [ ] Verify: usage events page shows correct total
- [ ] Include some duplicate idempotency keys > verify duplicates rejected, rest accepted
- [ ] Run billing > verify: usage correctly aggregated

---

## Phase 6: Invoice Operations

---

## FLOW 29: Void Invoice

- [ ] Void a finalized invoice with credits applied
- [ ] Verify: credits reversed (balance restored)
- [ ] Verify: Stripe PaymentIntent canceled
- [ ] Verify: dunning run resolved (if active)
- [ ] Verify: audit log shows "Voided invoice DEMO-XXXX"

---

## FLOW 30: Invoice Collect + Payment Timeline

### 30.1 Manual Collect
- [ ] Create a finalized unpaid invoice for a customer with a payment method
- [ ] `POST /v1/invoices/{id}/collect`
- [ ] Verify: Stripe PaymentIntent created, invoice payment_status updates

### 30.2 Payment Timeline
- [ ] `GET /v1/invoices/{id}/payment-timeline`
- [ ] Verify: shows all payment attempts with timestamps, amounts, status, Stripe PI IDs
- [ ] For a failed-then-succeeded invoice: verify both attempts shown in order

---

## FLOW 31: Invoice Email + PDF Preview

### 31.1 Send from UI
- [ ] Invoice detail > "Email" button
- [ ] Enter email address > send
- [ ] Verify: email sent (check SMTP logs or Mailtrap)
- [ ] Verify: PDF attached

### 31.2 PDF Preview
- [ ] Invoice detail > "Preview PDF"
- [ ] Verify: PDF renders in overlay iframe
- [ ] Verify: close via X button or backdrop click

---

## FLOW 32: Zero-Amount Invoice

- [ ] Create a plan with base_amount_cents = 0 and no meters
- [ ] Create subscription, run billing
- [ ] Verify: what happens? Either no invoice generated (preferred) or $0 invoice created
- [ ] If $0 invoice created: verify it's auto-marked as paid (no Stripe charge attempted)

---

## FLOW 33: Currency Consistency

- [ ] Create invoices in USD (default currency)
- [ ] Change tenant default_currency to EUR in Settings
- [ ] Run billing for same subscription
- [ ] Verify: NEW invoices use EUR
- [ ] Verify: EXISTING invoices still show USD (not retroactively changed)
- [ ] Verify: customer billing profile currency overrides tenant default

---

## FLOW 34: Credit Note on Voided Invoice

- [ ] Void an invoice
- [ ] Try to issue a credit note on the voided invoice
- [ ] Verify: error "cannot issue credit note on voided invoice" or similar
- [ ] Verify: credit note is NOT created

---

## Phase 7: Payment Recovery

---

## FLOW 35: Dunning (Payment Recovery)

### 35.1 Setup
- [ ] Create customer "Bad Card Corp" with card 4000 0000 0000 0341
- [ ] Create subscription, ingest usage, run billing
- [ ] Verify: payment fails, dunning run created

### 35.2 Dunning Page
- [ ] Verify: stat cards (Active, Escalated, Recovered, At Risk amount)
- [ ] Verify: tab filters (All/Active/Escalated/Recovered) with counts
- [ ] Verify: run shows state "Active", progress "No retries yet"
- [ ] Verify: Next Retry shows scheduled date
- [ ] Verify: sidebar Dunning badge shows count

### 35.3 Retry Cycle
- [ ] Fast-forward `next_action_at` in DB, wait for scheduler
- [ ] Verify: attempt count increments
- [ ] After max retries: state = "Escalated"

### 35.4 Resolution
- [ ] Click "Resolve" on active run
- [ ] Select "Payment recovered" > verify invoice marked paid
- [ ] Select "Manually resolved" on another > verify run resolved

### 35.5 Payment Update Email
- [ ] After payment failure, check server logs for payment update email
- [ ] Verify: email contains token-based URL (not plain invoice_id/customer_id)
- [ ] Verify: email says "This link will expire in 24 hours"
- [ ] See Flow 36 for full token flow testing

### 35.6 Per-Customer Dunning Override
- [ ] Customer detail > Dunning Override > Configure
- [ ] Set max retries = 5, grace period = 7 days
- [ ] Verify: saved and displayed in properties card
- [ ] Reset to Default > verify removed

---

## FLOW 36: Self-Service Payment Update (Token Flow)

### 36.1 Token Generation on Payment Failure
- [ ] Trigger a payment failure (bad card customer)
- [ ] Check server logs: should see "payment update email" with token URL
- [ ] Verify: URL format is `http://localhost:5173/update-payment?token=vlx_pt_...`

### 36.2 Customer Landing Page (No Login Required)
- [ ] Open an incognito/private browser window (NOT logged in)
- [ ] Paste the token URL
- [ ] Verify: page loads WITHOUT login -- shows customer name, invoice number, amount due
- [ ] Verify: "Secured by Stripe" text at bottom
- [ ] Verify: "Update Payment Method" button visible

### 36.3 Stripe Checkout Flow
- [ ] Click "Update Payment Method"
- [ ] Verify: redirected to Stripe Checkout (setup mode)
- [ ] Enter good card (4242...) > complete
- [ ] Verify: redirected back to update-payment page
- [ ] Verify: Stripe webhook fires `checkout.session.completed`
- [ ] Verify: customer payment method updated in Velox

### 36.4 Token Security
- [ ] Open the same token URL again after completing checkout
- [ ] Verify: "Link expired or invalid" error (token was marked as used)
- [ ] Try a random token: `/update-payment?token=vlx_pt_fake123`
- [ ] Verify: "Link expired or invalid" error
- [ ] Try with no token: `/update-payment`
- [ ] Verify: "No payment update token provided" error

### 36.5 Token Expiry
```sql
-- Manually expire a token for testing:
UPDATE payment_update_tokens SET expires_at = NOW() - INTERVAL '1 hour' WHERE id = (SELECT id FROM payment_update_tokens ORDER BY created_at DESC LIMIT 1);
```
- [ ] Open the token URL
- [ ] Verify: "Link expired or invalid" error

---

## Phase 8: Customer Management

---

## FLOW 37: Settings + Billing Profile

- [ ] Settings page: change company name, save
- [ ] Verify: "Saved" indicator, unsaved changes warning on navigation
- [ ] Change currency > verify invoices use new currency
- [ ] Customer detail > edit billing profile (address, tax ID)
- [ ] Verify: PDF shows updated bill-to info

---

## FLOW 38: Customer Portal API

- [ ] `GET /v1/customer-portal/{customer_id}/overview`
- [ ] Verify: returns active subscriptions, recent invoices, credit balance
- [ ] `GET /v1/customer-portal/{customer_id}/subscriptions`
- [ ] Verify: returns customer's subscriptions only
- [ ] `GET /v1/customer-portal/{customer_id}/invoices`
- [ ] Verify: returns customer's invoices only

---

## FLOW 39: GDPR Data Export + Deletion

### 39.1 Data Export (Right to Portability)
- [ ] `GET /v1/customers/{id}/export`
- [ ] Verify: response includes customer record, billing profile, invoices, subscriptions, credit ledger, credit balance
- [ ] Verify: Stripe IDs are redacted (only last 4 chars visible)
- [ ] Verify: payment method details redacted (card_last4 visible, full IDs hidden)

### 39.2 Data Deletion (Right to Erasure)
- [ ] Try deleting a customer with active subscriptions
- [ ] Verify: error "customer has active subscriptions; cancel them before deletion"
- [ ] Cancel the subscription first, then retry
- [ ] `POST /v1/customers/{id}/delete-data`
- [ ] Verify: customer display_name changed to "Deleted Customer"
- [ ] Verify: customer email cleared
- [ ] Verify: billing profile PII anonymized (name, email, phone, address, tax IDs)
- [ ] Verify: customer status set to "archived"
- [ ] Verify: invoices still exist (financial records preserved for compliance)
- [ ] Verify: audit log entry created for deletion
- [ ] Verify: export endpoint for deleted customer returns anonymized data

---

## FLOW 40: Customer Archival Cascade

### 40.1 Archive from UI
- [ ] Customer detail > click "Archive" button (only visible when active)
- [ ] Verify: confirmation dialog appears with warning
- [ ] Confirm > verify: amber banner "This customer has been archived. All data is read-only."
- [ ] Verify: all action buttons hidden (Edit, Set Up Billing, Configure, Set Up Payment, + Add)
- [ ] Verify: "Restore Customer" button visible in the banner
- [ ] Verify: customer badge shows "archived" (gray)

### 40.2 Billing stops
- [ ] Run billing > verify: no invoice generated for archived customer's subscriptions
- [ ] Verify: existing invoices still accessible (read-only)
- [ ] Verify: credits balance still visible

### 40.3 Restore
- [ ] Click "Restore Customer" in the banner
- [ ] Verify: banner disappears, action buttons reappear
- [ ] Verify: customer status back to "active"

### 40.4 List filter
- [ ] Customers list > click "Archived" tab
- [ ] Verify: shows archived customers (or "No archived customers" + Clear filter)
- [ ] Verify: tab stays visible even with zero results (can switch back to "All")

---

## Phase 9: Platform Features

---

## FLOW 41: Feature Flags

### 41.1 List Flags
- [ ] `GET /v1/feature-flags`
- [ ] Verify: all seeded flags returned (billing.auto_charge, billing.tax_basis_points, webhooks.enabled, dunning.enabled, credits.auto_apply, billing.stripe_tax)
- [ ] Verify: each flag has key, enabled, description, timestamps

### 41.2 Toggle Global Flag
- [ ] `PUT /v1/feature-flags/webhooks.enabled` with `{"enabled": false}`
- [ ] Verify: flag disabled globally
- [ ] Trigger a webhook event > verify: NOT delivered
- [ ] Re-enable > verify: delivery resumes

### 41.3 Per-Tenant Override
- [ ] `PUT /v1/feature-flags/dunning.enabled/overrides/{tenant_id}` with `{"enabled": false}`
- [ ] Verify: dunning disabled for this tenant only
- [ ] `DELETE /v1/feature-flags/dunning.enabled/overrides/{tenant_id}`
- [ ] Verify: tenant falls back to global setting

### 41.4 Cache Behavior
- [ ] Toggle a flag, immediately check `IsEnabled` -- verify change reflects within 30s

---

## FLOW 42: Webhook Secret Rotation

- [ ] Webhooks > Endpoints tab > click "Rotate Secret" on an endpoint
- [ ] Verify: new secret shown in modal (whsec_...)
- [ ] Verify: old secret no longer works for signature verification
- [ ] Verify: new secret works

---

## FLOW 43: Webhook Delivery Stats

- [ ] Webhooks > Endpoints tab
- [ ] Verify: "Success Rate" column shows percentage
- [ ] Color: green (>=95%), amber (70-94%), red (<70%)
- [ ] Replay a failed event > verify success rate updates

---

## FLOW 44: Usage Summary

- [ ] Ingest events across multiple meters for a customer
- [ ] `GET /v1/usage-summary/{customer_id}?from=2026-04-01&to=2026-04-30`
- [ ] Verify: aggregated totals per meter for the period
- [ ] Verify: quantities match what was ingested

---

## FLOW 45: Audit Log

- [ ] Perform several actions: create customer, grant credits, void invoice, change plan
- [ ] Audit Log page > verify all actions logged
- [ ] Verify: stat cards (Total, Today, Unique Actors, Destructive Actions)
- [ ] Verify: destructive actions (void, cancel) have red left border
- [ ] Click a row > verify expandable detail with metadata (amounts, IDs)
- [ ] Click "View" link > navigates to the actual resource
- [ ] Filter by resource type "Invoice" > verify filtered
- [ ] Filter by action "void" > verify filtered
- [ ] Date range filter > verify server-side filtering
- [ ] Export CSV > verify all entries exported

---

## FLOW 46: Empty Billing Cycle

- [ ] Ensure no subscriptions are due for billing (either no subscriptions, or all already billed)
- [ ] Click "Run Billing" or wait for scheduler
- [ ] Verify: "0 invoice(s) generated" -- clean exit, no errors
- [ ] Verify: no error logs in server output
- [ ] Verify: dashboard stats unchanged

---

## Phase 10: Observability

---

## FLOW 47: OpenTelemetry Tracing

### 47.1 Enable Tracing
```bash
docker run -d --name jaeger -p 16686:16686 -p 4318:4318 jaegertracing/jaeger:2
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 go run ./cmd/velox
```

### 47.2 Verify Traces
- [ ] Make several API requests (create customer, ingest usage, run billing)
- [ ] Open Jaeger UI at `http://localhost:16686`
- [ ] Verify: traces appear for service "velox"
- [ ] Verify: HTTP spans show method + path (e.g., `POST /v1/customers`)
- [ ] Verify: billing.RunCycle span with `batch_size` attribute
- [ ] Verify: billing.BillSubscription spans with `subscription_id`, `tenant_id` attributes
- [ ] Verify: trace context propagated (parent-child relationship between HTTP > billing spans)

---

## Phase 11: UI/UX

---

## FLOW 48: Dashboard Quality

- [ ] Verify: 4 KPI cards — MRR (with sparkline + trend %), Active Customers, Failed Payments (red if >0), Revenue 30d
- [ ] Verify: revenue bar chart (compact, no axes, link to Analytics)
- [ ] Verify: "Recent Activity" — last 5 invoices with status dot, badge, amount, relative time
- [ ] Verify: clicking an invoice row navigates to invoice detail
- [ ] Verify: Get Started checklist shows progress (numbered steps, checkmarks when done)
- [ ] Verify: Get Started disappears when all 4 steps complete
- [ ] Verify: "Trigger Billing" button in header, shows result after run
- [ ] Verify: no overlap with Analytics page (Dashboard has no period selector, no detailed charts)

---

## FLOW 48b: Analytics Page

- [ ] Navigate to Analytics (sidebar or Dashboard "View analytics →" link)
- [ ] Verify: revenue trend area chart with period tabs (30 days / 90 days / 12 months)
- [ ] Switch periods > verify: chart data updates
- [ ] Verify: chart has X-axis dates, Y-axis amounts, hover tooltips
- [ ] Verify: Payment Success Rate donut with percentage in center
- [ ] Verify: Invoice Summary bar chart (Paid/Open/Failed/Dunning)
- [ ] Verify: Customer Stats card (Active Customers, Subscriptions, Dunning, Open Invoices)
- [ ] Verify: Financial Summary card (MRR, Total Revenue, Outstanding AR, Avg Invoice, Credit Balance)
- [ ] Verify: no overlap with Dashboard (Analytics has charts + details, Dashboard has activity + KPIs)

---

## FLOW 49: Usage Events Page Quality

- [ ] Verify: stat cards (Total Events, Total Units, Active Meters, Active Customers)
- [ ] Verify: meter breakdown with horizontal bars
- [ ] Filter by customer > verify breakdown updates
- [ ] Filter by date range > verify
- [ ] Export CSV

---

## FLOW 50: Cmd+K Command Palette

- [ ] Press Cmd+K (Mac) or Ctrl+K (Windows)
- [ ] Verify: palette opens with navigation items visible
- [ ] Type "inv" > verify: Invoices nav item filtered, any matching invoices shown
- [ ] Arrow down to select a result > verify highlight moves
- [ ] Press Enter > verify: navigated to selected page
- [ ] Open again, type customer name > verify: customer appears in results
- [ ] Click a result > verify: navigates and closes palette
- [ ] Press Escape > verify: palette closes
- [ ] Verify: palette works from any page

---

## FLOW 50b: Keyboard Shortcuts

- [ ] Press `?` > verify: keyboard help overlay appears
- [ ] Verify: shows navigation shortcuts (g+d, g+c, g+i, g+s, g+u, g+p, g+a, g+k)
- [ ] Press Escape > verify: overlay closes
- [ ] Press `g` then `c` > verify: navigated to Customers
- [ ] Press `g` then `i` > verify: navigated to Invoices
- [ ] Press `g` then `a` > verify: navigated to Analytics
- [ ] Verify: shortcuts don't fire when typing in an input field

---

## FLOW 51: Dark Mode

- [ ] Click the dark mode toggle (Sun/Moon icon in sidebar footer)
- [ ] Verify: entire UI switches to dark theme
- [ ] Verify: sidebar, cards, tables, modals, forms all dark
- [ ] Verify: charts (revenue chart) readable in dark mode
- [ ] Verify: badges and status colors still distinguishable
- [ ] Refresh page > verify: dark mode persists (localStorage)
- [ ] Toggle back to light > verify: clean switch
- [ ] Delete localStorage 'velox-theme' > verify: follows system preference

---

## FLOW 52: Responsive / Mobile

- [ ] Open UI on tablet width (768px)
- [ ] Verify: tables scroll horizontally with fade indicator
- [ ] Verify: sidebar collapses to hamburger menu
- [ ] Verify: stat cards stack to 2-column grid
- [ ] Verify: modals don't overflow screen

---

## Phase 12: Edge Cases

---

## FLOW 53: Edge Cases

| Test | Expected |
|------|----------|
| Zero usage | Base fee only invoice |
| Meter without rating rule | Usage silently skipped |
| Duplicate idempotency key | 409 Conflict |
| Invalid external_customer_id | "customer not found" error |
| Invalid event_name | "meter not found" error |
| Void already voided invoice | Error message |
| Finalize non-draft invoice | Error message |
| Duplicate subscription code | Humanized error |
| Cancel canceled subscription | Error message |
| Revoke current session key | Warning dialog about logout |
| Create subscription for archived customer | Should work (backend allows) |
| Escape from modal with form data | "Unsaved changes" confirmation |

---

## Test Results

| # | Flow | Status | Notes |
|---|------|--------|-------|
| 1 | Config Validation | | |
| 2 | Health Checks | | |
| 3 | Migration Management (CLI) | | |
| 4 | API Key Permissions | | |
| 5 | Expired API Key Rejection | | |
| 6 | Multi-tenant RLS Isolation | | |
| 7 | Bootstrap Lockdown | | |
| 8 | Rate Limiting (Redis) | | |
| 9 | Security Headers + Metrics Auth | | |
| 10 | PII Encryption at Rest | | |
| 11 | Webhook Replay Attack | | |
| 12 | Complete Happy Path | | |
| 13 | Basis-Point Tax Precision | | |
| 14 | Invoice Idempotency | | |
| 15 | Auto-Charge Retry | | |
| 16 | Idempotency Key Behavior | | |
| 17 | Subscription Lifecycle | | |
| 18 | Plan Change + Proration | | |
| 19 | Usage Caps | | |
| 20 | Customer Price Overrides | | |
| 21 | Credits (Grant, Apply, Expire) | | |
| 22 | Credit Notes | | |
| 23 | Coupons + Plan Restrictions | | |
| 24 | Stripe Tax Integration | | |
| 25 | Multiple Meters | | |
| 26 | Negative Usage (Corrections) | | |
| 27 | Manual Line Items | | |
| 28 | Large Batch Usage Ingestion | | |
| 29 | Void Invoice | | |
| 30 | Invoice Collect + Payment Timeline | | |
| 31 | Invoice Email + PDF Preview | | |
| 32 | Zero-Amount Invoice | | |
| 33 | Currency Consistency | | |
| 34 | Credit Note on Voided Invoice | | |
| 35 | Dunning (Payment Recovery) | | |
| 36 | Self-Service Payment Update (Token) | | |
| 37 | Settings + Billing Profile | | |
| 38 | Customer Portal API | | |
| 39 | GDPR Data Export + Deletion | | |
| 40 | Customer Archival Cascade | | |
| 41 | Feature Flags | | |
| 42 | Webhook Secret Rotation | | |
| 43 | Webhook Delivery Stats | | |
| 44 | Usage Summary | | |
| 45 | Audit Log | | |
| 46 | Empty Billing Cycle | | |
| 47 | OpenTelemetry Tracing | | |
| 48 | Dashboard Quality | | |
| 48b | Analytics Page | | |
| 49 | Usage Events Page Quality | | |
| 50 | Cmd+K Command Palette | | |
| 50b | Keyboard Shortcuts | | |
| 51 | Dark Mode | | |
| 52 | Responsive / Mobile | | |
| 53 | Edge Cases | | |
