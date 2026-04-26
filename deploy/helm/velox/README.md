# Velox Helm chart

Generic-Kubernetes Helm chart for [Velox](https://github.com/sagarsuperuser/velox),
the open-source usage-based billing engine. Targets kind / k3s / EKS /
GKE / AKS.

If you don't already operate Kubernetes, **use the single-VM Compose
path instead** (`deploy/compose/`) — it's the supported reference
deploy. This chart exists for users who already run K8s.

## What it ships

| Resource | Purpose |
|---|---|
| `Deployment` | One velox-api pod by default. The v1 scheduler is leader-elected via Postgres advisory locks (`internal/billing/postgres_locker.go`), so multi-replica is safe — flip `replicaCount` or enable `autoscaling`. |
| `Service` | `ClusterIP` on `8080`. |
| `ConfigMap` | Non-secret env vars — `APP_ENV`, `PORT`, DB pool tuning, public URLs, OTEL endpoint. |
| `Secret` | `VELOX_ENCRYPTION_KEY`, `VELOX_BOOTSTRAP_TOKEN`, `DATABASE_URL` or `DB_PASSWORD`, optional Stripe / SMTP / Redis / metrics-token. Skip the chart-managed Secret with `secrets.existingSecret=<name>` if you use ESO / sealed-secrets / native cloud-provider secrets. |
| `ServiceAccount` | Dedicated non-default SA, no API token mount. |
| `Ingress` | Optional (`ingress.enabled=true`); off by default since most users front Velox with their existing ALB / Cloudflare / nginx. |
| `HorizontalPodAutoscaler` | Optional (`autoscaling.enabled=true`); off by default since v1 single-replica is the right shape for typical self-host volumes. |

The chart does **not** bundle Postgres. Bring your own — RDS, Cloud SQL,
Supabase, Neon, or a dedicated Postgres install in your cluster (out
of scope for this chart on purpose; running stateful Postgres in the
API release is an anti-pattern at production scale).

## Install

```bash
# 1. Generate required secrets.
ENC_KEY=$(openssl rand -hex 32)              # 64 hex chars
BOOTSTRAP=$(openssl rand -hex 32)
DB_URL="postgres://velox:strong-pw@your-rds-endpoint:5432/velox?sslmode=require"

# 2. Install.
helm install velox ./deploy/helm/velox \
  --namespace velox --create-namespace \
  --set secrets.encryptionKey=$ENC_KEY \
  --set secrets.bootstrapToken=$BOOTSTRAP \
  --set externalDatabase.url=$DB_URL \
  --set image.tag=0.1.0

# 3. Wait for ready.
kubectl -n velox rollout status deploy/velox

# 4. Bootstrap the first tenant.
kubectl -n velox port-forward svc/velox 8080:8080 &
curl -X POST http://localhost:8080/v1/bootstrap \
  -H "Authorization: Bearer $BOOTSTRAP" \
  -H "Content-Type: application/json" \
  -d '{"tenant_name":"my-co","admin_email":"you@example.com"}'
```

For non-interactive installs, put values into a YAML file and pass
`-f my-values.yaml`. Sealed-secrets / ESO / Vault users: leave
`secrets.*` empty in values, point `secrets.existingSecret` at your
externally-managed Secret, and the chart skips the Secret template
entirely.

## Configuration reference

All env-var keys mirror the binary's actual reads from
`internal/config/config.go` plus the per-package `os.Getenv`
callsites — same schema as `deploy/compose/.env.example`. No invented
keys.

### Required

| Value | Env var | Notes |
|---|---|---|
| `secrets.encryptionKey` | `VELOX_ENCRYPTION_KEY` | 64 hex chars (32 bytes). Production refuses to start without it (`config.validateFatal`). Generate with `openssl rand -hex 32`. |
| `secrets.bootstrapToken` | `VELOX_BOOTSTRAP_TOKEN` | Bearer token for the one-shot `POST /v1/bootstrap` that creates the first tenant. |
| `externalDatabase.url` *or* `externalDatabase.host` (+ split fields + `secrets.dbPassword`) | `DATABASE_URL` *or* `DB_HOST`/`DB_PORT`/`DB_NAME`/`DB_USER`/`DB_PASSWORD` | Pick one shape. Setting both is rejected by `config.loadDatabaseURL`. |

### Common

| Value | Env var | Default |
|---|---|---|
| `config.appEnv` | `APP_ENV` | `production` (turns on encryption-key fatal check, secure cookies, HSTS) |
| `config.runMigrations` | `RUN_MIGRATIONS_ON_BOOT` | `true` |
| `config.corsAllowedOrigins` | `CORS_ALLOWED_ORIGINS` | empty |
| `config.db.sslMode` | `DB_SSLMODE` | `require` (only used when split-host shape is set) |
| `replicaCount` | n/a | `1` |
| `image.tag` | n/a | `Chart.appVersion` (`0.1.0`) |
| `ingress.enabled` | n/a | `false` |
| `autoscaling.enabled` | n/a | `false` |

### Optional — outbound integrations

| Value | Env var | When you need it |
|---|---|---|
| `secrets.stripeWebhookSecret` | `STRIPE_WEBHOOK_SECRET` | Inbound Stripe webhooks (per-tenant Stripe API keys live in the DB, not here) |
| `secrets.smtpHost` / `smtpPort` / `smtpUsername` / `smtpPassword` / `smtpFrom` | `SMTP_*` | Outbound email (dunning, password reset, invoice notify). Velox always enqueues; the dispatcher noops if `SMTP_HOST` is empty. |
| `secrets.redisUrl` | `REDIS_URL` | Distributed rate limiting. Fails open without it. |
| `secrets.metricsToken` | `METRICS_TOKEN` | Bearer-gate `GET /metrics`. Empty exposes anonymously behind your ingress. |
| `config.otelEndpoint` / `otelServiceName` | `OTEL_*` | OpenTelemetry trace export. Noop when unset. |
| `config.dashboardUrl` / `dashboardPasswordResetUrl` / etc. | `VELOX_DASHBOARD_*` | URLs rendered into outbound email templates. |
| `config.hostedInvoiceBaseUrl` / `paymentUpdateUrl` / `customerPortalUrl` / `stripeCheckoutSuccessUrl` / `stripeCheckoutCancelUrl` | `HOSTED_INVOICE_BASE_URL` / `PAYMENT_UPDATE_URL` / etc. | Public URLs in invoice emails and Stripe redirects. |
| `config.billingAlertsInterval` | `VELOX_BILLING_ALERTS_INTERVAL` | Tick interval for the billing-alert evaluator (e.g. `1m`, `5m`). Defaults to `1h`. |

The full list (with defaults) lives in `values.yaml` and matches
`deploy/compose/.env.example` field-for-field.

## Validation

```bash
# Lint the chart.
helm lint deploy/helm/velox/

# Render and YAML-parse.
helm template deploy/helm/velox/ > /tmp/render.yaml
python3 -c "import yaml; list(yaml.safe_load_all(open('/tmp/render.yaml')))"

# Or render with sample values.
helm template deploy/helm/velox/ \
  --set secrets.encryptionKey=$(openssl rand -hex 32) \
  --set secrets.bootstrapToken=test \
  --set externalDatabase.url=postgres://localhost/test \
  --set ingress.enabled=true \
  --set autoscaling.enabled=true
```

## Upgrade / uninstall

```bash
# Upgrade (re-uses the same secrets unless overridden).
helm upgrade velox ./deploy/helm/velox -n velox \
  --reuse-values --set image.tag=0.2.0

# Uninstall (the database survives — it's external).
helm uninstall velox -n velox
```

## Sizing

A single replica with the default `512Mi`/`500m` limits handles ~1k
events/sec on commodity nodes. Tune `resources` for higher
throughput; multiple replicas safely coexist (advisory-lock leader
election handles the scheduler).

## Differences from the Compose path

The Compose stack at `deploy/compose/` ships with bundled `nginx`
(reverse proxy with rate-limit + `/metrics` allowlist) and `postgres`
(single instance, persistent volume, `velox_app` non-superuser RLS
role). Helm intentionally ships neither — your ingress and your
external Postgres come from the platform you already run. The
`postgres-init.sql` from the Compose stack is **still required** if
you want RLS enforced — run the role-creation snippet against your
external Postgres before installing the chart, or rely on the binary's
fallback (admin connection with a loud warning). Details:
`deploy/compose/postgres-init.sql`.

## What's not here

- **No PostgreSQL subchart** — bring your own, on purpose.
- **No nginx ingress controller** — install one separately if you need it (or use the chart's `ingress.enabled=true` to wire into one you already have).
- **No CronJob for backups** — managed Postgres handles backups; if you self-host Postgres in-cluster, see `docs/self-host/postgres-backup.md` for a `pg_basebackup` recipe and adapt to a Kubernetes CronJob.
