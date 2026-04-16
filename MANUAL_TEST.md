# Velox Manual E2E Test Plan

## Prerequisites

```bash
# 1. First-time setup
cp .env.example .env               # Fill in STRIPE_SECRET_KEY and STRIPE_WEBHOOK_SECRET

# 2. Start infrastructure + backend + frontend (3 terminals)
make up                             # Terminal 1: Postgres + Redis
make dev                            # Terminal 2: API server on :8080 (auto-migrates)
make web-dev                        # Terminal 3: Frontend on :5173

# 3. Stripe webhook forwarding (4th terminal)
stripe listen --forward-to localhost:8080/v1/webhooks/stripe
```

**First run:** Open http://localhost:5173 and follow the Get Started flow, or run `make bootstrap`.

**Fresh DB:** `docker compose down -v && make up` then `make dev` (re-runs all migrations).

**Run tests:** `make test-unit` (all 26 packages).

### Test Cards
| Card | Behavior |
|------|----------|
| `4242 4242 4242 4242` | Always succeeds |
| `4000 0000 0000 0341` | Attaches OK, declines on charge |
| `4000 0000 0000 9995` | Always declines |

---

## FLOW 1: Complete Happy Path

### 1.1 Setup
- [ ] Settings: company "Demo Corp", prefix "DEMO", net terms 15, USD, tax rate 10% GST
- [ ] Create rating rule: key `api_calls`, flat, $0.01/call
- [ ] Create meter: key `api_calls`, aggregation sum, link rule
- [ ] Create plan: code `starter`, $29/mo, attach api_calls meter
- [ ] Create customer: "Alpha Inc", external_id `alpha_inc`, email
- [ ] Set up billing profile with address
- [ ] Set up payment method (4242 card)
- [ ] Create subscription: calendar billing, start immediately

### 1.2 Usage + Billing
- [ ] Ingest 10,000 API calls
- [ ] Click "Run Billing"
- [ ] Verify: invoice auto-finalized, auto-charged via Stripe webhook
- [ ] Verify: subtotal = $29 + $100 = $129, tax = $12.90, total = $141.90
- [ ] Verify: PDF correct (FROM/BILL TO/amounts/tax)
- [ ] Verify: invoice number uses "DEMO" prefix
- [ ] Verify: due date = issued + 15 days
- [ ] Verify: sidebar badge updates (Invoices count changes)

### 1.3 Billing Cycle
- [ ] Subscription detail: billing period advanced to next month
- [ ] Dashboard: MRR shows $29, revenue chart updated, 1 paid invoice

---

## FLOW 2: Credits (Grant, Apply, Expire)

### 2.1 Grant with Expiry
- [ ] Credits page > Grant Credits to Alpha Inc
- [ ] Amount: $50, description: "Welcome credit", expires in 30 days
- [ ] Verify: balance = $50
- [ ] Verify: ledger shows "Expires" column with date

### 2.2 Apply Credits to Invoice
- [ ] Ingest 5,000 API calls, run billing
- [ ] Verify: credits applied, amount_due reduced
- [ ] Verify: Stripe charged only the remaining amount
- [ ] Verify: ledger shows "Applied to invoice DEMO-XXXX" with negative amount

### 2.3 Credits > Invoice Amount
- [ ] Grant $500 credits
- [ ] Generate invoice for $79
- [ ] Verify: credits applied = $79, amount_due = $0, balance = $421
- [ ] Verify: Stripe NOT charged

### 2.4 Credit Deduction
- [ ] Credits page > Deduct $20 from Alpha Inc
- [ ] Verify: confirmation dialog shown
- [ ] Verify: balance reduced, ledger shows deduction entry

---

## FLOW 3: Credit Notes

### 3.1 On Unpaid Invoice (reduces amount due)
- [ ] Open finalized unpaid invoice > "Issue Credit"
- [ ] Quick-pick "Billing error", amount $20
- [ ] Verify: blue preview banner shows "Invoice amount due will be reduced by $20.00"
- [ ] Issue > verify amount_due reduced
- [ ] Verify: CN page stat cards update

