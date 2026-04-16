import { cn } from '@/lib/cn'

interface EmptyStateProps {
  icon?: React.ReactNode
  title: string
  description?: string
  actionLabel?: string
  onAction?: () => void
  secondaryLabel?: string
  secondaryHref?: string
  className?: string
}

export function EmptyState({ icon, title, description, actionLabel, onAction, secondaryLabel, secondaryHref, className }: EmptyStateProps) {
  return (
    <div role="status" className={cn('py-16 text-center relative', className)}>
      {/* Dotted grid background */}
      <div className="absolute inset-0 opacity-[0.04] dark:opacity-[0.06]" style={{
        backgroundImage: 'radial-gradient(circle at 1px 1px, currentColor 1px, transparent 0)',
        backgroundSize: '24px 24px',
      }} />

      <div className="relative z-10">
        {icon && (
          <div className="inline-flex items-center justify-center w-12 h-12 rounded-xl bg-gray-100 dark:bg-gray-800 text-gray-400 dark:text-gray-500 mb-4">
            {icon}
          </div>
        )}
        <p className="text-sm font-semibold text-gray-900 dark:text-gray-100">{title}</p>
        {description && <p className="text-sm text-gray-500 dark:text-gray-400 mt-1 max-w-sm mx-auto">{description}</p>}
        <div className="flex items-center justify-center gap-3 mt-4">
          {actionLabel && onAction && (
            <button
              onClick={onAction}
              className="inline-flex items-center gap-1.5 px-4 py-2 text-sm font-medium text-white bg-velox-600 hover:bg-velox-700 rounded-lg shadow-sm transition-colors"
            >
              {actionLabel}
            </button>
          )}
          {secondaryLabel && secondaryHref && (
            <a
              href={secondaryHref}
              target="_blank"
              rel="noopener noreferrer"
              className="text-sm text-gray-500 hover:text-gray-700 dark:text-gray-400 dark:hover:text-gray-200 transition-colors"
            >
              {secondaryLabel}
            </a>
          )}
        </div>
      </div>
    </div>
  )
}
