# Velox on AWS — Terraform module

A one-shot Terraform module that stands up a single-VM Velox install
on AWS: VPC + EC2 + RDS Postgres + S3 backup bucket + IAM. v1
deliberately = boring + cheap. **NOT EKS, NOT autoscaling, NOT
multi-AZ.** If you already operate Kubernetes, use the Helm chart at
`deploy/helm/velox/` instead.

```
                         AWS Account
+-----------------------------------------------------+
|                          VPC                        |
|                                                     |
|   public-a (your IP)        public-b (RDS only)     |
|        +-------+                                    |
|        |  EC2  |  <── HTTP :80 from internet        |
|        +---+---+      SSH :22 from your CIDR        |
|            |                                        |
|   private-a                  private-b              |
|             \                       /               |
|              +---- RDS Postgres ----+               |
+-----------------------------------------------------+
                            |
                            v
            S3 backup bucket (versioned, SSE, BPA on,
            lifecycle Glacier IR -> Deep Archive)
```

## Cost estimate

Default sizing (`t3.small` EC2 + `db.t3.small` RDS + 20 GB gp3 + S3) costs
roughly **$30-50/month** if left running 24/7 on us-east-1, depending
on outbound bandwidth and backup volume:

| Resource | Default size | Approx. monthly (us-east-1, 24/7) |
|---|---|---|
| EC2 `t3.small` | 2 vCPU / 2 GB | ~$15 |
| EBS gp3 root volume | 30 GB | ~$2 |
| RDS `db.t3.small` Postgres | 2 vCPU / 2 GB | ~$25 |
| RDS gp3 storage | 20 GB | ~$2 |
| S3 backup bucket | <1 GB at start | < $1 |
| Public IPv4 + outbound | varies | $1-5 |

For a destroy-after-validation run (apply, smoke-test, destroy within
a couple of hours), expect **$1-2 of charges total** — both the EC2
and RDS instance bill per second. Verify against the AWS Pricing
Calculator before applying; pricing changes.

## Prerequisites

1. **AWS account** with admin or scoped IAM credentials configured (`aws configure` or `AWS_PROFILE`/`AWS_ACCESS_KEY_ID`).
2. **Terraform** ≥ 1.5.0 (`brew install terraform`).
3. **An existing EC2 key pair** in the target region. Create one out-of-band:
   ```bash
   aws ec2 create-key-pair --key-name velox-dev --region us-east-1 \
     --query 'KeyMaterial' --output text > ~/.ssh/velox-dev.pem
   chmod 600 ~/.ssh/velox-dev.pem
   ```
4. **Your operator IP** — the SSH security group default is a
   documentation-only placeholder (`203.0.113.0/24`); apply will leave
   you locked out unless you override `ssh_allowed_cidrs`.

## Walkthrough

```bash
cd deploy/terraform/aws

# 1. Fill in required vars.
cp terraform.tfvars.example terraform.tfvars
$EDITOR terraform.tfvars   # set region, db_password, key_name, ssh_allowed_cidrs

# 2. Initialize providers.
terraform init

# 3. Review the plan. Should show 28 resources to add.
terraform plan

# 4. Apply.
terraform apply

# 5. Connect.
terraform output ssh_command
ssh -i ~/.ssh/velox-dev.pem ec2-user@$(terraform output -raw ec2_public_ip)

# 6. On the host, recover the auto-generated bootstrap token.
sudo grep 'Bootstrap:' /var/log/velox-bootstrap.log

# 7. Hit the bootstrap endpoint with that token.
curl -X POST -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -d '{"tenant_name":"my-co","admin_email":"you@example.com"}' \
  http://$(terraform output -raw ec2_public_ip)/v1/bootstrap

# 8. When done, tear it all down.
terraform destroy
```

## Validate the module without applying

If you only want to confirm the module is structurally sound (no AWS
API calls, no apply), run:

```bash
terraform init -backend=false
terraform validate
terraform fmt -check
```

All three must exit zero. The PR that introduces this module gates CI
on exactly that.

## Inputs

### Required

| Variable | Type | Description |
|---|---|---|
| `region` | `string` | AWS region (e.g. `us-east-1`). |
| `db_password` | `string` (sensitive, min 16 chars) | RDS Postgres master password. Pick a long random value; rotate via RDS modify after launch. |
| `key_name` | `string` | Name of an existing EC2 key pair in the region. |

### Recommended overrides

| Variable | Default | Why override |
|---|---|---|
| `ssh_allowed_cidrs` | `["203.0.113.0/24"]` (doc-only) | Replace with your operator IP / VPN range or set to `[]` and rely on SSM Session Manager. |
| `velox_image_tag` | `"0.1.0"` | Pin to the Velox release you've validated. |
| `vpc_cidr` | `"10.30.0.0/16"` | Override if it collides with anything you peer with. |

### Sizing

| Variable | Default | Notes |
|---|---|---|
| `ec2_instance_type` | `t3.small` | 2 vCPU / 2 GB. Handles ~1k events/sec. |
| `ec2_root_volume_size_gb` | `30` | gp3, encrypted. |
| `rds_instance_class` | `db.t3.small` | 2 vCPU / 2 GB. |
| `rds_allocated_storage_gb` | `20` | gp3, encrypted, autoscales up to `rds_max_allocated_storage_gb`. |
| `rds_max_allocated_storage_gb` | `100` | Storage autoscaling cap. |
| `rds_backup_retention_days` | `7` | RDS automated backups. |
| `postgres_engine_version` | `"16.4"` | Velox is tested on 16.x. |

