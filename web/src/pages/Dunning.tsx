import { useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { api, formatDate, type DunningPolicy, type DunningRun, type Invoice } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'
import { Modal } from '@/components/Modal'
import { FormSelect } from '@/components/FormField'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { useToast } from '@/components/Toast'

export function DunningPage() {
  const [tab, setTab] = useState<'policy' | 'runs'>('policy')

  return (
    <Layout>
      <div>
        <h1 className="text-2xl font-semibold text-gray-900">Dunning</h1>
        <p className="text-sm text-gray-500 mt-1">Failed payment recovery policy and runs</p>
      </div>

      <div className="flex gap-1 mt-6 bg-gray-100 rounded-lg p-1 w-fit">
        {(['policy', 'runs'] as const).map(t => (
          <button key={t} onClick={() => setTab(t)}
            className={`px-4 py-1.5 rounded-md text-sm font-medium transition-colors ${
              tab === t ? 'bg-white text-gray-900 shadow-sm' : 'text-gray-500 hover:text-gray-700'
            }`}>
            {t === 'policy' ? 'Policy' : 'Runs'}
          </button>
        ))}
      </div>

      {tab === 'policy' ? <PolicyTab /> : <RunsTab />}
    </Layout>
  )
}

function PolicyTab() {
  const [form, setForm] = useState<Partial<DunningPolicy>>({
    name: '',
    enabled: false,
    max_retry_attempts: 3,
    grace_period_days: 3,
    final_action: 'manual_review',
  })
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [savedForm, setSavedForm] = useState<string>('')
  const [isExisting, setIsExisting] = useState(false)
  const toast = useToast()

  const loadPolicy = () => {
    setLoading(true)
    setError(null)
    api.getDunningPolicy()
      .then(p => { setForm(p); setSavedForm(JSON.stringify(p)); setIsExisting(true); setLoading(false) })
      .catch(err => {
        // "not found" means no policy yet, which is fine - show empty form
        const msg = err instanceof Error ? err.message : 'Failed to load policy'
        if (!msg.includes('not found') && !msg.includes('could not be found') && !msg.includes('404') && !msg.includes('Not Found')) {
          setError(msg)
        }
        setLoading(false)
      })
  }

  useEffect(() => { loadPolicy() }, [])

  const hasChanges = JSON.stringify(form) !== savedForm

  const handleSave = async () => {
    setSaving(true)
    try {
      const updated = await api.upsertDunningPolicy(form)
      setForm(updated)
      setSavedForm(JSON.stringify(updated))
      setIsExisting(true)
      toast.success('Dunning policy saved')
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to save policy')
    } finally {
      setSaving(false)
    }
  }

  if (loading) return <div className="mt-6"><LoadingSkeleton rows={4} columns={2} /></div>
  if (error) return <div className="mt-6"><ErrorState message={error} onRetry={loadPolicy} /></div>

  return (
    <div className="bg-white rounded-xl shadow-card mt-4 p-6 max-w-lg space-y-4">
      <div>
        <label className="block text-sm font-medium text-gray-700 mb-1">Policy Name</label>
        <input type="text" value={form.name || ''} onChange={e => setForm(f => ({ ...f, name: e.target.value }))}
          className="w-full px-3 py-2 border border-gray-200 rounded-lg shadow-sm text-sm focus:outline-none focus:ring-2 focus:ring-velox-500"
          placeholder="Default Dunning Policy" maxLength={100} />
      </div>

      <div className="flex items-center gap-3">
        <button type="button" onClick={() => setForm(f => ({ ...f, enabled: !f.enabled }))}
          className={`relative inline-flex h-6 w-11 items-center rounded-full transition-colors ${form.enabled ? 'bg-velox-600' : 'bg-gray-200'}`}>
          <span className={`inline-block h-4 w-4 transform rounded-full bg-white transition-transform ${form.enabled ? 'translate-x-6' : 'translate-x-1'}`} />
        </button>
        <span className="text-sm text-gray-700">{form.enabled ? 'Enabled' : 'Disabled'}</span>
      </div>

      <div className="grid grid-cols-2 gap-4">
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">Max Retry Attempts</label>
          <input type="number" min={1} max={10} value={form.max_retry_attempts ?? 3}
            onChange={e => setForm(f => ({ ...f, max_retry_attempts: parseInt(e.target.value) || 3 }))}
            className="w-full px-3 py-2 border border-gray-200 rounded-lg shadow-sm text-sm focus:outline-none focus:ring-2 focus:ring-velox-500" />
        </div>
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">Grace Period (days)</label>
          <input type="number" min={0} max={30} value={form.grace_period_days ?? 3}
            onChange={e => setForm(f => ({ ...f, grace_period_days: parseInt(e.target.value) || 0 }))}
            className="w-full px-3 py-2 border border-gray-200 rounded-lg shadow-sm text-sm focus:outline-none focus:ring-2 focus:ring-velox-500" />
        </div>
      </div>

      <div>
        <label className="block text-sm font-medium text-gray-700 mb-1">Final Action</label>
        <select value={form.final_action || 'manual_review'}
          onChange={e => setForm(f => ({ ...f, final_action: e.target.value }))}
          className="w-full px-3 py-2 border border-gray-200 rounded-lg shadow-sm text-sm focus:outline-none focus:ring-2 focus:ring-velox-500 bg-white">
          <option value="manual_review">Manual Review</option>
          <option value="pause">Pause Subscription</option>
          <option value="write_off_later">Write Off Later</option>
        </select>
      </div>

      <div className="pt-2 flex items-center gap-3">
        <button onClick={handleSave} disabled={saving || !hasChanges}
          className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow disabled:opacity-50 transition-colors">
          {saving ? 'Saving...' : hasChanges ? 'Save Changes' : 'Saved'}
        </button>
        {isExisting && !hasChanges && (
          <span className="text-xs text-emerald-600 flex items-center gap-1">
            <svg className="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M5 13l4 4L19 7" /></svg>
            Policy is active
          </span>
        )}
        {hasChanges && (
          <span className="text-xs text-amber-600">Unsaved changes</span>
        )}
      </div>
    </div>
  )
}

function RunsTab() {
  const [runs, setRuns] = useState<DunningRun[]>([])
  const [invoiceMap, setInvoiceMap] = useState<Record<string, Invoice>>({})
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [resolveTarget, setResolveTarget] = useState<DunningRun | null>(null)
  const toast = useToast()
  const navigate = useNavigate()

  const loadRuns = () => {
    setLoading(true)
    setError(null)
    Promise.all([
      api.listDunningRuns(),
      api.listInvoices().catch(() => ({ data: [] as Invoice[], total: 0 })),
    ]).then(([runsRes, invoicesRes]) => {
      setRuns(runsRes.data || [])
      const map: Record<string, Invoice> = {}
      invoicesRes.data.forEach(inv => { map[inv.id] = inv })
      setInvoiceMap(map)
      setLoading(false)
    }).catch(err => { setError(err instanceof Error ? err.message : 'Failed to load dunning runs'); setRuns([]); setLoading(false) })
  }

  useEffect(() => { loadRuns() }, [])

  return (
    <>
      <div className="bg-white rounded-xl shadow-card mt-4">
        {error ? <ErrorState message={error} onRetry={loadRuns} />
        : loading ? <LoadingSkeleton rows={5} columns={6} />
        : runs.length === 0 ? <EmptyState title="No dunning runs" description="Dunning runs appear when payment retries are triggered" />
        : (
          <div className="overflow-x-auto">
          <table className="w-full">
            <thead>
              <tr className="border-b border-gray-100">
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Invoice ID</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">State</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Attempts</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Next Action</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Resolution</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Created</th>
                <th className="text-right text-xs font-medium text-gray-500 px-6 py-3"></th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-50">
              {runs.map(run => (
                <tr key={run.id} className="hover:bg-gray-50 cursor-pointer transition-colors group" onClick={(e) => {
                  const target = e.target as HTMLElement
                  if (target.closest('button, a, input, select')) return
                  navigate(`/invoices/${run.invoice_id}`)
                }}>
                  <td className="px-6 py-3 text-sm">
                    <Link to={`/invoices/${run.invoice_id}`} className="text-velox-600 group-hover:text-velox-600 transition-colors hover:underline">
                      {invoiceMap[run.invoice_id]?.invoice_number || run.invoice_id.slice(0, 8) + '...'}
                    </Link>
                  </td>
                  <td className="px-6 py-3"><Badge status={run.state} /></td>
                  <td className="px-6 py-3 text-sm text-gray-500">{run.attempt_count}</td>
                  <td className="px-6 py-3 text-sm text-gray-500">{run.next_action_at ? formatDate(run.next_action_at) : '—'}</td>
                  <td className="px-6 py-3 text-sm text-gray-500">{run.resolution || '—'}</td>
                  <td className="px-6 py-3 text-sm text-gray-400">{formatDate(run.created_at)}</td>
                  <td className="px-6 py-3 text-right">
                    {run.state !== 'resolved' && run.state !== 'exhausted' && (
                      <button onClick={() => setResolveTarget(run)}
                        className="text-xs font-medium text-velox-600 hover:text-velox-700 bg-velox-50 hover:bg-velox-100 px-2.5 py-1 rounded-md transition-colors">Resolve</button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          </div>
        )}
      </div>

      {resolveTarget && (
        <ResolveModal run={resolveTarget} invoiceMap={invoiceMap} onClose={() => setResolveTarget(null)}
          onResolved={() => { setResolveTarget(null); loadRuns(); toast.success('Dunning run resolved') }} />
      )}
    </>
  )
}

function ResolveModal({ run, invoiceMap, onClose, onResolved }: { run: DunningRun; invoiceMap: Record<string, Invoice>; onClose: () => void; onResolved: () => void }) {
  const [resolution, setResolution] = useState('payment_succeeded')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setSaving(true); setError('')
    try {
      await api.resolveDunningRun(run.id, resolution)
      onResolved()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed')
    } finally {
      setSaving(false)
    }
  }

  return (
    <Modal open onClose={onClose} title="Resolve Dunning Run">
      <form onSubmit={handleSubmit} noValidate className="space-y-3">
        <p className="text-sm text-gray-500">Invoice: <span className="font-mono">{invoiceMap[run.invoice_id]?.invoice_number || run.invoice_id.slice(0, 8) + '...'}</span></p>
        <FormSelect label="Resolution" value={resolution}
          onChange={e => setResolution(e.target.value)}
          options={[
            { value: 'payment_succeeded', label: 'Payment Succeeded' },
            { value: 'invoice_not_collectible', label: 'Invoice Not Collectible' },
            { value: 'operator_resolved', label: 'Operator Resolved' },
          ]} />
        {error && <p className="text-sm text-red-600 bg-red-50 border border-red-200 rounded-lg px-3 py-2">{error}</p>}
        <div className="flex justify-end gap-3 pt-4 border-t border-gray-100 mt-2">
          <button type="button" onClick={onClose} className="px-4 py-2 border border-gray-300 text-gray-700 rounded-lg text-sm font-medium hover:bg-gray-50 transition-colors">Cancel</button>
          <button type="submit" disabled={saving}
            className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow disabled:opacity-50">
            {saving ? 'Resolving...' : 'Resolve'}
          </button>
        </div>
      </form>
    </Modal>
  )
}
