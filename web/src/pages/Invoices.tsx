import { useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { api, downloadPDF, formatCents, formatDate, type Invoice, type Customer } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { useToast } from '@/components/Toast'
import { Search, Download } from 'lucide-react'
import { downloadCSV } from '@/lib/csv'
import { Pagination } from '@/components/Pagination'

const STATUS_OPTIONS = ['All', 'draft', 'finalized', 'paid', 'voided'] as const

export function InvoicesPage() {
  const [invoices, setInvoices] = useState<Invoice[]>([])
  const [total, setTotal] = useState(0)
  const [customerMap, setCustomerMap] = useState<Record<string, Customer>>({})
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [statusFilter, setStatusFilter] = useState<string>('All')
  const [search, setSearch] = useState('')
  const [page, setPage] = useState(1)
  const pageSize = 25
  const toast = useToast()
  const navigate = useNavigate()

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

  const filtered = invoices.filter(inv => {
    if (statusFilter !== 'All' && inv.status !== statusFilter) return false
    if (search) {
      const q = search.toLowerCase()
      if (!inv.invoice_number.toLowerCase().includes(q)) return false
    }
    return true
  })

  return (
    <Layout>
      <div className="flex justify-between items-center">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900">Invoices</h1>
          <p className="text-sm text-gray-500 mt-1">
            {statusFilter !== 'All' ? `${filtered.length} of ${total} invoices` : `${total} total`}
          </p>
        </div>
        <div className="flex items-center gap-2">
          {invoices.length > 0 && (
            <button
              onClick={() => {
                const rows = filtered.map(inv => [
                  inv.invoice_number,
                  customerMap[inv.customer_id]?.display_name || 'Unknown',
                  inv.status,
                  inv.payment_status,
                  (inv.total_amount_cents / 100).toFixed(2),
                  inv.currency,
                  inv.billing_period_start,
                  inv.billing_period_end,
                  formatDate(inv.created_at),
                ])
                downloadCSV('invoices.csv', ['Invoice Number', 'Customer', 'Status', 'Payment Status', 'Amount', 'Currency', 'Period Start', 'Period End', 'Created'], rows)
              }}
              className="flex items-center gap-2 px-4 py-2 border border-gray-300 text-gray-700 rounded-lg text-sm font-medium hover:bg-gray-50 shadow-sm transition-colors"
            >
              <Download size={16} />
              Export CSV
            </button>
          )}
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
      </div>

      {/* Search */}
      {invoices.length > 0 && (
        <div className="relative mt-6">
          <Search size={16} className="absolute left-3 top-2.5 text-gray-400" />
          <input
            type="text"
            value={search}
            onChange={e => setSearch(e.target.value)}
            placeholder="Search invoices..."
            className="w-full pl-9 pr-4 py-2 border border-gray-200 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-velox-500 focus:border-transparent bg-white"
          />
        </div>
      )}

      <div className="bg-white rounded-xl shadow-card mt-4">
        {error ? (
          <ErrorState message={error} onRetry={loadInvoices} />
        ) : loading ? (
          <LoadingSkeleton rows={6} columns={6} />
        ) : filtered.length === 0 ? (
          <EmptyState
            title="No invoices found"
            description={statusFilter !== 'All' ? `No ${statusFilter} invoices. Try a different filter.` : 'Trigger a billing cycle to generate invoices.'}
          />
        ) : (() => {
          const totalPages = Math.ceil(filtered.length / pageSize)
          const currentPage = Math.min(page, totalPages || 1)
          const paginated = filtered.slice((currentPage - 1) * pageSize, currentPage * pageSize)
          return (
          <>
          <div className="overflow-x-auto">
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
              {paginated.map(inv => (
                <tr key={inv.id} className="hover:bg-gray-50 cursor-pointer transition-colors group" onClick={(e) => {
                  const target = e.target as HTMLElement
                  if (target.closest('button, a, input, select')) return
                  navigate(`/invoices/${inv.id}`)
                }}>
                  <td className="px-6 py-3">
                    <Link to={`/invoices/${inv.id}`} className="text-sm font-medium text-gray-900 group-hover:text-velox-600 transition-colors">
                      {inv.invoice_number}
                    </Link>
                  </td>
                  <td className="px-6 py-3 text-sm text-gray-500">
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
                      className="text-xs font-medium text-velox-600 hover:text-velox-700 bg-velox-50 hover:bg-velox-100 px-2.5 py-1 rounded-md transition-colors"
                    >
                      Download
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          </div>
          <Pagination page={currentPage} totalPages={totalPages} onPageChange={setPage} />
          </>
          )
        })()}
      </div>
    </Layout>
  )
}
