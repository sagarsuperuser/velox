import { useEffect, useState } from 'react'
import { api, formatDateTime, type UsageEvent, type Customer, type Meter } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { FormSelect } from '@/components/FormField'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { Pagination } from '@/components/Pagination'

export function UsageEventsPage() {
  const [events, setEvents] = useState<UsageEvent[]>([])
  const [customers, setCustomers] = useState<Customer[]>([])
  const [meters, setMeters] = useState<Meter[]>([])
  const [customerMap, setCustomerMap] = useState<Record<string, Customer>>({})
  const [meterMap, setMeterMap] = useState<Record<string, Meter>>({})
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  // Filters
  const [filterCustomer, setFilterCustomer] = useState('')
  const [filterMeter, setFilterMeter] = useState('')
  const [page, setPage] = useState(1)
  const pageSize = 25

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

  const loadEvents = () => {
    setLoading(true)
    setError(null)
    const parts: string[] = []
    if (filterCustomer) parts.push(`customer_id=${filterCustomer}`)
    if (filterMeter) parts.push(`meter_id=${filterMeter}`)
    const params = parts.length > 0 ? parts.join('&') : undefined
    api.listUsageEvents(params)
      .then(res => { setEvents(res.data || []); setLoading(false) })
      .catch(err => { setError(err instanceof Error ? err.message : 'Failed to load usage events'); setEvents([]); setLoading(false) })
  }

  useEffect(() => { loadRefs() }, [])
  useEffect(() => { loadEvents() }, [filterCustomer, filterMeter])

  const totalQuantity = events.reduce((sum, e) => sum + e.quantity, 0)

  return (
    <Layout>
      <div>
        <h1 className="text-2xl font-semibold text-gray-900">Usage Events</h1>
        <p className="text-sm text-gray-500 mt-1">View ingested usage events</p>
      </div>

      {/* Filter bar */}
      <div className="flex items-center gap-4 mt-6">
        <div className="w-48">
          <FormSelect value={filterCustomer}
            label=""
            onChange={e => { setFilterCustomer(e.target.value); setPage(1) }}
            placeholder="All customers"
            options={customers.map(c => ({ value: c.id, label: c.display_name }))} />
        </div>
        <div className="w-48">
          <FormSelect value={filterMeter}
            label=""
            onChange={e => { setFilterMeter(e.target.value); setPage(1) }}
            placeholder="All meters"
            options={meters.map(m => ({ value: m.id, label: m.name }))} />
        </div>
        {events.length > 0 && (
          <div className="ml-auto text-sm text-gray-500">
            {events.length.toLocaleString()} event{events.length !== 1 ? 's' : ''} · {totalQuantity.toLocaleString()} total units
          </div>
        )}
      </div>

      <div className="bg-white rounded-xl shadow-card mt-4">
        {error ? (
          <ErrorState message={error} onRetry={loadEvents} />
        ) : loading ? (
          <LoadingSkeleton rows={6} columns={4} />
        ) : events.length === 0 ? (
          <EmptyState title="No usage events" description="Usage events will appear here once ingested via the API" />
        ) : (
          (() => {
            const totalPages = Math.ceil(events.length / pageSize)
            const currentPage = Math.min(page, totalPages || 1)
            const paginated = events.slice((currentPage - 1) * pageSize, currentPage * pageSize)
            return (
            <>
            <div className="overflow-x-auto">
            <table className="w-full">
              <thead>
                <tr className="border-b border-gray-100 bg-gray-50">
                  <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Timestamp</th>
                  <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Customer</th>
                  <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Meter</th>
                  <th className="text-right text-xs font-medium text-gray-500 px-6 py-3">Quantity</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-50">
                {paginated.map(ev => (
                  <tr key={ev.id} className="hover:bg-gray-50/50 transition-colors">
                    <td className="px-6 py-3 text-sm text-gray-900">{formatDateTime(ev.timestamp)}</td>
                    <td className="px-6 py-3 text-sm text-gray-500">
                      {customerMap[ev.customer_id]?.display_name || ev.customer_id.slice(0, 8) + '...'}
                    </td>
                    <td className="px-6 py-3 text-sm text-gray-500">
                      {meterMap[ev.meter_id]?.name || ev.meter_id.slice(0, 8) + '...'}
                    </td>
                    <td className="px-6 py-3 text-sm font-medium text-gray-900 text-right tabular-nums">
                      {ev.quantity.toLocaleString()}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            </div>
            <Pagination page={currentPage} totalPages={totalPages} onPageChange={setPage} />
            </>
            )
          })()
        )}
      </div>
    </Layout>
  )
}
