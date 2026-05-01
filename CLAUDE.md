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
- Auth: dashboard uses email + password; API uses Bearer keys. Dashboard `POST /v1/auth/login` validates against `users.password_hash` (bcrypt cost 12) and mints an httpOnly `velox_session` cookie bound to `users.id` — not to any API key. SDK / curl callers send `Authorization: Bearer <vlx_…>`; `internal/session.MiddlewareOrAPIKey` accepts either, cookie taking precedence. Password reset uses single-use 1h tokens; SMTP delivery is deferred (link logged to stdout). No multi-user invites or 2FA in v1. See `docs/adr/011-email-password-auth-and-clean-api-keys.md`; ADR-007 and ADR-008 are superseded.
- Stripe: PaymentIntent-only pattern (no Stripe Billing/Invoices to avoid 0.5% fee)
- No Temporal dependency in v1 — simple background goroutine scheduler. Redis used for distributed rate limiting only.
- Credits use event-sourced ledger (immutable append-only)
