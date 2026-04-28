# Self-hosting Velox

Velox is designed to run on your own infrastructure. Pick the install
shape that matches where you operate today.

## Pick a path

| Shape | Use when | Availability posture | Status |
|---|---|---|---|
| **Docker Compose on a single VM** | Evaluating, dev/staging, demos, small single-tenant deployments where 5–30 min downtime windows are acceptable | Single-instance API on one VM (SPOF) — VM failure, AZ failure, kernel patch, or `docker compose down/up` all cause billing downtime | Ships today — [`deploy/compose/README.md`](../deploy/compose/README.md) |
| **Helm chart on Kubernetes** | Production workloads requiring availability, horizontal scale, rolling restarts | Multi-replica API behind a load balancer + Multi-AZ managed Postgres + leader-elected scheduler via PG advisory locks | Ships today — [`deploy/helm/velox/README.md`](../deploy/helm/velox/README.md) |
| **Terraform module for AWS** | One-shot VPC + EC2 + RDS install for evaluating or running Compose on a real AWS account without manual clicking | Inherits Compose's posture (Terraform runs the Compose stack on one EC2 host) — single-instance API | Ships today — [`deploy/terraform/aws/README.md`](../deploy/terraform/aws/README.md) |

Velox itself is one Go binary plus Postgres. All three shapes reach the
same end state on the application layer — same image, same migrations,
same env-var schema — but their *availability posture* differs:

- **Compose** runs a single `velox-api` process on one VM. The same
  process hosts the scheduler and outbox dispatchers (see
  [`docker-compose.yml`](../deploy/compose/docker-compose.yml) header
  comment). VM failure, AZ failure, kernel patch, or any restart cause
  billing downtime. Adding managed Postgres (RDS) removes the DB SPOF
  but leaves the API SPOF intact — that is *not* production with
  availability.
- **Helm** runs ≥2 `velox-api` replicas behind a load balancer with
  managed Postgres in Multi-AZ. The v1 scheduler is leader-elected via
  PG advisory locks (see [ADR-006](adr/006-background-scheduler-vs-message-queue.md)
  and [`internal/billing/postgres_locker.go`](../internal/billing/postgres_locker.go))
  so multiple API replicas coexist safely without duplicate billing
  cycles. This is the production-with-availability shape.
- **Terraform AWS** is functionally Compose with a turn-key VPC/EC2/RDS
  install — useful for fast evaluation on a real AWS account, but it
  inherits Compose's single-instance API posture. Graduate to Helm for
  production.

Pick by your downtime tolerance, not just what your team operates. A
billing engine outage queues Stripe webhooks, stalls subscription state
machines (trial flips, scheduled cancellations, dunning runs), and
hides invoices from customers. If those events are revenue-critical for
your business, Helm + Multi-AZ Postgres is the answer.

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

| Profile | Shape | RAM | vCPU | Postgres |
|---|---|---|---|---|
| Evaluation / dev / staging | Compose, single VM | 512 MB | 1 | 1 GB shared with API in-container |
| Small deployment, downtime acceptable | Compose + managed Postgres | 2 GB | 2 | 4 GB managed Postgres |
| Production with availability | Helm, ≥2 replicas + Multi-AZ Postgres | 2 GB per replica | 2 per replica | 8 GB+ Multi-AZ managed Postgres |
| Scaled SaaS (≥10k events/sec sustained) | Helm, multi-replica + partitioned writes | 4+ per replica | 4+ per replica | Sized to writes per second; partition `usage_events` |

Notes:

- The "small deployment" row is **not** "production with availability"
  — `velox-api` remains single-instance even with managed Postgres. Use
  this profile only for low-volume single-tenant setups where 5–30 min
  downtime windows are acceptable.
- For real production with availability use Helm with ≥2 replicas — the
  v1 scheduler is leader-elected via Postgres advisory locks so
  multiple API replicas safely coexist without duplicate billing
  cycles (see [ADR-006](adr/006-background-scheduler-vs-message-queue.md)
  and `internal/billing/postgres_locker.go`).
