# Velox Manual E2E Test Plan

## Prerequisites
- `cp .env.example .env` — fill in Stripe test keys
- `make up` — start Postgres
- `make dev` — start server (auto-migrates)
- `cd web && npm run dev` — start frontend
- `stripe listen --forward-to localhost:8080/v1/webhooks/stripe` — webhook forwarding
- Bootstrap: `POST /v1/bootstrap` or `make bootstrap`

---

## 1. Tenant Onboarding
- [ ] Open http://localhost:3000 — see login page
- [ ] Enter API key from bootstrap, click Login
- [ ] Dashboard loads with 0 customers, 0 subs, 0 invoices
- [ ] "Get Started" section visible

## 2. Settings
- [ ] Go to Settings
- [ ] Set company name, email, phone, address
- [ ] Set invoice prefix (e.g. "ACME"), net payment terms (e.g. 15), currency, timezone
- [ ] Save — "Settings saved" confirmation
- [ ] Refresh — values persisted
- [ ] **Validation**: bad email/phone — inline errors on blur
- [ ] Verify: hints explain what each setting controls

## 3. Pricing — Plans, Meters, Rating Rules
- [ ] Go to Pricing > Rules tab
- [ ] Create Rating Rule: key `api_calls`, name "API Call Pricing", mode Flat, $0.01/unit
- [ ] Status shows "active" (not draft)
- [ ] Go to Meters tab
- [ ] Create Meter: key `api_calls`, name "API Calls", unit "request", aggregation Sum
- [ ] **Select the rating rule** from dropdown (important — without this, usage won't be priced)
- [ ] Go to Plans tab
- [ ] Create Plan: code `pro`, name "Pro Plan", Monthly, $49.00, USD
- [ ] **Attach the meter** to the plan
- [ ] Click plan name — navigates to Plan detail
- [ ] Verify: meters section shows "API Calls" with "Detach" button
- [ ] Verify: full-row click works on all tabs

## 4. Customer Creation
- [ ] Go to Customers — empty state with single centered CTA
- [ ] Click "Add Customer"
- [ ] **Validation**: submit empty — inline errors
- [ ] **Validation**: bad email — error on blur
- [ ] Create: name "ACME CORP", external_id "acme_corp", email "billing@acme.com"
- [ ] Customer appears in list — full-row click to detail

## 5. Billing Profile
- [ ] Customer detail — "Set Up Billing Profile" CTA
- [ ] Fill: legal name, email, phone
- [ ] Select country "United States" — State becomes dropdown with full names ("California")
- [ ] Fill address, postal code, tax ID, currency
- [ ] Save — profile shows in 3-column grid on detail page
- [ ] Edit — "No changes" button when nothing changed

## 6. Payment Method Setup (Stripe)
- [ ] Customer detail — "Set Up Payment" button
- [ ] Opens Stripe Checkout in new tab
- [ ] Enter card `4242 4242 4242 4242`, expiry `12/30`, CVC `123`
- [ ] Complete — redirects back, payment status shows "ready"
- [ ] Check Stripe CLI — `checkout.session.completed` webhook received

## 7. Subscription Creation
- [ ] Customer detail — click "+ Add" in Subscriptions section
- [ ] Create: name, code, select Pro Plan, check "Start immediately"
- [ ] Subscription appears with "active" badge
- [ ] Click into subscription detail — Plan links to /plans/:id, Customer links to /customers/:id
- [ ] Verify: billing period set

## 8. Usage Ingestion
```bash
curl -X POST http://localhost:8080/v1/usage-events \
  -H "Authorization: Bearer <secret_key>" \
  -H "Content-Type: application/json" \
  -d '{
    "external_customer_id": "acme_corp",
    "event_name": "api_calls",
    "quantity": 5000
  }'
```
- [ ] Verify: Usage Events page shows the event with timestamp + time
- [ ] Verify: Customer detail "Usage This Period" shows meter name + unit + billing period dates

## 9. Invoice Generation + Auto-Charge
- [ ] Dashboard — click "Generate Invoices"
- [ ] Invoice created as **finalized** (not draft — auto-finalized)
- [ ] Credits auto-applied (if any) — amount_due reduced
- [ ] Auto-charge via Stripe — payment status goes to "processing" then "paid" (via webhook)
- [ ] Check Stripe CLI: `payment_intent.succeeded` received
- [ ] Invoice detail: Subtotal + Prepaid Credits (if any) + Amount Due breakdown
- [ ] Line items: base fee ($49) + usage (5,000 × $0.01 = $50) = $99 total

## 10. PDF Invoice
- [ ] Click "Download PDF"
- [ ] Verify: FROM (company from Settings) — name, address, email, phone
- [ ] Verify: BILL TO (customer billing profile) — name, full address, email
- [ ] Verify: invoice number from Settings prefix (e.g. ACME-000001)
- [ ] Verify: due date uses net payment terms from Settings
- [ ] Verify: line items with comma-formatted quantities
- [ ] Verify: Subtotal + Prepaid credits (if applied) + Amount Due
- [ ] Verify: "Paid on [date] - Thank you!" if paid

## 11. Credit Notes
- [ ] Go to finalized/paid invoice — click "Issue Credit"
- [ ] Enter reason, amount, type (Credit or Refund)
- [ ] Create — draft credit note appears on Credit Notes page
- [ ] Click "Issue" — credit note issued
- [ ] Invoice detail: amount_due reduced, credit note shown in totals breakdown
- [ ] PDF: credit note shown in totals

## 12. Credits
- [ ] Go to Credits — select customer
- [ ] Click "Grant Credits" — enter $25, description
- [ ] Confirm in dialog
- [ ] Balance shows $25, transaction history shows grant
- [ ] Generate next invoice — credits auto-applied, shown on invoice
- [ ] Credits page: "Applied to invoice ACME-000002" (human-readable, not raw ID)

## 13. Void Invoice
- [ ] Void a finalized invoice
- [ ] Stripe PaymentIntent canceled (if exists)
- [ ] Credits reversed — returned to customer balance
- [ ] Invoice status: voided

## 14. Dunning (Failed Payment Recovery)
- [ ] Go to Dunning > Policy — enable, set max retries 3, grace period 3 days
- [ ] Create second customer "Bad Card Corp" with billing profile + US address
- [ ] Set up payment with card `4000 0000 0000 0341` (saves but declines charges)
- [ ] Create subscription, ingest usage
- [ ] Generate invoice — auto-charge fails
- [ ] Dunning > Runs: run appears with state "scheduled", "0 of 3" retries
- [ ] Wait for scheduler (or fast-forward next_action_at in DB)
- [ ] After 3 retries: state "escalated"
- [ ] Click "Resolve" — select "Invoice Not Collectible"
- [ ] Invoice becomes voided
- [ ] Or: resolve as "Payment Succeeded" — invoice becomes paid

## 15. API Keys
- [ ] Create key — shown once, copy button
- [ ] Revoke — danger confirmation, self-revoke warning
- [ ] Key shows as revoked in list

## 16. Webhooks
- [ ] Add endpoint with URL validation
- [ ] Signing secret shown once
- [ ] Events tab shows delivery history

## 17. Audit Log
- [ ] Timeline feed grouped by day
- [ ] Human-readable descriptions ("Finalized invoice ACME-000001")
- [ ] Colored badges per action type
- [ ] Filters: resource type, action type

## 18. Dashboard
- [ ] Stat cards: Customers, Subscriptions, Invoices, Revenue (paid only), MRR
- [ ] "Needs Attention" — failed/pending invoices
- [ ] "Active Subscriptions" — with customer names
- [ ] "Recent Activity" — last 10 actions
- [ ] "Generate Invoices" button (solid primary)

## 19. UI/UX Quality
- [ ] **Badges**: visible with ring borders, rounded-md
- [ ] **Table headers**: bg-gray-50
- [ ] **Full-row click**: all entity tables navigate on click
- [ ] **Buttons**: solid=create, bordered=edit, bordered-red=danger
- [ ] **Edit buttons**: pencil icon
- [ ] **Modals**: header border, close button hover, footer border-t
- [ ] **No browser validation popups**: all forms use noValidate + inline errors
- [ ] **Save buttons**: disabled when no changes
- [ ] **Typography**: labels text-sm text-gray-500, values text-sm text-gray-900
- [ ] **Breadcrumbs**: text-gray-500 hover:text-gray-900
- [ ] **Empty states**: single CTA, no redundant buttons
- [ ] **Error messages**: humanized ("already exists" → friendly message)
- [ ] **Copy buttons**: on all detail page IDs

## 20. Multi-Instance Safety
- [ ] Billing: FOR UPDATE SKIP LOCKED on GetDueBilling
- [ ] Dunning: FOR UPDATE SKIP LOCKED on ListDueRuns
- [ ] No double-billing possible with multiple server instances

---

## Test Results

| # | Flow | Status | Notes |
|---|------|--------|-------|
| 1 | Tenant Onboarding | | |
| 2 | Settings | | |
| 3 | Pricing | | |
| 4 | Customer Creation | | |
| 5 | Billing Profile | | |
| 6 | Payment Setup (Stripe) | | |
| 7 | Subscription Creation | | |
| 8 | Usage Ingestion | | |
| 9 | Invoice + Auto-Charge | | |
| 10 | PDF Invoice | | |
| 11 | Credit Notes | | |
| 12 | Credits | | |
| 13 | Void Invoice | | |
| 14 | Dunning | | |
| 15 | API Keys | | |
| 16 | Webhooks | | |
| 17 | Audit Log | | |
| 18 | Dashboard | | |
| 19 | UI/UX Quality | | |
| 20 | Multi-Instance Safety | | |
