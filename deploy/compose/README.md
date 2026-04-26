# Velox self-host on a single VM (Docker Compose)

A 5-minute path from a fresh VM to a working Velox tenant. Three
containers behind one nginx: postgres, velox-api, nginx. Migrations run
on first boot.

## Prerequisites

- Docker 24+ with the Compose plugin (`docker compose version` works)
- Open inbound TCP/80 if you want the API reachable from outside the VM
- ~1 vCPU and 1 GB RAM is enough for evaluation; production sizing in
  [`docs/self-host.md`](../../docs/self-host.md)

## 1. Clone and configure

```bash
git clone https://github.com/sagarsuperuser/velox.git
cd velox/deploy/compose
cp .env.example .env
```

Edit `.env` and set the three required secrets:

```bash
# Postgres password — pick a long random string
POSTGRES_PASSWORD=$(openssl rand -hex 24)

# 64 hex chars (32 bytes) for PII encryption-at-rest
VELOX_ENCRYPTION_KEY=$(openssl rand -hex 32)

# Authorises POST /v1/bootstrap to create the first tenant
VELOX_BOOTSTRAP_TOKEN=$(openssl rand -hex 32)
```

(Or generate them in-shell and paste in.) Everything else in `.env` is
optional — the binary boots without SMTP, Stripe webhook secrets, or
Redis. See `.env.example` for the full list.

## 2. Bring the stack up

```bash
docker compose up -d
```

First boot does three things you'll see in the logs:

1. `postgres` initialises the `velox` database and creates the
   `velox_app` runtime role (see `postgres-init.sql`).
2. `velox-api` starts with `RUN_MIGRATIONS_ON_BOOT=true`, applies all
   pending migrations from `internal/platform/migrate/sql/`, then begins
   serving on `:8080` and starts the in-process scheduler.
3. `nginx` proxies host `:80` to `velox-api:8080`.

Tail the logs while it converges:

```bash
docker compose logs -f velox-api
```

You're done when you see `"listening" addr=:8080`.

## 3. Health check

```bash
curl -fsS http://localhost/health
# {"status":"ok"}

curl -fsS http://localhost/health/ready
# {"checks":{"api":"ok","database":"ok","scheduler":"..."},"status":"ok"}
```

`/health` is liveness (process up). `/health/ready` is readiness
(database reachable + scheduler not stalled). Wire your load balancer's
health check to `/health/ready`.

## 4. Create your first tenant

The bootstrap endpoint is gated by the token you set in `.env`:

```bash
curl -X POST http://localhost/v1/bootstrap \
  -H "Authorization: Bearer ${VELOX_BOOTSTRAP_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"tenant_name":"Acme","owner_email":"you@example.com","owner_password":"a-strong-password"}'
```

The response carries your first secret API key
(`vlx_secret_test_…`). Save it — Velox stores only the SHA-256 hash, so
the plaintext is never recoverable. Use that key against `/v1/*` from
here on.

## 5. Verify

```bash
SECRET=vlx_secret_test_...
curl -fsS http://localhost/v1/customers \
  -H "Authorization: Bearer ${SECRET}"
# {"data":[],"has_more":false}
```

You're live.

## Operating the stack

| Action | Command |
|---|---|
| View logs | `docker compose logs -f` |
| Restart the API | `docker compose restart velox-api` |
| Update to a new image tag | edit `VELOX_IMAGE` in `.env`, then `docker compose pull && docker compose up -d` |
| Stop everything (preserve data) | `docker compose stop` |
| Stop and wipe data | `docker compose down -v` |
| Run a one-off migration command | `docker compose run --rm velox-api migrate status` |
| Open a psql shell | `docker compose exec postgres psql -U velox -d velox` |

## TLS

This stack listens on plain HTTP. For production add TLS one of two
ways:

- **Managed load balancer in front** — AWS ALB, Cloudflare, GCP HTTPS LB.
  Terminate TLS there, target this VM's port 80 over private network.
- **certbot on the host** — install nginx on the host (not inside the
  compose stack), terminate TLS there, proxy to `localhost:80`. Or run
  certbot in a sidecar container and bind-mount certificates into the
  nginx container.

Either way, set `APP_ENV=production` (default) so secure-cookie and
HSTS protections are on.

## Backups

Once data matters, follow
[`docs/self-host/postgres-backup.md`](../../docs/self-host/postgres-backup.md)
for a `pg_basebackup` + WAL-archive recipe and a tested restore drill.

## Troubleshooting

**`velox-api` exits with `VELOX_ENCRYPTION_KEY is required in production`** —
set the key in `.env` (64 hex chars, generate with `openssl rand -hex 32`)
and `docker compose up -d` again. Production refuses to start without it
to avoid silently storing PII in plaintext.

**`velox-api` logs `running with admin database connection — RLS NOT enforced`** —
the `velox_app` role wasn't created. The init SQL in
`postgres-init.sql` only runs on a fresh `pgdata` volume. If you're
upgrading an existing volume, run the script manually:

```bash
docker compose exec -T postgres psql -U velox -d velox < postgres-init.sql
```

**`/health/ready` returns 503 with `scheduler: degraded`** — the
scheduler tick window has elapsed without a recorded run. Usually means
the API process is alive but the leader-locked work isn't progressing.
Check the API logs and the `pg_locks` table.

**Port 80 is already in use** — set `NGINX_HTTP_PORT=8080` (or any free
port) in `.env` and bring the stack back up.

## What's next

- [`docs/self-host.md`](../../docs/self-host.md) — top-level self-host landing
- [`docs/self-host/postgres-backup.md`](../../docs/self-host/postgres-backup.md) — backup + restore drill
- Helm chart for Kubernetes — coming soon (Week 9 follow-up lane)
- Terraform module for AWS VPC — coming soon (Week 9 follow-up lane)