### 3.2 On Paid Invoice — Credit type
- [ ] Open paid invoice > "Issue Credit"
- [ ] Amount $15, reason "Service disruption", type "Credit to balance"
- [ ] Verify: preview shows "will be added to customer's credit balance"
- [ ] Verify: customer credit balance increased by $15
- [ ] Verify: invoice detail shows CN in "Post-payment adjustments"

### 3.3 On Paid Invoice — Refund type
- [ ] Same paid invoice > "Issue Credit"
- [ ] Amount $10, type "Refund to payment method"
- [ ] Verify: Stripe refund processed (check Stripe CLI)
- [ ] Verify: CN listing shows "Refunded" badge
- [ ] Verify: credit balance NOT changed

### 3.4 CN Exceeding Limits
- [ ] Try CN for more than amount_due on unpaid invoice > verify error
- [ ] Try CN for more than amount_paid on paid invoice > verify error

### 3.5 CN Page Quality
- [ ] Verify: stat cards (Total Credited, Refunded to Card, Applied to Balance, Issued)
- [ ] Verify: tab filters (All/Draft/Issued/Voided) with counts
- [ ] Verify: search works (by number, customer, reason)
- [ ] Verify: draft CNs show Issue + Void buttons, issued CNs do not

---

## FLOW 4: Void Invoice

- [ ] Void a finalized invoice with credits applied
- [ ] Verify: credits reversed (balance restored)
- [ ] Verify: Stripe PaymentIntent canceled
- [ ] Verify: dunning run resolved (if active)
- [ ] Verify: audit log shows "Voided invoice DEMO-XXXX"

---

## FLOW 5: Dunning (Payment Recovery)

### 5.1 Setup
- [ ] Create customer "Bad Card Corp" with card 4000 0000 0000 0341
- [ ] Create subscription, ingest usage, run billing
- [ ] Verify: payment fails, dunning run created

### 5.2 Dunning Page
- [ ] Verify: stat cards (Active, Escalated, Recovered, At Risk amount)
- [ ] Verify: tab filters (All/Active/Escalated/Recovered) with counts
- [ ] Verify: run shows state "Active", progress "No retries yet"
- [ ] Verify: Next Retry shows scheduled date
- [ ] Verify: sidebar Dunning badge shows count

### 5.3 Retry Cycle
- [ ] Fast-forward `next_action_at` in DB, wait for scheduler
- [ ] Verify: attempt count increments
- [ ] After max retries: state = "Escalated"

### 5.4 Resolution
- [ ] Click "Resolve" on active run
- [ ] Select "Payment recovered" > verify invoice marked paid
- [ ] Select "Manually resolved" on another > verify run resolved

### 5.5 Payment Update Email
- [ ] After payment failure, check server logs for payment update email
- [ ] Verify: email contains token-based URL (not plain invoice_id/customer_id)
- [ ] Verify: email says "This link will expire in 24 hours"
- [ ] See Flow 12.5 for full token flow testing

### 5.6 Per-Customer Dunning Override
- [ ] Customer detail > Dunning Override > Configure
- [ ] Set max retries = 5, grace period = 7 days
- [ ] Verify: saved and displayed in properties card
- [ ] Reset to Default > verify removed

---

## FLOW 6: Plan Change with Proration

### 6.1 Upgrade (immediate)
- [ ] Create second plan: "Enterprise", $99/mo
- [ ] Active subscription > Change Plan > Enterprise > "Apply immediately"
- [ ] Verify: toast shows "Proration invoice created for $XX.XX"
- [ ] Verify: proration invoice appears in Invoices list
- [ ] Verify: subscription now on Enterprise plan

### 6.2 Downgrade (immediate)
- [ ] Change plan back to Starter, immediate
- [ ] Verify: toast shows "$XX.XX credited to customer balance"
- [ ] Verify: credit balance increased

### 6.3 Change at Period End
- [ ] Change plan without "immediately" checked
- [ ] Verify: no proration, plan changes at next billing

---

## FLOW 7: Usage Caps

