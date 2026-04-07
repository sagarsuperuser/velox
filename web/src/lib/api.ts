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
    throw new Error(err.error?.message || `HTTP ${res.status}`)
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

  // Pricing
  listMeters: () =>
    request<{ data: Meter[] }>('GET', '/meters'),
  createMeter: (data: { key: string; name: string; unit?: string; aggregation?: string; rating_rule_version_id?: string }) =>
    request<Meter>('POST', '/meters', data),
  listPlans: () =>
    request<{ data: Plan[] }>('GET', '/plans'),
  createPlan: (data: { code: string; name: string; currency: string; billing_interval: string; base_amount_cents: number; meter_ids?: string[] }) =>
    request<Plan>('POST', '/plans', data),
  listRatingRules: () =>
    request<{ data: RatingRule[] }>('GET', '/rating-rules'),
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

  // Billing
  triggerBilling: () =>
    request<{ invoices_generated: number; errors: string[] }>('POST', '/billing/run'),

  // Credits
  getBalance: (customerId: string) =>
    request<CreditBalance>('GET', `/credits/balance/${customerId}`),

  // Customer portal
  customerOverview: (customerId: string) =>
    request<CustomerOverview>('GET', `/customer-portal/${customerId}/overview`),

  // Usage
  usageSummary: (customerId: string) =>
    request<UsageSummary>('GET', `/usage-summary/${customerId}`),
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

export interface UsageSummary {
  customer_id: string
  meters: Record<string, number>
  total_events: number
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
