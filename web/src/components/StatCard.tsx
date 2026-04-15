import { cn } from '@/lib/cn'

interface StatCardProps {
  title: string
  value: string
  subtitle?: string
  trend?: 'up' | 'down' | 'neutral'
  className?: string
  variant?: 'money' | 'count' // money = tabular-nums, count = default
}

export function StatCard({ title, value, subtitle, className, variant }: StatCardProps) {
  const isMoney = variant === 'money' || value.startsWith('$') || value.startsWith('€') || value.startsWith('£') || value.startsWith('¥') || value.startsWith('₹')

  return (
    <div className={cn('bg-white dark:bg-gray-900 rounded-xl shadow-card hover:shadow-card-hover transition-shadow p-6', className)}>
      <p className="text-sm text-gray-600 dark:text-gray-400 font-medium">{title}</p>
      <p className={cn(
        'text-2xl font-semibold mt-1 text-gray-900 dark:text-gray-100',
        isMoney && 'tabular-nums tracking-tight'
      )}>{value}</p>
      {subtitle && <p className="text-sm text-gray-600 dark:text-gray-400 mt-1">{subtitle}</p>}
    </div>
  )
}
