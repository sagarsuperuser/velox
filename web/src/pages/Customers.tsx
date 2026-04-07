import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, formatDate, type Customer } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'

export function CustomersPage() {
  const [customers, setCustomers] = useState<Customer[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    api.listCustomers().then(res => {
      setCustomers(res.data)
      setTotal(res.total)
      setLoading(false)
    })
  }, [])

  return (
    <Layout>
      <div className="flex justify-between items-center">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900">Customers</h1>
          <p className="text-sm text-gray-500 mt-1">{total} total</p>
        </div>
      </div>

      <div className="bg-white rounded-xl border border-gray-200 mt-6">
        {loading ? (
          <div className="p-8 text-gray-400 animate-pulse">Loading...</div>
        ) : (
          <table className="w-full">
            <thead>
              <tr className="border-b border-gray-100">
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Name</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">External ID</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Email</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Status</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Created</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-50">
              {customers.map(c => (
                <tr key={c.id} className="hover:bg-gray-50 transition-colors">
                  <td className="px-6 py-3">
                    <Link to={`/customers/${c.id}`} className="text-sm font-medium text-gray-900 hover:text-velox-600">
                      {c.display_name}
                    </Link>
                  </td>
                  <td className="px-6 py-3 text-sm text-gray-500 font-mono">{c.external_id}</td>
                  <td className="px-6 py-3 text-sm text-gray-500">{c.email || '—'}</td>
                  <td className="px-6 py-3"><Badge status={c.status} /></td>
                  <td className="px-6 py-3 text-sm text-gray-400">{formatDate(c.created_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        {!loading && customers.length === 0 && (
          <p className="px-6 py-8 text-sm text-gray-400 text-center">No customers yet</p>
        )}
      </div>
    </Layout>
  )
}
