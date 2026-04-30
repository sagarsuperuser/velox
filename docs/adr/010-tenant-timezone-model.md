# ADR-010: Tenant Timezone Model

## Status
Accepted

## Date
2026-05-01

## Context

Until this commit, every date input on the Velox dashboard interpreted picks
in the operator's *browser* timezone, every display rendered in browser local,
and no rendered timestamp carried a timezone label. This created two real
problems:

1. **Operator-pick semantics shift across operators.** An operator in
   `Asia/Kolkata` picking "May 5" as an API key expiry got `May 5 23:59:59
   IST` (= `May 5 18:29:59 UTC`). An operator in `America/Los_Angeles`
   picking "May 5" got `May 5 23:59:59 PDT` (= `May 6 06:59:59 UTC`).
   Same picked date, different absolute expiry — the *business* didn't have
   a single answer to "when does this key expire?".

2. **Picker behavior wasn't transparent.** The "Key will expire on May 5,
   2026" hint hid the time and timezone. Operators had no surface
   indication that their browser TZ was the silent input.

A 2026-05-01 deep research pass across Stripe, GitHub, Auth0, Salesforce,
HubSpot, Linear, Recurly, Chargebee, and Zuora identified two industry
camps:

- **UTC-everywhere camp** (Stripe, GitHub, Auth0): wire/storage/billing math
  all UTC; account/tenant timezone is a *display + report-windowing*
  preference only. Stripe explicitly does NOT shift `billing_cycle_anchor`
  or `current_period_end` based on `account.timezone`.
- **Site-TZ camp** (Recurly, Chargebee, Linear): tenant TZ governs display
  AND billing-day interpretation, locked at schedule creation time.
- **Two-level camp** (Salesforce, HubSpot): both org TZ and per-user TZ;
  most date inputs respect both. Right shape for CRM-scale tools, overkill
  for a billing engine.

Velox is a developer-facing API + operator dashboard, audience-aligned with
Stripe and GitHub. The right philosophical anchor is the UTC-everywhere
camp, with tenant TZ as a *display + civil-date-input* preference.

The infrastructure for tenant TZ already existed:
`tenant_settings.timezone` shipped in migration 0001,
`domain.TenantSettings.Timezone` is plumbed through the service and API,
and the Settings page exposes a TZ Combobox. What was missing was *using
it* — the dashboard had a tenant TZ field with no semantic effect.

## Decision

### Storage — UTC everywhere

All instants stored as Postgres `timestamptz` (already the case). No naive
`timestamp` columns for instants. Date-only fields like a future
`coupon.valid_until_date` would use `DATE` (no timezone), Salesforce's
clean date-vs-datetime split.

### Wire format — UTC ISO 8601

All `*_at` fields on the wire are RFC 3339 with `Z` suffix. No naive local
strings. Matches Stripe and GitHub conventions; sidesteps Zuora's
public-cautionary-tale of returning naive local strings whose meaning
silently shifted when the tenant changed TZ.

### Tenant timezone — single setting, IANA name, default UTC

`tenant_settings.timezone` already exists, default `'UTC'`, validated as a
real IANA zone name on write. Used for:

- **Date-grade input interpretation.** When an operator picks "May 5" in
  a calendar widget — for API key expiry, coupon valid-until, future
  cancel-at, etc. — the dashboard interprets that picked civil date as
  end-of-day in tenant timezone, then converts to UTC for the wire.
- **Display.** Every rendered timestamp on the dashboard renders in tenant
  timezone with an explicit zone label (`"May 5, 2026 at 11:59 PM PDT"`).
  No bare dates. The label closes the round-trip-uncertainty loop —
  operators see exactly what timezone the value is in.
- **Future**: report windows, scheduled-job interpretation, dashboard
  "today" rendering.

### Subscription billing math — UTC, follow Stripe

`current_billing_period_start/end`, `billing_cycle_anchor` stay UTC.
"Monthly on the 5th" means *5th in UTC*, not 5th in tenant TZ. This
matches Stripe's model and avoids the DST + retroactive-shift pitfalls
that Chargebee documents. Velox's billing engine is already TZ-agnostic
absolute and remains so.

### Per-user timezone — none in v1

Velox has 1 operator and 0 design partners. A per-user TZ override is
pure speculation and would require a `users.timezone` column, a settings
page, and rendering plumbing. Defer until a multi-operator design partner
asks for it. The schema is non-breakingly extensible — adding
`users.timezone NULL` later defaults to tenant TZ for existing users.

### Frontend implementation

