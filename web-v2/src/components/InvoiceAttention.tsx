import { Link } from 'react-router-dom'
import { AlertTriangle, AlertCircle, Info, ExternalLink } from 'lucide-react'
import type {
  Invoice,
  InvoiceAttention as Attention,
  AttentionAction,
  AttentionSeverity,
} from '@/lib/api'
import { Card, CardContent } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

// InvoiceAttention is the unified "this invoice needs operator
// attention" banner. Reads server-derived invoice.attention and
// renders nothing when absent (healthy or terminal-state). Mirrors
// Stripe's invoice banner pattern — severity-tinted card, reason
// badge, headline message, prescribed actions, optional doc-link and
// raw-detail disclosure.
//
// Stays purely presentational: the typed reason / action / severity
// codes come from the server via ADR-009. Adding a new code on the
// server doesn't require touching this component (the default action
// renderer covers any AttentionAction).
export function InvoiceAttention({
  invoice,
  onRetryTax,
  retrying,
}: {
  invoice: Invoice
  onRetryTax?: () => void
  retrying?: boolean
}) {
  const att = invoice.attention
  if (!att) return null

  const styles = severityStyles(att.severity)
  const Icon = severityIcon(att.severity)

  return (
    <Card className={cn('mb-6', styles.card)}>
      <CardContent className="p-5 space-y-4">
        <div className="flex items-start gap-3">
          <Icon size={18} className={cn('mt-0.5 shrink-0', styles.icon)} />
          <div className="flex-1 space-y-1.5 min-w-0">
            <div className="flex items-center gap-2 flex-wrap">
              <Badge variant="outline" className={cn('text-xs', styles.badge)}>
                {humanReason(att.reason)}
              </Badge>
              {att.code && (
                <code className="text-[10px] text-muted-foreground font-mono">
                  {att.code}
                </code>
              )}
              {att.since && (
                <span className="text-[10px] text-muted-foreground">
                  · since {formatRelative(att.since)}
                </span>
              )}
            </div>
            <p className="text-sm text-foreground">{att.message}</p>
          </div>
        </div>

        {(att.actions?.length ?? 0) > 0 && (
          <div className="flex items-center gap-2 flex-wrap pl-7">
            {att.actions!.map((action, idx) => (
              <ActionButton
                key={action.code}
                action={action.code}
                label={action.label}
                primary={idx === 0}
                invoice={invoice}
                onRetryTax={onRetryTax}
                retrying={retrying}
              />
            ))}
            {att.doc_url && (
              <Button asChild variant="ghost" size="sm" className="text-xs">
                <a href={att.doc_url} target="_blank" rel="noopener noreferrer">
                  Learn more
                  <ExternalLink size={12} className="ml-1" />
                </a>
              </Button>
            )}
          </div>
        )}

        {att.detail && (
          <details className="pl-7 text-xs text-muted-foreground">
            <summary className="cursor-pointer hover:text-foreground">
              Provider response
            </summary>
            <pre className="mt-2 p-2 bg-muted/50 rounded text-[10px] font-mono overflow-x-auto whitespace-pre-wrap break-all max-w-xl">
              {att.detail}
            </pre>
          </details>
        )}
      </CardContent>
    </Card>
  )
}

// ActionButton dispatches typed action codes to the right UI element.
// edit_billing_profile / review_registration / wait_provider /
// rotate_api_key are link-out actions; retry_tax is server-bound and
// uses the supplied callback. Unknown codes fall through to a labeled
// no-op button so a server-added action doesn't render as broken.
function ActionButton({
  action,
  label,
  primary,
  invoice,
  onRetryTax,
  retrying,
}: {
  action: AttentionAction
  label?: string
  primary: boolean
  invoice: Invoice
  onRetryTax?: () => void
  retrying?: boolean
}) {
  const variant = primary ? 'default' : 'outline'
  const display = label ?? defaultLabel(action)

  switch (action) {
    case 'edit_billing_profile':
      return (
        <Button asChild variant={variant} size="sm">
          <Link to={`/customers/${invoice.customer_id}`}>{display}</Link>
        </Button>
      )
    case 'retry_tax':
      return (
        <Button
          variant={variant}
          size="sm"
          onClick={onRetryTax}
          disabled={!onRetryTax || retrying}
        >
          {retrying ? 'Retrying…' : display}
        </Button>
      )
    case 'wait_provider':
      return (
        <Button asChild variant={variant} size="sm">
          <a href="https://status.stripe.com/" target="_blank" rel="noopener noreferrer">
            {display}
            <ExternalLink size={12} className="ml-1" />
          </a>
        </Button>
      )
    case 'rotate_api_key':
      return (
        <Button asChild variant={variant} size="sm">
          <Link to="/settings">{display}</Link>
        </Button>
      )
    case 'review_registration':
      return (
        <Button asChild variant={variant} size="sm">
          <Link to="/settings">{display}</Link>
        </Button>
      )
    case 'reconcile_payment':
    case 'retry_payment':
    default:
      return (
        <Button variant={variant} size="sm" disabled>
          {display}
        </Button>
      )
  }
}

function severityStyles(s: AttentionSeverity) {
  switch (s) {
    case 'critical':
      return {
        card: 'border-destructive/40 bg-destructive/5',
        icon: 'text-destructive',
        badge: 'border-destructive/40 text-destructive',
      }
    case 'warning':
      return {
        card: 'border-amber-300/60 bg-amber-50/40 dark:bg-amber-950/20',
        icon: 'text-amber-600 dark:text-amber-400',
        badge: 'border-amber-300 text-amber-700 dark:text-amber-400',
      }
    case 'info':
    default:
      return {
        card: 'border-border bg-muted/30',
        icon: 'text-muted-foreground',
        badge: 'border-border text-muted-foreground',
      }
  }
}

function severityIcon(s: AttentionSeverity) {
  switch (s) {
    case 'critical':
      return AlertCircle
    case 'warning':
      return AlertTriangle
    case 'info':
    default:
      return Info
  }
}

// humanReason maps a typed reason code to dashboard-display copy.
// Server sends the typed code; the UI owns its own label so wording
// changes don't require a server roll.
function humanReason(reason: string): string {
  const map: Record<string, string> = {
    tax_calculation_failed: 'Tax calculation failed',
    tax_location_required: 'Customer address required',
    payment_failed: 'Payment failed',
    payment_unconfirmed: 'Payment unconfirmed',
    overdue: 'Past due',
  }
  return map[reason] ?? reason
}

function defaultLabel(action: AttentionAction): string {
  const map: Record<AttentionAction, string> = {
    edit_billing_profile: 'Edit billing profile',
    retry_tax: 'Retry tax',
    retry_payment: 'Retry payment',
    wait_provider: 'Check provider status',
    rotate_api_key: 'Rotate API key',
    reconcile_payment: 'Reconcile',
    review_registration: 'Review tax registration',
  }
  return map[action] ?? action
}

function formatRelative(iso: string): string {
  const ts = new Date(iso).getTime()
  if (Number.isNaN(ts)) return ''
  const deltaMs = Date.now() - ts
  const sec = Math.max(0, Math.floor(deltaMs / 1000))
  if (sec < 60) return 'just now'
  const min = Math.floor(sec / 60)
  if (min < 60) return `${min}m ago`
  const hr = Math.floor(min / 60)
  if (hr < 24) return `${hr}h ago`
  const days = Math.floor(hr / 24)
  return `${days}d ago`
}
