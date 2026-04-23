# Velox Backup and Disaster Recovery Runbook

## Recovery Objectives

| Metric | Target | Rationale |
|--------|--------|-----------|
| **RPO** (Recovery Point Objective) | 5 minutes | Financial data — invoices, ledger entries, payments — cannot be lost. WAL archiving provides continuous protection. |
| **RTO** (Recovery Time Objective) | 1 hour | Billing downtime directly impacts revenue collection. Restore from base backup + WAL replay must complete within this window. |

---

## 1. Backup Strategy Overview

Velox uses a layered backup approach:

| Layer | Tool | Frequency | Retention | Purpose |
|-------|------|-----------|-----------|---------|
| WAL archiving | WAL-G or pgBackRest | Continuous (every completed WAL segment) | 30 days | Point-in-time recovery to any moment within retention window |
| Base backup (physical) | WAL-G `backup-push` | Weekly (Sunday 02:00 UTC) | 30 days | Foundation for PITR — full filesystem-level snapshot |
| Incremental backup (physical) | WAL-G `backup-push` with delta | Daily (02:00 UTC, Mon-Sat) | 30 days | Reduces backup size and duration between full backups |
| Logical backup | `pg_dump` via `scripts/backup.sh` | Daily (03:00 UTC) | 30 days on disk, 90 days in S3 | Portable format for selective restores, cross-version migrations |
| Streaming replica | PostgreSQL streaming replication | Continuous | N/A | High availability failover |

---

## 2. WAL Archiving Setup (WAL-G)

WAL-G is the recommended tool for physical backups. It handles compression, encryption, and S3/GCS upload natively.

### 2.1 Install WAL-G

```bash
# Linux (amd64)
curl -L https://github.com/wal-g/wal-g/releases/download/v3.0.3/wal-g-pg-ubuntu-20.04-amd64 \
  -o /usr/local/bin/wal-g
chmod +x /usr/local/bin/wal-g

# Verify
wal-g --version
```

### 2.2 Configure PostgreSQL for WAL Archiving

Add to `postgresql.conf`:

```ini
# WAL settings
wal_level = replica
archive_mode = on
archive_command = 'wal-g wal-push %p'
archive_timeout = 60

# Ensure enough WAL for backup + replay
max_wal_senders = 5
wal_keep_size = 1024
```

Reload after changes:

```bash
psql -U velox -c "SELECT pg_reload_conf();"
```

### 2.3 Configure WAL-G Environment

Create `/etc/wal-g/env.sh` (sourced by backup scripts and archive_command):

```bash
export WALG_S3_PREFIX=s3://velox-backups/wal-g
export AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE
export AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY
export AWS_REGION=us-east-1

# Encryption (recommended for financial data)
export WALG_LIBSODIUM_KEY_PATH=/etc/wal-g/encryption-key

# Compression
export WALG_COMPRESSION_METHOD=lz4

# Retention
export WALG_RETENTION_FULL_BACKUP_COUNT=5
```

For GCS instead of S3:

```bash
export WALG_GS_PREFIX=gs://velox-backups/wal-g
export GOOGLE_APPLICATION_CREDENTIALS=/etc/wal-g/service-account.json
```

### 2.4 Create the Encryption Key

```bash
mkdir -p /etc/wal-g
head -c 32 /dev/urandom | base64 > /etc/wal-g/encryption-key
chmod 600 /etc/wal-g/encryption-key
```

**Store a copy of this key in your secrets manager.** Without it, backups are unrecoverable.

### 2.5 Take the Initial Base Backup

```bash
source /etc/wal-g/env.sh
wal-g backup-push /var/lib/postgresql/16/main
```

Verify:

```bash
wal-g backup-list
```

### 2.6 Schedule Backups via Cron

Add to the `postgres` user's crontab:

