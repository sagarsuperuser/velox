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
