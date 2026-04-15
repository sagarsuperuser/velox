import { useEffect, useState, useMemo, useCallback } from 'react'
import { api, formatDateTime, type UsageEvent, type Customer, type Meter } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { SearchSelect } from '@/components/SearchSelect'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { Pagination } from '@/components/Pagination'
import { downloadCSV } from '@/lib/csv'
import { Download, Activity, Hash, Gauge, Users } from 'lucide-react'
import { DatePicker } from '@/components/DatePicker'

const PAGE_SIZE = 25

export function UsageEventsPage() {
  const [events, setEvents] = useState<UsageEvent[]>([])
  const [total, setTotal] = useState(0)
  const [customers, setCustomers] = useState<Customer[]>([])
  const [meters, setMeters] = useState<Meter[]>([])
  const [customerMap, setCustomerMap] = useState<Record<string, Customer>>({})
  const [meterMap, setMeterMap] = useState<Record<string, Meter>>({})
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  // Filters
  const [filterCustomer, setFilterCustomer] = useState('')
  const [filterMeter, setFilterMeter] = useState('')
  const [filterFrom, setFilterFrom] = useState('')
  const [filterTo, setFilterTo] = useState('')
  const [page, setPage] = useState(1)

  const loadRefs = () => {
    Promise.all([
      api.listCustomers().catch(() => ({ data: [] as Customer[], total: 0 })),
      api.listMeters().catch(() => ({ data: [] as Meter[] })),
    ]).then(([custRes, meterRes]) => {
      setCustomers(custRes.data)
      setMeters(meterRes.data)
      const cMap: Record<string, Customer> = {}
      custRes.data.forEach(c => { cMap[c.id] = c })
      setCustomerMap(cMap)
      const mMap: Record<string, Meter> = {}
      meterRes.data.forEach(m => { mMap[m.id] = m })
      setMeterMap(mMap)
    })
  }

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
    const params = parts.join('&')
    api.listUsageEvents(params)
      .then(res => { setEvents(res.data || []); setTotal(res.total || 0); setLoading(false) })
      .catch(err => { setError(err instanceof Error ? err.message : 'Failed to load usage events'); setEvents([]); setTotal(0); setLoading(false) })
  }, [page, filterCustomer, filterMeter, filterFrom, filterTo])

  useEffect(() => { loadRefs() }, [])
  useEffect(() => { loadEvents() }, [loadEvents])

  // Computed stats from current page data
  const stats = useMemo(() => {
    const totalEvents = total
    const totalUnits = events.reduce((sum, e) => sum + e.quantity, 0)
    const activeMeters = new Set(events.map(e => e.meter_id)).size
    const activeCustomers = new Set(events.map(e => e.customer_id)).size
    return { totalEvents, totalUnits, activeMeters, activeCustomers }
  }, [events, total])

  // Meter breakdown from current page data
  const meterBreakdown = useMemo(() => {
    const grouped: Record<string, number> = {}
    for (const e of events) {
      grouped[e.meter_id] = (grouped[e.meter_id] || 0) + e.quantity
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

  const statCards = [
    { label: 'Total Events', value: stats.totalEvents.toLocaleString(), icon: Activity, color: 'text-velox-600 bg-velox-50' },
    { label: 'Page Units', value: stats.totalUnits.toLocaleString(), icon: Hash, color: 'text-blue-600 bg-blue-50' },
    { label: 'Active Meters', value: String(stats.activeMeters), icon: Gauge, color: 'text-amber-600 bg-amber-50' },
    { label: 'Active Customers', value: String(stats.activeCustomers), icon: Users, color: 'text-emerald-600 bg-emerald-50' },
  ]

  return (
    <Layout>
      {/* Header */}
      <div className="flex justify-between items-center">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900 dark:text-gray-100">Usage Events</h1>
          <p className="text-sm text-gray-600 dark:text-gray-400 mt-1">Track and analyze usage across customers and meters</p>
        </div>
        {total > 0 && (
          <button
            onClick={() => {
              // Export fetches all data for CSV
              const parts: string[] = []
              if (filterCustomer) parts.push(`customer_id=${filterCustomer}`)
              if (filterMeter) parts.push(`meter_id=${filterMeter}`)
              if (filterFrom) parts.push(`from=${new Date(filterFrom).toISOString()}`)
              if (filterTo) parts.push(`to=${new Date(filterTo + 'T23:59:59').toISOString()}`)
              const exportParams = parts.length > 0 ? parts.join('&') : undefined
              api.listUsageEvents(exportParams).then(res => {
                const rows = (res.data || []).map(ev => [
                  formatDateTime(ev.timestamp),
                  customerMap[ev.customer_id]?.display_name || ev.customer_id,
                  meterMap[ev.meter_id]?.name || ev.meter_id,
                  String(ev.quantity),
                ])
                downloadCSV('usage-events.csv', ['Timestamp', 'Customer', 'Meter', 'Quantity'], rows)
              })
            }}
            className="flex items-center gap-2 px-4 py-2 border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-300 rounded-lg text-sm font-medium hover:bg-gray-50 dark:hover:bg-gray-800 shadow-sm transition-colors"
          >
            <Download size={16} />
            Export CSV
          </button>
        )}
      </div>

      {/* Filter bar */}
      <div className="flex items-center gap-3 mt-6">
        <div className="w-52">
          <SearchSelect value={filterCustomer}
            onChange={(v) => { setFilterCustomer(v); setPage(1) }}
            placeholder="All customers"
            options={customers.map(c => ({ value: c.id, label: c.display_name, sublabel: c.external_id }))} />
        </div>
        <div className="w-52">
          <SearchSelect value={filterMeter}
            onChange={(v) => { setFilterMeter(v); setPage(1) }}
            placeholder="All meters"
            options={meters.map(m => ({ value: m.id, label: m.name, sublabel: m.key }))} />
        </div>
        <div className="w-36">
          <DatePicker value={filterFrom} onChange={v => { setFilterFrom(v); setPage(1) }} placeholder="From" />
        </div>
        <div className="w-36">
          <DatePicker value={filterTo} onChange={v => { setFilterTo(v); setPage(1) }} placeholder="To" />
        </div>
      </div>

      {error ? (
        <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card mt-6">
          <ErrorState message={error} onRetry={loadEvents} />
        </div>
      ) : loading ? (
        <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card mt-6">
          <LoadingSkeleton rows={6} columns={4} />
        </div>
      ) : events.length === 0 ? (
        <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card mt-6">
          <EmptyState
            title="No usage events yet"
            description="Ingest events via the API to start tracking usage"
          />
        </div>
      ) : (
        <>
          {/* Stat cards */}
          <div className="grid grid-cols-4 gap-4 mt-6">
            {statCards.map(card => (
              <div key={card.label} className="bg-white dark:bg-gray-900 rounded-xl shadow-card px-5 py-4">
                <div className="flex items-center gap-3">
                  <div className={`p-2 rounded-lg ${card.color}`}>
                    <card.icon size={18} />
                  </div>
                  <div>
                    <p className="text-xs font-medium text-gray-500 dark:text-gray-400">{card.label}</p>
                    <p className="text-xl font-semibold text-gray-900 dark:text-gray-100 tabular-nums mt-0.5">{card.value}</p>
                  </div>
                </div>
              </div>
            ))}
          </div>

          {/* Meter breakdown */}
          {meterBreakdown.length > 0 && (
            <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card mt-6">
              <div className="px-6 py-4 border-b border-gray-100 dark:border-gray-800">
                <h2 className="text-sm font-semibold text-gray-900 dark:text-gray-100">Usage by Meter</h2>
              </div>
              <div className="px-6 py-4 space-y-3">
                {meterBreakdown.map(m => (
                  <div key={m.id} className="flex items-center gap-3">
                    <span className="text-sm text-gray-700 w-32 shrink-0 truncate">{m.name}</span>
                    <div className="flex-1 h-2 bg-gray-100 rounded-full overflow-hidden">
                      <div className="h-full bg-velox-500 rounded-full" style={{ width: `${m.pct}%` }} />
                    </div>
                    <span className="text-sm font-medium text-gray-900 dark:text-gray-100 tabular-nums w-24 text-right">{m.total.toLocaleString()} {m.unit}</span>
                  </div>
                ))}
              </div>
            </div>
          )}

          {/* Events table */}
          <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card mt-6">
            <div className="px-6 py-4 border-b border-gray-100 dark:border-gray-800">
              <h2 className="text-sm font-semibold text-gray-900 dark:text-gray-100">Event Log</h2>
            </div>
            <div className="overflow-x-auto">
              <table className="w-full">
                <thead>
                  <tr className="border-b border-gray-100 dark:border-gray-800 bg-gray-50 dark:bg-gray-800/50">
                    <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Timestamp</th>
                    <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Customer</th>
                    <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Meter</th>
                    <th className="text-right text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Quantity</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
                  {events.map(ev => (
                    <tr key={ev.id} className="hover:bg-gray-50/50 dark:hover:bg-gray-800/50 transition-colors">
                      <td className="px-6 py-3 text-sm text-gray-900 dark:text-gray-100">{formatDateTime(ev.timestamp)}</td>
                      <td className="px-6 py-3 text-sm text-gray-600 dark:text-gray-400">
                        {customerMap[ev.customer_id]?.display_name || ev.customer_id.slice(0, 8) + '...'}
                      </td>
                      <td className="px-6 py-3 text-sm text-gray-600 dark:text-gray-400">
                        {meterMap[ev.meter_id]?.name || ev.meter_id.slice(0, 8) + '...'}
                      </td>
                      <td className={`px-6 py-3 text-sm font-medium text-right tabular-nums ${ev.quantity < 0 ? 'text-red-600' : 'text-gray-900'}`}>
                        {ev.quantity < 0 ? '' : '+'}{ev.quantity.toLocaleString()}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            <Pagination page={page} totalPages={totalPages} onPageChange={setPage} />
          </div>
        </>
      )}
    </Layout>
  )
}
