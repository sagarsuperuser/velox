import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, formatDate, type Subscription, type Customer, type Plan } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'
import { Modal } from '@/components/Modal'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { ErrorState } from '@/components/ErrorState'
import { useToast } from '@/components/Toast'
import { Plus, Search } from 'lucide-react'

export function SubscriptionsPage() {
  const [subs, setSubs] = useState<Subscription[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [showCreate, setShowCreate] = useState(false)
  const [customers, setCustomers] = useState<Customer[]>([])
  const [customerMap, setCustomerMap] = useState<Record<string, Customer>>({})
  const [plans, setPlans] = useState<Plan[]>([])
  const [search, setSearch] = useState('')
  const toast = useToast()

  const loadSubs = () => {
    setLoading(true)
    setError(null)
    api.listSubscriptions().then(res => { setSubs(res.data); setLoading(false) })
      .catch(err => { setError(err instanceof Error ? err.message : 'Failed to load subscriptions'); setLoading(false) })
  }

  useEffect(() => {
    loadSubs()
    api.listCustomers().then(res => {
      setCustomers(res.data)
      const cMap: Record<string, Customer> = {}
      res.data.forEach(c => { cMap[c.id] = c })
      setCustomerMap(cMap)
    })
    api.listPlans().then(res => setPlans(res.data))
  }, [])

  return (
    <Layout>
      <div className="flex justify-between items-center">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900">Subscriptions</h1>
          <p className="text-sm text-gray-500 mt-1">{subs.length} total</p>
        </div>
        <button onClick={() => setShowCreate(true)}
          className="flex items-center gap-2 px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow transition-colors">
          <Plus size={16} /> Add Subscription
        </button>
      </div>

      {/* Search */}
      {subs.length > 0 && (
        <div className="relative mt-6">
          <Search size={16} className="absolute left-3 top-2.5 text-gray-400" />
          <input
            type="text"
            value={search}
            onChange={e => setSearch(e.target.value)}
            placeholder="Search subscriptions..."
            className="w-full pl-9 pr-4 py-2 border border-gray-200 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-velox-500 focus:border-transparent bg-white"
          />
        </div>
      )}

      <div className="bg-white rounded-xl shadow-card mt-4">
        {error ? (
          <ErrorState message={error} onRetry={loadSubs} />
        ) : loading ? (
          <LoadingSkeleton rows={5} columns={6} />
        ) : subs.length === 0 ? (
          <div className="p-12 text-center">
            <p className="text-gray-400 text-sm">No subscriptions yet</p>
            <button onClick={() => setShowCreate(true)} className="mt-3 text-sm text-velox-600 hover:underline">
              Create your first subscription
            </button>
          </div>
        ) : (
          <div className="overflow-x-auto">
          <table className="w-full">
            <thead>
              <tr className="border-b border-gray-100">
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Name</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Customer</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Code</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Status</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Billing Period</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Created</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-50">
              {subs.filter(sub => {
                if (!search) return true
                const q = search.toLowerCase()
                return sub.display_name.toLowerCase().includes(q) || sub.code.toLowerCase().includes(q)
              }).map(sub => (
                <tr key={sub.id} className="hover:bg-gray-50 transition-colors">
                  <td className="px-6 py-3">
                    <Link to={`/subscriptions/${sub.id}`} className="text-sm font-medium text-gray-900 hover:text-velox-600">
                      {sub.display_name}
                    </Link>
                  </td>
                  <td className="px-6 py-3 text-sm text-gray-600">
                    {customerMap[sub.customer_id]?.display_name || 'Unknown'}
                  </td>
                  <td className="px-6 py-3 text-sm text-gray-500 font-mono">{sub.code}</td>
                  <td className="px-6 py-3"><Badge status={sub.status} /></td>
                  <td className="px-6 py-3 text-sm text-gray-500">
                    {sub.current_billing_period_start && sub.current_billing_period_end
                      ? `${formatDate(sub.current_billing_period_start)} — ${formatDate(sub.current_billing_period_end)}`
                      : '\u2014'}
                  </td>
                  <td className="px-6 py-3 text-sm text-gray-400">{formatDate(sub.created_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
          </div>
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
  })
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setSaving(true); setError('')
    try {
      const sub = await api.createSubscription(form)
      onCreated(sub)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed')
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
            placeholder="Acme Pro Monthly" required />
        </div>
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">Code <span className="text-red-500">*</span></label>
          <input type="text" value={form.code} onChange={e => setForm(f => ({ ...f, code: e.target.value }))}
            className="w-full px-3 py-2 border border-gray-200 rounded-lg shadow-sm text-sm focus:outline-none focus:ring-2 focus:ring-velox-500 font-mono"
            placeholder="acme-pro" required />
        </div>
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">Customer <span className="text-red-500">*</span></label>
          <select value={form.customer_id} onChange={e => setForm(f => ({ ...f, customer_id: e.target.value }))}
            className="w-full px-3 py-2 border border-gray-200 rounded-lg shadow-sm text-sm focus:outline-none focus:ring-2 focus:ring-velox-500 bg-white" required>
            <option value="">Select customer...</option>
            {customers.map(c => <option key={c.id} value={c.id}>{c.display_name} ({c.external_id})</option>)}
          </select>
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
        <div className="flex justify-end gap-3 pt-2">
          <button type="button" onClick={onClose} className="px-4 py-2 text-sm text-gray-600 hover:text-gray-900">Cancel</button>
          <button type="submit" disabled={saving}
            className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow disabled:opacity-50">
            {saving ? 'Creating...' : 'Create Subscription'}
          </button>
        </div>
      </form>
    </Modal>
  )
}
