import { setLastRequestId } from './lastRequestId'

const API_BASE = '/v1'

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

// apiRequest is the shared fetch wrapper for all Velox API calls. Session
// cookies ride automatically via `credentials: 'include'` (httpOnly, set by
// POST /v1/auth/login). No Authorization header — the dashboard is session
// authed, not API-key authed.
export async function apiRequest<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
  }

  const res = await fetch(`${API_BASE}${path}`, {
    method,
    headers,
    credentials: 'include',
    body: body ? JSON.stringify(body) : undefined,
  })

  // Record every observed request_id so "Report an issue" carries the most
  // recent trace handle even when the user's last request succeeded.
  const headerRequestId = res.headers.get('Velox-Request-Id') || ''
  if (headerRequestId) setLastRequestId(headerRequestId)

  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: { message: res.statusText } }))
    // Stripe-style envelope: { error: { type, code, message, param, request_id } }
    // param carries the offending field name for 422/409 responses. Older
    // string-shape errors ({ error: "..." }) fall back to the raw message.
    const detail = typeof err.error === 'object' ? err.error : null
    const raw = typeof err.error === 'string' ? err.error : (detail?.message || `HTTP ${res.status}`)
    const requestId = detail?.request_id || headerRequestId || undefined
    if (requestId) setLastRequestId(requestId)
    throw new ApiError(humanizeError(raw), res.status, {
      field: detail?.param || undefined,
      code: detail?.code || undefined,
      // Prefer request_id from the Stripe-style envelope; fall back to the
      // Velox-Request-Id header so errors that bypass the JSON writer (502s,
      // plain-text proxy errors) still carry a traceable ID to the toast.
      requestId,
    })
  }

  // 204 No Content / 205 Reset Content have no body — parsing as JSON would
  // throw SyntaxError and trip the caller's onError. Callers that use
  // apiRequest<void> expect undefined here.
  if (res.status === 204 || res.status === 205) {
    return undefined as T
  }
  return res.json()
}

