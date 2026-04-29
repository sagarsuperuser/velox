// Dashboard auth is API-key based: the operator pastes a `vlx_secret_…`
// key into the login screen; we validate it against /v1/whoami, store it
// in localStorage, and every subsequent apiRequest reads it back to set
// the Authorization: Bearer header. No cookies, no sessions, no users —
// the dashboard is a thin client over the public HTTP API.

import { apiRequest } from './api'

export interface SessionInfo {
  tenant_id: string
  key_id: string
  key_type: string
  livemode: boolean
}

const STORAGE_KEY = 'velox_api_key'

export function getApiKey(): string | null {
  try {
    return localStorage.getItem(STORAGE_KEY)
  } catch {
    return null
  }
}

export function setApiKey(key: string): void {
  localStorage.setItem(STORAGE_KEY, key)
}

export function clearApiKey(): void {
  try {
    localStorage.removeItem(STORAGE_KEY)
  } catch {
    /* ignore */
  }
}

export const authApi = {
  // whoami resolves the currently-stored API key against the backend.
  // Returns the tenant context on success; throws an ApiError on 401
  // (invalid / revoked key) so the caller can route the user to /login.
  whoami: () => apiRequest<SessionInfo>('GET', '/whoami'),
}
