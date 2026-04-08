import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, formatCents, formatRelativeTime, type Invoice, type Subscription, type Customer, type AuditEntry, type Plan } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { StatCard } from '@/components/StatCard'
import { Badge } from '@/components/Badge'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { ErrorState } from '@/components/ErrorState'
import { AlertTriangle, FileText, Users, CreditCard, CheckCircle, Clock, DollarSign, Settings, Shield } from 'lucide-react'

export function DashboardPage() {
  const [stats, setStats] = useState({ customers: 0, subscriptions: 0, invoices: 0, revenue: 0, mrr: 0 })
  const [attentionItems, setAttentionItems] = useState<Invoice[]>([])
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
        api.listInvoices(),
        api.listSubscriptions('status=active'),
        api.listAuditLog('limit=10').catch(() => ({ data: [] })),
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

      // Needs Attention: failed payments, draft invoices, overdue
      const attention = invoices.data.filter(inv =>
        inv.payment_status === 'failed' ||
        inv.status === 'draft' ||
        (inv.status === 'finalized' && inv.payment_status === 'pending')
      ).slice(0, 5)

      setStats({
        customers: customers.total,
        subscriptions: subs.data.length,
        invoices: invoices.total,
        revenue,
        mrr,
      })
      setAttentionItems(attention)
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
      loadDashboard()
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
            className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm disabled:opacity-50 transition-colors"
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
          {/* KPI Cards */}
          <div className="grid grid-cols-2 lg:grid-cols-5 gap-4 mt-6">
            <StatCard title="Customers" value={String(stats.customers)} />
            <StatCard title="Active Subscriptions" value={String(stats.subscriptions)} />
            <StatCard title="Total Invoices" value={String(stats.invoices)} />
            <StatCard title="Revenue" value={formatCents(stats.revenue)} subtitle="Paid invoices" />
            <StatCard title="MRR" value={formatCents(stats.mrr)} subtitle="Monthly recurring" />
          </div>

          {/* Get Started (only when empty) */}
          {stats.customers === 0 && (
            <div className="bg-velox-50/50 rounded-xl shadow-card p-6 mt-6">
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
                  Ingest usage events via the API, then generate invoices
                </li>
              </ol>
            </div>
          )}

          <div className="grid grid-cols-1 lg:grid-cols-2 gap-6 mt-6">
            {/* Needs Attention */}
            <div className="bg-white rounded-xl shadow-card">
              <div className="px-6 py-4 border-b border-gray-100 flex justify-between items-center">
                <div className="flex items-center gap-2">
                  <AlertTriangle size={15} className={attentionItems.length > 0 ? 'text-amber-500' : 'text-gray-300'} />
                  <h2 className="text-sm font-semibold text-gray-900">Needs Attention</h2>
                  {attentionItems.length > 0 && (
                    <span className="bg-amber-100 text-amber-700 text-xs font-medium px-1.5 py-0.5 rounded-full">
                      {attentionItems.length}
                    </span>
                  )}
                </div>
                <Link to="/invoices" className="text-sm text-gray-500 hover:text-gray-700 transition-colors">View all</Link>
              </div>
              <div className="divide-y divide-gray-50 max-h-[300px] overflow-y-auto">
                {attentionItems.length > 0 ? attentionItems.map(inv => (
                  <Link key={inv.id} to={`/invoices/${inv.id}`} className="flex items-center justify-between px-6 py-3 hover:bg-gray-50/80 transition-colors">
                    <div className="flex items-center gap-3">
                      <div className={`w-8 h-8 rounded-lg flex items-center justify-center ${
                        inv.payment_status === 'failed' ? 'bg-rose-50 text-rose-500' :
                        inv.status === 'draft' ? 'bg-gray-100 text-gray-500' :
                        'bg-amber-50 text-amber-500'
                      }`}>
                        {inv.payment_status === 'failed' ? <AlertTriangle size={14} /> :
                         inv.status === 'draft' ? <FileText size={14} /> :
                         <Clock size={14} />}
                      </div>
                      <div>
                        <p className="text-sm font-medium text-gray-900">{inv.invoice_number}</p>
                        <p className="text-xs text-gray-400">
                          {customerMap[inv.customer_id]?.display_name || 'Unknown'} · {
                            inv.payment_status === 'failed' ? 'Payment failed' :
                            inv.status === 'draft' ? 'Awaiting finalization' :
                            'Awaiting payment'
                          }
                        </p>
                      </div>
                    </div>
                    <div className="flex items-center gap-3">
                      <Badge status={inv.payment_status === 'failed' ? 'failed' : inv.status} />
                      <span className="text-sm font-medium text-gray-900">{formatCents(inv.total_amount_cents)}</span>
                    </div>
                  </Link>
                )) : (
                  <p className="px-6 py-6 text-sm text-gray-400 text-center">No pending issues</p>
                )}
              </div>
            </div>

            {/* Active Subscriptions */}
            <div className="bg-white rounded-xl shadow-card">
              <div className="px-6 py-4 border-b border-gray-100 flex justify-between items-center">
                <h2 className="text-sm font-semibold text-gray-900">Active Subscriptions</h2>
                <Link to="/subscriptions" className="text-sm text-gray-500 hover:text-gray-700 transition-colors">View all</Link>
              </div>
              <div className="divide-y divide-gray-50 max-h-[300px] overflow-y-auto">
                {activeSubs.map(sub => (
                  <Link key={sub.id} to={`/subscriptions/${sub.id}`} className="flex items-center justify-between px-6 py-3 hover:bg-gray-50/80 transition-colors">
                    <div className="flex items-center gap-3">
                      <div className="w-8 h-8 rounded-lg bg-emerald-50 text-emerald-500 flex items-center justify-center">
                        <CreditCard size={14} />
                      </div>
                      <div>
                        <p className="text-sm font-medium text-gray-900">{sub.display_name}</p>
                        <p className="text-xs text-gray-400">
                          {customerMap[sub.customer_id]?.display_name || 'Unknown'}
                        </p>
                      </div>
                    </div>
                    <Badge status={sub.status} />
                  </Link>
                ))}
                {activeSubs.length === 0 && (
                  <p className="px-6 py-8 text-sm text-gray-400 text-center">No active subscriptions</p>
                )}
              </div>
            </div>
          </div>

          {/* Recent Activity — unified timeline */}
          <div className="bg-white rounded-xl shadow-card mt-6">
            <div className="px-6 py-4 border-b border-gray-100 flex justify-between items-center">
              <h2 className="text-sm font-semibold text-gray-900">Recent Activity</h2>
              <Link to="/audit-log" className="text-sm text-gray-500 hover:text-gray-700 transition-colors">View all</Link>
            </div>
            <div className="divide-y divide-gray-50 max-h-[350px] overflow-y-auto">
              {recentActivity.length > 0 ? recentActivity.map(entry => {
                const { icon: Icon, bg, color } = getActivityStyle(entry.action, entry.resource_type)
                const label = formatActivityLabel(entry.action, entry.resource_type)

                return (
                  <div key={entry.id} className="flex items-center gap-3 px-6 py-3">
                    <div className={`w-8 h-8 rounded-lg flex items-center justify-center flex-shrink-0 ${bg}`}>
                      <Icon size={14} className={color} />
                    </div>
                    <div className="flex-1 min-w-0">
                      <p className="text-sm text-gray-700">{label}</p>
                    </div>
                    <span className="text-xs text-gray-400 flex-shrink-0">{formatRelativeTime(entry.created_at)}</span>
                  </div>
                )
              }) : (
                <p className="px-6 py-8 text-sm text-gray-400 text-center">No recent activity</p>
              )}
            </div>
          </div>
        </>
      )}
    </Layout>
  )
}

