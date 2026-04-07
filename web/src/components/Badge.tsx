import { cn } from '@/lib/cn'

const variants: Record<string, string> = {
  // Green: active, paid, succeeded, resolved, grant, create
  active: 'bg-emerald-50 text-emerald-600',
  paid: 'bg-emerald-50 text-emerald-600',
  succeeded: 'bg-emerald-50 text-emerald-600',
  resolved: 'bg-emerald-50 text-emerald-600',
  grant: 'bg-emerald-50 text-emerald-600',
  create: 'bg-emerald-50 text-emerald-600',

  // Blue: finalized, processing, issued, update, usage, scheduled
  finalized: 'bg-sky-50 text-sky-600',
  processing: 'bg-sky-50 text-sky-600',
  issued: 'bg-sky-50 text-sky-600',
  update: 'bg-sky-50 text-sky-600',
  usage: 'bg-sky-50 text-sky-600',
  scheduled: 'bg-sky-50 text-sky-600',

  // Red: voided, canceled, failed, delete, revoked
  voided: 'bg-rose-50 text-rose-600',
  canceled: 'bg-rose-50 text-rose-600',
  failed: 'bg-rose-50 text-rose-600',
  delete: 'bg-rose-50 text-rose-600',
  revoked: 'bg-rose-50 text-rose-600',

  // Amber: paused, pending, manual_review, adjustment
  paused: 'bg-amber-50 text-amber-600',
  pending: 'bg-amber-50 text-amber-600',
  manual_review: 'bg-amber-50 text-amber-600',
  adjustment: 'bg-amber-50 text-amber-600',

  // Gray: draft, archived, exhausted
  draft: 'bg-gray-100 text-gray-500',
  archived: 'bg-gray-100 text-gray-500',
  exhausted: 'bg-gray-100 text-gray-500',

  // Purple: escalated, secret
  escalated: 'bg-violet-50 text-violet-600',
  secret: 'bg-violet-50 text-violet-600',

  // Orange: retry_due, platform
  retry_due: 'bg-orange-50 text-orange-600',
  platform: 'bg-orange-50 text-orange-600',

  // Cyan: publishable
  publishable: 'bg-cyan-50 text-cyan-600',

  // Additional
  monthly: 'bg-sky-50 text-sky-600',
  yearly: 'bg-violet-50 text-violet-600',
  sum: 'bg-gray-100 text-gray-500',
  count: 'bg-gray-100 text-gray-500',
  max: 'bg-gray-100 text-gray-500',
  last: 'bg-gray-100 text-gray-500',
  flat: 'bg-sky-50 text-sky-600',
  graduated: 'bg-sky-50 text-sky-600',
  package: 'bg-sky-50 text-sky-600',
  base_fee: 'bg-gray-100 text-gray-500',
}

export function Badge({ status, label }: { status: string; label?: string }) {
  return (
    <span
      className={cn(
        'inline-flex items-center px-2 py-0.5 rounded-full text-xs font-medium',
        variants[status] || 'bg-gray-100 text-gray-500'
      )}
    >
      {label || status}
    </span>
  )
}
