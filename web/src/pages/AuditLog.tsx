import { useEffect, useState } from 'react'
import { api, type AuditEntry } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'
import { FormSelect } from '@/components/FormField'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { Pagination } from '@/components/Pagination'

function describeAction(entry: AuditEntry): string {
  const resource = entry.resource_type.replace(/_/g, ' ')
  const label = entry.resource_label || ''

  switch (entry.action) {
    case 'create': return `Created ${resource}${label ? ` "${label}"` : ''}`
    case 'update': return `Updated ${resource}${label ? ` "${label}"` : ''}`
    case 'delete': return `Deleted ${resource}${label ? ` "${label}"` : ''}`
    case 'activate': return `Activated ${resource}${label ? ` "${label}"` : ''}`
    case 'cancel': return `Canceled ${resource}${label ? ` "${label}"` : ''}`
    case 'pause': return `Paused ${resource}${label ? ` "${label}"` : ''}`
    case 'resume': return `Resumed ${resource}${label ? ` "${label}"` : ''}`
    case 'finalize': return `Finalized invoice${label ? ` ${label}` : ''}`
    case 'void': return `Voided invoice${label ? ` ${label}` : ''}`
    case 'issue': return `Issued credit note${label ? ` ${label}` : ''}`
    case 'resolve': return `Resolved dunning run${label ? ` ${label}` : ''}`
    case 'grant': return `Granted credits${label ? ` to "${label}"` : ''}`
    case 'adjust': return `Adjusted credits${label ? ` for "${label}"` : ''}`
    case 'revoke': return `Revoked API key${label ? ` "${label}"` : ''}`
    case 'run': return 'Billing cycle executed'
    case 'change_plan': return `Changed plan${label ? ` for "${label}"` : ''}`
    default: return `${entry.action} ${resource}${label ? ` "${label}"` : ''}`
  }
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
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [resourceType, setResourceType] = useState('')
  const [action, setAction] = useState('')
  const [page, setPage] = useState(1)
  const pageSize = 50

  const loadEntries = () => {
    setLoading(true)
    setError(null)
    const params = new URLSearchParams()
    if (resourceType) params.set('resource_type', resourceType)
    if (action) params.set('action', action)
    const qs = params.toString()
    api.listAuditLog(qs || undefined)
      .then(res => { setEntries(res.data || []); setLoading(false) })
      .catch(err => { setError(err instanceof Error ? err.message : 'Failed to load audit log'); setEntries([]); setLoading(false) })
  }

  useEffect(() => { loadEntries() }, [resourceType, action])

  return (
    <Layout>
      <div>
        <h1 className="text-2xl font-semibold text-gray-900">Audit Log</h1>
        <p className="text-sm text-gray-500 mt-1">Track all changes across your billing system</p>
      </div>

      {/* Filters */}
      <div className="flex items-center gap-4 mt-6">
        <div className="w-48">
          <FormSelect label="" value={resourceType}
            onChange={e => { setResourceType(e.target.value); setPage(1) }}
            placeholder="All resources"
            options={[
              { value: 'customer', label: 'Customer' },
              { value: 'subscription', label: 'Subscription' },
              { value: 'invoice', label: 'Invoice' },
              { value: 'plan', label: 'Plan' },
              { value: 'meter', label: 'Meter' },
              { value: 'credit_note', label: 'Credit Note' },
              { value: 'api_key', label: 'API Key' },
              { value: 'billing', label: 'Billing' },
              { value: 'billing_profile', label: 'Billing Profile' },
            ]} />
        </div>
        <div className="w-48">
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
              { value: 'grant', label: 'Grant' },
              { value: 'revoke', label: 'Revoke' },
            ]} />
        </div>
        {entries.length > 0 && (
          <span className="ml-auto text-sm text-gray-500">{entries.length} entries</span>
        )}
      </div>

      <div className="bg-white rounded-xl shadow-card mt-4">
        {error ? <ErrorState message={error} onRetry={loadEntries} />
        : loading ? <LoadingSkeleton rows={8} columns={3} />
        : entries.length === 0 ? <EmptyState title="No audit entries" description="Actions will be recorded here automatically" />
        : (
          (() => {
          const totalPages = Math.ceil(entries.length / pageSize)
          const currentPage = Math.min(page, totalPages || 1)
          const paginated = entries.slice((currentPage - 1) * pageSize, currentPage * pageSize)
          const groups = groupByDate(paginated)
          return (
          <>
          <div>
            {groups.map((group, gi) => (
              <div key={group.date}>
                {/* Day header */}
                <div className={`px-6 py-2 bg-gray-50 text-xs font-medium text-gray-500 ${gi > 0 ? 'border-t border-gray-100' : ''}`}>
                  {group.date}
                </div>
                {/* Events for this day */}
                <div className="divide-y divide-gray-50">
                  {group.entries.map(entry => {
                    const time = new Date(entry.created_at).toLocaleTimeString('en-US', {
                      hour: 'numeric', minute: '2-digit',
                    })
                    return (
                      <div key={entry.id} className="flex items-center px-6 py-2 hover:bg-gray-50/50 transition-colors">
                        <span className="text-sm text-gray-400 w-20 shrink-0">{time}</span>
                        <Badge status={entry.action} />
                        <span className="text-sm text-gray-900 ml-2.5 flex-1 truncate">{describeAction(entry)}</span>
                        <span className="text-sm text-gray-400 shrink-0 ml-4">
                          {entry.actor_type === 'api_key' ? 'API Key' : entry.actor_type === 'system' ? 'System' : entry.actor_type}
                        </span>
                      </div>
                    )
                  })}
                </div>
              </div>
            ))}
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
