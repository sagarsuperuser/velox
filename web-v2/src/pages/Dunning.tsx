import { Fragment, useState, useMemo } from 'react'
import { Link } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import {
  api,
  formatDateTime,
  formatCents,
  type DunningRun,
  type DunningEvent,
  type Invoice,
  type Customer,
} from '@/lib/api'
import { Layout } from '@/components/Layout'
import { TestClockBadge } from '@/components/TestClockBadge'
import { showApiError } from '@/lib/formErrors'
import { cn } from '@/lib/utils'
import { statusBadgeVariant, statusBorderColor } from '@/lib/status'
import { InitialsAvatar } from '@/components/InitialsAvatar'

import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent } from '@/components/ui/card'
import { Label } from '@/components/ui/label'
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

import { Loader2 } from 'lucide-react'

// effectiveNowMs resolves the "now" baseline for relative-time
// formatting on dunning rows. When effectiveNowISO is provided (the
// run's owning sub is clock-pinned and the backend embedded the
// clock's frozen_time on the row), use that; otherwise fall back to
// wall-clock. Replaces the prior 24h-divergence heuristic.
function effectiveNowMs(effectiveNowISO?: string): number {
  if (effectiveNowISO) {
    const ts = new Date(effectiveNowISO).getTime()
    if (!Number.isNaN(ts)) return ts
  }
  return Date.now()
}

// Human labels for the resolution enum — raw values like
// "payment_recovered" are backend identifiers, not operator copy.
const RESOLUTION_LABELS: Record<string, string> = {
  payment_recovered: 'Payment recovered',
  manually_resolved: 'Manually resolved',
  write_off: 'Written off',
  invoice_not_collectible: 'Uncollectible',
}

