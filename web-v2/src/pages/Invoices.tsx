import { useMemo } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api, downloadPDF, formatCents, formatDate, formatDateTime } from '@/lib/api'
import { formatYMDInTZ } from '@/lib/dates'
import type { Customer, Invoice } from '@/lib/api'
import { showApiError } from '@/lib/formErrors'
import { downloadServerCSV } from '@/lib/csv'
import { Layout } from '@/components/Layout'
import { useSortable, type SortDir } from '@/hooks/useSortable'
import { useUrlState } from '@/hooks/useUrlState'
import { cn } from '@/lib/utils'
import { statusBadgeVariant, statusBorderColor } from '@/lib/status'
import { InitialsAvatar } from '@/components/InitialsAvatar'
import { DueBadge } from '@/components/DueBadge'
import { TestClockBadge } from '@/components/TestClockBadge'

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
    // Sort wiring: SPA had clickable column headers + URL state but the
    // params were never sent to the API — list rendered in arbitrary
    // order on ties. Backend validates against a closed allow-list and
    // tie-breaks by id, so unknown values silently fall back to a
    // deterministic default.
    if (sortKey) params.set('sort', sortKey)
    if (sortDir) params.set('dir', sortDir)
    return params.toString()
  }, [page, statusFilter, paymentStatusFilter, sortKey, sortDir])

  const { data: invoicesData, isLoading: loading, error: loadErrorObj, refetch } = useQuery({
    queryKey: ['invoices', page, statusFilter, paymentStatusFilter, sortKey, sortDir],
    queryFn: () => api.listInvoices(queryParams),
  })

  // Fetch only the customers referenced by the visible invoices.
  // Prior shape (listCustomers() with no filter) paginated at 50 rows
  // and rendered "Unknown" on any invoice whose customer fell off
  // page 1. With the ids= filter the backend returns exactly the
  // customers we need; map lookup is now exhaustive for the page.
  const customerIds = useMemo(() => {
    const set = new Set<string>()
    ;(invoicesData?.data ?? []).forEach(inv => { if (inv.customer_id) set.add(inv.customer_id) })
    return Array.from(set)
  }, [invoicesData])
  const { data: customersData } = useQuery({
    queryKey: ['customers-by-ids', customerIds],
    queryFn: () => api.listCustomers(customerIds.length > 0 ? `ids=${customerIds.join(',')}&limit=${customerIds.length}` : ''),
    enabled: customerIds.length > 0,
  })

  // subscriptionsForChip → test_clock_id lookup so each row can carry
  // a TestClockBadge inline. Fetch is cheap (subs list is small in
  // practice; the dashboard already pages other lists) and avoids a
  // backend JOIN in the invoice list query.
  const { data: subscriptionsData } = useQuery({
    queryKey: ['subscriptions-for-test-clock-chip'],
    queryFn: () => api.listSubscriptions(),
  })

  // Test clocks list → frozen_time lookup so per-row "Due in N days"
  // badges read from simulation time when the row's subscription is
  // pinned to a clock. Without this, the badge defaults to wall-clock
  // and understates urgency on test-clock-driven invoices.
  const { data: testClocksData } = useQuery({
    queryKey: ['test-clocks-for-due-badge'],
    queryFn: () => api.listTestClocks(),
  })

  const invoices = invoicesData?.data ?? []
  const total = invoicesData?.total ?? 0
  const loadError = loadErrorObj instanceof Error ? loadErrorObj.message : loadErrorObj ? String(loadErrorObj) : null

  const customerMap = useMemo(() => {
    const cMap: Record<string, Customer> = {}
    ;(customersData?.data ?? []).forEach(c => { cMap[c.id] = c })
    return cMap
  }, [customersData])

  const subTestClockMap = useMemo(() => {
    const m: Record<string, string> = {}
    ;(subscriptionsData?.data ?? []).forEach(s => { if (s.test_clock_id) m[s.id] = s.test_clock_id })
    return m
  }, [subscriptionsData])

  const clockFrozenMap = useMemo(() => {
    const m: Record<string, string> = {}
    ;(testClocksData?.data ?? []).forEach(c => { m[c.id] = c.frozen_time })
    return m
  }, [testClocksData])

  // Client-side search + date filter on current page data
  const filtered = useMemo(() => invoices.filter((inv: Invoice) => {
    if (search) {
      const q = search.toLowerCase()
      if (!inv.invoice_number.toLowerCase().includes(q)) return false
    }
    // Compare created_at re-projected into tenant TZ so the date
    // operators pick matches what they see on the row (commit
    // b523c71 made formatDate render in tenant TZ; this filter
    // must follow the same projection or it disagrees with the
    // displayed date around UTC-day boundaries).
    if (dateFrom) {
      if (inv.created_at && formatYMDInTZ(inv.created_at) < dateFrom) return false
    }
    if (dateTo) {
      if (inv.created_at && formatYMDInTZ(inv.created_at) > dateTo) return false
    }
    return true
  }), [invoices, search, dateFrom, dateTo])

  // useSortable provides the click handler (flip-on-same-key, reset
  // direction on new key) + URL-state binding. We deliberately
  // discard `sorted` because sort is now server-side end-to-end —
  // the server returns rows in the requested order with a
  // deterministic id tie-break. Client-side re-sort would only sort
  // the current page (e.g. "top 50 by created_at re-sorted by
  // amount") which breaks pagination semantics.
  const { onSort } = useSortable(
    filtered,
    sortKey,
    sortDir,
    (key, dir) => setUrlState({ sort: key, dir }),
  )
  const sorted = filtered

  const totalPages = Math.ceil(total / PAGE_SIZE)

  // Server-side streaming export: full tenant dataset (every column
  // on Invoice including period boundaries + lifecycle timestamps),
  // not just the currently-visible page.
  const handleExport = async () => {
    try {
      await downloadServerCSV('/v1/exports/invoices.csv', 'invoices.csv')
    } catch (err) {
      const msg = err instanceof Error ? err.message : 'Export failed'
      toast.error(msg)
    }
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
            minDate={dateFrom ? new Date(dateFrom + 'T00:00:00') : undefined}
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
                    <SortableHead label="Payment" sortKey="payment_status" activeSortKey={sortKey} sortDir={sortDir} onSort={onSort} />
                    <SortableHead label="Period" sortKey="billing_period_start" activeSortKey={sortKey} sortDir={sortDir} onSort={onSort} />
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
                          {inv.subscription_id && subTestClockMap[inv.subscription_id] && (
                            <TestClockBadge testClockId={subTestClockMap[inv.subscription_id]} />
                          )}
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
                          {/* Due-date countdown only on open invoices
                              (status='finalized'). 'paid' and
                              'voided' are terminal; 'draft' has no
                              issued state yet. Pre-fix gate used
                              payment_status !== 'paid' which never
                              matched (domain uses 'succeeded'). */}
                          {inv.due_at && inv.status === 'finalized' && (
                            <DueBadge
                              dueAt={inv.due_at}
                              warningDays={3}
                              now={inv.subscription_id && subTestClockMap[inv.subscription_id]
                                ? clockFrozenMap[subTestClockMap[inv.subscription_id]]
                                : undefined}
                            />
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
