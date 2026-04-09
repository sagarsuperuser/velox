import { useEffect, useState } from 'react'
import { api, formatDateTime, type AuditEntry } from '@/lib/api'
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

export function AuditLogPage() {
  const [entries, setEntries] = useState<AuditEntry[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [resourceType, setResourceType] = useState('')
  const [action, setAction] = useState('')
  const [page, setPage] = useState(1)
  const pageSize = 25
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
          return (
          <>
          <div className="overflow-x-auto">
          <table className="w-full">
            <thead>
              <tr className="border-b border-gray-100 bg-gray-50">
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3 w-44">Timestamp</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Event</th>
                <th className="text-right text-xs font-medium text-gray-500 px-6 py-3 w-24">Actor</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-50">
              {paginated.map(entry => (
                <tr key={entry.id} className="hover:bg-gray-50/50 transition-colors group">
                  <td className="px-6 py-3 text-sm text-gray-500 whitespace-nowrap align-top">{formatDateTime(entry.created_at)}</td>
                  <td className="px-6 py-3">
                    <div className="flex items-start gap-2.5">
                      <Badge status={entry.action} />
                      <span className="text-sm text-gray-900">{describeAction(entry)}</span>
                    </div>
                  </td>
                  <td className="px-6 py-3 text-sm text-gray-500 text-right align-top">
                    {entry.actor_type === 'api_key' ? 'API Key' : entry.actor_type === 'system' ? 'System' : entry.actor_type}
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
