# Velox

Open-source usage-based billing engine. Built in Go.

Velox handles the complete billing lifecycle: pricing configuration, subscription management, usage metering, invoice generation, payment collection via Stripe, and automated dunning for failed payments.

## Architecture

```
cmd/velox/main.go           — Single binary, 95 lines
internal/
  domain/                   — Pure domain models (tenant, customer, pricing, invoice, etc.)
  auth/                     — API key auth (3 key types, permission middleware)
  tenant/                   — Tenant management
  customer/                 — Customer CRUD + billing profiles
  pricing/                  — Meters, rating rules, plans
  subscription/             — Subscription lifecycle (draft → active → canceled)
  usage/                    — Usage event ingestion + aggregation
  invoice/                  — Invoice state machine + PDF rendering
  billing/                  — Billing cycle engine + scheduler
  payment/                  — Stripe PaymentIntent + webhook handling
  dunning/                  — Payment retry state machine
  platform/postgres/        — RLS-aware database layer
  platform/migrate/         — Embedded SQL migrations
  errs/                     — Structured domain errors
  config/                   — Environment configuration
```

**Key design decisions:**
- Per-domain packages — each domain owns its store, service, and handler. Zero cross-domain imports.
- Row-Level Security — every tenant-scoped query runs inside an RLS-enforced transaction.
- PaymentIntent-only Stripe integration — no Stripe Billing/Invoices (avoids 0.5% fee). We own invoices, Stripe handles payment execution.
- Billing engine as coordinator — orchestrates across domains via narrow interfaces, not a god object.

## Quick Start

```bash
# Start Postgres + Velox
docker compose up -d

# Health check
curl http://localhost:8080/health
```

## API Key Types

| Key Type | Prefix | Access |
|---|---|---|
| Platform | `vlx_platform_` | Tenant management only |
| Secret | `vlx_secret_` | Full tenant access (14 permissions) |
| Publishable | `vlx_pub_` | Restricted — usage ingestion, customer CRUD, reads only |

## API Endpoints

```
GET    /health

# Platform (vlx_platform_ key)
POST   /v1/tenants
GET    /v1/tenants
GET    /v1/tenants/{id}

# Tenant-scoped (vlx_secret_ or vlx_pub_ key)
POST   /v1/api-keys
GET    /v1/api-keys
DELETE /v1/api-keys/{id}

POST   /v1/customers
GET    /v1/customers
GET    /v1/customers/{id}
PATCH  /v1/customers/{id}
PUT    /v1/customers/{id}/billing-profile
GET    /v1/customers/{id}/billing-profile

POST   /v1/meters
GET    /v1/meters
GET    /v1/meters/{id}

POST   /v1/plans
GET    /v1/plans
GET    /v1/plans/{id}
PATCH  /v1/plans/{id}

POST   /v1/rating-rules
GET    /v1/rating-rules
GET    /v1/rating-rules/{id}

POST   /v1/subscriptions
GET    /v1/subscriptions
GET    /v1/subscriptions/{id}
POST   /v1/subscriptions/{id}/activate
POST   /v1/subscriptions/{id}/cancel

POST   /v1/usage-events
GET    /v1/usage-events

GET    /v1/invoices
GET    /v1/invoices/{id}
GET    /v1/invoices/{id}/pdf
POST   /v1/invoices/{id}/finalize
POST   /v1/invoices/{id}/void

GET    /v1/dunning/policy
PUT    /v1/dunning/policy
GET    /v1/dunning/runs
GET    /v1/dunning/runs/{id}
POST   /v1/dunning/runs/{id}/resolve

POST   /v1/webhooks/stripe
```

## Billing Cycle

The billing engine runs automatically (1h in production, 5m in local dev):

1. Finds subscriptions where `next_billing_at <= now`
2. Aggregates usage per meter for the billing period
3. Computes charges using rating rules (flat, graduated, or package pricing)
4. Generates a draft invoice with line items
5. Advances the subscription to the next billing period

```
Subscription (active) → Usage Events → Billing Engine → Invoice (draft)
    → Finalize → Stripe PaymentIntent → Webhook → Invoice (paid)
    → Payment fails → Dunning (retry schedule → escalation)
```

## Pricing Models

| Mode | How it works | Example |
|---|---|---|
| Flat | Fixed amount per billing period regardless of usage | $25/mo for storage |
| Graduated | Tiered unit pricing (first N at $X, next M at $Y) | API calls: $0.10/call up to 1000, $0.05 after |
| Package | Fixed price per package, overage per unit | $20 per 1000 emails, $0.03 per extra |

## Development

```bash
# Run tests (unit only)
make test

# Run with local Postgres
docker compose up -d postgres
make dev

# Run all tests including integration (requires Postgres)
go test -p 1 ./... -count=1 -short=false
```

## Environment Variables

| Variable | Default | Description |
|---|---|---|
| `PORT` | `8080` | HTTP server port |
| `APP_ENV` | `local` | Environment (local, staging, production) |
| `DATABASE_URL` | — | PostgreSQL connection string |
| `RUN_MIGRATIONS_ON_BOOT` | `false` | Auto-run migrations on startup |
| `STRIPE_WEBHOOK_SECRET` | — | Stripe webhook signing secret |

## Tech Stack

- **Go 1.25** — API server
- **PostgreSQL 16** — System of record with Row-Level Security
- **chi/v5** — HTTP router
- **Stripe** — Payment execution (PaymentIntents only)
- **go-pdf/fpdf** — Invoice PDF rendering

## License

MIT
