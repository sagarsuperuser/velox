import { useEffect, useState, useMemo } from 'react'
import { useSearchParams } from 'react-router-dom'
import { api, formatDateTime, type WebhookEndpoint, type WebhookEvent } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'
import { Modal } from '@/components/Modal'
import { FormField } from '@/components/FormField'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { toast } from 'sonner'
import { useFormValidation, rules } from '@/hooks/useFormValidation'
import { Loader2 } from 'lucide-react'
import { Breadcrumbs } from '@/components/Breadcrumbs'

export function WebhooksPage() {
  const [searchParams, setSearchParams] = useSearchParams()
  const tab = (searchParams.get('tab') === 'events' ? 'events' : 'endpoints') as 'endpoints' | 'events'
  const setTab = (t: 'endpoints' | 'events') => setSearchParams(t === 'endpoints' ? {} : { tab: t })

  return (
    <Layout>
      <Breadcrumbs items={[{ label: 'System' }, { label: 'Webhooks' }]} />
      <div>
        <h1 className="text-2xl font-semibold text-gray-900 dark:text-gray-100">Webhooks</h1>
        <p className="text-sm text-gray-600 dark:text-gray-400 mt-1">Manage webhook endpoints and view event deliveries</p>
      </div>

      <div className="flex gap-1 mt-6 bg-gray-100 dark:bg-gray-800 rounded-lg p-1 w-fit">
        {(['endpoints', 'events'] as const).map(t => (
          <button key={t} onClick={() => setTab(t)}
            className={`px-4 py-1.5 rounded-md text-sm font-medium transition-colors ${
              tab === t ? 'bg-white dark:bg-gray-700 text-gray-900 dark:text-gray-100 shadow-sm' : 'text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-200'
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
  const [stats, setStats] = useState<Record<string, { total_deliveries: number; succeeded: number; failed: number; success_rate: number }>>({})
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [showCreate, setShowCreate] = useState(false)
  const [createdSecret, setCreatedSecret] = useState<string | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<WebhookEndpoint | null>(null)

  const loadEndpoints = () => {
    setLoading(true)
    setError(null)
    Promise.all([
      api.listWebhookEndpoints(),
      api.getWebhookEndpointStats(),
    ])
      .then(([epRes, statsRes]) => {
        setEndpoints(epRes.data || [])
        const map: typeof stats = {}
        for (const s of (statsRes.data || [])) {
          map[s.endpoint_id] = s
        }
        setStats(map)
        setLoading(false)
      })
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
      {endpoints.length > 0 && (
        <div className="flex justify-end mt-4">
          <button onClick={() => setShowCreate(true)}
            className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow transition-colors">
            Add Endpoint
          </button>
        </div>
      )}

      <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card mt-4">
        {error ? <ErrorState message={error} onRetry={loadEndpoints} />
        : loading ? <LoadingSkeleton rows={5} columns={5} />
        : endpoints.length === 0 ? <EmptyState title="No webhook endpoints" description="Add an endpoint to receive event notifications" actionLabel="Add Endpoint" onAction={() => setShowCreate(true)} />
        : (
          <div className="overflow-x-auto">
          <table className="w-full">
            <thead>
              <tr className="border-b border-gray-100 dark:border-gray-800 bg-gray-50 dark:bg-gray-800/50">
                <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">URL</th>
                <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Description</th>
                <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Events</th>
                <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Status</th>
                <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Success Rate</th>
                <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Created</th>
                <th className="text-right text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3"></th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
              {endpoints.map(ep => (
                <tr key={ep.id} className="hover:bg-gray-50/50 dark:hover:bg-gray-800/50 transition-colors">
                  <td className="px-6 py-3 text-sm font-mono text-gray-700 max-w-xs truncate">{ep.url}</td>
                  <td className="px-6 py-3 text-sm text-gray-600 dark:text-gray-400">{ep.description || '\u2014'}</td>
                  <td className="px-6 py-3">
                    <div className="flex flex-wrap gap-1">
                      {(ep.events || []).map(ev => (
                        <Badge key={ev} status={ev} />
                      ))}
                      {(!ep.events || ep.events.length === 0) && <span className="text-xs text-gray-500">all</span>}
                    </div>
                  </td>
                  <td className="px-6 py-3"><Badge status={ep.active ? 'active' : 'paused'} /></td>
                  <td className="px-6 py-3 text-sm font-medium">
                    {(() => {
                      const s = stats[ep.id]
                      if (!s || s.total_deliveries === 0) return <span className="text-gray-400">{'\u2014'}</span>
                      const rate = s.success_rate
                      const color = rate >= 95 ? 'text-green-600' : rate >= 70 ? 'text-amber-600' : 'text-red-600'
                      return <span className={color}>{rate.toFixed(1)}%</span>
                    })()}
                  </td>
                  <td className="px-6 py-3 text-sm text-gray-400 dark:text-gray-500">{formatDateTime(ep.created_at)}</td>
                  <td className="px-6 py-3 text-right">
                    <div className="flex items-center justify-end gap-1">
                      <button onClick={async () => {
                        try {
                          const res = await api.rotateWebhookSecret(ep.id)
                          setCreatedSecret(res.secret)
                          toast.success('Secret rotated')
                        } catch (err) {
                          toast.error(err instanceof Error ? err.message : 'Failed to rotate secret')
                        }
                      }}
                        className="text-xs font-medium text-velox-600 hover:text-velox-700 bg-velox-50 hover:bg-velox-100 px-2.5 py-1 rounded-md transition-colors">Rotate Secret</button>
                      <button onClick={() => setDeleteTarget(ep)}
                        className="text-xs font-medium text-red-600 hover:text-red-700 bg-red-50 hover:bg-red-100 px-2.5 py-1 rounded-md transition-colors">Delete</button>
                    </div>
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
            <p className="text-sm text-gray-600 dark:text-gray-400">
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
            <div className="flex justify-end pt-4 border-t border-gray-100 dark:border-gray-800 mt-2">
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

const EVENT_GROUPS: { label: string; events: { type: string; description: string }[] }[] = [
  {
    label: 'Invoice',
    events: [
      { type: 'invoice.created', description: 'Invoice created' },
      { type: 'invoice.finalized', description: 'Invoice finalized and ready for payment' },
      { type: 'invoice.paid', description: 'Invoice marked as paid' },
      { type: 'invoice.voided', description: 'Invoice voided' },
    ],
  },
  {
    label: 'Payment',
    events: [
      { type: 'payment.succeeded', description: 'Payment collected successfully' },
      { type: 'payment.failed', description: 'Payment attempt failed' },
    ],
  },
  {
    label: 'Subscription',
    events: [
      { type: 'subscription.created', description: 'Subscription created' },
      { type: 'subscription.activated', description: 'Subscription activated' },
      { type: 'subscription.canceled', description: 'Subscription canceled' },
      { type: 'subscription.paused', description: 'Subscription paused' },
      { type: 'subscription.resumed', description: 'Subscription resumed' },
    ],
  },
  {
    label: 'Customer',
    events: [
      { type: 'customer.created', description: 'Customer created' },
    ],
  },
  {
    label: 'Dunning',
    events: [
      { type: 'dunning.started', description: 'Dunning process started' },
      { type: 'dunning.escalated', description: 'Dunning escalated after retries exhausted' },
      { type: 'dunning.resolved', description: 'Dunning resolved (payment recovered)' },
    ],
  },
  {
    label: 'Credit',
    events: [
      { type: 'credit.granted', description: 'Credit granted to customer' },
      { type: 'credit_note.issued', description: 'Credit note issued' },
    ],
  },
]

const ALL_EVENT_TYPES = EVENT_GROUPS.flatMap(g => g.events.map(e => e.type))

function CreateEndpointModal({ onClose, onCreated }: { onClose: () => void; onCreated: (secret: string) => void }) {
  const [url, setUrl] = useState('')
  const [description, setDescription] = useState('')
  const [listenMode, setListenMode] = useState<'all' | 'specific'>('all')
  const [selectedEvents, setSelectedEvents] = useState<Set<string>>(new Set())
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  const fieldRules = useMemo(() => ({
    url: [rules.required('URL'), rules.url()],
  }), [])
  const { onBlur, validateAll, fieldError, registerRef } = useFormValidation(fieldRules)

  const toggleEvent = (eventType: string) => {
    setSelectedEvents(prev => {
      const next = new Set(prev)
      if (next.has(eventType)) next.delete(eventType)
      else next.add(eventType)
      return next
    })
  }

  const toggleGroup = (group: typeof EVENT_GROUPS[number]) => {
    const groupTypes = group.events.map(e => e.type)
    const allSelected = groupTypes.every(t => selectedEvents.has(t))
    setSelectedEvents(prev => {
      const next = new Set(prev)
      groupTypes.forEach(t => allSelected ? next.delete(t) : next.add(t))
      return next
    })
  }

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!validateAll({ url })) return
    setSaving(true); setError('')
    try {
      const eventList = listenMode === 'all' ? undefined : Array.from(selectedEvents)
      if (listenMode === 'specific' && (!eventList || eventList.length === 0)) {
        setError('Select at least one event')
        setSaving(false)
        return
      }
      const res = await api.createWebhookEndpoint({
        url,
        description: description || undefined,
        events: eventList,
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
      <form onSubmit={handleSubmit} noValidate className="space-y-4">
        <FormField label="URL" required type="url" value={url} placeholder="https://example.com/webhooks" maxLength={2048}
          ref={registerRef('url')} error={fieldError('url')}
          onChange={e => setUrl(e.target.value)}
          onBlur={() => onBlur('url', url)} />
        <FormField label="Description" value={description} placeholder="Production webhook" maxLength={500}
          onChange={e => setDescription(e.target.value)} />

        {/* Event selection */}
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-2">Events to send</label>
          <div className="flex gap-4 mb-3">
            <label className="flex items-center gap-2 cursor-pointer">
              <input type="radio" name="listenMode" checked={listenMode === 'all'}
                onChange={() => setListenMode('all')}
                className="w-4 h-4 text-velox-600 focus:ring-velox-500" />
              <span className="text-sm text-gray-700">Listen to all events</span>
            </label>
            <label className="flex items-center gap-2 cursor-pointer">
              <input type="radio" name="listenMode" checked={listenMode === 'specific'}
                onChange={() => setListenMode('specific')}
                className="w-4 h-4 text-velox-600 focus:ring-velox-500" />
              <span className="text-sm text-gray-700">Select specific events</span>
            </label>
          </div>

          {listenMode === 'specific' && (
            <div className="border border-gray-200 dark:border-gray-700 rounded-lg max-h-72 overflow-y-auto">
              {/* Select all bar */}
              <div className="flex items-center justify-between px-4 py-2.5 bg-gray-50 border-b border-gray-200 sticky top-0 z-10">
                <label className="flex items-center gap-2 cursor-pointer">
                  <input type="checkbox"
                    checked={selectedEvents.size === ALL_EVENT_TYPES.length}
                    ref={el => { if (el) el.indeterminate = selectedEvents.size > 0 && selectedEvents.size < ALL_EVENT_TYPES.length }}
                    onChange={() => {
                      if (selectedEvents.size === ALL_EVENT_TYPES.length) setSelectedEvents(new Set())
                      else setSelectedEvents(new Set(ALL_EVENT_TYPES))
                    }}
                    className="w-4 h-4 rounded text-velox-600 focus:ring-velox-500" />
                  <span className="text-sm font-medium text-gray-700">Select all</span>
                </label>
                <span className="text-xs text-gray-500">{selectedEvents.size} of {ALL_EVENT_TYPES.length} selected</span>
              </div>

              {EVENT_GROUPS.map(group => {
                const groupTypes = group.events.map(e => e.type)
                const selectedCount = groupTypes.filter(t => selectedEvents.has(t)).length
                const allSelected = selectedCount === groupTypes.length

                return (
                  <div key={group.label} className="border-b border-gray-100 last:border-b-0">
                    {/* Group header */}
                    <div className="flex items-center gap-2 px-4 py-2 bg-white dark:bg-gray-800">
                      <input type="checkbox"
                        checked={allSelected}
                        ref={el => { if (el) el.indeterminate = selectedCount > 0 && !allSelected }}
                        onChange={() => toggleGroup(group)}
                        className="w-4 h-4 rounded text-velox-600 focus:ring-velox-500" />
                      <span className="text-sm font-semibold text-gray-800">{group.label}</span>
                      <span className="text-xs text-gray-500 ml-auto">{selectedCount}/{groupTypes.length}</span>
                    </div>

                    {/* Individual events */}
                    {group.events.map(ev => (
                      <label key={ev.type}
                        className="flex items-start gap-2 px-4 py-1.5 pl-10 cursor-pointer hover:bg-gray-50 transition-colors">
                        <input type="checkbox"
                          checked={selectedEvents.has(ev.type)}
                          onChange={() => toggleEvent(ev.type)}
                          className="w-4 h-4 rounded text-velox-600 focus:ring-velox-500 mt-0.5 shrink-0" />
                        <div className="min-w-0">
                          <p className="text-sm text-gray-700 font-mono">{ev.type}</p>
                          <p className="text-xs text-gray-500">{ev.description}</p>
                        </div>
                      </label>
                    ))}
                  </div>
                )
              })}
            </div>
          )}
        </div>

        {error && <p className="text-sm text-red-600 bg-red-50 border border-red-200 rounded-lg px-3 py-2">{error}</p>}
        <div className="flex justify-end gap-3 pt-4 border-t border-gray-100 dark:border-gray-800 mt-2">
          <button type="button" onClick={onClose} className="px-4 py-2 border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-300 rounded-lg text-sm font-medium hover:bg-gray-50 dark:hover:bg-gray-800 transition-colors">Cancel</button>
          <button type="submit" disabled={saving}
            className="flex items-center justify-center gap-2 px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow disabled:opacity-50">
            {saving ? (<><Loader2 size={14} className="animate-spin" /> Creating...</>) : 'Create Endpoint'}
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
    <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card mt-4">
      {error ? <ErrorState message={error} onRetry={loadEvents} />
      : loading ? <LoadingSkeleton rows={5} columns={4} />
      : events.length === 0 ? <EmptyState title="No webhook events" description="Events will appear here as they are sent to your endpoints" />
      : (
        <div className="overflow-x-auto">
        <table className="w-full">
          <thead>
            <tr className="border-b border-gray-100 dark:border-gray-800 bg-gray-50 dark:bg-gray-800/50">
              <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Event Type</th>
              <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Event ID</th>
              <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Created</th>
              <th className="text-right text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3"></th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
            {events.map(ev => (
              <tr key={ev.id} className="hover:bg-gray-50/50 dark:hover:bg-gray-800/50 transition-colors">
                <td className="px-6 py-3"><Badge status={ev.event_type} /></td>
                <td className="px-6 py-3 text-sm font-mono text-gray-500">{ev.id.slice(0, 12)}...</td>
                <td className="px-6 py-3 text-sm text-gray-400 dark:text-gray-500">{formatDateTime(ev.created_at)}</td>
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
