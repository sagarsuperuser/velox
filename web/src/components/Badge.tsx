import { cn } from '@/lib/cn'

const variants: Record<string, string> = {
  // Green: active, paid, succeeded, resolved, grant, create
  active: 'bg-emerald-100 text-emerald-700 ring-emerald-600/20',
  paid: 'bg-emerald-100 text-emerald-700 ring-emerald-600/20',
  succeeded: 'bg-emerald-100 text-emerald-700 ring-emerald-600/20',
  resolved: 'bg-emerald-100 text-emerald-700 ring-emerald-600/20',
  grant: 'bg-emerald-100 text-emerald-700 ring-emerald-600/20',
  create: 'bg-emerald-100 text-emerald-700 ring-emerald-600/20',

  // Blue: finalized, processing, issued, update, usage, scheduled
  finalized: 'bg-blue-100 text-blue-700 ring-blue-600/20',
  processing: 'bg-blue-100 text-blue-700 ring-blue-600/20',
  issued: 'bg-blue-100 text-blue-700 ring-blue-600/20',
  update: 'bg-blue-100 text-blue-700 ring-blue-600/20',
  usage: 'bg-blue-100 text-blue-700 ring-blue-600/20',
  scheduled: 'bg-blue-100 text-blue-700 ring-blue-600/20',

  // Red: voided, canceled, failed, delete, revoked
  voided: 'bg-red-100 text-red-700 ring-red-600/20',
  canceled: 'bg-red-100 text-red-700 ring-red-600/20',
  failed: 'bg-red-100 text-red-700 ring-red-600/20',
  delete: 'bg-red-100 text-red-700 ring-red-600/20',
  revoked: 'bg-red-100 text-red-700 ring-red-600/20',

  // Amber: paused, pending, manual_review, adjustment
  paused: 'bg-amber-100 text-amber-700 ring-amber-600/20',
  pending: 'bg-amber-100 text-amber-700 ring-amber-600/20',
  manual_review: 'bg-amber-100 text-amber-700 ring-amber-600/20',
  adjustment: 'bg-amber-100 text-amber-700 ring-amber-600/20',

  // Gray: draft, archived, exhausted
  draft: 'bg-gray-200 text-gray-700 ring-gray-600/20',
  archived: 'bg-gray-200 text-gray-700 ring-gray-600/20',
  exhausted: 'bg-gray-200 text-gray-700 ring-gray-600/20',

  // Purple: escalated, secret
  escalated: 'bg-violet-100 text-violet-700 ring-violet-600/20',
  secret: 'bg-violet-100 text-violet-700 ring-violet-600/20',

  // Orange: retry_due, platform
  retry_due: 'bg-orange-100 text-orange-700 ring-orange-600/20',
  platform: 'bg-orange-100 text-orange-700 ring-orange-600/20',

  // Cyan: publishable
  publishable: 'bg-cyan-100 text-cyan-700 ring-cyan-600/20',

  // Audit actions
  run: 'bg-blue-100 text-blue-700 ring-blue-600/20',
  finalize: 'bg-blue-100 text-blue-700 ring-blue-600/20',
  void: 'bg-red-100 text-red-700 ring-red-600/20',
  cancel: 'bg-red-100 text-red-700 ring-red-600/20',
  activate: 'bg-emerald-100 text-emerald-700 ring-emerald-600/20',
  pause: 'bg-amber-100 text-amber-700 ring-amber-600/20',
  resume: 'bg-emerald-100 text-emerald-700 ring-emerald-600/20',
  change_plan: 'bg-blue-100 text-blue-700 ring-blue-600/20',
  issue: 'bg-blue-100 text-blue-700 ring-blue-600/20',
  resolve: 'bg-emerald-100 text-emerald-700 ring-emerald-600/20',
  adjust: 'bg-amber-100 text-amber-700 ring-amber-600/20',
  revoke: 'bg-red-100 text-red-700 ring-red-600/20',

  // Additional
  monthly: 'bg-blue-100 text-blue-700 ring-blue-600/20',
  yearly: 'bg-violet-100 text-violet-700 ring-violet-600/20',
  sum: 'bg-gray-200 text-gray-700 ring-gray-600/20',
  count: 'bg-gray-200 text-gray-700 ring-gray-600/20',
  max: 'bg-gray-200 text-gray-700 ring-gray-600/20',
  last: 'bg-gray-200 text-gray-700 ring-gray-600/20',
  flat: 'bg-blue-100 text-blue-700 ring-blue-600/20',
  graduated: 'bg-blue-100 text-blue-700 ring-blue-600/20',
  package: 'bg-blue-100 text-blue-700 ring-blue-600/20',
  base_fee: 'bg-gray-200 text-gray-700 ring-gray-600/20',
}

export function Badge({ status, label }: { status: string; label?: string }) {
  return (
    <span
      className={cn(
        'inline-flex items-center px-2 py-0.5 rounded-md text-xs font-medium ring-1 ring-inset',
        variants[status] || 'bg-gray-200 text-gray-700 ring-gray-600/20'
      )}
    >
      {label || status}
    </span>
  )
}