- `date-fns-tz` added as a dependency (canonical companion to the existing
  `date-fns`). Provides `formatInTimeZone(date, tz, fmt)` and
  `fromZonedTime(wallclock, tz)` for civil-date ↔ UTC conversion.
- `formatDate(iso, timezone?)` and `formatDateTime(iso, timezone?)` accept
  an optional IANA TZ argument. Existing call sites unchanged (default to
  browser local for now); call sites that should use tenant TZ pass it
  explicitly. Migrating each surface is a per-feature decision, not a
  big-bang rewrite.
- `ApiKeys.tsx` is the first surface migrated end-to-end:
  - Picker fetches tenant TZ from `/v1/settings`.
  - `addDaysInTZ(N, tz)` and `endOfDayInTZ(yyyymmdd, tz)` helpers compute
    end-of-day-in-tenant-TZ as a UTC ISO instant.
  - "Key will expire on..." hint renders in tenant TZ with the abbreviation
    (`"May 5, 2026 at 11:59 PM PDT"`).
  - Existing-keys list "Expires {date}" passes tenant TZ.

### What we deliberately did NOT do

- **Three-field civil round-trip** (`expires_at_civil DATE` +
  `expires_at_tz TEXT` alongside `expires_at TIMESTAMPTZ`). This is the
  Stripe-grade nice-to-have that guarantees "operator picked May 5"
  displays as May 5 to *every* viewer regardless of TZ. Defer until a
  multi-operator-with-different-TZs DP appears — the migration + wire
  shape change isn't justified for 1 operator.
- **Per-user TZ override.** See above.
- **Tenant-TZ-anchored billing cycles.** Adding tenant TZ to `billing_cycle
  _anchor` interpretation is a meaningful behavior change with DST and
  schedule-shift implications. Stripe's model (UTC-anchored cycles) is
  the safer default; if a DP requires "bill on the 5th in our TZ" we can
  ship that as opt-in later.
- **Tenant-local scheduled jobs.** Dunning, retry, etc. all run on UTC
  cron. "Send dunning at 9am tenant-local" requires per-tenant scheduler
  fan-out. Out of scope for v1.

### Migration of remaining surfaces (future, not part of this commit)

The dashboard has many date pickers and timestamp displays. Migrating each
to use tenant TZ is a per-feature decision:
- **Coupon `valid_until`** — civil-date semantics, should use tenant TZ.
  Audit when next we touch the coupon UI.
- **Subscription `start_date`, `cancel_at`** — civil-date semantics for
  operator picks; same pattern.
- **Invoice `due_at`** — already a UTC instant from server; display should
  use tenant TZ.
- **System timestamps** (`created_at`, `last_used_at`, audit log entries)
  — render in tenant TZ with UTC tooltip for debugging. Mirrors GitHub's
  `<time datetime="...Z">` pattern.

The pattern is the same everywhere: pickers compute UTC via tenant TZ;
displays format UTC via tenant TZ. Scaling this beyond ApiKeys is purely
edit-and-test work, no architectural changes.

## Consequences

### Positive

- Operator-picked civil dates are stable across operators (within a single
  tenant TZ).
- Picker UX is transparent: operators see what timezone they're picking in,
  with the abbreviation labelled.
- Backend remains UTC-pure; no billing math changes.
- The schema and infrastructure for per-user TZ (when needed) are
  non-breakingly extensible.
- Aligns Velox with Stripe's model — the closest conceptual peer for an
  API-first billing engine.

### Negative

- Re-render of a stored timestamp under a different tenant TZ shows a
  *different* civil date to viewers (e.g. tenant flips from PDT to IST,
  May 5 11:59 PDT becomes May 6 12:29 PM IST). Mitigated by labelling the
  TZ on every render. The Chargebee-style snapshot pattern would solve
  this fully but adds schema/wire complexity not justified for 1 operator.
- Operators in physical TZs different from tenant TZ pick "May 5" and get
  end-of-May-5 in *tenant* TZ (not their local) — they may briefly think
  the key expires earlier/later than expected. Mitigated by the hint
  labelling both the date and the TZ.
- Adds `date-fns-tz` (~6KB gzipped) to the dashboard bundle. Negligible.

### Open follow-ups

- Migrate the rest of the dashboard's date pickers and timestamp displays
  to use tenant TZ. Roughly 8-12 surfaces; do per-feature when each is
  next touched.
- Add per-user `users.timezone` when a multi-operator DP requests it.
- Add three-field civil round-trip (`expires_at_civil` + `_tz`) when a
  multi-tenant-TZ scenario surfaces.
- Add `Reports` API timezone parameter (Stripe-style) when the reports
  surface exists.
