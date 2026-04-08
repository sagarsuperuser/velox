import { useEffect, useState } from 'react'
import { api, formatDate, type UsageEvent, type Customer, type Meter } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'

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
  const [filterFrom, setFilterFrom] = useState('')
  const [filterTo, setFilterTo] = useState('')

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
    if (filterFrom) parts.push(`from=${filterFrom}`)
    if (filterTo) parts.push(`to=${filterTo}`)
    const params = parts.length > 0 ? parts.join('&') : undefined
    api.listUsageEvents(params)
      .then(res => { setEvents(res.data || []); setLoading(false) })
      .catch(err => { setError(err instanceof Error ? err.message : 'Failed to load usage events'); setEvents([]); setLoading(false) })
  }

  useEffect(() => { loadRefs() }, [])
  useEffect(() => { loadEvents() }, [filterCustomer, filterMeter, filterFrom, filterTo])

  return (
    <Layout>
      <div>
        <h1 className="text-2xl font-semibold text-gray-900">Usage Events</h1>
        <p className="text-sm text-gray-500 mt-1">View ingested usage events</p>
      </div>

      {/* Filter bar */}
      <div className="flex flex-wrap items-end gap-3 mt-6">
        <div>
          <label className="block text-xs font-medium text-gray-500 mb-1">Customer</label>
          <select value={filterCustomer} onChange={e => setFilterCustomer(e.target.value)}
            className="px-3 py-2 border border-gray-200 rounded-lg shadow-sm text-sm focus:outline-none focus:ring-2 focus:ring-velox-500 bg-white">
            <option value="">All customers</option>
            {customers.map(c => <option key={c.id} value={c.id}>{c.display_name}</option>)}
          </select>
        </div>
        <div>
          <label className="block text-xs font-medium text-gray-500 mb-1">Meter</label>
          <select value={filterMeter} onChange={e => setFilterMeter(e.target.value)}
            className="px-3 py-2 border border-gray-200 rounded-lg shadow-sm text-sm focus:outline-none focus:ring-2 focus:ring-velox-500 bg-white">
            <option value="">All meters</option>
            {meters.map(m => <option key={m.id} value={m.id}>{m.name}</option>)}
          </select>
        </div>
        <div>
          <label className="block text-xs font-medium text-gray-500 mb-1">From</label>
          <input type="date" value={filterFrom} onChange={e => setFilterFrom(e.target.value)}
            className="px-3 py-2 border border-gray-200 rounded-lg shadow-sm text-sm focus:outline-none focus:ring-2 focus:ring-velox-500 bg-white" />
        </div>
        <div>
          <label className="block text-xs font-medium text-gray-500 mb-1">To</label>
          <input type="date" value={filterTo} onChange={e => setFilterTo(e.target.value)}
            className="px-3 py-2 border border-gray-200 rounded-lg shadow-sm text-sm focus:outline-none focus:ring-2 focus:ring-velox-500 bg-white" />
        </div>
      </div>

      <div className="bg-white rounded-xl shadow-card mt-4">
        {error ? (
          <ErrorState message={error} onRetry={loadEvents} />
        ) : loading ? (
          <LoadingSkeleton rows={6} columns={5} />
        ) : events.length === 0 ? (
          <EmptyState title="No usage events" description="Usage events will appear here once ingested via the API" />
        ) : (
          <div className="overflow-x-auto">
          <table className="w-full">
            <thead>
              <tr className="border-b border-gray-100">
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Timestamp</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Customer</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Meter</th>
                <th className="text-right text-xs font-medium text-gray-500 px-6 py-3">Quantity</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Subscription</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-50">
              {events.map(ev => (
                <tr key={ev.id} className="hover:bg-gray-50 transition-colors">
                  <td className="px-6 py-3 text-sm text-gray-700">{formatDate(ev.timestamp)}</td>
                  <td className="px-6 py-3 text-sm text-gray-600">
                    {customerMap[ev.customer_id]?.display_name || ev.customer_id.slice(0, 8) + '...'}
                  </td>
                  <td className="px-6 py-3 text-sm text-gray-600">
                    {meterMap[ev.meter_id]?.name || ev.meter_id.slice(0, 8) + '...'}
                  </td>
                  <td className="px-6 py-3 text-sm font-medium text-gray-900 text-right">
                    {ev.quantity.toLocaleString()}
                  </td>
                  <td className="px-6 py-3 text-sm font-mono text-gray-500">
                    {ev.subscription_id ? ev.subscription_id.slice(0, 12) + '...' : '\u2014'}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          </div>
        )}
      </div>
    </Layout>
  )
}
