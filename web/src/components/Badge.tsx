import { cn } from '@/lib/cn'

const variants: Record<string, string> = {
  // Green: active, paid, succeeded, resolved, grant, create
  active: 'bg-emerald-100 text-emerald-700 ring-emerald-600/20 dark:bg-emerald-900/30 dark:text-emerald-400 dark:ring-emerald-400/20',
  paid: 'bg-emerald-100 text-emerald-700 ring-emerald-600/20 dark:bg-emerald-900/30 dark:text-emerald-400 dark:ring-emerald-400/20',
  succeeded: 'bg-emerald-100 text-emerald-700 ring-emerald-600/20 dark:bg-emerald-900/30 dark:text-emerald-400 dark:ring-emerald-400/20',
  resolved: 'bg-emerald-100 text-emerald-700 ring-emerald-600/20 dark:bg-emerald-900/30 dark:text-emerald-400 dark:ring-emerald-400/20',
  grant: 'bg-emerald-100 text-emerald-700 ring-emerald-600/20 dark:bg-emerald-900/30 dark:text-emerald-400 dark:ring-emerald-400/20',
  create: 'bg-emerald-100 text-emerald-700 ring-emerald-600/20 dark:bg-emerald-900/30 dark:text-emerald-400 dark:ring-emerald-400/20',
  refunded: 'bg-emerald-100 text-emerald-700 ring-emerald-600/20 dark:bg-emerald-900/30 dark:text-emerald-400 dark:ring-emerald-400/20',

  // Blue: finalized, processing, issued, update, usage, scheduled
  finalized: 'bg-blue-100 text-blue-700 ring-blue-600/20 dark:bg-blue-900/30 dark:text-blue-400 dark:ring-blue-400/20',
  processing: 'bg-blue-100 text-blue-700 ring-blue-600/20 dark:bg-blue-900/30 dark:text-blue-400 dark:ring-blue-400/20',
  issued: 'bg-blue-100 text-blue-700 ring-blue-600/20 dark:bg-blue-900/30 dark:text-blue-400 dark:ring-blue-400/20',
  update: 'bg-blue-100 text-blue-700 ring-blue-600/20 dark:bg-blue-900/30 dark:text-blue-400 dark:ring-blue-400/20',
  usage: 'bg-blue-100 text-blue-700 ring-blue-600/20 dark:bg-blue-900/30 dark:text-blue-400 dark:ring-blue-400/20',
  scheduled: 'bg-blue-100 text-blue-700 ring-blue-600/20 dark:bg-blue-900/30 dark:text-blue-400 dark:ring-blue-400/20',
  credit: 'bg-blue-100 text-blue-700 ring-blue-600/20 dark:bg-blue-900/30 dark:text-blue-400 dark:ring-blue-400/20',

  // Indigo: refund type
  refund: 'bg-indigo-100 text-indigo-700 ring-indigo-600/20 dark:bg-indigo-900/30 dark:text-indigo-400 dark:ring-indigo-400/20',

  // Red: voided, canceled, failed, delete, revoked, refund_failed
  voided: 'bg-red-100 text-red-700 ring-red-600/20 dark:bg-red-900/30 dark:text-red-400 dark:ring-red-400/20',
  canceled: 'bg-red-100 text-red-700 ring-red-600/20 dark:bg-red-900/30 dark:text-red-400 dark:ring-red-400/20',
  failed: 'bg-red-100 text-red-700 ring-red-600/20 dark:bg-red-900/30 dark:text-red-400 dark:ring-red-400/20',
  delete: 'bg-red-100 text-red-700 ring-red-600/20 dark:bg-red-900/30 dark:text-red-400 dark:ring-red-400/20',
  revoked: 'bg-red-100 text-red-700 ring-red-600/20 dark:bg-red-900/30 dark:text-red-400 dark:ring-red-400/20',
  refund_failed: 'bg-red-100 text-red-700 ring-red-600/20 dark:bg-red-900/30 dark:text-red-400 dark:ring-red-400/20',

  // Amber: paused, pending, manual_review, adjustment, refund_pending, retries_exhausted
  paused: 'bg-amber-100 text-amber-700 ring-amber-600/20 dark:bg-amber-900/30 dark:text-amber-400 dark:ring-amber-400/20',
  pending: 'bg-amber-100 text-amber-700 ring-amber-600/20 dark:bg-amber-900/30 dark:text-amber-400 dark:ring-amber-400/20',
  refund_pending: 'bg-amber-100 text-amber-700 ring-amber-600/20 dark:bg-amber-900/30 dark:text-amber-400 dark:ring-amber-400/20',
  manual_review: 'bg-amber-100 text-amber-700 ring-amber-600/20 dark:bg-amber-900/30 dark:text-amber-400 dark:ring-amber-400/20',
  adjustment: 'bg-amber-100 text-amber-700 ring-amber-600/20 dark:bg-amber-900/30 dark:text-amber-400 dark:ring-amber-400/20',
  retries_exhausted: 'bg-amber-100 text-amber-700 ring-amber-600/20 dark:bg-amber-900/30 dark:text-amber-400 dark:ring-amber-400/20',
  manually_resolved: 'bg-emerald-100 text-emerald-700 ring-emerald-600/20 dark:bg-emerald-900/30 dark:text-emerald-400 dark:ring-emerald-400/20',
  payment_recovered: 'bg-emerald-100 text-emerald-700 ring-emerald-600/20 dark:bg-emerald-900/30 dark:text-emerald-400 dark:ring-emerald-400/20',

  // Gray: draft, archived, expiry
  draft: 'bg-gray-200 text-gray-700 ring-gray-600/20 dark:bg-gray-700/40 dark:text-gray-300 dark:ring-gray-500/20',
  archived: 'bg-gray-200 text-gray-700 ring-gray-600/20 dark:bg-gray-700/40 dark:text-gray-300 dark:ring-gray-500/20',
  expiry: 'bg-gray-200 text-gray-700 ring-gray-600/20 dark:bg-gray-700/40 dark:text-gray-300 dark:ring-gray-500/20',

  // Purple: escalated, secret
  escalated: 'bg-violet-100 text-violet-700 ring-violet-600/20 dark:bg-violet-900/30 dark:text-violet-400 dark:ring-violet-400/20',
  secret: 'bg-violet-100 text-violet-700 ring-violet-600/20 dark:bg-violet-900/30 dark:text-violet-400 dark:ring-violet-400/20',

  // Orange: platform
  platform: 'bg-orange-100 text-orange-700 ring-orange-600/20 dark:bg-orange-900/30 dark:text-orange-400 dark:ring-orange-400/20',

  // Cyan: publishable
  publishable: 'bg-cyan-100 text-cyan-700 ring-cyan-600/20 dark:bg-cyan-900/30 dark:text-cyan-400 dark:ring-cyan-400/20',

  // Audit actions
  run: 'bg-blue-100 text-blue-700 ring-blue-600/20 dark:bg-blue-900/30 dark:text-blue-400 dark:ring-blue-400/20',
  finalize: 'bg-blue-100 text-blue-700 ring-blue-600/20 dark:bg-blue-900/30 dark:text-blue-400 dark:ring-blue-400/20',
  void: 'bg-red-100 text-red-700 ring-red-600/20 dark:bg-red-900/30 dark:text-red-400 dark:ring-red-400/20',
  cancel: 'bg-red-100 text-red-700 ring-red-600/20 dark:bg-red-900/30 dark:text-red-400 dark:ring-red-400/20',
  activate: 'bg-emerald-100 text-emerald-700 ring-emerald-600/20 dark:bg-emerald-900/30 dark:text-emerald-400 dark:ring-emerald-400/20',
  pause: 'bg-amber-100 text-amber-700 ring-amber-600/20 dark:bg-amber-900/30 dark:text-amber-400 dark:ring-amber-400/20',
  resume: 'bg-emerald-100 text-emerald-700 ring-emerald-600/20 dark:bg-emerald-900/30 dark:text-emerald-400 dark:ring-emerald-400/20',
  change_plan: 'bg-blue-100 text-blue-700 ring-blue-600/20 dark:bg-blue-900/30 dark:text-blue-400 dark:ring-blue-400/20',
  issue: 'bg-blue-100 text-blue-700 ring-blue-600/20 dark:bg-blue-900/30 dark:text-blue-400 dark:ring-blue-400/20',
  resolve: 'bg-emerald-100 text-emerald-700 ring-emerald-600/20 dark:bg-emerald-900/30 dark:text-emerald-400 dark:ring-emerald-400/20',
  adjust: 'bg-amber-100 text-amber-700 ring-amber-600/20 dark:bg-amber-900/30 dark:text-amber-400 dark:ring-amber-400/20',
  revoke: 'bg-red-100 text-red-700 ring-red-600/20 dark:bg-red-900/30 dark:text-red-400 dark:ring-red-400/20',

  // Coupons
  percentage: 'bg-blue-100 text-blue-700 ring-blue-600/20 dark:bg-blue-900/30 dark:text-blue-400 dark:ring-blue-400/20',
  fixed_amount: 'bg-violet-100 text-violet-700 ring-violet-600/20 dark:bg-violet-900/30 dark:text-violet-400 dark:ring-violet-400/20',
  inactive: 'bg-gray-200 text-gray-700 ring-gray-600/20 dark:bg-gray-700/40 dark:text-gray-300 dark:ring-gray-500/20',
  expired: 'bg-red-100 text-red-700 ring-red-600/20 dark:bg-red-900/30 dark:text-red-400 dark:ring-red-400/20',
  maxed: 'bg-amber-100 text-amber-700 ring-amber-600/20 dark:bg-amber-900/30 dark:text-amber-400 dark:ring-amber-400/20',

  // Additional
  monthly: 'bg-blue-100 text-blue-700 ring-blue-600/20 dark:bg-blue-900/30 dark:text-blue-400 dark:ring-blue-400/20',
  yearly: 'bg-violet-100 text-violet-700 ring-violet-600/20 dark:bg-violet-900/30 dark:text-violet-400 dark:ring-violet-400/20',
  sum: 'bg-gray-200 text-gray-700 ring-gray-600/20 dark:bg-gray-700/40 dark:text-gray-300 dark:ring-gray-500/20',
  count: 'bg-gray-200 text-gray-700 ring-gray-600/20 dark:bg-gray-700/40 dark:text-gray-300 dark:ring-gray-500/20',
  max: 'bg-gray-200 text-gray-700 ring-gray-600/20 dark:bg-gray-700/40 dark:text-gray-300 dark:ring-gray-500/20',
  last: 'bg-gray-200 text-gray-700 ring-gray-600/20 dark:bg-gray-700/40 dark:text-gray-300 dark:ring-gray-500/20',
  flat: 'bg-blue-100 text-blue-700 ring-blue-600/20 dark:bg-blue-900/30 dark:text-blue-400 dark:ring-blue-400/20',
  graduated: 'bg-blue-100 text-blue-700 ring-blue-600/20 dark:bg-blue-900/30 dark:text-blue-400 dark:ring-blue-400/20',
  package: 'bg-blue-100 text-blue-700 ring-blue-600/20 dark:bg-blue-900/30 dark:text-blue-400 dark:ring-blue-400/20',
  base_fee: 'bg-gray-200 text-gray-700 ring-gray-600/20 dark:bg-gray-700/40 dark:text-gray-300 dark:ring-gray-500/20',
}