function getActivityStyle(action: string, resourceType: string) {
  if (action === 'create') {
    switch (resourceType) {
      case 'customer': return { icon: Users, bg: 'bg-emerald-50', color: 'text-emerald-500' }
      case 'invoice': return { icon: FileText, bg: 'bg-sky-50', color: 'text-sky-500' }
      case 'subscription': return { icon: CreditCard, bg: 'bg-violet-50', color: 'text-violet-500' }
      case 'api_key': return { icon: Shield, bg: 'bg-amber-50', color: 'text-amber-500' }
      default: return { icon: CheckCircle, bg: 'bg-emerald-50', color: 'text-emerald-500' }
    }
  }
  if (action === 'finalize') return { icon: FileText, bg: 'bg-sky-50', color: 'text-sky-500' }
  if (action === 'void' || action === 'cancel' || action === 'delete' || action === 'revoke') {
    return { icon: AlertTriangle, bg: 'bg-rose-50', color: 'text-rose-500' }
  }
  if (action === 'grant') return { icon: DollarSign, bg: 'bg-emerald-50', color: 'text-emerald-500' }
  if (action === 'run') return { icon: Clock, bg: 'bg-sky-50', color: 'text-sky-500' }
  if (action === 'update') return { icon: Settings, bg: 'bg-gray-100', color: 'text-gray-500' }
  if (action === 'pause') return { icon: Clock, bg: 'bg-amber-50', color: 'text-amber-500' }
  if (action === 'resume' || action === 'activate') return { icon: CheckCircle, bg: 'bg-emerald-50', color: 'text-emerald-500' }
  return { icon: Settings, bg: 'bg-gray-100', color: 'text-gray-500' }
}

function formatActivityLabel(action: string, resourceType: string): string {
  const resource = resourceType.replace(/_/g, ' ')
  switch (action) {
    case 'create': return `New ${resource} created`
    case 'update': return `${capitalize(resource)} updated`
    case 'delete': return `${capitalize(resource)} deleted`
    case 'finalize': return `Invoice finalized`
    case 'void': return `Invoice voided`
    case 'cancel': return `Subscription canceled`
    case 'pause': return `Subscription paused`
    case 'resume': return `Subscription resumed`
    case 'activate': return `Subscription activated`
    case 'grant': return `Credits granted`
    case 'adjust': return `Credits adjusted`
    case 'revoke': return `API key revoked`
    case 'run': return `Billing cycle executed`
    case 'issue': return `Credit note issued`
    case 'resolve': return `Dunning run resolved`
    default: return `${capitalize(action)} ${resource}`
  }
}

function capitalize(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1)
}
