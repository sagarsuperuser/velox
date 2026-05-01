import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { authApi, type SessionInfo } from '@/lib/auth'
import { ApiError } from '@/lib/api'

// AuthContext exposes the resolved session context to the rest of the
// dashboard. Per ADR-011, the dashboard signs in via email + password;
// the resulting session is user-bound (UserContext.user_id is the
// canonical identifier). Email is rendered in the user dropdown.
//
// SDK callers using Authorization: Bearer never reach this context —
// they don't render React. The user_id field is therefore always
// present on a populated UserContext.
export interface UserContext {
  user_id: string
  tenant_id: string
  email: string
  livemode: boolean
}

interface AuthState {
  user: UserContext | null
  loading: boolean
  login: (email: string, password: string) => Promise<void>
  logout: () => Promise<void>
  refresh: () => Promise<void>
  setMode: (livemode: boolean) => Promise<void>
}

const AuthContext = createContext<AuthState | null>(null)

function toUserContext(s: SessionInfo): UserContext | null {
  // Cookie path returns user_id + email; Bearer path doesn't. The
  // dashboard only ever rides the cookie path, so a missing user_id
  // means whoami resolved to a Bearer key (e.g. someone hand-rolled
  // an Authorization header) — treat as not-signed-in.
  if (!s.user_id) return null
  return {
    user_id: s.user_id,
    tenant_id: s.tenant_id,
    email: s.email ?? '',
    livemode: s.livemode,
  }
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<UserContext | null>(null)
  const [loading, setLoading] = useState(true)
  const queryClient = useQueryClient()

  const refresh = useCallback(async () => {
    try {
      const info = await authApi.whoami()
      setUser(toUserContext(info))
    } catch (err) {
      // 401 means no session — the user will be sent to /login by
      // ProtectedRoute. Anything else is logged but treated the same
      // to avoid pinning the app on a transient error.
      if (!(err instanceof ApiError) || err.status !== 401) {
        console.error('whoami failed:', err)
      }
      setUser(null)
    }
  }, [])

  useEffect(() => {
    refresh().finally(() => setLoading(false))
  }, [refresh])

  const login = useCallback(async (email: string, password: string) => {
    const res = await authApi.login(email, password)
    setUser({
      user_id: res.user_id,
      tenant_id: res.tenant_id,
      email: res.email,
      livemode: res.livemode,
    })
    // Fresh session, fresh data — stale cache from any prior session
    // must not leak across login boundaries.
    queryClient.clear()
  }, [queryClient])

  const logout = useCallback(async () => {
    try {
      await authApi.logout()
    } catch (err) {
      // Cookie may already be expired server-side; treat as a local
      // clear and keep going.
      console.warn('logout request failed, clearing client state anyway:', err)
    }
    setUser(null)
    queryClient.clear()
  }, [queryClient])

  const setMode = useCallback(async (livemode: boolean) => {
    await authApi.setMode(livemode)
    setUser(prev => (prev ? { ...prev, livemode } : prev))
    // Mode-scoped data (customers, invoices, keys) must repopulate;
    // stale rows from the prior mode would otherwise render until the
    // next refetch.
    queryClient.clear()
  }, [queryClient])

  return (
    <AuthContext.Provider value={{ user, loading, login, logout, refresh, setMode }}>
      {children}
    </AuthContext.Provider>
  )
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthContext)
  if (!ctx) {
    throw new Error('useAuth must be used within AuthProvider')
  }
  return ctx
}
