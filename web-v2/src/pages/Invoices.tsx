import { useState, useMemo } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { toast } from 'sonner'
import { api, downloadPDF, formatCents, formatDate, formatDateTime } from '@/lib/api'
import type { Customer, Invoice } from '@/lib/api'
import { downloadCSV } from '@/lib/csv'
import { Layout } from '@/components/Layout'
import { useSortable } from '@/hooks/useSortable'
import { cn } from '@/lib/utils'
import { statusBadgeVariant } from '@/lib/status'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
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

import { Search, Download, Loader2, ArrowUpDown, ArrowUp, ArrowDown } from 'lucide-react'

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
  const [statusFilter, setStatusFilter] = useState<string>('')
  const [search, setSearch] = useState('')
  const [dateFrom, setDateFrom] = useState('')
  const [dateTo, setDateTo] = useState('')
  const [page, setPage] = useState(1)
  const navigate = useNavigate()

  const queryParams = useMemo(() => {
    const params = new URLSearchParams()
    params.set('limit', String(PAGE_SIZE))
    params.set('offset', String((page - 1) * PAGE_SIZE))
    if (statusFilter) params.set('status', statusFilter)
    return params.toString()
  }, [page, statusFilter])

  const { data: invoicesData, isLoading: loading, error: loadErrorObj, refetch } = useQuery({
    queryKey: ['invoices', page, statusFilter],
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

  const { sorted, sortKey, sortDir, onSort } = useSortable(filtered, 'created_at', 'desc')

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
          <p className="text-sm text-muted-foreground mt-1">
            {statusFilter ? `Showing ${statusFilter} invoices` : `${total} total`}
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
                onClick={() => { setStatusFilter(f.value); setPage(1) }}
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
              onChange={e => setSearch(e.target.value)}
              placeholder="Search within page..."
              className="pl-9"
            />
          </div>
          <Input
            type="date"
            value={dateFrom}
            onChange={e => setDateFrom(e.target.value)}
            placeholder="From"
            className="w-40"
          />
          <Input
            type="date"
            value={dateTo}
            onChange={e => setDateTo(e.target.value)}
            placeholder="To"
            className="w-40"
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
            <div className="p-8 flex justify-center">
              <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
            </div>
          ) : total === 0 ? (
            <div className="p-12 text-center">
              <p className="text-sm font-medium text-foreground">No invoices found</p>
              <p className="text-sm text-muted-foreground mt-1">Trigger a billing cycle to generate invoices</p>
            </div>
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
                      className="cursor-pointer hover:bg-muted/50 transition-colors"
                      onClick={(e) => {
                        const target = e.target as HTMLElement
                        if (target.closest('button, a, input, select')) return
                        navigate(`/invoices/${inv.id}`)
                      }}
                    >
                      <TableCell>
                        <Link
                          to={`/invoices/${inv.id}`}
                          className="text-sm font-medium text-foreground hover:text-primary transition-colors truncate block max-w-[140px]"
                          title={inv.invoice_number}
                        >
                          {inv.invoice_number}
                        </Link>
                      </TableCell>
                      <TableCell className="text-sm">
                        <Link
                          to={`/customers/${inv.customer_id}`}
                          onClick={e => e.stopPropagation()}
                          className="text-primary hover:underline truncate block max-w-[160px]"
                          title={customerMap[inv.customer_id]?.display_name || 'Unknown'}
                        >
                          {customerMap[inv.customer_id]?.display_name || 'Unknown'}
                        </Link>
                      </TableCell>
                      <TableCell>
                        <Badge variant={statusBadgeVariant(inv.status)}>{inv.status}</Badge>
                      </TableCell>
                      <TableCell>
                        <Badge variant={statusBadgeVariant(inv.payment_status)}>{inv.payment_status}</Badge>
                      </TableCell>
                      <TableCell className="text-sm text-muted-foreground">
                        {formatDate(inv.billing_period_start)} {'\u2014'} {formatDate(inv.billing_period_end)}
                      </TableCell>
                      <TableCell className="text-sm font-medium text-foreground text-right tabular-nums">
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
                              toast.error(err instanceof Error ? err.message : 'Failed to download PDF')
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
                          onClick={() => setPage(p => Math.max(1, p - 1))}
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
                              onClick={() => setPage(pageNum)}
                              isActive={page === pageNum}
                            >
                              {pageNum}
                            </PaginationLink>
                          </PaginationItem>
                        )
                      })}
                      <PaginationItem>
                        <PaginationNext
                          onClick={() => setPage(p => Math.min(totalPages, p + 1))}
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
