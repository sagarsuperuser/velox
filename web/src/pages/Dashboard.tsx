import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, formatCents, formatDate, type Invoice, type Subscription } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { StatCard } from '@/components/StatCard'
import { Badge } from '@/components/Badge'

export function DashboardPage() {
  const [stats, setStats] = useState({ customers: 0, subscriptions: 0, invoices: 0, revenue: 0 })
  const [recentInvoices, setRecentInvoices] = useState<Invoice[]>([])
  const [activeSubs, setActiveSubs] = useState<Subscription[]>([])
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    async function load() {
      try {
        const [customers, invoices, subs] = await Promise.all([
          api.listCustomers(),
          api.listInvoices('limit=10'),
          api.listSubscriptions('status=active'),
        ])

        const revenue = invoices.data
          .filter(i => i.status === 'paid')
          .reduce((sum, i) => sum + i.total_amount_cents, 0)

        setStats({
          customers: customers.total,
          subscriptions: subs.data.length,
          invoices: invoices.total,
          revenue,
        })
        setRecentInvoices(invoices.data.slice(0, 5))
        setActiveSubs(subs.data.slice(0, 5))
      } catch (err) {
        console.error('Failed to load dashboard:', err)
      } finally {
        setLoading(false)
      }
    }
    load()
  }, [])

  if (loading) {
    return <Layout><div className="animate-pulse text-gray-400">Loading...</div></Layout>
  }

  return (
    <Layout>
      <h1 className="text-2xl font-semibold text-gray-900">Dashboard</h1>
      <p className="text-sm text-gray-500 mt-1">Billing overview</p>

      <div className="grid grid-cols-4 gap-4 mt-6">
        <StatCard title="Customers" value={String(stats.customers)} />
        <StatCard title="Active Subscriptions" value={String(stats.subscriptions)} />
        <StatCard title="Total Invoices" value={String(stats.invoices)} />
        <StatCard title="Revenue" value={formatCents(stats.revenue)} subtitle="Paid invoices" />
      </div>

      <div className="grid grid-cols-2 gap-6 mt-8">
        {/* Recent invoices */}
        <div className="bg-white rounded-xl border border-gray-200">
          <div className="px-6 py-4 border-b border-gray-100 flex justify-between items-center">
            <h2 className="text-sm font-semibold text-gray-900">Recent Invoices</h2>
            <Link to="/invoices" className="text-xs text-velox-600 hover:underline">View all</Link>
          </div>
          <div className="divide-y divide-gray-50">
            {recentInvoices.map(inv => (
              <Link key={inv.id} to={`/invoices/${inv.id}`} className="flex items-center justify-between px-6 py-3 hover:bg-gray-50 transition-colors">
                <div>
                  <p className="text-sm font-medium text-gray-900">{inv.invoice_number}</p>
                  <p className="text-xs text-gray-400">{formatDate(inv.created_at)}</p>
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
        <div className="bg-white rounded-xl border border-gray-200">
          <div className="px-6 py-4 border-b border-gray-100 flex justify-between items-center">
            <h2 className="text-sm font-semibold text-gray-900">Active Subscriptions</h2>
            <Link to="/subscriptions" className="text-xs text-velox-600 hover:underline">View all</Link>
          </div>
          <div className="divide-y divide-gray-50">
            {activeSubs.map(sub => (
              <div key={sub.id} className="flex items-center justify-between px-6 py-3">
                <div>
                  <p className="text-sm font-medium text-gray-900">{sub.display_name}</p>
                  <p className="text-xs text-gray-400">{sub.code}</p>
                </div>
                <Badge status={sub.status} />
              </div>
            ))}
            {activeSubs.length === 0 && (
              <p className="px-6 py-4 text-sm text-gray-400">No active subscriptions</p>
            )}
          </div>
        </div>
      </div>
    </Layout>
  )
}