### 7.1 Setup
- [ ] Create subscription with usage_cap_units = 5000, overage_action = "block"
- [ ] Ingest 8000 events

### 7.2 Billing with Cap
- [ ] Run billing
- [ ] Verify: usage capped at 5000 (proportionally scaled across meters)
- [ ] Verify: invoice reflects capped usage, not full 8000

### 7.3 Overage = Charge
- [ ] Change subscription overage_action to "charge"
- [ ] Ingest 8000 more, run billing
- [ ] Verify: full 8000 billed (cap not enforced for "charge" mode)

---

## FLOW 8: Coupons with Plan Restrictions

### 8.1 Create Restricted Coupon
- [ ] Coupons > Create > code "PRO20", 20% off, restrict to Enterprise plan only
- [ ] Verify: plan checkboxes shown, Enterprise checked

### 8.2 Redeem
- [ ] Via API: redeem PRO20 for a Starter subscription
- [ ] Verify: error "coupon is not valid for this plan"
- [ ] Redeem PRO20 for Enterprise subscription > verify: discount applied

### 8.3 Copy Code
- [ ] Verify: copy button on coupon code works
- [ ] Verify: toast "Code copied"

---

## FLOW 9: Negative Usage (Corrections)

- [ ] Ingest 1000 events for a meter
- [ ] Ingest -200 events (correction) for same meter
- [ ] Verify: UsageEvents page shows -200 in red text
- [ ] Verify: meter breakdown shows net 800
- [ ] Run billing > verify: billed for 800, not 1000

---

## FLOW 10: Manual Line Items

- [ ] Create a draft invoice (via API: POST /v1/invoices)
- [ ] Invoice detail > "Add Line Item"
- [ ] Description: "Setup fee", type: Add-On, qty: 1, price: $250
- [ ] Verify: line item added, invoice total updated
- [ ] Add another: "Consulting", qty: 2, price: $150
- [ ] Verify: total = $250 + $300 = $550
- [ ] Finalize > verify auto-charges

---

## FLOW 11: Webhook Secret Rotation

- [ ] Webhooks > Endpoints tab > click "Rotate Secret" on an endpoint
- [ ] Verify: new secret shown in modal (whsec_...)
- [ ] Verify: old secret no longer works for signature verification
- [ ] Verify: new secret works

---

## FLOW 12: Invoice Email

### 12.1 Send from UI
- [ ] Invoice detail > "Email" button
- [ ] Enter email address > send
- [ ] Verify: email sent (check SMTP logs or Mailtrap)
- [ ] Verify: PDF attached

### 12.2 PDF Preview
- [ ] Invoice detail > "Preview PDF"
- [ ] Verify: PDF renders in overlay iframe
- [ ] Verify: close via X button or backdrop click

---

## FLOW 12.5: Self-Service Payment Update (Token Flow)

### 12.5.1 Token Generation on Payment Failure
- [ ] Trigger a payment failure (bad card customer)
- [ ] Check server logs: should see "payment update email" with token URL
- [ ] Verify: URL format is `http://localhost:5173/update-payment?token=vlx_pt_...`

### 12.5.2 Customer Landing Page (No Login Required)
- [ ] Open an incognito/private browser window (NOT logged in)
- [ ] Paste the token URL
- [ ] Verify: page loads WITHOUT login — shows customer name, invoice number, amount due
- [ ] Verify: "Secured by Stripe" text at bottom
- [ ] Verify: "Update Payment Method" button visible

### 12.5.3 Stripe Checkout Flow
- [ ] Click "Update Payment Method"
- [ ] Verify: redirected to Stripe Checkout (setup mode)
- [ ] Enter good card (4242...) > complete
- [ ] Verify: redirected back to update-payment page
- [ ] Verify: Stripe webhook fires `checkout.session.completed`
- [ ] Verify: customer payment method updated in Velox

### 12.5.4 Token Security
- [ ] Open the same token URL again after completing checkout
- [ ] Verify: "Link expired or invalid" error (token was marked as used)
- [ ] Try a random token: `/update-payment?token=vlx_pt_fake123`
- [ ] Verify: "Link expired or invalid" error
- [ ] Try with no token: `/update-payment`
- [ ] Verify: "No payment update token provided" error

