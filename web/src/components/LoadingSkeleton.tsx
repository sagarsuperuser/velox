const widths = ['w-4/5', 'w-3/5', 'w-11/12', 'w-2/3', 'w-3/4'] as const

function ShimmerBar({ className = '' }: { className?: string }) {
  return <div className={`skeleton-shimmer rounded h-4 ${className}`} />
}

interface LoadingSkeletonProps {
  variant?: 'table' | 'card' | 'detail' | 'chart'
  rows?: number
  columns?: number
}

export function LoadingSkeleton({ variant = 'table', rows = 5, columns = 4 }: LoadingSkeletonProps) {
  if (variant === 'card') {
    return (
      <div role="status" aria-label="Loading" aria-busy="true" className="grid grid-cols-2 lg:grid-cols-4 gap-4">
        {Array.from({ length: 4 }).map((_, i) => (
          <div key={i} className="bg-white dark:bg-gray-900 rounded-xl border border-gray-200 dark:border-gray-800 p-5 space-y-3">
            <ShimmerBar className={`h-3 ${widths[i % widths.length]}`} />
            <ShimmerBar className="h-7 w-1/2" />
            <ShimmerBar className="h-3 w-2/5" />
          </div>
        ))}
      </div>
    )
  }

  if (variant === 'detail') {
    return (
      <div role="status" aria-label="Loading" aria-busy="true" className="space-y-6">
        {/* Page header */}
        <div className="space-y-3">
          <ShimmerBar className="h-6 w-1/3" />
          <ShimmerBar className="h-4 w-1/2" />
        </div>
        {/* Sections */}
        {Array.from({ length: 3 }).map((_, i) => (
          <div key={i} className="bg-white dark:bg-gray-900 rounded-xl border border-gray-200 dark:border-gray-800 p-5 space-y-4">
            <ShimmerBar className={`h-4 ${widths[i % widths.length]}`} />
            <ShimmerBar className="h-4 w-full" />
            <ShimmerBar className={`h-4 ${widths[(i + 2) % widths.length]}`} />
          </div>
        ))}
      </div>
    )
  }

  if (variant === 'chart') {
    return (
      <div role="status" aria-label="Loading" aria-busy="true" className="bg-white dark:bg-gray-900 rounded-xl border border-gray-200 dark:border-gray-800 p-5 space-y-4">
        <ShimmerBar className="h-4 w-1/4" />
        <div className="flex items-end gap-2 h-40">
          {Array.from({ length: 12 }).map((_, i) => (
            <div
              key={i}
              className="skeleton-shimmer rounded flex-1"
              style={{ height: `${30 + Math.sin(i * 0.8) * 25 + 25}%` }}
            />
          ))}
        </div>
        <div className="flex justify-between">
          <ShimmerBar className="h-3 w-12" />
          <ShimmerBar className="h-3 w-12" />
          <ShimmerBar className="h-3 w-12" />
        </div>
      </div>
    )
  }

  // Default: table variant
  return (
    <div role="status" aria-label="Loading" aria-busy="true">
      {/* Header row */}
      <div className="flex gap-4 px-6 py-3 border-b border-gray-100 dark:border-gray-800">
        {Array.from({ length: columns }).map((_, j) => (
          <ShimmerBar key={j} className="h-3 flex-1 opacity-60" />
        ))}
      </div>
      {/* Data rows */}
      {Array.from({ length: rows }).map((_, i) => (
        <div key={i} className="flex gap-4 px-6 py-3 border-b border-gray-50 dark:border-gray-800/50">
          {Array.from({ length: columns }).map((_, j) => (
            <ShimmerBar key={j} className={`flex-1 ${widths[(i + j) % widths.length]}`} />
          ))}
        </div>
      ))}
    </div>
  )
}
