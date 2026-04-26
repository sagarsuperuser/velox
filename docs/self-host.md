# Self-hosting Velox

Velox is designed to run on your own infrastructure. Pick the install
shape that matches where you operate today.

## Pick a path

| Shape | Use when | Status |
|---|---|---|
| **Docker Compose on a single VM** | Evaluating, dev/staging, low-volume production (≤ 1k events/sec) | Ships today — [`deploy/compose/README.md`](../deploy/compose/README.md) |
| **Helm chart on Kubernetes** | You already run K8s and want to scale horizontally | Ships today — [`deploy/helm/velox/README.md`](../deploy/helm/velox/README.md) |
| **Terraform module for AWS** | You want a one-shot VPC + EC2 + RDS deploy | Ships today — [`deploy/terraform/aws/README.md`](../deploy/terraform/aws/README.md) |

Velox itself is one Go binary plus Postgres. Both Compose and the
Helm/Terraform paths reach the same end state — same image, same
migrations, same env-var schema. Pick by what your team already
operates, not by what's "production-grade." A single-VM Compose deploy
behind an ALB with managed Postgres (RDS) is a perfectly reasonable
v1.

## Quickstart — Docker Compose on a single VM

Five minutes from `docker compose up -d` to a working tenant.

[`deploy/compose/README.md`](../deploy/compose/README.md) — copy-pasteable
quickstart with prerequisites, configuration, health checks, and the
first-tenant bootstrap.

What you get:

- `nginx` reverse proxy on `:80`
- `velox-api` on `:8080` (internal-only)
- `postgres:16-alpine` with a persistent volume

Migrations run on first boot via `RUN_MIGRATIONS_ON_BOOT=true`. The
image is the same `ghcr.io/sagarsuperuser/velox` that ships from CI.

## Backup and restore

[`docs/self-host/postgres-backup.md`](self-host/postgres-backup.md) —
`pg_basebackup` + WAL-archive recipe, S3 layout, retention guidance,
and a quarterly restore drill. Run the drill before you need it.

If you're on managed Postgres (RDS, Cloud SQL, Supabase, Neon),
automated backups already cover this — verify retention matches your
RTO/RPO and you're done.

## Required configuration

Three env vars are mandatory on a fresh install. The rest of `.env`
ships with safe defaults; the binary tells you in the logs if anything
optional you care about (Stripe webhooks, SMTP, Redis) is unset.

| Variable | Purpose |
|---|---|
| `POSTGRES_PASSWORD` | Postgres superuser password |
| `VELOX_ENCRYPTION_KEY` | 64 hex chars (32 bytes) — encrypts customer PII at rest. Production refuses to start without it. |
| `VELOX_BOOTSTRAP_TOKEN` | Authorises the one-shot `POST /v1/bootstrap` that creates the first tenant |

The full env-var schema with defaults lives in
[`deploy/compose/.env.example`](../deploy/compose/.env.example).

## Health checks

| Endpoint | Use for |
|---|---|
| `GET /health` | Liveness — is the process running |
| `GET /health/ready` | Readiness — DB reachable + scheduler not stalled |

Wire your load balancer to `/health/ready`. Both endpoints are exempt
from rate limiting and audit logging.

## TLS

The Compose stack listens on plain HTTP on purpose so the local
quickstart works with `curl`. For production add TLS with one of:

- **Managed load balancer** in front (AWS ALB, Cloudflare, GCP HTTPS LB)
- **certbot on the host** terminating to `localhost:80`

`APP_ENV=production` (the default in `.env.example`) turns on
secure-cookie and HSTS protections automatically — no per-deploy
toggles.

## Sizing

Velox is lightweight; the baseline targets are:

| Profile | RAM | vCPU | Postgres |
|---|---|---|---|
| Eval / staging | 512 MB | 1 | 1 GB shared with API |
| Single-tenant production (~1k events/sec) | 2 GB | 2 | 4 GB managed Postgres |
| Multi-tenant SaaS (≥10k events/sec) | Multi-replica K8s | 4+ per replica | Sized to writes per second; partition `usage_events` |

For multi-replica deployments use the [Helm chart](../deploy/helm/velox/README.md) —
the v1 scheduler is leader-elected via Postgres advisory locks, so
multiple API replicas safely coexist without zombie locks (see
`internal/billing/postgres_locker.go`).

## Versioning

Velox is pre-1.0 (`0.MINOR.PATCH`). Pin `VELOX_IMAGE` to a tag in
`.env` rather than `:latest` once you put real traffic on it. The
release log lives at [`CHANGELOG.md`](../CHANGELOG.md); customer-facing
rollups are at the dashboard `/changelog` page.

## Choosing between the three install shapes

- **Compose on a single VM** is the v1 reference — the simplest shape, lowest cost, fewest moving parts. [`deploy/compose/README.md`](../deploy/compose/README.md). Best for: evaluation, single-tenant production at modest volume, anyone who doesn't already operate K8s.
- **Helm on Kubernetes** if you already run K8s. [`deploy/helm/velox/README.md`](../deploy/helm/velox/README.md). Targets generic K8s (kind / k3s / EKS / GKE / AKS); does NOT bundle Postgres — bring your own (RDS / Cloud SQL / Supabase / Neon).
- **Terraform on AWS** if you want a one-shot AWS install with no manual clicking. [`deploy/terraform/aws/README.md`](../deploy/terraform/aws/README.md). Provisions VPC + EC2 + RDS Postgres + S3 backup bucket; runs the Compose stack on the EC2 host. Cost: ~$30-50/mo at default sizing if left running 24/7, or ~$1-2 for an apply/destroy validation run.

All three reach the same end state: same image, same migrations, same
env-var schema. Pick by what your team already operates.

## What's not here yet

- **Cold-install on real AWS** — the Terraform module is structurally validated (`terraform init -backend=false && terraform validate` passes clean), but a non-Velox-engineer drill on a fresh AWS account is a separate Week 9 follow-up lane. Real-account friction (firewall rules, RDS SSL handshake, IAM trust quirks) only surfaces when you actually `terraform apply`.
- **Multi-AZ RDS, ALB-fronted TLS, Route 53 wiring** — out of scope for the v1 module on purpose. Standard upgrade path: flip `multi_az = true` on the `aws_db_instance` resource, add an `aws_lb` in front, point a Route 53 zone at it.

## Compliance posture

Compliance docs land in Week 10 of the
[90-day plan](90-day-plan.md): encryption-at-rest verification,
audit-log retention guide, SOC 2 control mapping, GDPR data
export/deletion. Until then, the operationally relevant facts are:

- Customer PII is encrypted at rest with `VELOX_ENCRYPTION_KEY` (AES-GCM)
- API keys are stored as SHA-256 hashes; plaintext is never recoverable
- Postgres RLS isolates tenants; the `velox_app` runtime role is
  non-superuser so the policies are enforced (see
  `deploy/compose/postgres-init.sql`)
- Webhook signing uses HMAC-SHA256 (inbound Stripe + outbound)

## Help

- File issues at <https://github.com/sagarsuperuser/velox/issues>
- Operational runbooks: [`docs/ops/`](ops/)
- Architecture decisions: [`docs/adr/`](adr/)
