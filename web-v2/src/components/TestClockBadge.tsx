import { FlaskConical } from 'lucide-react'

// TestClockBadge is the inline-row marker for entities pinned to a
// test clock. Hovering anchors the operator's expectation that
// timestamps in this row may be simulated rather than wall-clock,
// before they click into the detail page (where TestClockBanner
// elaborates and links to the clock).
//
// Rendered as a <span>, NOT a <Link>: many parent rows are <Link>
// (Dashboard recent invoices, CustomerDetail recent subs/invoices,
// portal lists). Nesting <a> inside <a> is invalid HTML and causes
// a React hydration error. The badge is informational; navigation
// to the clock detail page is one click away on the linked entity's
// detail page banner. Tooltip via native title= since this is a
// non-interactive marker; styled chip matches the existing test-mode
// banner aesthetic (amber).
export function TestClockBadge({
  testClockId,
  className = '',
}: {
  testClockId: string
  className?: string
}) {
  return (
    <span
      title={`Pinned to test clock ${testClockId} — some dates may reflect simulated time`}
      className={`inline-flex items-center gap-1 rounded border border-amber-500/30 bg-amber-500/10 px-1.5 py-0.5 text-[10px] font-medium leading-none text-amber-800 dark:text-amber-300 ${className}`}
    >
      <FlaskConical size={10} />
      Test clock
    </span>
  )
}

// SimulatedBadge marks an invoice (or invoice row) whose domain timestamps
// were stamped on a test clock's simulated time. Unlike TestClockBadge, it's
// driven by the backend-authoritative `invoice.is_simulated` flag rather than
// a sub→clock map lookup — so it badges manual one-off invoices correctly
// (they have no subscription to look through) and never rots when a clock is
// later unpinned. Same amber aesthetic; non-interactive <span> (safe to nest
// inside row <Link>s). See ADR-030 / feedback_no_heuristic_proxies.
export function SimulatedBadge({
  className = '',
  title = 'Dates on this invoice are simulated test-clock time, not wall-clock',
}: {
  className?: string
  title?: string
}) {
  return (
    <span
      title={title}
      className={`inline-flex shrink-0 items-center gap-1 rounded border border-amber-500/30 bg-amber-500/10 px-1.5 py-0.5 text-[10px] font-medium leading-none text-amber-800 dark:text-amber-300 ${className}`}
    >
      <FlaskConical size={10} />
      Simulated
    </span>
  )
}
