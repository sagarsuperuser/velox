# Velox Manual Test Runbook

Practical runbook for exercising Velox end-to-end. Three tiers — pick the
subset that matches the change you made.

| Tier | When | Time |
|------|------|------|
| **1 — Smoke** | Pre-merge / nightly | ~15 min |
| **2 — Full** | Pre-release | ~2 hrs |
| **3 — Deep** | Major releases, infra changes, post-mortems | ~3 hrs |

Each flow has a stable ID (A1, B2, …) for cross-referencing. Steps use
`- [ ]`; copy a section into a scratch doc when running it. This file is
the canonical source, not a progress tracker.

---

## Setup

### Prerequisites

- Go 1.25+, Docker & Compose, Node.js 22+, [Stripe CLI](https://stripe.com/docs/stripe-cli)
- A Stripe test account (keys go in the dashboard per-tenant, not env vars)

### First-time config

```bash
cp .env.example .env
# Required for local dev:
#   VELOX_BOOTSTRAP_TOKEN=<openssl rand -hex 32>
#   VELOX_ENCRYPTION_KEY=<openssl rand -hex 32>   (64 hex chars)
```

### Start the stack (4 terminals)

```bash
make up                      # 1: Postgres + Redis + Mailpit
make dev                     # 2: API on :8080 (auto-migrates)
cd web-v2 && npm run dev     # 3: Dashboard on :5173
stripe listen --forward-to localhost:8080/v1/webhooks/stripe/<vlx_spc_id>   # 4: Stripe webhooks (after Settings → Payments → Connect)
```

`make bootstrap` (first run only) prints operator email + password and the
test/live API keys. Mailpit web UI at http://localhost:8025 captures all
outbound mail.

### Shell setup

```bash
export API=http://localhost:8080
export KEY=vlx_secret_test_…
```

JSON bodies referencing shell vars must use double-quoted JSON with
backslash-escaped quotes: `-d "{\"api_key\":\"$KEY\"}"`. Single-quoted
JSON works when no shell var is referenced.

### Test cards

| Card | Behavior |
|------|----------|
| `4242 4242 4242 4242` | Always succeeds |
| `4000 0000 0000 0341` | Attaches OK, declines on charge (dunning trigger) |
| `4000 0000 0000 9995` | Always declines |

---

# Tier 1 — Smoke (~15 min)

## FLOW S1: End-to-end smoke

Brings the stack up, runs the full money path, signs out. Pre-merge canary.

### S1.1 Stack health
- [ ] `make up` — containers start clean.
- [ ] `make dev` — logs show `migrations: applied N` + `using app database connection (RLS enforced)`.
- [ ] `curl localhost:8080/health` → `{"status":"ok"}`.
- [ ] `curl localhost:8080/health/ready` → 200 with `database`, `scheduler` ok.
- [ ] Frontend at http://localhost:5173 loads.

### S1.2 Bootstrap + sign in
- [ ] `make bootstrap` (if no tenants) prints test/live secret keys + publishable test key. Copy the secret test key.
- [ ] Sign in at `/login`. Redirect to dashboard.
- [ ] Cookie `velox_session` set, `HttpOnly: ✓`. No API key in localStorage.

### S1.3 Stripe connection
- [ ] Settings → Payments → paste `sk_test_...` + `pk_test_...` → Connect. `vlx_spc_...` shown.
- [ ] Terminal 4: `stripe listen --forward-to localhost:8080/v1/webhooks/stripe/<vlx_spc_...>`. Paste the `whsec_...` back.
- [ ] Settings shows "Connected".

### S1.4 Build the graph
- [ ] Pricing → rating rule `api_calls` flat $0.01. Meter `api_calls` sum, link to rule. Plan `starter` $29/mo, attach meter.
- [ ] Customers → create "Smoke Corp", external_id `smoke_corp`, email any@any.test. Billing profile: address + USD + 10% tax.
- [ ] Customer detail → Set Up Payment → `4242 4242 4242 4242`.
- [ ] Mint a test clock (avoids 30-day wait):
  ```bash
  curl -sS -X POST "$API/v1/test-clocks" -H "Authorization: Bearer $KEY" \
    -H "Content-Type: application/json" \
    -d "{\"name\":\"smoke\",\"frozen_time\":\"$(date -u +%FT%TZ)\"}" | jq .
  ```
- [ ] Customer detail → New Subscription → Starter plan, **pin to test clock**.

### S1.5 Bill + charge
- [ ] Ingest 1,000 events:
  ```bash
  TS=$(date -u +%FT%TZ)
  jq -n --arg ts "$TS" '[range(1000) | {external_customer_id:"smoke_corp",event_name:"api_calls",quantity:"1",idempotency_key:"smoke_\($ts)_\(.)"}]' > /tmp/events.json
  curl -sS -X POST "$API/v1/usage-events/batch" -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" --data-binary @/tmp/events.json | jq .
  ```
- [ ] Advance the clock 31 days: `POST /v1/test-clocks/$CLK/advance` with `frozen_time = now+31d` (BSD `date -u -v+31d` / GNU `date -u -d '+31 days'`).
- [ ] `curl -sS -X POST "$API/v1/billing/run" -H "Authorization: Bearer $KEY"` → 1 invoice generated.
- [ ] Invoice auto-finalized, `payment_status=succeeded`. Line items: prorated base + usage + tax.
- [ ] Stripe CLI shows `payment_intent.succeeded`. Dashboard MRR > $0.

### S1.6 Sign out
- [ ] Sidebar → Sign Out. Redirect to /login.
- [ ] Stale cookie on `/v1/whoami` → 401.

**S1 passing = core engine healthy.**

---

# Tier 2 — Full Suite

One flow per shipping feature. Run only what your change touched.

## Tenant timezone

Single tenant-wide timezone used for date input and timestamp display
(UTC for storage and billing math). Set in Settings → Account → Timezone.

- [ ] Change timezone to `Asia/Kolkata` or `America/Los_Angeles` → dashboard timestamps shift, zone abbreviation appended (e.g. `2:14 PM PDT`).
- [ ] API key expiry / coupon valid-until / list-page from-to filters interpret civil dates as start/end of day in tenant TZ.
- [ ] Subscription billing math stays UTC ("monthly on the 5th" = 5th UTC).
- [ ] Wire format always UTC ISO 8601 with `Z`.

## FLOW A1: Sign-in

- [ ] Empty form → inline error, no request.
- [ ] Wrong password → 401 "Invalid email or password".
- [ ] 5 wrong attempts in 1 min → 429, locked 15 min.
- [ ] Right credentials → redirect to `/`, dashboard loads.
- [ ] Cookie `velox_session`: HttpOnly, SameSite=Lax. No `velox_*` in localStorage.
- [ ] Reload → still signed in.
- [ ] Sign out → cookie cleared, redirect to /login. Stale cookie → 401.

### Password reset

- [ ] Forgot password → submit any email → 200 (no enumeration).
- [ ] Reset email lands in Mailpit (http://localhost:8025).
- [ ] Click link → set new password (12+ chars) → /login?reset=success → sign in.
- [ ] Reused token → 422. Token >1h old → 422. Password <12 chars → 422.

## FLOW A2: /v1/whoami

- [ ] Cookie path: `curl -b /tmp/c.txt $API/v1/whoami` → `{tenant_id, user_id, email, livemode}`.
- [ ] Bearer path: `curl -H "Authorization: Bearer $KEY" $API/v1/whoami` → `{tenant_id, key_id, key_type, livemode}`.
- [ ] No credentials → 401.
- [ ] Cookie + Bearer with disagreeing identities → cookie wins.
- [ ] Revoked API key on Bearer → 401 immediately. Cookie sessions unaffected.
- [ ] Publishable key on Bearer → `key_type:"publishable"`. Most write endpoints → 403.

## FLOW A3: Test/Live mode toggle

- [ ] Top-right pill: amber "Test mode" default. Click → emerald "Live mode"; data repopulates.
- [ ] `/v1/whoami` reflects new `livemode` immediately.
- [ ] List endpoints filter by mode — flipping shows different counts.
- [ ] `POST /v1/auth/mode` without cookie → 401.
- [ ] Live mode + missing live Stripe creds → red banner with "Connect Stripe" link.

---

## FLOW K1: API key permissions

- [ ] Secret key: full read/write everywhere.
- [ ] Publishable key: read-only — POST → 403.
- [ ] Revoked key: any request → 401 `api key revoked`.
- [ ] Create dialog: raw key shown once, copy button, "you won't see this again" warning.

## FLOW K2: Expiration

- [ ] Create key with presets: No expiration / 30d / 90d / 1y / Custom.
- [ ] Custom: today is disabled in calendar grid + Today button (tooltip explains minDate).
- [ ] Tenant TZ consistency: pick "30d" → hint "Key will expire on <date> at 11:59 PM <TenantTZ>". Stored UTC matches "23:59:59.999 in tenant TZ".
- [ ] Create with `expires_at = now+90s` via API → 200 until expiry, 401 `api key expired` after.
- [ ] Backdate `expires_at` via psql → 401 `api key expired`.
- [ ] Keys ≤7 days from expiry → yellow "Expires in Xd" badge.
- [ ] Expired keys collapsed under "Expired keys" section; Revoke still enabled.

## FLOW K3: API Keys page UX

- [ ] Empty tenant: EmptyState with "Create API Key" button.
- [ ] Active card shows: name, masked prefix, key-type badge, Created/Last-used/Expires.
- [ ] "Expired keys" + "Revoked keys" sections collapsed by default.
- [ ] Create dialog: name (≤100 chars), key type (Secret/Publishable), expiration preset. Custom requires date.
- [ ] Submit success → "API Key Created" dialog with raw key + Copy button (toast "Copied"). Closing removes key from memory.
- [ ] Per-row Revoke → typed AlertDialog → revoke. Always enabled (sessions are user-bound, not key-bound).
- [ ] Per-row Rotate → modal with grace presets (Now / 1h / 24h / 7d). Old key works through window.
- [ ] Server validation surfaces inline (e.g. blank name → 422 with `field=name`).

## FLOW K4: Rotate

- [ ] Rotate with `expires_in_seconds=300` → new raw_key returned; old key works ~5 min.
- [ ] Rotate with `expires_in_seconds=0` → old key 401 `invalid api key` immediately.
- [ ] Rotate revoked key → 422 `cannot rotate a revoked key`.
- [ ] `expires_in_seconds > 604800` → 422.

---

## Test Clocks (test mode only)

## FLOW TC1: Test Clocks page

- [ ] Sidebar "Test mode" group + "Test Clocks" entry only when livemode=false. Live mode → entry hidden, `/test-clocks` redirects to `/`.
- [ ] Empty state with "New test clock" button.
- [ ] New dialog: optional name + datetime-local picker (defaults to now). Submit → detail page.

## FLOW TC2: Detail + Advance

- [ ] Detail header: name + status pill + Advance/Delete. Advance disabled when status≠ready.
- [ ] Advance dialog presets: `+1h / +1d / +1mo` + custom. Earlier-than-current time → inline error.
- [ ] Submit → status flips to **Advancing** with spinner; polls every ~1.5s until ready.
- [ ] Catchup failure → status **Failed** with destructive banner.
- [ ] Subscriptions table on detail page lists pinned subs.

## FLOW TC3: Pinning

- [ ] Test mode → Create Subscription has "Pin to test clock" dropdown. Empty = wall-clock sub.
- [ ] Live mode → dropdown hidden.
- [ ] `/subscriptions/:id` for pinned sub → "Test clock" badge linking to detail.

## FLOW TC4: Catchup correctness

- [ ] Pin monthly sub at "now". Advance 1 month → exactly 1 invoice.
- [ ] Advance 3 months → 3 sequential-period invoices.
- [ ] Advance backwards → 422 `must be strictly after current frozen_time`.
- [ ] Second advance while advancing → 409.
- [ ] Delete clock → pinned subs gone; standalone subs unaffected.

---

## Customer Portal

## FLOW CP1: Magic-link login

- [ ] `POST /v1/public/customer-portal/magic-link {email}` → 202 (always, any email).
- [ ] Real customer → email lands in Mailpit with link to `/portal/magic?token=…`.
- [ ] `CUSTOMER_PORTAL_URL` unset → server logs `magic-link delivery failed err="customerportal: CUSTOMER_PORTAL_URL not set"`.
- [ ] `POST /v1/public/customer-portal/magic/consume {token}` → 200 `{token, customer_id, livemode, expires_at}`. Reused token → 401.
- [ ] Returned token as Bearer on `/v1/me/invoices` → invoice list.
- [ ] `/portal/login` → email form. Submit real customer's email → "Check your email" card.
- [ ] Click email link → `/portal/magic?token=…` → "Signing you in…" → `/portal`.
- [ ] `/portal/magic` no token / invalid token → "Sign-in link not valid" + "Request a new link".
- [ ] `/portal` without session → redirect to `/portal/login`.
- [ ] `/portal` loads: Payment method, Subscriptions, Invoices sections. Header shows tenant company name + Sign out.

## FLOW CP2: Customer cancels subscription

- [ ] `GET /v1/me/subscriptions` (Bearer = portal token) → only that customer's subs.
- [ ] `POST /v1/me/subscriptions/{id}/cancel` → 200, status canceled.
- [ ] Cancel another customer's sub → 404 (no enumeration).
- [ ] Webhook `subscription.canceled` payload includes `canceled_by:"customer"`.
- [ ] UI: each non-canceled sub has Cancel button → typed confirm dialog (`CANCEL`) → row shows `canceled` badge after.

## FLOW CP3: Customer updates payment method

- [ ] Customer must have existing PaymentSetup.
- [ ] `POST /v1/me/payment-method/update {return_url}` → 201 `{url}` (Stripe Checkout setup-mode).
- [ ] Open URL → enter new card → Stripe redirects to return_url → webhook flips `payment_setups.setup_status=ready`.
- [ ] If invoices had `auto_charge_pending=true` → next scheduler tick collects them.
- [ ] No PaymentSetup → 400 `missing_payment_setup`. No live Stripe → 503 `stripe_unavailable`.

## FLOW CP4: Invoice PDF download

- [ ] `curl -OJ -H "Authorization: Bearer $PORTAL_TOKEN" $API/v1/me/invoices/$INV_ID/pdf` → PDF, `Content-Type: application/pdf`, `Content-Disposition: attachment`.
- [ ] Different customer's invoice → 404.

## FLOW CP5: Branding

- [ ] `GET /v1/me/branding` (Bearer = portal token) → tenant company name, logo URL, brand color, support URL.

## FLOW E: Email delivery (SMTP)

Single delivery path: when SMTP isn't configured every send returns
`ErrSMTPNotConfigured`. No stdout fallback. Local dev = Mailpit
(`docker compose up -d mailpit`, `SMTP_HOST=localhost:1025 SMTP_TLS=none`).

Boot warnings on startup (one each when var unset; never fatal):
- `SMTP NOT CONFIGURED`
- `CUSTOMER_PORTAL_URL NOT SET`
- `PAYMENT_UPDATE_URL NOT SET`

- [ ] **E1 STARTTLS**: `SMTP_TLS=starttls SMTP_PORT=587` + creds. Trigger invoice email → `email_outbox` row pending → dispatched within seconds → recipient receives.
- [ ] **E2 Implicit TLS**: `SMTP_TLS=implicit SMTP_PORT=465`. Same expectation; verifies `tls.Dial` path.
- [ ] **E3 Not configured**: unset `SMTP_HOST`. Boot → `SMTP NOT CONFIGURED` warning. Trigger send → outbox claims, logs `ErrSMTPNotConfigured`, retries with backoff, lands in DLQ.
- [ ] **E4 5xx bounce**: send to `foo@invalid` → `customers.email_status='bounced'`, `email_bounce_reason` carries the SMTP error.
- [ ] **E5 Per-provider**: verify SendGrid / Postmark / SES / Mailgun / Resend per `docs/ops/email-setup.md`.
- [ ] **E6 Mailpit dev path**: `SMTP_HOST=localhost:1025 SMTP_TLS=none` → mail lands at http://localhost:8025 with HTML+text bodies.

## FLOW EX: Streaming CSV exports

- [ ] **EX1 customers**: `curl -OJ $API/v1/exports/customers.csv` → `customers-YYYYMMDD-HHMMSS.csv`. Date filter `?from=…&to=…` works. Invalid `from` → 400.
- [ ] **EX2 invoices**: `$API/v1/exports/invoices.csv` → invoice rows incl. amounts, period, lifecycle timestamps.
- [ ] **EX3 subscriptions**: `$API/v1/exports/subscriptions.csv` → subs with `plan_ids` (pipe-delimited).
- [ ] **EX4 usage-events**: requires `from`+`to`; missing → 400. Span >366d → 400.
- [ ] Publishable key can call all exports (read-only perm).
- [ ] Streaming verified: large export shows progressive output via `tail -f`.

---

## Billing Engine

## FLOW B1: Arrears + proration

- [ ] New sub mid-month → `billing_period_end` = 1st of next month.
- [ ] Run billing before period close → 0 invoices.
- [ ] Backdate `current_period_end` → 1 invoice with prorated base + usage + tax + due date + invoice-number prefix.

## FLOW B2: Tax precision (basis points)

- [ ] Tax `7.25%` → `tax_rate_bp=725` (no float column).
- [ ] $100 subtotal → tax exactly $7.25.
- [ ] 3 line items $33.33+$33.33+$33.34: per-line tax sums exactly to invoice tax.

## FLOW B3: Idempotency

- [ ] Run billing twice in same period → no duplicate invoice. Logs `invoice already exists for billing period (idempotent skip)`.

## FLOW B4: Auto-charge retry

- [ ] Decline-on-charge card → invoice has `auto_charge_pending=true`, `payment_status=pending`.
- [ ] Update card → next scheduler tick → `payment_status=succeeded`, `auto_charge_pending=false`.

## FLOW B5: Idempotency-Key header

- [ ] POST with `Idempotency-Key: test-123` → 201.
- [ ] Same body + key → same response, 1 row.
- [ ] Same key + different body → 409.

## FLOW B6: Subscription lifecycle

- [ ] Trial 7 days → no invoice during trial. After trial → first invoice generated.
- [ ] Pause → confirm dialog → no billing, no metering. Resume → bills next period.
- [ ] Cancel → confirm dialog → status canceled, no future billing.

## FLOW B7: Plan change + proration

- [ ] Active sub on Starter → change to Enterprise "immediately" → proration invoice generated.
- [ ] Downgrade immediately → credits to balance.
- [ ] Plan change without immediately → no proration; applies at next period boundary.

## FLOW B8: Usage caps

- [ ] `usage_cap_units=5000`, `overage_action=block`, ingest 8000 → billed 5000.
- [ ] Switch to `overage_action=charge`, ingest 8000 → billed 8000.

## FLOW B9: Customer price overrides

- [ ] POST /v1/price-overrides → that customer's invoice uses override price.
- [ ] Other customers → default rule price.

## FLOW B10: Manual tax + cross-border zero-rating

- [ ] `tax_home_country="IN"`, `tax_rate_bp=1800`, `tax_name="IGST"`.
- [ ] Domestic IN customer: $100 → $18, name `IGST`, PDF `IGST (18.00%)`.
- [ ] Export US customer: $100 → $0, name `IGST (zero-rated export)`.
- [ ] Customer with no country → 18% applies.
- [ ] Customer with `tax_exempt=true` → $0, blank name.
- [ ] Clear `tax_home_country` → US customer back to 18%.
- [ ] India B2B reverse-charge: PDF carries supplier GSTIN under company line + "Tax payable on reverse charge basis: YES".
- [ ] EU reverse-charge: PDF retains EU wording.
- [ ] Stripe Tax `taxability_reason=not_collecting` round-trip → line item `tax_reason='not_collecting'`, badge in dashboard.
- [ ] `tax_status='exempt'` customer → `tax_reason='customer_exempt'`, PDF carries customer-exemption legend.
- [ ] Invalid country codes (`INDIA`, `in `, `XX`) → 422.

## FLOW B11: Tax-ID validation

- [ ] `in_gst` + `27AAEPM1234C1Z5` → accepted. Legacy `gstin` → normalized to `in_gst` on write.
- [ ] `eu_vat` + `DE123456789`, `au_abn` + `51824753556` → accepted.
- [ ] Unknown Stripe code (`za_vat`, `br_cnpj`) → accepted as-is.
- [ ] Malformed `in_gst` / `eu_vat` / `au_abn` → 422 with format-specific message.
- [ ] Empty `tax_id` → always accepted.

## FLOW B12: Subscription activity timeline

- [ ] Create → activate → pause → resume → plan change → cancel.
- [ ] `GET /v1/subscriptions/{id}/timeline` → events ascending; each carries timestamp, source, event_type, status, description, actor.
- [ ] Operator cancel → "Subscription canceled". Customer cancel → "Subscription canceled by customer".
- [ ] Status colors: emerald/amber/red/violet/blue.
- [ ] Subscription detail UI shows Activity card; resolved actor renders "by {actor_name}".
- [ ] Nonexistent sub ID → 404.

## FLOW B13: Multi-dimensional meters

- [ ] `POST /v1/usage-events` with dimensions `{model, operation, cached, tier}` → 201; value stored as NUMERIC.
- [ ] Decimal preserved end-to-end (`10000.5` round-trips).
- [ ] Replay same idempotency_key → 200 with original event id, no duplicate.
- [ ] Rule with `dimension_match={"operation":"input"}` claims only input events. More-specific match (`+cached:true`) wins over less-specific.
- [ ] Aggregations sum / count / max / last_during_period / last_ever all bill correctly. Switching aggregation between cycles re-bills next cycle without affecting past invoices.
- [ ] `cmd/velox-bench` sustains 50k events/sec on local Postgres.

## FLOW B14: Billing thresholds

- [ ] PATCH sub `billing_thresholds:[{meter_id, usage_gte:10000}]`. Ingest 9999 → no early finalize. Ingest 1 more → invoice auto-finalized within 1 tick, `billing_reason="threshold"`. New events start a new period.
- [ ] PATCH `{amount_gte:50000}` → cross $500 → same shape.
- [ ] Cross threshold + immediately `POST /v1/billing/run` → idempotent skip.
- [ ] Subscription detail "Spend Thresholds" card: empty state with Set button. Edit dialog has subtotal cap, reset_billing_cycle checkbox, per-item rows. Save shows `$1,000.00` (from cents) and `≥10000.5 units`. Clear thresholds → flips to empty.
- [ ] Canceled/archived subs → Set/Edit hidden.

### Billing alerts (backend-only — no UI page after lean-cut)

- [ ] `POST /v1/billing/alerts {customer_id, meter_id, threshold:1000, recurrence:"one_time"}` → 201. Cross threshold → `billing.alert.triggered` webhook + dashboard notification.
- [ ] Cross again same period → no second fire (one_time).
- [ ] `recurrence:"per_period"` → fires once per cycle.
- [ ] Webhook payload: `customer_id, meter_id, threshold, current_value, period_start, period_end, recurrence`.

## FLOW B17: Meter Detail page

- [ ] Breadcrumb `Pricing / <meter>`. Header: name, ID, default-aggregation badge.
- [ ] Default rule card renders latest version of linked rating rule (verify by editing rule → version badge bumps).
- [ ] Mode rendering: flat = price + per-unit; graduated = tiers table; package = inline.
- [ ] Dimension-matched rules table: Priority, Dimension chips, Aggregation, Rating rule, Created, trash.
- [ ] Add rule dialog: dimension k=v rows, aggregation select with helper text, priority input, rating-rule select.
- [ ] Dimension values coerce: `true/false` → bool, numeric strings → number, else string.
- [ ] Submit success → toast + table refetches in priority order.
- [ ] Per-row trash → typed `delete` confirm. Already-finalized invoices unaffected after delete.
- [ ] "Used by N plans" section lists plans with this meter.

---

## Pricing Recipes

## FLOW R1: List + preview

- [ ] `GET /v1/recipes` → 5 entries (anthropic_style, openai_style, replicate_style, b2b_saas_pro, marketplace_gmv).
- [ ] `POST /v1/recipes/{key}/preview` → projected products/prices/meters/dunning/webhooks (no DB writes).
- [ ] Unknown key → 404.

## FLOW R2: Instantiate

- [ ] `POST /v1/recipes/anthropic_style/instantiate {livemode:false}` → 201 with all created IDs. DB now has products + prices + meters + dunning policy + webhook endpoint.
- [ ] Pricing rules carry `dimension_match` JSONB.
- [ ] Audit log: one entry per resource, `actor=recipe:<key>`.
- [ ] Repeat for all 5 recipes — each completes <500ms.

## FLOW R3: Per-tenant idempotency

- [ ] Instantiate same recipe twice → second call 409 `recipe already instantiated`.
- [ ] Different tenant, same recipe → 201.
- [ ] `DELETE /v1/recipes/{key}/instance` cleans up product+prices+meters+webhook+dunning atomically.

## FLOW R4: Atomic rollback

- [ ] Inject mid-instantiate failure (e.g. invalid webhook URL) → 422; zero rows created.
- [ ] No `tenant_recipe_instances` row.

## FLOW R5: Dashboard UI

- [ ] `/recipes` → 5 cards, Preview opens side panel with projected resources.
- [ ] Instantiate dialog names side-effects ("creates 4 products + 12 prices + …"). Confirm → redirect to `/products`.
- [ ] Recipe card flips to "Installed" with date.

### Uninstall

- [ ] Installed card → configure dialog has Uninstall button.
- [ ] Confirm uninstall → `recipe_instances` row drops; plans/meters/etc. stay (no cascade).
- [ ] Re-install without renaming originals → 422 name collision.
- [ ] Re-install after archiving originals → succeeds.

---

## Invoices

## FLOW I1: Multiple meters

- [ ] Plan with API ($0.01/call) + Storage ($0.10/GB max). Ingest 2000 calls + 50 GB → invoice has 3 line items: base $29 + API $20 + storage $5.

## FLOW I2: Negative usage

- [ ] Ingest 1000 then -200 (correction) → meter shows net 800, billed for 800.

## FLOW I3: Manual line items

- [ ] POST /v1/invoices → draft. Add Line Item "Setup fee" $250, "Consulting" 2×$150 → total $550.
- [ ] Finalize → auto-charges via Stripe.

## FLOW I4: Void

- [ ] Void invoice with credits applied → credits reversed, balance restored, Stripe PI canceled, active dunning resolved, audit log entry.

## FLOW I5: Collect + payment timeline

- [ ] Finalized unpaid → POST /v1/invoices/{id}/collect → PI created.
- [ ] GET /v1/invoices/{id}/payment-timeline → all attempts in order with ts/amount/status/PI id.

## FLOW I5b: Invoice attention banner

Server-derived from invoice fields. Suppressed for healthy / paid / voided / draft invoices.

### Critical
- [ ] **tax_location_required**: US customer missing postal_code, finalize → red banner "Customer address required", primary action **Edit billing profile**, secondary **Retry tax**.
- [ ] **tax_calculation_failed (provider auth)**: revoke Stripe key → red banner code `tax.provider_auth`, action **Rotate API key**.
- [ ] **payment_failed**: card `4000 0000 0000 9995` → red banner code `payment.declined`, message = truncated `last_payment_error`.

### Warning
- [ ] **tax pending**: amber banner with same code/actions, severity warning.
- [ ] **overdue**: past `due_at` → amber banner code `lifecycle.overdue`, actions **Charge now** + **Send reminder**.

### Info
- [ ] **payment_processing**: muted banner, no actions.
- [ ] **payment_scheduled**: `auto_charge_pending=true` → muted banner, action **Charge now**.
- [ ] **awaiting_payment**: muted banner, actions **Charge now** + **Send reminder**.

### Banner shape
- [ ] Severity styling: critical=red+AlertCircle, warning=amber+AlertTriangle, info=muted+Info.
- [ ] Reason badge + dotted code in mono. `since` = relative time. `doc_url` → "Learn more ↗".
- [ ] `detail` (raw provider payload) → `<details>` "Provider response" disclosure.
- [ ] Healthy/paid/voided/draft → no banner.

### Retry tax
- [ ] Banner showing → click **Retry tax** → button "Retrying…" → audit log row `action='retry_tax'` with before/after attention codes.
- [ ] Issue fixed → invoice has `tax_status='ok'`, banner disappears, toast "Tax recalculated successfully".
- [ ] Still failing → banner refreshes with new reason. Each click bumps `tax_retry_count`.
- [ ] Retry on non-pending/non-failed invoice → 409.

### List + draft cleanup
- [ ] `/invoices` rows: severity-tinted dot next to invoice number; tooltip surfaces typed reason. Healthy/draft = no dot.
- [ ] Draft rows show "draft" pill (Dashboard) or em dash (Invoices, Subscription detail) instead of `payment_status='pending'`.
- [ ] Invoice detail draft row: muted "Draft invoice — finalize to issue and begin collection." hint.

## FLOW I6: Email + PDF preview

- [ ] Invoice detail → Email → outbox row → PDF attached → Mailpit shows delivery.
- [ ] Preview PDF → renders in overlay; close via X / backdrop.

### Branded HTML body

Multipart text+HTML with tenant chrome. Configure tenant `company_name`, `logo_url`, `brand_color`, `support_url`.

- [ ] Invoice email HTML: tenant logo + name in header, 2px brand-color accent bar, line-items summary, "Amount due" card, **View & pay invoice** CTA styled with brand_color.
- [ ] CTA URL → `{HOSTED_INVOICE_BASE_URL}/invoice/{public_token}`.
- [ ] Footer: "Contact support" + "Powered by Velox Billing".
- [ ] Plain-text part still present.
- [ ] Receipt email: same chrome, CTA "View receipt".
- [ ] Dunning email: attempt N of M, next retry date, CTA "Update payment".
- [ ] Payment-update-request email: CTA uses `PAYMENT_UPDATE_URL` token URL.
- [ ] Operator emails (portal magic-link, password-reset) intentionally plain text — no HTML chrome.

## FLOW I7: Zero-amount invoice

- [ ] Plan `base_amount_cents=0`, no meters → either no invoice or $0 auto-paid (no Stripe charge).

## FLOW I8: Currency consistency

- [ ] Tenant default USD → switch to EUR → new invoices EUR, existing unchanged.
- [ ] Customer with `billing_profile.currency=GBP` → invoices GBP regardless of tenant default.

## FLOW I9: Credit note on void

- [ ] Void invoice → issue CN → error "cannot issue credit note on voided invoice". CN not created.

## FLOW I11: `create_preview`

- [ ] `POST /v1/invoices/create_preview {subscription_id}` → invoice shape with `id=null`, no DB row.
- [ ] Plan-change confirmation dialog renders preview before commit.
- [ ] Cost-dashboard projection populated when engine returns a value.

## FLOW I12: One-off invoice composer

- [ ] Customer detail → "New invoice" → inline composer.
- [ ] Add line items → totals tick live → Save draft → row in customer's invoice list with `status=draft`, `subscription_id=null`.
- [ ] Finalize → standard PaymentIntent path.

## FLOW I10: Hosted invoice page

- [ ] Draft invoice has no `public_token`. Finalize → token minted (`vlx_pinv_` + 64 hex).
- [ ] Detail page: **Copy Link** button. **Rotate** typed-confirm dialog (type `ROTATE`). Buttons hidden on draft.

### Public render (open in incognito)
- [ ] Loads without login. Header: tenant logo + company_name + support_url. Optional 3px accent bar.
- [ ] Invoice meta: number (mono), amount due (large tabular), due date.
- [ ] Bill-to + From columns. Line-items table with tabular numerals.
- [ ] Totals: subtotal, optional discount, optional tax with rate, reverse-charge note if applicable, total, amount paid, **Amount due** bold.
- [ ] **Pay {amount}** primary button (brand_color). **Download PDF** secondary.
- [ ] Footer: "Secured by Stripe" + "Powered by Velox Billing".

### Pay
- [ ] Click Pay → `POST /v1/public/invoices/{token}/checkout` → Stripe Checkout. Pay with `4242…` → redirect to `{baseURL}/invoice/{token}?paid=1`.
- [ ] Provisional "Processing your payment…" banner (green spinner).
- [ ] Webhook arrives → invoice paid → page auto-refetches → "Paid on {date}" banner; Pay button gone.
- [ ] PDF still downloads on paid.

### Variants
- [ ] Voided invoice → "Voided on {date}" banner, no Pay, PDF works.
- [ ] Draft invoice URL → 404.
- [ ] Rotated → old URL 404, new works.

### Security
- [ ] Public JSON has no `tenant_id, subscription_id, tax_id, stripe_*_id`.
- [ ] 61+ req/min same IP → 429 with `Retry-After`.
- [ ] Operator `POST /v1/invoices/{id}/rotate-public-token` requires `PermInvoiceWrite`.

---

## Dunning

## FLOW D1: Retry cycle + escalation

- [ ] Decline card → run billing → dunning run created. Page shows stat cards (Active, Escalated, Recovered, At Risk $) + tab filters with counts.
- [ ] Sidebar Dunning badge shows count.
- [ ] Run state Active, "No retries yet", `next_action_at` scheduled.
- [ ] Backdate `next_action_at` → next tick increments attempt count.
- [ ] After max retries → state Escalated.

## FLOW D2: Resolution

- [ ] "Payment recovered" → invoice marked paid.
- [ ] "Manually resolved" → run resolved without touching invoice.

## FLOW D3: Per-customer override

- [ ] Customer detail → Dunning Override → max_retries=5, grace=7d → applies on next failure.
- [ ] Reset to Default → override removed.

## FLOW D4: Self-service payment update

- [ ] Trigger payment failure → email/log carries `http://localhost:5173/update-payment?token=vlx_pt_…`.
- [ ] Open in incognito → page loads without login, shows customer + invoice + amount, "Secured by Stripe".
- [ ] Click Update → Stripe Checkout setup → new card → redirect → webhook updates PM.
- [ ] Re-open same URL → "Link expired or invalid". Random token → same. No token → "No payment update token provided".

---

## Credits & Credit Notes

## FLOW C1: Credits lifecycle

- [ ] Grant $50 expires 30d → balance $50, ledger Expires column populated.
- [ ] Run billing → applied, amount_due reduced, Stripe charged remainder. Ledger entry "Applied to invoice <number>".
- [ ] Grant $500 + $79 invoice → fully credited, amount_due $0, balance $421, Stripe NOT charged.
- [ ] Deduct $20 → confirmation → balance reduced, ledger entry.

## FLOW C2: Credit notes

- [ ] Unpaid invoice → Issue Credit "Billing error" $20 → preview → Issue → amount_due reduced.
- [ ] Paid invoice → "Credit to balance" $15 → customer balance +$15; CN listed in invoice "Post-payment adjustments".
- [ ] Paid invoice → "Refund to payment method" $10 → Stripe refund; CN badge "Refunded"; balance unchanged.
- [ ] CN > amount_due (unpaid) or > amount_paid (paid) → error.
- [ ] CN page: stat cards (Total Credited, Refunded, Applied to Balance, Issued), tab filters with counts, search, draft CNs show Issue+Void.

## FLOW C3: Coupons

- [ ] Create `PRO20` 20% off, restricted to Enterprise.
- [ ] Redeem on Starter → "coupon is not valid for this plan". Enterprise → applied.
- [ ] Coupon detail Edit dialog: change name → header h1 updates without refresh; audit log records `coupon.updated`.
- [ ] Clear `expires_at` / `max_redemptions` → header tiles flip to "No expiry" / "N redeemed".
- [ ] Restrictions: setting only `min_amount` collapses card to single row. Clearing all three → card disappears.
- [ ] `min_amount: -50` → 422 inline on field.
- [ ] Archived coupon → Edit hidden, Restore visible.

---

## Webhooks

## FLOW W1: Stripe signature verification

- [ ] Valid payload + signature ≤300s → 200, processed.
- [ ] Replay 5+ min later → rejected (timestamp tolerance).
- [ ] Modified payload + original signature → rejected.

## FLOW W2: Outbound secret rotation (72h grace)

- [ ] Rotate Secret on endpoint → modal shows new `whsec_…` + green "Previous secret valid until {ts}" card.
- [ ] API response includes `secret` + `secondary_valid_until`.
- [ ] Endpoints table row shows "Dual-signing until {ts}".
- [ ] Trigger event during grace → header carries TWO `v1=` entries; both old and new verify.
- [ ] Backdate `secondary_secret_expires_at` → header carries one entry; only new verifies.

## FLOW W3: Delivery stats

- [ ] Endpoints page Success Rate column: green ≥95%, amber 70–94%, red <70%.
- [ ] Replay failed event → success rate updates.

## FLOW W4: Live event stream + replay

- [ ] Webhooks → Events → recent deliveries with state dot.
- [ ] Trigger event → row streams in <1s without refresh (SSE).
- [ ] Click delivery → side panel: URL, status, headers, body.
- [ ] Replay failed → fresh attempt fires; original preserved.
- [ ] Multi-retry event → "Diff" tab shows payload diff between attempts.
- [ ] Stop Redis or dispatcher → readiness degraded; UI loads but stops streaming.

---

## Customers

## FLOW CU1: Settings + billing profile

- [ ] Settings: company name change → "Saved" indicator. Navigating with unsaved changes prompts.
- [ ] Currency change → new invoices use it; existing unchanged.
- [ ] Edit billing profile (address, tax ID) → PDF reflects update.

## FLOW CU2: Operator customer-portal API

- [ ] `GET /v1/customer-portal/{customer_id}/overview` → active subs, recent invoices, credit balance.
- [ ] `/subscriptions`, `/invoices` scoped to that customer.

## FLOW CU3: GDPR export + erasure

- [ ] `GET /v1/customers/{id}/export` → customer + profile + invoices + subs + ledger + balance.
- [ ] Stripe IDs redacted (last 4 visible); PM details redacted.
- [ ] Delete with active subs → "cancel them before deletion".
- [ ] Cancel subs, `POST /v1/customers/{id}/delete-data` → display_name="Deleted Customer", email cleared, profile PII anonymized, status archived, invoices preserved, audit entry.
- [ ] Re-export deleted customer → anonymized.

## FLOW CU4: Archive cascade

- [ ] Archive → confirm → amber banner "data is read-only". Action buttons hidden.
- [ ] Run billing → no invoices for archived customer's subs. Existing invoices and credits still readable.
- [ ] Restore → banner gone, actions reappear.
- [ ] Customers list → Archived tab.

## FLOW CU6: Brand color + logo URL

- [ ] Settings → Business → Logo URL accepts public HTTPS URL. Live thumbnail renders. Invalid → "Couldn't load image".
- [ ] Brand color: native color picker + hex input + Clear. Invalid hex (`#zzz`, missing `#`, uppercase) → 422 client+server.
- [ ] Save → invoice PDF: company name tinted, 2px accent bar.
- [ ] Clear color → next PDF byte-identical to pre-migration neutral.

## FLOW CU8: Cost-dashboard widget

- [ ] `POST /v1/customers/{id}/rotate-cost-dashboard-token` → `{token, public_url}`. Token starts `vlx_pcd_` + 64 hex.
- [ ] `GET /v1/public/cost-dashboard/{token}` (no auth) → sanitized projection: customer_id, tenant_id, billing_period, subscriptions, usage[meter+rules+totals], totals, projected_total_cents.
- [ ] Absent fields: email, display_name, external_id, metadata, billing_profile.
- [ ] No active sub → empty arrays, `billing_period.source='no_subscription'`, NOT 5xx.
- [ ] Rotate → old URL 404 immediately. Audit log records rotation; plaintext token never logged.
- [ ] Rate limit: 61+ req/min/IP → 429.
- [ ] `<VeloxCostDashboard token baseUrl theme accent />` compiles cleanly with `tsc --noEmit`. Theme/accent params switch iframe styling.

---

## Platform

## FLOW P1: Feature flags

- [ ] `GET /v1/feature-flags` → seeded flags: `billing.auto_charge`, `billing.tax_basis_points`, `webhooks.enabled`, `dunning.enabled`, `credits.auto_apply`, `billing.stripe_tax`.
- [ ] `PUT /v1/feature-flags/webhooks.enabled {enabled:false}` → events not delivered. Re-enable → resumes.
- [ ] Per-tenant override: `PUT /…/overrides/{tenant_id}` disables for one tenant; DELETE falls back to global.
- [ ] Toggle reflects within 30s.

## FLOW P2: Audit log

- [ ] Several actions (create customer, grant credits, void invoice, change plan) → all logged.
- [ ] Stat cards: Total, Today, Unique Actors, Destructive Actions.
- [ ] Destructive rows have red left border. Expand → metadata + "View" link.
- [ ] Filters: resource type, action, date range. Export CSV → all entries.

## FLOW P3: Usage summary

- [ ] `GET /v1/usage-summary/{customer_id}?from=…&to=…` → per-meter aggregated totals matching ingestion.

## FLOW P4: Empty billing cycle

- [ ] No subs due → trigger billing → "0 invoice(s) generated", clean exit, dashboard unchanged.

## FLOW P5: Health checks

- [ ] `/health` → 200 `{"status":"ok"}`. `/health/ready` → 200 with database, scheduler ok.
- [ ] Stop Postgres → `/health/ready` → 503 `degraded` with `database: error:…`. `/health` still 200.
- [ ] Kill scheduler goroutine or wait past 2× interval → readiness shows scheduler degraded.

## FLOW P6: Tax fallback metrics

- [ ] `curl -H "Authorization: Bearer $METRICS_TOKEN" /metrics | grep velox_tax_fallback_total` → counter registered.
- [ ] Reasons increment correctly: `no_country` (customer missing country), `no_client_for_mode` (one-mode tenant), `api_error` (invalid Stripe key).
- [ ] Happy path → counter unchanged.

---

## UI / UX

## FLOW U0: Quickstart wizard (TTFI)

- [ ] Fresh tenant lands on `/onboarding`.
- [ ] Step 1 Template: 5 recipe cards. Pick → preview → "Use this template" instantiates.
- [ ] Step 2 Stripe: connect tenant key inline.
- [ ] Step 3 Tax: pick `stripe_tax`/`manual`; tax-id field with FLOW B11 validation.
- [ ] Step 4 Branding: brand color + logo URL.
- [ ] Step 5 First test invoice: spawns demo customer + sub + ingests event + runs billing → opens hosted invoice. Total elapsed shown (target <30s).
- [ ] On finish: TTFI recorded; `/v1/billing/dashboard` shows `time_to_first_invoice_seconds`.
- [ ] Refresh mid-wizard → resumes on last incomplete step.
- [ ] "Skip" → marks `onboarding_skipped_at`, lands on dashboard. Re-run `/onboarding?force=true` is idempotent on tenant_id.

## FLOW U1: Dashboard

- [ ] 4 KPI cards: MRR (sparkline+trend), Active Customers, Failed Payments (red if >0), Revenue 30d.
- [ ] Revenue bar chart, Recent Activity (last 5 invoices clickable).
- [ ] Get Started checklist: 6 steps, auto-tracks against server state, self-hides at 100%. Dismiss persists per-tenant.
- [ ] No "Trigger Billing" button (use `POST /v1/billing/run`).

## FLOW U3: Usage Events page

- [ ] Stat cards: Total Events, Total Units, Active Meters, Active Customers.
- [ ] Meter breakdown bars.
- [ ] Filters: customer, date range. Stat cards stay constant when paging (reflect all filtered rows).
- [ ] Decimal precision: `0.5 + 0.5 + 0.0001` → `1.0001` (no rounding).
- [ ] Export CSV.

## FLOW U5: Dark mode

- [ ] Toggle in sidebar footer → sidebar/cards/tables/modals/forms/charts switch.
- [ ] Refresh → persists (`localStorage:velox-theme`). Delete key → follows system preference.

## FLOW U6: Responsive

- [ ] 768px tablet: tables scroll horizontally with fade. Sidebar → hamburger.
- [ ] Stat cards stack 2-col. Modals don't overflow.

## FLOW U7: Edge cases

| Case | Expected |
|------|----------|
| Zero usage | Base fee only |
| Meter without rating rule | Usage silently skipped |
| Duplicate idempotency key, same body | Cached response, 1 row |
| Duplicate idempotency key, different body | 409 |
| Invalid `external_customer_id` on ingest | "customer not found" |
| Invalid `event_name` on ingest | "meter not found" |
| Void already voided invoice | Error |
| Finalize non-draft invoice | Error |
| Duplicate subscription code | Humanized error |
| Cancel canceled subscription | Error |
| Esc from modal with form data | "Unsaved changes" prompt |

## FLOW U8: Request-ID in error toasts

- [ ] Force any API error → toast shows `Request ID: req_…` (clickable to copy).
- [ ] Even when response envelope fails to parse → Request-Id from `Velox-Request-Id` header still appears.
- [ ] `grep req_… server.log` matches the toast.

## FLOW U9: Typed destructive confirms

- [ ] Type `VOID` to confirm: void invoice, void credit note.
- [ ] Type `CANCEL`: cancel subscription (operator + customer portal).
- [ ] Type `DELETE`: delete webhook endpoint.
- [ ] Wrong word → confirm button disabled. Cancel always closes.

## FLOW U10: Public pages

- [ ] `/invoice/:token` (FLOW I10), `/update-payment` (FLOW D4), `/checkout/success`, `/login` all load without auth.

## FLOW U11: Report-an-issue

- [ ] Signed-in: account menu → "Report an issue" → mailto with `tenant_id`, current URL, user agent, latest `Velox-Request-Id`.
- [ ] Trigger failing request, click report → Request-Id in mailto matches the toast.
- [ ] Signed out `/login` "Trouble signing in?" → mailto with URL + UA (no Request-Id pre-auth).

---

# Tier 3 — Deep / Rare

Major releases, infra changes, post-mortems.

## FLOW X1: RLS multi-tenant isolation

- [ ] Bootstrap Tenant A + key A; create "Alpha Corp". Bootstrap Tenant B + key B; list customers with key B → Alpha NOT visible.
- [ ] `GET /v1/customers/{alpha_id}` with key B → 404.
- [ ] Same check for invoices, subs, credits — cross-tenant reads must 404.

## FLOW X2: Bootstrap lockdown

- [ ] No `VELOX_BOOTSTRAP_TOKEN` → POST /v1/bootstrap → 403 `bootstrap disabled`.
- [ ] Wrong token → 403 `invalid bootstrap token`.
- [ ] Correct token, tenants exist → 409 `bootstrap already completed`.
- [ ] `make bootstrap` CLI always works.

## FLOW X3: Rate limiting

- [ ] 100+ concurrent requests → first 100 ok, rest 429 with `Retry-After` + `X-RateLimit-*` headers.
- [ ] Wait 10s, 20 more → ~16 allowed (GCRA refill 1.67/sec).
- [ ] Tenant A exhausted → Tenant B succeeds (separate buckets).
- [ ] Stop Redis → requests succeed (fail-open in dev). `APP_ENV=production` → fail-closed.
- [ ] `/health`, `/health/ready`, `/metrics` not rate-limited.

## FLOW X4: Security headers + metrics auth

- [ ] `curl -I /v1/customers` carries: `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Cache-Control: no-store`, `Referrer-Policy: strict-origin-when-cross-origin`.
- [ ] Staging/prod: `Strict-Transport-Security` present.
- [ ] `METRICS_TOKEN=secret123` set → `/metrics` 401 unauth, 200 with Bearer. Unset → `/metrics` accessible (dev).

## FLOW X5: PII encryption at rest

- [ ] `VELOX_ENCRYPTION_KEY` set (64 hex). Create customer + billing profile.
- [ ] `SELECT display_name, email FROM customers …` → values prefixed `enc:`. API returns plaintext.
- [ ] `SELECT legal_name, email, phone, tax_id FROM customer_billing_profiles …` → all 4 prefixed `enc:`.
- [ ] Pre-encryption rows still read correctly (no `enc:` prefix → returned as-is).

## FLOW X6: Webhook replay

- [ ] Capture real Stripe payload + signature → replay 5+ min later → rejected (timestamp tolerance >300s).
- [ ] Modified payload + original signature → rejected.

## FLOW X7: Stripe Tax

- [ ] `PUT /v1/feature-flags/billing.stripe_tax {enabled:true}`. Customer with full address → invoice tax calculated by Stripe; `tax_name` shows jurisdiction; per-line tax populated.
- [ ] Invalid Stripe key → invoice generates with $0 tax (graceful fallback); counter `velox_tax_fallback_total{reason="api_error"}` +1.
- [ ] `tax_exempt=true` → $0 tax regardless.
- [ ] India-registered Stripe account → blocked at account level → use FLOW B10.

## FLOW X8: Migration rollback

- [ ] `make migrate-status` → version N. `migrate rollback` → N-1. `make migrate` → N.
- [ ] `docker compose down -v && make up && make dev` → fresh DB applies all migrations; status matches `ls migrations/*.up.sql | wc -l`.

## FLOW X9: Config validation

- [ ] No `VELOX_ENCRYPTION_KEY` in production → fatal.
- [ ] Key not 64 hex / not valid hex → fatal.
- [ ] `APP_ENV=production` no `REDIS_URL` → warn "rate limiting will fail open".
- [ ] `APP_ENV=foo` → warn listing expected values. `PORT=not-a-port` → warn.
- [ ] `DB_MAX_IDLE_CONNS > DB_MAX_OPEN_CONNS` → warn.
- [ ] All valid → zero WARN-level config logs.

## FLOW X10: OpenTelemetry tracing

```bash
docker run -d --name jaeger -p 16686:16686 -p 4318:4318 jaegertracing/jaeger:2
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 go run ./cmd/velox
```

- [ ] Hit several endpoints. Jaeger UI at :16686, service `velox`.
- [ ] HTTP spans (method+path), `billing.RunCycle` with `batch_size`, `billing.BillSubscription` with `subscription_id`/`tenant_id`.
- [ ] HTTP → billing parent-child relationship visible.

## FLOW X11: Large batch usage ingestion

- [ ] POST /v1/usage-events/batch with 1000 events → `{accepted:1000, rejected:0}`.
- [ ] Include duplicate idempotency keys → duplicates rejected.
- [ ] Run billing → aggregated correctly.

## FLOW X12: Operator CLI (`velox-cli`)

- [ ] `--help` lists `sub`, `invoice`, `--api-url`, `--api-key`.
- [ ] `velox-cli sub list` → table; `--status`, `--limit`, `--customer`, `--plan` filters work.
- [ ] `--output=json` → `{data:[…], total:N}`. Multi-item subs: PLAN col joins with `,`; JSON preserves array.
- [ ] Missing/wrong key → exit 1 with stderr message.
- [ ] `invoice send <inv>` → 202; outbox row appears. Unknown id → 404.

## FLOW X14: Self-host (Compose)

- [ ] `docker compose up -d postgres redis mailpit` → 3 sidecars healthy.
- [ ] `make bootstrap` + `make dev` + `cd web-v2 && npm run dev`. `/health` and `/health/ready` 200.
- [ ] `RUN_MIGRATIONS_ON_BOOT=true` (default) applies all migrations idempotently.
- [ ] Mail catches at `localhost:8025`.

---

# Diagnostics

## Server won't start
- `VELOX_ENCRYPTION_KEY` rejected → FLOW X9 (must be 64 hex chars).
- Postgres unreachable → `docker compose ps`, `make up`.
- Port 8080 in use → `lsof -i :8080`, kill stale.
- Migration dirty → resolve SQL error, then `migrate force <version>`.

## Sign-in fails
- 401 → wrong creds. Re-bootstrap or use password-reset.
- 429 → 5 wrong attempts, 15-min lockout. Check `users.locked_until`.
- CORS: `CORS_ALLOWED_ORIGINS` must include frontend origin.
- Cookie not set → check `Set-Cookie` on response. `Secure` in dev should be off.
- Cookie present but every request 401s → check `dashboard_sessions.expires_at` / `revoked_at`.

## Invoice didn't generate
- Subscription not due → period end in future. Backdate for testing.
- Already billed → FLOW B3 (idempotent skip).
- Subscription paused / customer archived / trial active → no billing.
- `billing.auto_charge` off → invoice generated but not charged.

## Stripe Tax errors
- Unsupported home country → FLOW B10 manual fallback.
- Missing customer address → counter `velox_tax_fallback_total{reason="no_country"}` +1.
- Tenant in disconnected mode → `{reason="no_client_for_mode"}`.
- Invalid key → `{reason="api_error"}`.

## Rate limit not triggering
- Redis not connected → `redis-cli ping`, check `REDIS_URL`.
- Sequential curl too slow — use parallel.
- Public endpoints (`/health`, `/metrics`, `/v1/bootstrap`) intentionally unrestricted.

## PII not encrypted
- `VELOX_ENCRYPTION_KEY` not set when row created → backward-compat plaintext (FLOW X5).
- Wrong field — only customer display_name/email + billing profile legal_name/email/phone/tax_id are encrypted.

## Webhook signature fails
- Wrong `whsec_…` after `stripe listen` restart (CLI rotates per run).
- Clock skew >5 min → rejected (FLOW X6).
- Wrong webhook URL — must be `/v1/webhooks/stripe/<vlx_spc_…>`.
