const API_BASE = '/v1'
const API_KEY_STORAGE = 'velox_api_key'

export function getApiKey(): string | null {
  return localStorage.getItem(API_KEY_STORAGE)
}

export function setApiKey(key: string) {
  localStorage.setItem(API_KEY_STORAGE, key)
}

export function clearApiKey() {
  localStorage.removeItem(API_KEY_STORAGE)
}

// ApiError carries the structured pieces of a Velox error envelope so callers
// can route the message to the right UI surface. `field` identifies the single
// offending input (backed by the envelope's `param` slot); `code` is the
// stable business-rule code (e.g. billing_setup_incomplete) for switching on
// specific failure modes.
export class ApiError extends Error {
  status: number
  field?: string
  code?: string
  requestId?: string
  constructor(message: string, status: number, opts?: { field?: string; code?: string; requestId?: string }) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.field = opts?.field
    this.code = opts?.code
    this.requestId = opts?.requestId
  }
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
  const apiKey = getApiKey()
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
    // Stripe-style envelope: { error: { type, code, message, param, request_id } }
    // param carries the offending field name for 422/409 responses. Older
    // string-shape errors ({ error: "..." }) fall back to the raw message.
    const detail = typeof err.error === 'object' ? err.error : null
    const raw = typeof err.error === 'string' ? err.error : (detail?.message || `HTTP ${res.status}`)
    throw new ApiError(humanizeError(raw), res.status, {
      field: detail?.param || undefined,
      code: detail?.code || undefined,
      requestId: detail?.request_id || undefined,
    })
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
    request<{ data: Subscription[]; total: number }>('GET', `/subscriptions${params ? '?' + params : ''}`),
  createSubscription: (data: { code: string; display_name: string; customer_id: string; plan_id: string; start_now?: boolean; billing_time?: string; trial_days?: number; usage_cap_units?: number | null; overage_action?: string }) =>
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
  getPaymentTimeline: (invoiceId: string) =>
    request<{ events: TimelineEvent[] }>('GET', `/invoices/${invoiceId}/payment-timeline`),

  // Payment setup
  setupPayment: (data: { customer_id: string; customer_name: string; email: string; address_line1?: string; address_city?: string; address_state?: string; address_postal_code?: string; address_country?: string }) =>
    request<{ session_id: string; url: string; stripe_customer_id: string }>('POST', '/checkout/setup', data),
  getPaymentStatus: (customerId: string) =>
    request<PaymentSetup>('GET', `/checkout/status/${customerId}`),

  // Billing
  triggerBilling: () =>
    request<{ invoices_generated: number; errors: string[] }>('POST', '/billing/run'),

  // Credits
  listBalances: () =>
    request<{ data: CreditBalance[] }>('GET', '/credits/balances'),
  getBalance: (customerId: string) =>
    request<CreditBalance>('GET', `/credits/balance/${customerId}`),
  grantCredits: (data: { customer_id: string; amount_cents: number; description: string; expires_at?: string }) =>
    request<CreditLedgerEntry>('POST', '/credits/grant', data),
  adjustCredits: (data: { customer_id: string; amount_cents: number; description: string }) =>
    request<CreditLedgerEntry>('POST', '/credits/adjust', data),
  listLedger: (customerId: string, params?: { entry_type?: string; limit?: number; offset?: number }) => {
    const qs = new URLSearchParams()
    if (params?.entry_type) qs.set('entry_type', params.entry_type)
    if (params?.limit) qs.set('limit', String(params.limit))
    if (params?.offset) qs.set('offset', String(params.offset))
    const q = qs.toString()
    return request<{ data: CreditLedgerEntry[] }>('GET', `/credits/ledger/${customerId}${q ? '?' + q : ''}`)
  },

  // Customer portal
  customerOverview: (customerId: string) =>
    request<CustomerOverview>('GET', `/customer-portal/${customerId}/overview`),
  updatePaymentMethod: (customerId: string, returnUrl?: string) =>
    request<{ url: string }>('POST', `/payment-portal/${customerId}/update-payment-method`, returnUrl ? { return_url: returnUrl } : {}),

  // Usage
  usageSummary: (customerId: string, from?: string, to?: string) => {
    const params = new URLSearchParams()
    if (from) params.set('from', from)
    if (to) params.set('to', to)
    const qs = params.toString()
    return request<UsageSummary>('GET', `/usage-summary/${customerId}${qs ? '?' + qs : ''}`)
  },
  listUsageEvents: (params?: string) =>
    request<{ data: UsageEvent[]; total: number }>('GET', `/usage-events${params ? '?' + params : ''}`),

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
    request<{ subscription: Subscription; proration_factor?: number; proration?: { type: string; amount_cents: number; invoice_id?: string } }>('POST', `/subscriptions/${id}/change-plan`, data),
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
  listDunningRuns: (params?: string) => request<{ data: DunningRun[]; total: number }>('GET', `/dunning/runs${params ? '?' + params : ''}`),
  getDunningRun: (id: string) => request<{ run: DunningRun; events: DunningEvent[] }>('GET', `/dunning/runs/${id}`),
  resolveDunningRun: (id: string, resolution: string) => request<DunningRun>('POST', `/dunning/runs/${id}/resolve`, { resolution }),
  getCustomerDunningOverride: (customerId: string) => request<CustomerDunningOverride>('GET', `/dunning/customers/${customerId}/override`),
  upsertCustomerDunningOverride: (customerId: string, data: Partial<CustomerDunningOverride>) => request<CustomerDunningOverride>('PUT', `/dunning/customers/${customerId}/override`, data),
  deleteCustomerDunningOverride: (customerId: string) => request<{ status: string }>('DELETE', `/dunning/customers/${customerId}/override`),

  // Credit Notes
  listCreditNotes: (params?: string) => request<{ data: CreditNote[] }>('GET', `/credit-notes${params ? '?' + params : ''}`),
  createCreditNote: (data: { invoice_id: string; reason: string; refund_type?: string; auto_issue?: boolean; lines: { description: string; quantity: number; unit_amount_cents: number }[] }) => request<CreditNote>('POST', '/credit-notes', data),
  issueCreditNote: (id: string) => request<CreditNote>('POST', `/credit-notes/${id}/issue`),
  voidCreditNote: (id: string) => request<CreditNote>('POST', `/credit-notes/${id}/void`),

  // Coupons
  listCoupons: () => request<{ data: Coupon[] }>('GET', '/coupons'),
  getCoupon: (id: string) => request<Coupon>('GET', `/coupons/${id}`),
  createCoupon: (data: { code: string; name: string; type: string; amount_off?: number; percent_off?: number; currency?: string; max_redemptions?: number | null; expires_at?: string; plan_ids?: string[] }) =>
    request<Coupon>('POST', '/coupons', data),
  deactivateCoupon: (id: string) => request<{ status: string }>('POST', `/coupons/${id}/deactivate`),
  redeemCoupon: (data: { code: string; customer_id: string; subtotal_cents: number; subscription_id?: string; invoice_id?: string; plan_id?: string }) =>
    request<CouponRedemption>('POST', '/coupons/redeem', data),
  listCouponRedemptions: (id: string) => request<{ data: CouponRedemption[] }>('GET', `/coupons/${id}/redemptions`),

  // Audit Log
  listAuditLog: (params?: string) => request<{ data: AuditEntry[]; total: number }>('GET', `/audit-log${params ? '?' + params : ''}`),

  // Webhooks
  listWebhookEndpoints: () => request<{ data: WebhookEndpoint[] }>('GET', '/webhook-endpoints/endpoints'),
  createWebhookEndpoint: (data: { url: string; description?: string; events?: string[] }) => request<{ endpoint: WebhookEndpoint; secret: string }>('POST', '/webhook-endpoints/endpoints', data),
  deleteWebhookEndpoint: (id: string) => request<{ status: string }>('DELETE', `/webhook-endpoints/endpoints/${id}`),
  rotateWebhookSecret: (id: string) => request<{ secret: string }>('POST', `/webhook-endpoints/endpoints/${id}/rotate-secret`),
  getWebhookEndpointStats: () => request<{ data: { endpoint_id: string; total_deliveries: number; succeeded: number; failed: number; success_rate: number }[] }>('GET', '/webhook-endpoints/endpoints/stats'),
  listWebhookEvents: () => request<{ data: WebhookEvent[] }>('GET', '/webhook-endpoints/events'),
  replayWebhookEvent: (id: string) => request<{ status: string }>('POST', `/webhook-endpoints/events/${id}/replay`),

  // Analytics
  addInvoiceLineItem: (invoiceId: string, data: { description: string; line_type: string; quantity: number; unit_amount_cents: number }) =>
    request<LineItem>('POST', `/invoices/${invoiceId}/line-items`, data),

  getAnalyticsOverview: (period?: string) =>
    request<AnalyticsOverview>('GET', `/analytics/overview${period ? `?period=${period}` : ''}`),
  getRevenueChart: (period: string) =>
    request<{ period: string; data: RevenueDataPoint[] }>('GET', `/analytics/revenue-chart?period=${period}`),
  getMRRMovement: (period: string) =>
    request<MRRMovementResponse>('GET', `/analytics/mrr-movement?period=${period}`),
  getUsageAnalytics: (period: string) =>
    request<UsageAnalyticsResponse>('GET', `/analytics/usage?period=${period}`),

  // API Keys
  listApiKeys: () => request<{ data: ApiKeyInfo[] }>('GET', '/api-keys'),
  createApiKey: (data: { name: string; key_type: string; expires_at?: string }) => request<{ key: ApiKeyInfo; raw_key: string }>('POST', '/api-keys', data),
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
  usage_cap_units?: number | null
  overage_action?: string
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
  discount_cents: number
  tax_amount_cents: number
  tax_rate_bp: number
  tax_name: string
  tax_country?: string
  tax_id?: string
  total_amount_cents: number
  amount_due_cents: number
  amount_paid_cents: number
  credits_applied_cents: number
  billing_period_start: string
  billing_period_end: string
  stripe_payment_intent_id?: string
  last_payment_error?: string
  issued_at?: string
  due_at?: string
  voided_at?: string
  paid_at?: string
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