```cron
# Full base backup every Sunday at 02:00 UTC
0 2 * * 0  source /etc/wal-g/env.sh && wal-g backup-push /var/lib/postgresql/16/main --full 2>&1 | logger -t wal-g-full

# Delta (incremental) backup Mon-Sat at 02:00 UTC
0 2 * * 1-6  source /etc/wal-g/env.sh && wal-g backup-push /var/lib/postgresql/16/main --delta-from-user-data 2>&1 | logger -t wal-g-delta

# Logical backup daily at 03:00 UTC
0 3 * * *  /opt/velox/scripts/backup.sh 2>&1 | logger -t velox-logical-backup

# Prune old backups weekly (keep 5 full + associated WAL)
0 4 * * 0  source /etc/wal-g/env.sh && wal-g delete retain FULL 5 --confirm 2>&1 | logger -t wal-g-prune
```

---

## 3. Logical Backup (pg_dump)

Logical backups complement physical backups. They are portable across PostgreSQL versions and allow selective table restores.

Run the provided script:

```bash
DATABASE_URL="postgres://velox:velox@localhost:5432/velox?sslmode=disable" \
  ./scripts/backup.sh
```

For S3 upload, ensure `aws` CLI is configured:

```bash
DATABASE_URL="postgres://velox:velox@localhost:5432/velox?sslmode=disable" \
  BACKUP_S3_BUCKET=velox-backups \
  ./scripts/backup.sh
```

See `scripts/backup.sh` for full details.

---

## 4. Backup Validation

Backups that are never tested are not backups. They are hopes.

### 4.1 Automated Integrity Check (Weekly)

Run after each full backup to verify the archive is not corrupted:

```bash
source /etc/wal-g/env.sh
wal-g backup-verify /var/lib/postgresql/16/main
```

### 4.2 Logical Backup Verification (Daily)

The `scripts/backup.sh` script verifies each backup with `pg_restore --list` before completing. If verification fails, the script exits non-zero.

### 4.3 Monthly Restore Test

**This is mandatory. Schedule it. Do not skip it.**

Procedure:

1. Provision a throwaway PostgreSQL instance (use Docker):

```bash
docker run -d --name velox-restore-test \
  -e POSTGRES_DB=velox_restore \
  -e POSTGRES_USER=velox \
  -e POSTGRES_PASSWORD=velox \
  -p 15432:5432 \
  postgres:16-alpine
```

2. Restore the latest logical backup:

```bash
./scripts/restore.sh /var/backups/velox/latest.dump
```

Or for WAL-G (PITR to latest):

```bash
source /etc/wal-g/env.sh
wal-g backup-fetch /tmp/velox-restore LATEST
```

3. Verify data integrity:

```bash
psql -U velox -d velox_restore -p 15432 -c "
  SELECT 'invoices' AS table_name, count(*) FROM invoices
  UNION ALL
  SELECT 'credit_ledger', count(*) FROM credit_ledger_entries
  UNION ALL
  SELECT 'payments', count(*) FROM payments
  UNION ALL
  SELECT 'subscriptions', count(*) FROM subscriptions;
"
```

4. Verify the application can connect and serve requests against the restored database.

5. Record the result (pass/fail, duration, any issues) in the ops log.

6. Destroy the test instance:

```bash
docker rm -f velox-restore-test
```

### 4.4 Automated Drill

`scripts/restore-drill.sh` wraps §4.3 as a single command so the drill can be
wired into CI or cron without a human following 6 steps every month.

```bash
SOURCE_DATABASE_URL="postgres://velox:velox@prod-host:5432/velox?sslmode=require" \
  ./scripts/restore-drill.sh
```

What it does:

1. Captures row counts for critical tables (`tenants`, `customers`,
   `subscriptions`, `invoices`, `credit_ledger_entries`) from the source DB.
2. Runs `scripts/backup.sh` into a temporary workdir (no S3 upload, no
   interference with production backup rotation).
3. Starts a one-shot `postgres:16-alpine` container on port `15432`.
4. Runs `scripts/restore.sh` against the container with
   `VELOX_RESTORE_CONFIRM=yes`.
5. Re-reads the same row counts from the restored DB and compares.
6. Appends a `PASS|FAIL` line with timings to `~/.velox/drill.log`.
7. Tears down the container on every exit path (including `Ctrl-C`).

