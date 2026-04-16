import { useEffect, useState } from 'react'
import { useParams, Link } from 'react-router-dom'
import { useForm, Controller } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { toast } from 'sonner'
import { api, formatCents, formatDate, formatDateTime, type Customer, type CustomerOverview, type BillingProfile, type UsageSummary, type Meter, type Plan, type Subscription, type PaymentSetup, type CustomerDunningOverride } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'
import { Modal } from '@/components/Modal'
import { FormField, FormSelect } from '@/components/FormField'
import { SearchSelect } from '@/components/SearchSelect'
import { Breadcrumbs } from '@/components/Breadcrumbs'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { CreditCard, Pencil } from 'lucide-react'
import { CopyButton } from '@/components/CopyButton'

const editCustomerSchema = z.object({
  display_name: z.string().min(1, 'Display name is required'),
  email: z.string().email('Invalid email address').or(z.literal('')),
})
type EditCustomerData = z.infer<typeof editCustomerSchema>

const billingProfileSchema = z.object({
  legal_name: z.string(),
  email: z.string().email('Invalid email address').or(z.literal('')),
  phone: z.string().regex(/^[\+\d\s\-\(\)]{7,20}$/, 'Invalid phone number').or(z.literal('')),
  address_line1: z.string(), address_line2: z.string(),
  city: z.string(), state: z.string(), postal_code: z.string(),
  country: z.string(), currency: z.string(),
  tax_identifier: z.string(), tax_exempt: z.boolean(),
  tax_id: z.string(), tax_id_type: z.string(),
  tax_country: z.string(), tax_state: z.string(),
  tax_override_rate: z.string(), tax_override_name: z.string(),
})
type BillingProfileData = z.infer<typeof billingProfileSchema>

const createSubFromCustomerSchema = z.object({
  display_name: z.string().min(1, 'Display name is required'),
  code: z.string().min(1, 'Code is required').regex(/^[a-zA-Z0-9_\-]+$/, 'Only letters, numbers, hyphens, and underscores'),
  plan_id: z.string().min(1, 'Plan is required'),
  start_now: z.boolean(),
})
type CreateSubFromCustomerData = z.infer<typeof createSubFromCustomerSchema>

