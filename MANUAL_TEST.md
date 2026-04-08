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
- [ ] **Validation**: Enter bad email (e.g. "abc"), tab out — see "Invalid email address"
- [ ] **Validation**: Submit with bad phone — form blocked, field focused

## 3. Pricing Setup
- [ ] Go to Pricing page
- [ ] Create a Meter (key: `api_calls`, name: "API Calls", aggregation: sum)
- [ ] Create a Rating Rule (key: `api_calls`, mode: flat, amount: $0.01)
- [ ] Create a Plan (code: `pro`, name: "Pro Plan", interval: monthly, base: $49.00, attach the meter)
- [ ] Verify all 3 appear in their tables with **active** status
- [ ] Click plan name — navigates to Plan detail page
- [ ] Click meter name — navigates to Meter detail page
- [ ] **Validation**: Try creating plan with empty name — see inline error
- [ ] **Validation**: Try duplicate plan code — see humanized error message

## 4. Plan Detail
- [ ] Verify breadcrumbs: Pricing > Pro Plan (Pricing link goes to /pricing)
- [ ] Key metrics row: Base Price, Interval, Currency, Active Subscriptions
- [ ] Properties card with all details + copy button on ID
- [ ] Meters section shows attached meter with aggregation + unit info
- [ ] Click "+ Attach Meter" — opens picker modal
- [ ] Click "Detach" on meter — confirmation dialog appears
- [ ] Edit button (pencil icon) — opens Edit Plan modal
- [ ] Edit modal: change name, verify "No changes" button when nothing changed
- [ ] Edit modal: change status to Archived, verify it saves

## 5. Meter Detail
- [ ] Verify breadcrumbs: Pricing > API Calls
- [ ] Properties card, linked rating rule with pricing display
- [ ] Plans table shows which plans use this meter
- [ ] Full-row click navigates to plan detail

## 6. Customer Creation
- [ ] Go to Customers — when empty, single centered "Add Customer" CTA (no redundant header button)
- [ ] Click "Add Customer"
- [ ] **Validation**: Submit empty form — inline errors on required fields
- [ ] **Validation**: Bad email — inline error on blur
- [ ] Fill valid data: name "Acme Corp", external_id "acme_corp", email "billing@acme.com"
- [ ] Click Create, see success toast
- [ ] Customer appears in list — full-row click navigates to detail
- [ ] When list has data, header shows "Add Customer" button + "Export CSV"

## 7. Customer Detail
- [ ] Header: name, ID with copy button, Edit button (pencil icon)
- [ ] Key metrics: Email, Credit Balance, Subscriptions count, Created
- [ ] Details card: External ID, Email, Status, Created, ID
- [ ] All text consistently `text-sm` (no faint/tiny labels)

## 8. Billing Profile
- [ ] Click "Set Up Billing Profile" CTA (centered, prominent)
- [ ] Modal: 3 sections separated by dividers (CONTACT / ADDRESS / TAX & BILLING)
- [ ] Section labels are uppercase/tracking-wider (visually distinct from field labels)
- [ ] Select country "United States" — State field becomes dropdown with full state names ("California" not "CA")
- [ ] Select country "Canada" — Province dropdown with full names ("British Columbia")
- [ ] Select country "India" — State dropdown with full names ("Maharashtra")
- [ ] Other countries — State is free text
- [ ] Tax ID placeholder adapts: US="EIN (e.g. 12-3456789)", UK="VAT number", India="GSTIN"
- [ ] Currency shows full names: "USD — US Dollar"
- [ ] **Validation**: Bad email/phone — inline errors on blur
- [ ] Save, verify data shows on detail page in grouped layout (Contact, Address, Tax)
- [ ] Edit again — "No changes" button disabled when nothing changed
- [ ] After profile exists, header shows "Edit" button (pencil icon) instead of "Set up" CTA

## 9. Subscription Creation
- [ ] From Customer detail, click "+ Add" (solid primary button in Subscriptions section)
- [ ] OR go to Subscriptions page, click "Add Subscription" (only shows when list has data)
- [ ] When list empty — single centered CTA, no header button
- [ ] Fill: name, code, select plan, check "Start immediately"
- [ ] Click Create, see success toast
- [ ] Subscription shows on Customer detail with status badge (all statuses shown, not just active)

## 10. Subscription Detail
- [ ] Header: name, ID with copy, code badge
- [ ] **Draft status**: "Activate" button visible (solid primary)
- [ ] Click Activate — subscription becomes active with billing period
- [ ] **Active status**: Change Plan, Pause, Cancel buttons visible
- [ ] Key metrics: Customer (clickable link), Plan (clickable link), Billing Period, Status
- [ ] Properties: Plan links to /plans/:id, Customer links to /customers/:id
- [ ] Invoice Preview: shows preview when active, friendly message when draft ("Activate the subscription...")
- [ ] No raw "HTTP 422" errors

## 11. Usage Ingestion (via API)
```bash
# Using external identifiers (industry standard)
curl -X POST http://localhost:8080/v1/usage-events \
  -H "Authorization: Bearer <secret_key>" \
  -H "Content-Type: application/json" \
  -d '{
    "external_customer_id": "acme_corp",
    "event_name": "api_calls",
    "quantity": 1500
  }'

# Using internal IDs (also supported)
curl -X POST http://localhost:8080/v1/usage-events \
  -H "Authorization: Bearer <secret_key>" \
  -H "Content-Type: application/json" \
  -d '{
    "customer_id": "<vlx_cus_...>",
    "meter_id": "<vlx_mtr_...>",
    "quantity": 2500
  }'

# Batch events
curl -X POST http://localhost:8080/v1/usage-events/batch \
  -H "Authorization: Bearer <secret_key>" \
  -H "Content-Type: application/json" \
  -d '[
    {"external_customer_id": "acme_corp", "event_name": "api_calls", "quantity": 1500},
    {"external_customer_id": "acme_corp", "event_name": "api_calls", "quantity": 2500}
  ]'
```
- [ ] Verify events appear on Usage Events page
- [ ] Verify Customer Detail shows usage summary
- [ ] Quantity defaults to 1 when omitted (count-based meters)

