import { Fragment, useState, useMemo, useEffect } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import {
  api,
  formatDateTime,
  formatCents,
  type DunningPolicy,
  type DunningRun,
  type DunningEvent,
  type Invoice,
  type Customer,
} from '@/lib/api'
import { Layout } from '@/components/Layout'
import { cn } from '@/lib/utils'
import { statusBadgeVariant, statusBorderColor } from '@/lib/status'
import { InitialsAvatar } from '@/components/InitialsAvatar'

import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent } from '@/components/ui/card'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import {
  Pagination,
  PaginationContent,
  PaginationItem,
  PaginationLink,
  PaginationNext,
  PaginationPrevious,
} from '@/components/ui/pagination'
import { TableSkeleton } from '@/components/ui/TableSkeleton'

import { Loader2, X } from 'lucide-react'

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

export default function DunningPage() {
  const [searchParams, setSearchParams] = useSearchParams()
  const tab = (searchParams.get('tab') === 'runs' ? 'runs' : 'policy') as 'policy' | 'runs'
  const setTab = (t: string) => setSearchParams(t === 'policy' ? {} : { tab: t })

  return (
    <Layout>
      <div>
        <h1 className="text-2xl font-semibold text-foreground">Dunning</h1>
        <p className="text-sm text-muted-foreground mt-1">
          Recover failed payments automatically
        </p>
      </div>

      <Tabs value={tab} onValueChange={setTab} className="mt-6">
        <TabsList>
          <TabsTrigger value="policy">Policy</TabsTrigger>
          <TabsTrigger value="runs">Runs</TabsTrigger>
        </TabsList>

        <TabsContent value="policy">
          <PolicyTab />
        </TabsContent>
        <TabsContent value="runs">
          <RunsTab />
        </TabsContent>
      </Tabs>
    </Layout>
  )
}

/* ─────────────────────────── Policy Tab ─────────────────────────── */