export const api = {
  // Customers
  listCustomers: (params?: string) =>
    apiRequest<{ data: Customer[]; total: number }>('GET', `/customers${params ? '?' + params : ''}`),
  getCustomer: (id: string) =>
    apiRequest<Customer>('GET', `/customers/${id}`),
  createCustomer: (data: { external_id: string; display_name: string; email?: string }) =>
    apiRequest<Customer>('POST', '/customers', data),

  // Subscriptions
  listSubscriptions: (params?: string) =>
    apiRequest<{ data: Subscription[]; total: number }>('GET', `/subscriptions${params ? '?' + params : ''}`),
  createSubscription: (data: { code: string; display_name: string; customer_id: string; items: { plan_id: string; quantity?: number }[]; start_now?: boolean; billing_time?: string; trial_days?: number; usage_cap_units?: number | null; overage_action?: string }) =>
    apiRequest<Subscription>('POST', '/subscriptions', data),

  // Pricing
  listMeters: () =>
    apiRequest<{ data: Meter[] }>('GET', '/meters'),
  getMeter: (id: string) =>
    apiRequest<Meter>('GET', `/meters/${id}`),
  createMeter: (data: { key: string; name: string; unit?: string; aggregation?: string; rating_rule_version_id?: string }) =>
    apiRequest<Meter>('POST', '/meters', data),
  listMeterPricingRules: (meterId: string) =>
    apiRequest<{ data: MeterPricingRule[] }>('GET', `/meters/${meterId}/pricing-rules`),
  createMeterPricingRule: (
    meterId: string,
    data: {
      rating_rule_version_id: string
      dimension_match: Record<string, string | number | boolean>
      aggregation_mode: MeterAggregationMode
      priority: number
    },
  ) =>
    apiRequest<MeterPricingRule>('POST', `/meters/${meterId}/pricing-rules`, data),
  deleteMeterPricingRule: (meterId: string, ruleId: string) =>
    apiRequest<{ status: string }>('DELETE', `/meters/${meterId}/pricing-rules/${ruleId}`),
  customerUsage: (customerId: string, params?: { from?: string; to?: string }) => {
    const qs = new URLSearchParams()
    if (params?.from) qs.set('from', params.from)
    if (params?.to) qs.set('to', params.to)
    const q = qs.toString()
    return apiRequest<CustomerUsage>('GET', `/customers/${customerId}/usage${q ? '?' + q : ''}`)
  },
  listPlans: () =>
    apiRequest<{ data: Plan[] }>('GET', '/plans'),
  getPlan: (id: string) =>
    apiRequest<Plan>('GET', `/plans/${id}`),
  createPlan: (data: { code: string; name: string; currency: string; billing_interval: string; base_amount_cents: number; meter_ids?: string[] }) =>
    apiRequest<Plan>('POST', '/plans', data),
  updatePlan: (id: string, data: Partial<{ name: string; status: string; base_amount_cents: number; meter_ids: string[] }>) =>
    apiRequest<Plan>('PATCH', `/plans/${id}`, data),
  listRatingRules: () =>
    apiRequest<{ data: RatingRule[] }>('GET', '/rating-rules'),
  getRatingRule: (id: string) =>
    apiRequest<RatingRule>('GET', `/rating-rules/${id}`),
  createRatingRule: (data: { rule_key: string; name: string; mode: string; currency: string; flat_amount_cents?: number; graduated_tiers?: { up_to: number; unit_amount_cents: number }[]; package_size?: number; package_amount_cents?: number }) =>
    apiRequest<RatingRule>('POST', '/rating-rules', data),

  // Invoices
  listInvoices: (params?: string) =>
    apiRequest<{ data: Invoice[]; total: number }>('GET', `/invoices${params ? '?' + params : ''}`),
  getInvoice: (id: string) =>
    apiRequest<{ invoice: Invoice; line_items: LineItem[] }>('GET', `/invoices/${id}`),
  // Creates a draft invoice. Cycle invoices originate from the billing engine
  // and pass subscription_id; one-off invoices issued from the customer-page
  // composer omit it (backend allows null after migration 0060). Default
  // billing window is "now" when omitted, default net term is 30 days.
  createInvoice: (data: {
    customer_id: string
    subscription_id?: string
    currency?: string
    billing_period_start?: string
    billing_period_end?: string
    net_payment_term_days?: number
    memo?: string
  }) => apiRequest<Invoice>('POST', '/invoices', data),
  finalizeInvoice: (id: string) =>
    apiRequest<Invoice>('POST', `/invoices/${id}/finalize`),
  voidInvoice: (id: string) =>
    apiRequest<Invoice>('POST', `/invoices/${id}/void`),
  rotateInvoicePublicToken: (id: string) =>
    apiRequest<Invoice>('POST', `/invoices/${id}/rotate-public-token`),
  applyInvoiceCoupon: (id: string, data: { code: string; idempotency_key?: string }) =>
    apiRequest<Invoice>('POST', `/invoices/${id}/apply-coupon`, data),
  collectPayment: (id: string) =>
    apiRequest<Invoice>('POST', `/invoices/${id}/collect`),
  sendInvoiceEmail: (invoiceId: string, email: string) =>
    apiRequest<{ status: string }>('POST', `/invoices/${invoiceId}/send`, { email }),
  getPaymentTimeline: (invoiceId: string) =>
    apiRequest<{ events: TimelineEvent[] }>('GET', `/invoices/${invoiceId}/payment-timeline`),
  getSubscriptionTimeline: (subscriptionId: string) =>
    apiRequest<{ events: TimelineEvent[] }>('GET', `/subscriptions/${subscriptionId}/timeline`),

  // Payment setup
  setupPayment: (data: { customer_id: string; customer_name: string; email: string; address_line1?: string; address_city?: string; address_state?: string; address_postal_code?: string; address_country?: string }) =>
    apiRequest<{ session_id: string; url: string; stripe_customer_id: string }>('POST', '/checkout/setup', data),
  getPaymentStatus: (customerId: string) =>
    apiRequest<PaymentSetup>('GET', `/checkout/status/${customerId}`),

  // Credits
  listBalances: () =>
    apiRequest<{ data: CreditBalance[] }>('GET', '/credits/balances'),
  getBalance: (customerId: string) =>
    apiRequest<CreditBalance>('GET', `/credits/balance/${customerId}`),
  grantCredits: (data: { customer_id: string; amount_cents: number; description: string; expires_at?: string }) =>
    apiRequest<CreditLedgerEntry>('POST', '/credits/grant', data),
  adjustCredits: (data: { customer_id: string; amount_cents: number; description: string }) =>
    apiRequest<CreditLedgerEntry>('POST', '/credits/adjust', data),
  listLedger: (customerId: string, params?: { entry_type?: string; limit?: number; offset?: number }) => {
    const qs = new URLSearchParams()
    if (params?.entry_type) qs.set('entry_type', params.entry_type)
    if (params?.limit) qs.set('limit', String(params.limit))
    if (params?.offset) qs.set('offset', String(params.offset))
    const q = qs.toString()
    return apiRequest<{ data: CreditLedgerEntry[] }>('GET', `/credits/ledger/${customerId}${q ? '?' + q : ''}`)
  },

  // Customer portal
  customerOverview: (customerId: string) =>
    apiRequest<CustomerOverview>('GET', `/customer-portal/${customerId}/overview`),
  updatePaymentMethod: (customerId: string, returnUrl?: string) =>
    apiRequest<{ url: string }>('POST', `/payment-portal/${customerId}/update-payment-method`, returnUrl ? { return_url: returnUrl } : {}),

  // Usage
  usageSummary: (customerId: string, from?: string, to?: string) => {
    const params = new URLSearchParams()
    if (from) params.set('from', from)
    if (to) params.set('to', to)
    const qs = params.toString()
    return apiRequest<UsageSummary>('GET', `/usage-summary/${customerId}${qs ? '?' + qs : ''}`)
  },
  listUsageEvents: (params?: string) =>
    apiRequest<{ data: UsageEvent[]; total: number }>('GET', `/usage-events${params ? '?' + params : ''}`),

  // Customer updates
  updateCustomer: (id: string, data: { display_name?: string; email?: string }) =>
    apiRequest<Customer>('PATCH', `/customers/${id}`, data),

  // Subscription detail
  getSubscription: (id: string) =>
    apiRequest<Subscription>('GET', `/subscriptions/${id}`),
  activateSubscription: (id: string) =>
    apiRequest<Subscription>('POST', `/subscriptions/${id}/activate`),
  pauseSubscription: (id: string) =>
    apiRequest<Subscription>('POST', `/subscriptions/${id}/pause`),
  resumeSubscription: (id: string) =>
    apiRequest<Subscription>('POST', `/subscriptions/${id}/resume`),
  cancelSubscription: (id: string) =>
    apiRequest<Subscription>('POST', `/subscriptions/${id}/cancel`),
  scheduleSubscriptionCancel: (id: string, body: { at_period_end: true } | { cancel_at: string }) =>
    apiRequest<Subscription>('POST', `/subscriptions/${id}/schedule-cancel`, body),
  clearScheduledSubscriptionCancel: (id: string) =>
    apiRequest<Subscription>('DELETE', `/subscriptions/${id}/scheduled-cancel`),
  pauseSubscriptionCollection: (id: string, body: { behavior: 'keep_as_draft'; resumes_at?: string }) =>
    apiRequest<Subscription>('PUT', `/subscriptions/${id}/pause-collection`, body),
  resumeSubscriptionCollection: (id: string) =>
    apiRequest<Subscription>('DELETE', `/subscriptions/${id}/pause-collection`),
  endSubscriptionTrial: (id: string) =>
    apiRequest<Subscription>('POST', `/subscriptions/${id}/end-trial`),
  extendSubscriptionTrial: (id: string, body: { trial_end: string }) =>
    apiRequest<Subscription>('POST', `/subscriptions/${id}/extend-trial`, body),
  // Item CRUD. PATCH body is either `{quantity}` or `{new_plan_id, immediate}`,
  // never both — mirrors the backend's UpdateItemInput guard.
  addSubscriptionItem: (id: string, data: { plan_id: string; quantity?: number }) =>
    apiRequest<SubscriptionItem>('POST', `/subscriptions/${id}/items`, data),
  updateSubscriptionItem: (id: string, itemID: string, data: { quantity?: number; new_plan_id?: string; immediate?: boolean }) =>
    apiRequest<ItemChangeResult>('PATCH', `/subscriptions/${id}/items/${itemID}`, data),
  removeSubscriptionItem: (id: string, itemID: string) =>
    apiRequest<{ status: string }>('DELETE', `/subscriptions/${id}/items/${itemID}`),
  cancelPendingItemChange: (id: string, itemID: string) =>
    apiRequest<SubscriptionItem>('DELETE', `/subscriptions/${id}/items/${itemID}/pending-change`),
  invoicePreview: (subscriptionId: string) =>
    apiRequest<InvoicePreview>('GET', `/billing/preview/${subscriptionId}`),
  // Stripe Tier 1 parity for Invoice.upcoming. Same shape as the debug
  // route above; composes against customer + subscription + period.
  // Pass {customer_id} and the server picks the primary active sub +
  // current cycle. Pass {customer_id, subscription_id} to preview a
  // specific sub. Pass {customer_id, period: {from, to}} to preview an
  // explicit window. See docs/design-create-preview.md.
  createInvoicePreview: (data: { customer_id: string; subscription_id?: string; period?: { from: string; to: string } }) =>
    apiRequest<InvoicePreview>('POST', '/invoices/create_preview', data),

  // Billing profile
  getBillingProfile: (customerId: string) =>
    apiRequest<BillingProfile>('GET', `/customers/${customerId}/billing-profile`),
  upsertBillingProfile: (customerId: string, data: Partial<BillingProfile>) =>
    apiRequest<BillingProfile>('PUT', `/customers/${customerId}/billing-profile`, data),

  // Settings
  getSettings: () =>
    apiRequest<TenantSettings>('GET', '/settings'),
  updateSettings: (data: Partial<TenantSettings>) =>
    apiRequest<TenantSettings>('PUT', '/settings', data),

  // Dunning
  getDunningPolicy: () => apiRequest<DunningPolicy>('GET', '/dunning/policy'),
  upsertDunningPolicy: (data: Partial<DunningPolicy>) => apiRequest<DunningPolicy>('PUT', '/dunning/policy', data),
  listDunningRuns: (params?: string) => apiRequest<{ data: DunningRun[]; total: number }>('GET', `/dunning/runs${params ? '?' + params : ''}`),
  getDunningRun: (id: string) => apiRequest<{ run: DunningRun; events: DunningEvent[] }>('GET', `/dunning/runs/${id}`),
  resolveDunningRun: (id: string, resolution: string) => apiRequest<DunningRun>('POST', `/dunning/runs/${id}/resolve`, { resolution }),
  getCustomerDunningOverride: (customerId: string) => apiRequest<CustomerDunningOverride>('GET', `/dunning/customers/${customerId}/override`),
  upsertCustomerDunningOverride: (customerId: string, data: Partial<CustomerDunningOverride>) => apiRequest<CustomerDunningOverride>('PUT', `/dunning/customers/${customerId}/override`, data),
  deleteCustomerDunningOverride: (customerId: string) => apiRequest<{ status: string }>('DELETE', `/dunning/customers/${customerId}/override`),

  // Credit Notes
  listCreditNotes: (params?: string) => apiRequest<{ data: CreditNote[] }>('GET', `/credit-notes${params ? '?' + params : ''}`),
  createCreditNote: (data: { invoice_id: string; reason: string; refund_type?: string; auto_issue?: boolean; lines: { description: string; quantity: number; unit_amount_cents: number }[] }) => apiRequest<CreditNote>('POST', '/credit-notes', data),
  issueCreditNote: (id: string) => apiRequest<CreditNote>('POST', `/credit-notes/${id}/issue`),
  voidCreditNote: (id: string) => apiRequest<CreditNote>('POST', `/credit-notes/${id}/void`),

  // Coupons
  listCoupons: (includeArchived?: boolean) =>
    apiRequest<{ data: Coupon[] }>('GET', includeArchived ? '/coupons?include_archived=true' : '/coupons'),
  getCoupon: (id: string) => apiRequest<Coupon>('GET', `/coupons/${id}`),
  createCoupon: (data: { code: string; name: string; type: string; amount_off?: number; percent_off_bp?: number; currency?: string; max_redemptions?: number | null; expires_at?: string; plan_ids?: string[]; customer_id?: string; stackable?: boolean; duration?: 'once' | 'repeating' | 'forever'; duration_periods?: number; restrictions?: { min_amount_cents?: number; first_time_customer_only?: boolean; max_redemptions_per_customer?: number } }) =>
    apiRequest<Coupon>('POST', '/coupons', data),
  updateCoupon: (id: string, data: { name?: string; max_redemptions?: number | null; expires_at?: string | null; restrictions?: { min_amount_cents?: number; first_time_customer_only?: boolean; max_redemptions_per_customer?: number } }) =>
    apiRequest<Coupon>('PATCH', `/coupons/${id}`, data),
  archiveCoupon: (id: string) => apiRequest<{ status: string }>('POST', `/coupons/${id}/archive`),
  unarchiveCoupon: (id: string) => apiRequest<{ status: string }>('POST', `/coupons/${id}/unarchive`),
  previewCoupon: (data: { code: string; customer_id: string; subtotal_cents: number; subscription_id?: string; plan_id?: string; currency?: string }) =>
    apiRequest<{ discount_cents: number; coupon: Coupon }>('POST', '/coupons/preview', data),
  redeemCoupon: (data: { code: string; customer_id: string; subtotal_cents: number; subscription_id?: string; invoice_id?: string; plan_id?: string; currency?: string; idempotency_key?: string }) =>
    apiRequest<CouponRedemption>('POST', '/coupons/redeem', data),
  listCouponRedemptions: (id: string, params?: string) =>
    apiRequest<{ data: CouponRedemption[]; has_more?: boolean; next_cursor?: string }>(
      'GET',
      `/coupons/${id}/redemptions${params ? '?' + params : ''}`,
    ),

  // Customer-scoped coupon assignment. Applies to every future invoice
  // until revoked or the coupon's duration exhausts. 404 from
  // getCustomerCoupon simply means "no active assignment".
  getCustomerCoupon: (customerId: string) =>
    apiRequest<CustomerCouponAssignment>('GET', `/customers/${customerId}/coupon`),
  assignCustomerCoupon: (customerId: string, data: { code: string; idempotency_key?: string }) =>
    apiRequest<CustomerCouponAssignment>('POST', `/customers/${customerId}/coupon`, data),
  revokeCustomerCoupon: (customerId: string) =>
    apiRequest<void>('DELETE', `/customers/${customerId}/coupon`),

  // Recipes (pricing templates) — see docs/design-recipes.md
  listRecipes: () => apiRequest<{ data: Recipe[] }>('GET', '/recipes'),
  getRecipe: (key: string) => apiRequest<RecipeDetail>('GET', `/recipes/${key}`),
  previewRecipe: (key: string, overrides: Record<string, string | number | boolean>) =>
    apiRequest<RecipePreview>('POST', `/recipes/${key}/preview`, { overrides }),
  instantiateRecipe: (data: {
    key: string
    overrides?: Record<string, string | number | boolean>
    seed_sample_data?: boolean
    force?: boolean
    idempotency_key?: string
  }) => apiRequest<RecipeInstance>('POST', '/recipes/instantiate', data),
  deleteRecipeInstance: (id: string) =>
    apiRequest<{ status: string }>('DELETE', `/recipes/instances/${id}`),

  // Audit Log
  listAuditLog: (params?: string) => apiRequest<{ data: AuditEntry[]; total: number }>('GET', `/audit-log${params ? '?' + params : ''}`),
  getAuditFilters: () => apiRequest<AuditFilterOptions>('GET', '/audit-log/filters'),

  // Webhooks
  listWebhookEndpoints: () => apiRequest<{ data: WebhookEndpoint[] }>('GET', '/webhook-endpoints/endpoints'),
  createWebhookEndpoint: (data: { url: string; description?: string; events?: string[] }) => apiRequest<{ endpoint: WebhookEndpoint; secret: string }>('POST', '/webhook-endpoints/endpoints', data),
  deleteWebhookEndpoint: (id: string) => apiRequest<{ status: string }>('DELETE', `/webhook-endpoints/endpoints/${id}`),
  rotateWebhookSecret: (id: string) => apiRequest<{ secret: string; secondary_valid_until?: string }>('POST', `/webhook-endpoints/endpoints/${id}/rotate-secret`),
  getWebhookEndpointStats: () => apiRequest<{ data: { endpoint_id: string; total_deliveries: number; succeeded: number; failed: number; success_rate: number }[] }>('GET', '/webhook-endpoints/endpoints/stats'),
  listWebhookEvents: () => apiRequest<{ data: WebhookEvent[] }>('GET', '/webhook-endpoints/events'),
  replayWebhookEvent: (id: string) => apiRequest<{ status: string }>('POST', `/webhook-endpoints/events/${id}/replay`),

  // Week 6 real-time event UI. Replay returns the freshly-cloned event so
  // the UI can highlight it in the live tail; deliveries-list returns the
  // full per-attempt timeline for the expand-row diff view. The SSE stream
  // is NOT wired here — EventSource is opened directly from the page
  // because cookies ride automatically on same-origin requests and the
  // wrapper would only complicate the close/reconnect lifecycle.
  replayWebhookEventV2: (id: string) =>
    apiRequest<{ event_id: string; replay_of: string; status: string }>(
      'POST',
      `/webhook_events/${id}/replay`,
    ),
  listWebhookDeliveries: (id: string) =>
    apiRequest<WebhookDeliveriesResponse>(
      'GET',
      `/webhook_events/${id}/deliveries`,
    ),

  // Analytics
  addInvoiceLineItem: (invoiceId: string, data: { description: string; line_type: string; quantity: number; unit_amount_cents: number }) =>
    apiRequest<LineItem>('POST', `/invoices/${invoiceId}/line-items`, data),

  getAnalyticsOverview: (period?: string) =>
    apiRequest<AnalyticsOverview>('GET', `/analytics/overview${period ? `?period=${period}` : ''}`),
  getRevenueChart: (period: string) =>
    apiRequest<{ period: string; data: RevenueDataPoint[] }>('GET', `/analytics/revenue-chart?period=${period}`),
  getMRRMovement: (period: string) =>
    apiRequest<MRRMovementResponse>('GET', `/analytics/mrr-movement?period=${period}`),
  getUsageAnalytics: (period: string) =>
    apiRequest<UsageAnalyticsResponse>('GET', `/analytics/usage?period=${period}`),

  // API Keys
  listApiKeys: () => apiRequest<{ data: ApiKeyInfo[] }>('GET', '/api-keys'),
  createApiKey: (data: { name: string; key_type: string; expires_at?: string }) => apiRequest<{ key: ApiKeyInfo; raw_key: string }>('POST', '/api-keys', data),
  revokeApiKey: (id: string) => apiRequest<ApiKeyInfo>('DELETE', `/api-keys/${id}`),

  // Stripe credentials (per-tenant). Secrets post once here; the server keeps
  // them encrypted and only returns last4 + verify status. Deleting a row
  // revokes that mode entirely.
  listStripeCredentials: () => apiRequest<{ data: StripeProviderCredentials[] }>('GET', '/settings/stripe'),
  connectStripe: (data: { livemode: boolean; secret_key: string; publishable_key: string; webhook_secret?: string }) =>
    apiRequest<StripeProviderCredentials>('POST', '/settings/stripe', data),
  deleteStripeCredentials: (mode: 'live' | 'test') =>
    apiRequest<void>('DELETE', `/settings/stripe/${mode}`),
  // Second half of two-step setup: tenant connects API keys first, registers
  // the webhook endpoint in Stripe, then returns with the whsec_ secret.
  // Updates only the webhook secret on an existing row — no re-verify of the
  // API key, no need to re-paste secret/publishable.
  setStripeWebhookSecret: (mode: 'live' | 'test', webhook_secret: string) =>
    apiRequest<StripeProviderCredentials>('PATCH', `/settings/stripe/${mode}/webhook`, { webhook_secret }),

  // Billing alerts: operator-configured thresholds that fire a webhook +
  // dashboard notification when a customer's cycle spend crosses a limit.
  // Wire snake_case, dimensions as always-object {}, decimal as string per ADR-005.
  listBillingAlerts: (params?: { customer_id?: string; status?: BillingAlertStatus; limit?: number; offset?: number }) => {
    const q = new URLSearchParams()
    if (params?.customer_id) q.set('customer_id', params.customer_id)
    if (params?.status) q.set('status', params.status)
    if (params?.limit != null) q.set('limit', String(params.limit))
    if (params?.offset != null) q.set('offset', String(params.offset))
    const qs = q.toString()
    return apiRequest<{ data: BillingAlert[]; total: number }>('GET', `/billing/alerts${qs ? '?' + qs : ''}`)
  },
  getBillingAlert: (id: string) =>
    apiRequest<BillingAlert>('GET', `/billing/alerts/${id}`),
  createBillingAlert: (data: CreateBillingAlertRequest) =>
    apiRequest<BillingAlert>('POST', '/billing/alerts', data),
  archiveBillingAlert: (id: string) =>
    apiRequest<BillingAlert>('POST', `/billing/alerts/${id}/archive`),

  // Plan migrations — operator-initiated bulk plan swaps. preview is a
  // read-only dry-run; commit applies the swap and emits one cohort audit
  // entry plus per-customer subscription.plan_changed entries.
  previewPlanMigration: (data: PlanMigrationPreviewRequest) =>
    apiRequest<PlanMigrationPreviewResponse>('POST', '/admin/plan_migrations/preview', data),
  commitPlanMigration: (data: PlanMigrationCommitRequest) =>
    apiRequest<PlanMigrationCommitResponse>('POST', '/admin/plan_migrations/commit', data),
  listPlanMigrations: (params?: { limit?: number; cursor?: string }) => {
    const qs = new URLSearchParams()
    if (params?.limit) qs.set('limit', String(params.limit))
    if (params?.cursor) qs.set('cursor', params.cursor)
    const q = qs.toString()
    return apiRequest<PlanMigrationListResponse>('GET', `/admin/plan_migrations${q ? '?' + q : ''}`)
  },
  // Detail lookup: the server doesn't expose GET /admin/plan_migrations/{id}
  // (the list endpoint is the only read surface), so we walk pages of the
  // list until we find the row. Migrations are infrequent operator events,
  // so the typical hit is page 1; the cap (5 pages × 100 rows) is a defence
  // against an attempt to fetch a deleted/unknown id rather than a real
  // performance constraint. Returns null when not found.
  getPlanMigration: async (id: string): Promise<PlanMigrationListItem | null> => {
    let cursor: string | undefined = undefined
    for (let i = 0; i < 5; i++) {
      const res: PlanMigrationListResponse = await apiRequest<PlanMigrationListResponse>(
        'GET',
        `/admin/plan_migrations?limit=100${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`,
      )
      const hit = (res.migrations ?? []).find((m) => m.migration_id === id)
      if (hit) return hit
      if (!res.next_cursor) return null
      cursor = res.next_cursor
    }
    return null
  },

  // Bulk operations (Week 7) — operator-initiated cohort actions across
  // many customers. Both apply_coupon and schedule_cancel commit
  // synchronously and return per-target success/failure counts. v1
  // surfaces apply_coupon + schedule_cancel only; the wire shape is
  // open-ended so future action types extend without breaking clients.
  bulkActions: {
    applyCoupon: (data: BulkActionApplyCouponRequest) =>
      apiRequest<BulkActionCommitResponse>('POST', '/admin/bulk_actions/apply_coupon', data),
    scheduleCancel: (data: BulkActionScheduleCancelRequest) =>
      apiRequest<BulkActionCommitResponse>('POST', '/admin/bulk_actions/schedule_cancel', data),
    list: (params?: { limit?: number; cursor?: string; status?: string; action_type?: string }) => {
      const qs = new URLSearchParams()
      if (params?.limit) qs.set('limit', String(params.limit))
      if (params?.cursor) qs.set('cursor', params.cursor)
      if (params?.status) qs.set('status', params.status)
      if (params?.action_type) qs.set('action_type', params.action_type)
      const q = qs.toString()
      return apiRequest<BulkActionListResponse>('GET', `/admin/bulk_actions${q ? '?' + q : ''}`)
    },
    get: (id: string) =>
      apiRequest<BulkActionDetail>('GET', `/admin/bulk_actions/${id}`),
  },
}

