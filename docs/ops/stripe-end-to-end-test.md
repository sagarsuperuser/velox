# Stripe end-to-end test runbook

How to manually verify Velox's Stripe integration against a real Stripe test account.
Designed to be run before each design-partner cutover and after any change touching
`internal/payment/`, `internal/tenantstripe/`, or `internal/dunning/`.

**Audience:** Velox maintainer, design-partner technical contact during sandbox cutover.

**Prerequisite:** A Stripe **test-mode** account with at least Restricted Key / Standard
secret + publishable keys minted. The keys never leave the dashboard's connection
form once entered — Velox encrypts them at rest with the same AES-256-GCM key used
for customer email. **Never use live keys for this runbook.**

---

## Step 0 — Stand up Velox locally

```bash
docker compose up -d postgres redis mailpit
DATABASE_URL="postgres://velox:velox@localhost:5432/velox?sslmode=disable" \
  RUN_MIGRATIONS_ON_BOOT=true \
  go run ./cmd/velox-bootstrap   # only on first boot — creates owner + tenant
DATABASE_URL="postgres://velox:velox@localhost:5432/velox?sslmode=disable" \
  RUN_MIGRATIONS_ON_BOOT=true \
  go run ./cmd/velox &
```

Confirm the API is up: `curl -sf http://localhost:8080/health` returns
`{"status":"ok"}`.

Login as the owner the bootstrap step printed and capture cookies for the curl
calls below:

```bash
curl -s -X POST http://localhost:8080/v1/auth/login \
  -H 'Content-Type: application/json' \
  -c /tmp/velox-cookies.txt \
  -d '{"email":"<OWNER_EMAIL>","password":"<OWNER_PASSWORD>"}'
```

---

## Step 1 — Connect the Stripe test account

The connection form encrypts secret keys at rest (AES-256-GCM, same key as customer
email). **Do not paste keys into shell history or commit them.** Use a `umask 077`
tmp file and delete after:

```bash
umask 077 && cat > /tmp/stripe-connect.json <<'EOF'
{
  "livemode": false,
  "secret_key": "sk_test_...",
  "publishable_key": "pk_test_..."
}
EOF
curl -s -X POST http://localhost:8080/v1/settings/stripe \
  -b /tmp/velox-cookies.txt \
  -H 'Content-Type: application/json' \
  -d @/tmp/stripe-connect.json
rm -f /tmp/stripe-connect.json
```

**Expected response (200):**

```json
{
  "id": "vlx_spc_...",
  "tenant_id": "vlx_ten_...",
  "livemode": false,
  "stripe_account_id": "acct_...",
  "stripe_account_name": "<your account display name>",
  "secret_key_prefix": "sk_test_...",
  "secret_key_last4": "...",
  "verified_at": "2026-04-27T..."
}
```

**What this proves:** Velox round-tripped a `GET /v1/account` against the supplied
secret key (`internal/tenantstripe/service.go` `Connect()` → `sc.V1Accounts.Retrieve`)
and persisted the encrypted keys. A `verified_at` timestamp means the key shape
+ scope are valid and Stripe acknowledged the request.

If the response carries `last_verified_error`, the key is stored but Stripe
rejected the verify call. Common causes: live key in test mode, restricted key
without `read_only` scope on Account.

---

## Step 2 — Create a customer with a saved payment method

PaymentIntent flows require a saved payment method. The Velox dashboard handles
this via Stripe's Setup Intent flow, but the runbook below uses the API directly
with one of Stripe's pre-built test payment methods so the flow doesn't require
a browser.

### 2a. Create the Velox customer

```bash
curl -s -X POST http://localhost:8080/v1/customers \
  -b /tmp/velox-cookies.txt \
  -H 'Content-Type: application/json' \
  -d '{
    "display_name": "Stripe E2E Smoke",
    "email": "e2e-smoke@velox.test",
    "external_id": "stripe-e2e-smoke-1"
  }'
# → captures vlx_cus_...
```

### 2b. Attach a Stripe test card via the dashboard UI

Open `http://localhost:5173/customers/<vlx_cus_...>` in a browser, click
**Payment methods → Add card**, paste Stripe's standard test card:

- Card: `4242 4242 4242 4242`
- Expiry: any future date
- CVC: any 3 digits
- Postal code: any 5 digits

The dashboard mints a Setup Intent against Stripe via
`internal/payment/checkout_handler.go`, the user confirms in Stripe Elements,
and the resulting `pm_*` payment method ID lands on the Velox customer's
`stripe_payment_method_id` column.

**Expected:** customer detail shows the card with last4=4242, brand=visa, status=valid.

### 2c. Failed-card variant (run after the happy path)

