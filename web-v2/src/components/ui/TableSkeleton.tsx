import { Card, CardContent } from '@/components/ui/card'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { cn } from '@/lib/utils'

interface TableSkeletonProps {
  columns: number
  rows?: number
  // Optional CSS widths for each body-cell skeleton bar, one per column.
  // If omitted a varied default pattern is used so the shimmer reads as
  // "real data" rather than a uniform block. Provide explicit widths on
  // pages where the column semantics are known (e.g., short status cell).
  widths?: string[]
}

// Default bar widths cycle through a pattern that mimics realistic list
// data — wide name/description cells, narrower status/date cells. Pages
// can override via `widths` when the column mix differs noticeably.
const DEFAULT_BODY_WIDTHS = ['65%', '40%', '75%', '30%', '55%', '45%', '60%', '25%']
const DEFAULT_HEAD_WIDTH = '64px'

// TableSkeleton renders ghost rows with the same Table markup as real data,
// so there is no layout shift when rows arrive. Replaces the centered
// spinner previously used on initial list loads.
//
// Use only for initial data fetch — not for action spinners (form submit,
// CSV export). Those still show <Loader2> so the user sees local progress.
export function TableSkeleton({ columns, rows = 8, widths }: TableSkeletonProps) {
  const barWidth = (col: number) => widths?.[col] ?? DEFAULT_BODY_WIDTHS[col % DEFAULT_BODY_WIDTHS.length]

  return (
    <Table aria-busy aria-label="Loading">
      <TableHeader>
        <TableRow>
          {Array.from({ length: columns }).map((_, ci) => (
            <TableHead key={ci} className="text-xs font-medium">
              <Shimmer className="h-3" style={{ width: DEFAULT_HEAD_WIDTH }} />
            </TableHead>
          ))}
        </TableRow>
      </TableHeader>
      <TableBody>
        {Array.from({ length: rows }).map((_, ri) => (
          <TableRow key={ri}>
            {Array.from({ length: columns }).map((_, ci) => (
              <TableCell key={ci}>
                <Shimmer className="h-4" style={{ width: barWidth(ci) }} />
              </TableCell>
            ))}
          </TableRow>
        ))}
      </TableBody>
    </Table>
  )
}

// FeedSkeleton renders N rows in a time+badge+description layout, for list
// pages that stream events (AuditLog, webhook deliveries). Matches the row
// height and spacing of the real feed so the transition is not jarring.
export function FeedSkeleton({ rows = 8 }: { rows?: number }) {
  return (
    <div className="divide-y divide-border" aria-busy aria-label="Loading">
      {Array.from({ length: rows }).map((_, i) => (
        <div key={i} className="flex items-center px-6 py-2.5 gap-3">
          <Shimmer className="h-3 w-16" />
          <Shimmer className="h-5 w-20 rounded-full" />
          <Shimmer className="h-4 flex-1 max-w-md" />
        </div>
      ))}
    </div>
  )
}

// CardListSkeleton renders N ghost cards stacked vertically — for list pages
// whose rows are Cards rather than TableRows (e.g., ApiKeys). Mirrors the
// same shimmer primitive so loading UX is consistent across the app.
export function CardListSkeleton({ rows = 3 }: { rows?: number }) {
  return (
    <div className="space-y-3" aria-busy aria-label="Loading">
      {Array.from({ length: rows }).map((_, i) => (
        <Card key={i}>
          <CardContent className="px-6 py-4">
            <div className="flex items-start gap-3">
              <Shimmer className="h-9 w-9 rounded-lg shrink-0" />
              <div className="flex-1 space-y-2">
                <Shimmer className="h-4 w-48" />
                <Shimmer className="h-3 w-32" />
                <div className="flex gap-4">
                  <Shimmer className="h-3 w-16" />
                  <Shimmer className="h-3 w-24" />
                  <Shimmer className="h-3 w-20" />
                </div>
              </div>
            </div>
          </CardContent>
        </Card>
      ))}
    </div>
  )
}

interface ShimmerProps {
  className?: string
  style?: React.CSSProperties
}

// Shimmer is a single pulsing block. Kept local to this file so list-page
// loading visuals don't cross-depend on the analytics module's SkeletonBlock.
// UI-6 may unify the two into a shared `ui/skeleton.tsx` primitive.
function Shimmer({ className, style }: ShimmerProps) {
  return (
    <div
      aria-hidden
      style={style}
      className={cn(
        'relative overflow-hidden bg-muted/70 rounded',
        'before:absolute before:inset-0 before:-translate-x-full',
        'before:animate-[shimmer_1.5s_infinite]',
        'before:bg-gradient-to-r before:from-transparent before:via-white/20 before:to-transparent',
        'motion-reduce:before:animate-none',
        className,
      )}
    />
  )
}
