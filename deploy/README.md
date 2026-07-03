# Velox Deployment

One install shape ships in this directory. The canonical landing page is
[`docs/self-host.md`](../docs/self-host.md).

| Path | Use when |
|---|---|
| [`compose/`](compose/) | Single-VM eval, dev/staging, low-volume production. Reference deploy. |

Kubernetes (Helm) and Terraform paths are deliberately not shipped in
v1 — they land when a design partner names the flavour they actually
run (see the "Operational posture" section of `docs/self-host.md`).
Pre-emptively shipping three deployment paths produced surface nobody
was running.

## Local development

```bash
docker compose up -d postgres
DATABASE_URL="postgres://velox:velox@localhost:5432/velox?sslmode=disable" go run ./cmd/velox-bootstrap
DATABASE_URL="postgres://velox:velox@localhost:5432/velox?sslmode=disable" RUN_MIGRATIONS_ON_BOOT=true go run ./cmd/velox
```

Or run the whole stack in Docker:

```bash
docker build -t velox:local .        # from the repo root
cd deploy/compose
cp .env.example .env
$EDITOR .env   # set POSTGRES_PASSWORD, VELOX_APP_DB_PASSWORD, VELOX_ENCRYPTION_KEY, VELOX_BOOTSTRAP_TOKEN
VELOX_IMAGE=velox:local docker compose up -d
```

## Building the Docker image

```bash
docker build -t velox:latest .
docker run --rm -e DATABASE_URL="..." -p 8080:8080 velox:latest
```

## Required env vars

The compose-level schema is [`compose/.env.example`](compose/.env.example);
the full binary schema is the repo-root [`.env.example`](../.env.example),
which mirrors the binary's actual reads from `internal/config/config.go`
plus the per-package `os.Getenv` callsites. Mandatory in a production
install:

| Variable | Why |
|---|---|
| `POSTGRES_PASSWORD` | Admin/migration role credentials (compose builds `DATABASE_URL` from it). |
| `VELOX_APP_DB_PASSWORD` | Password for the least-privilege `velox_app` runtime role (compose builds `APP_DATABASE_URL` from it). Production refuses to boot with the default `velox_app` password. |
| `VELOX_ENCRYPTION_KEY` | 64 hex chars (32 bytes). Production refuses to start without it (`config.validateFatal`). |
| `VELOX_BOOTSTRAP_TOKEN` | Authorises the one-shot `POST /v1/bootstrap` that creates the first tenant. |

Per-tenant Stripe API keys live in the database (see migration 0032),
not in env vars. The optional `STRIPE_WEBHOOK_SECRET` env var only
gates inbound Stripe webhook signature verification.

## Health checks

| Endpoint | Purpose |
|---|---|
| `GET /health` | Liveness — returns 200 if the process is running. |
| `GET /health/ready` | Readiness — returns 200 if DB is reachable AND the scheduler is healthy. |

Wire your load balancer / ingress / probes to `/health/ready`. Both
endpoints are exempt from rate limiting and audit logging.

## Scaling

- **Horizontal:** Multi-replica is safe — schedulers and outbox
  dispatchers are leader-elected via Postgres advisory locks
  (`internal/billing/postgres_locker.go`), so replicas coexist without
  double-processing. The reference compose stack is single-replica;
  the multi-replica deployment path is paused until a design partner
  needs it.
- **Database:** Velox uses connection pooling (`DB_MAX_OPEN_CONNS`,
  default 20). When scaling replicas, ensure total connections across
  all instances don't exceed your PostgreSQL `max_connections`.
- **Migrations:** Only one instance should run migrations per rollout.
  `RUN_MIGRATIONS_ON_BOOT=true` is safe under races (appliers serialize
  on an advisory lock and re-check applied state under it), but a
  dedicated migration step before rollout is still the cleaner shape.