### 12.5.5 Token Expiry
```sql
-- Manually expire a token for testing:
UPDATE payment_update_tokens SET expires_at = NOW() - INTERVAL '1 hour' WHERE id = (SELECT id FROM payment_update_tokens ORDER BY created_at DESC LIMIT 1);
```
- [ ] Open the token URL
- [ ] Verify: "Link expired or invalid" error

---

## FLOW 13: Subscription Lifecycle

### 13.1 Trial
- [ ] Create subscription with trial_days = 7
- [ ] Verify: status active, trial end date shown
- [ ] Run billing > verify: no invoice (trial active)

### 13.2 Pause with Confirmation
- [ ] Pause active subscription
- [ ] Verify: confirmation dialog shown (billing stops, usage not metered)
- [ ] Verify: status = paused
- [ ] Run billing > verify: no invoice generated

### 13.3 Resume + Cancel
- [ ] Resume > verify active
- [ ] Cancel > confirmation dialog > verify canceled

---

## FLOW 14: Audit Log

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

## FLOW 15: Multiple Meters on One Plan

- [ ] Create second rule: `storage_gb`, $0.10/GB
- [ ] Create second meter: `storage_gb`, aggregation max
- [ ] Attach to plan
- [ ] Ingest: 2000 API calls + 50 GB storage
- [ ] Run billing
- [ ] Verify 3 line items: base ($29) + API (2000 × $0.02) + storage (50 × $0.10)

---

## FLOW 16: Usage Events Page Quality

- [ ] Verify: stat cards (Total Events, Total Units, Active Meters, Active Customers)
- [ ] Verify: meter breakdown with horizontal bars
- [ ] Filter by customer > verify breakdown updates
- [ ] Filter by date range > verify
- [ ] Export CSV

---

## FLOW 17: Dashboard Quality

- [ ] Verify: KPI cards (MRR, Active Customers, Outstanding AR, Paid 30d)
- [ ] Verify: revenue chart with period selector (30d/90d/12m)
- [ ] Verify: dunning + credits cards link to their pages
- [ ] Verify: Get Started checklist shows progress (steps checked off)
- [ ] Verify: "Run Billing" button shows timestamp after run

---

## FLOW 18: Webhook Delivery Stats

- [ ] Webhooks > Endpoints tab
- [ ] Verify: "Success Rate" column shows percentage
- [ ] Color: green (>=95%), amber (70-94%), red (<70%)
- [ ] Replay a failed event > verify success rate updates

---

## FLOW 19: API Key Permissions

