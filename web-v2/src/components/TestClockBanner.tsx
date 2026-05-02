import { Link } from 'react-router-dom'
import { FlaskConical } from 'lucide-react'

// TestClockBanner sits at the top of any detail page whose entity is
// pinned (directly or via its subscription) to a test clock. Test
// clocks let operators advance simulated time independently of
// wall-clock; rows finalized / billed / dunned during a clock-advance
// carry the simulated timestamp, which can render as a future date in
// the activity timeline (e.g. "Invoice finalized · Jun 1, 2026" while
// the operator is reading the page on May 2). The banner sets the
// expectation upfront so the operator stops wondering "why is this in
// the future." Industry pattern (Stripe, Lago, Orb): label the
// simulation, never rewrite the timestamp.
export function TestClockBanner({ testClockId }: { testClockId: string }) {
  return (
    <div className="mb-6 flex items-center gap-3 rounded-md border border-amber-300 bg-amber-50 px-4 py-3 text-sm text-amber-900 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-300">
      <FlaskConical size={16} className="shrink-0" />
      <div className="flex-1">
        <span className="font-medium">Test clock simulation.</span>{' '}
        Some dates on this page reflect simulated time, not wall-clock —
        rows produced during a clock-advance (finalize, dunning runs,
        retries) carry the simulated timestamp. Webhook arrivals and
        operator actions stay on real time.
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
