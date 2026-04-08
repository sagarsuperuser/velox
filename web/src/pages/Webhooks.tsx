import { useEffect, useState, useMemo } from 'react'
import { api, formatDate, type WebhookEndpoint, type WebhookEvent } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'
import { Modal } from '@/components/Modal'
import { FormField } from '@/components/FormField'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { useToast } from '@/components/Toast'
import { useFormValidation, rules } from '@/hooks/useFormValidation'

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
  const [error, setError] = useState<string | null>(null)
  const [showCreate, setShowCreate] = useState(false)
  const [createdSecret, setCreatedSecret] = useState<string | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<WebhookEndpoint | null>(null)
  const toast = useToast()

  const loadEndpoints = () => {
    setLoading(true)
    setError(null)
    api.listWebhookEndpoints()
      .then(res => { setEndpoints(res.data || []); setLoading(false) })
      .catch(err => { setError(err instanceof Error ? err.message : 'Failed to load endpoints'); setEndpoints([]); setLoading(false) })
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
          className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow transition-colors">
          Add Endpoint
        </button>
      </div>

      <div className="bg-white rounded-xl shadow-card mt-4">
        {error ? <ErrorState message={error} onRetry={loadEndpoints} />
        : loading ? <LoadingSkeleton rows={5} columns={5} />
        : endpoints.length === 0 ? <EmptyState title="No webhook endpoints" description="Add an endpoint to receive event notifications" />
        : (
          <div className="overflow-x-auto">
          <table className="w-full">
            <thead>
              <tr className="border-b border-gray-100 bg-gray-50">
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
                <tr key={ep.id} className="hover:bg-gray-50/50 transition-colors">
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
                      className="text-xs font-medium text-red-600 hover:text-red-700 bg-red-50 hover:bg-red-100 px-2.5 py-1 rounded-md transition-colors">Delete</button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          </div>
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
              <div className="flex items-start gap-2">
                <p className="font-mono text-sm text-amber-900 break-all select-all flex-1">{createdSecret}</p>
                <button
                  onClick={() => {
                    navigator.clipboard.writeText(createdSecret)
                    toast.success('Copied to clipboard')
                  }}
                  className="shrink-0 px-2 py-1 text-xs font-medium text-amber-700 border border-amber-300 rounded-md hover:bg-amber-100 transition-colors"
                >
                  Copy
                </button>
              </div>
            </div>
            <div className="flex justify-end pt-4 border-t border-gray-100 mt-2">
              <button onClick={() => setCreatedSecret(null)}
                className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow transition-colors">
                Done
              </button>
            </div>
          </div>
        </Modal>
      )}

      <ConfirmDialog
        open={!!deleteTarget}
        title="Delete Endpoint"
        message={deleteTarget ? `Are you sure you want to delete the endpoint "${deleteTarget.url}"?` : ''}
        confirmLabel="Delete"
        variant="danger"
        onConfirm={handleDelete}
        onCancel={() => setDeleteTarget(null)}
      />
    </>
  )
}

function CreateEndpointModal({ onClose, onCreated }: { onClose: () => void; onCreated: (secret: string) => void }) {
  const [url, setUrl] = useState('')
  const [description, setDescription] = useState('')
  const [events, setEvents] = useState('')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  const fieldRules = useMemo(() => ({
    url: [rules.required('URL'), rules.url()],
  }), [])
  const { onBlur, validateAll, fieldError, registerRef } = useFormValidation(fieldRules)

  const suggestedEvents = [
    'invoice.created', 'invoice.finalized', 'invoice.voided',
    'payment.succeeded', 'payment.failed',
    'subscription.created', 'subscription.canceled', 'subscription.paused',
    'customer.created', 'customer.updated',
  ]

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!validateAll({ url })) return
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
      <form onSubmit={handleSubmit} noValidate className="space-y-3">
        <FormField label="URL" required type="url" value={url} placeholder="https://example.com/webhooks" maxLength={2048}
          ref={registerRef('url')} error={fieldError('url')}
          onChange={e => setUrl(e.target.value)}
          onBlur={() => onBlur('url', url)} />
        <FormField label="Description" value={description} placeholder="Production webhook" maxLength={500}
          onChange={e => setDescription(e.target.value)} />
        <div>
          <FormField label="Events (comma-separated)" value={events} placeholder="invoice.created, payment.succeeded" maxLength={500}
            onChange={e => setEvents(e.target.value)} />
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
        {error && <p className="text-sm text-red-600 bg-red-50 border border-red-200 rounded-lg px-3 py-2">{error}</p>}
        <div className="flex justify-end gap-3 pt-4 border-t border-gray-100 mt-2">
          <button type="button" onClick={onClose} className="px-4 py-2 border border-gray-300 text-gray-700 rounded-lg text-sm font-medium hover:bg-gray-50 transition-colors">Cancel</button>
          <button type="submit" disabled={saving}
            className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow disabled:opacity-50">
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
  const [error, setError] = useState<string | null>(null)
  const toast = useToast()

  const loadEvents = () => {
    setLoading(true)
    setError(null)
    api.listWebhookEvents()
      .then(res => { setEvents(res.data || []); setLoading(false) })
      .catch(err => { setError(err instanceof Error ? err.message : 'Failed to load events'); setEvents([]); setLoading(false) })
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
    <div className="bg-white rounded-xl shadow-card mt-4">
      {error ? <ErrorState message={error} onRetry={loadEvents} />
      : loading ? <LoadingSkeleton rows={5} columns={4} />
      : events.length === 0 ? <EmptyState title="No webhook events" description="Events will appear here as they are sent to your endpoints" />
      : (
        <div className="overflow-x-auto">
        <table className="w-full">
          <thead>
            <tr className="border-b border-gray-100 bg-gray-50">
              <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Event Type</th>
              <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Event ID</th>
              <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Created</th>
              <th className="text-right text-xs font-medium text-gray-500 px-6 py-3"></th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-50">
            {events.map(ev => (
              <tr key={ev.id} className="hover:bg-gray-50/50 transition-colors">
                <td className="px-6 py-3"><Badge status={ev.event_type} /></td>
                <td className="px-6 py-3 text-sm font-mono text-gray-500">{ev.id.slice(0, 12)}...</td>
                <td className="px-6 py-3 text-sm text-gray-400">{formatDate(ev.created_at)}</td>
                <td className="px-6 py-3 text-right">
                  <button onClick={() => handleReplay(ev.id)}
                    className="text-xs font-medium text-velox-600 hover:text-velox-700 bg-velox-50 hover:bg-velox-100 px-2.5 py-1 rounded-md transition-colors">Replay</button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        </div>
      )}
    </div>
  )
}
