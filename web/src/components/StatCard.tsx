import { TrendingUp, TrendingDown, Minus } from 'lucide-react'
import { cn } from '@/lib/cn'

interface StatCardProps {
  title: string
  value: string
  subtitle?: string
  trend?: 'up' | 'down' | 'neutral'
  trendValue?: string
  icon?: React.ReactNode
  className?: string
  variant?: 'money' | 'count' // money = tabular-nums, count = default
}

export function StatCard({ title, value, subtitle, trend, trendValue, icon, className, variant }: StatCardProps) {
  const isMoney = variant === 'money' || value.startsWith('$') || value.startsWith('\u20AC') || value.startsWith('\u00A3') || value.startsWith('\u00A5') || value.startsWith('\u20B9')

  return (
    <div className={cn(
      'bg-white dark:bg-gray-900 rounded-xl shadow-card hover:shadow-card-hover transition-all duration-200 p-6 group',
      className,
    )}>
      <div className="flex items-start justify-between">
        <p className="text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-gray-400">{title}</p>
        {icon && (
          <div className="text-gray-400 dark:text-gray-500 group-hover:text-velox-500 transition-colors">
            {icon}
          </div>
        )}
      </div>
      <p className={cn(
        'text-2xl font-semibold mt-2 text-gray-900 dark:text-gray-100',
        isMoney && 'tabular-nums tracking-tight'
      )}>{value}</p>
      <div className="flex items-center gap-2 mt-1.5">
        {trend && trendValue && (
          <span className={cn(
            'inline-flex items-center gap-0.5 text-xs font-medium',
            trend === 'up' && 'text-emerald-600 dark:text-emerald-400',
            trend === 'down' && 'text-red-600 dark:text-red-400',
            trend === 'neutral' && 'text-gray-500 dark:text-gray-400',
          )}>
            {trend === 'up' && <TrendingUp size={13} />}
            {trend === 'down' && <TrendingDown size={13} />}
            {trend === 'neutral' && <Minus size={13} />}
            {trendValue}
          </span>
        )}
        {subtitle && <p className="text-xs text-gray-500 dark:text-gray-400">{subtitle}</p>}
      </div>
    </div>
  )
}
