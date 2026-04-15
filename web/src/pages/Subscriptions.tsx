import { useEffect, useState, useMemo, useCallback } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { api, formatDate, formatDateTime, type Subscription, type Customer, type Plan } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'
import { Modal } from '@/components/Modal'
import { FormField } from '@/components/FormField'
import { FormSelect } from '@/components/FormField'
import { SearchSelect } from '@/components/SearchSelect'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { ErrorState } from '@/components/ErrorState'
import { useToast } from '@/components/Toast'
import { useFormValidation, rules } from '@/hooks/useFormValidation'
import { Plus, Search, Download } from 'lucide-react'
import { downloadCSV } from '@/lib/csv'
import { Pagination } from '@/components/Pagination'

const PAGE_SIZE = 25

export function SubscriptionsPage() {
  const [subs, setSubs] = useState<Subscription[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [showCreate, setShowCreate] = useState(false)
  const [customers, setCustomers] = useState<Customer[]>([])
  const [customerMap, setCustomerMap] = useState<Record<string, Customer>>({})
  const [plans, setPlans] = useState<Plan[]>([])
  const [search, setSearch] = useState('')
  const [filterStatus, setFilterStatus] = useState('')
  const [page, setPage] = useState(1)
  const toast = useToast()
  const navigate = useNavigate()

  // Server-side paginated fetch
  const loadSubs = useCallback(() => {
    setLoading(true)
    setError(null)
    const params = new URLSearchParams()
    params.set('limit', String(PAGE_SIZE))
    params.set('offset', String((page - 1) * PAGE_SIZE))
    if (filterStatus) params.set('status', filterStatus)
    api.listSubscriptions(params.toString()).then(res => { setSubs(res.data); setTotal(res.total); setLoading(false) })
      .catch(err => { setError(err instanceof Error ? err.message : 'Failed to load subscriptions'); setLoading(false) })
  }, [page, filterStatus])

  useEffect(() => { loadSubs() }, [loadSubs])

  // Load reference data (customers/plans) once
  useEffect(() => {
    api.listCustomers().then(res => {
      setCustomers(res.data)
      const cMap: Record<string, Customer> = {}
      res.data.forEach(c => { cMap[c.id] = c })
      setCustomerMap(cMap)
    })
    api.listPlans().then(res => setPlans(res.data))
  }, [])

  // Client-side search on current page data
  const filtered = useMemo(() => {
    return subs.filter(sub => {
      if (!search) return true
      const q = search.toLowerCase()
      return sub.display_name.toLowerCase().includes(q) || sub.code.toLowerCase().includes(q)
    })
  }, [subs, search])

  const totalPages = Math.ceil(total / PAGE_SIZE)

  return (
    <Layout>
      <div className="flex justify-between items-center">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900 dark:text-gray-100">Subscriptions</h1>
          <p className="text-sm text-gray-600 dark:text-gray-400 mt-1">{total} total</p>
        </div>
        <div className="flex items-center gap-2">
          {total > 0 && (
            <button
              onClick={() => {
                // Export fetches all data for CSV
                api.listSubscriptions().then(res => {
                  const rows = res.data.map(sub => [
                    sub.display_name,
                    customerMap[sub.customer_id]?.display_name || 'Unknown',
                    sub.code,
                    sub.status,
                    sub.current_billing_period_start && sub.current_billing_period_end
                      ? `${formatDate(sub.current_billing_period_start)} - ${formatDate(sub.current_billing_period_end)}`
                      : '',
                    formatDateTime(sub.created_at),
                  ])
                  downloadCSV('subscriptions.csv', ['Name', 'Customer', 'Code', 'Status', 'Billing Period', 'Created'], rows)
                })
              }}
              className="flex items-center gap-2 px-4 py-2 border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-300 rounded-lg text-sm font-medium hover:bg-gray-50 dark:hover:bg-gray-800 shadow-sm transition-colors"
            >
              <Download size={16} />
              Export CSV
            </button>
          )}
          {total > 0 && (
            <div className="flex gap-1 bg-gray-100 dark:bg-gray-800 rounded-lg p-1">
              {[
                { value: '', label: 'All' },
                { value: 'active', label: 'Active' },
                { value: 'paused', label: 'Paused' },
                { value: 'canceled', label: 'Canceled' },
                { value: 'draft', label: 'Draft' },
              ].map(f => (
                <button key={f.value} onClick={() => { setFilterStatus(f.value); setPage(1) }}
                  className={`px-3 py-1.5 rounded-md text-xs font-medium transition-colors ${
                    filterStatus === f.value ? 'bg-white dark:bg-gray-700 text-gray-900 dark:text-gray-100 shadow-sm' : 'text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-200'
                  }`}>
                  {f.label}
                </button>
              ))}
            </div>
          )}
          {total > 0 && (
            <button onClick={() => setShowCreate(true)}
              className="flex items-center gap-2 px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow transition-colors">
              <Plus size={16} /> Add Subscription
            </button>
          )}
        </div>
      </div>

      {/* Search */}
      {total > 0 && (
        <div className="relative mt-6">
          <Search size={16} className="absolute left-3 top-2.5 text-gray-400" />
          <input
            type="text"
            value={search}
            onChange={e => setSearch(e.target.value)}
            placeholder="Search within page..."
            className="w-full pl-9 pr-4 py-2 border border-gray-200 dark:border-gray-700 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-velox-500 focus:border-transparent bg-white dark:bg-gray-800 dark:text-gray-100"
          />
          {search && <span className="absolute right-3 top-2.5 text-xs text-gray-400">Filtering within current page</span>}
        </div>
      )}

      <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card mt-4">
        {error ? (
          <ErrorState message={error} onRetry={loadSubs} />
        ) : loading ? (
          <LoadingSkeleton rows={5} columns={6} />
        ) : total === 0 ? (
          <div className="p-12 text-center">
            <p className="text-sm text-gray-900 dark:text-gray-100">No subscriptions yet</p>
            <p className="text-sm text-gray-600 dark:text-gray-400 mt-1">Create a subscription to start billing a customer</p>
            <button onClick={() => setShowCreate(true)}
              className="mt-4 inline-flex items-center gap-2 px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm transition-colors">
              <Plus size={16} /> Add Subscription
            </button>
          </div>
        ) : filtered.length === 0 ? (
          <p className="px-6 py-8 text-sm text-gray-400 text-center">No subscriptions match search on this page</p>
        ) : (
          <>
          <div className="overflow-x-auto">
          <table className="w-full">
            <thead>
              <tr className="border-b border-gray-100 dark:border-gray-800 bg-gray-50 dark:bg-gray-800/50">
                <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Name</th>
                <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Customer</th>
                <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Plan</th>
                <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Code</th>
                <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Status</th>
                <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Next Billing</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
              {filtered.map(sub => (
                <tr key={sub.id} className="hover:bg-gray-50 dark:hover:bg-gray-800/50 cursor-pointer transition-colors group" onClick={(e) => {
                  const target = e.target as HTMLElement
                  if (target.closest('button, a, input, select')) return
                  navigate(`/subscriptions/${sub.id}`)
                }}>
                  <td className="px-6 py-3">
                    <Link to={`/subscriptions/${sub.id}`} className="text-sm font-medium text-gray-900 dark:text-gray-100 group-hover:text-velox-600 transition-colors">
                      {sub.display_name}
                    </Link>
                  </td>
                  <td className="px-6 py-3 text-sm text-gray-600 dark:text-gray-400">
                    {customerMap[sub.customer_id]?.display_name || 'Unknown'}
                  </td>
                  <td className="px-6 py-3 text-sm text-gray-600 dark:text-gray-400">{plans.find(p => p.id === sub.plan_id)?.name || '\u2014'}</td>
                  <td className="px-6 py-3 text-sm text-gray-600 dark:text-gray-400 font-mono">{sub.code}</td>
                  <td className="px-6 py-3"><Badge status={sub.status} /></td>
                  <td className="px-6 py-3 text-sm text-gray-600 dark:text-gray-400">
                    {sub.next_billing_at ? formatDate(sub.next_billing_at) : '\u2014'}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          </div>
          <Pagination page={page} totalPages={totalPages} onPageChange={setPage} />
          </>
        )}
      </div>

      {showCreate && (
        <CreateSubscriptionModal
          onClose={() => setShowCreate(false)}
          customers={customers}
          plans={plans}
          onCreated={(sub) => {
            setShowCreate(false)
            toast.success(`Subscription "${sub.display_name}" created`)
            loadSubs()
          }}
        />
      )}
    </Layout>
  )
}

function CreateSubscriptionModal({ onClose, onCreated, customers, plans }: {
  onClose: () => void; onCreated: (sub: Subscription) => void; customers: Customer[]; plans: Plan[]
}) {
  const [form, setForm] = useState({
    code: '', display_name: '', customer_id: '', plan_id: '', start_now: true,
    billing_time: 'calendar', trial_days: '' as string,
    usage_cap_units: '' as string, overage_action: 'charge',
  })
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)

  const fieldRules = useMemo(() => ({
    display_name: [rules.required('Display name')],
    code: [rules.required('Code'), rules.slug()],
    customer_id: [rules.required('Customer')],
    plan_id: [rules.required('Plan')],
  }), [])
  const { onBlur, validateAll, fieldError, registerRef } = useFormValidation(fieldRules)

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!validateAll(form)) return
    setSaving(true); setError('')
    try {
      const sub = await api.createSubscription({
        code: form.code,
        display_name: form.display_name,
        customer_id: form.customer_id,
        plan_id: form.plan_id,
        start_now: form.start_now,
        billing_time: form.billing_time,
        ...(form.trial_days ? { trial_days: parseInt(form.trial_days) } : {}),
        ...(form.usage_cap_units ? { usage_cap_units: parseInt(form.usage_cap_units) } : {}),
        ...(form.overage_action !== 'charge' ? { overage_action: form.overage_action } : {}),
      })
      onCreated(sub)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create subscription')
    } finally {
      setSaving(false)
    }
  }

  return (
    <Modal open onClose={onClose} title="Create Subscription" dirty={!!(form.display_name || form.code)}>
      <form onSubmit={handleSubmit} noValidate className="space-y-3">
        <FormField label="Display Name" required value={form.display_name} placeholder="Acme Pro Monthly" maxLength={255}
          ref={registerRef('display_name')} error={fieldError('display_name')}
          onChange={e => setForm(f => ({ ...f, display_name: e.target.value }))}
          onBlur={() => onBlur('display_name', form.display_name)} />
        <FormField label="Code" required value={form.code} placeholder="acme-pro" maxLength={100} mono
          ref={registerRef('code')} error={fieldError('code')}
          onChange={e => setForm(f => ({ ...f, code: e.target.value }))}
          onBlur={() => onBlur('code', form.code)}
          hint="Only letters, numbers, hyphens, and underscores" />
        <SearchSelect label="Customer" required value={form.customer_id} placeholder="Select customer..."
          error={fieldError('customer_id')}
          onChange={(v) => { setForm(f => ({ ...f, customer_id: v })); onBlur('customer_id', v) }}
          options={customers.map(c => ({ value: c.id, label: c.display_name, sublabel: c.external_id }))} />
        <SearchSelect label="Plan" required value={form.plan_id} placeholder="Select plan..."
          error={fieldError('plan_id')}
          onChange={(v) => { setForm(f => ({ ...f, plan_id: v })); onBlur('plan_id', v) }}
          options={plans.map(p => ({ value: p.id, label: p.name, sublabel: p.code }))} />
        <div className="grid grid-cols-2 gap-3">
          <FormSelect label="Billing Time" value={form.billing_time}
            onChange={e => setForm(f => ({ ...f, billing_time: e.target.value }))}
            options={[
              { value: 'calendar', label: 'Calendar (month start)' },
              { value: 'anniversary', label: 'Anniversary (sub start date)' },
            ]} />
          <FormField label="Trial Days" type="number" min={0} value={form.trial_days}
            onChange={e => setForm(f => ({ ...f, trial_days: e.target.value }))}
            placeholder="0" hint="0 for no trial" />
        </div>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={form.start_now} onChange={e => setForm(f => ({ ...f, start_now: e.target.checked }))} />
          Start immediately (activate + set billing period)
        </label>

        <div className="border-t border-gray-100 dark:border-gray-800 pt-4 mt-2">
          <p className="text-xs font-semibold text-gray-400 uppercase tracking-wider mb-3">Usage Limits</p>
          <div className="grid grid-cols-2 gap-3">
            <FormField label="Usage Cap (units)" type="number" min={0} value={form.usage_cap_units}
              onChange={e => setForm(f => ({ ...f, usage_cap_units: e.target.value }))}
              placeholder="Unlimited" hint="Max units per billing period" />
            <FormSelect label="Over-limit Action" value={form.overage_action}
              onChange={e => setForm(f => ({ ...f, overage_action: e.target.value }))}
              options={[
                { value: 'charge', label: 'Charge overage' },
                { value: 'block', label: 'Cap at limit' },
              ]} />
          </div>
        </div>

        {error && <p className="text-sm text-red-600 bg-red-50 border border-red-200 rounded-lg px-3 py-2">{error}</p>}
        <div className="flex justify-end gap-3 pt-4 border-t border-gray-100 dark:border-gray-800 mt-2">
          <button type="button" onClick={onClose} className="px-4 py-2 border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-300 rounded-lg text-sm font-medium hover:bg-gray-50 dark:hover:bg-gray-800 transition-colors">Cancel</button>
          <button type="submit" disabled={saving}
            className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow disabled:opacity-50">
            {saving ? 'Creating...' : 'Create Subscription'}
          </button>
        </div>
      </form>
    </Modal>
  )
}
