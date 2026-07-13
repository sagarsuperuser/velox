// Minimal stub of `@/lib/api` for node --test. lib/dates only reads
// getTenantTimezone() for its 'UTC' fallback, which the tests never exercise
// (they always pass an explicit timezone). Returning undefined mirrors the
// pre-bootstrap state.
export function getTenantTimezone(): string | undefined {
  return undefined
}
