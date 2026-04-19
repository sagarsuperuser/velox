import { Badge } from '@/components/ui/badge'

interface ExpiryBadgeProps {
  expiresAt?: string | null
  warningDays?: number
  noExpiryLabel?: string
  label?: string
  className?: string
}

export function ExpiryBadge({
  expiresAt,
  warningDays = 7,
  noExpiryLabel,
  label = 'Expires',
  className,
}: ExpiryBadgeProps) {
  if (!expiresAt) {
    return noExpiryLabel ? (
      <Badge variant="secondary" className={className}>{noExpiryLabel}</Badge>
    ) : null
  }

  const days = Math.ceil((new Date(expiresAt).getTime() - Date.now()) / 86400000)

  if (days < 0) {
    return <Badge variant="destructive" className={className}>Expired</Badge>
  }
  if (days <= warningDays) {
    return (
      <Badge variant="warning" className={className}>
        {days === 0 ? `${label} today` : `${label} in ${days}d`}
      </Badge>
    )
  }
  if (days <= 30) {
    return (
      <Badge variant="outline" className={`text-muted-foreground ${className ?? ''}`}>
        {label} in {days}d
      </Badge>
    )
  }
  return (
    <Badge variant="success" className={className}>
      {label} in {days}d
    </Badge>
  )
}
