import { useMemo } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api, downloadPDF, formatCents, formatDate, formatDateTime } from '@/lib/api'
import type { Customer, Invoice } from '@/lib/api'
import { showApiError } from '@/lib/formErrors'
import { downloadCSV } from '@/lib/csv'
import { Layout } from '@/components/Layout'
import { useSortable, type SortDir } from '@/hooks/useSortable'
import { useUrlState } from '@/hooks/useUrlState'
import { cn } from '@/lib/utils'
import { statusBadgeVariant, statusBorderColor } from '@/lib/status'
import { InitialsAvatar } from '@/components/InitialsAvatar'
import { ExpiryBadge } from '@/components/ExpiryBadge'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { DatePicker } from '@/components/ui/date-picker'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent } from '@/components/ui/card'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import {
  Pagination,
  PaginationContent,
  PaginationItem,
  PaginationLink,
  PaginationNext,
  PaginationPrevious,
} from '@/components/ui/pagination'
import { TableSkeleton } from '@/components/ui/TableSkeleton'

import { Search, Download, ArrowUpDown, ArrowUp, ArrowDown, Receipt } from 'lucide-react'
import { EmptyState } from '@/components/EmptyState'

const PAGE_SIZE = 25

function SortableHead({
  label, sortKey: key, activeSortKey, sortDir, onSort, className,
}: {
  label: string; sortKey: string; activeSortKey: string; sortDir: 'asc' | 'desc'; onSort: (key: string) => void; className?: string
}) {
  const active = key === activeSortKey
  return (
    <TableHead className={className}>
      <button
        onClick={() => onSort(key)}
        className="flex items-center gap-1.5 text-xs font-medium hover:text-foreground transition-colors"
      >
        {label}
        {active ? (
          sortDir === 'asc' ? <ArrowUp size={14} /> : <ArrowDown size={14} />
        ) : (
          <ArrowUpDown size={14} className="opacity-40" />
        )}
      </button>
    </TableHead>
  )
}

