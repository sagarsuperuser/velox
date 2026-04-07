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
