#!/usr/bin/env bash
# migration-safety-test.sh — driver for cmd/velox-migrate-safety.
#
# Runs the populated-DB migration safety pass against a local Postgres in
# Docker (the same one the dev loop uses). NOT wired into CI: a full
# medium-scale pass (5M usage_events) takes ~6 minutes, which is too slow
# for every push. Re-run manually when:
#
#   * Adding a migration that touches a hot table (usage_events, invoices,
#     subscriptions, audit_log, customer_credit_ledger, webhook_outbox,
#     email_outbox).
#   * Before promoting any branch to main during Phase 3 (Week 9+).
#   * Quarterly, against the latest migration set, to re-check for drift
#     as table footprints grow.
#
# Findings are tracked in the internal velox-ops repo. After running, diff
# new CSV against the prior snapshot to spot regressions.
#
# Usage:
#   scripts/migration-safety-test.sh [scale]
#
# Scale presets:
#   tiny   — 24 events, 12 invoices       (~3s, smoke test)
#   small  — 500k events, 125k invoices   (~50s, default)
#   medium — 5M events, 100k invoices     (~6min, recommended)
#   large  — 20M events, 200k invoices    (~25min, only if you have time)
#
# Override individual knobs with the SCALE env var, e.g.:
#   SCALE="tenants=10,customers_per_tenant=100" scripts/migration-safety-test.sh
#
# Output: prints CSV report path and the highest-risk migrations.

set -euo pipefail

PRESET="${1:-small}"
SCALE_OVERRIDE="${SCALE:-}"

case "$PRESET" in
  tiny)
    DEFAULT_SCALE="tenants=2,customers_per_tenant=5,subs_per_tenant=3,events_per_sub=4,invoices_per_sub=2,audit_per_tenant=10"
    ;;
  small)
    DEFAULT_SCALE="tenants=50,customers_per_tenant=200,subs_per_tenant=500,events_per_sub=20,invoices_per_sub=5,audit_per_tenant=1000"
    ;;
  medium)
    DEFAULT_SCALE="tenants=20,customers_per_tenant=1000,subs_per_tenant=500,events_per_sub=500,invoices_per_sub=10,audit_per_tenant=5000"
    ;;
  large)
    DEFAULT_SCALE="tenants=40,customers_per_tenant=1000,subs_per_tenant=500,events_per_sub=1000,invoices_per_sub=10,audit_per_tenant=10000"
    ;;
  *)
    echo "unknown preset: $PRESET (want tiny|small|medium|large)" >&2
    exit 1
    ;;
esac

EFFECTIVE_SCALE="${SCALE_OVERRIDE:-$DEFAULT_SCALE}"

SCRATCH_DB="${SCRATCH_DB:-velox_migrate_safety}"
ADMIN_URL="${ADMIN_DATABASE_URL:-postgres://velox:velox@localhost:5432/postgres?sslmode=disable}"
REPORT_PATH="${REPORT_PATH:-/tmp/velox-migration-safety-${PRESET}.csv}"

echo "Velox migration safety pass — preset=$PRESET"
echo "  scale         $EFFECTIVE_SCALE"
echo "  scratch db    $SCRATCH_DB"
echo "  admin url     $ADMIN_URL"
echo "  report path   $REPORT_PATH"
echo

# Re-use existing scratch DB only if user asks; default is drop+recreate.
go run ./cmd/velox-migrate-safety \
  --admin-url "$ADMIN_URL" \
  --scratch-db "$SCRATCH_DB" \
  --scale "$EFFECTIVE_SCALE" \
  --report "$REPORT_PATH"

echo
echo "Top risky migrations (>1s up or down):"
awk -F, '$1 ~ /^(up|down)$/ && $3 > 1000 {printf "  %-4s %4d  %5dms  lock=%dms on %-22s\n", $1, $2, $3, $4, $5}' "$REPORT_PATH"
