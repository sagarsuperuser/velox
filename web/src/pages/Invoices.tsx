import { useEffect, useState, useMemo, useCallback } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { api, downloadPDF, formatCents, formatDate, formatDateTime, type Invoice, type Customer } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'
import { SortableHeader } from '@/components/SortableHeader'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { useToast } from '@/components/Toast'
import { useSortable } from '@/hooks/useSortable'
import { Search, Download } from 'lucide-react'
import { downloadCSV } from '@/lib/csv'
import { Pagination } from '@/components/Pagination'
import { DatePicker } from '@/components/DatePicker'

const PAGE_SIZE = 25

export function InvoicesPage() {
  const [invoices, setInvoices] = useState<Invoice[]>([])
  const [total, setTotal] = useState(0)
  const [customerMap, setCustomerMap] = useState<Record<string, Customer>>({})
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [statusFilter, setStatusFilter] = useState<string>('')
  const [search, setSearch] = useState('')
  const [dateFrom, setDateFrom] = useState('')
  const [dateTo, setDateTo] = useState('')
  const [page, setPage] = useState(1)
  const toast = useToast()
  const navigate = useNavigate()

  // Server-side paginated fetch
  const loadInvoices = useCallback(() => {
    setLoading(true)
    setError(null)
    const params = new URLSearchParams()
    params.set('limit', String(PAGE_SIZE))
    params.set('offset', String((page - 1) * PAGE_SIZE))
    if (statusFilter) params.set('status', statusFilter)

    Promise.all([
      api.listInvoices(params.toString()),
      api.listCustomers(),
    ]).then(([invRes, custRes]) => {
      setInvoices(invRes.data)
      setTotal(invRes.total)
      const cMap: Record<string, Customer> = {}
      custRes.data.forEach(c => { cMap[c.id] = c })
      setCustomerMap(cMap)
      setLoading(false)
    }).catch(err => { setError(err instanceof Error ? err.message : 'Failed to load invoices'); setLoading(false) })
  }, [page, statusFilter])

  useEffect(() => { loadInvoices() }, [loadInvoices])

  // Client-side search + date filter on current page data
  const filtered = useMemo(() => invoices.filter(inv => {
    if (search) {
      const q = search.toLowerCase()
      if (!inv.invoice_number.toLowerCase().includes(q)) return false
    }
    if (dateFrom) {
      if (inv.created_at && inv.created_at.slice(0, 10) < dateFrom) return false
    }
    if (dateTo) {
      if (inv.created_at && inv.created_at.slice(0, 10) > dateTo) return false
    }
    return true
  }), [invoices, search, dateFrom, dateTo])

  const { sorted, sortKey, sortDir, onSort } = useSortable(filtered, 'created_at', 'desc')

  const totalPages = Math.ceil(total / PAGE_SIZE)

  return (
    <Layout>
      <div className="flex justify-between items-center">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900 dark:text-gray-100">Invoices</h1>
          <p className="text-sm text-gray-600 dark:text-gray-400 mt-1">
            {statusFilter ? `Showing ${statusFilter} invoices` : `${total} total`}
          </p>
        </div>
        <div className="flex items-center gap-2">
          {total > 0 && (
            <button
              onClick={() => {
                // Export fetches all data for CSV
                api.listInvoices().then(invRes => {
                  const rows = invRes.data.map(inv => [
                    inv.invoice_number,
                    customerMap[inv.customer_id]?.display_name || 'Unknown',
                    inv.status,
                    inv.payment_status,
                    (inv.amount_due_cents / 100).toFixed(2),
                    inv.currency,
                    inv.billing_period_start,
                    inv.billing_period_end,
                    formatDateTime(inv.created_at),
                  ])
                  downloadCSV('invoices.csv', ['Invoice Number', 'Customer', 'Status', 'Payment Status', 'Amount', 'Currency', 'Period Start', 'Period End', 'Created'], rows)
                })
              }}
              className="flex items-center gap-2 px-4 py-2 border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-300 rounded-lg text-sm font-medium hover:bg-gray-50 dark:hover:bg-gray-800 shadow-sm transition-colors"
            >
              <Download size={16} />
              Export CSV
            </button>
          )}
          <div className="flex gap-1 bg-gray-100 dark:bg-gray-800 rounded-lg p-1">
            {[
              { value: '', label: 'All' },
              { value: 'draft', label: 'Draft' },
              { value: 'finalized', label: 'Open' },
              { value: 'paid', label: 'Paid' },
              { value: 'voided', label: 'Voided' },
            ].map(f => (
              <button key={f.value} onClick={() => { setStatusFilter(f.value); setPage(1) }}
                className={`px-3 py-1.5 rounded-md text-xs font-medium transition-colors ${
                  statusFilter === f.value ? 'bg-white dark:bg-gray-700 text-gray-900 dark:text-gray-100 shadow-sm' : 'text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-200'
                }`}>
                {f.label}
              </button>
            ))}
          </div>
        </div>
      </div>

      {/* Search */}
      {total > 0 && (
        <div className="relative mt-6">
          <Search size={16} className="absolute left-3 top-2.5 text-gray-400" />
          <input
            type="text"
            value={search}
            onChange={e => setSearch(e.target.value)}
            placeholder="Search within page..."
            className="w-full pl-9 pr-4 py-2 border border-gray-200 dark:border-gray-700 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-velox-500 focus:border-transparent bg-white dark:bg-gray-800 dark:text-gray-100"
          />
        </div>
      )}

      {/* Date range filter */}
      {total > 0 && (
        <div className="flex items-center gap-3 mt-3">
          <DatePicker value={dateFrom} onChange={v => { setDateFrom(v) }} placeholder="From" clearable />
          <DatePicker value={dateTo} onChange={v => { setDateTo(v) }} placeholder="To" clearable />
          {(search || dateFrom || dateTo) && (
            <span className="text-xs text-gray-400">Filtering within current page</span>
          )}
        </div>
      )}

      <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card mt-4">
        {error ? (
          <ErrorState message={error} onRetry={loadInvoices} />
        ) : loading ? (
          <LoadingSkeleton rows={6} columns={6} />
        ) : sorted.length === 0 ? (
          <EmptyState
            title="No invoices found"
            description={statusFilter ? `No ${statusFilter} invoices. Try a different filter.` : 'Trigger a billing cycle to generate invoices.'}
          />
        ) : (
          <>
          <div className="overflow-x-auto">
          <table className="w-full">
            <thead>
              <tr className="border-b border-gray-100 dark:border-gray-800 bg-gray-50 dark:bg-gray-800/50">
                <SortableHeader label="Invoice" sortKey="invoice_number" activeSortKey={sortKey} sortDir={sortDir} onSort={onSort} />
                <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Customer</th>
                <SortableHeader label="Status" sortKey="status" activeSortKey={sortKey} sortDir={sortDir} onSort={onSort} />
                <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Payment</th>
                <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Period</th>
                <SortableHeader label="Amount Due" sortKey="amount_due_cents" activeSortKey={sortKey} sortDir={sortDir} onSort={onSort} align="right" />
                <th className="text-right text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">PDF</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
              {sorted.map(inv => (
                <tr key={inv.id} className="hover:bg-gray-50 dark:hover:bg-gray-800/50 cursor-pointer transition-colors group" onClick={(e) => {
                  const target = e.target as HTMLElement
                  if (target.closest('button, a, input, select')) return
                  navigate(`/invoices/${inv.id}`)
                }}>
                  <td className="px-6 py-3">
                    <Link to={`/invoices/${inv.id}`} className="text-sm font-medium text-gray-900 dark:text-gray-100 group-hover:text-velox-600 transition-colors">
                      {inv.invoice_number}
                    </Link>
                  </td>
                  <td className="px-6 py-3 text-sm text-gray-600 dark:text-gray-400">
                    {customerMap[inv.customer_id]?.display_name || 'Unknown'}
                  </td>
                  <td className="px-6 py-3"><Badge status={inv.status} /></td>
                  <td className="px-6 py-3"><Badge status={inv.payment_status} /></td>
                  <td className="px-6 py-3 text-sm text-gray-600 dark:text-gray-400">
                    {formatDate(inv.billing_period_start)} — {formatDate(inv.billing_period_end)}
                  </td>
                  <td className="px-6 py-3 text-sm font-medium text-gray-900 dark:text-gray-100 text-right">
                    {formatCents(inv.amount_due_cents, inv.currency)}
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
          <Pagination page={page} totalPages={totalPages} onPageChange={setPage} />
          </>
        )}
      </div>
    </Layout>
  )
}