// Types
export interface Customer {
  id: string
  external_id: string
  display_name: string
  email: string
  status: string
  created_at: string
  // Deliverability signal populated by the email bounce-capture hook
  // (T0-20). Absent on cold tenants; defaults to 'unknown' server-side.
  email_status?: 'unknown' | 'ok' | 'bounced' | 'complained'
  email_last_bounced_at?: string
  email_bounce_reason?: string
}

export interface SubscriptionItem {
  id: string
  subscription_id: string
  plan_id: string
  quantity: number
  pending_plan_id?: string
  pending_plan_effective_at?: string
  // plan_changed_at stamps the last immediate plan swap on this item. Feeds
  // the per-item proration dedup key on the backend.
  plan_changed_at?: string
  created_at: string
  updated_at: string
}

export interface ProrationDetail {
  old_plan_id: string
  new_plan_id: string
  proration_factor: number
  amount_cents: number
  type: 'invoice' | 'credit'
  invoice_id?: string
}

export interface ItemChangeResult {
  item: SubscriptionItem
  effective_at: string
  proration?: ProrationDetail
}

export interface Subscription {
  id: string
  code: string
  display_name: string
  customer_id: string
  // items is the authoritative list of priced lines on the subscription.
  // Present on all list/get responses from FEAT-5 onward. Pre-FEAT-5 code
  // paths may omit it, so treat as optional and fall back gracefully.
  items?: SubscriptionItem[]
  status: string
  billing_time: string
  current_billing_period_start?: string
  current_billing_period_end?: string
  next_billing_at?: string
  trial_start_at?: string
  trial_end_at?: string
  usage_cap_units?: number | null
  overage_action?: string
  cancel_at_period_end?: boolean
  cancel_at?: string
  canceled_at?: string
  pause_collection?: {
    behavior: 'keep_as_draft'
    resumes_at?: string
  }
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
  tax_provider?: string
  tax_calculation_id?: string
  tax_transaction_id?: string
  tax_reverse_charge?: boolean
  tax_exempt_reason?: string
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
  // public_token is populated at finalize. Drafts and pre-addendum
  // finalized invoices (migrated-in but never rotated) carry empty.
  public_token?: string
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
  tax_amount_cents?: number
  tax_rate_bp?: number
  tax_jurisdiction?: string
  tax_code?: string
  currency: string
  pricing_mode?: string
}

