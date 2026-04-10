# Velox Manual E2E Test Plan — Edge Cases & User Flows

## Prerequisites
- `cp .env.example .env` — fill in Stripe test keys
- `make up` — start Postgres
- `make dev` — start server (auto-migrates)
- `cd web && npm run dev` — start frontend
- `stripe listen --forward-to localhost:8080/v1/webhooks/stripe` — webhook forwarding
- Bootstrap: `POST /v1/bootstrap` or `make bootstrap`

---

## FLOW 1: Complete Happy Path (End-to-End)

### 1.1 Setup
- [ ] Configure Settings: company name "Demo Corp", prefix "DEMO", net terms 15, USD, UTC
- [ ] Create Rating Rule: key `api_calls`, flat, $0.01/call
- [ ] Create Meter: key `api_calls`, aggregation sum, link rating rule
- [ ] Create Plan: code `starter`, $29/mo, attach api_calls meter
- [ ] Create Customer: "Alpha Inc", external_id `alpha_inc`
- [ ] Set up billing profile with US address
- [ ] Set up payment method (card 4242 4242 4242 4242)
- [ ] Create subscription: start immediately

### 1.2 Usage + Billing
- [ ] Ingest 10,000 API calls via API (`external_customer_id: alpha_inc, event_name: api_calls`)
- [ ] Generate invoices
- [ ] Verify: invoice auto-finalized, auto-charged, status "paid" via webhook
- [ ] Verify: invoice = $29 base + $100 usage (10,000 × $0.01) = $129
- [ ] Verify: PDF shows FROM (Demo Corp) + BILL TO (Alpha Inc) + correct amounts
- [ ] Verify: invoice number uses "DEMO" prefix
- [ ] Verify: due date = issued + 15 days (from settings)

### 1.3 Verify Billing Cycle Advanced
- [ ] Subscription detail: billing period moved to next month
- [ ] Usage This Period: shows $0 (new period, no events yet)

---

## FLOW 2: Credits Applied Before Charge

### 2.1 Grant Credits
- [ ] Go to Credits, select Alpha Inc
- [ ] Grant $50 credit with description "Promotional credit"
- [ ] Verify: balance = $50

### 2.2 Generate Invoice with Credits
- [ ] Ingest 5,000 more API calls
- [ ] Fast-forward billing period (DB fixture)
- [ ] Generate invoices
- [ ] Verify: invoice subtotal = $29 + $50 = $79
- [ ] Verify: prepaid credits applied = -$50
- [ ] Verify: amount_due = $29
- [ ] Verify: Stripe charged only $29 (not $79)
- [ ] Verify: Credits page shows "Applied to invoice DEMO-000002" with -$50
- [ ] Verify: credits balance = $0

---

## FLOW 3: Credit Notes (Paid vs Unpaid)

### 3.1 Credit Note on Unpaid Invoice
- [ ] Go to a finalized (unpaid) invoice
- [ ] Click "Issue Credit" — enter $20, reason "Billing error", type "Credit"
- [ ] Create credit note (draft)
- [ ] Go to Credit Notes — click "Issue"
- [ ] Verify: invoice amount_due reduced by $20
- [ ] Verify: invoice detail shows credit note in totals breakdown
- [ ] Verify: credits balance did NOT change (only amount_due reduced)

### 3.2 Credit Note on Paid Invoice (Credit type)
- [ ] Go to a paid invoice
- [ ] Click "Issue Credit" — enter $15, reason "Service disruption", type "Credit"
- [ ] Create + Issue the credit note
- [ ] Verify: invoice amount_due stays $0 (already paid)
- [ ] Verify: customer credit balance INCREASED by $15 (for next invoice)
- [ ] Verify: Credits page shows "Credit note CN-XXXX — Service disruption" +$15

### 3.3 Credit Note on Paid Invoice (Refund type)
- [ ] Go to a paid invoice
- [ ] Click "Issue Credit" — enter $10, reason "Overcharge", type "Refund"
- [ ] Create + Issue the credit note
- [ ] Verify: Stripe refund processed (check Stripe CLI for refund event)
- [ ] Verify: credits balance did NOT change (refund goes to payment method, not balance)
- [ ] Verify: PDF shows credit note in totals

---

