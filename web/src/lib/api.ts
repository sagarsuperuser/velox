const API_BASE = '/v1'

let apiKey = localStorage.getItem('velox_api_key') || ''

export function setApiKey(key: string) {
  apiKey = key
  localStorage.setItem('velox_api_key', key)
}

export function getApiKey(): string {
  return apiKey
}

export function clearApiKey() {
  apiKey = ''
  localStorage.removeItem('velox_api_key')
}

function humanizeError(msg: string): string {
  // "already exists: subscription code "acme-pro"" → "A subscription with code "acme-pro" already exists"
  const alreadyExists = msg.match(/already exists: (\w+) (?:code|with external_id|key) "(.+?)"/)
  if (alreadyExists) {
    const resource = alreadyExists[1].replace(/_/g, ' ')
    return `A ${resource} with that identifier ("${alreadyExists[2]}") already exists. Please use a different one.`
  }
  // "already exists: ..." generic
  if (msg.startsWith('already exists:')) {
    return 'This resource already exists. Please use a different identifier.'
  }
  // "not found" errors
  if (msg.includes('not found')) {
    return msg.replace(/not found/, 'could not be found')
  }
  return msg
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
  }
  if (apiKey) {
    headers['Authorization'] = `Bearer ${apiKey}`
  }

  const res = await fetch(`${API_BASE}${path}`, {
    method,
    headers,
    body: body ? JSON.stringify(body) : undefined,
  })

  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: { message: res.statusText } }))
    const raw = err.error?.message || `HTTP ${res.status}`
    throw new Error(humanizeError(raw))
  }

  return res.json()
}

