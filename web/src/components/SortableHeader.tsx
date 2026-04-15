import { ChevronUp, ChevronDown } from 'lucide-react'

interface SortableHeaderProps {
  label: string
  sortKey: string
  activeSortKey: string
  sortDir: 'asc' | 'desc'
  onSort: (key: string) => void
  align?: 'left' | 'right'
}

export function SortableHeader({ label, sortKey, activeSortKey, sortDir, onSort, align = 'left' }: SortableHeaderProps) {
  const isActive = sortKey === activeSortKey

  return (
    <th
      className={`${align === 'right' ? 'text-right' : 'text-left'} text-xs font-medium text-gray-500 px-6 py-3 cursor-pointer select-none hover:text-gray-700 transition-colors`}
      onClick={() => onSort(sortKey)}
    >
      <span className="inline-flex items-center gap-1">
        {label}
        {isActive ? (
          sortDir === 'asc' ? (
            <ChevronUp size={14} className="text-gray-700" />
          ) : (
            <ChevronDown size={14} className="text-gray-700" />
          )
        ) : (
          <ChevronDown size={14} className="text-gray-300" />
        )}
      </span>
    </th>
  )
}
