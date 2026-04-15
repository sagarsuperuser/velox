# Velox — Secrets Management

## Principle

Velox follows the 12-factor app principle: **secrets come from the environment.**
The application reads `os.Getenv()` and never knows where the secret originated.
Secret rotation, access control, and auditing are handled at the infrastructure layer.

## Architecture

```
Vault / AWS Secrets Manager / GCP Secret Manager
         │
         ▼
External Secrets Operator (K8s controller)
         │
         ▼
K8s Secret (velox-secrets)
         │
         ▼
Pod env vars → app reads os.Getenv()
```

This is the industry standard pattern used by Stripe, Shopify, and most K8s-native
SaaS companies. The app has zero secrets management code.

## Secrets Inventory

| Env Var | Description | Required |
|---------|-------------|----------|
| `DATABASE_URL` | PostgreSQL connection string | Yes |
| `STRIPE_SECRET_KEY` | Stripe API secret key (`sk_live_*` or `sk_test_*`) | Yes (for payments) |
| `STRIPE_WEBHOOK_SECRET` | Stripe webhook signing secret (`whsec_*`) | Yes (for webhooks) |
| `VELOX_ENCRYPTION_KEY` | 64-char hex key for PII encryption | Recommended in prod |
| `VELOX_BOOTSTRAP_TOKEN` | One-time bootstrap token | Optional |
| `REDIS_URL` | Redis connection string | Recommended in prod |
| `METRICS_TOKEN` | Bearer token for `/metrics` endpoint | Recommended in prod |

## Setup by Stage

### Development (Local)

Use `.env` file or export directly:

```bash
export DATABASE_URL="postgres://velox:velox@localhost:5432/velox?sslmode=disable"
export STRIPE_SECRET_KEY="sk_test_..."
export STRIPE_WEBHOOK_SECRET="whsec_..."
```

### Staging / Small Production

Use K8s Secrets directly:

```bash
kubectl create secret generic velox-secrets \
  --from-literal=DATABASE_URL="postgres://..." \
  --from-literal=STRIPE_SECRET_KEY="sk_live_..." \
  --from-literal=STRIPE_WEBHOOK_SECRET="whsec_..." \
  --from-literal=VELOX_ENCRYPTION_KEY="$(openssl rand -hex 32)" \
  -n velox
```

For GitOps, use [Sealed Secrets](https://github.com/bitnami-labs/sealed-secrets):

```bash
kubeseal --format=yaml < secret.yaml > sealed-secret.yaml
# sealed-secret.yaml is safe to commit — encrypted with cluster public key
```

### Production (with rotation)

Use [External Secrets Operator](https://external-secrets.io/):

```bash
# 1. Install ESO
helm install external-secrets external-secrets/external-secrets \
  -n external-secrets --create-namespace

# 2. Store secrets in AWS Secrets Manager
aws secretsmanager create-secret --name velox/stripe-secret-key --secret-string "sk_live_..."
aws secretsmanager create-secret --name velox/stripe-webhook-secret --secret-string "whsec_..."
aws secretsmanager create-secret --name velox/encryption-key --secret-string "$(openssl rand -hex 32)"
aws secretsmanager create-secret --name velox/database-url --secret-string "postgres://..."

# 3. Apply ESO manifests
kubectl apply -f deploy/k8s/external-secrets.yaml
```

ESO syncs secrets every 5 minutes. To rotate a secret:

```bash
# Update in AWS SM
aws secretsmanager update-secret --secret-id velox/stripe-secret-key --secret-string "sk_live_NEW..."

# ESO picks it up within 5 minutes → K8s Secret updated → pod restart picks it up
# For zero-downtime: use a rolling restart
kubectl rollout restart deployment/velox -n velox
```

See `deploy/k8s/external-secrets.yaml` for complete AWS SM and Vault configurations.

## Rotation Checklist

| Secret | Rotation method | Downtime |
|--------|----------------|----------|
| `STRIPE_SECRET_KEY` | Update in secrets store → rolling restart | Zero (rolling) |
| `STRIPE_WEBHOOK_SECRET` | Rotate in Stripe Dashboard → update secrets store → restart | Brief (webhook gap) |
| `VELOX_ENCRYPTION_KEY` | **Cannot rotate without re-encrypting data.** Plan a migration. | Requires maintenance window |
| `DATABASE_URL` | Update password in PG + secrets store → restart | Zero (rolling) |
| `REDIS_URL` | Update in secrets store → restart | Zero (fail-open) |

## Security Hardening

1. **Enable etcd encryption** for K8s Secrets at rest:
   ```yaml
   # /etc/kubernetes/encryption-config.yaml
   apiVersion: apiserver.config.k8s.io/v1
   kind: EncryptionConfiguration
   resources:
     - resources: [secrets]
       providers:
         - aescbc:
             keys:
               - name: key1
                 secret: <base64-encoded-32-byte-key>
         - identity: {}
   ```

2. **RBAC**: Restrict who can read secrets:
   ```yaml
   apiVersion: rbac.authorization.k8s.io/v1
   kind: Role
   metadata:
     name: velox-secret-reader
     namespace: velox
   rules:
     - apiGroups: [""]
       resources: ["secrets"]
       resourceNames: ["velox-secrets"]
       verbs: ["get"]
   ```

3. **Audit logging**: Enable K8s audit logs for secret access events.

4. **Network policy**: Restrict which pods can access the secrets namespace.
