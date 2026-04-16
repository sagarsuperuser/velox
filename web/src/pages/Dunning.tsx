import { Fragment, useEffect, useState, useMemo, useCallback } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { api, formatDateTime, formatCents, type DunningPolicy, type DunningRun, type DunningEvent, type Invoice, type Customer } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'
import { Modal } from '@/components/Modal'
// FormSelect available if needed
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { useToast } from '@/components/Toast'
import { Pagination } from '@/components/Pagination'

function relativeTime(dateStr: string): string {
  const now = Date.now()
  const then = new Date(dateStr).getTime()
  const diffMs = now - then
  const diffMins = Math.floor(diffMs / 60000)
  if (diffMins < 1) return 'just now'
  if (diffMins < 60) return `${diffMins}m ago`
  const diffHrs = Math.floor(diffMins / 60)
  if (diffHrs < 24) return `${diffHrs}h ago`
  const diffDays = Math.floor(diffHrs / 24)
  if (diffDays === 1) return 'yesterday'
  if (diffDays < 30) return `${diffDays}d ago`
  return formatDateTime(dateStr)
}

function futureRelativeTime(dateStr: string): string {
  const now = Date.now()
  const then = new Date(dateStr).getTime()
  const diffMs = then - now
  if (diffMs < 0) return 'overdue'
  const diffMins = Math.floor(diffMs / 60000)
  if (diffMins < 60) return `in ${diffMins}m`
  const diffHrs = Math.floor(diffMins / 60)
  if (diffHrs < 24) return `in ${diffHrs}h`
  const diffDays = Math.floor(diffHrs / 24)
  return `in ${diffDays}d`
}

export function DunningPage() {
  const [searchParams, setSearchParams] = useSearchParams()
  const tab = (searchParams.get('tab') === 'runs' ? 'runs' : 'policy') as 'policy' | 'runs'
  const setTab = (t: 'policy' | 'runs') => setSearchParams(t === 'policy' ? {} : { tab: t })

  return (
    <Layout>
      <div>
        <h1 className="text-2xl font-semibold text-gray-900 dark:text-gray-100">Dunning</h1>
        <p className="text-sm text-gray-600 dark:text-gray-400 mt-1">Automatically recover failed payments and manage delinquent invoices</p>
      </div>

      <div className="flex gap-1 mt-6 bg-gray-100 dark:bg-gray-800 rounded-lg p-1 w-fit">
        {(['policy', 'runs'] as const).map(t => (
          <button key={t} onClick={() => setTab(t)}
            className={`px-4 py-1.5 rounded-md text-sm font-medium transition-colors ${
              tab === t ? 'bg-white dark:bg-gray-700 text-gray-900 dark:text-gray-100 shadow-sm' : 'text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-200'
            }`}>
            {t === 'policy' ? 'Policy' : 'Runs'}
          </button>
        ))}
      </div>

      {tab === 'policy' ? <PolicyTab /> : <RunsTab />}
    </Layout>
  )
}

/* ───────────────────────────── Policy Tab ───────────────────────────── */

const FINAL_ACTIONS: { value: string; label: string; description: string; icon: string }[] = [
  { value: 'manual_review', label: 'Escalate for review', description: 'Flag the invoice for your team to manually review and decide next steps.', icon: '👁' },
  { value: 'pause', label: 'Pause subscription', description: 'Suspend the customer\'s subscription until payment is recovered.', icon: '⏸' },
  { value: 'write_off_later', label: 'Mark uncollectible', description: 'Accept the loss and mark the invoice as uncollectible. The invoice is voided.', icon: '✕' },
]

