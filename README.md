# Velox

Open-source usage-based billing engine. Built in Go.

[![CI](https://github.com/sagarsuperuser/velox/actions/workflows/ci.yml/badge.svg)](https://github.com/sagarsuperuser/velox/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go)](https://go.dev)
[![License](https://img.shields.io/badge/License-MIT-green)](LICENSE)

Velox handles the complete billing lifecycle: pricing configuration, subscription management, usage metering, invoice generation with PDF rendering, payment collection via Stripe, customer credits, and automated dunning for failed payments.

**136 Go files | 18,200+ lines | 324 tests | 76 API endpoints | 8-page React dashboard**

## Quick Start

```bash
git clone https://github.com/sagarsuperuser/velox.git && cd velox

# Backend
docker compose up -d postgres
make bootstrap    # Creates demo tenant + API keys
make dev          # Starts API server on :8080

# Frontend (new terminal)
cd web-v2 && npm install && npm run dev
# → http://localhost:5173 (paste your API key to login)
```

Or run the full billing cycle via CLI:
```bash
./scripts/demo.sh <YOUR_SECRET_KEY>
```

## Dashboard Screenshots

The operator dashboard provides real-time views of your billing data:

- **Dashboard** — KPI cards (customers, subscriptions, revenue) + recent invoices + active subscriptions
- **Customers** — searchable table with status badges, click-through to detail
- **Customer Detail** — credit balance, active subscriptions, recent invoices
- **Invoices** — status + payment badges, billing period, amount, PDF download
- **Invoice Detail** — line items breakdown (base fee, usage charges) with pricing mode
- **Subscriptions** — billing period tracking, status management

## API Examples

**Create a customer:**
```bash
curl -X POST http://localhost:8080/v1/customers \
  -H "Authorization: Bearer vlx_secret_..." \
  -H "Content-Type: application/json" \
  -d '{"external_id": "acme", "display_name": "Acme Corp", "email": "billing@acme.com"}'
```

```json
{
  "id": "vlx_cus_42014438d82ddf33b20f3dd5",
  "external_id": "acme",
  "display_name": "Acme Corp",
  "email": "billing@acme.com",
  "status": "active",
  "created_at": "2026-04-07T10:33:15Z"
}
```

**Ingest usage:**
```bash
curl -X POST http://localhost:8080/v1/usage-events \
  -H "Authorization: Bearer vlx_secret_..." \
  -d '{"customer_id": "vlx_cus_...", "meter_id": "vlx_mtr_...", "quantity": 500}'
```

**Trigger billing → Generate invoice:**
```bash
curl -X POST http://localhost:8080/v1/billing/run \
  -H "Authorization: Bearer vlx_secret_..."
```

```json
{"invoices_generated": 1, "errors": []}
```

**Error responses (Stripe-consistent format):**
```json
{
  "error": {
    "type": "invalid_request_error",
    "code": "not_found",
    "message": "customer not found",
    "request_id": "abc123"
  }
}
```

## Architecture

```
cmd/velox/                  — Single binary, 85 lines
internal/
  domain/                   — Pure domain models, zero dependencies
  api/respond/              — Unified Stripe-style JSON responses
  auth/                     — API key auth (3 key types, 16 permissions)
  tenant/                   — Tenant management + settings
  customer/                 — Customer CRUD + billing profiles
  pricing/                  — Meters, rating rules, plans, price overrides
  subscription/             — Lifecycle (draft → active → paused → canceled)
  usage/                    — Event ingestion, batch API, aggregation
  invoice/                  — State machine (draft → finalized → paid) + PDF
  billing/                  — Billing cycle engine + scheduler + preview
  payment/                  — Stripe PaymentIntent + webhook handling
  dunning/                  — Payment retry state machine
  credit/                   — Event-sourced prepaid balance ledger
  creditnote/               — Credit notes + refunds
  webhook/                  — Outbound webhooks (HMAC-signed delivery)
  audit/                    — Immutable append-only audit log
  platform/postgres/        — RLS-aware database layer
  platform/migrate/         — Embedded SQL migrations

web-v2/                     — Operator dashboard (React + TypeScript + shadcn/ui)
  src/pages/                — Dashboard, Customers, Invoices, Subscriptions, etc.
  src/components/           — Shared components (Layout, Badge, StatCard)
  src/lib/api.ts            — Typed API client with auth
```

**Key design decisions:**
- **Per-domain packages** — each domain owns its store, service, and handler. Zero cross-domain imports between peer packages.
- **Row-Level Security** — every tenant-scoped query runs inside an RLS-enforced transaction. Proven by integration tests.
- **PaymentIntent-only Stripe** — no Stripe Billing/Invoices (avoids 0.5% fee). We own invoices, Stripe handles payment execution.
- **Billing engine as coordinator** — orchestrates across domains via narrow interfaces, not a god object.
- **Event-sourced credits** — immutable append-only ledger, balance computed from entries.
- **HMAC-signed webhooks** — both inbound (Stripe) and outbound (tenant endpoints).

## Billing Cycle

```
Subscription (active) → Usage Events → Billing Engine → Invoice (draft)
    → Finalize → Stripe PaymentIntent → Webhook → Invoice (paid)
    → Payment fails → Dunning (retry schedule → escalation)
```

The billing engine:
1. Finds subscriptions where `next_billing_at <= now`
2. Checks for per-customer price overrides
3. Aggregates usage per meter for the billing period
4. Computes charges using rating rules (flat, graduated, or package)
5. Generates a draft invoice with itemized line items
6. Advances the subscription to the next billing period

## Performance

Pricing engine benchmarks (Apple M2, zero allocations):

```
BenchmarkComputeAmountCents_Flat              283M     4.2 ns/op    0 B/op    0 allocs/op
BenchmarkComputeAmountCents_Graduated_5Tiers  137M     8.7 ns/op    0 B/op    0 allocs/op
BenchmarkComputeAmountCents_Package           269M     4.5 ns/op    0 B/op    0 allocs/op
```

The pricing engine computes ~150M prices per second per core.

## Features

| Feature | Description |
|---|---|
| **3 Pricing Models** | Flat, graduated (tiered), package (bundled + overage) |
| **Per-Customer Overrides** | Custom pricing per customer per rating rule |
| **Trial Periods** | Configurable trial days, billing starts after trial |
| **Plan Changes** | Upgrade/downgrade with proration factor |
| **Pause/Resume** | Pause subscriptions without canceling |
| **Customer Credits** | Event-sourced prepaid balance, auto-applied before Stripe charge |
| **Credit Notes** | Issue against invoices (credit or refund) |
| **PDF Invoices** | Professional A4 PDF rendering with line items |
| **Invoice Preview** | Dry-run billing without persisting |
| **Batch Ingestion** | Up to 1000 usage events per request |
| **Dunning** | Configurable retry schedule, escalation, manual resolution |
| **Outbound Webhooks** | HMAC-signed delivery to tenant endpoints, 13 event types |
| **Audit Log** | Immutable log of all sensitive operations |
| **API Key Auth** | 3 key types (platform/secret/publishable), 16 permissions |
| **Idempotency Keys** | Prevent duplicate operations on retries |
| **Rate Limiting** | Token bucket with Stripe-convention headers |
| **RLS Isolation** | PostgreSQL Row-Level Security per tenant |
| **MRR/ARR Dashboard** | Real-time billing metrics |

## API Key Types

| Key Type | Prefix | Permissions |
|---|---|---|
| Platform | `vlx_platform_` | Tenant management only (2 permissions) |
| Secret | `vlx_secret_` | Full tenant access (14 permissions) |
| Publishable | `vlx_pub_` | Usage ingestion, customer CRUD, reads (6 permissions) |

## API Endpoints (68)

<details>
<summary>Click to expand full endpoint list</summary>

```
GET    /health                              — Health check
GET    /health/ready                        — Deep health (DB connectivity)

POST   /v1/tenants                          — Create tenant
GET    /v1/tenants                          — List tenants
GET    /v1/tenants/{id}                     — Get tenant

POST   /v1/api-keys                         — Create API key
GET    /v1/api-keys                         — List API keys
DELETE /v1/api-keys/{id}                    — Revoke API key

POST   /v1/customers                        — Create customer
GET    /v1/customers                        — List customers
GET    /v1/customers/{id}                   — Get customer
PATCH  /v1/customers/{id}                   — Update customer
PUT    /v1/customers/{id}/billing-profile   — Upsert billing profile
GET    /v1/customers/{id}/billing-profile   — Get billing profile

POST   /v1/meters                           — Create meter
GET    /v1/meters                           — List meters
GET    /v1/meters/{id}                      — Get meter

POST   /v1/plans                            — Create plan
GET    /v1/plans                            — List plans
GET    /v1/plans/{id}                       — Get plan
PATCH  /v1/plans/{id}                       — Update plan

POST   /v1/rating-rules                     — Create rating rule
GET    /v1/rating-rules                     — List rating rules
GET    /v1/rating-rules/{id}               — Get rating rule

POST   /v1/price-overrides                  — Create/update price override
GET    /v1/price-overrides                  — List price overrides

POST   /v1/subscriptions                    — Create subscription
GET    /v1/subscriptions                    — List subscriptions
GET    /v1/subscriptions/{id}              — Get subscription
POST   /v1/subscriptions/{id}/activate     — Activate draft subscription
POST   /v1/subscriptions/{id}/pause        — Pause subscription
POST   /v1/subscriptions/{id}/resume       — Resume subscription
POST   /v1/subscriptions/{id}/change-plan  — Change plan (upgrade/downgrade)
POST   /v1/subscriptions/{id}/cancel       — Cancel subscription

POST   /v1/usage-events                     — Ingest single usage event
POST   /v1/usage-events/batch              — Batch ingest (up to 1000)
GET    /v1/usage-events                     — List usage events
GET    /v1/usage-summary/{customer_id}     — Aggregated usage summary

GET    /v1/invoices                         — List invoices
GET    /v1/invoices/{id}                   — Get invoice + line items
GET    /v1/invoices/{id}/pdf               — Download invoice PDF
POST   /v1/invoices/{id}/finalize          — Finalize draft invoice
POST   /v1/invoices/{id}/void             — Void invoice

POST   /v1/billing/run                     — Trigger billing cycle
GET    /v1/billing/preview/{sub_id}        — Invoice preview (dry run)

POST   /v1/credit-notes                    — Create credit note
GET    /v1/credit-notes                    — List credit notes
GET    /v1/credit-notes/{id}              — Get credit note
POST   /v1/credit-notes/{id}/issue        — Issue credit note
POST   /v1/credit-notes/{id}/void         — Void credit note

POST   /v1/credits/grant                   — Grant customer credits
POST   /v1/credits/adjust                  — Manual credit adjustment
GET    /v1/credits/balance/{customer_id}   — Get credit balance
GET    /v1/credits/ledger/{customer_id}    — Full credit ledger

GET    /v1/dunning/policy                  — Get dunning policy
PUT    /v1/dunning/policy                  — Configure dunning policy
GET    /v1/dunning/runs                    — List dunning runs
GET    /v1/dunning/runs/{id}              — Get dunning run + events
POST   /v1/dunning/runs/{id}/resolve      — Resolve dunning run

POST   /v1/webhook-endpoints/endpoints    — Register webhook endpoint
GET    /v1/webhook-endpoints/endpoints    — List webhook endpoints
DELETE /v1/webhook-endpoints/endpoints/{id} — Delete endpoint
GET    /v1/webhook-endpoints/events       — List webhook events
GET    /v1/webhook-endpoints/events/{id}/deliveries — Delivery attempts

GET    /v1/settings                        — Get tenant settings
PUT    /v1/settings                        — Update tenant settings

GET    /v1/audit-log                       — Query audit log

POST   /v1/webhooks/stripe                 — Stripe webhook receiver
```

</details>

## Development

```bash
make test               # Unit tests
make test-integration   # Integration tests (requires Postgres)
make dev                # Run server locally
make bootstrap          # Create demo tenant + API keys
```

## Tech Stack

**Backend:**
- **Go 1.25** — API server (chi/v5 router)
- **PostgreSQL 16** — System of record with Row-Level Security
- **Stripe** — Payment execution (PaymentIntents + Checkout Sessions)
- **go-pdf/fpdf** — Invoice PDF rendering
- **Prometheus** — Metrics endpoint

**Frontend:**
- **React 19** + **TypeScript** — Operator dashboard
- **Vite** — Build tooling
- **TailwindCSS** — Styling
- **Lucide** — Icons

## License

MIT