The full input list lives in `variables.tf`.

## Outputs

| Output | Use |
|---|---|
| `ec2_public_ip` | Hit `http://<ip>/health` once the host bootstraps. |
| `rds_endpoint` | Used internally for `DATABASE_URL`; useful for direct `psql` from the EC2 host. |
| `s3_backup_bucket` | Wire as the `archive_command` target if you self-host Postgres on the EC2 instead of using RDS. See `docs/self-host/postgres-backup.md`. |
| `ssh_command` | Convenience copy-paste. |

## What this module does

1. **VPC** with two public + two private subnets across two AZs.
   Two AZs are required by RDS DB subnet groups even for single-AZ
   instances; the EC2 host lives in a single public subnet.
2. **Security groups** — EC2 allows HTTP from `http_allowed_cidrs`
   and SSH from `ssh_allowed_cidrs`; RDS allows 5432 only from the
   EC2 SG.
3. **EC2 instance** — Amazon Linux 2023, IMDSv2-required, encrypted
   root volume, IAM instance profile attached. The cloud-init user
   data installs Docker + the Compose plugin, clones this repo at
   `velox_repo_ref`, generates a `.env` with a random
   `VELOX_ENCRYPTION_KEY` + `VELOX_BOOTSTRAP_TOKEN` and a
   `DATABASE_URL` pointing at RDS, then runs `docker compose up -d
   nginx velox-api` from `deploy/compose/`.
4. **RDS Postgres** — single-AZ for cost, encrypted at rest, gp3
   storage with autoscaling, backups for `rds_backup_retention_days`.
5. **S3 backup bucket** — versioned, SSE (AES256), Block Public
   Access on, lifecycle rule that tiers `base/` to Glacier Instant
   Retrieval at 30 days, Deep Archive at 90 days, expires at 365
   days; `wal/` expires at 14 days.
6. **IAM** — EC2 instance profile with read/write to the backup
   bucket plus `AmazonSSMManagedInstanceCore` for Session Manager
   fallback.

## What this module does NOT do

- **No Route 53 / TLS.** Front the EC2 with an ALB or your DNS provider; this module ships HTTP-only on port 80 (matches the Compose stack default).
- **No multi-AZ RDS.** Standard pattern is to flip `multi_az = true` on the `aws_db_instance` resource for production; v2 follow-up.
- **No EKS, no Helm, no autoscaling.** Use `deploy/helm/velox/` if you want K8s.
- **No CloudWatch / SNS alarming.** Wire your own; the operational signals come from `/health`, `/health/ready`, and `/metrics` on the API.
- **No automated backup of the EC2 host.** RDS handles the database; the host is treated as cattle and re-launched from scratch on terraform apply.
- **No SES / outbound email setup.** Configure SMTP credentials in the host `.env` or via the Helm chart's `secrets.smtp*` if you switch to K8s.

## Typical edits after first apply

```bash
# Bump Velox image
$EDITOR terraform.tfvars   # update velox_image_tag
terraform apply             # user_data_replace_on_change=true rolls the host

# Lock down SSH after first install
$EDITOR terraform.tfvars   # ssh_allowed_cidrs = []
terraform apply             # SG update is in-place; no host churn

# Resize when you outgrow t3.small
$EDITOR terraform.tfvars   # rds_instance_class = "db.t3.medium"
terraform apply             # RDS does an in-place modify; ~5min downtime
```

## Destroy

```bash
terraform destroy
# Two minutes for VPC + EC2; ~5 for RDS to delete (skip_final_snapshot=true).
# The S3 backup bucket is force-deleted only if empty — empty it first
# if you've shipped backups during the run.
```

## Limitations / known sharp edges

- **`ssh_allowed_cidrs` default is fail-closed-ish** — `203.0.113.0/24` is a documentation-only block per RFC 5737, so apply succeeds but the host is unreachable until you fix it. This is intentional; never default-open.
- **RDS password lives in Terraform state.** State is sensitive; treat the backend store accordingly. Consider rotating via RDS modify post-apply.
- **No automated DNS.** You'll get a public IP. Wire it to Route 53 or your DNS provider out-of-band.
- **Bootstrap token is logged to `/var/log/velox-bootstrap.log` once.** Recover it via SSH; rotate or destroy the file once the first tenant is bootstrapped.
- **The `aws_db_parameter_group` is `postgres16`-family.** Bumping `postgres_engine_version` to a different major (e.g. 17.x) requires changing the family too, which forces parameter-group recreation.

## Where the env-var schema comes from

The user-data template emits a `.env` file matching
`deploy/compose/.env.example` exactly — same required keys
(`POSTGRES_PASSWORD`, `VELOX_ENCRYPTION_KEY`,
`VELOX_BOOTSTRAP_TOKEN`), same `DATABASE_URL` format, same
`APP_ENV=production` default. No invented keys; the binary's actual
reads from `internal/config/config.go` are the source of truth.
