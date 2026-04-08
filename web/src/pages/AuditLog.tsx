import { useEffect, useState, Fragment } from 'react'
import { api, formatDate, type AuditEntry } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { useToast } from '@/components/Toast'

export function AuditLogPage() {
  const [entries, setEntries] = useState<AuditEntry[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [resourceType, setResourceType] = useState('all')
  const [action, setAction] = useState('all')
  const [expandedMeta, setExpandedMeta] = useState<Set<string>>(new Set())
  const toast = useToast()

  const loadEntries = () => {
    setLoading(true)
    setError(null)
    const params = new URLSearchParams()
    if (resourceType !== 'all') params.set('resource_type', resourceType)
    if (action !== 'all') params.set('action', action)
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
      <div className="flex gap-3 mt-6">
        <select value={resourceType} onChange={e => setResourceType(e.target.value)}
          className="px-3 py-2 border border-gray-200 rounded-lg shadow-sm text-sm focus:outline-none focus:ring-2 focus:ring-velox-500 bg-white">
          <option value="all">All Resources</option>
          <option value="customer">Customer</option>
          <option value="subscription">Subscription</option>
          <option value="invoice">Invoice</option>
          <option value="plan">Plan</option>
          <option value="meter">Meter</option>
          <option value="api_key">API Key</option>
        </select>
        <select value={action} onChange={e => setAction(e.target.value)}
          className="px-3 py-2 border border-gray-200 rounded-lg shadow-sm text-sm focus:outline-none focus:ring-2 focus:ring-velox-500 bg-white">
          <option value="all">All Actions</option>
          <option value="create">Create</option>
          <option value="update">Update</option>
          <option value="delete">Delete</option>
          <option value="activate">Activate</option>
          <option value="cancel">Cancel</option>
          <option value="finalize">Finalize</option>
          <option value="void">Void</option>
        </select>
      </div>

      <div className="bg-white rounded-xl shadow-card mt-4">
        {error ? <ErrorState message={error} onRetry={loadEntries} />
        : loading ? <LoadingSkeleton rows={8} columns={6} />
        : entries.length === 0 ? <EmptyState title="No audit entries" description="Actions will be recorded here automatically" />
        : (
          <div className="overflow-x-auto">
          <table className="w-full">
            <thead>
              <tr className="border-b border-gray-100">
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Timestamp</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Actor</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Action</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Resource Type</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Resource ID</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Metadata</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-50">
              {entries.map(entry => {
                const hasMeta = entry.metadata && Object.keys(entry.metadata).length > 0
                const isExpanded = expandedMeta.has(entry.id)
                const metaPath = hasMeta && typeof entry.metadata === 'object' && 'path' in entry.metadata
                  ? (entry.metadata as Record<string, unknown>).path as string
                  : null
                return (
                  <Fragment key={entry.id}>
                    <tr className="hover:bg-gray-50">
                      <td className="px-6 py-3 text-sm text-gray-500">{formatDate(entry.created_at)}</td>
                      <td className="px-6 py-3 text-sm text-gray-500">{entry.actor_type === 'api_key' ? 'API Key' : entry.actor_type === 'system' ? 'System' : entry.actor_type}</td>
                      <td className="px-6 py-3"><Badge status={entry.action} /></td>
                      <td className="px-6 py-3 text-sm text-gray-500">{entry.resource_type}</td>
                      <td className="px-6 py-3">
                        <span
                          className="text-sm font-mono text-gray-500 cursor-pointer hover:text-gray-700"
                          title="Click to copy"
                          onClick={() => {
                            navigator.clipboard.writeText(entry.resource_id)
                            toast.success('Copied')
                          }}
                        >
                          {entry.resource_id}
                        </span>
                      </td>
                      <td className="px-6 py-3 text-sm text-gray-400">
                        {!hasMeta ? '\u2014' : metaPath && !isExpanded ? (
                          <span className="flex items-center gap-2">
                            <span className="font-mono text-xs">{metaPath}</span>
                            <button onClick={() => setExpandedMeta(prev => { const next = new Set(prev); next.add(entry.id); return next })}
                              className="text-xs text-velox-600 hover:underline">View</button>
                          </span>
                        ) : !isExpanded ? (
                          <button onClick={() => setExpandedMeta(prev => { const next = new Set(prev); next.add(entry.id); return next })}
                            className="text-xs text-velox-600 hover:underline">View</button>
                        ) : (
                          <button onClick={() => setExpandedMeta(prev => { const next = new Set(prev); next.delete(entry.id); return next })}
                            className="text-xs text-velox-600 hover:underline">Hide</button>
                        )}
                      </td>
                    </tr>
                    {isExpanded && hasMeta && (
                      <tr>
                        <td colSpan={6} className="px-6 py-3 bg-gray-50">
                          <pre className="text-xs text-gray-600 font-mono whitespace-pre-wrap">{JSON.stringify(entry.metadata, null, 2)}</pre>
                        </td>
                      </tr>
                    )}
                  </Fragment>
                )
              })}
            </tbody>
          </table>
          </div>
        )}
      </div>
    </Layout>
  )
}