export const api = {
  // Customers
  listCustomers: (params?: string) =>
    request<{ data: Customer[]; total: number }>('GET', `/customers${params ? '?' + params : ''}`),
  getCustomer: (id: string) =>
    request<Customer>('GET', `/customers/${id}`),
  createCustomer: (data: { external_id: string; display_name: string; email?: string }) =>
    request<Customer>('POST', '/customers', data),

  // Subscriptions
  listSubscriptions: (params?: string) =>
    request<{ data: Subscription[] }>('GET', `/subscriptions${params ? '?' + params : ''}`),
  createSubscription: (data: { code: string; display_name: string; customer_id: string; plan_id: string; start_now?: boolean }) =>
    request<Subscription>('POST', '/subscriptions', data),

  // Pricing
  listMeters: () =>
    request<{ data: Meter[] }>('GET', '/meters'),
  getMeter: (id: string) =>
    request<Meter>('GET', `/meters/${id}`),
  createMeter: (data: { key: string; name: string; unit?: string; aggregation?: string; rating_rule_version_id?: string }) =>
    request<Meter>('POST', '/meters', data),
  listPlans: () =>
    request<{ data: Plan[] }>('GET', '/plans'),
  getPlan: (id: string) =>
    request<Plan>('GET', `/plans/${id}`),
  createPlan: (data: { code: string; name: string; currency: string; billing_interval: string; base_amount_cents: number; meter_ids?: string[] }) =>
    request<Plan>('POST', '/plans', data),
  updatePlan: (id: string, data: Partial<{ name: string; status: string; base_amount_cents: number; meter_ids: string[] }>) =>
    request<Plan>('PATCH', `/plans/${id}`, data),
  listRatingRules: () =>
    request<{ data: RatingRule[] }>('GET', '/rating-rules'),
  getRatingRule: (id: string) =>
    request<RatingRule>('GET', `/rating-rules/${id}`),
  createRatingRule: (data: { rule_key: string; name: string; mode: string; currency: string; flat_amount_cents?: number; graduated_tiers?: { up_to: number; unit_amount_cents: number }[]; package_size?: number; package_amount_cents?: number }) =>
    request<RatingRule>('POST', '/rating-rules', data),

  // Invoices
  listInvoices: (params?: string) =>
    request<{ data: Invoice[]; total: number }>('GET', `/invoices${params ? '?' + params : ''}`),
  getInvoice: (id: string) =>
    request<{ invoice: Invoice; line_items: LineItem[] }>('GET', `/invoices/${id}`),
  finalizeInvoice: (id: string) =>
    request<Invoice>('POST', `/invoices/${id}/finalize`),
  voidInvoice: (id: string) =>
    request<Invoice>('POST', `/invoices/${id}/void`),
  collectPayment: (id: string) =>
    request<Invoice>('POST', `/invoices/${id}/collect`),
  sendInvoiceEmail: (invoiceId: string, email: string) =>
    request<{ status: string }>('POST', `/invoices/${invoiceId}/send`, { email }),

  // Payment setup
  setupPayment: (data: { customer_id: string; customer_name: string; email: string; address_line1?: string; address_city?: string; address_state?: string; address_postal_code?: string; address_country?: string }) =>
    request<{ session_id: string; url: string; stripe_customer_id: string }>('POST', '/checkout/setup', data),
  getPaymentStatus: (customerId: string) =>
    request<{ customer_id: string; setup_status: string; stripe_customer_id?: string; payment_method_type?: string }>('GET', `/checkout/status/${customerId}`),

  // Billing
  triggerBilling: () =>
    request<{ invoices_generated: number; errors: string[] }>('POST', '/billing/run'),

  // Credits
  getBalance: (customerId: string) =>
    request<CreditBalance>('GET', `/credits/balance/${customerId}`),
  grantCredits: (data: { customer_id: string; amount_cents: number; description: string }) =>
    request<CreditLedgerEntry>('POST', '/credits/grant', data),
  listLedger: (customerId: string) =>
    request<{ data: CreditLedgerEntry[] }>('GET', `/credits/ledger/${customerId}`),

  // Customer portal
  customerOverview: (customerId: string) =>
    request<CustomerOverview>('GET', `/customer-portal/${customerId}/overview`),

  // Usage
  usageSummary: (customerId: string, from?: string, to?: string) => {
    const params = new URLSearchParams()
    if (from) params.set('from', from)
    if (to) params.set('to', to)
    const qs = params.toString()
    return request<UsageSummary>('GET', `/usage-summary/${customerId}${qs ? '?' + qs : ''}`)
  },
  listUsageEvents: (params?: string) =>
    request<{ data: UsageEvent[] }>('GET', `/usage-events${params ? '?' + params : ''}`),

  // Customer updates
  updateCustomer: (id: string, data: { display_name?: string; email?: string }) =>
    request<Customer>('PATCH', `/customers/${id}`, data),

  // Subscription detail
  getSubscription: (id: string) =>
    request<Subscription>('GET', `/subscriptions/${id}`),
  activateSubscription: (id: string) =>
    request<Subscription>('POST', `/subscriptions/${id}/activate`),
  pauseSubscription: (id: string) =>
    request<Subscription>('POST', `/subscriptions/${id}/pause`),
  resumeSubscription: (id: string) =>
    request<Subscription>('POST', `/subscriptions/${id}/resume`),
  cancelSubscription: (id: string) =>
    request<Subscription>('POST', `/subscriptions/${id}/cancel`),
  changePlan: (id: string, data: { new_plan_id: string; immediate?: boolean }) =>
    request<{ subscription: Subscription; proration_factor?: number }>('POST', `/subscriptions/${id}/change-plan`, data),
  invoicePreview: (subscriptionId: string) =>
    request<InvoicePreview>('GET', `/billing/preview/${subscriptionId}`),

  // Billing profile
  getBillingProfile: (customerId: string) =>
    request<BillingProfile>('GET', `/customers/${customerId}/billing-profile`),
  upsertBillingProfile: (customerId: string, data: Partial<BillingProfile>) =>
    request<BillingProfile>('PUT', `/customers/${customerId}/billing-profile`, data),

  // Settings
  getSettings: () =>
    request<TenantSettings>('GET', '/settings'),
  updateSettings: (data: Partial<TenantSettings>) =>
    request<TenantSettings>('PUT', '/settings', data),

  // Dunning
  getDunningPolicy: () => request<DunningPolicy>('GET', '/dunning/policy'),
  upsertDunningPolicy: (data: Partial<DunningPolicy>) => request<DunningPolicy>('PUT', '/dunning/policy', data),
  listDunningRuns: (params?: string) => request<{ data: DunningRun[] }>('GET', `/dunning/runs${params ? '?' + params : ''}`),
  getDunningRun: (id: string) => request<{ run: DunningRun; events: DunningEvent[] }>('GET', `/dunning/runs/${id}`),
  resolveDunningRun: (id: string, resolution: string) => request<DunningRun>('POST', `/dunning/runs/${id}/resolve`, { resolution }),

  // Credit Notes
  listCreditNotes: (params?: string) => request<{ data: CreditNote[] }>('GET', `/credit-notes${params ? '?' + params : ''}`),
  createCreditNote: (data: { invoice_id: string; reason: string; refund_type?: string; lines: { description: string; quantity: number; unit_amount_cents: number }[] }) => request<CreditNote>('POST', '/credit-notes', data),
  issueCreditNote: (id: string) => request<CreditNote>('POST', `/credit-notes/${id}/issue`),
  voidCreditNote: (id: string) => request<CreditNote>('POST', `/credit-notes/${id}/void`),

  // Audit Log
  listAuditLog: (params?: string) => request<{ data: AuditEntry[] }>('GET', `/audit-log${params ? '?' + params : ''}`),

  // Webhooks
  listWebhookEndpoints: () => request<{ data: WebhookEndpoint[] }>('GET', '/webhook-endpoints/endpoints'),
  createWebhookEndpoint: (data: { url: string; description?: string; events?: string[] }) => request<{ endpoint: WebhookEndpoint; secret: string }>('POST', '/webhook-endpoints/endpoints', data),
  deleteWebhookEndpoint: (id: string) => request<{ status: string }>('DELETE', `/webhook-endpoints/endpoints/${id}`),
  listWebhookEvents: () => request<{ data: WebhookEvent[] }>('GET', '/webhook-endpoints/events'),
  replayWebhookEvent: (id: string) => request<{ status: string }>('POST', `/webhook-endpoints/events/${id}/replay`),

  // API Keys
  listApiKeys: () => request<{ data: ApiKeyInfo[] }>('GET', '/api-keys'),
  createApiKey: (data: { name: string; key_type: string }) => request<{ key: ApiKeyInfo; raw_key: string }>('POST', '/api-keys', data),
  revokeApiKey: (id: string) => request<ApiKeyInfo>('DELETE', `/api-keys/${id}`),
}

