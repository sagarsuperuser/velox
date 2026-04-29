// Dashboard auth: the operator pastes a `vlx_secret_…` key into the
// login screen; the backend's POST /v1/auth/exchange validates the key
// and issues an httpOnly session cookie. Subsequent requests ride the
// cookie via `credentials: 'include'`. The raw key never touches
// localStorage or any other JS-readable storage — the only place it
// exists in the browser is in the form field for the duration of the
// submit. See ADR-008 for the rationale.

import { apiRequest } from './api'

export interface SessionInfo {
  tenant_id: string
  key_id: string
  key_type: string
  livemode: boolean
}

export interface ExchangeResponse extends SessionInfo {
  expires_at: string
}

export const authApi = {
  // exchange takes a pasted API key, validates it server-side, and
  // sets the httpOnly session cookie on the response. The raw key is
  // never echoed back; the response carries only the resolved context.
  exchange: (apiKey: string) =>
    apiRequest<ExchangeResponse>('POST', '/auth/exchange', { api_key: apiKey }),

  // logout revokes the server-side session row and clears the cookie.
  logout: () => apiRequest<void>('POST', '/auth/logout'),

  // whoami resolves the current session (or Bearer key, for SDK
  // callers) to its tenant context. Throws ApiError(401) when neither
  // credential is present or both are stale.
  whoami: () => apiRequest<SessionInfo>('GET', '/whoami'),
}