export function CustomerDetailPage() {
  const { id } = useParams<{ id: string }>()
  const [customer, setCustomer] = useState<Customer | null>(null)
  const [overview, setOverview] = useState<CustomerOverview | null>(null)
  const [balance, setBalance] = useState(0)
  const [billingProfile, setBillingProfile] = useState<BillingProfile | null>(null)
  const [usageSummary, setUsageSummary] = useState<UsageSummary | null>(null)
  const [meterMap, setMeterMap] = useState<Record<string, { name: string; unit: string }>>({})
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [plans, setPlans] = useState<Plan[]>([])
  const [allSubs, setAllSubs] = useState<Subscription[]>([])
  const [showEditCustomer, setShowEditCustomer] = useState(false)
  const [showEditBilling, setShowEditBilling] = useState(false)
  const [showCreateSub, setShowCreateSub] = useState(false)
  const [paymentSetup, setPaymentSetup] = useState<PaymentSetup>({ customer_id: '', setup_status: 'missing' })
  const [settingUpPayment, setSettingUpPayment] = useState(false)
  const [dunningOverride, setDunningOverride] = useState<CustomerDunningOverride | null>(null)
  const [showDunningOverride, setShowDunningOverride] = useState(false)
  const loadData = () => {
    if (!id) return
    setLoading(true)
    setError(null)
    Promise.all([
      api.getCustomer(id),
      api.customerOverview(id),
      api.getBalance(id).catch(() => ({ balance_cents: 0 })),
      api.getBillingProfile(id).catch(() => null),
      api.listMeters().catch(() => ({ data: [] as Meter[] })),
      api.listPlans().catch(() => ({ data: [] as Plan[] })),
      api.listSubscriptions().catch(() => ({ data: [] as Subscription[] })),
    ]).then(([c, o, b, bp, metersRes, plansRes, subsRes]) => {
      setCustomer(c)
      setOverview(o)
      setBalance(b.balance_cents)
      setBillingProfile(bp)
      const mm: Record<string, { name: string; unit: string }> = {}
      metersRes.data.forEach(m => { mm[m.id] = { name: m.name, unit: m.unit }; mm[m.key] = { name: m.name, unit: m.unit } })
      setMeterMap(mm)
      setPlans(plansRes.data.filter(p => p.status === 'active'))
      const customerSubs = subsRes.data.filter(s => s.customer_id === id)
      setAllSubs(customerSubs)

      // Fetch usage summary scoped to active subscription's billing period
      const activeSub = customerSubs.find(s => s.status === 'active' && s.current_billing_period_start && s.current_billing_period_end)
      if (activeSub) {
        api.usageSummary(id, activeSub.current_billing_period_start!, activeSub.current_billing_period_end!)
          .then(us => setUsageSummary(us)).catch(() => setUsageSummary(null))
      } else {
        api.usageSummary(id).then(us => setUsageSummary(us)).catch(() => setUsageSummary(null))
      }

      // Fetch payment setup (includes card details)
      api.getPaymentStatus(id).then(ps => setPaymentSetup(ps)).catch(() => {})
      // Fetch dunning override (may 404 if none set)
      api.getCustomerDunningOverride(id).then(o => setDunningOverride(o)).catch(() => setDunningOverride(null))
      setLoading(false)
    }).catch(err => { setError(err instanceof Error ? err.message : 'Failed to load customer'); setLoading(false) })
  }

  const handleSetupPayment = async () => {
    if (!id || !customer) return
    setSettingUpPayment(true)
    try {
      // If payment method is already set up, use the update flow
      if (paymentSetup.setup_status === 'ready') {
        const res = await api.updatePaymentMethod(id)
        window.open(res.url, '_blank')
        toast.success('Stripe payment update page opened in new tab')
      } else {
        const res = await api.setupPayment({
          customer_id: id,
          customer_name: customer.display_name,
          email: customer.email || billingProfile?.email || '',
          address_line1: billingProfile?.address_line1 || '',
          address_city: billingProfile?.city || '',
          address_state: billingProfile?.state || '',
          address_postal_code: billingProfile?.postal_code || '',
          address_country: billingProfile?.country || 'US',
        })
        window.open(res.url, '_blank')
        setPaymentSetup(prev => ({ ...prev, setup_status: 'pending' }))
        toast.success('Stripe checkout opened in new tab')
      }
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to set up payment')
    } finally {
      setSettingUpPayment(false)
    }
  }

  useEffect(() => { loadData() }, [id])

  if (loading) {
    return (
      <Layout>
        <Breadcrumbs items={[{ label: 'Customers', to: '/customers' }, { label: 'Loading...' }]} />
        <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card">
          <LoadingSkeleton rows={8} columns={3} />
        </div>
      </Layout>
    )
  }

  if (error) return <Layout><ErrorState message={error} onRetry={loadData} /></Layout>

  if (!customer) return <Layout><p>Customer not found</p></Layout>

  return (
    <Layout>
      <Breadcrumbs items={[{ label: 'Customers', to: '/customers' }, { label: customer.display_name }]} />

      {/* Header */}
      <div className="sticky top-0 z-10 bg-white dark:bg-gray-950 pb-4 -mx-4 px-4 md:-mx-8 md:px-8 pt-2 border-b border-gray-100 dark:border-gray-800">
        <div className="flex items-start justify-between">
          <div>
            <h1 className="text-2xl font-semibold text-gray-900 dark:text-gray-100">{customer.display_name}</h1>
            <div className="flex items-center gap-1.5 mt-1">
              <span className="text-xs text-gray-500 dark:text-gray-500 font-mono">{customer.id}</span>
              <CopyButton text={customer.id} />
            </div>
          </div>
          <div className="flex items-center gap-3">
            <button
              onClick={() => setShowEditCustomer(true)}
              className="inline-flex items-center gap-1.5 px-4 py-2 border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-300 rounded-lg text-sm font-medium hover:bg-gray-50 dark:hover:bg-gray-800 hover:border-gray-400 transition-colors"
            >
              <Pencil size={14} />
              Edit
            </button>
            <Badge status={customer.status} />
          </div>
        </div>
      </div>

      {/* Key Metrics Row */}
      <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card mt-6">
        <div className="flex divide-x divide-gray-100 dark:divide-gray-800">
          <div className="flex-1 px-6 py-4">
            <p className="text-sm text-gray-600 dark:text-gray-400">Email</p>
            <p className="text-sm font-medium text-gray-900 dark:text-gray-100 mt-1">{customer.email || '\u2014'}</p>
          </div>
          <Link to={`/credits?customer=${id}`} className="flex-1 px-6 py-4 hover:bg-gray-50 transition-colors">
            <p className="text-sm text-gray-600 dark:text-gray-400">Credit Balance</p>
            <p className={`text-sm font-medium mt-1 ${balance > 0 ? 'text-emerald-600' : 'text-gray-900'}`}>{formatCents(balance)}</p>
          </Link>
          <div className="flex-1 px-6 py-4">
            <p className="text-sm text-gray-600 dark:text-gray-400">Subscriptions</p>
            <p className="text-sm font-medium text-gray-900 dark:text-gray-100 mt-1">{allSubs.length}</p>
          </div>
          <div className="flex-1 px-6 py-4">
            <p className="text-sm text-gray-600 dark:text-gray-400">Created</p>
            <p className="text-sm font-medium text-gray-900 dark:text-gray-100 mt-1">{formatDateTime(customer.created_at)}</p>
          </div>
        </div>
      </div>

      {/* Details */}
      <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card mt-6">
        <div className="px-6 py-4 border-b border-gray-100 dark:border-gray-800">
          <h2 className="text-sm font-semibold text-gray-900 dark:text-gray-100">Details</h2>
        </div>
        <div className="divide-y divide-gray-100 dark:divide-gray-800">
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-sm text-gray-600 dark:text-gray-400">External ID</span>
            <span className="text-sm text-gray-900 dark:text-gray-100 font-mono">{customer.external_id}</span>
          </div>
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-sm text-gray-600 dark:text-gray-400">Email</span>
            <span className="text-sm text-gray-900 dark:text-gray-100">{customer.email || '\u2014'}</span>
          </div>
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-sm text-gray-600 dark:text-gray-400">Status</span>
            <Badge status={customer.status} />
          </div>
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-sm text-gray-600 dark:text-gray-400">Created</span>
            <span className="text-sm text-gray-900 dark:text-gray-100">{formatDateTime(customer.created_at)}</span>
          </div>
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-sm text-gray-600 dark:text-gray-400">ID</span>
            <div className="flex items-center gap-1.5">
              <span className="text-sm text-gray-900 dark:text-gray-100 font-mono">{customer.id}</span>
              <CopyButton text={customer.id} />
            </div>
          </div>
        </div>
      </div>

      {/* Billing Profile */}
      <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card mt-6">
        {billingProfile ? (
          <>
            <div className="px-6 py-4 border-b border-gray-100 flex justify-between items-center">
              <h2 className="text-sm font-semibold text-gray-900 dark:text-gray-100">Billing Profile</h2>
              <button
                onClick={() => setShowEditBilling(true)}
                className="inline-flex items-center gap-1.5 px-3 py-1.5 border border-gray-300 text-gray-700 rounded-lg text-xs font-medium hover:bg-gray-50 hover:border-gray-400 transition-colors"
              >
                <Pencil size={12} />
                Edit
              </button>
            </div>
            <div className="px-6 py-5">
              <div className="grid grid-cols-1 lg:grid-cols-3 gap-x-8 gap-y-5">
                <div>
                  <p className="text-sm text-gray-600 dark:text-gray-400">Legal Name</p>
                  <p className="text-sm text-gray-900 mt-1">{billingProfile.legal_name || '\u2014'}</p>
                </div>
                <div>
                  <p className="text-sm text-gray-600 dark:text-gray-400">Email</p>
                  <p className="text-sm text-gray-900 mt-1">{billingProfile.email || '\u2014'}</p>
                </div>
                <div>
                  <p className="text-sm text-gray-600 dark:text-gray-400">Phone</p>
                  <p className="text-sm text-gray-900 mt-1">{billingProfile.phone || '\u2014'}</p>
                </div>
                <div>
                  <p className="text-sm text-gray-600 dark:text-gray-400">Address</p>
                  {billingProfile.address_line1 || billingProfile.city ? (
                    <div className="text-sm text-gray-900 mt-1 leading-relaxed">
                      {billingProfile.address_line1 && <p>{billingProfile.address_line1}</p>}
                      {billingProfile.address_line2 && <p>{billingProfile.address_line2}</p>}
                      <p>
                        {[billingProfile.city, billingProfile.state].filter(Boolean).join(', ')}
                        {billingProfile.postal_code && ` ${billingProfile.postal_code}`}
                      </p>
                      {billingProfile.country && <p>{billingProfile.country}</p>}
                    </div>
                  ) : (
                    <p className="text-sm text-gray-400 mt-1">\u2014</p>
                  )}
                </div>
                <div>
                  <p className="text-sm text-gray-600 dark:text-gray-400">Tax ID</p>
                  <p className="text-sm text-gray-900 mt-1 font-mono">{billingProfile.tax_id || billingProfile.tax_identifier || '\u2014'}</p>
                  {billingProfile.tax_id_type && <p className="text-xs text-gray-500 dark:text-gray-500 mt-0.5 uppercase">{billingProfile.tax_id_type}</p>}
                </div>
                <div>
                  <p className="text-sm text-gray-600 dark:text-gray-400">Tax Jurisdiction</p>
                  <p className="text-sm text-gray-900 mt-1">
                    {[billingProfile.tax_country, billingProfile.tax_state].filter(Boolean).join(' / ') || '\u2014'}
                  </p>
                  {billingProfile.tax_override_rate != null && (
                    <p className="text-xs text-gray-500 dark:text-gray-500 mt-0.5">
                      Rate override: {billingProfile.tax_override_rate}%{billingProfile.tax_override_name ? ` (${billingProfile.tax_override_name})` : ''}
                    </p>
                  )}
                </div>
                <div>
                  <p className="text-sm text-gray-600 dark:text-gray-400">Currency</p>
                  <p className="text-sm text-gray-900 mt-1">{billingProfile.currency ? billingProfile.currency.toUpperCase() : 'Default (from settings)'}</p>
                </div>
              </div>
            </div>
          </>
        ) : (
          <>
            <div className="px-6 py-4 border-b border-gray-100 dark:border-gray-800">
              <h2 className="text-sm font-semibold text-gray-900 dark:text-gray-100">Billing Profile</h2>
            </div>
            <div className="px-6 py-10 text-center">
              <div className="w-10 h-10 rounded-full bg-gray-100 flex items-center justify-center mx-auto mb-3">
                <CreditCard size={18} className="text-gray-400" />
              </div>
              <p className="text-sm text-gray-900 dark:text-gray-100">No billing profile</p>
              <p className="text-sm text-gray-400 mt-1 max-w-xs mx-auto">Set up billing details to enable invoicing and payments for this customer</p>
              <button
                onClick={() => setShowEditBilling(true)}
                className="mt-4 px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm transition-colors"
              >
                Set Up Billing Profile
              </button>
            </div>
          </>
        )}
      </div>

      {/* Dunning Override */}
      <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card mt-6">
        <div className="px-6 py-4 border-b border-gray-100 flex items-center justify-between">
          <h2 className="text-sm font-semibold text-gray-900 dark:text-gray-100">Dunning Override</h2>
          <button onClick={() => setShowDunningOverride(true)}
            className="text-xs font-medium text-velox-600 hover:text-velox-700 transition-colors">
            {dunningOverride ? 'Edit' : 'Configure'}
          </button>
        </div>
        {dunningOverride ? (
          <div className="divide-y divide-gray-100 dark:divide-gray-800">
            {dunningOverride.max_retry_attempts != null && (
              <div className="flex items-center justify-between px-6 py-3">
                <span className="text-sm text-gray-600 dark:text-gray-400">Max Retry Attempts</span>
                <span className="text-sm text-gray-900 dark:text-gray-100">{dunningOverride.max_retry_attempts}</span>
              </div>
            )}
            {dunningOverride.grace_period_days != null && (
              <div className="flex items-center justify-between px-6 py-3">
                <span className="text-sm text-gray-600 dark:text-gray-400">Grace Period</span>
                <span className="text-sm text-gray-900 dark:text-gray-100">{dunningOverride.grace_period_days} days</span>
              </div>
            )}
            {dunningOverride.final_action && (
              <div className="flex items-center justify-between px-6 py-3">
                <span className="text-sm text-gray-600 dark:text-gray-400">Final Action</span>
                <Badge status={dunningOverride.final_action} />
              </div>
            )}
          </div>
        ) : (
          <p className="px-6 py-6 text-sm text-gray-400 text-center">Using tenant default policy</p>
        )}
      </div>

      {/* Usage This Period */}
      <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card mt-6">
        <div className="px-6 py-4 border-b border-gray-100 flex justify-between items-center">
          <div>
            <h2 className="text-sm font-semibold text-gray-900 dark:text-gray-100">Usage This Period</h2>
            {(() => {
              const activeSub = allSubs.find(s => s.status === 'active' && s.current_billing_period_start && s.current_billing_period_end)
              return activeSub ? (
                <p className="text-sm text-gray-600 mt-0.5">{formatDate(activeSub.current_billing_period_start!)} — {formatDate(activeSub.current_billing_period_end!)}</p>
              ) : null
            })()}
          </div>
        </div>
        {usageSummary && Object.keys(usageSummary.meters).length > 0 ? (
          <div className="divide-y divide-gray-100 dark:divide-gray-800">
            {Object.entries(usageSummary.meters).map(([meter, qty]) => {
              const m = meterMap[meter]
              return (
                <div key={meter} className="flex items-center justify-between px-6 py-3">
                  <div>
                    <p className="text-sm text-gray-900 dark:text-gray-100">{m?.name || meter}</p>
                    {m?.unit && <p className="text-sm text-gray-600 dark:text-gray-400">{m.unit}</p>}
                  </div>
                  <span className="text-sm font-semibold text-gray-900 dark:text-gray-100 tabular-nums">{qty.toLocaleString()}</span>
                </div>
              )
            })}
          </div>
        ) : (
          <EmptyState title="No usage recorded" description="Usage events will appear here once ingested" />
        )}
      </div>

      {/* Payment Method */}
      <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card mt-6">
        <div className="px-6 py-4 border-b border-gray-100 flex justify-between items-center">
          <h2 className="text-sm font-semibold text-gray-900 dark:text-gray-100">Payment Method</h2>
          <button onClick={handleSetupPayment} disabled={settingUpPayment}
            className={`px-4 py-2 rounded-lg text-sm font-medium shadow-sm disabled:opacity-50 transition-colors ${
              paymentSetup.setup_status === 'ready'
                ? 'border border-gray-300 text-gray-700 hover:bg-gray-50'
                : 'bg-velox-600 text-white hover:bg-velox-700'
            }`}>
            {settingUpPayment ? 'Setting up...' : paymentSetup.setup_status === 'ready' ? 'Update Payment Method' : paymentSetup.setup_status === 'pending' ? 'Complete Setup' : 'Set Up Payment'}
          </button>
        </div>
        <div className="px-6 py-4 flex items-center justify-between">
          <div className="flex items-center gap-3">
            {paymentSetup.setup_status === 'ready' && paymentSetup.card_last4 ? (
              <>
                <div className="w-10 h-10 rounded-lg bg-gray-900 flex items-center justify-center">
                  <CreditCard size={18} className="text-white" />
                </div>
                <div>
                  <p className="text-sm font-medium text-gray-900 dark:text-gray-100">
                    {(paymentSetup.card_brand || 'Card').charAt(0).toUpperCase() + (paymentSetup.card_brand || 'card').slice(1)} ending in {paymentSetup.card_last4}
                  </p>
                  <p className="text-sm text-gray-600 dark:text-gray-400">
                    Expires {String(paymentSetup.card_exp_month).padStart(2, '0')}/{paymentSetup.card_exp_year}
                  </p>
                </div>
              </>
            ) : (
              <>
                <div className={`w-10 h-10 rounded-lg flex items-center justify-center ${
                  paymentSetup.setup_status === 'ready' ? 'bg-emerald-50' : paymentSetup.setup_status === 'pending' ? 'bg-amber-50' : 'bg-gray-100'
                }`}>
                  <CreditCard size={18} className={
                    paymentSetup.setup_status === 'ready' ? 'text-emerald-500' : paymentSetup.setup_status === 'pending' ? 'text-amber-500' : 'text-gray-400'
                  } />
                </div>
                <div>
                  <p className="text-sm text-gray-900 dark:text-gray-100">
                    {paymentSetup.setup_status === 'ready' ? 'Payment method active' : paymentSetup.setup_status === 'pending' ? 'Awaiting payment method setup' : 'No payment method'}
                  </p>
                  <p className="text-sm text-gray-600 dark:text-gray-400">
                    {paymentSetup.setup_status === 'ready' ? 'Invoices will be charged automatically' : paymentSetup.setup_status === 'pending' ? 'Customer needs to complete Stripe Checkout' : 'Set up a payment method to enable automatic billing'}
                  </p>
                </div>
              </>
            )}
          </div>
          {paymentSetup.setup_status === 'ready' && (
            <Badge status="active" label="Active" />
          )}
        </div>
      </div>

      <div className="grid grid-cols-2 gap-6 mt-6">
        {/* Subscriptions */}
        <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card">
          <div className="px-6 py-4 border-b border-gray-100 flex justify-between items-center">
            <h2 className="text-sm font-semibold text-gray-900 dark:text-gray-100">Subscriptions ({allSubs.length})</h2>
            <button
              onClick={() => setShowCreateSub(true)}
              className="px-3 py-1.5 bg-velox-600 text-white rounded-lg text-xs font-medium hover:bg-velox-700 shadow-sm transition-colors"
            >
              + Add
            </button>
          </div>
          <div className="divide-y divide-gray-100 dark:divide-gray-800">
            {allSubs.map(sub => (
              <Link key={sub.id} to={`/subscriptions/${sub.id}`} className="flex items-center justify-between px-6 py-3 hover:bg-gray-50 transition-colors">
                <div>
                  <p className="text-sm font-medium text-gray-900 dark:text-gray-100">{sub.display_name}</p>
                  <p className="text-xs text-gray-500">{sub.code}</p>
                </div>
                <Badge status={sub.status} />
              </Link>
            ))}
            {allSubs.length === 0 && (
              <p className="px-6 py-6 text-sm text-gray-600 text-center">No subscriptions</p>
            )}
          </div>
        </div>

        {/* Recent invoices */}
        <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card">
          <div className="px-6 py-4 border-b border-gray-100 dark:border-gray-800">
            <h2 className="text-sm font-semibold text-gray-900 dark:text-gray-100">Recent Invoices</h2>
          </div>
          <div className="divide-y divide-gray-100 dark:divide-gray-800">
            {overview?.recent_invoices.map(inv => (
              <Link key={inv.id} to={`/invoices/${inv.id}`} className="flex items-center justify-between px-6 py-3 hover:bg-gray-50 transition-colors">
                <div>
                  <p className="text-sm font-medium text-gray-900 dark:text-gray-100">{inv.invoice_number}</p>
                  <p className="text-xs text-gray-500">{formatDateTime(inv.created_at)}</p>
                </div>
                <div className="flex items-center gap-3">
                  <Badge status={inv.status} />
                  <span className="text-sm font-medium">{formatCents(inv.total_amount_cents)}</span>
                </div>
              </Link>
            ))}
            {(!overview?.recent_invoices.length) && (
              <p className="px-6 py-4 text-sm text-gray-400 dark:text-gray-500">No invoices</p>
            )}
          </div>
        </div>
      </div>

      {/* Edit Customer Modal */}
      {showEditCustomer && (
        <EditCustomerModal
          customer={customer}
          onClose={() => setShowEditCustomer(false)}
          onSaved={(updated) => {
            setCustomer(updated)
            setShowEditCustomer(false)
            toast.success('Customer updated')
          }}
        />
      )}

      {/* Create Subscription Modal */}
      {showCreateSub && id && (
        <CreateSubscriptionFromCustomerModal
          customerId={id}
          plans={plans}
          onClose={() => setShowCreateSub(false)}
          onCreated={(sub) => {
            setShowCreateSub(false)
            toast.success(`Subscription "${sub.display_name}" created`)
            loadData()
          }}
        />
      )}

      {/* Edit Billing Profile Modal */}
      {showEditBilling && id && (
        <EditBillingProfileModal
          customerId={id}
          profile={billingProfile}
          onClose={() => setShowEditBilling(false)}
          onSaved={(updated) => {
            setBillingProfile(updated)
            setShowEditBilling(false)
            toast.success('Billing profile saved')
          }}
        />
      )}

      {/* Dunning Override Modal */}
      {showDunningOverride && id && (
        <DunningOverrideModal
          customerId={id}
          override={dunningOverride}
          onClose={() => setShowDunningOverride(false)}
          onSaved={(updated) => {
            setDunningOverride(updated)
            setShowDunningOverride(false)
            toast.success('Dunning override saved')
          }}
          onDeleted={() => {
            setDunningOverride(null)
            setShowDunningOverride(false)
            toast.success('Dunning override removed')
          }}
        />
      )}
    </Layout>
  )
}

