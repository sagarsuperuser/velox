import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { FlaskConical } from 'lucide-react'
import { api, formatDateTime } from '@/lib/api'

// TestClockBanner sits at the top of any detail page whose entity is
// pinned (directly or via its subscription) to a test clock. Test
// clocks let operators advance simulated time independently of
// wall-clock; rows finalized / billed / dunned during a clock-advance
// carry the simulated timestamp, which can render as a future date in
// the activity timeline (e.g. "Invoice finalized · Jun 1, 2026" while
// the operator is reading the page on May 2). The banner sets the
// expectation upfront so the operator stops wondering "why is this in
// the future."
//
// Crucially, the banner shows the test clock's CURRENT frozen_time so
// the operator has a reference point. Without this, an invoice with
// "Due Jul 17, 2026" rendered alongside a "Past due" pill looks
// contradictory — until you know the simulation has been advanced to
// Sep 2, 2026 and Jul 17 is genuinely past in simulation time.
// Industry pattern (Stripe Test Clocks UI): the simulation timestamp
// is always visible on every page that consults it.
//
// React Query dedupes the test-clock fetch across components — the
// parent page often already loads the same clock for other UI
// purposes, so this hook adds no network round-trip.
export function TestClockBanner({ testClockId }: { testClockId: string }) {
  const { data: clock } = useQuery({
    queryKey: ['test-clock', testClockId],
    queryFn: () => api.getTestClock(testClockId),
    enabled: !!testClockId,
  })

  return (
    <div className="mb-6 flex items-center gap-3 rounded-md border border-amber-300 bg-amber-50 px-4 py-3 text-sm text-amber-900 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-300">
      <FlaskConical size={16} className="shrink-0" />
      <div className="flex-1">
        <span className="font-medium">Test clock simulation.</span>{' '}
        {clock ? (
          <>
            Currently at <span className="font-mono font-medium">{formatDateTime(clock.frozen_time)}</span>.
            Some dates on this page reflect simulated time, not wall-clock —
            rows produced during a clock-advance (finalize, dunning runs,
            retries) carry the simulated timestamp.
          </>
        ) : (
          <>
            Some dates on this page reflect simulated time, not wall-clock —
            rows produced during a clock-advance (finalize, dunning runs,
            retries) carry the simulated timestamp.
          </>
        )}
      </div>
      <Link
        to={`/test-clocks/${testClockId}`}
        className="shrink-0 text-xs underline-offset-2 hover:underline"
      >
        View clock
      </Link>
    </div>
  )
}
