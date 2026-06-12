# Velox — Self-host

Velox runs as a single Go binary against Postgres. The supported deployment
shape today is Docker Compose on a single VM. A managed-Kubernetes path
(Helm chart, multi-replica HA, Terraform-as-IaC) is not in v1; it lands
when a design partner names which Kubernetes flavour they actually run.

## Compose path

```bash
git clone https://github.com/sagarsuperuser/velox.git
cd velox

docker compose up -d postgres
VELOX_OWNER_EMAIL=you@example.com VELOX_OWNER_PASSWORD=change-me-please \
  make bootstrap
make dev
```

That gives you:

- `postgres` on `:5432` (volume-backed, password `velox`)
- `redis` on `:6379` (used by the rate limiter)
- `mailpit` on `:1025` SMTP / `:8025` web UI (catches outbound transactional mail)
- `velox-api` on `:8080`

The dashboard:

```bash
cd web-v2 && npm install && npm run dev
# → http://localhost:5173
```

## Operational posture

This deployment shape is a **single-VM, single-instance** install:

- API: 1 `velox-api` process. Restart and you have downtime until it
  comes back up.
- DB: 1 Postgres instance on the same host (or a managed Postgres if you
  point `DATABASE_URL` elsewhere).
- Scheduler: in-process goroutine inside `velox-api` (per ADR-006). One
  scheduler at a time; safe because there's only one API process.
- LB: none.

This is appropriate for: development, evaluation, single-tenant
self-hosting where ~minutes of downtime per deploy/restart is acceptable.
It is **not** a production-with-availability shape — for that, the next
step is a multi-replica deployment with leader-elected scheduling and
managed Postgres. That work is paused until a design partner with a
specific Kubernetes flavour comes through; pre-emptively shipping three
independent deployment paths produced surface nobody was running.

## Postgres

Compose ships Postgres 16 with default settings — sufficient for eval.
For your own VM:

- Version: 16.x
- Extensions: none required (Velox uses standard `gen_random_bytes`,
  `LATERAL`, RLS — all built-in).
- **`velox_app` role (required for tenant isolation).** Velox enforces
  multi-tenant isolation with Row-Level Security, which only applies to a
  **non-owner** role. At request time it connects as `velox_app`, derived from
  `DATABASE_URL` by swapping the credentials to `velox_app`/`velox_app`. The
  compose path creates this role automatically
  ([`deploy/compose/postgres-init.sql`](../deploy/compose/postgres-init.sql));
  on your own Postgres you must create it:

  ```sql
  CREATE ROLE velox_app WITH LOGIN PASSWORD 'velox_app';
  GRANT velox_app TO <the role in your DATABASE_URL>;
  -- migrations GRANT the needed table privileges to velox_app
  ```

  **With `APP_ENV=staging` or `production`, Velox refuses to start** if it
  can't open the `velox_app` pool — running as the table owner would bypass
  RLS and leak data across tenants. (In `local` it warns and continues, since
  a single-tenant dev box often uses one superuser URL.)
- Backups: take a `pg_dump` snapshot on whatever cadence your data loss
  tolerance allows. Stripe's webhook outbox + Velox's audit log are the
  two surfaces where lost rows are most expensive; both are covered by a
  consistent snapshot.

## Migrations

`RUN_MIGRATIONS_ON_BOOT=true` (default for `make dev`) runs forward
migrations on startup. Migrations are versioned and idempotent
([`internal/platform/migrate/sql/`](../internal/platform/migrate/sql/)).
Down-migrations exist for development reversal but production rollbacks
are forward-only.

## Environment

Required:

| Var | Purpose |
|---|---|
| `DATABASE_URL` | Postgres DSN |
| `VELOX_OWNER_EMAIL` | Bootstrap dashboard owner |
| `VELOX_OWNER_PASSWORD` | Bootstrap dashboard owner |

Optional:

| Var | Default | Purpose |
|---|---|---|
| `RUN_MIGRATIONS_ON_BOOT` | `false` | Run migrations on startup |
| `APP_ENV` | `local` | `local`/`staging`/`production`. Gates the cookie `Secure` flag and RLS fail-closed boot — `staging`/`production` refuse to start without the `velox_app` role (see Postgres above) |
| `DASHBOARD_BASE_URL` | _(unset)_ | Canonical dashboard origin for password-reset links. **Unset disables password-reset emails** — the origin is never derived from request headers (host-header poisoning). Set to e.g. `http://localhost:5173` in dev |
| `SMTP_HOST` / `SMTP_PORT` | _(unset)_ | Outbound email relay. Unset → emails are not sent (`ErrSMTPNotConfigured`). The compose path points these at mailpit (`localhost:1025`) |

Stripe is configured per-tenant via the dashboard (`POST /v1/settings/stripe`), not env vars — each tenant connects their own Stripe account.

## Scaling considerations

Single-replica is fine to ~tens of millions of usage events per month
on a 4-vCPU VM with 8 GB RAM. Beyond that the bottleneck is usually the
per-cycle aggregation scan; Postgres tuning (`work_mem`,
`shared_buffers`, an index review on `usage_events(tenant_id,
ingested_at)`) gets you another order of magnitude.

The ceiling on single-replica is the in-process scheduler — when one API
process can no longer keep up with the cycle scan plus webhook delivery,
that's the trigger for the multi-replica work currently paused.

## Observability

Velox exposes Prometheus metrics on `/metrics` and structured logs to
stdout. Hook these into whatever stack you already run; the v1 install
does not ship a Grafana / Prometheus / Alertmanager bundle (deferred —
local dev observability is `tail -f` on the API logs).

Key metrics to watch:

- `velox_billing_cycle_run_duration_seconds` — cycle scan latency
- `velox_tax_outcome_total{outcome,reason}` — tax-provider failure modes
- `velox_audit_write_errors_total` — audit log write failures
- `velox_stripe_webhook_in_total{result}` — inbound webhook outcomes

## Related

- [docs/ops/tax-calculation.md](./ops/tax-calculation.md) — tax
  providers and their failure handling
- [docs/ops/stripe-end-to-end-test.md](./ops/stripe-end-to-end-test.md) —
  manual end-to-end Stripe smoke test
- [docs/adr/](./adr/) — architecture decisions worth knowing about
  (PaymentIntent-only, RLS multi-tenancy, in-process scheduler)