// Types
export interface Customer {
  id: string
  external_id: string
  display_name: string
  email: string
  status: string
  created_at: string
}

export interface Subscription {
  id: string
  code: string
  display_name: string
  customer_id: string
  plan_id: string
  status: string
  billing_time: string
  current_billing_period_start?: string
  current_billing_period_end?: string
  next_billing_at?: string
  created_at: string
}

export interface Invoice {
  id: string
  invoice_number: string
  customer_id: string
  subscription_id: string
  status: string
  payment_status: string
  currency: string
  subtotal_cents: number
  total_amount_cents: number
  amount_due_cents: number
  billing_period_start: string
  billing_period_end: string
  created_at: string
}

export interface LineItem {
  id: string
  line_type: string
  description: string
  quantity: number
  unit_amount_cents: number
  amount_cents: number
  total_amount_cents: number
  currency: string
  pricing_mode?: string
}

export interface CreditBalance {
  customer_id: string
  balance_cents: number
  total_granted: number
  total_used: number
}

export interface CreditLedgerEntry {
  id: string
  customer_id: string
  entry_type: string
  amount_cents: number
  balance_after: number
  description: string
  invoice_id: string
  created_at: string
}

export interface CustomerOverview {
  customer_id: string
  active_subscriptions: Subscription[]
  recent_invoices: Invoice[]
}

export interface Meter {
  id: string
  key: string
  name: string
  unit: string
  aggregation: string
  rating_rule_version_id: string
  created_at: string
}

export interface Plan {
  id: string
  code: string
  name: string
  currency: string
  billing_interval: string
  status: string
  base_amount_cents: number
  meter_ids: string[]
  created_at: string
}

export interface RatingRule {
  id: string
  rule_key: string
  name: string
  version: number
  mode: string
  currency: string
  flat_amount_cents: number
  graduated_tiers?: { up_to: number; unit_amount_cents: number }[]
  package_size: number
  package_amount_cents: number
  created_at: string
}

