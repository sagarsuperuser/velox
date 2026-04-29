import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { authApi, getApiKey, setApiKey, clearApiKey, type SessionInfo } from '@/lib/auth'
import { ApiError } from '@/lib/api'

// AuthContext exposes the resolved API-key context to the rest of the
// dashboard. `user` is the historical name; for API-key auth there is no
// user account, so it carries the key's tenant_id / livemode / key_type
// instead. `email` and `user_id` no longer apply and aren't surfaced.
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
    if (!getApiKey()) {
      setUser(null)
      return
    }
    try {
      const info = await authApi.whoami()
      setUser(toUserContext(info))
    } catch (err) {
      // 401 means the stored key was revoked or invalid — drop it and
      // route the user to /login by clearing state. Anything else is
      // logged but treated the same to avoid pinning the app on a
      // transient error.
      if (!(err instanceof ApiError) || err.status !== 401) {
        console.error('whoami failed:', err)
      }
      clearApiKey()
      setUser(null)
    }
  }, [])

  useEffect(() => {
    refresh().finally(() => setLoading(false))
  }, [refresh])

  const login = useCallback(async (apiKey: string) => {
    // Stage the key so apiRequest can attach it on the whoami probe.
    setApiKey(apiKey)
    try {
      const info = await authApi.whoami()
      setUser(toUserContext(info))
      // Fresh login, fresh data — stale cache from any prior key must
      // not leak across login boundaries.
      queryClient.clear()
    } catch (err) {
      // Roll back the staged key so a failed login doesn't leave a bad
      // value in localStorage that the next refresh would pick up.
      clearApiKey()
      throw err
    }
  }, [queryClient])

  const logout = useCallback(async () => {
    clearApiKey()
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
