import { cn } from '@/lib/cn'

const variants: Record<string, string> = {
  active: 'bg-emerald-50 text-emerald-700 border-emerald-200',
  draft: 'bg-gray-50 text-gray-600 border-gray-200',
  finalized: 'bg-blue-50 text-blue-700 border-blue-200',
  paid: 'bg-emerald-50 text-emerald-700 border-emerald-200',
  voided: 'bg-red-50 text-red-600 border-red-200',
  canceled: 'bg-red-50 text-red-600 border-red-200',
  paused: 'bg-amber-50 text-amber-700 border-amber-200',
  pending: 'bg-amber-50 text-amber-700 border-amber-200',
  processing: 'bg-blue-50 text-blue-700 border-blue-200',
  succeeded: 'bg-emerald-50 text-emerald-700 border-emerald-200',
  failed: 'bg-red-50 text-red-600 border-red-200',
  archived: 'bg-gray-50 text-gray-500 border-gray-200',
  retry_due: 'bg-orange-50 text-orange-700 border-orange-200',
  escalated: 'bg-purple-50 text-purple-700 border-purple-200',
  exhausted: 'bg-gray-50 text-gray-500 border-gray-200',
  resolved: 'bg-emerald-50 text-emerald-700 border-emerald-200',
  issued: 'bg-blue-50 text-blue-700 border-blue-200',
  scheduled: 'bg-blue-50 text-blue-600 border-blue-200',
  manual_review: 'bg-amber-50 text-amber-700 border-amber-200',
  grant: 'bg-emerald-50 text-emerald-700 border-emerald-200',
  usage: 'bg-blue-50 text-blue-700 border-blue-200',
  adjustment: 'bg-amber-50 text-amber-700 border-amber-200',
  create: 'bg-emerald-50 text-emerald-700 border-emerald-200',
  update: 'bg-blue-50 text-blue-700 border-blue-200',
  delete: 'bg-red-50 text-red-600 border-red-200',
  secret: 'bg-purple-50 text-purple-700 border-purple-200',
  publishable: 'bg-cyan-50 text-cyan-700 border-cyan-200',
  platform: 'bg-orange-50 text-orange-700 border-orange-200',
  revoked: 'bg-red-50 text-red-600 border-red-200',
}

export function Badge({ status }: { status: string }) {
  return (
    <span
      className={cn(
        'inline-flex items-center px-2 py-0.5 rounded-md text-xs font-medium border',
        variants[status] || 'bg-gray-50 text-gray-600 border-gray-200'
      )}
    >
      {status}
    </span>
  )
}