function PolicyTab() {
  const defaultPolicy: Partial<DunningPolicy> = {
    name: '',
    enabled: false,
    max_retry_attempts: 3,
    grace_period_days: 3,
    final_action: 'manual_review',
  }
  const [form, setForm] = useState<Partial<DunningPolicy>>(defaultPolicy)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [savedForm, setSavedForm] = useState<string>('')
  const toast = useToast()

  const loadPolicy = () => {
    setLoading(true)
    setError(null)
    api.getDunningPolicy()
      .then(p => { setForm(p); setSavedForm(JSON.stringify(p)); setLoading(false) })
      .catch(err => {
        const msg = err instanceof Error ? err.message : 'Failed to load policy'
        if (!msg.includes('not found') && !msg.includes('could not be found') && !msg.includes('404') && !msg.includes('Not Found')) {
          setError(msg)
        }
        // No policy exists yet — use defaults and mark as "saved" so the
        // unsaved-changes bar doesn't appear on first load
        setSavedForm(JSON.stringify(defaultPolicy))
        setLoading(false)
      })
  }

  useEffect(() => { loadPolicy() }, [])

  const graceDays = form.grace_period_days ?? 3
  const retryCount = form.max_retry_attempts ?? 3
  const hasChanges = JSON.stringify(form) !== savedForm

  // retry_schedule has N-1 entries for N retries: the gap between consecutive retries.
  // retry_schedule[0] = gap between retry 1 and retry 2
  // retry_schedule[1] = gap between retry 2 and retry 3
  // Grace period (separate field) = gap before retry 1.
  const gapDays = useMemo(() => {
    const schedule = form.retry_schedule || []
    if (schedule.length === 0) return [3, 5, 7] // backend defaults
    return schedule.map(s => {
      const match = s.match(/^(\d+)h$/)
      return match ? Math.round(parseInt(match[1]) / 24) : 3
    })
  }, [form.retry_schedule])

  // Update both retry_schedule (N-1 gaps) and max_retry_attempts (N) in sync
  const setRetryCount = (count: number) => {
    const newGaps = [...gapDays]
    // Add gaps if increasing, trim if decreasing
    while (newGaps.length < count - 1) {
      const last = newGaps[newGaps.length - 1] ?? 3
      newGaps.push(Math.min(last + 2, 14))
    }
    while (newGaps.length > count - 1) {
      newGaps.pop()
    }
    setForm(f => ({
      ...f,
      max_retry_attempts: count,
      retry_schedule: newGaps.map(d => `${d * 24}h`),
    }))
  }

  const updateGap = (index: number, days: number) => {
    const next = [...gapDays]
    next[index] = days
    setForm(f => ({
      ...f,
      retry_schedule: next.map(d => `${d * 24}h`),
    }))
  }

  // Compute cumulative day for each retry step
  const timelineSteps = useMemo(() => {
    const steps: { day: number }[] = []
    let cumulative = graceDays // retry 1 is at grace period
    steps.push({ day: cumulative })
    for (let i = 0; i < gapDays.length && i < retryCount - 1; i++) {
      cumulative += gapDays[i]
      steps.push({ day: cumulative })
    }
    return steps
  }, [gapDays, graceDays, retryCount])

  const handleSave = async () => {
    setSaving(true)
    try {
      const updated = await api.upsertDunningPolicy(form)
      setForm(updated)
      setSavedForm(JSON.stringify(updated))
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
    <div className="mt-4 max-w-3xl space-y-6">
      {/* Enable toggle card */}
      <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card">
        <div className="px-6 py-5 flex items-center justify-between">
          <div>
            <p className="text-sm font-semibold text-gray-900 dark:text-gray-100">Automatic payment recovery</p>
            <p className="text-sm text-gray-600 dark:text-gray-400 mt-1">When enabled, Velox will automatically retry failed payments on a schedule and take action after all retries are exhausted.</p>
          </div>
          <button type="button" onClick={() => setForm(f => ({ ...f, enabled: !f.enabled }))}
            className={`relative inline-flex h-6 w-11 items-center rounded-full transition-colors shrink-0 ml-4 ${form.enabled ? 'bg-velox-600' : 'bg-gray-200'}`}>
            <span className={`inline-block h-4 w-4 transform rounded-full bg-white transition-transform shadow-sm ${form.enabled ? 'translate-x-[22px]' : 'translate-x-[3px]'}`} />
          </button>
        </div>
      </div>

      {/* Retry schedule builder — shown always, disabled when dunning is off */}
      <div className={!form.enabled ? 'opacity-50 pointer-events-none select-none' : ''}>
        {!form.enabled && (
          <p className="text-xs text-gray-500 dark:text-gray-400 mb-3 italic">Enable automatic payment recovery above to configure these settings</p>
        )}
        <>
          {/* Retry schedule builder */}
          <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card">
            <div className="px-6 py-4 border-b border-gray-100 dark:border-gray-800">
              <p className="text-sm font-semibold text-gray-900 dark:text-gray-100">Retry schedule</p>
              <p className="text-xs text-gray-500 dark:text-gray-500 mt-0.5">Configure when each retry attempt happens after a payment fails</p>
            </div>

            <div className="px-6 py-4">
              {/* Grace period */}
              <div className="flex items-start gap-3 relative pb-5">
                <div className="absolute left-[11px] top-6 bottom-0 w-px bg-gray-200" />
                <div className="relative z-10 mt-0.5 w-[23px] h-[23px] rounded-full bg-red-100 border-2 border-red-300 flex items-center justify-center shrink-0">
                  <span className="text-[10px] font-bold text-red-500">!</span>
                </div>
                <div className="flex-1 min-w-0">
                  <p className="text-sm font-medium text-gray-900 dark:text-gray-100">Payment fails</p>
                  <p className="text-xs text-gray-500">Day 0</p>
                </div>
              </div>

              {/* Grace period wait */}
              <div className="flex items-start gap-3 relative pb-5">
                <div className="absolute left-[11px] top-6 bottom-0 w-px bg-gray-200" />
                <div className="relative z-10 mt-0.5 w-[23px] h-[23px] rounded-full bg-gray-100 border-2 border-gray-300 border-dashed flex items-center justify-center shrink-0" />
                <div className="flex-1 flex items-center gap-3">
                  <div className="min-w-0">
                    <p className="text-sm text-gray-600 dark:text-gray-400">Wait</p>
                  </div>
                  <select value={graceDays}
                    onChange={e => setForm(f => ({ ...f, grace_period_days: parseInt(e.target.value) }))}
                    className="px-2.5 py-1 border border-gray-200 dark:border-gray-700 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-velox-500 bg-white dark:bg-gray-800">
                    <option value={0}>no grace period</option>
                    <option value={1}>1 day</option>
                    <option value={2}>2 days</option>
                    <option value={3}>3 days</option>
                    <option value={5}>5 days</option>
                    <option value={7}>7 days</option>
                    <option value={14}>14 days</option>
                  </select>
                  <p className="text-xs text-gray-500">Grace period — time for customer to update payment method</p>
                </div>
              </div>

              {/* Retry steps */}
              {Array.from({ length: retryCount }, (_, i) => (
                <div key={i}>
                  <div className="flex items-start gap-3 relative pb-2">
                    <div className="absolute left-[11px] top-6 bottom-0 w-px bg-gray-200" />
                    <div className="relative z-10 mt-0.5 w-[23px] h-[23px] rounded-full bg-blue-100 border-2 border-blue-400 flex items-center justify-center shrink-0">
                      <span className="text-[10px] font-bold text-blue-600">{i + 1}</span>
                    </div>
                    <div className="flex-1 flex items-center gap-3 flex-wrap min-w-0">
                      <p className="text-sm font-medium text-gray-900 dark:text-gray-100 shrink-0">Retry {i + 1}</p>
                      {i === 0 && (
                        <span className="text-xs text-gray-500">after grace period</span>
                      )}
                      {i > 0 && (
                        <select value={gapDays[i - 1] ?? 3}
                          onChange={e => updateGap(i - 1, parseInt(e.target.value))}
                          className="px-2.5 py-1 border border-gray-200 dark:border-gray-700 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-velox-500 bg-white dark:bg-gray-800">
                          {[1,2,3,5,7,10,14].map(d => (
                            <option key={d} value={d}>{d} {d === 1 ? 'day' : 'days'} after retry {i}</option>
                          ))}
                        </select>
                      )}
                      <span className="text-xs text-gray-500 shrink-0 tabular-nums">Day {timelineSteps[i]?.day ?? 0}</span>
                      {retryCount > 1 && (
                        <button onClick={() => setRetryCount(retryCount - 1)}
                          className="ml-auto shrink-0 text-gray-300 hover:text-red-500 transition-colors p-1" title="Remove last retry">
                          <svg className="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                            <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M6 18L18 6M6 6l12 12" />
                          </svg>
                        </button>
                      )}
                    </div>
                  </div>
                  <div className="h-3" />
                </div>
              ))}

              {/* Add retry button */}
              {retryCount < 8 && (
                <div className="flex items-start gap-3 relative pb-5">
                  <div className="absolute left-[11px] top-6 bottom-0 w-px bg-gray-200" />
                  <div className="relative z-10 mt-0.5 w-[23px] h-[23px] rounded-full bg-white border-2 border-dashed border-gray-300 flex items-center justify-center shrink-0">
                    <span className="text-[10px] font-bold text-gray-400">+</span>
                  </div>
                  <button onClick={() => setRetryCount(retryCount + 1)}
                    className="text-sm text-velox-600 hover:text-velox-700 font-medium transition-colors">
                    Add retry attempt
                  </button>
                </div>
              )}

              {/* Final action */}
              <div className="flex items-start gap-3 relative">
                <div className="relative z-10 mt-0.5 w-[23px] h-[23px] rounded-full bg-amber-100 border-2 border-amber-400 flex items-center justify-center shrink-0">
                  <span className="text-[10px] font-bold text-amber-600">!</span>
                </div>
                <div className="flex-1 min-w-0">
                  <p className="text-sm font-semibold text-amber-700">
                    {FINAL_ACTIONS.find(a => a.value === (form.final_action || 'manual_review'))?.label || 'Final action'}
                  </p>
                  <p className="text-xs text-gray-500">Day {timelineSteps[timelineSteps.length - 1]?.day ?? graceDays} — if all retries fail</p>
                </div>
              </div>
            </div>
          </div>

          {/* Final action selection */}
          <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card">
            <div className="px-6 py-4 border-b border-gray-100 dark:border-gray-800">
              <p className="text-sm font-semibold text-gray-900 dark:text-gray-100">If all retries fail</p>
              <p className="text-xs text-gray-500 dark:text-gray-500 mt-0.5">Action to take when all {retryCount} retry {retryCount === 1 ? 'attempt has' : 'attempts have'} been exhausted</p>
            </div>
            <div className="p-4 grid gap-3">
              {FINAL_ACTIONS.map(action => {
                const selected = (form.final_action || 'manual_review') === action.value
                return (
                  <button key={action.value} type="button"
                    onClick={() => setForm(f => ({ ...f, final_action: action.value }))}
                    className={`text-left px-4 py-3 rounded-lg border-2 transition-all ${
                      selected
                        ? 'border-velox-500 bg-velox-50 ring-1 ring-velox-500/20'
                        : 'border-gray-200 hover:border-gray-300 hover:bg-gray-50'
                    }`}>
                    <div className="flex items-start gap-3">
                      <div>
                        <p className={`text-sm font-medium ${selected ? 'text-velox-700' : 'text-gray-900'}`}>{action.label}</p>
                        <p className="text-xs text-gray-500 dark:text-gray-500 mt-0.5">{action.description}</p>
                      </div>
                      {selected && (
                        <div className="ml-auto shrink-0 mt-1">
                          <div className="w-5 h-5 rounded-full bg-velox-600 flex items-center justify-center">
                            <svg className="w-3 h-3 text-white" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                              <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={3} d="M5 13l4 4L19 7" />
                            </svg>
                          </div>
                        </div>
                      )}
                    </div>
                  </button>
                )
              })}
            </div>
          </div>
        </>
      </div>

      {/* Sticky save bar */}
      {hasChanges && (
        <div className="sticky bottom-4 z-20">
          <div className="flex items-center justify-between gap-3 bg-gray-900 text-white rounded-xl px-5 py-3 shadow-lg">
            <p className="text-sm">You have unsaved changes</p>
            <div className="flex items-center gap-2">
              <button onClick={() => { setForm(JSON.parse(savedForm || '{}')); }}
                className="px-3 py-1.5 text-sm font-medium text-gray-300 hover:text-white transition-colors">
                Discard
              </button>
              <button onClick={handleSave} disabled={saving}
                className="px-4 py-1.5 bg-velox-500 hover:bg-velox-400 text-white rounded-lg text-sm font-medium disabled:opacity-50 transition-colors">
                {saving ? 'Saving...' : 'Save changes'}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

/* ───────────────────────────── Runs Tab ───────────────────────────── */

const RUNS_PAGE_SIZE = 25

function RunsTab() {
  const [runs, setRuns] = useState<DunningRun[]>([])
  const [total, setTotal] = useState(0)
  const [invoiceMap, setInvoiceMap] = useState<Record<string, Invoice>>({})
  const [customerMap, setCustomerMap] = useState<Record<string, Customer>>({})
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [resolveTarget, setResolveTarget] = useState<DunningRun | null>(null)
  const [expandedRun, setExpandedRun] = useState<string | null>(null)
  const [runEvents, setRunEvents] = useState<Record<string, DunningEvent[]>>({})
  const [filterStatus, setFilterStatus] = useState('')
  const [page, setPage] = useState(1)
  const toast = useToast()

  // Server-side paginated fetch
  const loadRuns = useCallback(() => {
    setLoading(true)
    setError(null)
    const params = new URLSearchParams()
    params.set('limit', String(RUNS_PAGE_SIZE))
    params.set('offset', String((page - 1) * RUNS_PAGE_SIZE))
    if (filterStatus) params.set('state', filterStatus)
    Promise.all([
      api.listDunningRuns(params.toString()),
      api.listInvoices().catch(() => ({ data: [] as Invoice[], total: 0 })),
      api.listCustomers().catch(() => ({ data: [] as Customer[], total: 0 })),
      api.getDunningPolicy().catch(() => null),
    ]).then(([runsRes, invoicesRes, custRes, _policyRes]) => {
      setRuns(runsRes.data || [])
      setTotal(runsRes.total || 0)
      const iMap: Record<string, Invoice> = {}
      invoicesRes.data.forEach(inv => { iMap[inv.id] = inv })
      setInvoiceMap(iMap)
      const cMap: Record<string, Customer> = {}
      custRes.data.forEach(c => { cMap[c.id] = c })
      setCustomerMap(cMap)
      setLoading(false)
    }).catch(err => { setError(err instanceof Error ? err.message : 'Failed to load dunning runs'); setRuns([]); setTotal(0); setLoading(false) })
  }, [page, filterStatus])

  useEffect(() => { loadRuns() }, [loadRuns])

  const loadRunEvents = async (runId: string) => {
    if (runEvents[runId]) return
    try {
      const res = await api.getDunningRun(runId)
      setRunEvents(prev => ({ ...prev, [runId]: res.events || [] }))
    } catch {
      // silently fail — events are supplementary
    }
  }

  const toggleExpand = (runId: string) => {
    if (expandedRun === runId) {
      setExpandedRun(null)
    } else {
      setExpandedRun(runId)
      loadRunEvents(runId)
    }
  }

  // Compute stats
  const stats = useMemo(() => {
    const active = runs.filter(r => r.state === 'active').length
    const escalated = runs.filter(r => r.state === 'escalated').length
    const resolved = runs.filter(r => r.state === 'resolved').length
    const atRiskCents = runs
      .filter(r => r.state === 'active' || r.state === 'escalated')
      .reduce((sum, r) => sum + (invoiceMap[r.invoice_id]?.amount_due_cents || 0), 0)
    return { active, escalated, resolved, atRiskCents }
  }, [runs, invoiceMap])

  const statCards = [
    { label: 'Active', value: stats.active, color: 'text-blue-600', bg: 'bg-blue-50', ring: 'ring-blue-200' },
    { label: 'Escalated', value: stats.escalated, color: 'text-violet-600', bg: 'bg-violet-50', ring: 'ring-violet-200' },
    { label: 'Recovered', value: stats.resolved, color: 'text-emerald-600', bg: 'bg-emerald-50', ring: 'ring-emerald-200' },
    { label: 'At risk', value: stats.atRiskCents, color: 'text-red-600', bg: 'bg-red-50', ring: 'ring-red-200', isCurrency: true },
  ]

  return (
    <>
      {/* Summary stats */}
      {!loading && total > 0 && (
        <div className="grid grid-cols-2 lg:grid-cols-4 gap-3 mt-4">
          {statCards.map(stat => (
            <div key={stat.label} className={`${stat.bg} rounded-xl px-4 py-3 ring-1 ${stat.ring}`}>
              <p className="text-xs font-medium text-gray-500 dark:text-gray-400">{stat.label}</p>
              <p className={`text-xl font-semibold ${stat.color} mt-1 tabular-nums`}>
                {stat.isCurrency ? formatCents(stat.value) : stat.value}
              </p>
            </div>
          ))}
        </div>
      )}

      {/* Filter */}
      {total > 0 && (
        <div className="flex items-center gap-2 mt-4">
          <div className="flex gap-1 bg-gray-100 dark:bg-gray-800 rounded-lg p-1">
            {[
              { value: '', label: 'All' },
              { value: 'active', label: 'Active' },
              { value: 'escalated', label: 'Escalated' },
              { value: 'resolved', label: 'Recovered' },
            ].map(f => (
              <button key={f.value} onClick={() => { setFilterStatus(f.value); setPage(1) }}
                className={`px-3 py-1 rounded-md text-xs font-medium transition-colors ${
                  filterStatus === f.value ? 'bg-white dark:bg-gray-700 text-gray-900 dark:text-gray-100 shadow-sm' : 'text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-200'
                }`}>
                {f.label}
              </button>
            ))}
          </div>
        </div>
      )}

      {/* Runs list */}
      <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card mt-4">
        {error ? <ErrorState message={error} onRetry={loadRuns} />
        : loading ? <LoadingSkeleton rows={5} columns={6} />
        : runs.length === 0 ? <EmptyState title="No dunning runs" description="Dunning runs will appear here when Velox detects a failed payment and begins the recovery process." />
        : (
          <>
            <div className="overflow-x-auto">
            <table className="w-full">
              <thead>
                <tr className="border-b border-gray-100 dark:border-gray-800 bg-gray-50 dark:bg-gray-800/50">
                  <th className="w-8 px-3 py-3"></th>
                  <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-4 py-3">Customer</th>
                  <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-4 py-3">Invoice</th>
                  <th className="text-right text-xs font-medium text-gray-500 dark:text-gray-400 px-4 py-3">Amount Due</th>
                  <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-4 py-3">Status</th>
                  <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-4 py-3">Progress</th>
                  <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-4 py-3">Next Retry</th>
                  <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-4 py-3">Started</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
                {runs.map(run => {
                  const inv = invoiceMap[run.invoice_id]
                  const cust = customerMap[run.customer_id]
                  const isExpanded = expandedRun === run.id
                  const isFinished = run.state !== 'active'

                  return (
                  <Fragment key={run.id}>
                  <tr className={`hover:bg-gray-50 transition-colors group ${isExpanded ? 'bg-gray-50' : ''}`}>
                    {/* Expand chevron */}
                    <td className="px-3 py-3">
                      <button onClick={() => toggleExpand(run.id)}
                        className="w-6 h-6 flex items-center justify-center rounded hover:bg-gray-200 transition-colors">
                        <svg className={`w-4 h-4 text-gray-400 transition-transform ${isExpanded ? 'rotate-90' : ''}`} fill="none" stroke="currentColor" viewBox="0 0 24 24">
                          <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M9 5l7 7-7 7" />
                        </svg>
                      </button>
                    </td>

                    {/* Customer */}
                    <td className="px-4 py-3">
                      <p className="text-sm font-medium text-gray-900 dark:text-gray-100">{cust?.display_name || run.customer_id.slice(0, 8) + '...'}</p>
                      {cust?.email && <p className="text-xs text-gray-500 truncate max-w-[180px]">{cust.email}</p>}
                    </td>

                    {/* Invoice */}
                    <td className="px-4 py-3">
                      <Link to={`/invoices/${run.invoice_id}`} className="text-sm font-mono text-velox-600 hover:text-velox-700 transition-colors">
                        {inv?.invoice_number || run.invoice_id.slice(0, 8) + '...'}
                      </Link>
                    </td>

                    {/* Amount */}
                    <td className="px-4 py-3 text-sm font-semibold text-gray-900 dark:text-gray-100 text-right tabular-nums">
                      {inv ? formatCents(inv.amount_due_cents) : '\u2014'}
                    </td>

                    {/* Status */}
                    <td className="px-4 py-3">
                      <div className="flex items-center gap-2">
                        <Badge status={run.state} />
                        {run.resolution && run.resolution !== run.state && (
                          <Badge status={run.resolution} />
                        )}
                        {!isFinished && (
                          <button onClick={(e) => { e.stopPropagation(); setResolveTarget(run) }}
                            className="text-xs font-medium text-velox-600 hover:text-velox-700 bg-velox-50 hover:bg-velox-100 px-2 py-0.5 rounded transition-colors ml-1">
                            Resolve
                          </button>
                        )}
                      </div>
                    </td>

                    {/* Progress */}
                    <td className="px-4 py-3">
                      <span className="text-xs text-gray-500 tabular-nums">
                        {run.attempt_count === 0 ? 'No retries yet' : `${run.attempt_count} ${run.attempt_count === 1 ? 'retry' : 'retries'}`}
                      </span>
                    </td>

                    {/* Next retry */}
                    <td className="px-4 py-3">
                      {run.next_action_at
                        ? <span className="text-xs text-gray-500" title={formatDateTime(run.next_action_at)}>{futureRelativeTime(run.next_action_at)}</span>
                        : <span className="text-xs text-gray-300">{'\u2014'}</span>
                      }
                    </td>

                    {/* Started */}
                    <td className="px-4 py-3">
                      <span className="text-xs text-gray-500" title={formatDateTime(run.created_at)}>{relativeTime(run.created_at)}</span>
                    </td>

                  </tr>

                  {/* Expanded event timeline */}
                  {isExpanded && (
                    <tr>
                      <td colSpan={8} className="bg-gray-50 px-0 py-0">
                        <div className="px-12 py-4 border-t border-gray-200 dark:border-gray-700">
                          <p className="text-xs font-semibold text-gray-500 uppercase tracking-wider mb-3">Recovery timeline</p>
                          <RunTimeline events={runEvents[run.id]} run={run} />
                          {run.reason && (
                            <div className="mt-3 bg-red-50 border border-red-100 rounded-lg px-3 py-2">
                              <p className="text-xs font-medium text-red-700">Last failure reason</p>
                              <p className="text-xs text-red-600 mt-0.5">{run.reason}</p>
                            </div>
                          )}
                        </div>
                      </td>
                    </tr>
                  )}
                  </Fragment>
                  )
                })}
              </tbody>
            </table>
            </div>
            <Pagination page={page} totalPages={Math.ceil(total / RUNS_PAGE_SIZE)} onPageChange={setPage} />
          </>
        )}
      </div>

      {resolveTarget && (
        <ResolveModal run={resolveTarget} invoiceMap={invoiceMap} onClose={() => setResolveTarget(null)}
          onResolved={() => { setResolveTarget(null); loadRuns(); toast.success('Dunning run resolved') }} />
      )}
    </>
  )
}

/* ───────────────────────────── Event Timeline ───────────────────────────── */

const EVENT_LABELS: Record<string, { label: string; color: string }> = {
  dunning_started: { label: 'Dunning started', color: 'bg-blue-400' },
  retry_scheduled: { label: 'Retry scheduled', color: 'bg-gray-400' },
  retry_attempted: { label: 'Retry attempted', color: 'bg-blue-400' },
  retry_succeeded: { label: 'Payment recovered', color: 'bg-emerald-500' },
  retry_failed: { label: 'Retry failed', color: 'bg-red-400' },
  paused: { label: 'Dunning paused', color: 'bg-amber-400' },
  resumed: { label: 'Dunning resumed', color: 'bg-blue-400' },
  escalated: { label: 'Escalated', color: 'bg-violet-500' },
  resolved: { label: 'Resolved', color: 'bg-emerald-500' },
}

function RunTimeline({ events }: { events?: DunningEvent[]; run: DunningRun }) {
  if (!events) {
    return (
      <div className="flex items-center gap-2 text-xs text-gray-500">
        <div className="w-3 h-3 border-2 border-gray-300 border-t-transparent rounded-full animate-spin" />
        Loading timeline...
      </div>
    )
  }

  if (events.length === 0) {
    return <p className="text-xs text-gray-500">No events recorded yet</p>
  }

  return (
    <div className="relative">
      {events.map((ev, i) => {
        const meta = EVENT_LABELS[ev.event_type] || { label: ev.event_type.replace(/_/g, ' '), color: 'bg-gray-400' }
        return (
          <div key={ev.id} className="flex items-start gap-3 pb-3 last:pb-0 relative">
            {i < events.length - 1 && (
              <div className="absolute left-[7px] top-4 bottom-0 w-px bg-gray-200" />
            )}
            <div className={`relative z-10 w-[15px] h-[15px] rounded-full ${meta.color} mt-0.5 shrink-0 ring-2 ring-white`} />
            <div className="flex-1 min-w-0">
              <div className="flex items-center gap-2">
                <p className="text-sm font-medium text-gray-700">{meta.label}</p>
                {ev.attempt_count > 0 && (
                  <span className="text-xs text-gray-500">Attempt {ev.attempt_count}</span>
                )}
              </div>
              {ev.reason && <p className="text-xs text-gray-500 dark:text-gray-500 mt-0.5">{ev.reason}</p>}
              <p className="text-xs text-gray-500 dark:text-gray-500 mt-0.5">{formatDateTime(ev.created_at)}</p>
            </div>
          </div>
        )
      })}
    </div>
  )
}

/* ───────────────────────────── Resolve Modal ───────────────────────────── */

function ResolveModal({ run, invoiceMap, onClose, onResolved }: { run: DunningRun; invoiceMap: Record<string, Invoice>; onClose: () => void; onResolved: () => void }) {
  const [resolution, setResolution] = useState('payment_recovered')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  const inv = invoiceMap[run.invoice_id]

  const resolutionOptions = [
    { value: 'payment_recovered', label: 'Payment recovered', description: 'Customer has paid — mark invoice as paid and close dunning.', icon: 'emerald' },
    { value: 'manually_resolved', label: 'Manually resolved', description: 'Issue resolved through other means (offline payment, negotiation, etc.)', icon: 'blue' },
  ]

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setSaving(true); setError('')
    try {
      await api.resolveDunningRun(run.id, resolution)
      onResolved()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to resolve dunning run')
    } finally {
      setSaving(false)
    }
  }

  return (
    <Modal open onClose={onClose} title="Resolve Dunning Run">
      <form onSubmit={handleSubmit} noValidate className="space-y-4">
        {/* Context */}
        <div className="bg-gray-50 rounded-lg p-3 flex items-center justify-between">
          <div>
            <p className="text-xs text-gray-500">Invoice</p>
            <p className="text-sm font-mono font-medium text-gray-900">{inv?.invoice_number || run.invoice_id.slice(0, 8) + '...'}</p>
          </div>
          <div className="text-right">
            <p className="text-xs text-gray-500">Amount due</p>
            <p className="text-sm font-semibold text-gray-900 dark:text-gray-100 tabular-nums">{inv ? formatCents(inv.amount_due_cents) : '\u2014'}</p>
          </div>
        </div>

        {/* Resolution options as cards */}
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-2">Resolution</label>
          <div className="space-y-2">
            {resolutionOptions.map(opt => {
              const selected = resolution === opt.value
              return (
                <button key={opt.value} type="button"
                  onClick={() => setResolution(opt.value)}
                  className={`w-full text-left px-4 py-3 rounded-lg border-2 transition-all ${
                    selected
                      ? opt.icon === 'red'
                        ? 'border-red-400 bg-red-50 ring-1 ring-red-400/20'
                        : 'border-velox-500 bg-velox-50 ring-1 ring-velox-500/20'
                      : 'border-gray-200 hover:border-gray-300 hover:bg-gray-50'
                  }`}>
                  <p className={`text-sm font-medium ${selected ? (opt.icon === 'red' ? 'text-red-700' : 'text-velox-700') : 'text-gray-900'}`}>{opt.label}</p>
                  <p className="text-xs text-gray-500 dark:text-gray-500 mt-0.5">{opt.description}</p>
                </button>
              )
            })}
          </div>
        </div>

        {resolution === 'invoice_not_collectible' && (
          <div className="bg-red-50 border border-red-200 rounded-lg p-3">
            <p className="text-xs font-medium text-red-700">This action will void the invoice, reverse any credits applied, and cancel the Stripe payment intent. This cannot be undone.</p>
          </div>
        )}

        {error && <p className="text-sm text-red-600 bg-red-50 border border-red-200 rounded-lg px-3 py-2">{error}</p>}
        <div className="flex justify-end gap-3 pt-4 border-t border-gray-100 dark:border-gray-800 mt-2">
          <button type="button" onClick={onClose} className="px-4 py-2 border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-300 rounded-lg text-sm font-medium hover:bg-gray-50 dark:hover:bg-gray-800 transition-colors">Cancel</button>
          <button type="submit" disabled={saving}
            className={`px-4 py-2 rounded-lg text-sm font-medium shadow-sm hover:shadow disabled:opacity-50 transition-colors ${
              resolution === 'invoice_not_collectible'
                ? 'bg-red-600 hover:bg-red-700 text-white'
                : 'bg-velox-600 hover:bg-velox-700 text-white'
            }`}>
            {saving ? 'Resolving...' : 'Resolve'}
          </button>
        </div>
      </form>
    </Modal>
  )
}

