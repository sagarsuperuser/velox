import { useState, useMemo, useCallback, useEffect } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api, formatDateTime, eventDimensions } from '@/lib/api'
import type { Customer, Meter, UsageEvent } from '@/lib/api'
import { downloadCSV } from '@/lib/csv'
import { Layout } from '@/components/Layout'
import { cn } from '@/lib/utils'

import { Button } from '@/components/ui/button'
import { DatePicker } from '@/components/ui/date-picker'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
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

import { Download, Activity, Hash, Gauge, Users } from 'lucide-react'
import { EmptyState } from '@/components/EmptyState'
import { TableSkeleton } from '@/components/ui/TableSkeleton'
import { useUrlState } from '@/hooks/useUrlState'

const PAGE_SIZE = 25

export default function UsageEventsPage() {
  const [urlState, setUrlState] = useUrlState({
    customer: '',
    meter: '',
    from: '',
    to: '',
    dim: '',
    page: '1',
  })
  const { customer: filterCustomer, meter: filterMeter, from: filterFrom, to: filterTo, dim: filterDim } = urlState
  const page = Math.max(1, parseInt(urlState.page) || 1)
  const [events, setEvents] = useState<UsageEvent[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const { data: customersData } = useQuery({
    queryKey: ['customers-ref'],
    queryFn: () => api.listCustomers(),
  })

  const { data: metersData } = useQuery({
    queryKey: ['meters-ref'],
    queryFn: () => api.listMeters(),
  })

  const customers = customersData?.data ?? []
  const meters = metersData?.data ?? []

  const customerMap = useMemo(() => {
    const cMap: Record<string, Customer> = {}
    customers.forEach(c => { cMap[c.id] = c })
    return cMap
  }, [customers])

  const meterMap = useMemo(() => {
    const mMap: Record<string, Meter> = {}
    meters.forEach(m => { mMap[m.id] = m })
    return mMap
  }, [meters])

  // Server-side paginated fetch
  const loadEvents = useCallback(() => {
    setLoading(true)
    setError(null)
    const parts: string[] = []
    parts.push(`limit=${PAGE_SIZE}`)
    parts.push(`offset=${(page - 1) * PAGE_SIZE}`)
    if (filterCustomer) parts.push(`customer_id=${filterCustomer}`)
    if (filterMeter) parts.push(`meter_id=${filterMeter}`)
    if (filterFrom) parts.push(`from=${new Date(filterFrom).toISOString()}`)
    if (filterTo) parts.push(`to=${new Date(filterTo + 'T23:59:59').toISOString()}`)
    if (filterDim) parts.push(`dimensions=${encodeURIComponent(filterDim)}`)
    const params = parts.join('&')
    api.listUsageEvents(params)
      .then(res => { setEvents(res.data || []); setTotal(res.total || 0); setLoading(false) })
      .catch(err => { setError(err instanceof Error ? err.message : 'Failed to load usage events'); setEvents([]); setTotal(0); setLoading(false) })
  }, [page, filterCustomer, filterMeter, filterFrom, filterTo, filterDim])

  useEffect(() => { loadEvents() }, [loadEvents])

  // `quantity` is wire-encoded as a string (NUMERIC(38, 12)) for decimal
  // precision; coerce to number for chart math + stats only. Authoritative
  // money math stays server-side.
  const eventQuantity = (e: UsageEvent): number => {
    const n = Number(e.quantity)
    return Number.isNaN(n) ? 0 : n
  }

  // Show the dimensions column + filter only when at least one event in the
  // current page actually carries dimensions, OR when a dimension filter is
  // set. Pre-multi-dim tenants stay visually clean.
  const hasDimensions = useMemo(
    () => events.some(e => eventDimensions(e) !== undefined) || !!filterDim,
    [events, filterDim],
  )

  // Computed stats from current page data
  const stats = useMemo(() => {
    const totalEvents = total
    const totalUnits = events.reduce((sum, e) => sum + eventQuantity(e), 0)
    const activeMeters = new Set(events.map(e => e.meter_id)).size
    const activeCustomers = new Set(events.map(e => e.customer_id)).size
    return { totalEvents, totalUnits, activeMeters, activeCustomers }
  }, [events, total])

  // Meter breakdown from current page data
  const meterBreakdown = useMemo(() => {
    const grouped: Record<string, number> = {}
    for (const e of events) {
      grouped[e.meter_id] = (grouped[e.meter_id] || 0) + eventQuantity(e)
    }
    const grandTotal = Object.values(grouped).reduce((a, b) => a + b, 0)
    return Object.entries(grouped)
      .map(([id, total]) => ({
        id,
        name: meterMap[id]?.name || id.slice(0, 12) + '...',
        unit: meterMap[id]?.unit || 'units',
        total,
        pct: grandTotal > 0 ? (total / grandTotal) * 100 : 0,
      }))
      .sort((a, b) => b.total - a.total)
  }, [events, meterMap])

  const totalPages = Math.ceil(total / PAGE_SIZE)

  const handleExport = () => {
    const parts: string[] = []
    if (filterCustomer) parts.push(`customer_id=${filterCustomer}`)
    if (filterMeter) parts.push(`meter_id=${filterMeter}`)
    if (filterFrom) parts.push(`from=${new Date(filterFrom).toISOString()}`)
    if (filterTo) parts.push(`to=${new Date(filterTo + 'T23:59:59').toISOString()}`)
    if (filterDim) parts.push(`dimensions=${encodeURIComponent(filterDim)}`)
    const exportParams = parts.length > 0 ? parts.join('&') : undefined
    api.listUsageEvents(exportParams).then(res => {
      const rows = (res.data || []).map(ev => [
        formatDateTime(ev.timestamp),
        customerMap[ev.customer_id]?.display_name || ev.customer_id,
        meterMap[ev.meter_id]?.name || ev.meter_id,
        ev.quantity,
        eventDimensions(ev) ? JSON.stringify(eventDimensions(ev)) : '',
      ])
      downloadCSV('usage-events.csv', ['Timestamp', 'Customer', 'Meter', 'Value', 'Dimensions'], rows)
    })
  }

  const statCards = [
    { label: 'Total Events', value: stats.totalEvents.toLocaleString(), icon: Activity, color: 'text-primary' },
    { label: 'Page Units', value: stats.totalUnits.toLocaleString(), icon: Hash, color: 'text-blue-600' },
    { label: 'Active Meters', value: String(stats.activeMeters), icon: Gauge, color: 'text-amber-600' },
    { label: 'Active Customers', value: String(stats.activeCustomers), icon: Users, color: 'text-emerald-600' },
  ]

  return (
    <Layout>
      {/* Header */}
      <div className="flex justify-between items-center">
        <div>
          <h1 className="text-2xl font-semibold text-foreground">Usage Events</h1>
          <p className="text-sm text-muted-foreground mt-1">Track and analyze usage across customers and meters</p>
        </div>
        {total > 0 && (
          <Button variant="outline" size="sm" onClick={handleExport}>
            <Download size={16} className="mr-2" />
            Export CSV
          </Button>
        )}
      </div>

      {/* Filter bar */}
      <div className="flex items-center gap-3 mt-6">
        <select
          value={filterCustomer}
          onChange={(e) => setUrlState({ customer: e.target.value, page: '1' })}
          className="flex h-9 w-52 rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-xs transition-colors focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
        >
          <option value="">All customers</option>
          {customers.map(c => (
            <option key={c.id} value={c.id}>{c.display_name}</option>
          ))}
        </select>
        <select
          value={filterMeter}
          onChange={(e) => setUrlState({ meter: e.target.value, page: '1' })}
          className="flex h-9 w-52 rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-xs transition-colors focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
        >
          <option value="">All meters</option>
          {meters.map(m => (
            <option key={m.id} value={m.id}>{m.name}</option>
          ))}
        </select>
        <DatePicker
          value={filterFrom}
          onChange={v => setUrlState({ from: v, page: '1' })}
          placeholder="From date"
          className="w-44"
        />
        <DatePicker
          value={filterTo}
          onChange={v => setUrlState({ to: v, page: '1' })}
          placeholder="To date"
          className="w-44"
        />
        <input
          type="text"
          value={filterDim}
          onChange={(e) => setUrlState({ dim: e.target.value, page: '1' })}
          placeholder="dimension (e.g. model=gpt-4)"
          className="flex h-9 w-56 rounded-md border border-input bg-transparent px-3 py-1 text-sm font-mono shadow-xs transition-colors focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring placeholder:text-muted-foreground placeholder:font-sans"
        />
      </div>

      {error ? (
        <Card className="mt-6">
          <CardContent className="p-8 text-center">
            <p className="text-sm text-destructive mb-3">{error}</p>
            <Button variant="outline" size="sm" onClick={loadEvents}>
              Retry
            </Button>
          </CardContent>
        </Card>
      ) : loading ? (
        <Card className="mt-6">
          <CardContent className="p-0">
            <TableSkeleton columns={4} />
          </CardContent>
        </Card>
      ) : events.length === 0 ? (
        <Card className="mt-6">
          <CardContent className="p-0">
            <EmptyState
              icon={Activity}
              title="No usage events yet"
              description="Ingest events via the API to start tracking usage against your meters."
            />
          </CardContent>
        </Card>
      ) : (
        <>
          {/* Stat cards */}
          <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4 mt-6">
            {statCards.map(card => (
              <Card key={card.label}>
                <CardContent className="px-5 py-4">
                  <div className="flex items-center gap-3">
                    <div className={cn('p-2 rounded-lg bg-muted', card.color)}>
                      <card.icon size={18} />
                    </div>
                    <div>
                      <p className="text-xs font-medium text-muted-foreground">{card.label}</p>
                      <p className="text-xl font-semibold text-foreground tabular-nums mt-0.5">{card.value}</p>
                    </div>
                  </div>
                </CardContent>
              </Card>
            ))}
          </div>

          {/* Meter breakdown */}
          {meterBreakdown.length > 0 && (
            <Card className="mt-6">
              <CardHeader className="pb-3">
                <CardTitle className="text-sm">Usage by Meter</CardTitle>
              </CardHeader>
              <CardContent className="space-y-3">
                {meterBreakdown.map(m => (
                  <div key={m.id} className="flex items-center gap-3">
                    <span className="text-sm text-foreground w-32 shrink-0 truncate" title={m.name}>{m.name}</span>
                    <div className="flex-1 h-2 bg-muted rounded-full overflow-hidden">
                      <div className="h-full bg-primary rounded-full" style={{ width: `${m.pct}%` }} />
                    </div>
                    <span className="text-sm font-medium text-foreground tabular-nums w-24 text-right">{m.total.toLocaleString()} {m.unit}</span>
                  </div>
                ))}
              </CardContent>
            </Card>
          )}

          {/* Events table */}
          <Card className="mt-6">
            <CardHeader className="pb-3">
              <CardTitle className="text-sm">Event Log</CardTitle>
            </CardHeader>
            <CardContent className="p-0">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead className="text-xs font-medium">Timestamp</TableHead>
                    <TableHead className="text-xs font-medium">Customer</TableHead>
                    <TableHead className="text-xs font-medium">Meter</TableHead>
                    {hasDimensions && <TableHead className="text-xs font-medium">Dimensions</TableHead>}
                    <TableHead className="text-xs font-medium text-right">Value</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {events.map(ev => {
                    const v = eventQuantity(ev)
                    // Render the raw string to preserve full decimal precision
                    // — losing trailing zeros via toLocaleString hides the
                    // distinction between "1.5" and "1.500000000000".
                    const display = ev.quantity
                    const dims = eventDimensions(ev) ?? null
                    return (
                      <TableRow key={ev.id}>
                        <TableCell className="text-sm text-foreground">{formatDateTime(ev.timestamp)}</TableCell>
                        <TableCell className="text-sm text-muted-foreground">
                          {customerMap[ev.customer_id]?.display_name || ev.customer_id.slice(0, 8) + '...'}
                        </TableCell>
                        <TableCell className="text-sm text-muted-foreground">
                          {meterMap[ev.meter_id]?.name || ev.meter_id.slice(0, 8) + '...'}
                        </TableCell>
                        {hasDimensions && (
                          <TableCell>
                            {dims ? (
                              <div className="flex flex-wrap gap-1">
                                {Object.entries(dims).map(([k, val]) => (
                                  <span key={k} className="inline-flex items-center text-xs font-mono bg-muted px-1.5 py-0.5 rounded">
                                    {k}=<span className="text-foreground">{String(val)}</span>
                                  </span>
                                ))}
                              </div>
                            ) : (
                              <span className="text-xs text-muted-foreground">—</span>
                            )}
                          </TableCell>
                        )}
                        <TableCell className={cn('text-sm font-medium text-right tabular-nums', v < 0 ? 'text-destructive' : 'text-foreground')}>
                          {v < 0 ? '' : '+'}{display}
                        </TableCell>
                      </TableRow>
                    )
                  })}
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
            </CardContent>
          </Card>
        </>
      )}
    </Layout>
  )
}
