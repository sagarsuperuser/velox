import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api } from '@/lib/api'

// useClockFrozenMap returns a `test_clock_id -> frozen_time` lookup so any
// relative-time / cycle-progress surface can compute its "now" baseline from
// the SIMULATED time of a clock-pinned entity instead of wall-clock Date.now().
//
// Test clocks let operators advance simulated time independently of the wall
// clock; an entity's domain timestamps (billing-period bounds, finalized_at,
// due/expiry dates) are then stamped in simulation time and can sit far in the
// browser's future/past. Any UI that renders a value RELATIVE to "now" — "X
// ago", "in N days", cycle progress %, "Day N of M" — has to read that "now"
// from the clock, or it lies. See ExpiryBadge / DueBadge / InvoiceAttention /
// pages/Dunning for the established effective-now pattern.
//
// Previously this map was hand-rolled inline (Subscriptions.tsx); the omission
// of that copy in CostDashboard is exactly what left its cycle progress reading
// wall-clock time on clock-pinned subs. Centralizing it here removes the
// per-call-site drift that caused the miss.
export function useClockFrozenMap(): Record<string, string> {
  const { data } = useQuery({
    queryKey: ['test-clocks'],
    queryFn: () => api.listTestClocks(),
  })
  return useMemo(() => {
    const m: Record<string, string> = {}
    ;(data?.data ?? []).forEach(c => {
      m[c.id] = c.frozen_time
    })
    return m
  }, [data])
}

// clockNow resolves the effective "now" ISO for an entity: the owning test
// clock's frozen_time when the entity is pinned, else undefined so the caller
// falls back to real Date.now(). Pass the entity's own test_clock_id (a
// subscription's, a customer's) — NOT a clock id you already know is set.
export function clockNow(
  map: Record<string, string>,
  testClockId?: string | null,
): string | undefined {
  return testClockId ? map[testClockId] : undefined
}
