import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, formatCents, formatDate, type Invoice } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'

export function InvoicesPage() {
  const [invoices, setInvoices] = useState<Invoice[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    api.listInvoices().then(res => {
      setInvoices(res.data)
      setTotal(res.total)
      setLoading(false)
    })
  }, [])

  return (
    <Layout>
      <h1 className="text-2xl font-semibold text-gray-900">Invoices</h1>
      <p className="text-sm text-gray-500 mt-1">{total} total</p>

      <div className="bg-white rounded-xl border border-gray-200 mt-6">
        {loading ? (
          <div className="p-8 text-gray-400 animate-pulse">Loading...</div>
        ) : (
          <table className="w-full">
            <thead>
              <tr className="border-b border-gray-100">
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Invoice</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Status</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Payment</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Period</th>
                <th className="text-right text-xs font-medium text-gray-500 px-6 py-3">Amount</th>
                <th className="text-right text-xs font-medium text-gray-500 px-6 py-3">PDF</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-50">
              {invoices.map(inv => (
                <tr key={inv.id} className="hover:bg-gray-50 transition-colors">
                  <td className="px-6 py-3">
                    <Link to={`/invoices/${inv.id}`} className="text-sm font-medium text-gray-900 hover:text-velox-600">
                      {inv.invoice_number}
                    </Link>
                  </td>
                  <td className="px-6 py-3"><Badge status={inv.status} /></td>
                  <td className="px-6 py-3"><Badge status={inv.payment_status} /></td>
                  <td className="px-6 py-3 text-sm text-gray-500">
                    {formatDate(inv.billing_period_start)} — {formatDate(inv.billing_period_end)}
                  </td>
                  <td className="px-6 py-3 text-sm font-medium text-gray-900 text-right">
                    {formatCents(inv.total_amount_cents)}
                  </td>
                  <td className="px-6 py-3 text-right">
                    <a
                      href={`/v1/invoices/${inv.id}/pdf`}
                      target="_blank"
                      className="text-xs text-velox-600 hover:underline"
                    >
                      Download
                    </a>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        {!loading && invoices.length === 0 && (
          <p className="px-6 py-8 text-sm text-gray-400 text-center">No invoices yet. Trigger a billing cycle to generate invoices.</p>
        )}
      </div>
    </Layout>
  )
}
