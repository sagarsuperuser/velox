import { apiRequest } from './api'

// SessionInfo mirrors the /v1/session GET response. Lives here rather than
// api.ts because it's tied to the auth/session lifecycle, not tenant data.
export interface SessionInfo {
  user_id: string
  email: string
  tenant_id: string
  livemode: boolean
}

export interface LoginResponse extends SessionInfo {
  expires_at: string
}

export const authApi = {
  login: (email: string, password: string) =>
    apiRequest<LoginResponse>('POST', '/auth/login', { email, password }),

  logout: () => apiRequest<void>('POST', '/auth/logout'),

  whoami: () => apiRequest<SessionInfo>('GET', '/session'),

  setLivemode: (livemode: boolean) =>
    apiRequest<{ livemode: boolean }>('PATCH', '/session', { livemode }),

  // Always resolves (backend returns 202 regardless of whether the email
  // matches a real user) — enumeration resistance lives in the server.
  requestPasswordReset: (email: string) =>
    apiRequest<void>('POST', '/auth/password-reset-request', { email }),

  confirmPasswordReset: (token: string, password: string) =>
    apiRequest<void>('POST', '/auth/password-reset-confirm', { token, password }),
}