export default function InvoicesPage() {
  const [urlState, setUrlState] = useUrlState({
    search: '',
    status: '',
    payment_status: '',
    dateFrom: '',
    dateTo: '',
    page: '1',
    sort: 'created_at',
    dir: 'desc',
  })
  const { search, status: statusFilter, payment_status: paymentStatusFilter, dateFrom, dateTo, sort: sortKey } = urlState
  const sortDir = urlState.dir as SortDir
  const page = Math.max(1, parseInt(urlState.page) || 1)
  const navigate = useNavigate()

  const queryParams = useMemo(() => {
    const params = new URLSearchParams()
    params.set('limit', String(PAGE_SIZE))
    params.set('offset', String((page - 1) * PAGE_SIZE))
    if (statusFilter) params.set('status', statusFilter)
    if (paymentStatusFilter) params.set('payment_status', paymentStatusFilter)
    return params.toString()
  }, [page, statusFilter, paymentStatusFilter])

  const { data: invoicesData, isLoading: loading, error: loadErrorObj, refetch } = useQuery({
    queryKey: ['invoices', page, statusFilter, paymentStatusFilter],
    queryFn: () => api.listInvoices(queryParams),
  })

  const { data: customersData } = useQuery({
    queryKey: ['customers-map'],
    queryFn: () => api.listCustomers(),
  })

  const invoices = invoicesData?.data ?? []
  const total = invoicesData?.total ?? 0
  const loadError = loadErrorObj instanceof Error ? loadErrorObj.message : loadErrorObj ? String(loadErrorObj) : null

  const customerMap = useMemo(() => {
    const cMap: Record<string, Customer> = {}
    ;(customersData?.data ?? []).forEach(c => { cMap[c.id] = c })
    return cMap
  }, [customersData])

  // Client-side search + date filter on current page data
  const filtered = useMemo(() => invoices.filter((inv: Invoice) => {
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

  const { sorted, onSort } = useSortable(
    filtered,
    sortKey,
    sortDir,
    (key, dir) => setUrlState({ sort: key, dir }),
  )

  const totalPages = Math.ceil(total / PAGE_SIZE)

  const handleExport = () => {
    api.listInvoices().then(invRes => {
      const rows = invRes.data.map((inv: Invoice) => [
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
  }

  return (
    <Layout>
      {/* Page header */}
      <div className="flex justify-between items-center">
        <div>
          <h1 className="text-2xl font-semibold text-foreground">Invoices</h1>
          <p className="text-sm text-muted-foreground mt-1 flex items-center gap-2">
            <span>
              Track invoices, payments, and billing history
              {statusFilter ? ` · Showing ${statusFilter}` : !paymentStatusFilter && total > 0 ? ` · ${total} total` : ''}
            </span>
            {paymentStatusFilter && (
              <Badge variant="secondary" className="gap-1">
                payment: {paymentStatusFilter}
                <button
                  type="button"
                  onClick={() => setUrlState({ payment_status: '', page: '1' })}
                  className="ml-0.5 hover:text-foreground"
                  aria-label="Clear payment status filter"
                >
                  ×
                </button>
              </Badge>
            )}
          </p>
        </div>
        <div className="flex items-center gap-2">
          {total > 0 && (
            <Button variant="outline" size="sm" onClick={handleExport}>
              <Download size={16} className="mr-2" />
              Export CSV
            </Button>
          )}
          <div className="flex gap-1 bg-muted rounded-lg p-1">
            {[
              { value: '', label: 'All' },
              { value: 'draft', label: 'Draft' },
              { value: 'finalized', label: 'Open' },
              { value: 'paid', label: 'Paid' },
              { value: 'voided', label: 'Voided' },
            ].map(f => (
              <button
                key={f.value}
                onClick={() => setUrlState({ status: f.value, page: '1' })}
                className={cn(
                  'px-3 py-1.5 rounded-md text-xs font-medium transition-colors',
                  statusFilter === f.value
                    ? 'bg-background text-foreground shadow-sm'
                    : 'text-muted-foreground hover:text-foreground'
                )}
              >
                {f.label}
              </button>
            ))}
          </div>
        </div>
      </div>

      {/* Search + date filters */}
      {total > 0 && (
        <div className="flex items-center gap-3 mt-6">
          <div className="relative flex-1">
            <Search size={16} className="absolute left-3 top-2.5 text-muted-foreground" />
            <Input
              value={search}
              onChange={e => setUrlState({ search: e.target.value })}
              placeholder="Search within page..."
              className="pl-9"
            />
          </div>
          <DatePicker
            value={dateFrom}
            onChange={(v) => setUrlState({ dateFrom: v })}
            placeholder="From date"
            className="w-44"
          />
          <DatePicker
            value={dateTo}
            onChange={(v) => setUrlState({ dateTo: v })}
            placeholder="To date"
            className="w-44"
          />
          {(search || dateFrom || dateTo) && (
            <span className="text-xs text-muted-foreground">Filtering within current page</span>
          )}
        </div>
      )}

      {/* Table */}
      <Card className="mt-4">
        <CardContent className="p-0">
          {loadError ? (
            <div className="p-8 text-center">
              <p className="text-sm text-destructive mb-3">{loadError}</p>
              <Button variant="outline" size="sm" onClick={() => refetch()}>
                Retry
              </Button>
            </div>
          ) : loading ? (
            <TableSkeleton columns={7} />
          ) : total === 0 ? (
            statusFilter || paymentStatusFilter ? (
              <EmptyState
                title={
                  statusFilter && paymentStatusFilter
                    ? `No ${statusFilter} invoices with payment ${paymentStatusFilter}`
                    : statusFilter
                    ? `No ${statusFilter} invoices`
                    : `No invoices with payment ${paymentStatusFilter}`
                }
                description="Try a different filter to see more results."
                action={{
                  label: 'Clear filters',
                  variant: 'outline',
                  onClick: () => setUrlState({ status: '', payment_status: '', page: '1' }),
                }}
              />
            ) : (
              <EmptyState
                icon={Receipt}
                title="No invoices yet"
                description="Invoices are generated when a billing cycle runs for an active subscription."
                action={{
                  label: 'View Subscriptions',
                  to: '/subscriptions',
                  variant: 'outline',
                }}
              />
            )
          ) : sorted.length === 0 ? (
            <p className="px-6 py-8 text-sm text-muted-foreground text-center">
              No invoices match filters on this page
            </p>
          ) : (
            <>
              <Table>
                <TableHeader>
                  <TableRow>
                    <SortableHead label="Invoice" sortKey="invoice_number" activeSortKey={sortKey} sortDir={sortDir} onSort={onSort} />
                    <TableHead className="text-xs font-medium">Customer</TableHead>
                    <SortableHead label="Status" sortKey="status" activeSortKey={sortKey} sortDir={sortDir} onSort={onSort} />
                    <TableHead className="text-xs font-medium">Payment</TableHead>
                    <TableHead className="text-xs font-medium">Period</TableHead>
                    <SortableHead label="Amount Due" sortKey="amount_due_cents" activeSortKey={sortKey} sortDir={sortDir} onSort={onSort} className="text-right" />
                    <TableHead className="text-xs font-medium text-right">PDF</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {sorted.map((inv: Invoice) => (
                    <TableRow
                      key={inv.id}
                      className={cn('cursor-pointer hover:bg-muted/50 transition-colors border-l-[3px]', statusBorderColor(inv.payment_status))}
                      onClick={(e) => {
                        const target = e.target as HTMLElement
                        if (target.closest('button, a, input, select')) return
                        navigate(`/invoices/${inv.id}`)
                      }}
                    >
                      <TableCell>
                        <div className="flex items-center gap-1.5">
                          <Link
                            to={`/invoices/${inv.id}`}
                            className="text-sm font-medium text-foreground hover:text-primary transition-colors truncate block max-w-[140px]"
                            title={inv.invoice_number}
                          >
                            {inv.invoice_number}
                          </Link>
                          <AttentionDot attention={inv.attention} />
                        </div>
                      </TableCell>
                      <TableCell className="text-sm">
                        <div className="flex items-center gap-2.5">
                          <InitialsAvatar name={customerMap[inv.customer_id]?.display_name || 'Unknown'} size="xs" />
                          <Link
                            to={`/customers/${inv.customer_id}`}
                            onClick={e => e.stopPropagation()}
                            className="text-sm font-medium text-foreground hover:text-primary truncate block max-w-[160px]"
                            title={customerMap[inv.customer_id]?.display_name || 'Unknown'}
                          >
                            {customerMap[inv.customer_id]?.display_name || 'Unknown'}
                          </Link>
                        </div>
                      </TableCell>
                      <TableCell>
                        <Badge variant={statusBadgeVariant(inv.status)}>{inv.status}</Badge>
                      </TableCell>
                      <TableCell>
                        <div className="flex items-center gap-2">
                          {/* payment_status is only meaningful once an invoice
                              is finalized — drafts default to "pending" but
                              no PaymentIntent exists yet. Hiding the pill on
                              drafts (Stripe parity) avoids the misleading
                              "this is stuck on payment" reading. */}
                          {inv.status !== 'draft' && (
                            <Badge variant={statusBadgeVariant(inv.payment_status)}>{inv.payment_status}</Badge>
                          )}
                          {inv.due_at && inv.payment_status !== 'paid' && inv.status !== 'draft' && (
                            <ExpiryBadge expiresAt={inv.due_at} label="Due" warningDays={3} />
                          )}
                          {inv.status === 'draft' && (
                            <span className="text-xs text-muted-foreground">—</span>
                          )}
                        </div>
                      </TableCell>
                      <TableCell className="text-sm text-muted-foreground">
                        {formatDate(inv.billing_period_start)} {'\u2014'} {formatDate(inv.billing_period_end)}
                      </TableCell>
                      <TableCell className="text-right tabular-nums font-mono text-sm">
                        {formatCents(inv.amount_due_cents, inv.currency)}
                      </TableCell>
                      <TableCell className="text-right">
                        <Button
                          variant="outline"
                          size="sm"
                          onClick={async (e) => {
                            e.stopPropagation()
                            try {
                              await downloadPDF(inv.id, inv.invoice_number)
                            } catch (err) {
                              showApiError(err, 'Failed to download PDF')
                            }
                          }}
                        >
                          Download
                        </Button>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>

              {/* Pagination */}
              {totalPages > 1 && (
                <div className="border-t border-border px-4 py-3 flex items-center justify-between">
                  <p className="text-xs text-muted-foreground">
                    Showing {(page - 1) * PAGE_SIZE + 1}
                    {'\u2013'}
                    {Math.min(page * PAGE_SIZE, total)} of {total}
                  </p>
                  <Pagination>
                    <PaginationContent>
                      <PaginationItem>
                        <PaginationPrevious
                          onClick={() => setUrlState({ page: String(Math.max(1, page - 1)) })}
                          className={cn(page <= 1 && 'pointer-events-none opacity-50')}
                        />
                      </PaginationItem>
                      {Array.from({ length: Math.min(totalPages, 5) }, (_, i) => {
                        let pageNum: number
                        if (totalPages <= 5) {
                          pageNum = i + 1
                        } else if (page <= 3) {
                          pageNum = i + 1
                        } else if (page >= totalPages - 2) {
                          pageNum = totalPages - 4 + i
                        } else {
                          pageNum = page - 2 + i
                        }
                        return (
                          <PaginationItem key={pageNum}>
                            <PaginationLink
                              onClick={() => setUrlState({ page: String(pageNum) })}
                              isActive={page === pageNum}
                            >
                              {pageNum}
                            </PaginationLink>
                          </PaginationItem>
                        )
                      })}
                      <PaginationItem>
                        <PaginationNext
                          onClick={() => setUrlState({ page: String(Math.min(totalPages, page + 1)) })}
                          className={cn(page >= totalPages && 'pointer-events-none opacity-50')}
                        />
                      </PaginationItem>
                    </PaginationContent>
                  </Pagination>
                </div>
              )}
            </>
          )}
        </CardContent>
      </Card>
    </Layout>
  )
}

// AttentionDot is the list-row signal that an invoice needs operator
// attention. Severity-tinted disc, title attribute carries the typed
// reason + message — hover surfaces the diagnosis without leaving the
// list. Renders nothing when invoice.attention is absent.
function AttentionDot({ attention }: { attention: Invoice['attention'] }) {
  if (!attention) return null
  const color =
    attention.severity === 'critical'
      ? 'bg-destructive'
      : attention.severity === 'warning'
        ? 'bg-amber-500'
        : 'bg-blue-500'
  const title = `${attention.reason} — ${attention.message}`
  return (
    <span
      className={cn('inline-block w-1.5 h-1.5 rounded-full shrink-0', color)}
      title={title}
      aria-label={title}
    />
  )
}