export interface TimelineEvent {
  timestamp: string
  source: 'stripe' | 'dunning'
  event_type: string
  status: string
  description: string
  error?: string
  amount_cents?: number
  currency?: string
  payment_intent_id?: string
  attempt_count?: number
}

export interface CreditBalance {
  customer_id: string
  balance_cents: number
  total_granted: number
  total_used: number
  total_expired: number
}

export interface PaymentSetup {
  customer_id: string
  setup_status: string
  stripe_customer_id?: string
  payment_method_type?: string
  card_brand?: string
  card_last4?: string
  card_exp_month?: number
  card_exp_year?: number
}

export interface CreditLedgerEntry {
  id: string
  customer_id: string
  entry_type: string
  amount_cents: number
  balance_after: number
  description: string
  invoice_id: string
  expires_at?: string
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
  tax_exempt: boolean
  tax_id: string
  tax_id_type: string
  tax_override_rate_bp?: number | null
  profile_status: string
}

export interface TenantSettings {
  tenant_id: string
  default_currency: string
  timezone: string
  invoice_prefix: string
  invoice_next_seq: number
  net_payment_terms: number
  tax_rate_bp: number
  tax_name: string
  tax_inclusive: boolean
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

export interface CustomerDunningOverride {
  customer_id: string
  max_retry_attempts?: number | null
  grace_period_days?: number | null
  final_action?: string
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
  refund_status: string
  currency: string
  issued_at?: string
  voided_at?: string
  created_at: string
}

export interface Coupon {
  id: string
  code: string
  name: string
  type: 'percentage' | 'fixed_amount'
  amount_off: number
  percent_off: number
  currency: string
  max_redemptions: number | null
  times_redeemed: number
  expires_at?: string
  plan_ids?: string[]
  active: boolean
  created_at: string
}

export interface CouponRedemption {
  id: string
  coupon_id: string
  customer_id: string
  subscription_id: string
  invoice_id: string
  discount_cents: number
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

// Matches OverviewResponse in internal/analytics/overview.go. All money is
// in cents; rates are in [0, 1].
export interface AnalyticsOverview {
  period: string
  mrr: number
  mrr_prev: number
  arr: number
  arr_prev: number
  revenue: number
  revenue_prev: number
  outstanding_ar: number
  avg_invoice_value: number
  credit_balance_total: number
  active_customers: number
  new_customers: number
  active_subscriptions: number
  trialing_subscriptions: number
  paid_invoices: number
  failed_payments: number
  open_invoices: number
  dunning_active: number
  usage_events: number
  logo_churn_rate: number
  revenue_churn_rate: number
  nrr: number
  dunning_recovery_rate: number
  mrr_movement: MRRMovementTotals
}

export interface MRRMovementTotals {
  new: number
  expansion: number
  contraction: number
  churned: number
  net: number
}

export interface MRRMovementPoint {
  date: string
  new: number
  expansion: number
  contraction: number
  churned: number
  net: number
}

export interface MRRMovementResponse {
  period: string
  data: MRRMovementPoint[]
  totals: MRRMovementTotals
}

export interface RevenueDataPoint {
  date: string
  revenue_cents: number
  invoice_count: number
}

export interface UsagePoint {
  date: string
  events: number
  quantity: number
}

export interface TopMeterUsage {
  meter_id: string
  meter_name: string
  key: string
  events: number
  quantity: number
}

export interface UsageAnalyticsResponse {
  period: string
  data: UsagePoint[]
  top_meters: TopMeterUsage[]
  totals: { events: number; quantity: number }
}

export async function downloadPDF(invoiceId: string, invoiceNumber: string) {
  const apiKey = getApiKey()
  const headers: Record<string, string> = {}
  if (apiKey) {
    headers['Authorization'] = `Bearer ${apiKey}`
  }
  const res = await fetch(`${API_BASE}/invoices/${invoiceId}/pdf`, {
    headers,
  })
  const blob = await res.blob()
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `${invoiceNumber}.pdf`
  a.click()
  URL.revokeObjectURL(url)
}

const CURRENCY_SYMBOLS: Record<string, string> = {
  USD: '$', EUR: '€', GBP: '£', JPY: '¥', CNY: '¥',
  INR: '₹', BRL: 'R$', CAD: 'CA$', AUD: 'A$', CHF: 'CHF ',
  SGD: 'S$', HKD: 'HK$', NZD: 'NZ$', SEK: 'kr ', NOK: 'kr ',
  DKK: 'kr ', ZAR: 'R', KRW: '₩', MXN: 'MX$', PLN: 'zł',
  CZK: 'Kč ', HUF: 'Ft ', ILS: '₪', AED: 'AED ', SAR: 'SAR ',
  THB: '฿', MYR: 'RM ', IDR: 'Rp ', PHP: '₱', VND: '₫',
  CLP: 'CL$', COP: 'CO$', PEN: 'S/', ARS: 'AR$', TWD: 'NT$',
}

let _activeCurrency = 'USD'

export function setActiveCurrency(code: string) {
  _activeCurrency = code.toUpperCase()
}

export function getActiveCurrency(): string {
  return _activeCurrency
}

export function getCurrencySymbol(code?: string): string {
  return CURRENCY_SYMBOLS[(code || _activeCurrency).toUpperCase()] || (code || _activeCurrency) + ' '
}

export function formatCents(cents: number, currency?: string): string {
  const sign = cents < 0 ? '-' : ''
  const abs = Math.abs(cents)
  const symbol = getCurrencySymbol(currency)
  return `${sign}${symbol}${Math.floor(abs / 100)}.${String(abs % 100).padStart(2, '0')}`
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
