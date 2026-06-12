import { useEffect } from 'react'

// usePageTitle sets the browser-tab title for the current page —
// "Invoices · Velox", "INV-2026-0042 · Velox" — so multi-tab operator
// workflows are navigable (every tab previously read the Vite default).
// Pass the entity label once loaded; undefined falls back to the app name.
export function usePageTitle(title?: string) {
  useEffect(() => {
    document.title = title ? `${title} · Velox` : 'Velox'
    return () => {
      document.title = 'Velox'
    }
  }, [title])
}
