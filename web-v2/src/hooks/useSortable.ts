import { useState, useMemo } from 'react'

type SortDir = 'asc' | 'desc'

export function useSortable<T>(data: T[], defaultSort: string, defaultDir: SortDir = 'desc') {
  const [sortKey, setSortKey] = useState(defaultSort)
  const [sortDir, setSortDir] = useState<SortDir>(defaultDir)

  const onSort = (key: string) => {
    if (key === sortKey) {
      setSortDir(d => (d === 'asc' ? 'desc' : 'asc'))
    } else {
      setSortKey(key)
      setSortDir(defaultDir)
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

  return { sorted, sortKey, sortDir, onSort }
}
