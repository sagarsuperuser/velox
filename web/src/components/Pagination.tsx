import { ChevronLeft, ChevronRight } from 'lucide-react'

interface PaginationProps {
  page: number
  totalPages: number
  onPageChange: (page: number) => void
  pageSize?: number
  total?: number
}

export function Pagination({ page, totalPages, onPageChange, pageSize, total }: PaginationProps) {
  if (totalPages <= 1 && !total) return null

  const pages: (number | '...')[] = []

  if (totalPages > 1) {
    pages.push(1)
    const start = Math.max(2, page - 1)
    const end = Math.min(totalPages - 1, page + 1)
    if (start > 2) pages.push('...')
    for (let i = start; i <= end; i++) pages.push(i)
    if (end < totalPages - 1) pages.push('...')
    if (totalPages > 1) pages.push(totalPages)
  }

  const showingFrom = total && pageSize ? (page - 1) * pageSize + 1 : 0
  const showingTo = total && pageSize ? Math.min(page * pageSize, total) : 0

  return (
    <nav aria-label="Pagination" className="flex items-center justify-between py-4">
      {/* Showing X-Y of Z */}
      <div className="text-xs text-gray-500 dark:text-gray-400">
        {total != null && pageSize ? (
          <span>Showing <span className="font-medium text-gray-700 dark:text-gray-300">{showingFrom}–{showingTo}</span> of <span className="font-medium text-gray-700 dark:text-gray-300">{total.toLocaleString()}</span></span>
        ) : (
          <span>Page {page}{totalPages > 1 ? ` of ${totalPages}` : ''}</span>
        )}
      </div>

      {/* Page buttons */}
      {totalPages > 1 && (
        <div className="flex items-center gap-1">
          <button
            onClick={() => onPageChange(page - 1)}
            disabled={page === 1}
            className="flex items-center justify-center w-8 h-8 rounded-lg text-sm text-gray-600 dark:text-gray-400 hover:bg-gray-100 dark:hover:bg-gray-800 disabled:opacity-30 disabled:cursor-not-allowed transition-colors"
            aria-label="Previous page"
          >
            <ChevronLeft size={16} />
          </button>
          {pages.map((p, i) =>
            p === '...' ? (
              <span key={`ellipsis-${i}`} className="w-8 h-8 flex items-center justify-center text-xs text-gray-500">
                ...
              </span>
            ) : (
              <button
                key={p}
                onClick={() => onPageChange(p)}
                aria-label={`Page ${p}`}
                aria-current={p === page ? 'page' : undefined}
                className={`w-8 h-8 rounded-lg text-sm font-medium transition-colors ${
                  p === page
                    ? 'bg-velox-600 text-white'
                    : 'text-gray-600 dark:text-gray-400 hover:bg-gray-100 dark:hover:bg-gray-800'
                }`}
              >
                {p}
              </button>
            )
          )}
          <button
            onClick={() => onPageChange(page + 1)}
            disabled={page === totalPages}
            className="flex items-center justify-center w-8 h-8 rounded-lg text-sm text-gray-600 dark:text-gray-400 hover:bg-gray-100 dark:hover:bg-gray-800 disabled:opacity-30 disabled:cursor-not-allowed transition-colors"
            aria-label="Next page"
          >
            <ChevronRight size={16} />
          </button>
        </div>
      )}
    </nav>
  )
}