export interface TimelineEvent {
  timestamp: string
  // 'audit' is emitted by the subscription timeline (T0-18); invoice
  // timeline keeps 'stripe' and 'dunning'. Kept as a string union so the
  // renderer can default-case unknown sources rather than hiding them.
  source: 'stripe' | 'dunning' | 'audit' | string
  event_type: string
  status: string
  description: string
  error?: string
  amount_cents?: number
  currency?: string
  payment_intent_id?: string
  attempt_count?: number
  // Audit-sourced events carry the actor who performed the action. The
  // invoice payment timeline never populates these (webhook + dunning are
  // system-driven) so they're strictly optional.
  actor_type?: string
  actor_name?: string
  actor_id?: string
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
  tax_code?: string
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
  // String-encoded NUMERIC(38, 12) — decimal precision for fractional GPU-hours
  // and partial tokens. Coerce via Number() / parseFloat() at display time.
  quantity: string
  // Free-form dimensions per docs/design-multi-dim-meters.md, subset-matched
  // by pricing rules.
  dimensions?: Record<string, string | number | boolean>
  idempotency_key: string
  timestamp: string
}

export type MeterAggregationMode =
  | 'sum'
  | 'count'
  | 'last_during_period'
  | 'last_ever'
  | 'max'

export interface MeterPricingRule {
  id: string
  meter_id: string
  rating_rule_version_id: string
  dimension_match: Record<string, string | number | boolean>
  aggregation_mode: MeterAggregationMode
  priority: number
  created_at: string
}