## FLOW 4: Void Invoice — Credits Reversed

### 4.1 Setup
- [ ] Grant $30 credits to Alpha Inc
- [ ] Generate invoice — credits applied, amount_due reduced

### 4.2 Void
- [ ] Void the invoice
- [ ] Verify: credits reversed — balance restored to $30
- [ ] Verify: Credits page shows "Reversed — invoice DEMO-XXXX voided"
- [ ] Verify: Stripe PaymentIntent canceled (if was charged)
- [ ] Verify: invoice status = voided

---

## FLOW 5: Payment Failure → Dunning → Resolution

### 5.1 Setup Bad Card Customer
- [ ] Create customer "Bad Pay Corp", external_id `bad_pay`
- [ ] Set up billing profile with address
- [ ] Set up payment with card `4000 0000 0000 0341` (declines on charge)
- [ ] Create subscription, ingest usage

### 5.2 Trigger Failure
- [ ] Fast-forward billing period, generate invoice
- [ ] Verify: invoice finalized, payment_status = failed
- [ ] Verify: Dunning > Runs shows new run, state "scheduled", "0 of 3"

### 5.3 Retry Cycle
- [ ] Fast-forward next_action_at in DB
- [ ] Wait for scheduler tick (5 min)
- [ ] Verify: retries = "1 of 3", next retry scheduled
- [ ] Repeat 2 more times
- [ ] After 3rd retry: state = "escalated"

### 5.4 Resolution
- [ ] Click "Resolve" on escalated run
- [ ] Select "Invoice Not Collectible"
- [ ] Verify: run state = resolved
- [ ] Verify: invoice status = voided
- [ ] Try another: resolve as "Payment Succeeded"
- [ ] Verify: invoice status = paid

---

## FLOW 6: Price Versioning

### 6.1 Initial Price
- [ ] Verify current rule: api_calls v1 at $0.01/call
- [ ] Generate invoice — usage billed at $0.01

### 6.2 Create New Version
- [ ] Create new rating rule: same key `api_calls`, flat, $0.02/call (creates v2)
- [ ] Verify: Meter detail page shows v2 pricing ($0.02), not v1
- [ ] No plan or meter changes needed

### 6.3 Bill at New Price
- [ ] Ingest more usage, fast-forward billing period
- [ ] Generate invoice
- [ ] Verify: usage billed at $0.02/call (v2), not $0.01 (v1)
- [ ] Verify: invoice line item references v2 rule

---

## FLOW 7: Multiple Meters on One Plan

### 7.1 Setup
- [ ] Create second rating rule: key `storage_gb`, flat, $0.10/GB
- [ ] Create second meter: key `storage_gb`, aggregation max, link rule
- [ ] Go to Plan detail — click "+ Attach Meter" — attach storage_gb
- [ ] Plan now has 2 meters

### 7.2 Ingest Both
```bash
# API calls
curl -X POST http://localhost:8080/v1/usage-events \
  -H "Authorization: Bearer <key>" \
  -d '{"external_customer_id":"alpha_inc","event_name":"api_calls","quantity":2000}'

# Storage
curl -X POST http://localhost:8080/v1/usage-events \
  -H "Authorization: Bearer <key>" \
  -d '{"external_customer_id":"alpha_inc","event_name":"storage_gb","quantity":50}'
```

### 7.3 Verify Invoice
- [ ] Generate invoice
- [ ] Verify 3 line items: base fee + API calls + storage
- [ ] API: 2,000 × $0.02 = $40
- [ ] Storage: 50 × $0.10 = $5
- [ ] Total: $29 + $40 + $5 = $74

---

## FLOW 8: Subscription Lifecycle

### 8.1 Pause
- [ ] Go to active subscription — click "Pause"
- [ ] Verify: status = paused
- [ ] Verify: billing cycle does NOT generate invoice for paused sub

### 8.2 Resume
- [ ] Click "Resume"
- [ ] Verify: status = active
- [ ] Verify: billing resumes

### 8.3 Change Plan
- [ ] Create a second plan: "Enterprise", $99/mo
- [ ] Go to subscription — click "Change Plan"
- [ ] Select Enterprise plan
- [ ] Verify: next invoice uses new plan pricing

