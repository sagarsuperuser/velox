#!/usr/bin/env bash
#
# lint-funding-set.sh — guards the FUNDING-SET invariant.
#
# The invariant (the one that, left implicit, caused the 2026-06-15
# multi-invoice proration class — see ADR-048 amendment):
#
#   A subscription PERIOD can be funded by MORE THAN ONE invoice (a base
#   invoice + any mid-period upgrade/qty proration invoice). Any code that
#   computes a period-wide CREDIT, CLAWBACK, or VOID must reconcile against
#   the WHOLE funding set via FindFundingInvoicesForPeriod — never a
#   single-row lookup.
#
# What this enforces:
#   In the proration DECISION packages (subscription, billing), a call to a
#   single-row period lookup — FindBaseInvoiceForPeriod or
#   GetByProrationSource — must carry an explicit justification on the same
#   or immediately-preceding line:
#       // single-invoice-ok: <one-line reason>
#   so a NEW caller can't silently re-introduce the single-invoice assumption
#   without a reviewer signing off. The legitimate uses today are the
#   paid/unpaid routing gate and the proration-dedup replay.
#
# Run with: make lint-funding-set   (exits non-zero on an unjustified call)

set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"

PACKAGES=("internal/subscription" "internal/billing")
# Single-row period/proration lookups that MUST NOT drive a period-wide
# credit/clawback/void decision without justification.
LOOKUPS='\.(FindBaseInvoiceForPeriod|GetByProrationSource)\('

violations=0
violation_lines=""

while IFS= read -r file; do
  [ -z "$file" ] && continue
  while IFS=: read -r line_num line_content; do
    [ -z "$line_num" ] && continue
    # Skip pure comment lines (the lookup named in a doc comment isn't a call).
    if echo "$line_content" | grep -qE "^\s*//"; then
      continue
    fi
    # Accept if THIS line carries the justification.
    if echo "$line_content" | grep -qE "//\s*single-invoice-ok:"; then
      continue
    fi
    # Accept if any of the 3 preceding lines (the leading comment block of a
    # long call) carries the justification.
    start=$((line_num - 3))
    [ "$start" -lt 1 ] && start=1
    prev_block=$(sed -n "${start},$((line_num - 1))p" "$file" 2>/dev/null || true)
    if echo "$prev_block" | grep -qE "//\s*single-invoice-ok:"; then
      continue
    fi
    violations=$((violations + 1))
    violation_lines="${violation_lines}${file}:${line_num}: ${line_content}\n"
  done < <(grep -nE "$LOOKUPS" "$file" 2>/dev/null || true)
done < <(for pkg in "${PACKAGES[@]}"; do find "$pkg" -name "*.go" -not -name "*_test.go" 2>/dev/null || true; done)

if [ "$violations" -gt 0 ]; then
  echo "FUNDING-SET invariant violation: $violations single-invoice lookup(s) in proration decision code without justification."
  echo ""
  echo -e "$violation_lines" | head -50
  echo ""
  echo "A period can be funded by MULTIPLE invoices. Period-wide credit/clawback/void"
  echo "must use FindFundingInvoicesForPeriod, not a single-row lookup."
  echo "If a single-row lookup is genuinely correct here (e.g. a routing gate or a"
  echo "dedup replay), add a same-line or preceding-line justification:"
  echo "    // single-invoice-ok: <one-line reason>"
  echo ""
  echo "See docs/adr/048-credit-clawback-tax-reversal.md (Amendment)."
  exit 1
fi

echo "lint-funding-set: ok (${#PACKAGES[@]} packages scanned, all single-invoice lookups justified)"