// CustomerUsage — response of GET /v1/customers/{id}/usage. Live shape from
// internal/usage/customer_usage.go (PR #26). `totals` is unconditionally an
// array (single-currency tenants get length 1) so the client iterates without
// branching. `warnings` is a plain string list — the backend opted for human
// messages rather than structured codes since dashboards just render them.
export interface CustomerUsage {
  customer_id: string
  period: {
    from: string
    to: string
    source: 'current_billing_cycle' | 'explicit'
  }
  subscriptions: CustomerUsageSubscription[]
  meters: CustomerUsageMeter[]
  totals: { currency: string; amount_cents: number }[]
  warnings: string[]
}

export interface CustomerUsageSubscription {
  id: string
  plan_id: string
  plan_name: string
  currency: string
  current_period_start: string
  current_period_end: string
}

export interface CustomerUsageMeter {
  meter_id: string
  meter_key: string
  meter_name: string
  unit: string
  currency: string
  total_quantity: string
  total_amount_cents: number
  rules: CustomerUsageRule[]
}

export interface CustomerUsageRule {
  rating_rule_version_id: string
  rule_key: string
  dimension_match?: Record<string, string | number | boolean>
  quantity: string
  amount_cents: number
}

export interface RecipeCreatesSummary {
  meters: number
  pricing_rules: number
  plans: number
  products: number
  rating_rules: number
  dunning_policies: number
  webhook_endpoints: number
}

