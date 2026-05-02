import { Badge } from '@/components/ui/badge'

interface ExpiryBadgeProps {
  expiresAt?: string | null
  warningDays?: number
  noExpiryLabel?: string
  label?: string
  className?: string
  // now overrides Date.now() for the days-until calculation. Pass the
  // test clock's frozen_time when the entity is pinned to a clock so
  // the badge reads "Due in N days" relative to simulation time, not
  // wall-clock. Engine actions (dunning, finalize) fire on simulation
  // time; the badge has to match or it understates urgency. Stripe /
  // Lago / Orb pattern: every relative-time UI on a test-clock
  // entity reads from the simulation, not real time.
  now?: string | Date
}

export function ExpiryBadge({
  expiresAt,
  warningDays = 7,
  noExpiryLabel,
  label = 'Expires',
  className,
  now,
}: ExpiryBadgeProps) {
  if (!expiresAt) {
    return noExpiryLabel ? (
      <Badge variant="secondary" className={className}>{noExpiryLabel}</Badge>
    ) : null
  }

  const referenceMs = now
    ? (typeof now === 'string' ? new Date(now).getTime() : now.getTime())
    : Date.now()
  const days = Math.ceil((new Date(expiresAt).getTime() - referenceMs) / 86400000)

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
