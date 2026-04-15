import { useEffect, useState, useCallback } from 'react'
import { Link } from 'react-router-dom'
import { api, formatDateTime, type AuditEntry } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'
import { FormSelect } from '@/components/FormField'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { Pagination } from '@/components/Pagination'
import { Download, ChevronRight } from 'lucide-react'
import { DatePicker } from '@/components/DatePicker'
import { downloadCSV } from '@/lib/csv'

const PAGE_SIZE = 50

function describeAction(entry: AuditEntry): string {
  const label = entry.resource_label || ''

  switch (entry.action) {
    case 'create': return `Created ${label || entry.resource_type}`
    case 'update': return `Updated ${label || entry.resource_type}`
    case 'delete': return `Deleted ${label || entry.resource_type}`
    case 'activate': return `Activated ${label || 'subscription'}`
    case 'cancel': return `Canceled ${label || 'subscription'}`
    case 'pause': return `Paused ${label || 'subscription'}`
    case 'resume': return `Resumed ${label || 'subscription'}`
    case 'finalize': return `Finalized ${label || 'invoice'}`
    case 'void': return `Voided ${label || 'invoice'}`
    case 'issue': return `Issued ${label || 'credit note'}`
    case 'resolve': return `Resolved ${label || 'dunning run'}`
    case 'grant': return `Granted credits${label ? ` to ${label}` : ''}`
    case 'adjust': return `Adjusted credits${label ? ` for ${label}` : ''}`
    case 'credit.adjustment': return `Adjusted credits${label ? ` for ${label}` : ''}`
    case 'credit.deduction': return `Deducted credits${label ? ` from ${label}` : ''}`
    case 'credit_note.issued': return `Issued credit note ${label}`
    case 'subscription.plan_changed': return `Changed plan${label ? ` for ${label}` : ''}`
    case 'revoke': return `Revoked API key ${label}`
    case 'run': return 'Billing cycle executed'
    case 'change_plan': return `Changed plan${label ? ` for ${label}` : ''}`
    default: return `${entry.action.replace(/_/g, ' ')} ${label || entry.resource_type}`
  }
}

// Severity: which actions are important vs routine
const HIGH_SEVERITY = new Set(['void', 'cancel', 'delete', 'revoke', 'credit.deduction'])
const MEDIUM_SEVERITY = new Set(['finalize', 'grant', 'issue', 'credit_note.issued', 'subscription.plan_changed', 'change_plan'])

function resourceLink(entry: AuditEntry): string | null {
  switch (entry.resource_type) {
    case 'invoice': return `/invoices/${entry.resource_id}`
    case 'customer': return `/customers/${entry.resource_id}`
    case 'subscription': return `/subscriptions/${entry.resource_id}`
    case 'plan': return `/plans/${entry.resource_id}`
    case 'meter': return `/meters/${entry.resource_id}`
    default: return null
  }
}

function formatActorName(entry: AuditEntry): string {
  if (entry.actor_type === 'system') return 'System'
  if (entry.actor_type === 'api_key') {
    // Show key ID prefix if available
    return entry.actor_id.startsWith('vlx_') ? `Key ${entry.actor_id.slice(0, 16)}...` : 'API Key'
  }
  return entry.actor_type
}

function formatMetadata(meta: Record<string, unknown> | undefined): { label: string; value: string }[] {
  if (!meta) return []
  const items: { label: string; value: string }[] = []
  for (const [key, val] of Object.entries(meta)) {
    if (key === 'resource_label') continue // Already shown in description
    const label = key.replace(/_/g, ' ').replace(/\b\w/g, c => c.toUpperCase())
    if (typeof val === 'number' && key.includes('cents')) {
      items.push({ label: label.replace(' Cents', ''), value: `$${(val / 100).toFixed(2)}` })
    } else if (val !== null && val !== undefined && val !== '') {
      items.push({ label, value: String(val) })
    }
  }
  return items
}

