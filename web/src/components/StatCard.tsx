import { cn } from '@/lib/cn'

interface StatCardProps {
  title: string
  value: string
  subtitle?: string
  trend?: 'up' | 'down' | 'neutral'
  className?: string
}

export function StatCard({ title, value, subtitle, className }: StatCardProps) {
  return (
    <div className={cn('bg-white rounded-xl shadow-card hover:shadow-card-hover transition-shadow p-6', className)}>
      <p className="text-sm text-gray-500 font-medium">{title}</p>
      <p className="text-2xl font-semibold mt-1 text-gray-900">{value}</p>
      {subtitle && <p className="text-xs text-gray-400 mt-1">{subtitle}</p>}
    </div>
  )
}
