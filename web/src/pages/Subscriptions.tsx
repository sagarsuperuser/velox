import { useEffect, useState } from 'react'
import { api, formatDate, type Subscription } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'

export function SubscriptionsPage() {
  const [subs, setSubs] = useState<Subscription[]>([])
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    api.listSubscriptions().then(res => {
      setSubs(res.data)
      setLoading(false)
    })
  }, [])

  return (
    <Layout>
      <h1 className="text-2xl font-semibold text-gray-900">Subscriptions</h1>
      <p className="text-sm text-gray-500 mt-1">{subs.length} total</p>

      <div className="bg-white rounded-xl border border-gray-200 mt-6">
        {loading ? (
          <div className="p-8 text-gray-400 animate-pulse">Loading...</div>
        ) : (
          <table className="w-full">
            <thead>
              <tr className="border-b border-gray-100">
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Name</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Code</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Status</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Billing Period</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Created</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-50">
              {subs.map(sub => (
                <tr key={sub.id} className="hover:bg-gray-50 transition-colors">
                  <td className="px-6 py-3 text-sm font-medium text-gray-900">{sub.display_name}</td>
                  <td className="px-6 py-3 text-sm text-gray-500 font-mono">{sub.code}</td>
                  <td className="px-6 py-3"><Badge status={sub.status} /></td>
                  <td className="px-6 py-3 text-sm text-gray-500">
                    {sub.current_billing_period_start && sub.current_billing_period_end
                      ? `${formatDate(sub.current_billing_period_start)} — ${formatDate(sub.current_billing_period_end)}`
                      : '—'}
                  </td>
                  <td className="px-6 py-3 text-sm text-gray-400">{formatDate(sub.created_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        {!loading && subs.length === 0 && (
          <p className="px-6 py-8 text-sm text-gray-400 text-center">No subscriptions yet</p>
        )}
      </div>
    </Layout>
  )
}