function relativeTime(dateStr: string, effectiveNowISO?: string): string {
  const now = effectiveNowMs(effectiveNowISO)
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

function futureRelativeTime(dateStr: string, effectiveNowISO?: string): string {
  const now = effectiveNowMs(effectiveNowISO)
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
  return (
    <Layout>
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold text-foreground">Dunning</h1>
          <p className="text-sm text-muted-foreground mt-1">
            Recover failed payments automatically
          </p>
        </div>
        <Link to="/dunning-policies">
          <Button variant="outline" size="sm">Manage policies</Button>
        </Link>
      </div>
      <div className="mt-6">
        <RunsTab />
      </div>
    </Layout>
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
      api.listSubscriptions().catch(() => ({ data: [], total: 0 })),
    ]).then(([runsRes, invoicesRes, custRes, subsRes]) => {
      const iMap: Record<string, Invoice> = {}
      invoicesRes.data.forEach(inv => { iMap[inv.id] = inv })
      const cMap: Record<string, Customer> = {}
      custRes.data.forEach(c => { cMap[c.id] = c })
      const sMap: Record<string, string> = {}
      subsRes.data.forEach(s => { if (s.test_clock_id) sMap[s.id] = s.test_clock_id })
      return { runs: runsRes.data || [], total: runsRes.total || 0, invoiceMap: iMap, customerMap: cMap, subTestClockMap: sMap }
    }),
  })

  // Aggregate stats for the dashboard cards. Comes from a backend
  // COUNT(*) GROUP BY state + SUM(amount_due_cents) query — accurate
  // regardless of pagination or filter on the runs list. Deriving
  // counts from the paginated `runs` slice silently undercounts as
  // soon as total runs exceed the page size.
  const { data: stats } = useQuery({
    queryKey: ['dunning-stats'],
    queryFn: () => api.getDunningStats(),
  })

  const runs = runsData?.runs ?? []
  const total = runsData?.total ?? 0
  const invoiceMap = runsData?.invoiceMap ?? {}
  const subTestClockMap = runsData?.subTestClockMap ?? {}
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

  const statCards = [
    { label: 'Active', value: stats?.active_count ?? 0, color: 'text-blue-600', bg: 'bg-blue-50 dark:bg-blue-500/10', ring: 'ring-blue-200 dark:ring-blue-500/20' },
    { label: 'Escalated', value: stats?.escalated_count ?? 0, color: 'text-violet-600', bg: 'bg-violet-50 dark:bg-violet-500/10', ring: 'ring-violet-200 dark:ring-violet-500/20' },
    { label: 'Recovered', value: stats?.resolved_count ?? 0, color: 'text-emerald-600', bg: 'bg-emerald-50 dark:bg-emerald-500/10', ring: 'ring-emerald-200 dark:ring-emerald-500/20' },
    { label: 'At risk', value: stats?.at_risk_cents ?? 0, color: 'text-red-600', bg: 'bg-red-50 dark:bg-red-500/10', ring: 'ring-red-200 dark:ring-red-500/20', isCurrency: true },
  ]

  const totalPages = Math.ceil(total / RUNS_PAGE_SIZE)
  const errorMsg = loadError instanceof Error ? loadError.message : loadError ? String(loadError) : null

  return (
    <>
      {/* Summary stats — only meaningful on the unfiltered view; filter chips
          drive the table beneath. Showing them while filtered would invert
          the relationship (stat counts for one state, table empty for another). */}
      {!loading && total > 0 && !filterStatus && (
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

      {/* Filter — keep visible whenever a filter is active, even if the
          filtered total is zero, so the user can always click back to All. */}
      {(total > 0 || filterStatus) && (
        <div className="flex items-center gap-2 mt-4">
          <div className="flex gap-1 bg-muted rounded-lg p-1">
            {[
              { value: '', label: 'All' },
              { value: 'active', label: 'Active' },
              { value: 'escalated', label: 'Escalated' },
              { value: 'resolved', label: 'Recovered' },
            ].map(f => (
              <button key={f.value} type="button" onClick={() => { setFilterStatus(f.value); setPage(1) }}
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
              {filterStatus ? (
                <>
                  <p className="text-sm font-medium text-foreground">
                    No {filterStatus === 'resolved' ? 'recovered' : filterStatus} runs
                  </p>
                  <p className="text-sm text-muted-foreground mt-1">
                    Try a different filter to see other dunning runs.
                  </p>
                </>
              ) : (
                <>
                  <p className="text-sm font-medium text-foreground">No dunning runs</p>
                  <p className="text-sm text-muted-foreground mt-1">
                    Dunning runs will appear here when Velox detects a failed payment and begins the recovery process.
                  </p>
                  <Link to="/dunning-policies" className="inline-block mt-4">
                    <Button variant="outline" size="sm">Configure a dunning policy</Button>
                  </Link>
                </>
              )}
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
                            <div className="flex items-center gap-1.5">
                              <Link to={`/invoices/${run.invoice_id}`} onClick={e => e.stopPropagation()}
                                className="text-sm font-mono text-primary hover:underline">
                                {run.invoice_number || inv?.invoice_number || run.invoice_id.slice(0, 8) + '...'}
                              </Link>
                              {inv?.subscription_id && subTestClockMap[inv.subscription_id] && (
                                <TestClockBadge testClockId={subTestClockMap[inv.subscription_id]} />
                              )}
                            </div>
                          </TableCell>
                          <TableCell className="text-right tabular-nums font-mono text-sm">
                            {/* Prefer the backend-embedded denormalised
                                amount (server-side JOIN). Falls back to
                                the invoiceMap lookup for rows where the
                                join failed (rare RLS gap). */}
                            {run.invoice_amount_due_cents !== undefined && run.invoice_amount_due_cents !== 0
                              ? formatCents(run.invoice_amount_due_cents)
                              : inv ? formatCents(inv.amount_due_cents) : '\u2014'}
                          </TableCell>
                          <TableCell>
                            <div className="flex items-center gap-2">
                              <Badge variant={statusBadgeVariant(run.state)}>{run.state}</Badge>
                              {run.resolution && run.resolution !== run.state && (
                                <Badge variant={run.resolution === 'payment_recovered' ? 'success' : run.resolution === 'manually_resolved' ? 'info' : run.resolution === 'write_off' ? 'warning' : 'outline'}>{RESOLUTION_LABELS[run.resolution] ?? run.resolution}</Badge>
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
                              ? <span className="text-xs text-muted-foreground" title={formatDateTime(run.next_action_at)}>{futureRelativeTime(run.next_action_at, run.effective_now)}</span>
                              : <span className="text-xs text-muted-foreground">{'\u2014'}</span>
                            }
                          </TableCell>
                          <TableCell>
                            <span className="text-xs text-muted-foreground" title={formatDateTime(run.created_at)}>{relativeTime(run.created_at, run.effective_now)}</span>
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
    { value: 'invoice_not_collectible', label: 'Write off invoice', description: 'Marks the invoice as uncollectible (bad debt). Halts dunning automation. The invoice stays on the books for audit. Subscription stays active — cancel separately if needed.', variant: 'destructive' as const },
  ]

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setSaving(true)
    try {
      await api.resolveDunningRun(run.id, resolution)
      onResolved()
    } catch (err) {
      showApiError(err, 'Failed to resolve dunning run')
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
            <div className="bg-amber-500/10 border border-amber-500/20 rounded-lg p-3">
              <p className="text-xs font-medium text-amber-800 dark:text-amber-300">
                Marks the invoice <strong>uncollectible</strong> — recorded as bad debt. No refund, no credit reversal, no void. The invoice can still be recovered later via Record Payment (Stripe-parity uncollectible → paid) or reclassified via Void if the situation changes.
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
