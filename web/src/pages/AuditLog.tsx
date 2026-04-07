import { useEffect, useState } from 'react'
import { api, formatDate, type AuditEntry } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { EmptyState } from '@/components/EmptyState'

export function AuditLogPage() {
  const [entries, setEntries] = useState<AuditEntry[]>([])
  const [loading, setLoading] = useState(true)
  const [resourceType, setResourceType] = useState('all')
  const [action, setAction] = useState('all')

  const loadEntries = () => {
    setLoading(true)
    const params = new URLSearchParams()
    if (resourceType !== 'all') params.set('resource_type', resourceType)
    if (action !== 'all') params.set('action', action)
    const qs = params.toString()
    api.listAuditLog(qs || undefined)
      .then(res => { setEntries(res.data || []); setLoading(false) })
      .catch(() => { setEntries([]); setLoading(false) })
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
          className="px-3 py-2 border border-gray-300 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-velox-500 bg-white">
          <option value="all">All Resources</option>
          <option value="customer">Customer</option>
          <option value="subscription">Subscription</option>
          <option value="invoice">Invoice</option>
          <option value="plan">Plan</option>
          <option value="meter">Meter</option>
          <option value="api_key">API Key</option>
        </select>
        <select value={action} onChange={e => setAction(e.target.value)}
          className="px-3 py-2 border border-gray-300 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-velox-500 bg-white">
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

      <div className="bg-white rounded-xl border border-gray-200 mt-4">
        {loading ? <LoadingSkeleton rows={8} columns={6} />
        : entries.length === 0 ? <EmptyState title="No audit entries" description="Actions will be recorded here automatically" />
        : (
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
              {entries.map(entry => (
                <tr key={entry.id} className="hover:bg-gray-50">
                  <td className="px-6 py-3 text-sm text-gray-500">{formatDate(entry.created_at)}</td>
                  <td className="px-6 py-3"><Badge status={entry.actor_type} /></td>
                  <td className="px-6 py-3"><Badge status={entry.action} /></td>
                  <td className="px-6 py-3 text-sm text-gray-500">{entry.resource_type}</td>
                  <td className="px-6 py-3 text-sm font-mono text-gray-500">{entry.resource_id.slice(0, 12)}...</td>
                  <td className="px-6 py-3 text-sm text-gray-400">
                    {entry.metadata ? JSON.stringify(entry.metadata) : '\u2014'}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </Layout>
  )
}
