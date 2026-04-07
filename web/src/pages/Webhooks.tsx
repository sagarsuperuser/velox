import { useEffect, useState } from 'react'
import { api, formatDate, type WebhookEndpoint, type WebhookEvent } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'
import { Modal } from '@/components/Modal'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { EmptyState } from '@/components/EmptyState'
import { useToast } from '@/components/Toast'

export function WebhooksPage() {
  const [tab, setTab] = useState<'endpoints' | 'events'>('endpoints')

  return (
    <Layout>
      <div>
        <h1 className="text-2xl font-semibold text-gray-900">Webhooks</h1>
        <p className="text-sm text-gray-500 mt-1">Manage webhook endpoints and view event deliveries</p>
      </div>

      <div className="flex gap-1 mt-6 bg-gray-100 rounded-lg p-1 w-fit">
        {(['endpoints', 'events'] as const).map(t => (
          <button key={t} onClick={() => setTab(t)}
            className={`px-4 py-1.5 rounded-md text-sm font-medium transition-colors ${
              tab === t ? 'bg-white text-gray-900 shadow-sm' : 'text-gray-500 hover:text-gray-700'
            }`}>
            {t === 'endpoints' ? 'Endpoints' : 'Events'}
          </button>
        ))}
      </div>

      {tab === 'endpoints' ? <EndpointsTab /> : <EventsTab />}
    </Layout>
  )
}