export interface RecipeOverrideSchema {
  key: string
  type: 'string' | 'number' | 'boolean'
  default?: string | number | boolean
  description?: string
  enum?: (string | number)[]
  max_length?: number
  pattern?: string
}

export interface Recipe {
  key: string
  version: string
  name: string
  summary: string
  creates: RecipeCreatesSummary
  overridable: RecipeOverrideSchema[]
  instantiated?: { id: string; instantiated_at: string } | null
}

export interface RecipeDetail extends Recipe {
  description: string
}

export interface RecipePreview {
  key: string
  version: string
  objects: {
    products?: { code: string; name: string; description?: string }[]
    meters?: { key: string; name: string; unit: string; aggregation: string }[]
    rating_rules?: { rule_key: string; mode: string; currency: string; flat_amount_cents?: number }[]
    pricing_rules?: {
      meter_key: string
      rating_rule_key: string
      dimension_match: Record<string, string | number | boolean>
      aggregation_mode: MeterAggregationMode
      priority: number
    }[]
    plans?: { code: string; name: string; currency: string; billing_interval: string; base_amount_cents: number; meter_keys: string[] }[]
    dunning_policies?: { name: string; max_retries: number; intervals_hours: number[] }[]
    webhook_endpoints?: { url: string; events: string[]; _placeholder?: boolean }[]
  }
  warnings: string[]
}

export interface RecipeInstance {
  id: string
  key: string
  version: string
  tenant_id: string
  created_at: string
  created_objects: {
    product_ids: string[]
    meter_ids: string[]
    rating_rule_ids: string[]
    pricing_rule_ids: string[]
    plan_ids: string[]
    dunning_policy_id: string
    webhook_endpoint_id: string
  }
}

export interface UsageSummary {
  customer_id: string
  meters: Record<string, number>
  total_events: number
}

export type CustomerTaxStatus = 'standard' | 'exempt' | 'reverse_charge'

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
  tax_status: CustomerTaxStatus
  tax_exempt_reason: string
  tax_id: string
  tax_id_type: string
  profile_status: string
}

export interface TenantSettings {
  tenant_id: string
  default_currency: string
  timezone: string
  invoice_prefix: string
  invoice_next_seq: number
  net_payment_terms: number
  tax_provider: 'none' | 'manual' | 'stripe_tax'
  tax_rate_bp: number
  tax_name: string
  tax_inclusive: boolean
  default_product_tax_code: string
  company_name: string
  company_address_line1: string
  company_address_line2: string
  company_city: string
  company_state: string
  company_postal_code: string
  company_country: string
  company_email: string
  company_phone: string
  logo_url: string
  brand_color: string
  tax_id: string
  support_url: string
  invoice_footer: string
}

