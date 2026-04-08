import { cn } from '@/lib/cn'

interface EmptyStateProps {
  icon?: React.ReactNode
  title: string
  description?: string
  actionLabel?: string
  onAction?: () => void
  className?: string
}

export function EmptyState({ icon, title, description, actionLabel, onAction, className }: EmptyStateProps) {
  return (
    <div className={cn('py-12 text-center', className)}>
      {icon && <div className="text-gray-300 mb-3 flex justify-center">{icon}</div>}
      <p className="text-sm font-medium text-gray-500">{title}</p>
      {description && <p className="text-sm text-gray-500 mt-1">{description}</p>}
      {actionLabel && onAction && (
        <button onClick={onAction} className="mt-3 text-sm text-velox-600 hover:underline">
          {actionLabel}
        </button>
      )}
    </div>
  )
}