function groupByDate(entries: AuditEntry[]): { date: string; entries: AuditEntry[] }[] {
  const groups: { date: string; entries: AuditEntry[] }[] = []
  let currentDate = ''
  for (const entry of entries) {
    const date = new Date(entry.created_at).toLocaleDateString('en-US', {
      year: 'numeric', month: 'long', day: 'numeric',
    })
    if (date !== currentDate) {
      groups.push({ date, entries: [] })
      currentDate = date
    }
    groups[groups.length - 1].entries.push(entry)
  }
  return groups
}

export function AuditLogPage() {
  const [entries, setEntries] = useState<AuditEntry[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [resourceType, setResourceType] = useState('')
  const [action, setAction] = useState('')
  const [actorFilter, setActorFilter] = useState('')
  const [resourceIdFilter, setResourceIdFilter] = useState('')
  const [dateFrom, setDateFrom] = useState('')
  const [dateTo, setDateTo] = useState('')
  const [expandedId, setExpandedId] = useState<string | null>(null)
  const [page, setPage] = useState(1)

  // Server-side paginated fetch
  const loadEntries = useCallback(() => {
    setLoading(true)
    setError(null)
    const params = new URLSearchParams()
    params.set('limit', String(PAGE_SIZE))
    params.set('offset', String((page - 1) * PAGE_SIZE))
    if (resourceType) params.set('resource_type', resourceType)
    if (action) params.set('action', action)
    if (resourceIdFilter) params.set('resource_id', resourceIdFilter)
    if (actorFilter) params.set('actor_id', actorFilter)
    if (dateFrom) params.set('date_from', dateFrom)
    if (dateTo) params.set('date_to', dateTo)
    const qs = params.toString()
    api.listAuditLog(qs)
      .then(res => { setEntries(res.data || []); setTotal(res.total || 0); setLoading(false) })
      .catch(err => { setError(err instanceof Error ? err.message : 'Failed to load audit log'); setEntries([]); setTotal(0); setLoading(false) })
  }, [page, resourceType, action, resourceIdFilter, actorFilter, dateFrom, dateTo])

  useEffect(() => { loadEntries() }, [loadEntries])

  const totalPages = Math.ceil(total / PAGE_SIZE)
  const groups = groupByDate(entries)

  return (
    <Layout>
      <div className="flex justify-between items-center">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900 dark:text-gray-100">Audit Log</h1>
          <p className="text-sm text-gray-600 dark:text-gray-400 mt-1">Track all changes across your billing system</p>
        </div>
        {entries.length > 0 && (
          <button
            onClick={() => {
              // Export fetches all data for CSV
              const exportParams = new URLSearchParams()
              if (resourceType) exportParams.set('resource_type', resourceType)
              if (action) exportParams.set('action', action)
              if (resourceIdFilter) exportParams.set('resource_id', resourceIdFilter)
              if (actorFilter) exportParams.set('actor_id', actorFilter)
              if (dateFrom) exportParams.set('date_from', dateFrom)
              if (dateTo) exportParams.set('date_to', dateTo)
              const qs = exportParams.toString()
              api.listAuditLog(qs || undefined).then(res => {
                const rows = (res.data || []).map(e => [
                  formatActorName(e),
                  e.action,
                  e.resource_type,
                  e.resource_id,
                  e.resource_label || '',
                  formatDateTime(e.created_at),
                ])
                downloadCSV('audit-log.csv', ['Actor', 'Action', 'Resource Type', 'Resource ID', 'Resource Label', 'Date'], rows)
              })
            }}
            className="flex items-center gap-2 px-4 py-2 border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-300 rounded-lg text-sm font-medium hover:bg-gray-50 dark:hover:bg-gray-800 shadow-sm transition-colors"
          >
            <Download size={16} /> Export
          </button>
        )}
      </div>

      {/* Filters */}
      <div className="flex flex-wrap items-center gap-3 mt-6">
        <div className="w-44">
          <FormSelect label="" value={resourceType}
            onChange={e => { setResourceType(e.target.value); setPage(1) }}
            placeholder="All resources"
            options={[
              { value: 'customer', label: 'Customer' },
              { value: 'subscription', label: 'Subscription' },
              { value: 'invoice', label: 'Invoice' },
              { value: 'plan', label: 'Plan' },
              { value: 'meter', label: 'Meter' },
              { value: 'credit', label: 'Credit' },
              { value: 'credit_note', label: 'Credit Note' },
              { value: 'api_key', label: 'API Key' },
              { value: 'billing', label: 'Billing' },
              { value: 'billing_profile', label: 'Billing Profile' },
            ]} />
        </div>
        <div className="w-44">
          <FormSelect label="" value={action}
            onChange={e => { setAction(e.target.value); setPage(1) }}
            placeholder="All actions"
            options={[
              { value: 'create', label: 'Create' },
              { value: 'update', label: 'Update' },
              { value: 'delete', label: 'Delete' },
              { value: 'activate', label: 'Activate' },
              { value: 'cancel', label: 'Cancel' },
              { value: 'pause', label: 'Pause' },
              { value: 'resume', label: 'Resume' },
              { value: 'finalize', label: 'Finalize' },
              { value: 'void', label: 'Void' },
              { value: 'run', label: 'Billing Run' },
              { value: 'grant', label: 'Grant Credits' },
              { value: 'credit.adjustment', label: 'Credit Adjustment' },
              { value: 'credit.deduction', label: 'Credit Deduction' },
              { value: 'credit_note.issued', label: 'Credit Note Issued' },
              { value: 'subscription.plan_changed', label: 'Plan Changed' },
              { value: 'revoke', label: 'Revoke' },
            ]} />
        </div>
        <input type="text" value={actorFilter} onChange={e => { setActorFilter(e.target.value); setPage(1) }}
          placeholder="Filter by actor..."
          className="w-40 px-3 py-1.5 border border-gray-300 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-velox-500 bg-white" />
        <input type="text" value={resourceIdFilter} onChange={e => { setResourceIdFilter(e.target.value); setPage(1) }}
          placeholder="Filter by resource ID..."
          className="w-44 px-3 py-1.5 border border-gray-300 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-velox-500 bg-white" />
        <div className="w-36">
          <DatePicker value={dateFrom} onChange={v => { setDateFrom(v); setPage(1) }} placeholder="From" clearable />
        </div>
        <div className="w-36">
          <DatePicker value={dateTo} onChange={v => { setDateTo(v); setPage(1) }} placeholder="To" clearable />
        </div>
        {(resourceType || action || actorFilter || resourceIdFilter || dateFrom || dateTo) && (
          <button onClick={() => {
            setResourceType(''); setAction(''); setActorFilter(''); setResourceIdFilter(''); setDateFrom(''); setDateTo(''); setPage(1)
          }}
            className="px-3 py-1.5 text-sm text-gray-600 hover:text-gray-900 border border-gray-300 rounded-lg hover:bg-gray-50 transition-colors">
            Clear all
          </button>
        )}
      </div>

      {/* Timeline */}
      <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card mt-4">
        {error ? <ErrorState message={error} onRetry={loadEntries} />
        : loading ? <LoadingSkeleton rows={8} columns={3} />
        : entries.length === 0 ? <EmptyState title="No audit entries" description="Actions will be recorded here automatically as you use the platform" />
        : (
          <>
          <div>
            {groups.map((group, gi) => (
              <div key={group.date}>
                <div className={`px-6 py-2 bg-gray-50 dark:bg-gray-800/50 text-xs font-medium text-gray-500 dark:text-gray-400 ${gi > 0 ? 'border-t border-gray-200 dark:border-gray-700' : ''}`}>
                  {group.date}
                </div>
                <div className="divide-y divide-gray-100 dark:divide-gray-800">
                  {group.entries.map(entry => {
                    const time = new Date(entry.created_at).toLocaleTimeString('en-US', { hour: 'numeric', minute: '2-digit' })
                    const isHigh = HIGH_SEVERITY.has(entry.action)
                    const isMedium = MEDIUM_SEVERITY.has(entry.action)
                    const isExpanded = expandedId === entry.id
                    const link = resourceLink(entry)
                    const meta = formatMetadata(entry.metadata)

                    return (
                      <div key={entry.id}>
                        <div
                          className={`flex items-center px-6 py-2.5 transition-colors cursor-pointer ${
                            isExpanded ? 'bg-gray-50' : 'hover:bg-gray-50/50'
                          } ${isHigh ? 'border-l-2 border-l-red-400' : isMedium ? 'border-l-2 border-l-amber-300' : 'border-l-2 border-l-transparent'}`}
                          onClick={() => setExpandedId(isExpanded ? null : entry.id)}
                        >
                          <span className="text-sm text-gray-400 w-20 shrink-0 tabular-nums">{time}</span>
                          <Badge status={entry.action} />
                          <span className="text-sm text-gray-900 ml-2.5 flex-1 truncate">
                            {describeAction(entry)}
                          </span>
                          {link && (
                            <Link
                              to={link}
                              onClick={e => e.stopPropagation()}
                              className="text-xs text-velox-600 hover:text-velox-700 hover:underline shrink-0 ml-3"
                            >
                              View
                            </Link>
                          )}
                          <span className="text-xs text-gray-500 shrink-0 ml-3 w-28 text-right truncate" title={entry.actor_id}>
                            {formatActorName(entry)}
                          </span>
                          <ChevronRight size={14} className={`text-gray-300 ml-2 shrink-0 transition-transform ${isExpanded ? 'rotate-90' : ''}`} />
                        </div>

                        {/* Expanded detail */}
                        {isExpanded && (
                          <div className="bg-gray-50 px-6 py-3 border-t border-gray-100 dark:border-gray-800">
                            <div className="grid grid-cols-2 lg:grid-cols-4 gap-3 text-sm">
                              <div>
                                <p className="text-xs text-gray-500">Resource Type</p>
                                <p className="text-gray-700 mt-0.5">{entry.resource_type.replace(/_/g, ' ')}</p>
                              </div>
                              <div>
                                <p className="text-xs text-gray-500">Resource ID</p>
                                <p className="text-gray-700 font-mono text-xs mt-0.5 truncate" title={entry.resource_id}>{entry.resource_id}</p>
                              </div>
                              <div>
                                <p className="text-xs text-gray-500">Actor</p>
                                <p className="text-gray-700 mt-0.5">{formatActorName(entry)}</p>
                              </div>
                              <div>
                                <p className="text-xs text-gray-500">Timestamp</p>
                                <p className="text-gray-700 mt-0.5">{formatDateTime(entry.created_at)}</p>
                              </div>
                            </div>
                            {meta.length > 0 && (
                              <div className="mt-3 pt-3 border-t border-gray-200 dark:border-gray-700">
                                <p className="text-xs font-medium text-gray-500 dark:text-gray-400 mb-2">Details</p>
                                <div className="grid grid-cols-2 lg:grid-cols-3 gap-2">
                                  {meta.map(m => (
                                    <div key={m.label} className="bg-white rounded-lg px-3 py-2 border border-gray-100">
                                      <p className="text-xs text-gray-500">{m.label}</p>
                                      <p className="text-sm text-gray-900 mt-0.5 font-mono truncate" title={m.value}>{m.value}</p>
                                    </div>
                                  ))}
                                </div>
                              </div>
                            )}
                          </div>
                        )}
                      </div>
                    )
                  })}
                </div>
              </div>
            ))}
          </div>
          <Pagination page={page} totalPages={totalPages} onPageChange={setPage} />
          </>
        )}
      </div>
    </Layout>
  )
}
