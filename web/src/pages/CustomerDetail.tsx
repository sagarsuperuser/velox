import { useEffect, useState } from 'react'
import { useParams, Link } from 'react-router-dom'
import { api, formatCents, formatDate, type Customer, type CustomerOverview } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'
import { StatCard } from '@/components/StatCard'

export function CustomerDetailPage() {
  const { id } = useParams<{ id: string }>()
  const [customer, setCustomer] = useState<Customer | null>(null)
  const [overview, setOverview] = useState<CustomerOverview | null>(null)
  const [balance, setBalance] = useState(0)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    if (!id) return
    Promise.all([
      api.getCustomer(id),
      api.customerOverview(id),
      api.getBalance(id).catch(() => ({ balance_cents: 0 })),
    ]).then(([c, o, b]) => {
      setCustomer(c)
      setOverview(o)
      setBalance(b.balance_cents)
      setLoading(false)
    })
  }, [id])

  if (loading) return <Layout><div className="animate-pulse text-gray-400">Loading...</div></Layout>
  if (!customer) return <Layout><p>Customer not found</p></Layout>

  return (
    <Layout>
      <div className="flex items-center gap-3 mb-6">
        <Link to="/customers" className="text-sm text-gray-400 hover:text-gray-600">&larr; Customers</Link>
      </div>

      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900">{customer.display_name}</h1>
          <p className="text-sm text-gray-500 mt-0.5 font-mono">{customer.external_id}</p>
        </div>
        <Badge status={customer.status} />
      </div>

      <div className="grid grid-cols-3 gap-4 mt-6">
        <StatCard title="Email" value={customer.email || '—'} />
        <StatCard title="Credit Balance" value={formatCents(balance)} />
        <StatCard title="Active Subscriptions" value={String(overview?.active_subscriptions.length || 0)} />
      </div>

      <div className="grid grid-cols-2 gap-6 mt-8">
        {/* Subscriptions */}
        <div className="bg-white rounded-xl border border-gray-200">
          <div className="px-6 py-4 border-b border-gray-100">
            <h2 className="text-sm font-semibold text-gray-900">Subscriptions</h2>
          </div>
          <div className="divide-y divide-gray-50">
            {overview?.active_subscriptions.map(sub => (
              <div key={sub.id} className="flex items-center justify-between px-6 py-3">
                <div>
                  <p className="text-sm font-medium text-gray-900">{sub.display_name}</p>
                  <p className="text-xs text-gray-400">{sub.code}</p>
                </div>
                <Badge status={sub.status} />
              </div>
            ))}
            {(!overview?.active_subscriptions.length) && (
              <p className="px-6 py-4 text-sm text-gray-400">No subscriptions</p>
            )}
          </div>
        </div>

        {/* Recent invoices */}
        <div className="bg-white rounded-xl border border-gray-200">
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
    </Layout>
  )
}
