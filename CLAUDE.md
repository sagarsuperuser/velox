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
- Auth: dashboard uses email + password; API uses Bearer keys. Dashboard `POST /v1/auth/login` validates against `users.password_hash` (bcrypt cost 12) and mints an httpOnly `velox_session` cookie bound to `users.id` — not to any API key. SDK / curl callers send `Authorization: Bearer <vlx_…>`; `internal/session.MiddlewareOrAPIKey` accepts either, cookie taking precedence. Password reset uses single-use 1h tokens delivered via SMTP (Mailpit in local dev — `docker compose up -d mailpit`). No multi-user invites or 2FA in v1. See `docs/adr/011-email-password-auth-and-clean-api-keys.md`; ADR-007 and ADR-008 are superseded.
- Email: single delivery path. `Sender` returns `ErrSMTPNotConfigured` when `SMTP_HOST` is unset — no stdout fallback. Boot logs WARN once per missing env (`SMTP_HOST`, `CUSTOMER_PORTAL_URL`, `PAYMENT_UPDATE_URL`); the producer always wires the real adapter.
- Stripe: PaymentIntent-only pattern (no Stripe Billing/Invoices to avoid 0.5% fee)
- No Temporal dependency in v1 — simple background goroutine scheduler. Redis used for distributed rate limiting only.
- Credits use event-sourced ledger (immutable append-only)

## Documentation discipline

Every user-visible ship updates the docs that describe it, in the same PR:

- `CHANGELOG.md` (Keep-a-Changelog) + `web-v2/src/pages/Changelog.tsx` (Linear-style) — what shipped, dated.
- `MANUAL_TEST.md` — add or revise the matching FLOW so the assertions still match observable behavior. If a flow lies, future-you can't run it; that's the rot trigger we already paid for once. Trimmed shape (post-2026-05-02): one observable per checkbox, no preamble prose, drop DB introspection unless it's the actual assertion. Stale flows = delete or rewrite, not leave-and-document.
- `docs/adr/` if the change is a decision worth re-litigating later.
- `README.md` "Recently shipped" / "In flight" sections — keep aligned with reality; if a "Roadmap" item is silently descoped, edit the README first, then act.

The bar isn't "perfect docs." The bar is "the doc doesn't lie." A flow that says "logs link to stdout" when the code returns an error is worse than no flow.