function EditCustomerModal({ customer, onClose, onSaved }: {
  customer: Customer; onClose: () => void; onSaved: (c: Customer) => void
}) {
  const [error, setError] = useState('')

  const { register, handleSubmit, formState: { errors, isSubmitting, isDirty } } = useForm<EditCustomerData>({
    resolver: zodResolver(editCustomerSchema),
    defaultValues: { display_name: customer.display_name, email: customer.email || '' },
  })

  const onSubmit = handleSubmit(async (data) => {
    if (!isDirty) return
    setError('')
    try {
      const updated = await api.updateCustomer(customer.id, data)
      onSaved(updated)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to update customer')
    }
  })

  return (
    <Modal open onClose={onClose} title="Edit Customer">
      <form onSubmit={onSubmit} noValidate className="space-y-3">
        <FormField label="Display Name" required maxLength={255}
          error={errors.display_name?.message}
          {...register('display_name')} />
        <FormField label="Email" type="email" maxLength={254}
          error={errors.email?.message}
          {...register('email')} />
        {error && <p className="text-sm text-red-600 bg-red-50 border border-red-200 rounded-lg px-3 py-2">{error}</p>}
        <div className="flex justify-end gap-3 pt-4 border-t border-gray-100 dark:border-gray-800 mt-2">
          <button type="button" onClick={onClose} className="px-4 py-2 border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-300 rounded-lg text-sm font-medium hover:bg-gray-50 dark:hover:bg-gray-800 transition-colors">Cancel</button>
          <button type="submit" disabled={isSubmitting || !isDirty}
            className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow disabled:opacity-50">
            {isSubmitting ? 'Saving...' : isDirty ? 'Save' : 'No changes'}
          </button>
        </div>
      </form>
    </Modal>
  )
}

