#!/usr/bin/env bash
#
# lint-tz — enforce the timezone-display invariant (ADR-076).
#
# THE RULE: a CIVIL-DAY date shown to a human ("Jun 1, 2026") must be rendered in
# an EXPLICIT timezone via `.In(loc)` — the owning entity's billing/tenant zone.
# It must NOT use:
#   * bare `t.Format(...)`  — renders in the process/host zone (the ADR-058/074/075
#                             bug class: wrong civil day on a non-UTC host); or
#   * `t.UTC().Format(...)` for a customer calendar date — the ADR-075 audit found
#                             this on credit-note descriptions printing a day early.
#
# The invariant used to be "correct by developer discipline"; the arc shipped ~8
# violations of exactly this shape. This gate makes it correct-by-CI: a green run
# is a machine-checked proof that every civil-day render carries an explicit zone.
#
# ESCAPE HATCH: a genuine UTC/instant/filename case (e.g. a download-filename
# stamp) carries a trailing `//tz:ok` comment on the SAME line, which documents
# the exception and passes the gate.
#
# Also asserts the process-UTC pin (ADR-075) is present in the server entrypoint.

set -euo pipefail

fail=0

# ── 1. Civil-day .Format must use .In(loc) or a //tz:ok waiver ────────────────
# Match .Format("<layout>") whose layout contains a DATE token (2006 / January /
# "Jan "). Exclude layouts that also carry a CLOCK component (a ':' inside the
# format string) — those are timestamp serializations, not civil-day renders.
# Then drop anything already rendered .In(a zone) or explicitly waived.
offenders="$(grep -rnE '\.Format\("[^"]*(2006|January|Jan )[^"]*"\)' internal/ cmd/ --include='*.go' \
  | grep -v '_test.go:' \
  | grep -vE '\.Format\("[^"]*:[^"]*"\)' \
  | grep -v '\.In(' \
  | grep -v '//tz:ok' || true)"

if [ -n "$offenders" ]; then
  echo "✗ lint-tz: civil-day .Format() without an explicit .In(loc) zone (ADR-076):"
  echo
  echo "$offenders" | sed 's/^/    /'
  echo
  echo "  Civil-day dates must render in the billing/tenant timezone, e.g.:"
  echo "      t.In(domain.LoadLocationOrUTC(inv.BillingTimezone)).Format(\"2006-01-02\")"
  echo "  A genuine UTC/instant/filename case may carry a trailing  //tz:ok  comment."
  fail=1
fi

# ── 2. The process-UTC pin (ADR-075) must be present ──────────────────────────
if ! grep -q 'time.Local = time.UTC' cmd/velox/main.go; then
  echo "✗ lint-tz: cmd/velox/main.go must pin the process to UTC (time.Local = time.UTC), ADR-075."
  echo "  Without it, pgx decodes timestamptz in the host zone and the API wire leaks a host offset."
  fail=1
fi

if [ "$fail" -eq 0 ]; then
  echo "✓ lint-tz: every civil-day render carries an explicit zone; UTC pin present."
fi
exit "$fail"