- Throughput numbers are conservative starter sizing; the engine has
  not been benchmarked end-to-end at the upper bound. Treat them as
  floors, not ceilings, and run a load test against your own profile
  before committing.

## Versioning

Velox is pre-1.0 (`0.MINOR.PATCH`). Pin `VELOX_IMAGE` to a tag in
`.env` rather than `:latest` once you put real traffic on it. The
release log lives at [`CHANGELOG.md`](../CHANGELOG.md); customer-facing
rollups are at the dashboard `/changelog` page.

## Choosing between the three install shapes

- **Compose on a single VM** — the simplest shape, lowest cost, fewest moving parts. [`deploy/compose/README.md`](../deploy/compose/README.md). Single `velox-api` process on one VM; same process hosts the scheduler and outbox dispatchers. **No API HA.** Best for: evaluation, dev/staging, demos, and small single-tenant deployments where 5–30 min downtime windows are acceptable.
- **Helm on Kubernetes** — the production-with-availability shape. [`deploy/helm/velox/README.md`](../deploy/helm/velox/README.md). Multi-replica API behind a load balancer; leader-elected scheduler via PG advisory locks. Targets generic K8s (kind / k3s / EKS / GKE / AKS); does NOT bundle Postgres — bring your own with Multi-AZ enabled (RDS / Cloud SQL / Supabase / Neon). Use this for any workload where billing downtime is a customer-visible revenue event.
- **Terraform on AWS** — a turn-key VPC + EC2 + RDS install for evaluating or running Compose on real AWS without manual clicking. [`deploy/terraform/aws/README.md`](../deploy/terraform/aws/README.md). Provisions VPC + EC2 + RDS Postgres + S3 backup bucket and runs the Compose stack on the EC2 host. Inherits Compose's single-instance API posture — same use cases as Compose. Cost: ~$30–50/mo at default sizing if left running 24/7, or ~$1–2 for an apply/destroy validation run. For production graduate to Helm.

All three reach the same end state on the application layer — same
image, same migrations, same env-var schema. The choice is about
*availability posture*, not application capability.

## Migrating from Stripe Billing

If you run on Stripe Billing today and want to move to Velox without
missing an invoice, see [`docs/migration-from-stripe.md`](migration-from-stripe.md).
It covers the full operator playbook:

- **Pre-migration checklist** — Velox tenant provisioned, Stripe
  restricted key, `VELOX_ENCRYPTION_KEY` + `VELOX_EMAIL_BIDX_KEY`
  verified, downstream webhook consumers inventoried.
- **The five importer phases** — `velox-import` reads via
  `--api-key=rk_live_…`, writes via `DATABASE_URL`. Resources run in
  strict dependency order regardless of CLI input order: customers
  (Phase 0) → products → prices (Phase 1) → subscriptions (Phase 2)
  → finalized invoices (Phase 3). Per-row outcomes are
  `insert` / `skip-equivalent` / `skip-divergent` / `error` written to
  a CSV report.
- **Rehearsal run in test mode** — full pipeline against `sk_test_…`
  before touching production data; same code paths, isolated by the
  `livemode` column.
- **Production parallel-run cutover (T-14 → T+14)** — Phase A Prepare
  → Phase B Initial backfill → Phase C Parallel run with webhook
  shadow → Phase D Cutover → Phase E Stabilize → Phase F Rollback.
  The 14-day parallel window keeps Stripe Billing as the source of
  truth until reconciliation across customer count / active subs /
  paid invoices / revenue ties out at the cutover threshold.
- **Reconciliation toolkit** — copy-pasteable SQL recipes that match
  Velox totals against the Stripe report API for every reconciliation
  axis the cutover gates on.
- **Webhook redirection** — parallel webhook posture during the
  shadow window, swap-over at T-0, and the rollback procedure if the
  primary needs to flip back to Stripe.
- **Known limitations** — Schedules, Quotes, Promotion Codes,
  multi-item subscriptions, graduated/tiered prices, metered
  `usage_type`, Connect, and draft/open invoices are out of scope
  today; each has a documented manual recreation path.

