#!/usr/bin/env bash
#
# check-manual-test-currency.sh — cheap anti-rot guard for MANUAL_TEST.md.
#
# The runbook's whole value is "a flow that lies is worse than no flow"
# (CLAUDE.md). CI catches drift in generated artifacts but NOT in prose, so
# stale flows accumulate between the rare full manual runs. This guard catches
# the highest-signal class of lie a solo author keeps re-introducing: a flow
# that names a metric / table / column / meter-key / route that the code no
# longer has. It is deliberately narrow (near-zero false positives), not a
# general "every backtick must exist" check.
#
# Two checks:
#   1. DENYLIST — identifiers that were deleted from the code and must never
#      reappear in MANUAL_TEST.md. Add to REMOVED below whenever a migration
#      or refactor drops a column/table/metric/meter-key the runbook referenced.
#   2. METRIC EXISTENCE — every `velox_*` Prometheus metric named in the runbook
#      must exist somewhere in internal/ Go source (catches renamed/removed
#      metrics like the velox_tax_fallback_total -> velox_tax_outcome_total move).
#
# Bypass: a line that legitimately names a retired token — to explain it was
# removed, or because it's an EXTERNAL field (e.g. Stripe's own `tax_exempt` on
# the Stripe Customer object) — carries a trailing `currency-ok:` HTML comment
# and is skipped, e.g.
#     - [ ] ... no `tax_rate_bp` column exists. <!-- currency-ok: explains removal -->
# (Same idiom as lint-clock's `// wall-clock:` escape.)

set -euo pipefail

DOC="MANUAL_TEST.md"
CODE_DIR="internal"
fail=0

if [ ! -f "$DOC" ]; then
  echo "check-manual-test-currency: $DOC not found (run from repo root)" >&2
  exit 2
fi

# 1. Identifiers deleted from the code — must not appear in the runbook.
#    Each entry: "<token>|<why / what replaced it>".
REMOVED=(
  "tax_home_country|dropped (ADR-038); manual tax is a flat rate, exemption via customer tax_status"
  "tax_rate_bp|dropped (ADR-043); tax_rate NUMERIC(7,4) is the only rate column"
  "tax_exempt|replaced by customer tax_status enum (standard/exempt/reverse_charge)"
  "velox_tax_fallback_total|renamed to velox_tax_outcome_total (ADR-041; zero-tax fallback cut)"
  "tenant_recipe_instances|table is named recipe_instances"
  "tokens_input|canonical meter is tokens with token_type=input (ADR-044)"
  "tokens_output|canonical meter is tokens with token_type=output (ADR-044)"
  "customer_payment_setups|table dropped (migration 0097); composed from customers + payment_methods"
)

# Lines carrying a `currency-ok:` marker are intentional references (removal
# explanations, external/Stripe fields) and are excluded from both checks.
scannable=$(grep -vF 'currency-ok:' "$DOC" || true)

for entry in "${REMOVED[@]}"; do
  token="${entry%%|*}"
  why="${entry##*|}"
  hits=$(printf '%s\n' "$scannable" | grep -nwF "$token" || true)
  if [ -n "$hits" ]; then
    echo "✗ $DOC references removed identifier '$token' — $why" >&2
    printf '%s\n' "$hits" | sed 's/^/    /' >&2
    fail=1
  fi
done

# 2. Every velox_* metric named in the runbook must exist in the code.
metrics=$(printf '%s\n' "$scannable" | grep -oE 'velox_[a-z0-9_]+' | sort -u || true)
for m in $metrics; do
  if ! grep -rqF "$m" "$CODE_DIR" --include='*.go'; then
    echo "✗ $DOC references metric '$m' not found anywhere in $CODE_DIR/**.go (renamed or removed?)" >&2
    fail=1
  fi
done

if [ "$fail" -ne 0 ]; then
  echo "" >&2
  echo "MANUAL_TEST.md drift detected. Fix the flow to match current code, or update" >&2
  echo "the REMOVED denylist in scripts/check-manual-test-currency.sh if a token was" >&2
  echo "intentionally retired. A flow that lies is worse than no flow." >&2
  exit 1
fi

echo "check-manual-test-currency: OK (${#REMOVED[@]} denied identifiers, $(echo "$metrics" | grep -c . || true) metrics verified)"