Exit codes:

- `0` pass — all table counts match
- `2` backup failed
- `3` restore failed
- `4` row-count mismatch
- `5` ephemeral Postgres didn't come up

The `~/.velox/drill.log` history file lets you spot duration trend-over-time:
if yesterday's drill took 90s and today's took 900s, backup size or DB
contention changed and that's a signal worth investigating before the real
RPO/RTO bites during an outage.

**Schedule:** add to cron on the ops host (or a dedicated runner):

```cron
# Monthly drill, first Sunday at 04:00 UTC, against production read-replica
0 4 1-7 * 0  SOURCE_DATABASE_URL=postgres://velox:PW@replica:5432/velox /opt/velox/scripts/restore-drill.sh 2>&1 | logger -t velox-drill
```

Point at a read-replica in production, not the primary, so the backup step
doesn't add load to the write path.

---

## 5. Disaster Recovery Procedures

### 5.1 Full Database Restore from Physical Backup (WAL-G)

**When to use:** Total database loss, corrupted data directory, hardware failure.

**Estimated time:** 15-45 minutes depending on database size.

```bash
# 1. Stop PostgreSQL
sudo systemctl stop postgresql

# 2. Clear the data directory (DANGER — only after confirming backups exist)
sudo rm -rf /var/lib/postgresql/16/main/*

# 3. Fetch the latest base backup
source /etc/wal-g/env.sh
wal-g backup-fetch /var/lib/postgresql/16/main LATEST

# 4. Create recovery signal file for WAL replay
touch /var/lib/postgresql/16/main/recovery.signal

# 5. Configure recovery in postgresql.auto.conf
cat >> /var/lib/postgresql/16/main/postgresql.auto.conf <<EOF
restore_command = 'wal-g wal-fetch %f %p'
recovery_target = 'immediate'
recovery_target_action = 'promote'
EOF

# 6. Fix ownership
sudo chown -R postgres:postgres /var/lib/postgresql/16/main

# 7. Start PostgreSQL — it will replay WAL segments automatically
sudo systemctl start postgresql

# 8. Monitor recovery progress
sudo tail -f /var/log/postgresql/postgresql-16-main.log

# 9. After recovery completes, verify
psql -U velox -d velox -c "SELECT pg_is_in_recovery();"
# Should return 'f' (false) after promotion
```

### 5.2 Point-in-Time Recovery (PITR)

**When to use:** Accidental data deletion, bad migration, need to recover to a specific moment.

**Estimated time:** 15-45 minutes.

```bash
# 1. Stop PostgreSQL
sudo systemctl stop postgresql

# 2. Clear data directory
sudo rm -rf /var/lib/postgresql/16/main/*

# 3. Fetch base backup
source /etc/wal-g/env.sh
wal-g backup-fetch /var/lib/postgresql/16/main LATEST

# 4. Configure PITR target — recover to just BEFORE the incident
#    Replace the timestamp with the actual target time (UTC)
cat >> /var/lib/postgresql/16/main/postgresql.auto.conf <<EOF
restore_command = 'wal-g wal-fetch %f %p'
recovery_target_time = '2026-04-15 14:30:00 UTC'
recovery_target_action = 'pause'
EOF

touch /var/lib/postgresql/16/main/recovery.signal

# 5. Fix ownership and start
sudo chown -R postgres:postgres /var/lib/postgresql/16/main
sudo systemctl start postgresql

# 6. Verify the state at the recovery point
psql -U velox -d velox -c "SELECT pg_is_in_recovery();"
# Should return 't' (true) — paused at target

# 7. Inspect data to confirm you recovered to the right point
psql -U velox -d velox -c "SELECT id, created_at FROM invoices ORDER BY created_at DESC LIMIT 5;"

# 8. If satisfied, promote to primary
psql -U velox -d velox -c "SELECT pg_wal_replay_resume();"
# Or: SELECT pg_promote();

# 9. Remove recovery settings from postgresql.auto.conf
psql -U velox -d velox -c "ALTER SYSTEM RESET restore_command;"
psql -U velox -d velox -c "ALTER SYSTEM RESET recovery_target_time;"
psql -U velox -d velox -c "ALTER SYSTEM RESET recovery_target_action;"
psql -U velox -d velox -c "SELECT pg_reload_conf();"
```