function EndpointsTab() {
  const [endpoints, setEndpoints] = useState<WebhookEndpoint[]>([])
  const [loading, setLoading] = useState(true)
  const [showCreate, setShowCreate] = useState(false)
  const [createdSecret, setCreatedSecret] = useState<string | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<WebhookEndpoint | null>(null)
  const toast = useToast()

  const loadEndpoints = () => {
    setLoading(true)
    api.listWebhookEndpoints()
      .then(res => { setEndpoints(res.data || []); setLoading(false) })
      .catch(() => { setEndpoints([]); setLoading(false) })
  }

  useEffect(() => { loadEndpoints() }, [])

  const handleDelete = async () => {
    if (!deleteTarget) return
    try {
      await api.deleteWebhookEndpoint(deleteTarget.id)
      toast.success('Endpoint deleted')
      setDeleteTarget(null)
      loadEndpoints()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to delete endpoint')
    }
  }

  return (
    <>
      <div className="flex justify-end mt-4">
        <button onClick={() => setShowCreate(true)}
          className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 transition-colors">
          Add Endpoint
        </button>
      </div>

      <div className="bg-white rounded-xl border border-gray-200 mt-4">
        {loading ? <LoadingSkeleton rows={5} columns={5} />
        : endpoints.length === 0 ? <EmptyState title="No webhook endpoints" description="Add an endpoint to receive event notifications" />
        : (
          <table className="w-full">
            <thead>
              <tr className="border-b border-gray-100">
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">URL</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Description</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Events</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Status</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Created</th>
                <th className="text-right text-xs font-medium text-gray-500 px-6 py-3"></th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-50">
              {endpoints.map(ep => (
                <tr key={ep.id} className="hover:bg-gray-50">
                  <td className="px-6 py-3 text-sm font-mono text-gray-700 max-w-xs truncate">{ep.url}</td>
                  <td className="px-6 py-3 text-sm text-gray-500">{ep.description || '\u2014'}</td>
                  <td className="px-6 py-3">
                    <div className="flex flex-wrap gap-1">
                      {(ep.events || []).map(ev => (
                        <Badge key={ev} status={ev} />
                      ))}
                      {(!ep.events || ep.events.length === 0) && <span className="text-xs text-gray-400">all</span>}
                    </div>
                  </td>
                  <td className="px-6 py-3"><Badge status={ep.active ? 'active' : 'paused'} /></td>
                  <td className="px-6 py-3 text-sm text-gray-400">{formatDate(ep.created_at)}</td>
                  <td className="px-6 py-3 text-right">
                    <button onClick={() => setDeleteTarget(ep)}
                      className="text-xs text-red-600 hover:underline">Delete</button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {showCreate && (
        <CreateEndpointModal
          onClose={() => setShowCreate(false)}
          onCreated={(secret) => {
            setShowCreate(false)
            setCreatedSecret(secret)
            loadEndpoints()
            toast.success('Endpoint created')
          }}
        />
      )}

      {createdSecret && (
        <Modal open onClose={() => setCreatedSecret(null)} title="Signing Secret">
          <div className="space-y-3">
            <p className="text-sm text-gray-600">
              Save this signing secret now. It will not be shown again.
            </p>
            <div className="bg-amber-50 border border-amber-200 rounded-lg p-4">
              <p className="text-xs text-amber-700 font-medium mb-1">Signing Secret</p>
              <p className="font-mono text-sm text-amber-900 break-all select-all">{createdSecret}</p>
            </div>
            <p className="text-xs text-gray-400">Copy the secret above and store it securely.</p>
            <div className="flex justify-end pt-2">
              <button onClick={() => setCreatedSecret(null)}
                className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700">
                Done
              </button>
            </div>
          </div>
        </Modal>
      )}

      {deleteTarget && (
        <Modal open onClose={() => setDeleteTarget(null)} title="Delete Endpoint">
          <div className="space-y-3">
            <p className="text-sm text-gray-600">
              Are you sure you want to delete this endpoint?
            </p>
            <p className="text-sm font-mono text-gray-500 truncate">{deleteTarget.url}</p>
            <div className="flex justify-end gap-3 pt-2">
              <button onClick={() => setDeleteTarget(null)}
                className="px-4 py-2 text-sm text-gray-600 hover:text-gray-900">Cancel</button>
              <button onClick={handleDelete}
                className="px-4 py-2 bg-red-600 text-white rounded-lg text-sm font-medium hover:bg-red-700">
                Delete
              </button>
            </div>
          </div>
        </Modal>
      )}
    </>
  )
}

function CreateEndpointModal({ onClose, onCreated }: { onClose: () => void; onCreated: (secret: string) => void }) {
  const [url, setUrl] = useState('')
  const [description, setDescription] = useState('')
  const [events, setEvents] = useState('')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  const suggestedEvents = [
    'invoice.created', 'invoice.finalized', 'invoice.voided',
    'payment.succeeded', 'payment.failed',
    'subscription.created', 'subscription.canceled', 'subscription.paused',
    'customer.created', 'customer.updated',
  ]

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!url.startsWith('https://') && !url.startsWith('http://localhost')) {
      setError('URL must start with https:// or http://localhost')
      return
    }
    setSaving(true); setError('')
    try {
      const eventList = events.split(',').map(s => s.trim()).filter(Boolean)
      const res = await api.createWebhookEndpoint({
        url,
        description: description || undefined,
        events: eventList.length > 0 ? eventList : undefined,
      })
      onCreated(res.secret)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create endpoint')
    } finally {
      setSaving(false)
    }
  }

  return (
    <Modal open onClose={onClose} title="Add Webhook Endpoint">
      <form onSubmit={handleSubmit} className="space-y-3">
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">URL</label>
          <input type="text" value={url} onChange={e => setUrl(e.target.value)} required
            placeholder="https://example.com/webhooks"
            className="w-full px-3 py-2 border border-gray-300 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-velox-500" />
        </div>
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">Description</label>
          <input type="text" value={description} onChange={e => setDescription(e.target.value)}
            placeholder="Production webhook"
            className="w-full px-3 py-2 border border-gray-300 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-velox-500" />
        </div>
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">Events (comma-separated)</label>
          <input type="text" value={events} onChange={e => setEvents(e.target.value)}
            placeholder="invoice.created, payment.succeeded"
            className="w-full px-3 py-2 border border-gray-300 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-velox-500" />
          <div className="flex flex-wrap gap-1 mt-2">
            {suggestedEvents.map(ev => (
              <button key={ev} type="button"
                onClick={() => {
                  const current = events.split(',').map(s => s.trim()).filter(Boolean)
                  if (!current.includes(ev)) {
                    setEvents(current.length > 0 ? `${events}, ${ev}` : ev)
                  }
                }}
                className="text-xs px-2 py-0.5 rounded-md border border-gray-200 text-gray-500 hover:bg-gray-50 hover:text-gray-700">
                {ev}
              </button>
            ))}
          </div>
        </div>
        {error && <p className="text-red-600 text-xs">{error}</p>}
        <div className="flex justify-end gap-3 pt-2">
          <button type="button" onClick={onClose} className="px-4 py-2 text-sm text-gray-600 hover:text-gray-900">Cancel</button>
          <button type="submit" disabled={saving}
            className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 disabled:opacity-50">
            {saving ? 'Creating...' : 'Create Endpoint'}
          </button>
        </div>
      </form>
    </Modal>
  )
}

function EventsTab() {
  const [events, setEvents] = useState<WebhookEvent[]>([])
  const [loading, setLoading] = useState(true)
  const toast = useToast()

  const loadEvents = () => {
    setLoading(true)
    api.listWebhookEvents()
      .then(res => { setEvents(res.data || []); setLoading(false) })
      .catch(() => { setEvents([]); setLoading(false) })
  }

  useEffect(() => { loadEvents() }, [])

  const handleReplay = async (id: string) => {
    try {
      await api.replayWebhookEvent(id)
      toast.success('Event replayed')
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to replay event')
    }
  }

  return (
    <div className="bg-white rounded-xl border border-gray-200 mt-4">
      {loading ? <LoadingSkeleton rows={5} columns={4} />
      : events.length === 0 ? <EmptyState title="No webhook events" description="Events will appear here as they are sent to your endpoints" />
      : (
        <table className="w-full">
          <thead>
            <tr className="border-b border-gray-100">
              <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Event Type</th>
              <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Event ID</th>
              <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Created</th>
              <th className="text-right text-xs font-medium text-gray-500 px-6 py-3"></th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-50">
            {events.map(ev => (
              <tr key={ev.id} className="hover:bg-gray-50">
                <td className="px-6 py-3"><Badge status={ev.event_type} /></td>
                <td className="px-6 py-3 text-sm font-mono text-gray-500">{ev.id.slice(0, 12)}...</td>
                <td className="px-6 py-3 text-sm text-gray-400">{formatDate(ev.created_at)}</td>
                <td className="px-6 py-3 text-right">
                  <button onClick={() => handleReplay(ev.id)}
                    className="text-xs text-velox-600 hover:underline">Replay</button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}
