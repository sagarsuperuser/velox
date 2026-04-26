# Velox Deployment

Three install shapes ship in this directory; pick by what your team
already operates. The canonical landing page is
[`docs/self-host.md`](../docs/self-host.md).

| Path | Use when |
|---|---|
| [`compose/`](compose/) | Single-VM eval, dev/staging, low-volume production. Reference deploy. |
| [`helm/velox/`](helm/velox/) | You already operate Kubernetes (kind / k3s / EKS / GKE / AKS). |
| [`terraform/aws/`](terraform/aws/) | One-shot AWS install (VPC + EC2 + RDS + S3) — wraps the Compose stack. |

Each path's README is the source of truth for that shape; what
follows is just signposting.

## Local development

```bash
docker compose up -d postgres
DATABASE_URL="postgres://velox:velox@localhost:5432/velox?sslmode=disable" go run ./cmd/velox-bootstrap
DATABASE_URL="postgres://velox:velox@localhost:5432/velox?sslmode=disable" RUN_MIGRATIONS_ON_BOOT=true go run ./cmd/velox
```

Or run the whole stack in Docker:

```bash
cd deploy/compose
cp .env.example .env
$EDITOR .env   # set POSTGRES_PASSWORD, VELOX_ENCRYPTION_KEY, VELOX_BOOTSTRAP_TOKEN
docker compose up -d
```

## Building the Docker image

```bash
docker build -t velox:latest .
docker run --rm -e DATABASE_URL="..." -p 8080:8080 velox:latest
```

## Helm — quick install

```bash
helm install velox deploy/helm/velox \
  --namespace velox --create-namespace \
  --set secrets.encryptionKey=$(openssl rand -hex 32) \
  --set secrets.bootstrapToken=$(openssl rand -hex 32) \
  --set externalDatabase.url="postgres://velox:strong-pw@your-rds:5432/velox?sslmode=require"
```

See [`helm/velox/README.md`](helm/velox/README.md) for the full
values reference and ingress / autoscaling / external-secret options.

## Terraform — quick apply

```bash
cd deploy/terraform/aws
cp terraform.tfvars.example terraform.tfvars
$EDITOR terraform.tfvars   # set region, db_password, key_name, ssh_allowed_cidrs
terraform init
terraform apply
```

See [`terraform/aws/README.md`](terraform/aws/README.md) for the full
inputs / outputs reference, cost estimate, and the explicit
"what this does NOT do" list.

## Raw Kubernetes manifests

The [`k8s/`](k8s/) directory has bare manifests for users who don't
want Helm. They're maintained alongside the chart but the chart is
the supported install path; raw manifests will lag the chart on new
features.

## Required env vars

The full env-var schema is in
[`compose/.env.example`](compose/.env.example) and mirrors the
binary's actual reads from `internal/config/config.go` plus the
per-package `os.Getenv` callsites. Three keys are mandatory in a
production install:

| Variable | Why |
|---|---|
| `POSTGRES_PASSWORD` (Compose) / `DATABASE_URL` or split fields (Helm/Terraform) | Database credentials. |
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

- **Horizontal:** Multi-replica is safe — the v1 scheduler is leader-elected via Postgres advisory locks (`internal/billing/postgres_locker.go`), so multiple API replicas coexist without zombie locks. Helm chart's HPA template gates on `autoscaling.enabled`.
- **Vertical:** Adjust `resources.limits` / `resources.requests` in the chart values or the deployment manifest.
- **Database:** Velox uses connection pooling (`DB_MAX_OPEN_CONNS`, default 20). When scaling replicas, ensure total connections across all pods don't exceed your PostgreSQL `max_connections`. Consider PgBouncer for higher replica counts.
- **Migrations:** Only one pod should run migrations. Set `RUN_MIGRATIONS_ON_BOOT=true` and use a rolling update with `maxUnavailable: 0` so migrations complete on the first new pod before old pods terminate (this is the chart's default).
