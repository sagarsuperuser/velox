import { format } from 'date-fns'
import { formatInTimeZone, fromZonedTime } from 'date-fns-tz'
import { getTenantTimezone } from '@/lib/api'

// Date helpers that interpret operator-picked civil dates in tenant
// timezone, returning UTC ISO 8601 timestamps suitable for the wire.
// See ADR-010 for the model.
//
// All helpers fall back to 'UTC' if no tenant TZ is set (e.g. before
// /v1/settings has loaded). The fallback matches the DB default and
// keeps the dashboard usable during the boot window.

function tenantTZ(): string {
  return getTenantTimezone() || 'UTC'
}

// endOfDayInTZ returns UTC ISO 8601 of "yyyy-mm-dd at 23:59:59.999
// in tenant TZ". Used for civil-date expiry pickers ("expires
// May 5") so the operator-intended last-second-of-the-day matches
// regardless of who's viewing or where they are physically.
export function endOfDayInTZ(yyyymmdd: string, timezone?: string): string {
  const tz = timezone || tenantTZ()
  return fromZonedTime(`${yyyymmdd} 23:59:59.999`, tz).toISOString()
}

// startOfDayInTZ returns UTC ISO 8601 of "yyyy-mm-dd at 00:00:00.000
// in tenant TZ". Used for date-range filters ("from May 1") so the
// "from" boundary matches the operator's mental model of "from the
// start of that day".
export function startOfDayInTZ(yyyymmdd: string, timezone?: string): string {
  const tz = timezone || tenantTZ()
  return fromZonedTime(`${yyyymmdd} 00:00:00.000`, tz).toISOString()
}

// addDaysInTZ rolls forward N days from today in tenant TZ and
// returns end-of-day-in-tenant-TZ as UTC ISO. The day-arithmetic
// uses a browser-local Date with explicit y/m/d construction (TZ-
// safe for day-grade math), then re-grounds in tenant TZ.
export function addDaysInTZ(days: number, timezone?: string): string {
  const tz = timezone || tenantTZ()
  const todayStr = formatInTimeZone(new Date(), tz, 'yyyy-MM-dd')
  const [y, m, d] = todayStr.split('-').map(Number)
  const target = new Date(y, m - 1, d + days)
  const targetStr = `${target.getFullYear()}-${String(target.getMonth() + 1).padStart(2, '0')}-${String(target.getDate()).padStart(2, '0')}`
  return endOfDayInTZ(targetStr, tz)
}

// formatDateInTZ returns a human-readable date+time string in tenant
// TZ with zone abbreviation. Used by the "Key will expire on..."
// hint and similar copy that needs explicit TZ context inline.
export function formatExpiryHintInTZ(iso: string, timezone?: string): string {
  const tz = timezone || tenantTZ()
  return formatInTimeZone(new Date(iso), tz, "MMMM d, yyyy 'at' h:mm a (zzz)")
}

// formatYMD returns a yyyy-MM-dd string from a Date object — used
// when we need a date-only string for picker round-trips. Just a
// thin wrapper over date-fns format() so callers don't import
// date-fns directly for one-liners.
export function formatYMD(d: Date): string {
  return format(d, 'yyyy-MM-dd')
}

// formatYMDInTZ returns the yyyy-MM-dd date prefix of a UTC ISO
// timestamp re-projected into tenant TZ. Used by client-side list
// filters that compare a row's date against a picked date string —
// without this, a row with created_at = May 4 22:00 UTC (= May 5
// IST) would compare as "May 4" against an operator's "from May 5"
// pick and get filtered out, contradicting the dashboard's tenant-
// TZ display.
export function formatYMDInTZ(iso: string, timezone?: string): string {
  const tz = timezone || tenantTZ()
  return formatInTimeZone(new Date(iso), tz, 'yyyy-MM-dd')
}

