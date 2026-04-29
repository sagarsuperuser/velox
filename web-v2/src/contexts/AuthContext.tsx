import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { authApi, type SessionInfo } from '@/lib/auth'
import { ApiError } from '@/lib/api'

// AuthContext exposes the resolved session context to the rest of the
// dashboard. There is no user account in v1 (see ADR-007/008); the
// "user" object carries the parent API key's tenant_id, key_id,
// key_type, and livemode for display.
export interface UserContext {
  tenant_id: string
  key_id: string
  key_type: string
  livemode: boolean
}

interface AuthState {
  user: UserContext | null
  loading: boolean
  login: (apiKey: string) => Promise<void>
  logout: () => Promise<void>
  refresh: () => Promise<void>
}

const AuthContext = createContext<AuthState | null>(null)

function toUserContext(s: SessionInfo): UserContext {
  return {
    tenant_id: s.tenant_id,
    key_id: s.key_id,
    key_type: s.key_type,
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

  const login = useCallback(async (apiKey: string) => {
    const res = await authApi.exchange(apiKey)
    setUser(toUserContext(res))
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

  return (
    <AuthContext.Provider value={{ user, loading, login, logout, refresh }}>
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
