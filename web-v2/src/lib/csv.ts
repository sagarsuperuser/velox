// downloadServerCSV hits a server-side streaming CSV endpoint
// (/v1/exports/*.csv) and triggers a browser download. Used for
// "all rows" exports where the dataset is too large to load
// client-side. The dashboard rides cookie auth (credentials:
// 'include'); SDK callers can use the underlying URL directly with
// a Bearer header.
//
// The endpoint sets Content-Disposition with a timestamped filename;
// the browser respects that, so we don't need to set `download` on
// the anchor — but we do for older browsers that ignore the header.
export async function downloadServerCSV(path: string, defaultFilename: string): Promise<void> {
  const API_BASE = (window as { __VELOX_API_BASE?: string }).__VELOX_API_BASE || ''
  const url = `${API_BASE}${path}`
  const res = await fetch(url, { credentials: 'include' })
  if (!res.ok) {
    const text = await res.text().catch(() => res.statusText)
    throw new Error(`Export failed (${res.status}): ${text}`)
  }
  // Honour the server's filename if present.
  let filename = defaultFilename
  const cd = res.headers.get('Content-Disposition') || ''
  const match = /filename="([^"]+)"/.exec(cd)
  if (match) filename = match[1]
  const blob = await res.blob()
  const blobUrl = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = blobUrl
  a.download = filename
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  URL.revokeObjectURL(blobUrl)
}

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