- [ ] Secret key: full access to all endpoints
- [ ] Publishable key: read-only (can't create plans, can read customers)
- [ ] Revoked key: 401 on any request
- [ ] Create key > verify raw key shown once with copy button
- [ ] Verify: key type description shown in modal

---

## FLOW 20: Settings + Billing Profile

- [ ] Settings page: change company name, save
- [ ] Verify: "Saved" indicator, unsaved changes warning on navigation
- [ ] Change currency > verify invoices use new currency
- [ ] Customer detail > edit billing profile (address, tax ID)
- [ ] Verify: PDF shows updated bill-to info

---

## FLOW 21: Edge Cases

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

## FLOW 22: Responsive / Mobile

- [ ] Open UI on tablet width (768px)
- [ ] Verify: tables scroll horizontally with fade indicator
- [ ] Verify: sidebar collapses to hamburger menu
- [ ] Verify: stat cards stack to 2-column grid
- [ ] Verify: modals don't overflow screen

---

## FLOW 23: GDPR Data Export + Deletion

### 23.1 Data Export (Right to Portability)
- [ ] `GET /v1/customers/{id}/export`
- [ ] Verify: response includes customer record, billing profile, invoices, subscriptions, credit ledger, credit balance
- [ ] Verify: Stripe IDs are redacted (only last 4 chars visible)
- [ ] Verify: payment method details redacted (card_last4 visible, full IDs hidden)

### 23.2 Data Deletion (Right to Erasure)
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

## FLOW 24: Feature Flags

### 24.1 List Flags
- [ ] `GET /v1/feature-flags`
- [ ] Verify: all seeded flags returned (billing.auto_charge, billing.tax_basis_points, webhooks.enabled, dunning.enabled, credits.auto_apply, billing.stripe_tax)
- [ ] Verify: each flag has key, enabled, description, timestamps

### 24.2 Toggle Global Flag
- [ ] `PUT /v1/feature-flags/webhooks.enabled` with `{"enabled": false}`
- [ ] Verify: flag disabled globally
- [ ] Trigger a webhook event > verify: NOT delivered
- [ ] Re-enable > verify: delivery resumes

### 24.3 Per-Tenant Override
- [ ] `PUT /v1/feature-flags/dunning.enabled/overrides/{tenant_id}` with `{"enabled": false}`
- [ ] Verify: dunning disabled for this tenant only
- [ ] `DELETE /v1/feature-flags/dunning.enabled/overrides/{tenant_id}`
- [ ] Verify: tenant falls back to global setting

### 24.4 Cache Behavior
- [ ] Toggle a flag, immediately check `IsEnabled` — verify change reflects within 30s

---

## FLOW 25: Stripe Tax Integration

### 25.1 Enable Stripe Tax
- [ ] Enable feature flag: `PUT /v1/feature-flags/billing.stripe_tax` with `{"enabled": true}`
- [ ] Ensure customer has a billing profile with full address (country, state, postal code)

### 25.2 Billing with Stripe Tax
- [ ] Ingest usage, run billing
- [ ] Verify: invoice tax is calculated by Stripe (check `tax_name` field — should show jurisdiction name like "CA Sales Tax")
- [ ] Verify: per-line-item tax amounts populated

### 25.3 Fallback on Stripe Error
- [ ] Temporarily set an invalid Stripe key
- [ ] Run billing > verify: invoice still generated with zero tax (graceful fallback)
- [ ] Check logs for "tax calculation failed" warning
- [ ] Restore valid key

### 25.4 Tax-Exempt Customer
- [ ] Set customer billing profile `tax_exempt: true`
- [ ] Run billing > verify: zero tax regardless of Stripe Tax or manual rate

---

## FLOW 26: PII Encryption at Rest

### 26.1 Enable Encryption
- [ ] Set `VELOX_ENCRYPTION_KEY` (64 hex chars) and restart
- [ ] Create a new customer with email and display name

### 26.2 Verify Encryption
```sql
SELECT display_name, email FROM customers ORDER BY created_at DESC LIMIT 1;
```
- [ ] Verify: values start with `enc:` prefix (encrypted in DB)
- [ ] Verify: API response returns decrypted plaintext (transparent to clients)

### 26.3 Billing Profile Encryption
- [ ] Create billing profile with legal_name, email, phone, tax_id
```sql
SELECT legal_name, email, phone, tax_identifier FROM customer_billing_profiles ORDER BY created_at DESC LIMIT 1;
```
- [ ] Verify: all PII fields start with `enc:` prefix in DB
- [ ] Verify: API returns decrypted values

### 26.4 Backward Compatibility
- [ ] Existing plaintext records (created before encryption key was set) should still read correctly
- [ ] Verify: no `enc:` prefix → returned as-is (no decryption attempted)

---

## FLOW 27: Rate Limiting (Redis)

### 27.1 Basic Rate Limit
- [ ] Send 100+ requests in rapid succession (use `for i in {1..110}; do curl -s ...; done`)
- [ ] Verify: first 100 return 200, remaining return 429
- [ ] Verify: response headers present: `X-RateLimit-Limit`, `X-RateLimit-Remaining`, `X-RateLimit-Reset`
- [ ] Verify: `Retry-After` header on 429 responses

### 27.2 Per-Tenant Isolation
- [ ] Exhaust rate limit for Tenant A
- [ ] Verify: Tenant B requests still succeed (separate buckets)

### 27.3 Fail-Open
- [ ] Stop Redis (`docker compose stop redis`)
- [ ] Verify: API requests still succeed (no 429s, rate limiting disabled)
- [ ] Check logs for "rate_limiter: redis error, failing open"
- [ ] Restart Redis > verify: rate limiting resumes

### 27.4 Health/Metrics Bypass
- [ ] Verify: `/health`, `/health/ready`, `/metrics` are never rate-limited

---

## FLOW 28: Security Headers + Metrics Auth

### 28.1 Security Headers
- [ ] `curl -I http://localhost:8080/v1/customers`
- [ ] Verify: `X-Content-Type-Options: nosniff`
- [ ] Verify: `X-Frame-Options: DENY`
- [ ] Verify: `Cache-Control: no-store`
- [ ] Verify: `Referrer-Policy: strict-origin-when-cross-origin`
- [ ] In staging/production (APP_ENV != local): verify `Strict-Transport-Security` header present

### 28.2 Metrics Auth
- [ ] Set `METRICS_TOKEN=secret123` env var, restart
- [ ] `curl http://localhost:8080/metrics` > verify: 401 Unauthorized
- [ ] `curl -H "Authorization: Bearer secret123" http://localhost:8080/metrics` > verify: 200 with Prometheus metrics
- [ ] Unset `METRICS_TOKEN`, restart > verify: `/metrics` accessible without auth (dev mode)

---

## FLOW 29: Health Checks

### 29.1 Liveness
- [ ] `GET /health` > verify: `{"status": "ok"}`

### 29.2 Readiness (Healthy)
- [ ] `GET /health/ready` > verify: `{"status": "ok", "checks": {"api": "ok", "database": "ok", "scheduler": "ok"}}`

### 29.3 Database Down
- [ ] Stop postgres
- [ ] `GET /health/ready` > verify: 503, `{"status": "degraded", "checks": {"database": "error: ..."}}`
- [ ] `GET /health` > verify: still returns 200 (liveness != readiness)
- [ ] Restart postgres

### 29.4 Scheduler Stalled
- [ ] Wait for 2x scheduler interval with scheduler stopped
- [ ] `GET /health/ready` > verify: scheduler check shows "degraded" or "no run within expected interval"

---

## FLOW 30: Auto-Charge Retry

### 30.1 Setup
- [ ] Create customer with a card that declines on charge (4000 0000 0000 0341)
- [ ] Create subscription, ingest usage, run billing

### 30.2 Verify Pending Flag
```sql
SELECT id, auto_charge_pending, payment_status FROM invoices ORDER BY created_at DESC LIMIT 1;
```
- [ ] Verify: `auto_charge_pending = true`, `payment_status = 'pending'`

### 30.3 Retry on Next Scheduler Tick
- [ ] Update customer's card to a working one (via Stripe dashboard or checkout)
- [ ] Wait for next scheduler tick (or trigger manually)
- [ ] Verify: `auto_charge_pending` cleared to `false` after successful charge
- [ ] Verify: invoice `payment_status = 'succeeded'`

---

## FLOW 31: Invoice Idempotency

### 31.1 Double Billing Prevention
- [ ] Run billing for a subscription
- [ ] Note the billing period (start/end) on the generated invoice
- [ ] Run billing again immediately (same period)
- [ ] Verify: NO duplicate invoice created
- [ ] Verify: logs show "invoice already exists for billing period (idempotent skip)"

### 31.2 Verify Unique Constraint
```sql
SELECT COUNT(*) FROM invoices
WHERE subscription_id = '{sub_id}'
  AND billing_period_start = '{start}'
  AND billing_period_end = '{end}'
  AND status != 'voided';
```
- [ ] Verify: exactly 1 row

---

## FLOW 32: Customer Price Overrides

### 32.1 Create Override
- [ ] `POST /v1/price-overrides` with customer_id, rating_rule_id, custom flat_amount_cents
- [ ] Verify: override saved

### 32.2 Billing with Override
- [ ] Ingest usage for the customer with the override
- [ ] Run billing
- [ ] Verify: invoice uses the override price, NOT the default rule price
- [ ] Verify: line item shows custom amount

### 32.3 Second Customer (No Override)
- [ ] Ingest same usage for a different customer
- [ ] Run billing
- [ ] Verify: invoice uses default rule price

---

## FLOW 33: Customer Portal API

- [ ] `GET /v1/customer-portal/{customer_id}/overview`
- [ ] Verify: returns active subscriptions, recent invoices, credit balance
- [ ] `GET /v1/customer-portal/{customer_id}/subscriptions`
- [ ] Verify: returns customer's subscriptions only
- [ ] `GET /v1/customer-portal/{customer_id}/invoices`
- [ ] Verify: returns customer's invoices only

---

## FLOW 34: Invoice Collect + Payment Timeline

### 34.1 Manual Collect
- [ ] Create a finalized unpaid invoice for a customer with a payment method
- [ ] `POST /v1/invoices/{id}/collect`
- [ ] Verify: Stripe PaymentIntent created, invoice payment_status updates

### 34.2 Payment Timeline
- [ ] `GET /v1/invoices/{id}/payment-timeline`
- [ ] Verify: shows all payment attempts with timestamps, amounts, status, Stripe PI IDs
- [ ] For a failed-then-succeeded invoice: verify both attempts shown in order

---

## FLOW 35: Usage Summary

- [ ] Ingest events across multiple meters for a customer
- [ ] `GET /v1/usage-summary/{customer_id}?from=2026-04-01&to=2026-04-30`
- [ ] Verify: aggregated totals per meter for the period
- [ ] Verify: quantities match what was ingested

---

## FLOW 36: Migration Management (CLI)

### 36.1 Status
```bash
go run ./cmd/velox migrate status
```
- [ ] Verify: lists all migrations with applied/pending status and timestamps

### 36.2 Dry Run
```bash
go run ./cmd/velox migrate dry-run
```
- [ ] Verify: shows pending migrations without applying them
- [ ] Verify: database state unchanged

### 36.3 Rollback (Staging Only)
```bash
go run ./cmd/velox migrate rollback 0020_feature_flags
```
- [ ] Verify: feature_flags tables dropped
- [ ] Verify: migration removed from schema_migrations
- [ ] Re-apply: `go run ./cmd/velox migrate` > verify: tables recreated

---

## FLOW 37: OpenTelemetry Tracing

### 37.1 Enable Tracing
```bash
docker run -d --name jaeger -p 16686:16686 -p 4318:4318 jaegertracing/jaeger:2
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 go run ./cmd/velox
```

### 37.2 Verify Traces
- [ ] Make several API requests (create customer, ingest usage, run billing)
- [ ] Open Jaeger UI at `http://localhost:16686`
- [ ] Verify: traces appear for service "velox"
- [ ] Verify: HTTP spans show method + path (e.g., `POST /v1/customers`)
- [ ] Verify: billing.RunCycle span with `batch_size` attribute
- [ ] Verify: billing.BillSubscription spans with `subscription_id`, `tenant_id` attributes
- [ ] Verify: trace context propagated (parent-child relationship between HTTP → billing spans)

---

## FLOW 38: Config Validation

### 38.1 Missing Stripe Key
```bash
unset STRIPE_SECRET_KEY
go run ./cmd/velox
```
- [ ] Verify: stderr shows "config warning: STRIPE_SECRET_KEY is not set — payment processing will fail"
- [ ] Verify: server still starts (warnings, not errors)

### 38.2 Invalid Stripe Key Prefix
```bash
STRIPE_SECRET_KEY=bad_key_123 go run ./cmd/velox
```
- [ ] Verify: warning about key not starting with `sk_`

### 38.3 Production Without Redis
```bash
APP_ENV=production unset REDIS_URL go run ./cmd/velox
```
- [ ] Verify: warning about rate limiting failing open

### 38.4 Invalid Encryption Key
```bash
VELOX_ENCRYPTION_KEY=tooshort go run ./cmd/velox
```
- [ ] Verify: warning about encryption key length

---

## FLOW 39: Basis-Point Tax Precision

### 39.1 Fractional Tax Rate
- [ ] Settings: set tax rate to 7.25% (725 basis points)
- [ ] Run billing for a $100 subtotal
- [ ] Verify: tax = $7.25 exactly (725 cents)
- [ ] Verify: `tax_rate_bp = 725` in the invoice record

### 39.2 Per-Line Rounding
- [ ] Run billing with 3 line items: $33.33, $33.33, $33.34
- [ ] Verify: per-line taxes sum exactly to invoice total tax
- [ ] Verify: no off-by-one-cent discrepancy

---

## FLOW 40: Idempotency Key Behavior

### 40.1 Duplicate Prevention
- [ ] `POST /v1/customers` with `Idempotency-Key: test-123` header
- [ ] Repeat exact same request with same `Idempotency-Key: test-123`
- [ ] Verify: second response is identical to first (cached)
- [ ] Verify: only one customer actually created

### 40.2 Different Key = Different Request
- [ ] `POST /v1/customers` with `Idempotency-Key: test-456` and different body
- [ ] Verify: new customer created (different idempotency key)

---

## Test Results

| # | Flow | Status | Notes |
|---|------|--------|-------|
| 1 | Happy Path E2E | | |
| 2 | Credits (Grant, Apply, Expire) | | |
| 3 | Credit Notes | | |
| 4 | Void Invoice | | |
| 5 | Dunning | | |
| 6 | Plan Change + Proration | | |
| 7 | Usage Caps | | |
| 8 | Coupons + Plan Restrictions | | |
| 9 | Negative Usage | | |
| 10 | Manual Line Items | | |
| 11 | Webhook Secret Rotation | | |
| 12 | Invoice Email + PDF Preview | | |
| 12.5 | Self-Service Payment Update (Token) | | |
| 13 | Subscription Lifecycle | | |
| 14 | Audit Log | | |
| 15 | Multiple Meters | | |
| 16 | Usage Events Page | | |
| 17 | Dashboard | | |
| 18 | Webhook Delivery Stats | | |
| 19 | API Key Permissions | | |
| 20 | Settings + Billing Profile | | |
| 21 | Edge Cases | | |
| 22 | Responsive / Mobile | | |
| 23 | GDPR Export + Deletion | | |
| 24 | Feature Flags | | |
| 25 | Stripe Tax Integration | | |
| 26 | PII Encryption at Rest | | |
| 27 | Rate Limiting (Redis) | | |
| 28 | Security Headers + Metrics Auth | | |
| 29 | Health Checks | | |
| 30 | Auto-Charge Retry | | |
| 31 | Invoice Idempotency | | |
| 32 | Customer Price Overrides | | |
| 33 | Customer Portal API | | |
| 34 | Invoice Collect + Payment Timeline | | |
| 35 | Usage Summary | | |
| 36 | Migration Management (CLI) | | |
| 37 | OpenTelemetry Tracing | | |
| 38 | Config Validation | | |
| 39 | Basis-Point Tax Precision | | |
| 40 | Idempotency Key Behavior | | |

| # | Flow | Status | Notes |
|---|------|--------|-------|
| 1 | Happy Path E2E | | |
| 2 | Credits (Grant, Apply, Expire) | | |
| 3 | Credit Notes | | |
| 4 | Void Invoice | | |
| 5 | Dunning | | |
| 6 | Plan Change + Proration | | |
| 7 | Usage Caps | | |
| 8 | Coupons + Plan Restrictions | | |
| 9 | Negative Usage | | |
| 10 | Manual Line Items | | |
| 11 | Webhook Secret Rotation | | |
| 12 | Invoice Email + PDF Preview | | |
| 12.5 | Self-Service Payment Update (Token) | | |
| 13 | Subscription Lifecycle | | |
| 14 | Audit Log | | |
| 15 | Multiple Meters | | |
| 16 | Usage Events Page | | |
| 17 | Dashboard | | |
| 18 | Webhook Delivery Stats | | |
| 19 | API Key Permissions | | |
| 20 | Settings + Billing Profile | | |
| 21 | Edge Cases | | |
| 22 | Responsive / Mobile | | |
,main