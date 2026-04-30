import { useEffect } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api, setTenantTimezone } from '@/lib/api'
import { useAuth } from '@/contexts/AuthContext'

// TenantTimezoneBootstrap is a render-nothing component that fetches
// /v1/settings once the user is authenticated and seeds the module-
// scoped tenant timezone in lib/api.ts. Every subsequent
// formatDate/formatDateTime call across the dashboard then renders
// in tenant TZ without per-callsite changes. See ADR-010.
//
// Sits inside AuthProvider in main.tsx (so useAuth resolves) and
// uses react-query so the settings fetch is shared with any
// per-page useQuery(['settings']) callers — no duplicate request.
//
// Reactivity: the module-scoped variable is updated whenever the
// settings query data changes (e.g. operator saves a new TZ in
// Settings and the mutation invalidates ['settings']). Existing
// rendered components don't re-render automatically; a navigation
// or page refresh picks up the new TZ. Acceptable for v1.
export function TenantTimezoneBootstrap() {
  const { user } = useAuth()
  const { data } = useQuery({
    queryKey: ['settings'],
    queryFn: () => api.getSettings(),
    enabled: !!user,
    // Settings change rarely; let the cache stay fresh for the
    // session and only refetch on explicit invalidate (Settings
    // save) or window refocus.
    staleTime: 5 * 60 * 1000,
  })

  useEffect(() => {
    if (data?.timezone) {
      setTenantTimezone(data.timezone)
    } else if (!user) {
      // Logout — reset to browser-local fallback so a subsequent
      // login from a different tenant doesn't render the prior
      // tenant's TZ.
      setTenantTimezone(null)
    }
  }, [data?.timezone, user])

  return null
}
