import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, downloadPDF, formatCents, formatDate, type Invoice, type Customer } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { useToast } from '@/components/Toast'

const STATUS_OPTIONS = ['All', 'draft', 'finalized', 'paid', 'voided'] as const

export function InvoicesPage() {
  const [invoices, setInvoices] = useState<Invoice[]>([])
  const [total, setTotal] = useState(0)
  const [customerMap, setCustomerMap] = useState<Record<string, Customer>>({})
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [statusFilter, setStatusFilter] = useState<string>('All')
  const toast = useToast()

  const loadInvoices = () => {
    setLoading(true)
    setError(null)
    Promise.all([
      api.listInvoices(),
      api.listCustomers(),
    ]).then(([invRes, custRes]) => {
      setInvoices(invRes.data)
      setTotal(invRes.total)
      const cMap: Record<string, Customer> = {}
      custRes.data.forEach(c => { cMap[c.id] = c })
      setCustomerMap(cMap)
      setLoading(false)
    }).catch(err => { setError(err instanceof Error ? err.message : 'Failed to load invoices'); setLoading(false) })
  }

  useEffect(() => { loadInvoices() }, [])

  const filtered = statusFilter === 'All'
    ? invoices
    : invoices.filter(inv => inv.status === statusFilter)

  return (
    <Layout>
      <div className="flex justify-between items-center">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900">Invoices</h1>
          <p className="text-sm text-gray-500 mt-1">
            {statusFilter !== 'All' ? `${filtered.length} of ${total} invoices` : `${total} total`}
          </p>
        </div>
        <select
          value={statusFilter}
          onChange={e => setStatusFilter(e.target.value)}
          className="px-3 py-2 border border-gray-200 rounded-lg shadow-sm text-sm focus:outline-none focus:ring-2 focus:ring-velox-500 bg-white"
        >
          {STATUS_OPTIONS.map(s => (
            <option key={s} value={s}>{s === 'All' ? 'All statuses' : s.charAt(0).toUpperCase() + s.slice(1)}</option>
          ))}
        </select>
      </div>

      <div className="bg-white rounded-xl shadow-card mt-6">
        {error ? (
          <ErrorState message={error} onRetry={loadInvoices} />
        ) : loading ? (
          <LoadingSkeleton rows={6} columns={6} />
        ) : filtered.length === 0 ? (
          <EmptyState
            title="No invoices found"
            description={statusFilter !== 'All' ? `No ${statusFilter} invoices. Try a different filter.` : 'Trigger a billing cycle to generate invoices.'}
          />
        ) : (
          <table className="w-full">
            <thead>
              <tr className="border-b border-gray-100">
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Invoice</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Customer</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Status</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Payment</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Period</th>
                <th className="text-right text-xs font-medium text-gray-500 px-6 py-3">Amount</th>
                <th className="text-right text-xs font-medium text-gray-500 px-6 py-3">PDF</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-50">
              {filtered.map(inv => (
                <tr key={inv.id} className="hover:bg-gray-50 transition-colors">
                  <td className="px-6 py-3">
                    <Link to={`/invoices/${inv.id}`} className="text-sm font-medium text-gray-900 hover:text-velox-600">
                      {inv.invoice_number}
                    </Link>
                  </td>
                  <td className="px-6 py-3 text-sm text-gray-600">
                    {customerMap[inv.customer_id]?.display_name || 'Unknown'}
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
                    <button
                      onClick={async () => {
                        try {
                          await downloadPDF(inv.id, inv.invoice_number)
                        } catch (err) {
                          toast.error(err instanceof Error ? err.message : 'Failed to download PDF')
                        }
                      }}
                      className="text-xs text-velox-600 hover:underline"
                    >
                      Download
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </Layout>
  )
}
