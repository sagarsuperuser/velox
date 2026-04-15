# Velox Deployment

## Local Development

```bash
docker compose up -d postgres
DATABASE_URL="postgres://velox:velox@localhost:5432/velox?sslmode=disable" go run ./cmd/velox-bootstrap
DATABASE_URL="postgres://velox:velox@localhost:5432/velox?sslmode=disable" RUN_MIGRATIONS_ON_BOOT=true go run ./cmd/velox
```

Or run everything in Docker:

```bash
docker compose up -d
```

## Building the Docker Image

```bash
docker build -t velox:latest .
docker run --rm -e DATABASE_URL="..." -p 8080:8080 velox:latest
```

## Deploying to Kubernetes (Raw Manifests)

1. Update secrets in `deploy/k8s/secret.yaml` with real values.

2. Apply all manifests:

```bash
kubectl apply -f deploy/k8s/namespace.yaml
kubectl apply -f deploy/k8s/
```

3. Verify the deployment:

```bash
kubectl -n velox get pods
kubectl -n velox logs -l app.kubernetes.io/name=velox
```

## Deploying with Helm

```bash
helm install velox deploy/helm/velox \
  --namespace velox --create-namespace \
  --set secrets.databaseUrl="postgres://..." \
  --set secrets.stripeSecretKey="sk_live_..." \
  --set secrets.stripeWebhookSecret="whsec_..." \
  --set ingress.enabled=true \
  --set autoscaling.enabled=true
```

Upgrade an existing release:

```bash
helm upgrade velox deploy/helm/velox --namespace velox --reuse-values \
  --set image.tag=abc123
```

## Required Secrets

| Variable | Description |
|---|---|
| `DATABASE_URL` | PostgreSQL connection string |
| `STRIPE_SECRET_KEY` | Stripe API secret key |
| `STRIPE_WEBHOOK_SECRET` | Stripe webhook signing secret |
| `SMTP_HOST` | SMTP server host (for invoice emails) |
| `SMTP_USERNAME` | SMTP username |
| `SMTP_PASSWORD` | SMTP password |

## Health Check Endpoints

| Endpoint | Purpose |
|---|---|
| `GET /health` | Liveness -- returns 200 if the process is running |
| `GET /health/ready` | Readiness -- returns 200 if the database is reachable |

## Scaling

- **Horizontal**: The HPA scales from 2 to 10 replicas based on CPU utilization (target 70%).
- **Vertical**: Adjust `resources.limits` and `resources.requests` in values.yaml or the deployment manifest.
- **Database**: Velox uses connection pooling (`DB_MAX_OPEN_CONNS`, default 20). When scaling replicas, ensure total connections across all pods do not exceed your PostgreSQL `max_connections`. Consider PgBouncer for higher replica counts.
- **Migrations**: Only one pod should run migrations. Set `RUN_MIGRATIONS_ON_BOOT=true` and use a rolling update with `maxUnavailable: 0` so migrations complete on the first new pod before old pods terminate.
