import { useMemo, type ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { useAuth } from '@/contexts/AuthContext'

const queryDefaults = {
  queries: {
    staleTime: 5_000,
    retry: 1,
    refetchOnWindowFocus: true,
    refetchOnMount: true,
  },
} as const

// Mode-scoped data (customers, invoices, subscriptions, API keys, audit
// log entries, etc.) lives in fully separate caches per mode. Toggling
// Test↔Live swaps the active QueryClient, so every useQuery/useMutation
// inside the route tree resubscribes against the new mode's cache. Going
// back the other way reuses the prior cache, so back-and-forth toggling
// is instant after the first fetch — no refetch storm, no stale rows.
//
// Recreated whenever the user identity changes (logout / different
// sign-in) so the previous user's cache is gc'd. Anonymous bucket while
// auth loads keeps the public routes (/login, /portal) functional.
export function ModeAwareQueryProvider({ children }: { children: ReactNode }) {
  const { user } = useAuth()
  const userKey = user ? `${user.tenant_id}:${user.user_id}` : 'anon'

  const { testClient, liveClient } = useMemo(
    () => ({
      testClient: new QueryClient({ defaultOptions: queryDefaults }),
      liveClient: new QueryClient({ defaultOptions: queryDefaults }),
    }),
    [userKey],
  )

  const active = user?.livemode ? liveClient : testClient
  return <QueryClientProvider client={active}>{children}</QueryClientProvider>
}