export interface StripeProviderCredentials {
  id: string
  tenant_id: string
  livemode: boolean
  stripe_account_id?: string
  stripe_account_name?: string
  secret_key_prefix?: string
  secret_key_last4: string
  publishable_key: string
  webhook_secret_last4?: string
  has_webhook_secret: boolean
  verified_at?: string | null
  last_verified_error?: string
  created_at: string
  updated_at: string
}

// InvoicePreview is the response shape for both /v1/billing/preview/{id}
// (in-app debug route) and /v1/invoices/create_preview (Stripe Tier 1
// surface). Per-(meter, rule) lines mean multi-dim meters render one row
// per rule with dimension_match echoed; totals[] is always-array (one
// entry per distinct currency, even when there's only one) so a single
// reader handles both shapes. quantity is a decimal STRING per ADR-005
// — fractional AI-usage primitives (GPU-hours, cached-token ratios)
// round-trip without precision loss.
export interface InvoicePreview {
  customer_id: string
  subscription_id: string
  plan_name: string
  billing_period_start: string
  billing_period_end: string
  lines: InvoicePreviewLine[]
  totals: InvoicePreviewTotal[]
  warnings: string[]
  generated_at: string
}

export interface InvoicePreviewLine {
  line_type: string
  description: string
  meter_id?: string
  rating_rule_version_id?: string
  rule_key?: string
  dimension_match?: Record<string, unknown>
  currency: string
  quantity: string
  unit_amount_cents: number
  amount_cents: number
  pricing_mode?: string
}