### 5.3 Restore from Logical Backup

**When to use:** Selective table restore, cross-version migration, or when physical backup is unavailable.

```bash
# Full database restore
./scripts/restore.sh /var/backups/velox/velox_20260415_030000.dump

# Selective table restore (e.g., just invoices)
pg_restore --dbname=velox --table=invoices --data-only \
  /var/backups/velox/velox_20260415_030000.dump
```

### 5.4 Failover to Streaming Replica

**When to use:** Primary is down and cannot be recovered quickly. RTO is being exceeded.

**Prerequisites:** A streaming replica must already be running.

```bash
# 1. Verify replica is caught up (run on replica)
psql -U velox -c "SELECT pg_last_wal_replay_lsn(), pg_last_wal_receive_lsn();"

# 2. Promote the replica
sudo -u postgres pg_ctlcluster 16 main promote
# Or: psql -U velox -c "SELECT pg_promote();"

# 3. Verify promotion
psql -U velox -c "SELECT pg_is_in_recovery();"
# Should return 'f' (false)

# 4. Update application connection strings to point to the new primary
#    In Kubernetes: update the velox-secrets Secret with new DATABASE_URL
kubectl -n velox patch secret velox-secrets \
  --type='json' \
  -p='[{"op":"replace","path":"/stringData/DATABASE_URL","value":"postgres://velox:PASSWORD@new-primary:5432/velox?sslmode=require"}]'

# 5. Restart application pods to pick up new connection
kubectl -n velox rollout restart deployment/velox

# 6. After the original primary is recovered, set it up as the new replica
```

### 5.5 Recovery from Accidental Data Deletion

**When to use:** An operator or bug deleted rows that should not have been deleted.

For a small number of rows, use PITR on a separate instance and copy the data back:

```bash
# 1. Restore to a temporary database at the point just before deletion
docker run -d --name velox-pitr-temp \
  -e POSTGRES_DB=velox \
  -e POSTGRES_USER=velox \
  -e POSTGRES_PASSWORD=velox \
  -p 15432:5432 \
  postgres:16-alpine

# 2. Restore from logical backup taken before the deletion
pg_restore --dbname="postgres://velox:velox@localhost:15432/velox" \
  /var/backups/velox/velox_BEFORE_DELETION.dump

# 3. Copy the missing rows back to production
pg_dump -t invoices --data-only --column-inserts \
  "postgres://velox:velox@localhost:15432/velox" \
  | psql "postgres://velox:PASSWORD@production:5432/velox"

# 4. Clean up
docker rm -f velox-pitr-temp
```

---

## 6. Data Retention Policy

Velox handles financial data subject to regulatory requirements.

| Data Category | Retention Period | Rationale | Implementation |
|--------------|-----------------|-----------|----------------|
| Invoices | 7 years | Tax/accounting law (IRS, HMRC, EU VAT Directive) | Never hard-delete. Soft-delete with `deleted_at`. |
| Credit ledger entries | 7 years | Financial audit trail, immutable by design | Append-only table. No deletes. |
| Payment records | 7 years | PCI DSS record-keeping, dispute resolution | Never hard-delete. |
| Subscription history | 7 years | Billing dispute resolution | Soft-delete. |
| Customer records | 7 years after last invoice | Tax authority requirements | GDPR: anonymize PII, retain financial links. |
| Usage events (raw) | 90 days | Operational. Aggregated data persists in invoices. | Partition by month, drop old partitions. |
| Audit log entries | 7 years (summary), 90 days (detail payload) | Security audit requirements | Prune `details` JSON after 90 days, keep summary row. |
| Webhook delivery logs | 30 days | Debugging only | Hard delete via scheduled job. |
| API request logs | 30 days | Debugging only | Hard delete or rotate log files. |

