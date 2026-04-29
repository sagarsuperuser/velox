# Velox — Claude Code Context

## What is this?
Velox is an open-source usage-based billing engine built in Go. It handles pricing, subscriptions, usage metering, invoice generation, Stripe payments, dunning, and customer credits.

## Architecture
- Per-domain packages in `internal/` — each domain owns store, service, handler
- Zero cross-domain imports between peer packages
- Billing engine coordinates via narrow interfaces
- PostgreSQL with Row-Level Security for tenant isolation
- chi/v5 for HTTP routing

## Key patterns
- Store interfaces per domain (not a god Repository)
- RLS via `db.BeginTx(ctx, postgres.TxTenant, tenantID)`
- API key auth with 3 types: platform, secret, publishable
- HMAC-SHA256 webhook signing (both inbound Stripe and outbound)

## Running locally
```bash
docker compose up -d postgres
DATABASE_URL="postgres://velox:velox@localhost:5432/velox?sslmode=disable" go run ./cmd/velox-bootstrap
DATABASE_URL="postgres://velox:velox@localhost:5432/velox?sslmode=disable" RUN_MIGRATIONS_ON_BOOT=true go run ./cmd/velox
```

## Testing
```bash
go test ./... -short          # unit tests only
go test -p 1 ./... -short=false  # includes integration tests (needs postgres)
```

## Important decisions
- Auth: the API key is the durable credential. Dashboard operator pastes a `vlx_secret_…` key into `/login`; backend `POST /v1/auth/exchange` validates the key and mints an httpOnly `velox_session` cookie tied to that key. Subsequent dashboard requests ride the cookie via `credentials: 'include'`. SDK / curl callers send `Authorization: Bearer <key>` and skip the cookie path entirely; `internal/session.MiddlewareOrAPIKey` accepts either, with cookie taking precedence so stale Bearer headers can't bypass session revocation. No user accounts, no password reset, no invitations in v1. See `docs/adr/007-revert-to-api-key-dashboard-auth.md` (revert) and `docs/adr/008-session-from-api-key.md` (httpOnly cookie refinement).
- Stripe: PaymentIntent-only pattern (no Stripe Billing/Invoices to avoid 0.5% fee)
- No Temporal dependency in v1 — simple background goroutine scheduler. Redis used for distributed rate limiting only.
- Credits use event-sourced ledger (immutable append-only)