export interface UsageEvent {
  id: string
  customer_id: string
  meter_id: string
  subscription_id: string
  quantity: number
  idempotency_key: string
  timestamp: string
}

export interface UsageSummary {
  customer_id: string
  meters: Record<string, number>
  total_events: number
}

export interface BillingProfile {
  customer_id: string
  legal_name: string
  email: string
  phone: string
  address_line1: string
  address_line2: string
  city: string
  state: string
  postal_code: string
  country: string
  currency: string
  tax_identifier: string
  profile_status: string
}

export interface TenantSettings {
  tenant_id: string
  default_currency: string
  timezone: string
  invoice_prefix: string
  invoice_next_seq: number
  net_payment_terms: number
  company_name: string
  company_address: string
  company_email: string
  company_phone: string
  logo_url: string
}

export interface InvoicePreview {
  customer_id: string
  subscription_id: string
  plan_name: string
  currency: string
  billing_period_start: string
  billing_period_end: string
  lines: { line_type: string; description: string; meter_id?: string; quantity: number; unit_amount_cents: number; amount_cents: number; pricing_mode?: string }[]
  subtotal_cents: number
  generated_at: string
}

export interface DunningPolicy {
  id: string
  name: string
  enabled: boolean
  retry_schedule: string[]
  max_retry_attempts: number
  final_action: string
  grace_period_days: number
}

export interface DunningRun {
  id: string
  invoice_id: string
  customer_id: string
  policy_id: string
  state: string
  reason: string
  attempt_count: number
  last_attempt_at?: string
  next_action_at?: string
  paused: boolean
  resolved_at?: string
  resolution: string
  created_at: string
}

export interface DunningEvent {
  id: string
  run_id: string
  event_type: string
  state: string
  reason: string
  attempt_count: number
  created_at: string
}

export interface CreditNote {
  id: string
  invoice_id: string
  customer_id: string
  credit_note_number: string
  status: string
  reason: string
  subtotal_cents: number
  total_cents: number
  refund_amount_cents: number
  credit_amount_cents: number
  currency: string
  issued_at?: string
  voided_at?: string
  created_at: string
}

export interface AuditEntry {
  id: string
  actor_type: string
  actor_id: string
  action: string
  resource_type: string
  resource_id: string
  resource_label?: string
  metadata?: Record<string, unknown>
  created_at: string
}

export interface WebhookEndpoint {
  id: string
  url: string
  description: string
  events: string[]
  active: boolean
  created_at: string
}

export interface WebhookEvent {
  id: string
  event_type: string
  payload: Record<string, unknown>
  created_at: string
}

export interface ApiKeyInfo {
  id: string
  key_prefix: string
  key_type: string
  name: string
  created_at: string
  expires_at?: string
  revoked_at?: string
  last_used_at?: string
}

export async function downloadPDF(invoiceId: string, invoiceNumber: string) {
  const res = await fetch(`${API_BASE}/invoices/${invoiceId}/pdf`, {
    headers: { Authorization: `Bearer ${apiKey}` },
  })
  const blob = await res.blob()
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `${invoiceNumber}.pdf`
  a.click()
  URL.revokeObjectURL(url)
}

export function formatCents(cents: number): string {
  const sign = cents < 0 ? '-' : ''
  const abs = Math.abs(cents)
  return `${sign}$${Math.floor(abs / 100)}.${String(abs % 100).padStart(2, '0')}`
}

export function formatDate(iso: string): string {
  return new Date(iso).toLocaleDateString('en-US', {
    year: 'numeric', month: 'short', day: 'numeric',
  })
}

export function formatDateTime(iso: string): string {
  return new Date(iso).toLocaleString('en-US', {
    year: 'numeric', month: 'short', day: 'numeric',
    hour: 'numeric', minute: '2-digit',
  })
}

export function formatRelativeTime(iso: string): string {
  const seconds = Math.floor((Date.now() - new Date(iso).getTime()) / 1000)
  if (seconds < 60) return 'just now'
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m ago`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h ago`
  const days = Math.floor(hours / 24)
  return `${days}d ago`
}
