import type { LucideIcon } from 'lucide-react'
import { Link } from 'react-router-dom'

import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

// A single discretionary action rendered inside an empty state — either
// a plain onClick handler or a route link. `icon` is typically Plus for
// "create" actions; leave undefined for navigation/link actions.
interface EmptyStateAction {
  label: string
  onClick?: () => void
  to?: string
  icon?: LucideIcon
  variant?: 'default' | 'outline' | 'ghost' | 'secondary'
}

interface EmptyStateProps {
  icon?: LucideIcon
  title: string
  description: string
  action?: EmptyStateAction
  secondaryAction?: EmptyStateAction
  className?: string
}

export function EmptyState({
  icon: Icon,
  title,
  description,
  action,
  secondaryAction,
  className,
}: EmptyStateProps) {
  return (
    <div
      className={cn(
        'flex flex-col items-center justify-center px-6 py-12 text-center',
        className,
      )}
    >
      {Icon && (
        <div className="w-12 h-12 rounded-full bg-muted flex items-center justify-center mb-4">
          <Icon size={22} className="text-muted-foreground" />
        </div>
      )}
      <p className="text-sm font-medium text-foreground">{title}</p>
      <p className="text-sm text-muted-foreground mt-1 max-w-sm">{description}</p>
      {(action || secondaryAction) && (
        <div className="flex items-center gap-2 mt-4">
          {action && <ActionButton {...action} />}
          {secondaryAction && (
            <ActionButton variant={secondaryAction.variant ?? 'outline'} {...secondaryAction} />
          )}
        </div>
      )}
    </div>
  )
}

function ActionButton({
  label,
  onClick,
  to,
  icon: Icon,
  variant = 'default',
}: EmptyStateAction) {
  const content = (
    <>
      {Icon && <Icon size={16} className="mr-2" />}
      {label}
    </>
  )
  if (to) {
    return (
      <Link to={to}>
        <Button size="sm" variant={variant}>
          {content}
        </Button>
      </Link>
    )
  }
  return (
    <Button size="sm" variant={variant} onClick={onClick}>
      {content}
    </Button>
  )
}