## What's not here yet

- **Cold-install on real AWS** — the Terraform module is structurally validated (`terraform init -backend=false && terraform validate` passes clean), but a non-Velox-engineer drill on a fresh AWS account is a separate Week 9 follow-up lane. Real-account friction (firewall rules, RDS SSL handshake, IAM trust quirks) only surfaces when you actually `terraform apply`.
- **Multi-AZ RDS, ALB-fronted TLS, Route 53 wiring** — out of scope for the v1 module on purpose. Standard upgrade path: flip `multi_az = true` on the `aws_db_instance` resource, add an `aws_lb` in front, point a Route 53 zone at it.

## Compliance posture

Audit-log retention, encryption-at-rest, and SOC 2 control mapping
guides have shipped. GDPR data export/deletion is still pending.

Available now:

- [docs/ops/audit-log-retention.md](ops/audit-log-retention.md) —
  what the audit log captures, regime-by-regime retention
  recommendations (SOC 2 / GDPR / PCI-DSS / HIPAA / SOX), prune +
  S3 archive pattern, and the restore path. The Velox default is
  18 months in the live `audit_log` table with archived exports
  retained per the regime that applies.
- [docs/ops/encryption-at-rest.md](ops/encryption-at-rest.md) —
  what Velox encrypts at the application layer (customer PII,
  webhook signing secrets, per-tenant Stripe credentials via
  AES-256-GCM under `VELOX_ENCRYPTION_KEY`), what it hashes (API keys,
  passwords, sessions, portal tokens), the email blind index for
  magic-link lookup under `VELOX_EMAIL_BIDX_KEY`, copy-pasteable SQL
  verification recipes, the honest disclosure that key rotation is
  **not implemented today**, and the SOC 2 / PCI-DSS / GDPR / HIPAA
  control mapping.
- [docs/compliance/soc2-mapping.md](compliance/soc2-mapping.md) —
  SOC 2 Trust Services Criteria control mapping. Walks all five
  Common Criteria families (CC1 Control Environment, CC2
  Communication, CC3 Risk Assessment, CC4 Monitoring, CC5 Control
  Activities, CC6 Logical Access, CC7 System Operations, CC8 Change
  Management, CC9 Risk Mitigation) plus the optional Availability /
  Confidentiality / Processing Integrity / Privacy categories.
  Each criterion has plain-English requirement, how Velox addresses
  it with code-level evidence pointers (`internal/...path/file.go:line`
  format), explicit gaps, and the artifacts an auditor would
  request. Closes with a 17-item gap list ranked by audit impact
  (key rotation tooling, SECURITY.md, MFA, govulncheck-blocking,
  SAST, CODE_OF_CONDUCT, CODEOWNERS, status page, image signing —
  priority list before a Type 1) and a flat evidence index. Pre-
  launch / pre-audit posture; this is audit-prep input rather than
  an attestation.

The other operationally relevant facts:

- Customer PII is encrypted at rest with `VELOX_ENCRYPTION_KEY` (AES-GCM)
- API keys are stored as SHA-256 hashes; plaintext is never recoverable
- Postgres RLS isolates tenants; the `velox_app` runtime role is
  non-superuser so the policies are enforced (see
  `deploy/compose/postgres-init.sql`)
- Webhook signing uses HMAC-SHA256 (inbound Stripe + outbound)
- The audit log is append-only by DB trigger
  ([migration `0011_audit_append_only`](../internal/platform/migrate/sql/0011_audit_append_only.up.sql))
  so no compromised code path or stray ORM call can rewrite or erase
  evidence. Per-tenant fail-closed posture (`tenant_settings.audit_fail_closed`)
  forces a 503 on audit-write failure rather than silently flushing
  the handler response.

## Help

- File issues at <https://github.com/sagarsuperuser/velox/issues>
- Operational runbooks: [`docs/ops/`](ops/)
- Architecture decisions: [`docs/adr/`](adr/)