### 8.4 Cancel
- [ ] Click "Cancel"
- [ ] Verify: status = canceled
- [ ] Verify: no more invoices generated

---

## FLOW 9: Multiple Customers, Same Plan

- [ ] Create 3 customers with payment methods
- [ ] Create subscription for each on same plan
- [ ] Ingest different usage amounts for each
- [ ] Generate invoices — verify each gets correct amount
- [ ] Verify: billing engine processes all 3 in one cycle
- [ ] Verify: no double-billing (FOR UPDATE SKIP LOCKED)

---

## FLOW 10: Edge Cases

### 10.1 Zero Usage
- [ ] Customer with subscription but no usage events
- [ ] Generate invoice — should have base fee only, no usage lines
- [ ] Verify: $29 invoice (base only)

### 10.2 Meter Without Rating Rule
- [ ] Create meter without linking a rating rule
- [ ] Attach to plan
- [ ] Ingest usage
- [ ] Generate invoice — meter usage should be silently skipped
- [ ] Verify: invoice has base fee only

### 10.3 Duplicate Usage Events
- [ ] Ingest event with idempotency_key "test-1"
- [ ] Ingest same event again with same idempotency_key "test-1"
- [ ] Verify: second call returns conflict error
- [ ] Verify: only 1 event in usage list

### 10.4 Invalid External Customer ID
- [ ] Ingest event with non-existent external_customer_id
- [ ] Verify: error "customer not found"

### 10.5 Invalid Event Name
- [ ] Ingest event with non-existent event_name
- [ ] Verify: error "meter not found"

### 10.6 Credits > Invoice Amount
- [ ] Grant $500 credits
- [ ] Generate invoice for $79
- [ ] Verify: credits applied = $79 (not $500)
- [ ] Verify: amount_due = $0
- [ ] Verify: remaining balance = $421
- [ ] Verify: Stripe NOT charged (amount_due = 0)

### 10.7 Credit Note > Amount Due
- [ ] Issue credit note for more than amount_due
- [ ] Verify: amount_due goes to $0 (not negative)

### 10.8 Void Already Voided Invoice
- [ ] Try to void a voided invoice
- [ ] Verify: error message

### 10.9 Finalize Non-Draft Invoice
- [ ] Try to finalize a paid invoice
- [ ] Verify: error message (invoices are auto-finalized now, so test via API)

### 10.10 Create Duplicate Subscription Code
- [ ] Create subscription with code "alpha-pro"
- [ ] Create another with same code "alpha-pro"
- [ ] Verify: humanized error message about duplicate

---

## FLOW 11: API Key Permissions

### 11.1 Secret Key
- [ ] Use secret key — can access all endpoints
- [ ] Can create customers, subscriptions, ingest usage

### 11.2 Publishable Key
- [ ] Create a publishable key
- [ ] Try to create a plan with publishable key
- [ ] Verify: 403 forbidden (publishable can't manage pricing)
- [ ] Try to read customers with publishable key
- [ ] Verify: works (publishable can read customers)

### 11.3 Revoked Key
- [ ] Revoke a key
- [ ] Try to use revoked key
- [ ] Verify: 401 unauthorized

---

## FLOW 12: Webhook Reliability

### 12.1 Payment Success Webhook
- [ ] Finalize invoice with good card
- [ ] Verify: payment_intent.succeeded webhook received
- [ ] Verify: invoice status = paid

### 12.2 Payment Failed Webhook
- [ ] Charge bad card customer
- [ ] Verify: payment_intent.payment_failed webhook received
- [ ] Verify: dunning started

### 12.3 Duplicate Webhook
- [ ] Same webhook event delivered twice (Stripe retries)
- [ ] Verify: second delivery is idempotent (no duplicate processing)

---

## Test Results

| # | Flow | Status | Notes |
|---|------|--------|-------|
| 1 | Happy Path E2E | | |
| 2 | Credits Applied | | |
| 3 | Credit Note | | |
| 4 | Void + Credit Reversal | | |
| 5 | Dunning | | |
| 6 | Price Versioning | | |
| 7 | Multiple Meters | | |
| 8 | Subscription Lifecycle | | |
| 9 | Multiple Customers | | |
| 10 | Edge Cases | | |
| 11 | API Key Permissions | | |
| 12 | Webhook Reliability | | |