Repeat 2b with `4000 0000 0000 0002` (Stripe's "card declined" test). Setup
Intent should succeed (card is valid for SI) but the eventual PaymentIntent
in step 4 will fail and trigger the dunning flow.

---

## Step 3 — Subscribe the customer to a flat plan

Pick the bootstrap-seeded `Starter` plan (or instantiate a recipe — see the
recipes test below):

```bash
PLAN_ID=$(curl -s http://localhost:8080/v1/plans -b /tmp/velox-cookies.txt \
  | jq -r '.data[] | select(.code=="starter").id')

curl -s -X POST http://localhost:8080/v1/subscriptions \
  -b /tmp/velox-cookies.txt \
  -H 'Content-Type: application/json' \
  -d "{
    \"customer_id\": \"<vlx_cus_...>\",
    \"plan_id\": \"$PLAN_ID\",
    \"start_at\": \"now\"
  }"
```

**Expected:** `vlx_sub_...` returned with `status=active`, current period bounds set
to monthly (anchor: now).

---

## Step 4 — Trigger the immediate invoice + PaymentIntent

The flat-fee charge for the first cycle should fire the moment the subscription
activates. Confirm:

```bash
curl -s "http://localhost:8080/v1/invoices?customer_id=<vlx_cus_...>" \
  -b /tmp/velox-cookies.txt | jq '.data[0]'
```

**Expected:** an invoice with `status=open` (or `paid` if the auto-finalize
window has passed), `amount_due_cents=2900`, and `stripe_payment_intent_id`
populated. The PaymentIntent ID also shows up on the Stripe dashboard under
**Test data → Payments**.

If the customer has the `4242` card from step 2b, the PaymentIntent should
auto-confirm and the invoice flips to `status=paid` within ~5 seconds.

---

## Step 5 — Webhook delivery from Stripe → Velox

Velox's webhook ingestion (signed via `STRIPE_WEBHOOK_SECRET` per
`internal/payment/handler.go`) verifies signatures and stores the raw event
in `webhook_event` keyed on `(tenant_id, stripe_event_id)` for idempotency.

For local testing without exposing port 8080 to the public internet:

```bash
brew install stripe/stripe-cli/stripe   # one-time
stripe listen --api-key $STRIPE_SK \
  --forward-to http://localhost:8080/v1/webhooks/stripe
```

The CLI will print a temporary webhook secret. Paste it into the Velox dashboard
under **Settings → Stripe → Webhook secret** (or via
`PATCH /v1/settings/stripe/test/webhook`). All subsequent test-mode events flow
through the CLI tunnel.

**Expected:** every Stripe event for steps 2–4 (`payment_method.attached`,
`payment_intent.created`, `payment_intent.succeeded`, `charge.succeeded`) lands
in `webhook_event` with `processed=true` and is visible at
`/webhook_events` in the dashboard.

---

## Step 6 — Failed payment + dunning

Re-run step 3 against the customer from step 2c (declined card). The
PaymentIntent fails and dunning kicks in:

```bash
curl -s "http://localhost:8080/v1/dunning/attempts?customer_id=<vlx_cus_...>" \
  -b /tmp/velox-cookies.txt | jq
```

**Expected:** the default dunning policy schedules retries (Day 1, Day 3, Day 7,
Day 14, then `final_action=cancel`). The first retry should already be
scheduled. Each retry is a fresh PaymentIntent; check the Stripe dashboard's
Payments page to confirm the PI ID changes per attempt.

To recover the customer, swap the card via step 2b with `4242` and either
(a) wait for the next dunning retry, or (b) call
`POST /v1/dunning/attempts/<id>/retry` to force one.

---

## Step 7 — Refund via credit note

Issue a credit note against the paid invoice from step 4:

```bash
curl -s -X POST http://localhost:8080/v1/credit-notes \
  -b /tmp/velox-cookies.txt \
  -H 'Content-Type: application/json' \
  -d '{
    "invoice_id": "<vlx_inv_...>",
    "reason": "duplicate",
    "amount_cents": 2900
  }'
```

**Expected:** credit note created with `status=issued`. Velox calls
`refunds.Create` against Stripe, the refund lands on the same charge, and a
`charge.refunded` webhook flows back. The invoice's `amount_refunded_cents`
should equal `2900` post-refund.

---

## Step 8 — Disconnect

```bash
curl -s -X DELETE http://localhost:8080/v1/settings/stripe/test \
  -b /tmp/velox-cookies.txt
```

**Expected:** 204. The credentials row is deleted; the encrypted blobs are
purged. Subsequent payment attempts return a `stripe_credentials_missing`
error envelope rather than panicking.

---

## What to capture in your test report

A passing run reports:

- Stripe account name + ID returned from step 1 verify (proof the key works
  and is scoped to the intended account)
- Webhook event count from step 5 (≥4 events expected for the happy path)
- Final invoice status from step 4 (`paid` for happy, `open` after recovery
  in the dunning path)
- Refund webhook landed within 30 seconds (step 7)

Anything that doesn't match the **Expected** sections above is a bug. File it
under the Velox repo with the request ID Velox returns in the error envelope
(see `docs/ops/runbook.md#error-envelope` for the shape).

---

## What this runbook does NOT cover

- **Stripe Connect / Express accounts** — Velox is a self-host product, not a
  marketplace. Connect onboarding is out of scope.
- **Live mode** — explicitly forbidden in this runbook. Live testing belongs in
  the design-partner cutover playbook
  (`docs/design-partner-onboarding.md` → Cutover Week section).
- **Tax** — Stripe Tax integration is exercised separately; see
  `docs/ops/tax-calculation.md`.
- **Payouts / balance** — Velox does not surface Stripe payouts; the operator's
  Stripe dashboard is canonical for that.

---

## Last verified

| Date | Velox SHA | Stripe API version | Verified by | Result |
|---|---|---|---|---|
| 2026-04-27 | `f1b2301` | `2025-08-27.basil` (default) | maintainer | Step 1 (connect + verify) ✅; steps 2–8 documented as runbook, full flow validated during sandbox cutover for first design partner |

Re-run and append a row after every change touching `internal/payment/`,
`internal/tenantstripe/`, or `internal/dunning/`.
