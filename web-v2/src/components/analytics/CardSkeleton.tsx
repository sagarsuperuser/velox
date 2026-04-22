import type { CSSProperties } from 'react'
import { Card, CardContent } from '@/components/ui/card'
import { cn } from '@/lib/utils'

interface SkeletonProps {
  className?: string
  style?: CSSProperties
}

// Shimmer block — used as a primitive by the larger skeletons below.
export function SkeletonBlock({ className, style }: SkeletonProps) {
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

export function TrendCardSkeleton() {
  return (
    <Card aria-busy>
      <CardContent className="p-5">
        <SkeletonBlock className="h-3 w-24 mb-3" />
        <SkeletonBlock className="h-7 w-32 mb-3" />
        <SkeletonBlock className="h-3 w-20 mb-4" />
        <SkeletonBlock className="h-8 w-full" />
      </CardContent>
    </Card>
  )
}

export function ChartCardSkeleton({ height = 280 }: { height?: number }) {
  return (
    <Card aria-busy>
      <CardContent className="p-5">
        <div className="flex items-center justify-between mb-4">
          <SkeletonBlock className="h-4 w-32" />
          <SkeletonBlock className="h-8 w-48" />
        </div>
        <SkeletonBlock className="w-full" style={{ height }} />
      </CardContent>
    </Card>
  )
}