const displayNames: Record<string, string> = {
  // Invoice / payment
  draft: 'Draft', finalized: 'Finalized', paid: 'Paid', voided: 'Voided',
  pending: 'Pending', processing: 'Processing', succeeded: 'Succeeded', failed: 'Failed',
  // Dunning states
  escalated: 'Escalated', resolved: 'Resolved',
  // Dunning resolutions
  payment_recovered: 'Payment Recovered', retries_exhausted: 'Retries Exhausted',
  manually_resolved: 'Manually Resolved',
  // Credit notes
  issued: 'Issued', refunded: 'Refunded', refund_failed: 'Refund Failed',
  refund_pending: 'Refund Pending',
  // Credits
  grant: 'Grant', usage: 'Usage', adjustment: 'Adjustment', expiry: 'Expiry',
  // Subscriptions
  active: 'Active', paused: 'Paused', canceled: 'Canceled',
  // Pricing
  flat: 'Flat', graduated: 'Graduated', package: 'Package',
  // CN types
  credit: 'Credit', refund: 'Refund',
  // Coupons
  percentage: 'Percentage', fixed_amount: 'Fixed Amount',
  inactive: 'Inactive', expired: 'Expired', maxed: 'Maxed Out',
  deactivate: 'Deactivate',
  // Other
  revoked: 'Revoked',
}

export function Badge({ status, label }: { status: string; label?: string }) {
  return (
    <span
      className={cn(
        'inline-flex items-center px-2 py-0.5 rounded-md text-xs font-medium ring-1 ring-inset',
        variants[status] || 'bg-gray-200 text-gray-700 ring-gray-600/20'
      )}
    >
      {label || displayNames[status] || status.replace(/_/g, ' ')}
    </span>
  )
}
