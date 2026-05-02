# Customer Portal API

The `/v1/me/*` surface is the customer self-service API. It lives
behind a portal session (magic-link issued via `POST /v1/public/customer-portal/magic-link`,
exchanged via `POST /v1/public/customer-portal/magic/consume`). The
session token authenticates `/v1/me/*` requests as a specific
`(tenant_id, customer_id)` pair — every endpoint scopes to that
identity automatically.

DPs typically embed these endpoints into their own product's
customer dashboard. A standalone Velox-hosted portal page is on the
roadmap (Sprint 1.5).

## Authentication

```
Authorization: Bearer <portal_session_token>
```

The token is returned by `POST /v1/public/customer-portal/magic/consume`.
Sessions are 1-hour by default. Renew by re-requesting a magic link.

## Endpoints

### Invoices

#### `GET /v1/me/invoices`

List the current customer's invoices. Filterable by status; paginated.

Query params: `status`, `payment_status`, `limit`, `offset`.

```jsonc
{
  "data": [
    {
      "id": "vlx_inv_...",
      "invoice_number": "DEMO-000123",
      "status": "finalized",
      "payment_status": "paid",
      "currency": "USD",
      "total_amount_cents": 4900,
      "amount_due_cents": 0,
      "amount_paid_cents": 4900,
      "issued_at": "2026-05-01T...",
      "paid_at": "2026-05-01T...",
      "public_token": "abc..."  // for hosted invoice URL
    }
  ],
  "total": 12
}
```

#### `GET /v1/me/invoices/{id}/pdf`

Download invoice as PDF. Returns `application/pdf` with
`Content-Disposition: attachment` for browser-driven download.

The endpoint validates the invoice belongs to the session's customer;
returns 404 (not 403) on cross-customer attempts to avoid leaking
existence.

### Subscriptions

#### `GET /v1/me/subscriptions`

List the current customer's subscriptions, hydrated with items + plans.

```jsonc
{
  "data": [
    {
      "id": "vlx_sub_...",
      "status": "active",
      "code": "pro-monthly",
      "display_name": "Pro Monthly",
      "items": [{"plan_id": "vlx_pln_...", "quantity": 1}],
      "current_billing_period_end": "2026-06-01T...",
      "next_billing_at": "2026-06-01T..."
    }
  ],
  "total": 1
}
```

#### `POST /v1/me/subscriptions/{id}/cancel`

Cancel the subscription immediately. The customer's portal session
must own the subscription; cross-customer attempts return 404.

Fires a `subscription.canceled` webhook with
`canceled_by: "customer"` so DPs can distinguish customer-initiated
cancels from operator-initiated ones.

Body: empty.

Response: the canceled subscription object.

### Payment method

#### `POST /v1/me/payment-method/update`

Start a Stripe Checkout (setup mode) flow to collect a new payment
method. Returns a URL the customer redirects to.

```jsonc
// Request (body optional)
{
  "return_url": "https://app.tenant.com/billing?updated=true"
}

// Response
{
  "url": "https://checkout.stripe.com/c/pay/cs_test_..."
}
```

**Flow**:

1. Customer clicks "Update payment method" in the DP's UI.
2. DP calls `POST /v1/me/payment-method/update`.
3. DP redirects the customer to the returned `url`.
4. Customer enters new card details on Stripe Checkout.
5. Stripe redirects back to `return_url`.
6. Stripe webhook fires; Velox updates `PaymentSetup` → ready.
7. Engine's `RetryPendingCharges` scheduler auto-collects any
   `auto_charge_pending` invoices on its next sweep (Chargebee's
   "Collect Invoice on Card Update" — operator does nothing).

**Errors**:

- `400 missing_payment_setup`: customer has no Stripe customer
  record yet. DP must run the initial setup flow (operator-driven
  `POST /v1/checkout/setup`) before the customer can update.
- `503 stripe_unavailable`: tenant has no Stripe configuration for
  the active livemode. DP needs to configure Stripe credentials.

### Branding

#### `GET /v1/me/branding`

Returns the tenant's branding for portal-page rendering: company
name, logo URL, brand color, support URL. Used by DPs that embed the
portal API in their own UI to match Velox's email branding.

```jsonc
{
  "company_name": "Acme Corp",
  "logo_url": "https://...",
  "brand_color": "#1a1a1a",
  "support_url": "https://acme.com/support"
}
```

## Public endpoints (no portal session)

### `POST /v1/public/customer-portal/magic-link`

Request a magic link. Always returns 202 regardless of whether the
email matches a customer (no enumeration oracle).

```jsonc
{ "email": "alice@example.com" }
```

The customer receives an email with a tokenized link. The DP's
portal frontend handles the consume step.

### `POST /v1/public/customer-portal/magic/consume`

Exchange a single-use magic-link token for a portal session.

```jsonc
// Request
{ "token": "raw_magic_token_from_email" }

// Response
{
  "token": "session_token_for_bearer_auth",
  "customer_id": "vlx_cus_...",
  "livemode": false,
  "expires_at": "2026-05-02T..."
}
```

Use the returned `token` as `Authorization: Bearer <token>` for all
`/v1/me/*` calls until `expires_at`.

## Operator surface (Bearer key, not portal session)

For operator-driven actions on behalf of a customer (admin tools, CS
flows), use the operator API:

- `POST /v1/portal/{customerID}/update-payment-method` — operator
  initiates a PM update Checkout session. Same Stripe flow as the
  customer-self-serve `/v1/me/payment-method/update` endpoint, but
  authenticated with a Bearer key.

These endpoints accept a Bearer API key and require the operator to
specify the `customer_id` in the URL. The portal endpoints above
infer it from session ctx.

## Security notes

- **No tenant ID in request bodies.** Every endpoint reads tenant
  from session ctx; passing a different tenant ID in the body is
  ignored.
- **Cross-customer access returns 404, not 403.** This avoids
  leaking that a resource exists for a different customer.
- **Magic-link tokens are single-use.** Replaying a consumed token
  fails with the same generic 401 as an unknown token.
- **Session tokens are 1-hour by default.** No refresh — request a
  new magic link to extend.
- **Rate-limited.** Both magic-link request and `/v1/me/*` traffic
  go through the standard rate limiter; per-tenant quotas apply.

## What's not in the portal API yet

These flows exist on the operator surface but are not exposed to
customers. Add as a DP requests:

- Subscription upgrade / downgrade (operator-driven via plan-change).
- Customer profile self-edit (name, billing address).
- Coupon application by customer (operator-applied today).
- Usage breakdown for the current cycle (per-meter charts).

When a DP needs these, prioritise based on their support-team
overhead — these are the next operator-cost reductions.
