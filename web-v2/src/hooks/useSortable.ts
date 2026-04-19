import { useMemo } from 'react'

export type SortDir = 'asc' | 'desc'

// Controlled client-side sort. State (sortKey, sortDir) lives outside so
// callers can back it with URL params via useUrlState. onChange handles the
// "flip direction on same key, reset to desc on new key" convention; callers
// only need to pass it to column headers.
export function useSortable<T>(
  data: T[],
  sortKey: string,
  sortDir: SortDir,
  onChange: (key: string, dir: SortDir) => void,
  defaultDir: SortDir = 'desc',
) {
  const onSort = (key: string) => {
    if (key === sortKey) {
      onChange(key, sortDir === 'asc' ? 'desc' : 'asc')
    } else {
      onChange(key, defaultDir)
    }
  }

  const sorted = useMemo(() => {
    const copy = [...data]
    copy.sort((a, b) => {
      const aVal = (a as Record<string, unknown>)[sortKey]
      const bVal = (b as Record<string, unknown>)[sortKey]

      if (aVal == null && bVal == null) return 0
      if (aVal == null) return 1
      if (bVal == null) return -1

      let cmp: number
      if (typeof aVal === 'number' && typeof bVal === 'number') {
        cmp = aVal - bVal
      } else {
        cmp = String(aVal).localeCompare(String(bVal))
      }

      return sortDir === 'asc' ? cmp : -cmp
    })
    return copy
  }, [data, sortKey, sortDir])

  return { sorted, onSort }
}