function EditBillingProfileModal({ customerId, profile, onClose, onSaved }: {
  customerId: string; profile: BillingProfile | null; onClose: () => void; onSaved: (bp: BillingProfile) => void
}) {
  const [error, setError] = useState('')

  const defaultValues: BillingProfileData = {
    legal_name: profile?.legal_name || '',
    email: profile?.email || '',
    phone: profile?.phone || '',
    address_line1: profile?.address_line1 || '',
    address_line2: profile?.address_line2 || '',
    city: profile?.city || '',
    state: profile?.state || '',
    postal_code: profile?.postal_code || '',
    country: profile?.country || '',
    currency: profile?.currency || '',
    tax_identifier: profile?.tax_identifier || '',
    tax_exempt: profile?.tax_exempt || false,
    tax_id: profile?.tax_id || '',
    tax_id_type: profile?.tax_id_type || '',
    tax_country: profile?.tax_country || '',
    tax_state: profile?.tax_state || '',
    tax_override_rate: profile?.tax_override_rate != null ? String(profile.tax_override_rate) : '',
    tax_override_name: profile?.tax_override_name || '',
  }

  const { register, handleSubmit, watch, setValue, control, formState: { errors: formErrors, isSubmitting, isDirty } } = useForm<BillingProfileData>({
    resolver: zodResolver(billingProfileSchema),
    defaultValues,
  })

  const form = watch()
  const hasChanges = isDirty

  const onSubmit = handleSubmit(async (data) => {
    if (!hasChanges) return
    setError('')
    try {
      const payload = {
        ...data,
        tax_override_rate: data.tax_override_rate !== '' ? parseFloat(data.tax_override_rate) : null,
      }
      const updated = await api.upsertBillingProfile(customerId, payload)
      onSaved(updated)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to save billing profile')
    }
  })

  const usStates = [
    ['AL','Alabama'],['AK','Alaska'],['AZ','Arizona'],['AR','Arkansas'],['CA','California'],
    ['CO','Colorado'],['CT','Connecticut'],['DE','Delaware'],['DC','District of Columbia'],['FL','Florida'],
    ['GA','Georgia'],['HI','Hawaii'],['ID','Idaho'],['IL','Illinois'],['IN','Indiana'],
    ['IA','Iowa'],['KS','Kansas'],['KY','Kentucky'],['LA','Louisiana'],['ME','Maine'],
    ['MD','Maryland'],['MA','Massachusetts'],['MI','Michigan'],['MN','Minnesota'],['MS','Mississippi'],
    ['MO','Missouri'],['MT','Montana'],['NE','Nebraska'],['NV','Nevada'],['NH','New Hampshire'],
    ['NJ','New Jersey'],['NM','New Mexico'],['NY','New York'],['NC','North Carolina'],['ND','North Dakota'],
    ['OH','Ohio'],['OK','Oklahoma'],['OR','Oregon'],['PA','Pennsylvania'],['RI','Rhode Island'],
    ['SC','South Carolina'],['SD','South Dakota'],['TN','Tennessee'],['TX','Texas'],['UT','Utah'],
    ['VT','Vermont'],['VA','Virginia'],['WA','Washington'],['WV','West Virginia'],['WI','Wisconsin'],['WY','Wyoming'],
  ]
  const caProvinces = [
    ['AB','Alberta'],['BC','British Columbia'],['MB','Manitoba'],['NB','New Brunswick'],
    ['NL','Newfoundland and Labrador'],['NS','Nova Scotia'],['NT','Northwest Territories'],
    ['NU','Nunavut'],['ON','Ontario'],['PE','Prince Edward Island'],['QC','Quebec'],
    ['SK','Saskatchewan'],['YT','Yukon'],
  ]
  const inStates = [
    ['AP','Andhra Pradesh'],['AR','Arunachal Pradesh'],['AS','Assam'],['BR','Bihar'],
    ['CT','Chhattisgarh'],['GA','Goa'],['GJ','Gujarat'],['HR','Haryana'],['HP','Himachal Pradesh'],
    ['JK','Jammu & Kashmir'],['JH','Jharkhand'],['KA','Karnataka'],['KL','Kerala'],['MP','Madhya Pradesh'],
    ['MH','Maharashtra'],['MN','Manipur'],['ML','Meghalaya'],['MZ','Mizoram'],['NL','Nagaland'],
    ['OD','Odisha'],['PB','Punjab'],['RJ','Rajasthan'],['SK','Sikkim'],['TN','Tamil Nadu'],
    ['TG','Telangana'],['TR','Tripura'],['UP','Uttar Pradesh'],['UK','Uttarakhand'],['WB','West Bengal'],
    ['DL','Delhi'],['CH','Chandigarh'],['PY','Puducherry'],
  ]

  return (
    <Modal open onClose={onClose} title="Billing Profile" wide>
      <form onSubmit={onSubmit} noValidate className="max-h-[70vh] overflow-y-auto -mx-6 px-6 pb-1">
        {/* Contact */}
        <div className="space-y-3 pb-5">
          <p className="text-xs font-semibold text-gray-400 uppercase tracking-wider">Contact</p>
          <FormField label="Legal Name" maxLength={255} placeholder="Acme Corporation Inc."
            {...register('legal_name')} />
          <div className="grid grid-cols-2 gap-4">
            <FormField label="Email" type="email" maxLength={254} placeholder="billing@acme.com"
              error={formErrors.email?.message}
              {...register('email')} />
            <FormField label="Phone" type="tel" placeholder="+1 (555) 123-4567" maxLength={20}
              error={formErrors.phone?.message}
              {...register('phone')} />
          </div>
        </div>

        {/* Address */}
        <div className="space-y-3 py-5 border-t border-gray-100 dark:border-gray-800">
          <p className="text-xs font-semibold text-gray-400 uppercase tracking-wider">Address</p>
          <FormSelect label="Country" value={form.country}
            onChange={e => { setValue('country', e.target.value, { shouldDirty: true }); setValue('state', '', { shouldDirty: true }) }}
            placeholder="Select country..."
            options={[['US','United States'],['CA','Canada'],['GB','United Kingdom'],['DE','Germany'],['FR','France'],['IN','India'],['JP','Japan'],['AU','Australia'],['BR','Brazil'],['MX','Mexico'],['SG','Singapore'],['NL','Netherlands'],['SE','Sweden'],['CH','Switzerland']].map(([code, name]) => ({ value: code, label: name }))} />
          <FormField label="Street Address" maxLength={200} placeholder="123 Main Street"
            {...register('address_line1')} />
          <FormField label="Apt / Suite / Floor" maxLength={200} placeholder="Suite 100"
            {...register('address_line2')} />
          <div className="grid grid-cols-3 gap-4">
            <FormField label="City" maxLength={100} placeholder="San Francisco"
              {...register('city')} />
            {form.country === 'US' ? (
              <FormSelect label="State" {...register('state')}
                placeholder="Select state..."
                options={usStates.map(([code, name]) => ({ value: code, label: name }))} />
            ) : form.country === 'CA' ? (
              <FormSelect label="Province" {...register('state')}
                placeholder="Select province..."
                options={caProvinces.map(([code, name]) => ({ value: code, label: name }))} />
            ) : form.country === 'IN' ? (
              <FormSelect label="State" {...register('state')}
                placeholder="Select state..."
                options={inStates.map(([code, name]) => ({ value: code, label: name }))} />
            ) : (
              <FormField label="State / Province" placeholder="State" maxLength={50}
                {...register('state')} />
            )}
            <FormField label="Postal Code"
              placeholder={form.country === 'US' ? '94105' : form.country === 'GB' ? 'SW1A 1AA' : form.country === 'IN' ? '400001' : 'Postal code'} maxLength={10}
              {...register('postal_code')} />
          </div>
        </div>

        {/* Tax & Billing */}
        <div className="space-y-3 py-5 border-t border-gray-100 dark:border-gray-800">
          <p className="text-xs font-semibold text-gray-400 uppercase tracking-wider">Tax & Billing</p>
          <div className="grid grid-cols-2 gap-4">
            <FormField label="Tax ID" maxLength={50}
              placeholder={form.tax_country === 'US' ? 'EIN (e.g. 12-3456789)' : form.tax_country === 'IN' ? 'GSTIN (e.g. 29ABCDE1234F1Z5)' : form.tax_country === 'GB' ? 'VAT number' : 'Tax ID'} mono
              {...register('tax_id')} />
            <FormSelect label="Tax ID Type"
              {...register('tax_id_type')}
              placeholder="Select type..."
              options={[
                { value: '', label: 'None' },
                { value: 'gst', label: 'GST' },
                { value: 'vat', label: 'VAT' },
                { value: 'ein', label: 'EIN' },
                { value: 'abn', label: 'ABN' },
                { value: 'other', label: 'Other' },
              ]} />
            <Controller
              name="tax_country"
              control={control}
              render={({ field }) => (
                <FormField label="Tax Country" maxLength={2}
                  placeholder="ISO code (e.g. US, IN, DE)"
                  name={field.name} ref={field.ref} onBlur={field.onBlur}
                  value={field.value}
                  onChange={e => field.onChange(e.target.value.toUpperCase())} />
              )}
            />
            <Controller
              name="tax_state"
              control={control}
              render={({ field }) => (
                <FormField label="Tax State" maxLength={10}
                  placeholder="State/province code (e.g. KA, CA)"
                  name={field.name} ref={field.ref} onBlur={field.onBlur}
                  value={field.value}
                  onChange={e => field.onChange(e.target.value.toUpperCase())} />
              )}
            />
            <FormField label="Tax Rate Override" maxLength={6}
              placeholder="e.g. 18.00 (leave empty for default)"
              {...register('tax_override_rate')} />
            <FormField label="Tax Name Override" maxLength={30}
              placeholder="e.g. VAT, GST, Sales Tax"
              {...register('tax_override_name')} />
          </div>
          <div className="grid grid-cols-2 gap-4">
            <FormField label="Tax Identifier (legacy)" maxLength={30}
              placeholder="Legacy tax identifier" mono
              {...register('tax_identifier')} />
            <div className="flex items-center gap-3 pt-6">
              <Controller
                name="tax_exempt"
                control={control}
                render={({ field }) => (
                  <button type="button" onClick={() => field.onChange(!field.value)}
                    className={`relative inline-flex h-5 w-9 items-center rounded-full transition-colors ${field.value ? 'bg-velox-600' : 'bg-gray-200'}`}>
                    <span className={`inline-block h-3.5 w-3.5 transform rounded-full bg-white transition-transform ${field.value ? 'translate-x-[18px]' : 'translate-x-[2px]'}`} />
                  </button>
                )}
              />
              <span className="text-sm text-gray-700">Tax Exempt</span>
            </div>
            <FormSelect label="Billing Currency"
              {...register('currency')}
              placeholder="Default (from settings)"
              options={[
                { value: '', label: 'Default (from settings)' },
                { value: 'usd', label: 'USD — US Dollar' },
                { value: 'eur', label: 'EUR — Euro' },
                { value: 'gbp', label: 'GBP — British Pound' },
                { value: 'cad', label: 'CAD — Canadian Dollar' },
                { value: 'aud', label: 'AUD — Australian Dollar' },
                { value: 'jpy', label: 'JPY — Japanese Yen' },
                { value: 'inr', label: 'INR — Indian Rupee' },
                { value: 'chf', label: 'CHF — Swiss Franc' },
                { value: 'sgd', label: 'SGD — Singapore Dollar' },
                { value: 'brl', label: 'BRL — Brazilian Real' },
                { value: 'mxn', label: 'MXN — Mexican Peso' },
                { value: 'sek', label: 'SEK — Swedish Krona' },
                { value: 'nzd', label: 'NZD — New Zealand Dollar' },
                { value: 'hkd', label: 'HKD — Hong Kong Dollar' },
                { value: 'zar', label: 'ZAR — South African Rand' },
              ]} />
          </div>
        </div>

        {error && <p className="text-sm text-red-600 bg-red-50 border border-red-200 rounded-lg px-3 py-2 mt-4">{error}</p>}
        <div className="flex justify-end gap-3 pt-4 mt-4 border-t border-gray-100 dark:border-gray-800">
          <button type="button" onClick={onClose} className="px-4 py-2 border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-300 rounded-lg text-sm font-medium hover:bg-gray-50 dark:hover:bg-gray-800 transition-colors">Cancel</button>
          <button type="submit" disabled={isSubmitting || !hasChanges}
            className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow disabled:opacity-50">
            {isSubmitting ? 'Saving...' : hasChanges ? 'Save' : 'No changes'}
          </button>
        </div>
      </form>
    </Modal>
  )
}

