import { useEffect, useState } from 'react'
import { useParams, Link } from 'react-router-dom'
import { api, formatCents, formatDate, type Customer, type CustomerOverview, type BillingProfile, type UsageSummary, type Meter, type Plan, type Subscription } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'
import { StatCard } from '@/components/StatCard'
import { Modal } from '@/components/Modal'
import { Breadcrumbs } from '@/components/Breadcrumbs'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { useToast } from '@/components/Toast'

export function CustomerDetailPage() {
  const { id } = useParams<{ id: string }>()
  const [customer, setCustomer] = useState<Customer | null>(null)
  const [overview, setOverview] = useState<CustomerOverview | null>(null)
  const [balance, setBalance] = useState(0)
  const [billingProfile, setBillingProfile] = useState<BillingProfile | null>(null)
  const [usageSummary, setUsageSummary] = useState<UsageSummary | null>(null)
  const [meterMap, setMeterMap] = useState<Record<string, string>>({})
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [plans, setPlans] = useState<Plan[]>([])
  const [showEditCustomer, setShowEditCustomer] = useState(false)
  const [showEditBilling, setShowEditBilling] = useState(false)
  const [showCreateSub, setShowCreateSub] = useState(false)
  const toast = useToast()

  const loadData = () => {
    if (!id) return
    setLoading(true)
    setError(null)
    Promise.all([
      api.getCustomer(id),
      api.customerOverview(id),
      api.getBalance(id).catch(() => ({ balance_cents: 0 })),
      api.getBillingProfile(id).catch(() => null),
      api.usageSummary(id).catch(() => null),
      api.listMeters().catch(() => ({ data: [] as Meter[] })),
      api.listPlans().catch(() => ({ data: [] as Plan[] })),
    ]).then(([c, o, b, bp, us, metersRes, plansRes]) => {
      setCustomer(c)
      setOverview(o)
      setBalance(b.balance_cents)
      setBillingProfile(bp)
      setUsageSummary(us)
      const mm: Record<string, string> = {}
      metersRes.data.forEach(m => { mm[m.key] = m.name })
      setMeterMap(mm)
      setPlans(plansRes.data.filter(p => p.status === 'active'))
      setLoading(false)
    }).catch(err => { setError(err instanceof Error ? err.message : 'Failed to load customer'); setLoading(false) })
  }

  useEffect(() => { loadData() }, [id])

  if (loading) {
    return (
      <Layout>
        <Breadcrumbs items={[{ label: 'Customers', to: '/customers' }, { label: 'Loading...' }]} />
        <div className="bg-white rounded-xl shadow-card">
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

      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900">{customer.display_name}</h1>
          <p className="text-sm text-gray-500 mt-0.5 font-mono">{customer.external_id}</p>
        </div>
        <div className="flex items-center gap-3">
          <button
            onClick={() => setShowEditCustomer(true)}
            className="px-3 py-1.5 border border-gray-300 text-gray-700 rounded-lg text-xs font-medium hover:bg-gray-50 transition-colors"
          >
            Edit Customer
          </button>
          <Badge status={customer.status} />
        </div>
      </div>

      <div className="grid grid-cols-3 gap-4 mt-6">
        <StatCard title="Email" value={customer.email || '\u2014'} />
        <StatCard title="Credit Balance" value={formatCents(balance)} />
        <StatCard title="Active Subscriptions" value={String(overview?.active_subscriptions.length || 0)} />
      </div>

      {/* Billing Profile */}
      <div className="bg-white rounded-xl shadow-card mt-6">
        <div className="px-6 py-4 border-b border-gray-100 flex justify-between items-center">
          <h2 className="text-sm font-semibold text-gray-900">Billing Profile</h2>
          {billingProfile && (
            <button
              onClick={() => setShowEditBilling(true)}
              className="text-xs text-velox-600 hover:underline"
            >
              Edit
            </button>
          )}
        </div>
        {billingProfile ? (
          <div className="px-6 py-4 grid grid-cols-2 lg:grid-cols-3 gap-4">
            <div>
              <p className="text-xs text-gray-500">Legal Name</p>
              <p className="text-sm text-gray-900 mt-0.5">{billingProfile.legal_name || '\u2014'}</p>
            </div>
            <div>
              <p className="text-xs text-gray-500">Email</p>
              <p className="text-sm text-gray-900 mt-0.5">{billingProfile.email || '\u2014'}</p>
            </div>
            <div>
              <p className="text-xs text-gray-500">Phone</p>
              <p className="text-sm text-gray-900 mt-0.5">{billingProfile.phone || '\u2014'}</p>
            </div>
            <div>
              <p className="text-xs text-gray-500">Address</p>
              <p className="text-sm text-gray-900 mt-0.5">
                {[billingProfile.address_line1, billingProfile.address_line2, billingProfile.city, billingProfile.state, billingProfile.postal_code, billingProfile.country].filter(Boolean).join(', ') || '\u2014'}
              </p>
            </div>
            <div>
              <p className="text-xs text-gray-500">Tax ID</p>
              <p className="text-sm text-gray-900 mt-0.5">{billingProfile.tax_identifier || '\u2014'}</p>
            </div>
            <div>
              <p className="text-xs text-gray-500">Currency</p>
              <p className="text-sm text-gray-900 mt-0.5">{billingProfile.currency || '\u2014'}</p>
            </div>
          </div>
        ) : (
          <EmptyState
            title="No billing profile"
            description="Set up a billing profile to enable invoicing"
            actionLabel="Set up billing profile"
            onAction={() => setShowEditBilling(true)}
          />
        )}
      </div>

      {/* Usage This Period */}
      <div className="bg-white rounded-xl shadow-card mt-6">
        <div className="px-6 py-4 border-b border-gray-100">
          <h2 className="text-sm font-semibold text-gray-900">Usage This Period</h2>
        </div>
        {usageSummary && Object.keys(usageSummary.meters).length > 0 ? (
          <div className="px-6 py-4">
            <div className="space-y-2">
              {Object.entries(usageSummary.meters).map(([meter, qty]) => (
                <div key={meter} className="flex items-center justify-between py-1">
                  <span className="text-sm text-gray-700">{meterMap[meter] || meter}</span>
                  <span className="text-sm font-medium text-gray-900">{qty.toLocaleString()}</span>
                </div>
              ))}
            </div>
            <div className="mt-3 pt-3 border-t border-gray-100 flex items-center justify-between">
              <span className="text-xs text-gray-500">Total events</span>
              <span className="text-sm font-medium text-gray-900">{usageSummary.total_events.toLocaleString()}</span>
            </div>
          </div>
        ) : (
          <EmptyState title="No usage recorded" description="Usage events will appear here once ingested" />
        )}
      </div>

      <div className="grid grid-cols-2 gap-6 mt-8">
        {/* Subscriptions */}
        <div className="bg-white rounded-xl shadow-card">
          <div className="px-6 py-4 border-b border-gray-100 flex justify-between items-center">
            <h2 className="text-sm font-semibold text-gray-900">Subscriptions</h2>
            <button
              onClick={() => setShowCreateSub(true)}
              className="text-xs text-velox-600 hover:underline"
            >
              + Add subscription
            </button>
          </div>
          <div className="divide-y divide-gray-50">
            {overview?.active_subscriptions.map(sub => (
              <Link key={sub.id} to={`/subscriptions/${sub.id}`} className="flex items-center justify-between px-6 py-3 hover:bg-gray-50 transition-colors">
                <div>
                  <p className="text-sm font-medium text-gray-900">{sub.display_name}</p>
                  <p className="text-xs text-gray-400">{sub.code}</p>
                </div>
                <Badge status={sub.status} />
              </Link>
            ))}
            {(!overview?.active_subscriptions.length) && (
              <div className="px-6 py-4 text-center">
                <p className="text-sm text-gray-400">No subscriptions</p>
                <button onClick={() => setShowCreateSub(true)} className="mt-2 text-xs text-velox-600 hover:underline">
                  Create subscription
                </button>
              </div>
            )}
          </div>
        </div>

        {/* Recent invoices */}
        <div className="bg-white rounded-xl shadow-card">
          <div className="px-6 py-4 border-b border-gray-100">
            <h2 className="text-sm font-semibold text-gray-900">Recent Invoices</h2>
          </div>
          <div className="divide-y divide-gray-50">
            {overview?.recent_invoices.map(inv => (
              <Link key={inv.id} to={`/invoices/${inv.id}`} className="flex items-center justify-between px-6 py-3 hover:bg-gray-50">
                <div>
                  <p className="text-sm font-medium text-gray-900">{inv.invoice_number}</p>
                  <p className="text-xs text-gray-400">{formatDate(inv.created_at)}</p>
                </div>
                <div className="flex items-center gap-3">
                  <Badge status={inv.status} />
                  <span className="text-sm font-medium">{formatCents(inv.total_amount_cents)}</span>
                </div>
              </Link>
            ))}
            {(!overview?.recent_invoices.length) && (
              <p className="px-6 py-4 text-sm text-gray-400">No invoices</p>
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
    </Layout>
  )
}

function EditCustomerModal({ customer, onClose, onSaved }: {
  customer: Customer; onClose: () => void; onSaved: (c: Customer) => void
}) {
  const [form, setForm] = useState({ display_name: customer.display_name, email: customer.email || '' })
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setSaving(true); setError('')
    try {
      const updated = await api.updateCustomer(customer.id, form)
      onSaved(updated)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed')
    } finally {
      setSaving(false)
    }
  }

  return (
    <Modal open onClose={onClose} title="Edit Customer">
      <form onSubmit={handleSubmit} className="space-y-3">
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">Display Name <span className="text-red-500">*</span></label>
          <input type="text" value={form.display_name} onChange={e => setForm(f => ({ ...f, display_name: e.target.value }))}
            className="w-full px-3 py-2 border border-gray-200 rounded-lg shadow-sm text-sm focus:outline-none focus:ring-2 focus:ring-velox-500" required maxLength={255} />
        </div>
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">Email</label>
          <input type="email" value={form.email} onChange={e => setForm(f => ({ ...f, email: e.target.value }))}
            className="w-full px-3 py-2 border border-gray-200 rounded-lg shadow-sm text-sm focus:outline-none focus:ring-2 focus:ring-velox-500"
            maxLength={254} pattern="[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}" title="Enter a valid email address" />
        </div>
        {error && <p className="text-red-600 text-xs">{error}</p>}
        <div className="flex justify-end gap-3 pt-4 border-t border-gray-100 mt-1">
          <button type="button" onClick={onClose} className="px-4 py-2 border border-gray-300 text-gray-700 rounded-lg text-sm font-medium hover:bg-gray-50 transition-colors">Cancel</button>
          <button type="submit" disabled={saving}
            className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow disabled:opacity-50">
            {saving ? 'Saving...' : 'Save'}
          </button>
        </div>
      </form>
    </Modal>
  )
}

function EditBillingProfileModal({ customerId, profile, onClose, onSaved }: {
  customerId: string; profile: BillingProfile | null; onClose: () => void; onSaved: (bp: BillingProfile) => void
}) {
  const [form, setForm] = useState({
    legal_name: profile?.legal_name || '',
    email: profile?.email || '',
    phone: profile?.phone || '',
    address_line1: profile?.address_line1 || '',
    address_line2: profile?.address_line2 || '',
    city: profile?.city || '',
    state: profile?.state || '',
    postal_code: profile?.postal_code || '',
    country: profile?.country || '',
    currency: profile?.currency || 'usd',
    tax_identifier: profile?.tax_identifier || '',
  })
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setSaving(true); setError('')
    try {
      const updated = await api.upsertBillingProfile(customerId, form)
      onSaved(updated)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed')
    } finally {
      setSaving(false)
    }
  }

  const fieldClass = "w-full px-3 py-2 border border-gray-200 rounded-lg shadow-sm text-sm focus:outline-none focus:ring-2 focus:ring-velox-500"

  return (
    <Modal open onClose={onClose} title="Billing Profile">
      <form onSubmit={handleSubmit} className="space-y-3 max-h-[70vh] overflow-y-auto">
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">Legal Name</label>
          <input type="text" value={form.legal_name} onChange={e => setForm(f => ({ ...f, legal_name: e.target.value }))} className={fieldClass} maxLength={255} />
        </div>
        <div className="grid grid-cols-2 gap-3">
          <div>
            <label className="block text-sm font-medium text-gray-700 mb-1">Email</label>
            <input type="email" value={form.email} onChange={e => setForm(f => ({ ...f, email: e.target.value }))} className={fieldClass}
              maxLength={254} pattern="[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}" title="Enter a valid email address" />
          </div>
          <div>
            <label className="block text-sm font-medium text-gray-700 mb-1">Phone</label>
            <input type="tel" value={form.phone} onChange={e => setForm(f => ({ ...f, phone: e.target.value }))} className={fieldClass}
              placeholder="+1 (555) 123-4567" maxLength={20} pattern="[\+\d\s\-\(\)]{7,20}" title="Enter a valid phone number" />
          </div>
        </div>
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">Address Line 1</label>
          <input type="text" value={form.address_line1} onChange={e => setForm(f => ({ ...f, address_line1: e.target.value }))} className={fieldClass} maxLength={200} />
        </div>
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">Address Line 2</label>
          <input type="text" value={form.address_line2} onChange={e => setForm(f => ({ ...f, address_line2: e.target.value }))} className={fieldClass} maxLength={200} />
        </div>
        <div className="grid grid-cols-3 gap-3">
          <div>
            <label className="block text-sm font-medium text-gray-700 mb-1">City</label>
            <input type="text" value={form.city} onChange={e => setForm(f => ({ ...f, city: e.target.value }))} className={fieldClass} maxLength={100} />
          </div>
          <div>
            <label className="block text-sm font-medium text-gray-700 mb-1">State</label>
            <input type="text" value={form.state} onChange={e => setForm(f => ({ ...f, state: e.target.value }))} className={fieldClass}
              placeholder="e.g., CA, NY, TX" maxLength={50} />
          </div>
          <div>
            <label className="block text-sm font-medium text-gray-700 mb-1">Postal Code</label>
            <input type="text" value={form.postal_code} onChange={e => setForm(f => ({ ...f, postal_code: e.target.value }))} className={fieldClass}
              placeholder="e.g., 10001 or SW1A 1AA" maxLength={10} />
          </div>
        </div>
        <div className="grid grid-cols-2 gap-3">
          <div>
            <label className="block text-sm font-medium text-gray-700 mb-1">Country</label>
            <select value={form.country} onChange={e => setForm(f => ({ ...f, country: e.target.value }))} className={fieldClass + ' bg-white'}>
              <option value="">Select country...</option>
              {[['US', 'United States'], ['CA', 'Canada'], ['GB', 'United Kingdom'], ['DE', 'Germany'], ['FR', 'France'], ['IN', 'India'], ['JP', 'Japan'], ['AU', 'Australia'], ['BR', 'Brazil'], ['MX', 'Mexico'], ['SG', 'Singapore'], ['NL', 'Netherlands'], ['SE', 'Sweden'], ['CH', 'Switzerland']].map(([code, name]) => (
                <option key={code} value={code}>{name} ({code})</option>
              ))}
            </select>
          </div>
          <div>
            <label className="block text-sm font-medium text-gray-700 mb-1">Currency</label>
            <select value={form.currency} onChange={e => setForm(f => ({ ...f, currency: e.target.value }))} className={fieldClass + ' bg-white'}>
              <option value="">Select currency...</option>
              {['USD', 'EUR', 'GBP', 'CAD', 'AUD', 'JPY', 'INR', 'CHF'].map(c => (
                <option key={c} value={c}>{c}</option>
              ))}
            </select>
          </div>
        </div>
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">Tax ID (VAT / EIN / GST)</label>
          <input type="text" value={form.tax_identifier} onChange={e => setForm(f => ({ ...f, tax_identifier: e.target.value }))} className={fieldClass}
            maxLength={30} placeholder="e.g., US12-3456789" />
        </div>
        {error && <p className="text-red-600 text-xs">{error}</p>}
        <div className="flex justify-end gap-3 pt-4 border-t border-gray-100 mt-1">
          <button type="button" onClick={onClose} className="px-4 py-2 border border-gray-300 text-gray-700 rounded-lg text-sm font-medium hover:bg-gray-50 transition-colors">Cancel</button>
          <button type="submit" disabled={saving}
            className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow disabled:opacity-50">
            {saving ? 'Saving...' : 'Save'}
          </button>
        </div>
      </form>
    </Modal>
  )
}

function CreateSubscriptionFromCustomerModal({ customerId, plans, onClose, onCreated }: {
  customerId: string; plans: Plan[]; onClose: () => void; onCreated: (sub: Subscription) => void
}) {
  const [form, setForm] = useState({
    code: '', display_name: '', plan_id: '', start_now: true,
  })
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setSaving(true); setError('')
    try {
      const sub = await api.createSubscription({
        ...form,
        customer_id: customerId,
      })
      onCreated(sub)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create subscription')
    } finally {
      setSaving(false)
    }
  }

  return (
    <Modal open onClose={onClose} title="Create Subscription">
      <form onSubmit={handleSubmit} className="space-y-3">
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">Display Name <span className="text-red-500">*</span></label>
          <input type="text" value={form.display_name} onChange={e => setForm(f => ({ ...f, display_name: e.target.value }))}
            className="w-full px-3 py-2 border border-gray-200 rounded-lg shadow-sm text-sm focus:outline-none focus:ring-2 focus:ring-velox-500"
            placeholder="Pro Monthly" required maxLength={255} />
        </div>
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">Code <span className="text-red-500">*</span></label>
          <input type="text" value={form.code} onChange={e => setForm(f => ({ ...f, code: e.target.value }))}
            className="w-full px-3 py-2 border border-gray-200 rounded-lg shadow-sm text-sm focus:outline-none focus:ring-2 focus:ring-velox-500 font-mono"
            placeholder="pro-monthly" required maxLength={100}
            pattern="[a-zA-Z0-9_\-]+" title="Only letters, numbers, underscores, and hyphens" />
        </div>
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">Plan <span className="text-red-500">*</span></label>
          <select value={form.plan_id} onChange={e => setForm(f => ({ ...f, plan_id: e.target.value }))}
            className="w-full px-3 py-2 border border-gray-200 rounded-lg shadow-sm text-sm focus:outline-none focus:ring-2 focus:ring-velox-500 bg-white" required>
            <option value="">Select plan...</option>
            {plans.map(p => <option key={p.id} value={p.id}>{p.name} ({p.code})</option>)}
          </select>
        </div>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={form.start_now} onChange={e => setForm(f => ({ ...f, start_now: e.target.checked }))} />
          Start immediately (activate + set billing period)
        </label>
        {error && <p className="text-red-600 text-xs">{error}</p>}
        <div className="flex justify-end gap-3 pt-4 border-t border-gray-100 mt-1">
          <button type="button" onClick={onClose} className="px-4 py-2 border border-gray-300 text-gray-700 rounded-lg text-sm font-medium hover:bg-gray-50 transition-colors">Cancel</button>
          <button type="submit" disabled={saving}
            className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow disabled:opacity-50">
            {saving ? 'Creating...' : 'Create Subscription'}
          </button>
        </div>
      </form>
    </Modal>
  )
}