## 12. Invoice Generation
- [ ] Go to Dashboard, click "Generate Invoices" (solid primary button)
- [ ] See "Generated N invoice(s)" result
- [ ] Go to Invoices page, verify invoice appears as "draft"
- [ ] Full-row click navigates to invoice detail
- [ ] Verify line items: base plan ($49.00) + usage charges
- [ ] Verify customer name (not raw ID) in properties

## 13. Invoice Actions
- [ ] "Finalize" button (solid primary) — status changes to "finalized"
- [ ] "Download PDF" (solid primary) — PDF downloads correctly
- [ ] "Email" (bordered) — enter email, send
- [ ] "Issue Credit" (bordered) — opens credit note modal
- [ ] "Void" (bordered red) — confirm dialog with danger icon — invoice voided
- [ ] **Button hierarchy**: Finalize/Download = solid, Email/Issue Credit = bordered, Void = bordered red

## 14. Credit Notes
- [ ] Go to Credit Notes, click "Create Credit Note"
- [ ] Wide modal with Reason + Type side by side
- [ ] Type explains itself: "Credit — apply to balance", "Refund — return to payment method"
- [ ] Line Item section with divider
- [ ] Description hint: "Defaults to reason if left blank"
- [ ] Create credit note (draft), "Issue" button in table, "Void" button (danger)

## 15. Credits
- [ ] Select a customer, click "Grant Credits"
- [ ] **Validation**: Empty amount, amount = 0
- [ ] Confirm grant in confirmation dialog
- [ ] Verify balance updates, ledger shows entry

## 16. Dunning
- [ ] Go to Dunning > Policy tab — shows form (no "Something went wrong" error)
- [ ] Save policy, toggle enabled/disabled
- [ ] Runs tab — empty state or dunning runs with "Resolve" button

## 17. API Keys
- [ ] Create key, see created key modal with copy button
- [ ] Revoke — danger confirmation dialog
- [ ] Key shows as revoked in list

## 18. Webhooks
- [ ] Add endpoint with URL validation
- [ ] See signing secret modal
- [ ] Events tab shows delivery history

## 19. Audit Log
- [ ] All actions logged with correct labels
- [ ] Resource IDs copyable

## 20. Dashboard
- [ ] Stat cards: Customers, Subscriptions, Invoices, Revenue, MRR
- [ ] "Needs Attention" — shows actionable items or "No pending issues"
- [ ] "Active Subscriptions" — scroll contained
- [ ] "Recent Activity" — typed icons per action
- [ ] "View all" links are `text-sm text-gray-500` (visible but not loud)
- [ ] "Generate Invoices" is solid primary button

## 21. UI/UX Quality Check
- [ ] **Badges**: visible with stronger backgrounds, ring borders, rounded-md (not pills)
- [ ] **Table headers**: bg-gray-50 background on all tables
- [ ] **Full-row click**: click anywhere on row navigates (not just name link)
- [ ] **Buttons**: solid = create/add, bordered = edit/change, bordered red = danger
- [ ] **Edit buttons**: pencil icon, hover:border-gray-400 darkening
- [ ] **Modals**: header with border, close button with hover bg, footer with border-t
- [ ] **Cancel buttons**: bordered (not bare text)
- [ ] **Confirm dialogs**: contextual icon (red triangle for danger, blue info for default)
- [ ] **No browser validation popups**: all forms use noValidate + inline errors
- [ ] **Inline errors**: red border + text on blur, focus first error on submit
- [ ] **Empty states**: single centered CTA when list empty, header button when data exists
- [ ] **Typography**: labels = text-sm text-gray-500, values = text-sm text-gray-900
- [ ] **Breadcrumbs**: text-gray-500 hover:text-gray-900 (not faint gray-400)
- [ ] **Copy buttons**: w-6 h-6 on all detail page IDs
- [ ] **Error messages**: humanized ("already exists" → friendly message)
- [ ] **Save buttons**: disabled + "No changes" when form unchanged (Edit modals)

## 22. Responsive & Edge Cases
- [ ] All tables horizontally scrollable on narrow screens
- [ ] Empty states consistent across all pages (EmptyState component)
- [ ] Error states show retry button
- [ ] Pagination on Customers, Invoices, Subscriptions, Audit Log, Usage Events
- [ ] CSV export on Customers and Invoices pages
- [ ] Search/filter on Customers, Subscriptions, Invoices

---

## Test Results

| # | Flow | Status | Notes |
|---|------|--------|-------|
| 1 | Tenant Onboarding | | |
| 2 | Settings | | |
| 3 | Pricing Setup | | |
| 4 | Plan Detail | | |
| 5 | Meter Detail | | |
| 6 | Customer Creation | | |
| 7 | Customer Detail | | |
| 8 | Billing Profile | | |
| 9 | Subscription Creation | | |
| 10 | Subscription Detail | | |
| 11 | Usage Ingestion | | |
| 12 | Invoice Generation | | |
| 13 | Invoice Actions | | |
| 14 | Credit Notes | | |
| 15 | Credits | | |
| 16 | Dunning | | |
| 17 | API Keys | | |
| 18 | Webhooks | | |
| 19 | Audit Log | | |
| 20 | Dashboard | | |
| 21 | UI/UX Quality | | |
| 22 | Responsive & Edge Cases | | |