### Implementing Usage Event Partitioning

```sql
-- Partition usage_events by month for efficient retention
ALTER TABLE usage_events RENAME TO usage_events_old;

CREATE TABLE usage_events (
    LIKE usage_events_old INCLUDING ALL
) PARTITION BY RANGE (timestamp);

-- Create monthly partitions
CREATE TABLE usage_events_2026_04 PARTITION OF usage_events
    FOR VALUES FROM ('2026-04-01') TO ('2026-05-01');

-- Drop partitions older than 90 days
DROP TABLE IF EXISTS usage_events_2026_01;
```

### Implementing Audit Log Pruning

```sql
-- Run monthly: strip detail payloads older than 90 days
UPDATE audit_log
SET details = '{"pruned": true}'::jsonb
WHERE created_at < NOW() - INTERVAL '90 days'
  AND details != '{"pruned": true}'::jsonb;
```

---

## 7. Monitoring and Alerting

### 7.1 Backup Health Checks

Add these to your monitoring system (Prometheus + Alertmanager, Datadog, PagerDuty, etc.):

| Alert | Condition | Severity |
|-------|-----------|----------|
| Backup age too old | Latest WAL-G backup older than 25 hours | **Critical** |
| WAL archiving stopped | `pg_stat_archiver.last_archived_time` older than 10 minutes | **Critical** |
| WAL archive failures | `pg_stat_archiver.failed_count` increases | **Warning** |
| Logical backup failed | `scripts/backup.sh` exit code != 0 | **Critical** |
| Replica lag | `pg_stat_replication.replay_lag` > 5 minutes | **Critical** (RPO breach) |
| Backup size anomaly | Backup size deviates > 50% from 7-day average | **Warning** |
| Disk space for WAL | WAL directory > 80% full | **Critical** |

### 7.2 Prometheus Queries

```yaml
# WAL archiving status
- alert: WALArchivingStopped
  expr: (time() - pg_stat_archiver_last_archive_time) > 600
  for: 5m
  labels:
    severity: critical
  annotations:
    summary: "WAL archiving has stopped"
    description: "No WAL segment archived in the last 10 minutes."

# Replication lag
- alert: ReplicationLagHigh
  expr: pg_stat_replication_replay_lag > 300
  for: 2m
  labels:
    severity: critical
  annotations:
    summary: "Replication lag exceeds RPO (5 minutes)"
    description: "Replica is {{ $value }}s behind primary."
```

### 7.3 Manual Health Check

Run this at any time to verify backup infrastructure is healthy:

```bash
# Check WAL archiving status
psql -U velox -c "SELECT * FROM pg_stat_archiver;"

# Check replication status (run on primary)
psql -U velox -c "SELECT client_addr, state, sent_lsn, replay_lsn, replay_lag FROM pg_stat_replication;"

# List recent WAL-G backups
source /etc/wal-g/env.sh
wal-g backup-list

# Check latest logical backup
ls -lah /var/backups/velox/ | tail -5
```

---

## 8. Runbook Checklist

Use this checklist when setting up backup infrastructure for a new environment.

- [ ] PostgreSQL `wal_level` set to `replica`
- [ ] `archive_mode = on` and `archive_command` configured
- [ ] WAL-G installed and configured with S3/GCS credentials
- [ ] Encryption key generated and stored in secrets manager
- [ ] Initial base backup taken and verified
- [ ] Cron jobs scheduled (full weekly, delta daily, logical daily, prune weekly)
- [ ] `scripts/backup.sh` tested manually
- [ ] `scripts/restore.sh` tested on throwaway instance
- [ ] Streaming replica configured and replicating
- [ ] Monitoring alerts configured (backup age, WAL lag, archive failures)
- [ ] Monthly restore test scheduled in team calendar
- [ ] Backup credentials rotated and stored in secrets manager
- [ ] Data retention policy implemented (partitioning, pruning jobs)
- [ ] This runbook reviewed by at least one other engineer
