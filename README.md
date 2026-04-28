# Velox

### Turn usage into revenue.

**The open-source billing engine for AI and usage-heavy SaaS — runs in your own VPC.**

[![CI](https://github.com/sagarsuperuser/velox/actions/workflows/ci.yml/badge.svg)](https://github.com/sagarsuperuser/velox/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go)](https://go.dev)
[![License](https://img.shields.io/badge/License-MIT-green)](LICENSE)

Velox owns the layer above PaymentIntent: pricing, subscriptions, multi-dimensional usage metering, invoicing, dunning, and credits. Stripe still does cards under the hood. The 0.5% Stripe Billing fee disappears, and customer billing data never leaves your infrastructure.

Built for two market truths Stripe Billing structurally cannot serve:

1. **AI/LLM apps need multi-dimensional pricing.** Real model pricing today is `model × operation × cached × tier × context-window`. Stripe's Meter API forces one Meter per dimension combination — six Meters and ugly subscription wiring just to model GPT-4 input/output/cached/uncached. Velox treats dimensions as first-class on the meter.
2. **Regulated tenants cannot put customer billing data on Stripe's servers.** EU GDPR-strict, India RBI data localization, healthcare-adjacent SaaS, government procurement. Stripe's whole model is "send us the data." Velox runs in your VPC.

---

## The wedge in code

Bill Anthropic-style multi-dimensional pricing (model × operation × cached) with **one meter** and a few pricing rules — not 24 Stripe Meters and a Subscription Item per combination.

```bash
# 1. Create one meter for "tokens"
curl -X POST https://api.velox.dev/v1/meters \
  -H "Authorization: Bearer $VELOX_SECRET" \
  -d '{"key": "tokens", "name": "LLM tokens", "unit": "token"}'

# 2. Ingest events that carry the dimensions inline
curl -X POST https://api.velox.dev/v1/usage-events \
  -H "Authorization: Bearer $VELOX_SECRET" \
  -H "Idempotency-Key: req_8f2c..." \
  -d '{
    "event_name": "tokens",
    "external_customer_id": "cust_acme",
    "quantity": "12450",
    "dimensions": {"model": "gpt-4", "operation": "input", "cached": false}
  }'

# 3. Define one pricing rule per (dimension subset, rate)
curl -X POST https://api.velox.dev/v1/meters/mtr_tokens/pricing-rules \
  -d '{
    "dimension_match": {"model": "gpt-4", "operation": "input", "cached": false},
    "rating_rule_version_id": "rrv_gpt4_input_uncached",
    "aggregation_mode": "sum",
    "priority": 100
  }'
```

A coarser rule (`{"model": "gpt-4"}`) plus a finer override (`{"model": "gpt-4", "cached": true}` at higher priority) compose cleanly — each event is claimed by the highest-priority matching rule, no double-count. The full design — schema, aggregation semantics, decimal quantities, all five aggregation modes (`sum`, `count`, `last_during_period`, `last_ever`, `max`) — lives in [`docs/design-multi-dim-meters.md`](docs/design-multi-dim-meters.md).

---

## What's in the box

### AI/usage-native (the wedge)
- **Multi-dimensional meters** — one meter, N rules, dimensions on every event
- **Decimal quantities** — `NUMERIC(38, 12)` for fractional GPU-hours and partial tokens
- **Per-rule aggregation modes** — `sum`, `count`, `last_during_period`, `last_ever`, `max`
- **Pricing recipes** — one call instantiates products + prices + meters + dunning (`anthropic_style`, `openai_style`, `replicate_style`, `b2b_saas_pro`, `marketplace_gmv`)
- **Embeddable cost dashboard** — drop `<VeloxCostDashboard customerId={…} />` into your app and end users see "$4.31 of GPT-4 today" with a projected bill

### Self-host first
- **Helm chart, Docker Compose, Terraform** — runs in your VPC in ≤1 hour
- **Data sovereignty** — customer billing data never leaves your infrastructure
- **Append-only audit log** — DB-trigger enforced tamper-evidence
- **Row-Level Security** — one Velox deployment cleanly serves N internal tenants

### Stripe-grade primitives (already shipped)
- **Subscriptions** — trial state machine with atomic flips · pause-collection · scheduled cancellation · plan changes with proration · per-customer price overrides
- **Invoicing & collection** — PDF invoices · hosted invoice page with secure tokens · branded multipart emails · dunning with breaker
- **Credits & refunds** — event-sourced credit ledger · credit notes + refunds
- **Reliability** — idempotency · transactional outbox · webhook signing with 72h rotation grace · test clocks

See [`CHANGELOG.md`](CHANGELOG.md) for the full ship log.

---

## How it fits

|                          | **Velox** | Stripe Billing | Lago        | Orb / Metronome   | OpenMeter        |
|--------------------------|-----------|----------------|-------------|-------------------|------------------|
| OSS / self-host          | ✅        | ❌             | ✅          | ❌                | ✅               |
| AI-native pricing        | ✅        | ❌             | ⚠️ generic  | ⚠️ closed source  | ⚠️ metering only |
| Full billing engine      | ✅        | ✅             | ✅          | ✅                | ❌               |
| Stripe-grade primitives  | ✅        | ✅             | ⚠️          | ✅                | ❌               |
| Pricing                  | OSS       | 0.5% of GMV    | OSS / cloud | $30K+/yr          | OSS              |
| Data sovereignty         | ✅        | ❌             | ⚠️          | ❌                | ✅ (no billing)  |

Velox lives in the empty cell: **OSS + self-host + AI-native + full billing engine.** Decision tree: pick **Stripe Billing** for vanilla per-seat SaaS; pick **Lago** for generic OSS metering without an AI-shaped wedge; pick **Orb/Metronome** if you can't self-host and budget for usage-based contracts; pick **Velox** when you need all four.

---

## What Velox is **not**

Stating these loudly so the wrong customers self-select out:

- **Not for vanilla card-first SaaS** with simple per-seat pricing — Stripe Billing is fine for them.
- **Not multi-PSP yet** — Stripe is the only payment processor. Razorpay/Adyen come when a paying tenant asks.
- **Not for marketplaces or Stripe Connect** — Velox bills the tenant's customers directly; it doesn't split payouts across sub-merchants. (Velox itself is multi-tenant via RLS — many billing tenants per deployment — but each tenant collects on its own behalf, not on behalf of others.)
- **No Revenue Recognition / Sigma** — bring your own warehouse + dbt.
- **No Quotes or Subscription Schedules** — sales-led contract billing should pick Recurly or Maxio.
- **No 50+ payment-method types** — cards via Stripe + send-invoice. ACH/SEPA expand from there.

---

## Quick start

```bash
git clone https://github.com/sagarsuperuser/velox.git && cd velox

# Backend — Postgres + bootstrap demo tenant + run the API
docker compose up -d postgres
make bootstrap       # creates a demo tenant and prints API keys
make dev             # API on :8080

# Operator dashboard (separate terminal)
cd web-v2 && npm install && npm run dev
# → http://localhost:5173 — paste your secret key to log in
```

End-to-end demo (creates a customer, ingests usage, runs billing, generates a PDF invoice):

```bash
./scripts/demo.sh $VELOX_SECRET
```

Five-minute self-host (Helm chart + Terraform module): under construction. Postgres backup playbook is already in [`docs/self-host/postgres-backup.md`](docs/self-host/postgres-backup.md).

---

## Migrating from Stripe Billing

Already running on Stripe Billing? `velox-import` reads via Stripe's
restricted-key API, writes via `DATABASE_URL`, and rebuilds an entire
tenant's customers / products / prices / subscriptions / finalized
invoices in a single CLI run. Per-row outcomes (`insert` /
`skip-equivalent` / `skip-divergent` / `error`) are written to a CSV
report so the same input rerun produces only `skip-equivalent` rows —
safe to invoke nightly during a parallel-run cutover.

```bash
velox-import \
  --api-key=rk_live_…             \  # Stripe restricted key (read-only)
  --tenant=ten_…                  \  # Velox tenant id
  --resource=customers,products,prices,subscriptions,invoices \
  --since=2024-01-01
```

The full operator playbook — pre-migration checklist, rehearsal run,
T-14 → T+14 parallel-run cutover, reconciliation toolkit, webhook
redirection, rollback procedure, and known limitations — lives in
[`docs/migration-from-stripe.md`](docs/migration-from-stripe.md).

---

## Architecture

```
cmd/velox/                  — single Go binary
internal/
  domain/                   — pure domain models, zero deps
  api/respond/              — Stripe-style JSON responses
  auth/                     — API key auth (3 key types, 16 permissions)
  tenant/                   — tenants + settings
  customer/                 — customer CRUD + billing profiles
  pricing/                  — meters, rating rules, plans, price overrides
  subscription/             — lifecycle (draft → trialing → active → paused → canceled)
  usage/                    — event ingestion + multi-dim aggregation
  invoice/                  — state machine (draft → finalized → paid) + PDF
  billing/                  — billing engine + scheduler + preview
  payment/                  — Stripe PaymentIntent + webhook receiver
  dunning/                  — payment retry state machine
  credit/                   — event-sourced prepaid balance ledger
  creditnote/               — credit notes + refunds
  webhook/                  — outbound webhooks (HMAC-signed delivery)
  audit/                    — immutable append-only audit log
  platform/postgres/        — RLS-aware database layer
  platform/migrate/         — embedded SQL migrations

web-v2/                     — operator dashboard (React 19 + TypeScript + Tailwind)
```

Design rules:
- **Per-domain packages** — each domain owns its store, service, and handler. Zero cross-domain imports between peer packages.
- **Row-Level Security** — every tenant-scoped query runs inside an RLS-enforced transaction. Proven by integration tests.
- **PaymentIntent-only Stripe** — no Stripe Billing/Invoices. We own invoices end-to-end; Stripe executes the card charge.
- **Billing engine as coordinator** — orchestrates across domains via narrow interfaces, not a god object.
- **Append-only event sourcing for money** — credits, audit log, outbound webhook outbox.

ADRs explaining the load-bearing decisions live in [`docs/adr/`](docs/adr/).

---

## API surface (selected)

```
POST   /v1/usage-events                 — ingest with dimensions + decimal value
POST   /v1/usage-events/batch           — batch ingest, up to 1000 per call
POST   /v1/meters/{id}/pricing-rules    — add a dimension-matched pricing rule
GET    /v1/customers/{id}/usage         — period aggregation, grouped by dimension

POST   /v1/customers                    — create customer
POST   /v1/subscriptions                — create subscription
POST   /v1/subscriptions/{id}/change-plan        — plan change w/ proration
POST   /v1/subscriptions/{id}/pause-collection   — keep cycle, invoice as draft
POST   /v1/subscriptions/{id}/extend-trial       — push trial_end_at later

POST   /v1/billing/run                  — finalize all due cycles
GET    /v1/billing/preview/{sub_id}     — invoice preview (dry run)

POST   /v1/credit-notes                 — issue credit note (credit or refund)
POST   /v1/credits/grant                — grant prepaid credits to a customer
GET    /v1/credits/balance/{customer_id} — current balance + ledger

GET    /v1/dunning/runs                 — list dunning runs
POST   /v1/webhook-endpoints/endpoints  — register an outbound webhook endpoint
GET    /v1/audit-log                    — query the append-only audit log
```

Full endpoint list and request/response shapes: [`api/openapi.yaml`](api/openapi.yaml).

---

## API key types

| Type        | Prefix            | What it can do                                           |
|-------------|-------------------|----------------------------------------------------------|
| Platform    | `vlx_platform_`   | Tenant management only                                   |
| Secret      | `vlx_secret_`     | Full tenant access (server-side)                         |
| Publishable | `vlx_pub_`        | Usage ingestion + customer-bound reads (browser-safe)    |

All keys are HMAC-rotated on a 72-hour overlap window, matching Stripe's webhook-signature pattern.

---

## Roadmap

### Recently shipped

- **Multi-dimensional meters** — one meter, N pricing rules, decimal quantities (`NUMERIC(38, 12)`), all five aggregation modes
- **Pricing recipes** — `anthropic_style`, `openai_style`, `replicate_style`, `b2b_saas_pro`, `marketplace_gmv`; recipe-picker UI; one-click uninstall
- **Quickstart wizard** — sign-up → first invoice in under five minutes
- **`create_preview`, billing thresholds, billing alerts** — Stripe Tier-1 surfaces with multi-dim parity
- **Embeddable cost dashboard** — `<VeloxCostDashboard customerId={…} />` plus token-authenticated public URL
- **Plan-migration UI** — preview, batch apply, history; bulk operations on customers/subscriptions/invoices
- **Live event stream + invoice composer** — operator UX for ingestion debugging and one-off invoice creation
- **`velox-import`** — Stripe Billing → Velox cutover with idempotent reruns and reconciliation CSV; full operator playbook in [`docs/migration-from-stripe.md`](docs/migration-from-stripe.md)

See [`CHANGELOG.md`](CHANGELOG.md) for the full ship log.

### In flight

- **Self-host packaging** — Helm chart + Terraform module (Postgres backup runbook already in [`docs/self-host/postgres-backup.md`](docs/self-host/postgres-backup.md))
- **Compliance posture** — encryption-at-rest hardening, SOC 2 control mapping, GDPR export coverage
- **First design-partner cutover** to Velox in production

---

## Tech stack

**Backend** — Go 1.25, chi/v5 router, PostgreSQL 16 with RLS, `shopspring/decimal` for money, `go-pdf/fpdf` for invoices, Prometheus metrics.

**Frontend** — React 19, TypeScript, Vite, TailwindCSS, shadcn/ui, Lucide icons.

**Payments** — Stripe (PaymentIntents + Checkout Sessions). No Stripe Billing dependency.

---

## Running tests

```bash
make test                # unit tests only
make test-integration    # full integration suite (needs Postgres)
```

Integration tests exercise real Postgres with RLS enforced. Per project convention, tests must hit a real database — never mocks — so a passing test means migrations and RLS work end-to-end.

---

## Contributing

Velox is open source under MIT. We're early and looking for design partners (12 months free hosted access in exchange for weekly check-ins and a co-branded case study). If you run AI inference, a vector DB, or any usage-heavy SaaS at $1M–$50M ARR and Stripe Billing is starting to chafe — open an issue or email `partners@velox.dev`.

For code contributions, see [`CONTRIBUTING.md`](CONTRIBUTING.md). Major features land with a design RFC alongside the code — read any `docs/design-*.md` for the pattern.

---

## License

[MIT](LICENSE)
