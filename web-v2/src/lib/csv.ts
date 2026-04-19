export function downloadCSV(filename: string, headers: string[], rows: string[][]) {
  const csv = [headers.join(','), ...rows.map(r => r.map(cell => `"${String(cell).replace(/"/g, '""')}"`).join(','))].join('\n')
  const blob = new Blob([csv], { type: 'text/csv' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.click()
  URL.revokeObjectURL(url)
}

// downloadCsv is an ergonomic wrapper that accepts an array of objects and
// derives headers from the first row (or from an explicit column list).
// Values are stringified via String(); null/undefined become empty cells.
export function downloadCsv<T extends Record<string, unknown>>(
  filename: string,
  rows: T[],
  columns?: string[],
) {
  if (rows.length === 0) return
  const headers = columns ?? Object.keys(rows[0])
  const body: string[][] = rows.map(row =>
    headers.map(h => {
      const v = (row as Record<string, unknown>)[h]
      return v == null ? '' : String(v)
    })
  )
  downloadCSV(filename, headers, body)
}
