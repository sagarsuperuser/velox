// Customer-portal client-side auth + fetch wrapper. Distinct from the
// operator dashboard's cookie-based session (AuthContext) — the
// portal carries a 1-hour bearer token returned by
// /v1/public/customer-portal/magic/consume and replays it on every
// /v1/me/* request.
//
// Storage: localStorage. Tokens are short-lived (1h default) and
// only hold customer-scoped read/cancel/PM-update permission, so
// localStorage is acceptable per ADR-008's threat model for the
// invoice-public-token equivalent. XSS exfiltration risk is bounded
// by the 1-hour TTL and the customer-only authority.

const TOKEN_KEY = 'velox_portal_token'
const EXPIRES_KEY = 'velox_portal_expires_at'
const CUSTOMER_KEY = 'velox_portal_customer_id'

const apiBase = import.meta.env.VITE_API_URL || ''

export interface PortalSession {
  token: string
  customerId: string
  expiresAt: Date
}

export function setPortalSession(s: PortalSession): void {
  localStorage.setItem(TOKEN_KEY, s.token)
  localStorage.setItem(EXPIRES_KEY, s.expiresAt.toISOString())
  localStorage.setItem(CUSTOMER_KEY, s.customerId)
}

export function getPortalSession(): PortalSession | null {
  const token = localStorage.getItem(TOKEN_KEY)
  const expiresStr = localStorage.getItem(EXPIRES_KEY)
  const customerId = localStorage.getItem(CUSTOMER_KEY)
  if (!token || !expiresStr || !customerId) return null
  const expiresAt = new Date(expiresStr)
  if (Number.isNaN(expiresAt.getTime()) || expiresAt < new Date()) {
    clearPortalSession()
    return null
  }
  return { token, customerId, expiresAt }
}

export function clearPortalSession(): void {
  localStorage.removeItem(TOKEN_KEY)
  localStorage.removeItem(EXPIRES_KEY)
  localStorage.removeItem(CUSTOMER_KEY)
}

// portalFetch is a fetch wrapper that attaches the portal Bearer
// token, parses JSON, and surfaces server errors as thrown Error
// (with .status if it was an HTTP error). On 401, clears the
// session so the caller's next render hits the redirect.
export async function portalFetch<T>(method: string, path: string, body?: unknown): Promise<T> {
  const session = getPortalSession()
  if (!session) {
    throw new PortalAuthError('Portal session expired. Please request a new login link.')
  }
  const res = await fetch(`${apiBase}${path}`, {
    method,
    headers: {
      'Authorization': `Bearer ${session.token}`,
      'Content-Type': 'application/json',
    },
    body: body ? JSON.stringify(body) : undefined,
  })
  if (res.status === 401) {
    clearPortalSession()
    throw new PortalAuthError('Portal session expired. Please request a new login link.')
  }
  if (!res.ok) {
    const errBody = await res.json().catch(() => ({ error: { message: res.statusText } }))
    const detail = typeof errBody.error === 'object' ? errBody.error : null
    const message = detail?.message || `HTTP ${res.status}`
    const err = new Error(message) as Error & { status?: number }
    err.status = res.status
    throw err
  }
  if (res.status === 204 || res.status === 205) {
    return undefined as T
  }
  return res.json() as Promise<T>
}

// Public (no-auth) portal endpoints — magic-link request + consume.

export async function requestMagicLink(email: string): Promise<void> {
  const res = await fetch(`${apiBase}/v1/public/customer-portal/magic-link`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ email }),
  })
  if (!res.ok && res.status !== 202) {
    const body = await res.json().catch(() => ({}))
    throw new Error(body?.error?.message || `HTTP ${res.status}`)
  }
}

interface ConsumeResponse {
  token: string
  customer_id: string
  livemode: boolean
  expires_at: string
}

export async function consumeMagicLink(token: string): Promise<PortalSession> {
  const res = await fetch(`${apiBase}/v1/public/customer-portal/magic/consume`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ token }),
  })
  if (!res.ok) {
    const body = await res.json().catch(() => ({}))
    throw new Error(body?.error?.message || 'invalid or expired magic link')
  }
  const data = (await res.json()) as ConsumeResponse
  const session: PortalSession = {
    token: data.token,
    customerId: data.customer_id,
    expiresAt: new Date(data.expires_at),
  }
  setPortalSession(session)
  return session
}

// PortalAuthError: thrown when the session is missing/expired.
// React pages catch this to redirect to /portal/login instead of
// showing a generic error.
export class PortalAuthError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'PortalAuthError'
  }
}