const FINAL_ACTIONS: { value: string; label: string; description: string }[] = [
  { value: 'manual_review', label: 'Escalate for review', description: 'Flag the invoice for your team to manually review and decide next steps.' },
  { value: 'pause', label: 'Pause subscription', description: "Suspend the customer's subscription until payment is recovered." },
  { value: 'write_off_later', label: 'Mark uncollectible', description: 'Accept the loss and mark the invoice as uncollectible. The invoice is voided.' },
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
        setSavedForm(JSON.stringify(defaultPolicy))
        setLoading(false)
      })
  }

  useEffect(() => { loadPolicy() }, [])

  const graceDays = form.grace_period_days ?? 3
  const retryCount = form.max_retry_attempts ?? 3
  const hasChanges = JSON.stringify(form) !== savedForm

  const gapDays = useMemo(() => {
    const schedule = form.retry_schedule || []
    if (schedule.length === 0) return [3, 5, 7]
    return schedule.map(s => {
      const match = s.match(/^(\d+)h$/)
      return match ? Math.round(parseInt(match[1]) / 24) : 3
    })
  }, [form.retry_schedule])

  const setRetryCount = (count: number) => {
    const newGaps = [...gapDays]
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

  const timelineSteps = useMemo(() => {
    const steps: { day: number }[] = []
    let cumulative = graceDays
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

  if (loading) return (
    <div className="mt-6 flex justify-center p-8">
      <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
    </div>
  )
  if (error) return (
    <div className="mt-6 p-8 text-center">
      <p className="text-sm text-destructive mb-3">{error}</p>
      <Button variant="outline" size="sm" onClick={loadPolicy}>Retry</Button>
    </div>
  )

  return (
    <div className="mt-4 max-w-3xl space-y-6">
      {/* Enable toggle */}
      <Card>
        <CardContent className="px-6 py-5 flex items-center justify-between">
          <div>
            <p className="text-sm font-semibold text-foreground">Automatic payment recovery</p>
            <p className="text-sm text-muted-foreground mt-1">
              When enabled, Velox will automatically retry failed payments on a schedule and take action after all retries are exhausted.
            </p>
          </div>
          <Switch
            checked={form.enabled ?? false}
            onCheckedChange={(checked) => setForm(f => ({ ...f, enabled: !!checked }))}
            className="ml-4 shrink-0"
          />
        </CardContent>
      </Card>

      {/* Retry schedule builder */}
      <div className={cn(!form.enabled && 'opacity-50 pointer-events-none select-none')}>
        {!form.enabled && (
          <p className="text-xs text-muted-foreground mb-3 italic">
            Enable automatic payment recovery above to configure these settings
          </p>
        )}

        <Card>
          <CardContent className="p-0">
            <div className="px-6 py-4 border-b border-border">
              <p className="text-sm font-semibold text-foreground">Retry schedule</p>
              <p className="text-xs text-muted-foreground mt-0.5">Configure when each retry attempt happens after a payment fails</p>
            </div>

            <div className="px-6 py-4">
              {/* Payment fails */}
              <div className="flex items-start gap-3 relative pb-5">
                <div className="absolute left-[15px] top-6 bottom-0 w-px bg-border" />
                <div className="relative z-10 mt-0.5 w-8 h-8 rounded-full bg-destructive/10 border-2 border-destructive/30 flex items-center justify-center shrink-0">
                  <span className="text-xs font-bold text-destructive">!</span>
                </div>
                <div className="flex-1 min-w-0">
                  <p className="text-sm font-medium text-foreground">Payment fails</p>
                  <p className="text-xs text-muted-foreground">Day 0</p>
                </div>
              </div>

              {/* Grace period wait */}
              <div className="flex items-start gap-3 relative pb-5">
                <div className="absolute left-[15px] top-6 bottom-0 w-px bg-border" />
                <div className="relative z-10 mt-0.5 w-8 h-8 rounded-full bg-muted border-2 border-border border-dashed flex items-center justify-center shrink-0" />
                <div className="flex-1 flex items-center gap-3">
                  <p className="text-sm text-muted-foreground">Wait</p>
                  <Select value={String(graceDays)} onValueChange={v => setForm(f => ({ ...f, grace_period_days: parseInt(v ?? '0') }))}>
                    <SelectTrigger className="w-auto">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="0">no grace period</SelectItem>
                      <SelectItem value="1">1 day</SelectItem>
                      <SelectItem value="2">2 days</SelectItem>
                      <SelectItem value="3">3 days</SelectItem>
                      <SelectItem value="5">5 days</SelectItem>
                      <SelectItem value="7">7 days</SelectItem>
                      <SelectItem value="14">14 days</SelectItem>
                    </SelectContent>
                  </Select>
                  <p className="text-xs text-muted-foreground">Grace period</p>
                </div>
              </div>

              {/* Retry steps */}
              {Array.from({ length: retryCount }, (_, i) => (
                <div key={i}>
                  <div className="flex items-start gap-3 relative pb-2">
                    <div className="absolute left-[15px] top-6 bottom-0 w-px bg-border" />
                    <div className="relative z-10 mt-0.5 w-8 h-8 rounded-full bg-primary/10 border-2 border-primary/40 flex items-center justify-center shrink-0">
                      <span className="text-xs font-bold text-primary">{i + 1}</span>
                    </div>
                    <div className="flex-1 flex items-center gap-3 flex-wrap min-w-0">
                      <p className="text-sm font-medium text-foreground shrink-0">Retry {i + 1}</p>
                      {i === 0 && (
                        <span className="text-xs text-muted-foreground">after grace period</span>
                      )}
                      {i > 0 && (
                        <Select value={String(gapDays[i - 1] ?? 3)} onValueChange={v => updateGap(i - 1, parseInt(v ?? '0'))}>
                          <SelectTrigger className="w-auto">
                            <SelectValue />
                          </SelectTrigger>
                          <SelectContent>
                            {[1, 2, 3, 5, 7, 10, 14].map(d => (
                              <SelectItem key={d} value={String(d)}>{d} {d === 1 ? 'day' : 'days'} after retry {i}</SelectItem>
                            ))}
                          </SelectContent>
                        </Select>
                      )}
                      <span className="text-xs text-muted-foreground shrink-0 tabular-nums">Day {timelineSteps[i]?.day ?? 0}</span>
                      {retryCount > 1 && i === retryCount - 1 && (
                        <Button variant="ghost" size="sm" onClick={() => setRetryCount(retryCount - 1)}
                          className="ml-auto shrink-0 text-muted-foreground hover:text-destructive h-7 w-7 p-0" title="Remove retry">
                          <X size={14} />
                        </Button>
                      )}
                    </div>
                  </div>
                  <div className="h-3" />
                </div>
              ))}

              {/* Add retry */}
              {retryCount < 8 && (
                <div className="flex items-start gap-3 relative pb-5">
                  <div className="absolute left-[15px] top-6 bottom-0 w-px bg-border" />
                  <div className="relative z-10 mt-0.5 w-8 h-8 rounded-full bg-background border-2 border-dashed border-border flex items-center justify-center shrink-0">
                    <span className="text-xs font-bold text-muted-foreground">+</span>
                  </div>
                  <Button variant="outline" size="sm" onClick={() => setRetryCount(retryCount + 1)}>
                    + Add retry attempt
                  </Button>
                </div>
              )}

              {/* Final action marker */}
              <div className="flex items-start gap-3 relative">
                <div className="relative z-10 mt-0.5 w-8 h-8 rounded-full bg-amber-100 dark:bg-amber-500/20 border-2 border-amber-400 flex items-center justify-center shrink-0">
                  <span className="text-xs font-bold text-amber-600">!</span>
                </div>
                <div className="flex-1 min-w-0">
                  <p className="text-sm font-semibold text-amber-700 dark:text-amber-400">
                    {FINAL_ACTIONS.find(a => a.value === (form.final_action || 'manual_review'))?.label || 'Final action'}
                  </p>
                  <p className="text-xs text-muted-foreground">
                    Day {timelineSteps[timelineSteps.length - 1]?.day ?? graceDays} -- if all retries fail
                  </p>
                </div>
              </div>
            </div>
          </CardContent>
        </Card>

        {/* Final action selection */}
        <Card>
          <CardContent className="p-0">
            <div className="px-6 py-4 border-b border-border">
              <p className="text-sm font-semibold text-foreground">If all retries fail</p>
              <p className="text-xs text-muted-foreground mt-0.5">
                Action to take when all {retryCount} retry {retryCount === 1 ? 'attempt has' : 'attempts have'} been exhausted
              </p>
            </div>
            <div className="p-4 grid gap-3">
              {FINAL_ACTIONS.map(action => {
                const selected = (form.final_action || 'manual_review') === action.value
                return (
                  <button key={action.value} type="button"
                    onClick={() => setForm(f => ({ ...f, final_action: action.value }))}
                    className={cn(
                      'text-left px-4 py-3 rounded-lg border-2 transition-all',
                      selected
                        ? 'border-primary bg-primary/5 ring-1 ring-primary/20'
                        : 'border-border hover:border-border/80 hover:bg-accent'
                    )}>
                    <div className="flex items-start gap-3">
                      <div>
                        <p className={cn('text-sm font-medium', selected ? 'text-primary' : 'text-foreground')}>{action.label}</p>
                        <p className="text-xs text-muted-foreground mt-0.5">{action.description}</p>
                      </div>
                      {selected && (
                        <div className="ml-auto shrink-0 mt-1">
                          <div className="w-5 h-5 rounded-full bg-primary flex items-center justify-center">
                            <svg className="w-3 h-3 text-primary-foreground" fill="none" stroke="currentColor" viewBox="0 0 24 24">
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
          </CardContent>
        </Card>
      </div>

      {/* Sticky save bar */}
      {hasChanges && (
        <div className="sticky bottom-4 z-20">
          <div className="flex items-center justify-between gap-3 bg-popover text-popover-foreground rounded-xl px-5 py-3 shadow-lg ring-1 ring-border">
            <p className="text-sm">You have unsaved changes</p>
            <div className="flex items-center gap-2">
              <Button variant="ghost" size="sm" onClick={() => { setForm(JSON.parse(savedForm || '{}')) }}>
                Discard
              </Button>
              <Button size="sm" onClick={handleSave} disabled={saving}>
                {saving ? <><Loader2 size={14} className="animate-spin mr-2" /> Saving...</> : 'Save changes'}
              </Button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

/* ─────────────────────────── Runs Tab ─────────────────────────── */

const RUNS_PAGE_SIZE = 25

function RunsTab() {
  const [resolveTarget, setResolveTarget] = useState<DunningRun | null>(null)
  const [expandedRun, setExpandedRun] = useState<string | null>(null)
  const [runEvents, setRunEvents] = useState<Record<string, DunningEvent[]>>({})
  const [filterStatus, setFilterStatus] = useState('')
  const [page, setPage] = useState(1)
  const queryClient = useQueryClient()

  const queryParams = useMemo(() => {
    const params = new URLSearchParams()
    params.set('limit', String(RUNS_PAGE_SIZE))
    params.set('offset', String((page - 1) * RUNS_PAGE_SIZE))
    if (filterStatus) params.set('state', filterStatus)
    return params.toString()
  }, [page, filterStatus])

  const { data: runsData, isLoading: loading, error: loadError, refetch: loadRuns } = useQuery({
    queryKey: ['dunning-runs', page, filterStatus],
    queryFn: () => Promise.all([
      api.listDunningRuns(queryParams),
      api.listInvoices().catch(() => ({ data: [] as Invoice[], total: 0 })),
      api.listCustomers().catch(() => ({ data: [] as Customer[], total: 0 })),
    ]).then(([runsRes, invoicesRes, custRes]) => {
      const iMap: Record<string, Invoice> = {}
      invoicesRes.data.forEach(inv => { iMap[inv.id] = inv })
      const cMap: Record<string, Customer> = {}
      custRes.data.forEach(c => { cMap[c.id] = c })
      return { runs: runsRes.data || [], total: runsRes.total || 0, invoiceMap: iMap, customerMap: cMap }
    }),
  })

  const runs = runsData?.runs ?? []
  const total = runsData?.total ?? 0
  const invoiceMap = runsData?.invoiceMap ?? {}
  const customerMap = runsData?.customerMap ?? {}

  const loadRunEvents = async (runId: string) => {
    if (runEvents[runId]) return
    try {
      const res = await api.getDunningRun(runId)
      setRunEvents(prev => ({ ...prev, [runId]: res.events || [] }))
    } catch {
      // events are supplementary
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
    { label: 'Active', value: stats.active, color: 'text-blue-600', bg: 'bg-blue-50 dark:bg-blue-500/10', ring: 'ring-blue-200 dark:ring-blue-500/20' },
    { label: 'Escalated', value: stats.escalated, color: 'text-violet-600', bg: 'bg-violet-50 dark:bg-violet-500/10', ring: 'ring-violet-200 dark:ring-violet-500/20' },
    { label: 'Recovered', value: stats.resolved, color: 'text-emerald-600', bg: 'bg-emerald-50 dark:bg-emerald-500/10', ring: 'ring-emerald-200 dark:ring-emerald-500/20' },
    { label: 'At risk', value: stats.atRiskCents, color: 'text-red-600', bg: 'bg-red-50 dark:bg-red-500/10', ring: 'ring-red-200 dark:ring-red-500/20', isCurrency: true },
  ]

  const totalPages = Math.ceil(total / RUNS_PAGE_SIZE)
  const errorMsg = loadError instanceof Error ? loadError.message : loadError ? String(loadError) : null

  return (
    <>
      {/* Summary stats */}
      {!loading && total > 0 && (
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-3 mt-4">
          {statCards.map(stat => (
            <div key={stat.label} className={cn(stat.bg, 'rounded-xl px-4 py-3 ring-1', stat.ring)}>
              <p className="text-xs font-medium text-muted-foreground">{stat.label}</p>
              <p className={cn('text-xl font-semibold mt-1 tabular-nums', stat.color)}>
                {stat.isCurrency ? formatCents(stat.value) : stat.value}
              </p>
            </div>
          ))}
        </div>
      )}

      {/* Filter */}
      {total > 0 && (
        <div className="flex items-center gap-2 mt-4">
          <div className="flex gap-1 bg-muted rounded-lg p-1">
            {[
              { value: '', label: 'All' },
              { value: 'active', label: 'Active' },
              { value: 'escalated', label: 'Escalated' },
              { value: 'resolved', label: 'Recovered' },
            ].map(f => (
              <button key={f.value} onClick={() => { setFilterStatus(f.value); setPage(1) }}
                className={cn(
                  'px-3 py-1 rounded-md text-xs font-medium transition-colors',
                  filterStatus === f.value
                    ? 'bg-background text-foreground shadow-sm'
                    : 'text-muted-foreground hover:text-foreground'
                )}>
                {f.label}
              </button>
            ))}
          </div>
        </div>
      )}

      {/* Runs list */}
      <Card className="mt-4">
        <CardContent className="p-0">
          {errorMsg ? (
            <div className="p-8 text-center">
              <p className="text-sm text-destructive mb-3">{errorMsg}</p>
              <Button variant="outline" size="sm" onClick={() => loadRuns()}>Retry</Button>
            </div>
          ) : loading ? (
            <TableSkeleton columns={8} />
          ) : runs.length === 0 ? (
            <div className="p-12 text-center">
              <p className="text-sm font-medium text-foreground">No dunning runs</p>
              <p className="text-sm text-muted-foreground mt-1">
                Dunning runs will appear here when Velox detects a failed payment and begins the recovery process.
              </p>
            </div>
          ) : (
            <>
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead className="w-8"></TableHead>
                    <TableHead>Customer</TableHead>
                    <TableHead>Invoice</TableHead>
                    <TableHead className="text-right">Amount Due</TableHead>
                    <TableHead>Status</TableHead>
                    <TableHead>Progress</TableHead>
                    <TableHead>Next Retry</TableHead>
                    <TableHead>Started</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {runs.map(run => {
                    const inv = invoiceMap[run.invoice_id]
                    const cust = customerMap[run.customer_id]
                    const isExpanded = expandedRun === run.id
                    const isFinished = run.state !== 'active'

                    return (
                      <Fragment key={run.id}>
                        <TableRow className={cn(isExpanded && 'bg-accent', 'border-l-[3px]', statusBorderColor(run.state))}>
                          <TableCell className="px-3">
                            <button onClick={() => toggleExpand(run.id)}
                              className="w-6 h-6 flex items-center justify-center rounded hover:bg-accent transition-colors">
                              <svg className={cn('w-4 h-4 text-muted-foreground transition-transform', isExpanded && 'rotate-90')}
                                fill="none" stroke="currentColor" viewBox="0 0 24 24">
                                <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M9 5l7 7-7 7" />
                              </svg>
                            </button>
                          </TableCell>
                          <TableCell>
                            <div className="flex items-center gap-2.5">
                              <InitialsAvatar name={cust?.display_name || 'Unknown'} size="xs" />
                              <div>
                                <Link to={`/customers/${run.customer_id}`} onClick={e => e.stopPropagation()}
                                  className="text-sm font-medium text-foreground hover:text-primary">
                                  {cust?.display_name || run.customer_id.slice(0, 8) + '...'}
                                </Link>
                                {cust?.email && <p className="text-xs text-muted-foreground truncate max-w-[180px]" title={cust.email}>{cust.email}</p>}
                              </div>
                            </div>
                          </TableCell>
                          <TableCell>
                            <Link to={`/invoices/${run.invoice_id}`} onClick={e => e.stopPropagation()}
                              className="text-sm font-mono text-primary hover:underline">
                              {inv?.invoice_number || run.invoice_id.slice(0, 8) + '...'}
                            </Link>
                          </TableCell>
                          <TableCell className="text-right tabular-nums font-mono text-sm">
                            {inv ? formatCents(inv.amount_due_cents) : '\u2014'}
                          </TableCell>
                          <TableCell>
                            <div className="flex items-center gap-2">
                              <Badge variant={statusBadgeVariant(run.state)}>{run.state}</Badge>
                              {run.resolution && run.resolution !== run.state && (
                                <Badge variant={run.resolution === 'payment_recovered' ? 'success' : run.resolution === 'manually_resolved' ? 'info' : run.resolution === 'write_off' ? 'warning' : 'outline'}>{run.resolution}</Badge>
                              )}
                              {!isFinished && (
                                <Button variant="outline" size="sm" className="h-6 text-xs ml-1"
                                  onClick={(e) => { e.stopPropagation(); setResolveTarget(run) }}>
                                  Resolve
                                </Button>
                              )}
                            </div>
                          </TableCell>
                          <TableCell>
                            <span className="text-xs text-muted-foreground tabular-nums">
                              {run.attempt_count === 0 ? 'No retries yet' : `${run.attempt_count} ${run.attempt_count === 1 ? 'retry' : 'retries'}`}
                            </span>
                          </TableCell>
                          <TableCell>
                            {run.next_action_at
                              ? <span className="text-xs text-muted-foreground" title={formatDateTime(run.next_action_at)}>{futureRelativeTime(run.next_action_at)}</span>
                              : <span className="text-xs text-muted-foreground">{'\u2014'}</span>
                            }
                          </TableCell>
                          <TableCell>
                            <span className="text-xs text-muted-foreground" title={formatDateTime(run.created_at)}>{relativeTime(run.created_at)}</span>
                          </TableCell>
                        </TableRow>

                        {/* Expanded event timeline */}
                        {isExpanded && (
                          <TableRow>
                            <TableCell colSpan={8} className="bg-accent px-0 py-0">
                              <div className="px-12 py-4 border-t border-border">
                                <p className="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-3">Recovery timeline</p>
                                <RunTimeline events={runEvents[run.id]} run={run} />
                                {run.reason && (
                                  <div className="mt-3 bg-destructive/10 border border-destructive/20 rounded-lg px-3 py-2">
                                    <p className="text-xs font-medium text-destructive">Last failure reason</p>
                                    <p className="text-xs text-destructive/80 mt-0.5">{run.reason}</p>
                                  </div>
                                )}
                              </div>
                            </TableCell>
                          </TableRow>
                        )}
                      </Fragment>
                    )
                  })}
                </TableBody>
              </Table>

              {/* Pagination */}
              {totalPages > 1 && (
                <div className="border-t border-border px-4 py-3 flex items-center justify-between">
                  <p className="text-xs text-muted-foreground">
                    Showing {(page - 1) * RUNS_PAGE_SIZE + 1}{'\u2013'}{Math.min(page * RUNS_PAGE_SIZE, total)} of {total}
                  </p>
                  <Pagination>
                    <PaginationContent>
                      <PaginationItem>
                        <PaginationPrevious
                          onClick={() => setPage(p => Math.max(1, p - 1))}
                          className={cn(page <= 1 && 'pointer-events-none opacity-50')}
                        />
                      </PaginationItem>
                      {Array.from({ length: Math.min(totalPages, 5) }, (_, i) => {
                        let pageNum: number
                        if (totalPages <= 5) {
                          pageNum = i + 1
                        } else if (page <= 3) {
                          pageNum = i + 1
                        } else if (page >= totalPages - 2) {
                          pageNum = totalPages - 4 + i
                        } else {
                          pageNum = page - 2 + i
                        }
                        return (
                          <PaginationItem key={pageNum}>
                            <PaginationLink onClick={() => setPage(pageNum)} isActive={page === pageNum}>
                              {pageNum}
                            </PaginationLink>
                          </PaginationItem>
                        )
                      })}
                      <PaginationItem>
                        <PaginationNext
                          onClick={() => setPage(p => Math.min(totalPages, p + 1))}
                          className={cn(page >= totalPages && 'pointer-events-none opacity-50')}
                        />
                      </PaginationItem>
                    </PaginationContent>
                  </Pagination>
                </div>
              )}
            </>
          )}
        </CardContent>
      </Card>

      {resolveTarget && (
        <ResolveDialog
          run={resolveTarget}
          invoiceMap={invoiceMap}
          onClose={() => setResolveTarget(null)}
          onResolved={() => {
            setResolveTarget(null)
            queryClient.invalidateQueries({ queryKey: ['dunning-runs'] })
            toast.success('Dunning run resolved')
          }}
        />
      )}
    </>
  )
}

/* ─── Event Timeline ─── */

const EVENT_LABELS: Record<string, { label: string; color: string }> = {
  dunning_started: { label: 'Dunning started', color: 'bg-blue-400' },
  retry_scheduled: { label: 'Retry scheduled', color: 'bg-muted-foreground' },
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
      <div className="flex items-center gap-2 text-xs text-muted-foreground">
        <Loader2 size={12} className="animate-spin" />
        Loading timeline...
      </div>
    )
  }

  if (events.length === 0) {
    return <p className="text-xs text-muted-foreground">No events recorded yet</p>
  }

  return (
    <div className="relative">
      {events.map((ev, i) => {
        const meta = EVENT_LABELS[ev.event_type] || { label: ev.event_type.replace(/_/g, ' '), color: 'bg-muted-foreground' }
        return (
          <div key={ev.id} className="flex items-start gap-3 pb-3 last:pb-0 relative">
            {i < events.length - 1 && (
              <div className="absolute left-[7px] top-4 bottom-0 w-px bg-border" />
            )}
            <div className={cn('relative z-10 w-[15px] h-[15px] rounded-full mt-0.5 shrink-0 ring-2 ring-background', meta.color)} />
            <div className="flex-1 min-w-0">
              <div className="flex items-center gap-2">
                <p className="text-sm font-medium text-foreground">{meta.label}</p>
                {ev.attempt_count > 0 && (
                  <span className="text-xs text-muted-foreground">Attempt {ev.attempt_count}</span>
                )}
              </div>
              {ev.reason && <p className="text-xs text-muted-foreground mt-0.5">{ev.reason}</p>}
              <p className="text-xs text-muted-foreground mt-0.5">{formatDateTime(ev.created_at)}</p>
            </div>
          </div>
        )
      })}
    </div>
  )
}

/* ─── Resolve Dialog ─── */

function ResolveDialog({ run, invoiceMap, onClose, onResolved }: {
  run: DunningRun
  invoiceMap: Record<string, Invoice>
  onClose: () => void
  onResolved: () => void
}) {
  const [resolution, setResolution] = useState('payment_recovered')
  const [saving, setSaving] = useState(false)

  const inv = invoiceMap[run.invoice_id]

  const resolutionOptions = [
    { value: 'payment_recovered', label: 'Payment recovered', description: 'Customer has paid -- mark invoice as paid and close dunning.', variant: 'default' as const },
    { value: 'manually_resolved', label: 'Manually resolved', description: 'Issue resolved through other means (offline payment, negotiation, etc.)', variant: 'default' as const },
    { value: 'invoice_not_collectible', label: 'Write off invoice', description: 'Mark the invoice as uncollectible. This will void the invoice.', variant: 'destructive' as const },
  ]

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setSaving(true)
    try {
      await api.resolveDunningRun(run.id, resolution)
      onResolved()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to resolve dunning run')
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose() }}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Resolve Dunning Run</DialogTitle>
        </DialogHeader>
        <form onSubmit={handleSubmit} noValidate className="space-y-4">
          {/* Context */}
          <div className="bg-muted rounded-lg p-3 flex items-center justify-between">
            <div>
              <p className="text-xs text-muted-foreground">Invoice</p>
              <p className="text-sm font-mono font-medium text-foreground">{inv?.invoice_number || run.invoice_id.slice(0, 8) + '...'}</p>
            </div>
            <div className="text-right">
              <p className="text-xs text-muted-foreground">Amount due</p>
              <p className="text-sm font-semibold text-foreground tabular-nums">{inv ? formatCents(inv.amount_due_cents) : '\u2014'}</p>
            </div>
          </div>

          {/* Resolution options */}
          <div>
            <Label className="mb-2 block">Resolution</Label>
            <div className="space-y-2">
              {resolutionOptions.map(opt => {
                const selected = resolution === opt.value
                return (
                  <button key={opt.value} type="button"
                    onClick={() => setResolution(opt.value)}
                    className={cn(
                      'w-full text-left px-4 py-3 rounded-lg border-2 transition-all',
                      selected
                        ? opt.variant === 'destructive'
                          ? 'border-destructive bg-destructive/5 ring-1 ring-destructive/20'
                          : 'border-primary bg-primary/5 ring-1 ring-primary/20'
                        : 'border-border hover:border-border/80 hover:bg-accent'
                    )}>
                    <p className={cn('text-sm font-medium', selected ? (opt.variant === 'destructive' ? 'text-destructive' : 'text-primary') : 'text-foreground')}>
                      {opt.label}
                    </p>
                    <p className="text-xs text-muted-foreground mt-0.5">{opt.description}</p>
                  </button>
                )
              })}
            </div>
          </div>

          {resolution === 'invoice_not_collectible' && (
            <div className="bg-destructive/10 border border-destructive/20 rounded-lg p-3">
              <p className="text-xs font-medium text-destructive">
                This action will void the invoice, reverse any credits applied, and cancel the Stripe payment intent. This cannot be undone.
              </p>
            </div>
          )}

          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
            <Button type="submit" disabled={saving}
              variant={resolution === 'invoice_not_collectible' ? 'destructive' : 'default'}>
              {saving ? 'Resolving...' : 'Resolve'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