function CreateSubscriptionFromCustomerModal({ customerId, plans, onClose, onCreated }: {
  customerId: string; plans: Plan[]; onClose: () => void; onCreated: (sub: Subscription) => void
}) {
  const [error, setError] = useState('')

  const { register, handleSubmit, control, formState: { errors, isSubmitting } } = useForm<CreateSubFromCustomerData>({
    resolver: zodResolver(createSubFromCustomerSchema),
    defaultValues: { code: '', display_name: '', plan_id: '', start_now: true },
  })

  const onSubmit = handleSubmit(async (data) => {
    setError('')
    try {
      const sub = await api.createSubscription({
        ...data,
        customer_id: customerId,
      })
      onCreated(sub)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create subscription')
    }
  })

  return (
    <Modal open onClose={onClose} title="Create Subscription">
      <form onSubmit={onSubmit} noValidate className="space-y-3">
        <FormField label="Display Name" required placeholder="Pro Monthly" maxLength={255}
          error={errors.display_name?.message}
          {...register('display_name')} />
        <FormField label="Code" required placeholder="pro-monthly" maxLength={100} mono
          error={errors.code?.message}
          {...register('code')} />
        <Controller
          name="plan_id"
          control={control}
          render={({ field }) => (
            <SearchSelect label="Plan" required value={field.value} error={errors.plan_id?.message}
              onChange={field.onChange}
              placeholder="Select plan..."
              options={plans.map(p => ({ value: p.id, label: p.name, sublabel: p.code }))} />
          )}
        />
        <Controller
          name="start_now"
          control={control}
          render={({ field }) => (
            <label className="flex items-center gap-2 text-sm">
              <input type="checkbox" checked={field.value} onChange={e => field.onChange(e.target.checked)} />
              Start immediately (activate + set billing period)
            </label>
          )}
        />
        {error && <p className="text-sm text-red-600 bg-red-50 border border-red-200 rounded-lg px-3 py-2">{error}</p>}
        <div className="flex justify-end gap-3 pt-4 border-t border-gray-100 dark:border-gray-800 mt-2">
          <button type="button" onClick={onClose} className="px-4 py-2 border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-300 rounded-lg text-sm font-medium hover:bg-gray-50 dark:hover:bg-gray-800 transition-colors">Cancel</button>
          <button type="submit" disabled={isSubmitting}
            className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow disabled:opacity-50">
            {isSubmitting ? 'Creating...' : 'Create Subscription'}
          </button>
        </div>
      </form>
    </Modal>
  )
}

