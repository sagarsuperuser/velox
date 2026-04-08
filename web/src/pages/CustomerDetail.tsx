import { useEffect, useState, useMemo } from 'react'
import { useParams, Link } from 'react-router-dom'
import { api, formatCents, formatDate, type Customer, type CustomerOverview, type BillingProfile, type UsageSummary, type Meter, type Plan, type Subscription } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'
import { Modal } from '@/components/Modal'
import { FormField, FormSelect } from '@/components/FormField'
import { Breadcrumbs } from '@/components/Breadcrumbs'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { useToast } from '@/components/Toast'
import { useFormValidation, rules } from '@/hooks/useFormValidation'
import { Copy, Check, CreditCard, Pencil } from 'lucide-react'

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
  const [copiedField, setCopiedField] = useState<string | null>(null)
  const toast = useToast()

  const copyToClipboard = (value: string, field: string) => {
    navigator.clipboard.writeText(value)
    setCopiedField(field)
    setTimeout(() => setCopiedField(null), 2000)
  }

  const CopyButton = ({ value, field }: { value: string; field: string }) => (
    <button
      onClick={() => copyToClipboard(value, field)}
      className="inline-flex items-center justify-center w-5 h-5 rounded hover:bg-gray-100 transition-colors text-gray-400 hover:text-gray-600"
      title="Copy to clipboard"
    >
      {copiedField === field ? (
        <Check className="w-3.5 h-3.5 text-green-500" />
      ) : (
        <Copy className="w-3.5 h-3.5" />
      )}
    </button>
  )

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

      {/* Header */}
      <div className="flex items-start justify-between">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900">{customer.display_name}</h1>
          <div className="flex items-center gap-1.5 mt-1">
            <span className="text-xs text-gray-500 font-mono">{customer.id}</span>
            <CopyButton value={customer.id} field="header-id" />
          </div>
        </div>
        <div className="flex items-center gap-3">
          <button
            onClick={() => setShowEditCustomer(true)}
            className="inline-flex items-center gap-1.5 px-4 py-2 border border-gray-300 text-gray-700 rounded-lg text-sm font-medium hover:bg-gray-50 hover:border-gray-400 transition-colors"
          >
            <Pencil size={14} />
            Edit
          </button>
          <Badge status={customer.status} />
        </div>
      </div>

      {/* Key Metrics Row */}
      <div className="bg-white rounded-xl shadow-card mt-6">
        <div className="flex divide-x divide-gray-100">
          <div className="flex-1 px-6 py-4">
            <p className="text-xs text-gray-500">Email</p>
            <p className="text-sm font-medium text-gray-900 mt-1">{customer.email || '\u2014'}</p>
          </div>
          <div className="flex-1 px-6 py-4">
            <p className="text-xs text-gray-500">Credit Balance</p>
            <p className="text-sm font-medium text-gray-900 mt-1">{formatCents(balance)}</p>
          </div>
          <div className="flex-1 px-6 py-4">
            <p className="text-xs text-gray-500">Active Subs</p>
            <p className="text-sm font-medium text-gray-900 mt-1">{overview?.active_subscriptions.length || 0}</p>
          </div>
          <div className="flex-1 px-6 py-4">
            <p className="text-xs text-gray-500">Created</p>
            <p className="text-sm font-medium text-gray-900 mt-1">{formatDate(customer.created_at)}</p>
          </div>
        </div>
      </div>

      {/* Properties Card */}
      <div className="bg-white rounded-xl shadow-card mt-6">
        <div className="px-6 py-4 border-b border-gray-100">
          <h2 className="text-sm font-semibold text-gray-900">Properties</h2>
        </div>
        <div className="divide-y divide-gray-50">
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-xs text-gray-500 w-36 shrink-0">External ID</span>
            <span className="text-sm text-gray-900 font-mono">{customer.external_id}</span>
          </div>
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-xs text-gray-500 w-36 shrink-0">Email</span>
            <span className="text-sm text-gray-900">{customer.email || '\u2014'}</span>
          </div>
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-xs text-gray-500 w-36 shrink-0">Status</span>
            <Badge status={customer.status} />
          </div>
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-xs text-gray-500 w-36 shrink-0">Created</span>
            <span className="text-sm text-gray-900">{formatDate(customer.created_at)}</span>
          </div>
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-xs text-gray-500 w-36 shrink-0">ID</span>
            <div className="flex items-center gap-1.5">
              <span className="text-sm text-gray-900 font-mono">{customer.id}</span>
              <CopyButton value={customer.id} field="props-id" />
            </div>
          </div>
        </div>
      </div>

      {/* Billing Profile */}
      <div className="bg-white rounded-xl shadow-card mt-6">
        {billingProfile ? (
          <>
            <div className="px-6 py-4 border-b border-gray-100 flex justify-between items-center">
              <h2 className="text-sm font-semibold text-gray-900">Billing Profile</h2>
              <button
                onClick={() => setShowEditBilling(true)}
                className="inline-flex items-center gap-1.5 px-3 py-1.5 border border-gray-300 text-gray-700 rounded-lg text-xs font-medium hover:bg-gray-50 hover:border-gray-400 transition-colors"
              >
                <Pencil size={12} />
                Edit
              </button>
            </div>
            <div className="px-6 py-5">
              {/* Contact & Legal */}
              <div className="grid grid-cols-1 lg:grid-cols-3 gap-x-8 gap-y-4">
                <div>
                  <p className="text-xs font-medium text-gray-400 uppercase tracking-wider">Legal Name</p>
                  <p className="text-sm text-gray-900 mt-1">{billingProfile.legal_name || '\u2014'}</p>
                </div>
                <div>
                  <p className="text-xs font-medium text-gray-400 uppercase tracking-wider">Email</p>
                  <p className="text-sm text-gray-900 mt-1">{billingProfile.email || '\u2014'}</p>
                </div>
                <div>
                  <p className="text-xs font-medium text-gray-400 uppercase tracking-wider">Phone</p>
                  <p className="text-sm text-gray-900 mt-1">{billingProfile.phone || '\u2014'}</p>
                </div>
              </div>

              {/* Address */}
              <div className="mt-5 pt-5 border-t border-gray-100">
                <p className="text-xs font-medium text-gray-400 uppercase tracking-wider mb-2">Address</p>
                {billingProfile.address_line1 || billingProfile.city ? (
                  <div className="text-sm text-gray-900 leading-relaxed">
                    {billingProfile.address_line1 && <p>{billingProfile.address_line1}</p>}
                    {billingProfile.address_line2 && <p>{billingProfile.address_line2}</p>}
                    <p>
                      {[billingProfile.city, billingProfile.state].filter(Boolean).join(', ')}
                      {billingProfile.postal_code && ` ${billingProfile.postal_code}`}
                    </p>
                    {billingProfile.country && <p>{billingProfile.country}</p>}
                  </div>
                ) : (
                  <p className="text-sm text-gray-400">No address on file</p>
                )}
              </div>

              {/* Tax & Currency */}
              <div className="mt-5 pt-5 border-t border-gray-100 grid grid-cols-1 lg:grid-cols-3 gap-x-8 gap-y-4">
                <div>
                  <p className="text-xs font-medium text-gray-400 uppercase tracking-wider">Tax ID</p>
                  <p className="text-sm text-gray-900 mt-1 font-mono">{billingProfile.tax_identifier || '\u2014'}</p>
                </div>
                <div>
                  <p className="text-xs font-medium text-gray-400 uppercase tracking-wider">Currency</p>
                  <p className="text-sm text-gray-900 mt-1">{(billingProfile.currency || '\u2014').toUpperCase()}</p>
                </div>
              </div>
            </div>
          </>
        ) : (
          <>
            <div className="px-6 py-4 border-b border-gray-100">
              <h2 className="text-sm font-semibold text-gray-900">Billing Profile</h2>
            </div>
            <div className="px-6 py-10 text-center">
              <div className="w-10 h-10 rounded-full bg-gray-100 flex items-center justify-center mx-auto mb-3">
                <CreditCard size={18} className="text-gray-400" />
              </div>
              <p className="text-sm font-medium text-gray-900">No billing profile</p>
              <p className="text-xs text-gray-400 mt-1 max-w-xs mx-auto">Set up billing details to enable invoicing and payments for this customer</p>
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
              className="px-3 py-1.5 bg-velox-600 text-white rounded-lg text-xs font-medium hover:bg-velox-700 shadow-sm transition-colors"
            >
              + Add
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
              <p className="px-6 py-6 text-sm text-gray-400 text-center">No active subscriptions</p>
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
              <Link key={inv.id} to={`/invoices/${inv.id}`} className="flex items-center justify-between px-6 py-3 hover:bg-gray-50 transition-colors">
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

  const fieldRules = useMemo(() => ({
    display_name: [rules.required('Display name')],
    email: [rules.email()],
  }), [])
  const { onBlur, validateAll, fieldError, registerRef } = useFormValidation(fieldRules)

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!validateAll(form)) return
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
      <form onSubmit={handleSubmit} noValidate className="space-y-3">
        <FormField label="Display Name" required value={form.display_name} maxLength={255}
          ref={registerRef('display_name')} error={fieldError('display_name')}
          onChange={e => setForm(f => ({ ...f, display_name: e.target.value }))}
          onBlur={() => onBlur('display_name', form.display_name)} />
        <FormField label="Email" type="email" value={form.email} maxLength={254}
          ref={registerRef('email')} error={fieldError('email')}
          onChange={e => setForm(f => ({ ...f, email: e.target.value }))}
          onBlur={() => onBlur('email', form.email)} />
        {error && <p className="text-sm text-red-600 bg-red-50 border border-red-200 rounded-lg px-3 py-2">{error}</p>}
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

  const fieldRules = useMemo(() => ({
    email: [rules.email()],
    phone: [rules.phone()],
  }), [])
  const { onBlur, validateAll, fieldError, registerRef } = useFormValidation(fieldRules)

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!validateAll(form)) return
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

  return (
    <Modal open onClose={onClose} title="Billing Profile" wide>
      <form onSubmit={handleSubmit} noValidate className="max-h-[70vh] overflow-y-auto -mx-6 px-6 pb-1">
        {/* Contact */}
        <div className="space-y-3">
          <FormField label="Legal Name" value={form.legal_name} maxLength={255} placeholder="Acme Corporation Inc."
            onChange={e => setForm(f => ({ ...f, legal_name: e.target.value }))} />
          <div className="grid grid-cols-2 gap-4">
            <FormField label="Email" type="email" value={form.email} maxLength={254} placeholder="billing@acme.com"
              ref={registerRef('email')} error={fieldError('email')}
              onChange={e => setForm(f => ({ ...f, email: e.target.value }))}
              onBlur={() => onBlur('email', form.email)} />
            <FormField label="Phone" type="tel" value={form.phone} placeholder="+1 (555) 123-4567" maxLength={20}
              ref={registerRef('phone')} error={fieldError('phone')}
              onChange={e => setForm(f => ({ ...f, phone: e.target.value }))}
              onBlur={() => onBlur('phone', form.phone)} />
          </div>
        </div>

        {/* Address */}
        <div className="space-y-3 mt-5">
          <FormSelect label="Country" value={form.country}
            onChange={e => setForm(f => ({ ...f, country: e.target.value, state: '' }))}
            placeholder="Select country..."
            options={[['US', 'United States'], ['CA', 'Canada'], ['GB', 'United Kingdom'], ['DE', 'Germany'], ['FR', 'France'], ['IN', 'India'], ['JP', 'Japan'], ['AU', 'Australia'], ['BR', 'Brazil'], ['MX', 'Mexico'], ['SG', 'Singapore'], ['NL', 'Netherlands'], ['SE', 'Sweden'], ['CH', 'Switzerland']].map(([code, name]) => ({ value: code, label: `${name} (${code})` }))} />
          <FormField label="Address" value={form.address_line1} maxLength={200} placeholder="123 Main Street"
            onChange={e => setForm(f => ({ ...f, address_line1: e.target.value }))} />
          <FormField label="Address Line 2" value={form.address_line2} maxLength={200} placeholder="Suite 100, Floor 2"
            onChange={e => setForm(f => ({ ...f, address_line2: e.target.value }))} />
          <div className="grid grid-cols-3 gap-4">
            <FormField label="City" value={form.city} maxLength={100} placeholder="San Francisco"
              onChange={e => setForm(f => ({ ...f, city: e.target.value }))} />
            {form.country === 'US' ? (
              <FormSelect label="State" value={form.state}
                onChange={e => setForm(f => ({ ...f, state: e.target.value }))}
                placeholder="Select..."
                options={[
                  'AL','AK','AZ','AR','CA','CO','CT','DE','FL','GA','HI','ID','IL','IN','IA','KS','KY','LA','ME','MD',
                  'MA','MI','MN','MS','MO','MT','NE','NV','NH','NJ','NM','NY','NC','ND','OH','OK','OR','PA','RI','SC',
                  'SD','TN','TX','UT','VT','VA','WA','WV','WI','WY','DC'
                ].map(s => ({ value: s, label: s }))} />
            ) : form.country === 'CA' ? (
              <FormSelect label="Province" value={form.state}
                onChange={e => setForm(f => ({ ...f, state: e.target.value }))}
                placeholder="Select..."
                options={['AB','BC','MB','NB','NL','NS','NT','NU','ON','PE','QC','SK','YT'].map(s => ({ value: s, label: s }))} />
            ) : form.country === 'IN' ? (
              <FormSelect label="State" value={form.state}
                onChange={e => setForm(f => ({ ...f, state: e.target.value }))}
                placeholder="Select..."
                options={[
                  'AN','AP','AR','AS','BR','CH','CT','DD','DL','GA','GJ','HP','HR','JH','JK','KA','KL','LA','LD',
                  'MH','ML','MN','MP','MZ','NL','OD','PB','PY','RJ','SK','TG','TN','TR','UK','UP','WB'
                ].map(s => ({ value: s, label: s }))} />
            ) : (
              <FormField label="State / Province" value={form.state} placeholder={form.country === 'GB' ? 'London' : 'State'} maxLength={50}
                onChange={e => setForm(f => ({ ...f, state: e.target.value }))} />
            )}
            <FormField label="Postal Code" value={form.postal_code} placeholder={form.country === 'US' ? '94105' : form.country === 'GB' ? 'SW1A 1AA' : form.country === 'IN' ? '400001' : '10001'} maxLength={10}
              onChange={e => setForm(f => ({ ...f, postal_code: e.target.value }))} />
          </div>
        </div>

        {/* Billing */}
        <div className="space-y-3 mt-5">
          <div className="grid grid-cols-2 gap-4">
            <FormField label="Tax ID" value={form.tax_identifier} maxLength={30} placeholder="VAT / EIN / GST number" mono
              onChange={e => setForm(f => ({ ...f, tax_identifier: e.target.value }))} />
            <FormSelect label="Currency" value={form.currency}
              onChange={e => setForm(f => ({ ...f, currency: e.target.value }))}
              placeholder="Select currency..."
              options={['USD', 'EUR', 'GBP', 'CAD', 'AUD', 'JPY', 'INR', 'CHF'].map(c => ({ value: c, label: c }))} />
          </div>
        </div>

        {error && <p className="text-sm text-red-600 bg-red-50 border border-red-200 rounded-lg px-3 py-2 mt-4">{error}</p>}
        <div className="flex justify-end gap-3 pt-4 border-t border-gray-100">
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

  const fieldRules = useMemo(() => ({
    display_name: [rules.required('Display name')],
    code: [rules.required('Code'), rules.slug()],
    plan_id: [rules.required('Plan')],
  }), [])
  const { onBlur, validateAll, fieldError, registerRef } = useFormValidation(fieldRules)

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!validateAll(form)) return
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
      <form onSubmit={handleSubmit} noValidate className="space-y-3">
        <FormField label="Display Name" required value={form.display_name} placeholder="Pro Monthly" maxLength={255}
          ref={registerRef('display_name')} error={fieldError('display_name')}
          onChange={e => setForm(f => ({ ...f, display_name: e.target.value }))}
          onBlur={() => onBlur('display_name', form.display_name)} />
        <FormField label="Code" required value={form.code} placeholder="pro-monthly" maxLength={100} mono
          ref={registerRef('code')} error={fieldError('code')}
          onChange={e => setForm(f => ({ ...f, code: e.target.value }))}
          onBlur={() => onBlur('code', form.code)} />
        <FormSelect label="Plan" required value={form.plan_id} error={fieldError('plan_id')}
          onChange={e => { setForm(f => ({ ...f, plan_id: e.target.value })); onBlur('plan_id', e.target.value) }}
          placeholder="Select plan..."
          options={plans.map(p => ({ value: p.id, label: `${p.name} (${p.code})` }))} />
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={form.start_now} onChange={e => setForm(f => ({ ...f, start_now: e.target.checked }))} />
          Start immediately (activate + set billing period)
        </label>
        {error && <p className="text-sm text-red-600 bg-red-50 border border-red-200 rounded-lg px-3 py-2">{error}</p>}
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