// ---------------------------------------------------------------------------
// Inclusive billing-period display (ADR-050).
//
// Billing periods are stored HALF-OPEN [start, end): `end` is the EXCLUSIVE
// boundary where the next period begins (Jun 1 → Jul 1 = "covers June"). Every
// billing engine (Stripe, Zuora, Recurly, Chargebee, Lago) DISPLAYS a period by
// its INCLUSIVE last covered day instead — "Jun 1 – Jun 30", not "Jun 1 – Jul 1"
// — so the operator never has to mentally subtract a day.
//
// These two helpers are the client-side mirror of the Go functions
// domain.InclusiveDisplayEnd / domain.FormatInclusivePeriod (the canonical
// spec — keep them byte-for-byte identical). The invoice surfaces get the
// string straight from the backend (`billing_period_display`, computed by
// FormatInclusivePeriod) because the PDF is server-rendered; the React
// subscription surfaces already receive the raw half-open instants over the
// wire, so they compute the same string here rather than round-tripping a
// derived field through every read path. Display only — the wire stays
// half-open.
//
// The conversion is a CALENDAR step, never a 24h instant subtraction: snap the
// exclusive boundary to its civil date in tenant TZ, then step back one calendar
// day. A 24h subtraction lands on the wrong civil date across a DST boundary or
// a non-midnight end — the same off-by-one class ADR-050 fixed in the engine.

// inclusiveEndYMD returns [year, month, day] of the last calendar day fully
// covered by a half-open period whose exclusive end is `endISO`, in tenant TZ.
function inclusiveEndYMD(endISO: string, tz: string): [number, number, number] {
  // The boundary's civil date in tenant TZ (e.g. 2028-07-01).
  const civil = formatInTimeZone(new Date(endISO), tz, 'yyyy-MM-dd')
  const [y, m, d] = civil.split('-').map(Number)
  // Step back ONE calendar day. JS Date normalizes day 0 → last day of the
  // previous month and Jan 0 → Dec 31 of the prior year, so month/year rollover
  // and leap-Februarys fall out correctly. Explicit y/m/d construction keeps
  // this browser-TZ-safe (same idiom as addDaysInTZ above).
  const back = new Date(y, m - 1, d - 1)
  return [back.getFullYear(), back.getMonth() + 1, back.getDate()]
}

// formatCivilDate renders the INCLUSIVE last covered day of a half-open
// period_end as "MMM d, yyyy" in tenant TZ ("Jun 30, 2028"). Use for a single
// period-boundary label that means "last day covered" (e.g. a cycle's end).
// NOT for event dates ("Renews", "Cancels", "Next billing") — those fire on the
// exclusive boundary instant and must keep formatDate(). Empty input → "".
// Mirrors Go domain.InclusiveDisplayEnd + the "Jan 2, 2006" layout.
export function formatCivilDate(endISO?: string | null, timezone?: string): string {
  if (!endISO) return ''
  const tz = timezone || tenantTZ()
  const [y, m, d] = inclusiveEndYMD(endISO, tz)
  return format(new Date(y, m - 1, d), 'MMM d, yyyy')
}

// formatCivilPeriod renders the human period range "<start> – <inclusiveEnd>"
// (en-dash) in tenant TZ — the exact string the invoice's backend
// billing_period_display shows, so a period reads identically wherever it
// appears. Returns "" when the period is empty (start == end). Clamps the
// inclusive end to >= the start's civil day so a sub-day stub never inverts.
// Mirrors Go domain.FormatInclusivePeriod.
export function formatCivilPeriod(
  startISO?: string | null,
  endISO?: string | null,
  timezone?: string,
): string {
  if (!startISO || !endISO) return ''
  if (new Date(startISO).getTime() === new Date(endISO).getTime()) return ''
  const tz = timezone || tenantTZ()
  const startStr = formatInTimeZone(new Date(startISO), tz, 'MMM d, yyyy')
  // Start's civil day in tenant TZ — the clamp floor for the inclusive end.
  const [sy, sm, sd] = formatInTimeZone(new Date(startISO), tz, 'yyyy-MM-dd')
    .split('-')
    .map(Number)
  const startDate = new Date(sy, sm - 1, sd)
  const [ey, em, ed] = inclusiveEndYMD(endISO, tz)
  let endDate = new Date(ey, em - 1, ed)
  if (endDate.getTime() < startDate.getTime()) endDate = startDate
  return `${startStr} – ${format(endDate, 'MMM d, yyyy')}`
}
