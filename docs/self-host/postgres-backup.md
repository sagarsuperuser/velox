# Postgres backup and restore for self-hosted Velox

A tested recipe for point-in-time-recovery (PITR) backups of the
`velox` database using `pg_basebackup` plus continuous WAL archiving.
Restore drill at the bottom — run it before you need it.

## Why PITR (and not just `pg_dump`)

`pg_dump` is a logical snapshot, fine for migrations between major
versions or moving to managed Postgres. For self-host operations you
want **physical** backups + WAL streaming because:

- Restore is fast (file-system copy, not row-by-row INSERT replay).
- You can recover to any second within the WAL retention window, not
  just the snapshot boundary.
- The base backup uses copy-on-write read transactions — no table
  locks, no impact on the running workload.

References:

- [`pg_basebackup`](https://www.postgresql.org/docs/16/app-pgbasebackup.html) — the tool we use for the base backup.
- [Continuous Archiving and PITR](https://www.postgresql.org/docs/16/continuous-archiving.html) — the manual chapter behind every recipe below.
- [Backup and Restore — Best Practices](https://www.postgresql.org/docs/16/backup.html) — chapter overview.

## Recipe overview

You need three things: a base backup taken on a schedule, continuous
WAL files shipped to durable storage, and a tested restore procedure.

```
                          +---------------------+
                          |  S3 / object store  |
                          +----+-----------+----+
                               ^           ^
        nightly base backup    |           |   continuous WAL archive
                               |           |
                          +----+-----------+----+
                          |   primary postgres  |
                          +---------------------+
```

For a **managed Postgres** (RDS, Cloud SQL, Supabase, Neon) this is
already handled — verify automated backups are on, set the retention
window to match your RTO/RPO, and skip the rest of this page. The
recipe below is for self-managed Postgres on a VM.

## Step 1 — enable WAL archiving on the primary

In `postgresql.conf`:

```conf
# Required for archiving and replication
wal_level = replica

# Make sure WAL doesn't get recycled before it's archived
archive_mode = on
archive_command = 'test ! -f /var/lib/postgresql/wal-archive/%f && cp %p /var/lib/postgresql/wal-archive/%f'

# Keep enough WAL so a slow base backup can complete
max_wal_size = 1GB
min_wal_size = 80MB

# Optional but recommended — checksums catch silent disk corruption
# (only effective if the cluster was initdb'd with --data-checksums)
```

In the Compose stack from `deploy/compose/`, add a bind-mount for the
archive directory and set the same options via `command: postgres -c`:

```yaml
postgres:
  command: >
    postgres
    -c wal_level=replica
    -c archive_mode=on
    -c archive_command='test ! -f /wal-archive/%f && cp %p /wal-archive/%f'
    -c max_wal_size=1GB
  volumes:
    - pgdata:/var/lib/postgresql/data
    - ./wal-archive:/wal-archive
    - ./postgres-init.sql:/docker-entrypoint-initdb.d/00-init.sql:ro
```

Reload Postgres after the change (`SELECT pg_reload_conf();` or
restart the container). Confirm:

```sql
SHOW archive_mode;
SHOW archive_command;
SELECT pg_walfile_name(pg_current_wal_lsn());
-- A new file should appear under /wal-archive/ within a minute.
```

For production, replace the `cp` archive_command with a script that
ships to S3 or another off-host store; see [Archiving via shell command](https://www.postgresql.org/docs/16/continuous-archiving.html#BACKUP-ARCHIVING-WAL).
The `archive_command` must exit non-zero on failure or Postgres will
think the WAL is safe and move on.

## Step 2 — schedule a base backup

A base backup is a consistent on-disk copy of the cluster. Take one
nightly (more often if your data churns fast).

```bash
TIMESTAMP=$(date -u +%Y%m%dT%H%M%SZ)
DEST="/backups/base/${TIMESTAMP}"
mkdir -p "${DEST}"

# Run as the postgres OS user, or pass --username + --no-password with
# a .pgpass entry. Tar format compresses well and is easy to ship.
pg_basebackup \
  --pgdata="${DEST}" \
  --format=tar \
  --gzip \
  --progress \
  --verbose \
  --checkpoint=fast \
  --wal-method=stream
```

What each flag does, drawn from the
[`pg_basebackup` docs](https://www.postgresql.org/docs/16/app-pgbasebackup.html):

| Flag | Why |
|---|---|
| `--format=tar` | Single tarball per tablespace, easy to ship |
| `--gzip` | Compression — Velox's tables compress 4–10x |
| `--checkpoint=fast` | Don't wait up to `checkpoint_timeout` for backup to start |
| `--wal-method=stream` | Open a second connection to stream WAL alongside the backup so the resulting set is self-contained |
| `--progress --verbose` | Visible during long backups; safe to drop in cron |

Cron example (nightly at 02:15 UTC, ship to S3, prune old):

```cron
15 2 * * * /usr/local/bin/velox-pg-backup.sh
```

`velox-pg-backup.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail

TS=$(date -u +%Y%m%dT%H%M%SZ)
LOCAL=/var/lib/postgresql/backups/base/${TS}
mkdir -p "${LOCAL}"

su - postgres -c "pg_basebackup \
  --pgdata=${LOCAL} \
  --format=tar --gzip \
  --checkpoint=fast \
  --wal-method=stream"

aws s3 sync "${LOCAL}" "s3://acme-velox-backups/base/${TS}/" \
  --storage-class STANDARD_IA

# Local retention — 3 days. S3 lifecycle handles long-term retention.
find /var/lib/postgresql/backups/base/ -mindepth 1 -maxdepth 1 -type d -mtime +3 -exec rm -rf {} +
```

## Step 3 — retention recommendation

Mirror your RTO/RPO requirements; below is a sensible default for an
early-production self-host:

| Tier | Retention | Storage class |
|---|---|---|
| Last 7 daily base backups | 7 days | Hot (S3 Standard / Standard-IA) |
| Weekly base backup | 4 weeks | Cool (S3 Glacier Instant Retrieval) |
| Monthly base backup | 12 months | Cold (S3 Glacier Deep Archive) |
| WAL archive | 7 days minimum | Hot — it's PITR fuel |

Implement with S3 Lifecycle rules; don't try to roll your own pruner.
For non-S3 stores the equivalent (GCS Object Lifecycle, Azure Blob
tiering) works identically.

## Step 4 — restore drill (run quarterly)

A backup you've never tested is a backup you don't have. Drill on a
fresh VM, never on the primary. The drill below recovers to a specific
point in time using the base backup + archived WAL.

```bash
# 1. Stop any running cluster on the drill host.
sudo systemctl stop postgresql

# 2. Wipe the data directory (safety: confirm twice).
sudo rm -rf /var/lib/postgresql/16/main/*

# 3. Pull the most recent base backup from S3.
aws s3 sync s3://acme-velox-backups/base/20260425T021500Z/ /tmp/restore/

# 4. Extract the base backup into the cluster's data directory.
sudo -u postgres tar -xzf /tmp/restore/base.tar.gz -C /var/lib/postgresql/16/main/

# 5. Tell Postgres how to fetch archived WAL.
sudo -u postgres tee /var/lib/postgresql/16/main/postgresql.auto.conf <<EOF
restore_command = 'aws s3 cp s3://acme-velox-backups/wal/%f %p'
recovery_target_time = '2026-04-26 14:32:00 UTC'
recovery_target_action = 'promote'
EOF

# 6. Trigger recovery — Postgres looks for this file at startup.
sudo -u postgres touch /var/lib/postgresql/16/main/recovery.signal

# 7. Start the cluster. Watch the log for "consistent recovery state reached"
#    followed by "archive recovery complete".
sudo systemctl start postgresql
sudo journalctl -u postgresql -f
```

Verify the recovered cluster:

```sql
-- Are tenants present and counts plausible?
SELECT count(*) FROM tenants;
SELECT max(created_at) FROM invoices;

-- Migration version matches what the binary expects (see schema_migrations).
SELECT version, dirty FROM schema_migrations;
```

If the drill restore boots cleanly, applies migrations cleanly, and
serves a `GET /v1/customers` request, the recipe works. **Document
how long the drill took** — that's your RTO floor.

The full chapter on this is
[Recovery Configuration](https://www.postgresql.org/docs/16/runtime-config-wal.html#RUNTIME-CONFIG-WAL-ARCHIVE-RECOVERY)
and the [PITR walkthrough](https://www.postgresql.org/docs/16/continuous-archiving.html#BACKUP-PITR-RECOVERY).

## What this doesn't cover

- **Logical backups for cross-version migrations** — use `pg_dump
  --format=custom` for that. PITR backups can't cross major Postgres
  versions.
- **Encryption at rest of the backup files** — handled at the storage
  layer (S3 SSE, GCS CMEK). Don't try to encrypt the tarball itself
  client-side; you'll regret it during a restore.
- **High-availability / hot standby** — orthogonal. PITR is your last
  line of defence; HA is your first. A standard pattern is async
  streaming replica + WAL archive both pointing at S3.
- **Encryption keys** — Velox PII is encrypted at-rest by
  `VELOX_ENCRYPTION_KEY`. **Back up that key separately** (e.g. AWS
  KMS, 1Password vault). A Postgres backup without the key is
  unreadable for any encrypted column.
