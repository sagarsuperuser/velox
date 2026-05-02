import { Link } from 'react-router-dom'
import { FlaskConical } from 'lucide-react'

// TestClockBadge is the inline-row marker for entities pinned to a
// test clock. Hovering anchors the operator's expectation that
// timestamps in this row may be simulated rather than wall-clock,
// before they click into the detail page (where TestClockBanner
// elaborates). Tiny chip styling matches the existing test-mode
// banner aesthetic (amber).
export function TestClockBadge({
  testClockId,
  className = '',
}: {
  testClockId: string
  className?: string
}) {
  return (
    <Link
      to={`/test-clocks/${testClockId}`}
      onClick={(e) => e.stopPropagation()}
      title="Pinned to a test clock — some dates may reflect simulated time"
      className={`inline-flex items-center gap-1 rounded border border-amber-500/30 bg-amber-500/10 px-1.5 py-0.5 text-[10px] font-medium leading-none text-amber-800 hover:bg-amber-500/15 dark:text-amber-300 ${className}`}
    >
      <FlaskConical size={10} />
      Test clock
    </Link>
  )
}
