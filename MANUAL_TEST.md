# Velox Manual E2E Test Plan

## Prerequisites
- Server running: `DATABASE_URL="..." go run ./cmd/velox`
- Frontend running: `cd web && npm run dev`
- Bootstrap done: `POST /v1/bootstrap`
- Logged in with platform API key

---

## 1. Tenant Onboarding
- [ ] Open app, see login page
- [ ] Enter platform API key, click Login
- [ ] Dashboard loads with 0 customers, 0 subscriptions, 0 invoices
- [ ] "Get Started" section visible with 3 steps

## 2. Settings
- [ ] Go to Settings
- [ ] Set company name, email, phone, address
- [ ] Set invoice prefix (e.g. "ACME"), currency, timezone, net terms
- [ ] Click Save, see "Settings saved" confirmation
- [ ] Refresh page, verify values persisted
- [ ] **Validation**: Enter single-digit phone (e.g. "5"), tab out — see "Invalid phone number" inline error
- [ ] **Validation**: Enter bad email (e.g. "abc"), tab out — see "Invalid email address" inline error
- [ ] **Validation**: Submit with bad phone — form blocked, field focused

## 3. Pricing Setup
- [ ] Go to Pricing page
- [ ] Create a Meter (key: `api_calls`, name: "API Calls", aggregation: sum)
- [ ] Create a Rating Rule (key: `api_calls`, mode: flat, amount: $0.01)
- [ ] Create a Plan (code: `pro`, name: "Pro Plan", interval: monthly, base: $49.00)
- [ ] Verify all 3 appear in their tables
- [ ] **Validation**: Try creating plan with empty name — see inline error

## 4. Customer Creation
- [ ] Go to Customers
- [ ] Click "Add Customer"
- [ ] **Validation**: Submit empty form — see "Display name is required", "External ID is required"
- [ ] **Validation**: Enter external ID with spaces — see "Only letters, numbers, hyphens, and underscores"
- [ ] **Validation**: Enter bad email — see "Invalid email address"
- [ ] Fill valid data: name "Acme Corp", external_id "acme_corp", email "billing@acme.com"
- [ ] Click Create, see success toast
- [ ] Verify customer appears in list

## 5. Billing Profile
- [ ] Click into customer detail
- [ ] Click "Edit" on billing profile section
- [ ] Fill legal name, email, phone, address, country, currency, tax ID
- [ ] **Validation**: Enter phone "123" — see "Invalid phone number" on blur
- [ ] Save with valid data, see "Billing profile saved" toast
- [ ] Verify data persisted on page refresh

## 6. Subscription Creation
- [ ] Go to Subscriptions, click "Add Subscription"
- [ ] **Validation**: Submit empty — see errors on display name, code, customer, plan
- [ ] Fill: name "Acme Pro", code "acme-pro", select customer, select plan
- [ ] Check "Start immediately"
- [ ] Click Create, see success toast
- [ ] Verify subscription shows as "active" with billing period set

## 7. Usage Ingestion (via API)
```bash
# Single event (get customer_id and meter_id from the UI)
curl -X POST http://localhost:8080/v1/usage-events \
  -H "Authorization: Bearer <secret_key>" \
  -H "Content-Type: application/json" \
  -d '{
    "customer_id": "<vlx_cus_...>",
    "meter_id": "<vlx_mtr_...>",
    "quantity": 1500
  }'

# Batch events
curl -X POST http://localhost:8080/v1/usage-events/batch \
  -H "Authorization: Bearer <secret_key>" \
  -H "Content-Type: application/json" \
  -d '[
    {"customer_id": "<vlx_cus_...>", "meter_id": "<vlx_mtr_...>", "quantity": 1500},
    {"customer_id": "<vlx_cus_...>", "meter_id": "<vlx_mtr_...>", "quantity": 2500}
  ]'
```
- [ ] Go to Usage Events page, verify events appear
- [ ] Go to Customer Detail, verify usage summary shows

## 8. Invoice Generation
- [ ] Go to Dashboard, click "Generate Invoices"
- [ ] See "Generated 1 invoice(s)" result
- [ ] Go to Invoices page, verify invoice appears as "draft"
- [ ] Click into invoice detail
- [ ] Verify line items: base plan ($49.00) + usage charges (4000 x $0.01 = $40.00)
- [ ] Verify customer info displays correctly

## 9. Invoice Actions
- [ ] Click "Finalize" — status changes to "finalized"
- [ ] Click "Download PDF" — PDF downloads with correct line items (no gibberish chars)
- [ ] Click "Email Invoice" — enter email, send
- [ ] **Validation**: Enter invalid email — see inline error
- [ ] Click "Void" — confirm dialog with danger icon — invoice voided