function DunningOverrideModal({ customerId, override, onClose, onSaved, onDeleted }: {
  customerId: string; override: CustomerDunningOverride | null; onClose: () => void; onSaved: (o: CustomerDunningOverride) => void; onDeleted: () => void
}) {
  const [form, setForm] = useState({
    max_retry_attempts: override?.max_retry_attempts != null ? String(override.max_retry_attempts) : '',
    grace_period_days: override?.grace_period_days != null ? String(override.grace_period_days) : '',
    final_action: override?.final_action || '',
  })
  const [saving, setSaving] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [error, setError] = useState('')

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setSaving(true); setError('')
    try {
      const payload: Partial<CustomerDunningOverride> = {}
      if (form.max_retry_attempts !== '') payload.max_retry_attempts = parseInt(form.max_retry_attempts, 10)
      if (form.grace_period_days !== '') payload.grace_period_days = parseInt(form.grace_period_days, 10)
      if (form.final_action) payload.final_action = form.final_action
      const updated = await api.upsertCustomerDunningOverride(customerId, payload)
      onSaved(updated)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to save')
    } finally {
      setSaving(false)
    }
  }

  const handleDelete = async () => {
    setDeleting(true); setError('')
    try {
      await api.deleteCustomerDunningOverride(customerId)
      onDeleted()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to delete')
    } finally {
      setDeleting(false)
    }
  }

  return (
    <Modal open onClose={onClose} title="Dunning Override">
      <form onSubmit={handleSubmit} noValidate className="space-y-3">
        <FormField label="Max Retry Attempts" type="number" value={form.max_retry_attempts}
          placeholder="Leave empty for tenant default"
          onChange={e => setForm(f => ({ ...f, max_retry_attempts: e.target.value }))}
          hint="How many times to retry the failed payment before escalating" />
        <FormField label="Grace Period (days)" type="number" value={form.grace_period_days}
          placeholder="Leave empty for tenant default"
          onChange={e => setForm(f => ({ ...f, grace_period_days: e.target.value }))}
          hint="Days to wait after initial failure before first retry" />
        <FormSelect label="Final Action" value={form.final_action}
          onChange={e => setForm(f => ({ ...f, final_action: e.target.value }))}
          placeholder="Tenant default"
          options={[
            { value: '', label: 'Tenant default' },
            { value: 'manual_review', label: 'Escalate for review' },
            { value: 'pause', label: 'Pause subscription' },
            { value: 'write_off_later', label: 'Mark uncollectible' },
          ]} />
        {error && <p className="text-sm text-red-600 bg-red-50 border border-red-200 rounded-lg px-3 py-2">{error}</p>}
        <div className="flex justify-between pt-4 border-t border-gray-100 dark:border-gray-800 mt-2">
          {override ? (
            <button type="button" onClick={handleDelete} disabled={deleting}
              className="px-4 py-2 border border-red-300 text-red-600 rounded-lg text-sm font-medium hover:bg-red-50 transition-colors disabled:opacity-50">
              {deleting ? 'Removing...' : 'Reset to Default'}
            </button>
          ) : <div />}
          <div className="flex gap-3">
            <button type="button" onClick={onClose} className="px-4 py-2 border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-300 rounded-lg text-sm font-medium hover:bg-gray-50 dark:hover:bg-gray-800 transition-colors">Cancel</button>
            <button type="submit" disabled={saving}
              className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow disabled:opacity-50">
              {saving ? 'Saving...' : 'Save'}
            </button>
          </div>
        </div>
      </form>
    </Modal>
  )
}
