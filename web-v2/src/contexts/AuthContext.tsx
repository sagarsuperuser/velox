import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { authApi, type SessionInfo } from '@/lib/auth'
import { ApiError } from '@/lib/api'

interface AuthState {
  user: SessionInfo | null
  loading: boolean
  login: (email: string, password: string) => Promise<void>
  logout: () => Promise<void>
  toggleLivemode: () => Promise<void>
  refresh: () => Promise<void>
}

const AuthContext = createContext<AuthState | null>(null)

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<SessionInfo | null>(null)
  const [loading, setLoading] = useState(true)
  const queryClient = useQueryClient()

  const refresh = useCallback(async () => {
    try {
      const info = await authApi.whoami()
      setUser(info)
    } catch (err) {
      // 401 is the expected "not logged in" path — the user will be sent to
      // /login by ProtectedRoute. Anything else is logged but treated the
      // same to avoid pinning the app on a transient error.
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
      email: res.email,
      tenant_id: res.tenant_id,
      livemode: res.livemode,
    })
    // Fresh session, fresh data — stale cache from a prior session must not
    // leak across login boundaries.
    queryClient.clear()
  }, [queryClient])

  const logout = useCallback(async () => {
    try {
      await authApi.logout()
    } catch (err) {
      // Cookie may already be expired server-side; treat as a local clear.
      console.warn('logout request failed, clearing client state anyway:', err)
    }
    setUser(null)
    queryClient.clear()
  }, [queryClient])

  const toggleLivemode = useCallback(async () => {
    if (!user) return
    const next = !user.livemode
    const res = await authApi.setLivemode(next)
    setUser({ ...user, livemode: res.livemode })
    // Test and live are fully partitioned views — clear all cached tenant
    // data so the UI re-fetches under the new mode.
    queryClient.clear()
  }, [user, queryClient])

  return (
    <AuthContext.Provider value={{ user, loading, login, logout, toggleLivemode, refresh }}>
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