## 10. Credit Notes
- [ ] Go to Credit Notes
- [ ] Click "Create Credit Note"
- [ ] **Validation**: Submit empty — see "Invoice is required", "Reason is required"
- [ ] Select a finalized invoice, enter reason, set amount
- [ ] Create credit note (draft)
- [ ] Click "Issue" — confirm dialog — credit note issued
- [ ] Click "Void" on another — confirm dialog (danger) — voided

## 11. Credits
- [ ] Go to Credits
- [ ] Select a customer from dropdown
- [ ] Click "Grant Credits"
- [ ] **Validation**: Submit with empty amount — see "Amount is required"
- [ ] **Validation**: Enter 0 — see "Must be at least 0.01"
- [ ] Enter $25.00, description "Welcome credit"
- [ ] Confirm grant in confirmation dialog
- [ ] Verify balance updates, ledger shows grant entry

## 12. Quick Refund (from Invoice Detail)
- [ ] Go to a paid invoice
- [ ] Click "Issue Credit / Refund"
- [ ] **Validation**: Empty reason — see inline error
- [ ] Enter reason and amount, select "Credit" type
- [ ] Create — credit note created via toast

## 13. Dunning
- [ ] Go to Dunning > Policy tab
- [ ] Set name, enable toggle, max retries: 3, grace period: 5, final action: manual_review
- [ ] Save — see "Dunning policy saved" toast
- [ ] Toggle enabled/disabled, verify unsaved indicator
- [ ] Go to Runs tab — see "No dunning runs" empty state
- [ ] (Runs appear when Stripe payment fails — test with Stripe test mode)

## 14. API Keys
- [ ] Go to API Keys
- [ ] Click "Create API Key"
- [ ] **Validation**: Submit with empty name — see "Name is required"
- [ ] Enter name, select type (secret), create
- [ ] See created key modal — copy key, click Done
- [ ] Verify key appears in list
- [ ] Click "Revoke" on a key — see danger confirmation dialog with warning
- [ ] Confirm revoke — key status changes

## 15. Webhooks
- [ ] Go to Webhooks > Endpoints tab
- [ ] Click "Add Endpoint"
- [ ] **Validation**: Enter invalid URL — see "Must be a valid URL"
- [ ] Enter valid HTTPS URL, add events via chip buttons
- [ ] Create — see signing secret modal — copy, Done
- [ ] Verify endpoint appears in list
- [ ] Go to Events tab — see event delivery history

## 16. Audit Log
- [ ] Go to Audit Log
- [ ] Verify all actions from above are logged
- [ ] Each entry has: action, resource type, resource ID, timestamp
- [ ] Click resource IDs — they should be copyable

## 17. Dashboard Verification
- [ ] Go to Dashboard
- [ ] Verify stat cards: Customers > 0, Subscriptions > 0, Invoices > 0, Revenue, MRR
- [ ] "Needs Attention" — shows draft/failed/pending invoices if any, or "No pending issues"
- [ ] "Active Subscriptions" — shows subs with customer name
- [ ] "Recent Activity" — shows typed icons per action
- [ ] All cards scroll-contained (no page overflow)

## 18. Modal/Dialog Quality Check
- [ ] Open any create modal — header has border separator, close button has hover bg
- [ ] Cancel button is bordered (not bare text)
- [ ] Footer has border-t separator
- [ ] Confirm dialogs have contextual icon (red triangle for danger, blue info for default)
- [ ] No browser-native validation popups anywhere (no yellow tooltips)
- [ ] Inline errors show red border + red text below field on blur
- [ ] Submit with errors focuses first error field

## 19. Responsive & Edge Cases
- [ ] All tables horizontally scrollable on narrow screens
- [ ] Empty states show on all list pages when no data
- [ ] Error states show retry button
- [ ] Pagination works on Customers, Invoices, Subscriptions, Audit Log, Usage Events
- [ ] CSV export works on Customers and Invoices pages
- [ ] Search/filter works on Customers and Subscriptions

---

## Test Results

| # | Flow | Status | Notes |
|---|------|--------|-------|
| 1 | Tenant Onboarding | | |
| 2 | Settings | | |
| 3 | Pricing Setup | | |
| 4 | Customer Creation | | |
| 5 | Billing Profile | | |
| 6 | Subscription Creation | | |
| 7 | Usage Ingestion | | |
| 8 | Invoice Generation | | |
| 9 | Invoice Actions | | |
| 10 | Credit Notes | | |
| 11 | Credits | | |
| 12 | Quick Refund | | |
| 13 | Dunning | | |
| 14 | API Keys | | |
| 15 | Webhooks | | |
| 16 | Audit Log | | |
| 17 | Dashboard | | |
| 18 | Modal/Dialog Quality | | |
| 19 | Responsive & Edge Cases | | |