export interface InvoicePreviewTotal {
  currency: string
  amount_cents: number
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

export interface CouponRestrictions {
  min_amount_cents?: number
  first_time_customer_only?: boolean
  max_redemptions_per_customer?: number
}

export interface Coupon {
  id: string
  code: string
  name: string
  type: 'percentage' | 'fixed_amount'
  amount_off: number
  percent_off_bp: number
  currency: string
  max_redemptions: number | null
  times_redeemed: number
  expires_at?: string
  plan_ids?: string[]
  archived_at?: string | null
  customer_id?: string
  stackable?: boolean
  duration?: 'once' | 'repeating' | 'forever'
  duration_periods?: number | null
  restrictions?: CouponRestrictions
  metadata?: Record<string, unknown>
  created_at: string
}

export interface CouponRedemption {
  id: string
  coupon_id: string
  customer_id: string
  subscription_id: string
  invoice_id: string
  discount_cents: number
  idempotency_key?: string
  created_at: string
}

export interface CustomerCouponAssignment {
  id: string
  coupon_id: string
  customer_id: string
  periods_applied: number
  created_at: string
  coupon: Coupon
}

export interface AuditEntry {
  id: string
  actor_type: string
  actor_id: string
  actor_name?: string
  action: string
  resource_type: string
  resource_id: string
  resource_label?: string
  metadata?: Record<string, unknown>
  ip_address?: string
  request_id?: string
  created_at: string
}

export interface AuditFilterOptions {
  actions: string[]
  resource_types: string[]
}

export interface WebhookEndpoint {
  id: string
  url: string
  description: string
  events: string[]
  active: boolean
  created_at: string
  secret_last4?: string
  // Populated only during a rotation's 72h grace window. When set, the
  // dispatcher signs outbound events with BOTH the current and previous
  // secrets so the receiver's verifier can be staged without an outage.
  secondary_secret_last4?: string
  secondary_secret_expires_at?: string
}

export interface WebhookEvent {
  id: string
  event_type: string
  payload: Record<string, unknown>
  created_at: string
  // Week 6: replay-tree pivot. Original events leave this nil; replay
  // clones point at the root original (single-pivot rule — the chain is
  // collapsed at create time so it's always one hop). Optional in the TS
  // type because legacy events written before migration 0058 don't carry
  // it.
  replay_of_event_id?: string | null
}

// SSE-shaped projection of a webhook event the live tail consumes via
// EventSource on /v1/webhook_events/stream. Snake_case keys pinned by
// TestWireShape_WebhookEventsStream_SnakeCase.
export interface WebhookEventStreamFrame {
  event_id: string
  event_type: string
  customer_id: string
  status: string  // "pending" | "succeeded" | "failed"
  last_attempt_at: string | null
  created_at: string
  livemode: boolean
  replay_of_event_id: string | null
}

// Per-attempt timeline row served by GET /v1/webhook_events/{id}/deliveries.
// Each row carries everything the dashboard's expandable timeline shows
// plus the request_payload_sha256 the diff viewer uses to flag "payload
// identical between attempts" (Stripe-style replay's common case). Keys
// pinned by TestWireShape_WebhookEventDeliveries.
export interface WebhookDeliveryView {
  id: string
  event_id: string
  endpoint_id: string
  attempt_no: number
  status: string  // "pending" | "succeeded" | "failed"
  status_code: number
  response_body: string
  error: string
  request_payload_sha256: string
  attempted_at: string
  completed_at: string | null
  next_retry_at: string | null
  is_replay: boolean
  replay_event_id: string
}

export interface WebhookDeliveriesResponse {
  root_event_id: string
  deliveries: WebhookDeliveryView[]
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
  [k: string]: unknown
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
  [k: string]: unknown
}

export interface UsagePoint {
  date: string
  events: number
  quantity: number
  [k: string]: unknown
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

// Billing alerts. Mirrors internal/billingalert/handler.go wireAlert.
// dimensions is always an object ({} when absent); threshold always has
// both keys present (one as null) so the dashboard can read both
// without a conditional null guard.
export type BillingAlertStatus = 'active' | 'triggered' | 'triggered_for_period' | 'archived'
export type BillingAlertRecurrence = 'one_time' | 'per_period'

export interface BillingAlertFilter {
  meter_id?: string
  dimensions: Record<string, unknown>
}

export interface BillingAlertThreshold {
  // BIGINT cents, e.g. 100_00 for $100. Mutually exclusive with usage_gte.
  amount_gte: number | null
  // Decimal-as-string per ADR-005 (NUMERIC(38,12) preserves precision over
  // the wire). Mutually exclusive with amount_gte.
  usage_gte: string | null
}

export interface BillingAlert {
  id: string
  title: string
  customer_id: string
  filter: BillingAlertFilter
  threshold: BillingAlertThreshold
  recurrence: BillingAlertRecurrence
  status: BillingAlertStatus
  last_triggered_at: string | null
  last_period_start: string | null
  created_at: string
  updated_at: string
}

export interface CreateBillingAlertRequest {
  title: string
  customer_id: string
  // Optional: omit for "all meters / all dimensions" semantics.
  filter?: {
    meter_id?: string
    dimensions?: Record<string, unknown>
  }
  // Exactly one of amount_gte / usage_gte must be set. usage_gte is decimal-as-string.
  threshold: {
    amount_gte?: number
    usage_gte?: string
  }
  recurrence: BillingAlertRecurrence
}

// Plan migration tool types — wire shapes for /v1/admin/plan_migrations.

export interface PlanMigrationCustomerFilter {
  // "all"  → every active subscription on from_plan_id for the tenant
  // "ids"  → only subscriptions whose customer_id is in `ids`
  // "tag"  → reserved (server rejects with code "filter_type_unsupported")
  type: 'all' | 'ids' | 'tag'
  ids?: string[]
  value?: string
}

export interface PlanMigrationPreviewRequest {
  from_plan_id: string
  to_plan_id: string
  customer_filter: PlanMigrationCustomerFilter
}

export interface PlanMigrationCustomerPreview {
  customer_id: string
  current_plan_id: string
  target_plan_id: string
  // before / after are billing PreviewResult shapes — we only render totals
  // and a single delta in the dashboard table, so the embedded structure is
  // typed loosely here to avoid coupling the table to the engine schema.
  before: { totals: Array<{ currency: string; amount_cents: number }>; lines?: unknown[] }
  after: { totals: Array<{ currency: string; amount_cents: number }>; lines?: unknown[] }
  delta_amount_cents: number
  currency: string
}

export interface PlanMigrationTotal {
  currency: string
  before_amount_cents: number
  after_amount_cents: number
  delta_amount_cents: number
}

export interface PlanMigrationPreviewResponse {
  previews: PlanMigrationCustomerPreview[]
  totals: PlanMigrationTotal[]
  warnings: string[]
}

export interface PlanMigrationCommitRequest {
  from_plan_id: string
  to_plan_id: string
  customer_filter: PlanMigrationCustomerFilter
  idempotency_key: string
  effective: 'immediate' | 'next_period'
}

export interface PlanMigrationCommitResponse {
  migration_id: string
  applied_count: number
  audit_log_id: string
  idempotent_replay?: boolean
}

export interface PlanMigrationListItem {
  migration_id: string
  from_plan_id: string
  to_plan_id: string
  effective: 'immediate' | 'next_period'
  applied_at: string
  applied_by: string
  applied_by_type: string
  applied_count: number
  customer_filter: PlanMigrationCustomerFilter
  totals: PlanMigrationTotal[]
  idempotency_key: string
  audit_log_id?: string
}

export interface PlanMigrationListResponse {
  migrations: PlanMigrationListItem[]
  next_cursor: string
}

// Bulk operations (Week 7) — wire shapes for /v1/admin/bulk_actions.
// Same idempotency-key + customer-filter pattern as plan migrations,
// generalised to any action type (apply_coupon, schedule_cancel today;
// release_payment_hold etc. follow). Always-snake_case + always-array
// errors[] are non-negotiable; see internal/bulkaction/wire_shape_test.go.

export type BulkActionType = 'apply_coupon' | 'schedule_cancel'

export type BulkActionStatus =
  | 'pending'
  | 'running'
  | 'completed'
  | 'partial'
  | 'failed'

export interface BulkActionCustomerFilter {
  // "all"  → every customer for the tenant
  // "ids"  → only customers whose id is in `ids`
  // "tag"  → reserved (server rejects with code "filter_type_unsupported")
  type: 'all' | 'ids' | 'tag'
  ids?: string[]
  value?: string
}

export interface BulkActionApplyCouponRequest {
  idempotency_key: string
  customer_filter: BulkActionCustomerFilter
  coupon_code: string
}

export interface BulkActionScheduleCancelRequest {
  idempotency_key: string
  customer_filter: BulkActionCustomerFilter
  // Exactly one of at_period_end / cancel_at must be set; the server
  // rejects both-set or both-unset with a 422.
  at_period_end?: boolean
  cancel_at?: string
}

export interface BulkActionTargetError {
  customer_id: string
  error: string
}

export interface BulkActionCommitResponse {
  bulk_action_id: string
  status: BulkActionStatus
  target_count: number
  succeeded_count: number
  failed_count: number
  errors: BulkActionTargetError[]
  idempotent_replay?: boolean
}

export interface BulkActionListItem {
  bulk_action_id: string
  action_type: BulkActionType
  status: BulkActionStatus
  target_count: number
  succeeded_count: number
  failed_count: number
  customer_filter: BulkActionCustomerFilter
  params: Record<string, unknown>
  idempotency_key: string
  created_by: string
  created_at: string
  completed_at?: string
}

export interface BulkActionDetail extends BulkActionListItem {
  errors: BulkActionTargetError[]
}

export interface BulkActionListResponse {
  bulk_actions: BulkActionListItem[]
  next_cursor: string
}

export async function downloadPDF(invoiceId: string, invoiceNumber: string) {
  const res = await fetch(`${API_BASE}/invoices/${invoiceId}/pdf`, {
    credentials: 'include',
  })
  const blob = await res.blob()
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `${invoiceNumber}.pdf`
  a.click()
  URL.revokeObjectURL(url)
}

export async function downloadCreditNotePDF(creditNoteId: string, creditNoteNumber: string) {
  const res = await fetch(`${API_BASE}/credit-notes/${creditNoteId}/pdf`, {
    credentials: 'include',
  })
  if (!res.ok) {
    throw new Error(`Failed to download credit note PDF (${res.status})`)
  }
  const blob = await res.blob()
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `${creditNoteNumber || creditNoteId}.pdf`
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
