import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, formatCents, formatDate, formatRelativeTime, type Invoice, type Subscription, type Customer, type AuditEntry, type Plan } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { StatCard } from '@/components/StatCard'
import { Badge } from '@/components/Badge'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { ErrorState } from '@/components/ErrorState'

export function DashboardPage() {
  const [stats, setStats] = useState({ customers: 0, subscriptions: 0, invoices: 0, revenue: 0, mrr: 0 })
  const [recentInvoices, setRecentInvoices] = useState<Invoice[]>([])
  const [activeSubs, setActiveSubs] = useState<Subscription[]>([])
  const [customerMap, setCustomerMap] = useState<Record<string, Customer>>({})
  const [recentActivity, setRecentActivity] = useState<AuditEntry[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [billingResult, setBillingResult] = useState<string | null>(null)
  const [runningBilling, setRunningBilling] = useState(false)

  const loadDashboard = async () => {
    setLoading(true)
    setError(null)
    try {
      const [customers, invoices, subs, auditRes, plansRes] = await Promise.all([
        api.listCustomers(),
        api.listInvoices('limit=10'),
        api.listSubscriptions('status=active'),
        api.listAuditLog('limit=8').catch(() => ({ data: [] })),
        api.listPlans().catch(() => ({ data: [] as Plan[] })),
      ])
      setRecentActivity(auditRes.data || [])

      const cMap: Record<string, Customer> = {}
      customers.data.forEach(c => { cMap[c.id] = c })
      setCustomerMap(cMap)

      const revenue = invoices.data
        .filter(i => i.status === 'paid')
        .reduce((sum, i) => sum + i.total_amount_cents, 0)

      const planMap: Record<string, Plan> = {}
      plansRes.data.forEach(p => { planMap[p.id] = p })
      let mrr = 0
      subs.data.forEach(sub => {
        const plan = planMap[sub.plan_id]
        if (plan) {
          mrr += plan.billing_interval === 'yearly'
            ? Math.round(plan.base_amount_cents / 12)
            : plan.base_amount_cents
        }
      })

      setStats({
        customers: customers.total,
        subscriptions: subs.data.length,
        invoices: invoices.total,
        revenue,
        mrr,
      })
      setRecentInvoices(invoices.data.slice(0, 5))
      setActiveSubs(subs.data.slice(0, 5))
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load dashboard')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => { loadDashboard() }, [])

  const handleTriggerBilling = async () => {
    setRunningBilling(true)
    setBillingResult(null)
    try {
      const res = await api.triggerBilling()
      setBillingResult(`Generated ${res.invoices_generated} invoice(s)`)
      const invoices = await api.listInvoices('limit=10')
      const revenue = invoices.data.filter(i => i.status === 'paid').reduce((s, i) => s + i.total_amount_cents, 0)
      setStats(prev => ({ ...prev, invoices: invoices.total, revenue }))
      setRecentInvoices(invoices.data.slice(0, 5))
    } catch (err) {
      setBillingResult('Failed: ' + (err instanceof Error ? err.message : 'unknown error'))
    } finally {
      setRunningBilling(false)
    }
  }

  return (
    <Layout>
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900">Dashboard</h1>
          <p className="text-sm text-gray-500 mt-1">Billing overview</p>
        </div>
        <div className="flex items-center gap-3">
          {billingResult && (
            <span className="text-xs text-gray-500">{billingResult}</span>
          )}
          <button
            onClick={handleTriggerBilling}
            disabled={runningBilling}
            title="Generate invoices for subscriptions due for billing"
            className="px-4 py-2 border border-gray-300 text-gray-700 rounded-lg text-sm font-medium hover:bg-gray-50 shadow-sm disabled:opacity-50 transition-colors"
          >
            {runningBilling ? 'Generating...' : 'Generate Invoices'}
          </button>
        </div>
      </div>

      {error ? (
        <div className="bg-white rounded-xl shadow-card mt-6">
          <ErrorState message={error} onRetry={loadDashboard} />
        </div>
      ) : loading ? (
        <div className="bg-white rounded-xl shadow-card mt-6">
          <LoadingSkeleton rows={6} columns={4} />
        </div>
      ) : (
        <>
          <div className="grid grid-cols-2 lg:grid-cols-5 gap-4 mt-6">
            <StatCard title="Customers" value={String(stats.customers)} />
            <StatCard title="Active Subscriptions" value={String(stats.subscriptions)} />
            <StatCard title="Total Invoices" value={String(stats.invoices)} />
            <StatCard title="Revenue" value={formatCents(stats.revenue)} subtitle="Paid invoices" />
            <StatCard title="MRR" value={formatCents(stats.mrr)} subtitle="Monthly recurring" />
          </div>

          {stats.customers === 0 && (
            <div className="bg-velox-50 border border-velox-100 rounded-xl p-6 mt-6">
              <h3 className="text-sm font-semibold text-velox-900">Get Started</h3>
              <p className="text-sm text-velox-700 mt-1">Set up your billing in 3 steps:</p>
              <ol className="mt-3 space-y-2 text-sm text-velox-700">
                <li className="flex items-center gap-2">
                  <span className="w-5 h-5 rounded-full bg-velox-600 text-white flex items-center justify-center text-xs font-bold">1</span>
                  <Link to="/pricing" className="hover:underline">Configure pricing</Link> — create meters, rating rules, and plans
                </li>
                <li className="flex items-center gap-2">
                  <span className="w-5 h-5 rounded-full bg-velox-600 text-white flex items-center justify-center text-xs font-bold">2</span>
                  <Link to="/customers" className="hover:underline">Add customers</Link> — create your first customer and subscription
                </li>
                <li className="flex items-center gap-2">
                  <span className="w-5 h-5 rounded-full bg-velox-600 text-white flex items-center justify-center text-xs font-bold">3</span>
                  Ingest usage events via the API, then run a billing cycle
                </li>
              </ol>
            </div>
          )}

          <div className="grid grid-cols-1 lg:grid-cols-2 gap-6 mt-8">
            {/* Recent invoices */}
            <div className="bg-white rounded-xl shadow-card">
              <div className="px-6 py-4 border-b border-gray-100 flex justify-between items-center">
                <h2 className="text-sm font-semibold text-gray-900">Recent Invoices</h2>
                <Link to="/invoices" className="text-xs text-velox-600 hover:underline">View all</Link>
              </div>
              <div className="divide-y divide-gray-50">
                {recentInvoices.map(inv => (
                  <Link key={inv.id} to={`/invoices/${inv.id}`} className="flex items-center justify-between px-6 py-3 hover:bg-gray-50 transition-colors">
                    <div>
                      <p className="text-sm font-medium text-gray-900">{inv.invoice_number}</p>
                      <p className="text-xs text-gray-400">
                        {customerMap[inv.customer_id]?.display_name || 'Unknown customer'} &middot; {formatDate(inv.created_at)}
                      </p>
                    </div>
                    <div className="flex items-center gap-3">
                      <Badge status={inv.status} />
                      <span className="text-sm font-medium text-gray-900">{formatCents(inv.total_amount_cents)}</span>
                    </div>
                  </Link>
                ))}
                {recentInvoices.length === 0 && (
                  <p className="px-6 py-4 text-sm text-gray-400">No invoices yet</p>
                )}
              </div>
            </div>

            {/* Active subscriptions */}
            <div className="bg-white rounded-xl shadow-card">
              <div className="px-6 py-4 border-b border-gray-100 flex justify-between items-center">
                <h2 className="text-sm font-semibold text-gray-900">Active Subscriptions</h2>
                <Link to="/subscriptions" className="text-xs text-velox-600 hover:underline">View all</Link>
              </div>
              <div className="divide-y divide-gray-50">
                {activeSubs.map(sub => (
                  <Link key={sub.id} to={`/subscriptions/${sub.id}`} className="flex items-center justify-between px-6 py-3 hover:bg-gray-50 transition-colors">
                    <div>
                      <p className="text-sm font-medium text-gray-900">{sub.display_name}</p>
                      <p className="text-xs text-gray-400">
                        {customerMap[sub.customer_id]?.display_name || 'Unknown customer'}
                      </p>
                    </div>
                    <Badge status={sub.status} />
                  </Link>
                ))}
                {activeSubs.length === 0 && (
                  <p className="px-6 py-4 text-sm text-gray-400">No active subscriptions</p>
                )}
              </div>
            </div>
          </div>

          {/* Recent Activity */}
          <div className="bg-white rounded-xl shadow-card mt-8">
            <div className="px-6 py-4 border-b border-gray-100 flex justify-between items-center">
              <h2 className="text-sm font-semibold text-gray-900">Recent Activity</h2>
              <Link to="/audit-log" className="text-xs text-velox-600 hover:underline">View all</Link>
            </div>
            <div className="divide-y divide-gray-50">
              {recentActivity.length > 0 ? recentActivity.map(entry => {
                const dotColor = entry.action.startsWith('create')
                  ? 'bg-emerald-500'
                  : entry.action.startsWith('update')
                  ? 'bg-blue-500'
                  : entry.action.startsWith('delete') || entry.action.startsWith('revoke') || entry.action.startsWith('void')
                  ? 'bg-red-500'
                  : 'bg-gray-400'

                const actionLabel = entry.action.charAt(0).toUpperCase() + entry.action.slice(1)
                const resourceLabel = entry.resource_type.replace(/_/g, ' ')

                return (
                  <div key={entry.id} className="flex items-center gap-3 px-6 py-3">
                    <span className={`w-2 h-2 rounded-full flex-shrink-0 ${dotColor}`} />
                    <span className="text-sm text-gray-700">
                      {actionLabel}d {resourceLabel}
                    </span>
                    <span className="text-xs text-gray-400 ml-auto">{formatRelativeTime(entry.created_at)}</span>
                  </div>
                )
              }) : (
                <p className="px-6 py-4 text-sm text-gray-400">No recent activity</p>
              )}
            </div>
          </div>

        </>
      )}
    </Layout>
  )
}
