import { useEffect, useMemo, useState } from 'react'
import { useLocation } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import {
  AlertTriangle,
  CheckCircle2,
  History,
  Layers,
  Loader2,
  Ticket,
  XCircle,
} from 'lucide-react'

import { Layout } from '@/components/Layout'
import {
  api,
  formatDateTime,
  type BulkActionApplyCouponRequest,
  type BulkActionCommitResponse,
  type BulkActionCustomerFilter,
  type BulkActionListItem,
  type BulkActionScheduleCancelRequest,
  type BulkActionStatus,
  type Coupon,
} from '@/lib/api'
import { showApiError } from '@/lib/formErrors'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent } from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { EmptyState } from '@/components/EmptyState'

type ActionTab = 'apply_coupon' | 'schedule_cancel'
type FilterMode = 'all' | 'ids'

// genIdempotencyKey produces a fresh key for each commit attempt; the
// operator can override it (we surface it in the confirm modal) so a
// retry of a half-finished run reuses the same key and short-circuits
// to the prior row instead of re-firing.
function genIdempotencyKey(prefix: string): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return `${prefix}_${crypto.randomUUID()}`
  }
  return `${prefix}_${Date.now()}_${Math.random().toString(36).slice(2, 10)}`
}

// statusVariant maps the server's status string onto the badge variants
// the rest of the dashboard uses, keeping the colour story consistent
// with subscriptions / invoices / etc.
function statusVariant(s: BulkActionStatus): 'default' | 'secondary' | 'destructive' | 'outline' {
  switch (s) {
    case 'completed':
      return 'default'
    case 'partial':
      return 'secondary'
    case 'failed':
      return 'destructive'
    default:
      return 'outline'
  }
}

function actionTypeLabel(t: string): string {
  if (t === 'apply_coupon') return 'Apply coupon'
  if (t === 'schedule_cancel') return 'Schedule cancel'
  return t
}

// extractCohortFromRoute pulls preselected customer IDs off the
// react-router location.state. The Customers page passes them when an
// operator clicks the Bulk actions dropdown so we open this page
// pre-scoped to that selection.
function extractCohortFromRoute(state: unknown): string[] {
  if (!state || typeof state !== 'object') return []
  const ids = (state as { customer_ids?: unknown }).customer_ids
  if (!Array.isArray(ids)) return []
  return ids.filter((s): s is string => typeof s === 'string' && s.length > 0)
}

export default function BulkActionsPage() {
  const queryClient = useQueryClient()
  const location = useLocation()

  // --- Form state ---------------------------------------------------
  const [tab, setTab] = useState<ActionTab>('apply_coupon')
  const [filterMode, setFilterMode] = useState<FilterMode>('all')
  const [customerIdsInput, setCustomerIdsInput] = useState<string>('')
  const [couponCode, setCouponCode] = useState<string>('')
  const [atPeriodEnd, setAtPeriodEnd] = useState<boolean>(true)
  const [cancelAt, setCancelAt] = useState<string>('')
  const [showConfirm, setShowConfirm] = useState(false)
  const [idempotencyKey, setIdempotencyKey] = useState<string>(() => genIdempotencyKey('bact'))
  const [committed, setCommitted] = useState<BulkActionCommitResponse | null>(null)

  // Hydrate cohort from the Customers page selection if we routed in
  // with state; runs once on mount.
  useEffect(() => {
    const ids = extractCohortFromRoute(location.state)
    if (ids.length > 0) {
      setFilterMode('ids')
      setCustomerIdsInput(ids.join('\n'))
    }
  }, [location.state])

  // --- Data fetches -------------------------------------------------
  const { data: couponsData, isLoading: couponsLoading } = useQuery({
    queryKey: ['coupons', 'active-for-bulk'],
    queryFn: () => api.listCoupons(),
  })
  const coupons: Coupon[] = couponsData?.data ?? []

  const { data: listData } = useQuery({
    queryKey: ['bulk-actions', 'list'],
    queryFn: () => api.bulkActions.list({ limit: 10 }),
  })
  const recent: BulkActionListItem[] = listData?.bulk_actions ?? []

  // --- Server interactions -----------------------------------------
  const buildFilter = (): BulkActionCustomerFilter => {
    if (filterMode === 'ids') {
      return {
        type: 'ids',
        ids: customerIdsInput
          .split(/[\s,]+/)
          .map((s) => s.trim())
          .filter(Boolean),
      }
    }
    return { type: 'all' }
  }

  const applyMutation = useMutation({
    mutationFn: (req: BulkActionApplyCouponRequest) => api.bulkActions.applyCoupon(req),
    onSuccess: (res) => {
      setCommitted(res)
      setShowConfirm(false)
      queryClient.invalidateQueries({ queryKey: ['bulk-actions'] })
      queryClient.invalidateQueries({ queryKey: ['customers'] })
      if (res.idempotent_replay) {
        toast.info('Same idempotency key already applied — replaying prior result.')
      } else {
        toast.success(`Coupon applied to ${res.succeeded_count}/${res.target_count} customers`)
      }
    },
    onError: (err) => showApiError(err, 'Failed to apply coupon'),
  })

  const cancelMutation = useMutation({
    mutationFn: (req: BulkActionScheduleCancelRequest) => api.bulkActions.scheduleCancel(req),
    onSuccess: (res) => {
      setCommitted(res)
      setShowConfirm(false)
      queryClient.invalidateQueries({ queryKey: ['bulk-actions'] })
      queryClient.invalidateQueries({ queryKey: ['subscriptions'] })
      if (res.idempotent_replay) {
        toast.info('Same idempotency key already applied — replaying prior result.')
      } else {
        toast.success(`Scheduled cancel on ${res.succeeded_count}/${res.target_count} customers`)
      }
    },
    onError: (err) => showApiError(err, 'Failed to schedule cancel'),
  })

  const isPending = applyMutation.isPending || cancelMutation.isPending

  const cohortIds = useMemo(
    () =>
      customerIdsInput
        .split(/[\s,]+/)
        .map((s) => s.trim())
        .filter(Boolean),
    [customerIdsInput],
  )

  const filterValid =
    filterMode === 'all' || (filterMode === 'ids' && cohortIds.length > 0)

  const applyValid = filterValid && couponCode.trim().length > 0
  const cancelValid =
    filterValid && (atPeriodEnd ? !cancelAt : cancelAt.trim().length > 0)

  const canSubmit = tab === 'apply_coupon' ? applyValid : cancelValid

  const onSubmit = () => {
    if (!canSubmit) return
    setIdempotencyKey(genIdempotencyKey('bact'))
    setShowConfirm(true)
  }

  const onConfirm = () => {
    if (tab === 'apply_coupon') {
      applyMutation.mutate({
        idempotency_key: idempotencyKey,
        customer_filter: buildFilter(),
        coupon_code: couponCode.trim(),
      })
      return
    }
    const req: BulkActionScheduleCancelRequest = {
      idempotency_key: idempotencyKey,
      customer_filter: buildFilter(),
    }
    if (atPeriodEnd) {
      req.at_period_end = true
    } else {
      req.cancel_at = new Date(cancelAt).toISOString()
    }
    cancelMutation.mutate(req)
  }

  const reset = () => {
    setTab('apply_coupon')
    setFilterMode('all')
    setCustomerIdsInput('')
    setCouponCode('')
    setAtPeriodEnd(true)
    setCancelAt('')
    setCommitted(null)
    applyMutation.reset()
    cancelMutation.reset()
  }

  return (
    <Layout>
      <div className="container mx-auto px-4 py-8 max-w-7xl">
        <div className="flex items-start justify-between mb-8">
          <div>
            <h1 className="text-2xl font-semibold flex items-center gap-2">
              <Layers className="h-6 w-6" />
              Bulk actions
            </h1>
            <p className="text-sm text-muted-foreground mt-1 max-w-2xl">
              Run an operator action across many customers at once. v1 supports
              applying a coupon and scheduling cancellations on every active
              subscription. Each run is idempotent on the key in the confirm
              dialog and is recorded in the audit log per customer.
            </p>
          </div>
          {committed && (
            <Button variant="outline" onClick={reset}>
              Start over
            </Button>
          )}
        </div>

        <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">
          <div className="lg:col-span-2 space-y-6">
            {/* Tabs */}
            <div className="inline-flex gap-1 bg-muted rounded-lg p-1">
              <button
                onClick={() => setTab('apply_coupon')}
                className={
                  'px-3 py-1.5 rounded-md text-xs font-medium transition-colors ' +
                  (tab === 'apply_coupon'
                    ? 'bg-background text-foreground shadow-sm'
                    : 'text-muted-foreground hover:text-foreground')
                }
              >
                <span className="inline-flex items-center gap-1.5">
                  <Ticket className="h-3.5 w-3.5" />
                  Apply coupon
                </span>
              </button>
              <button
                onClick={() => setTab('schedule_cancel')}
                className={
                  'px-3 py-1.5 rounded-md text-xs font-medium transition-colors ' +
                  (tab === 'schedule_cancel'
                    ? 'bg-background text-foreground shadow-sm'
                    : 'text-muted-foreground hover:text-foreground')
                }
              >
                <span className="inline-flex items-center gap-1.5">
                  <XCircle className="h-3.5 w-3.5" />
                  Schedule cancel
                </span>
              </button>
            </div>

            <Card>
              <CardContent className="p-6 space-y-6">
                {/* Cohort selector — shared between both tabs */}
                <div className="space-y-2">
                  <Label>Customer cohort</Label>
                  <Select
                    value={filterMode}
                    onValueChange={(v) => setFilterMode(v as FilterMode)}
                  >
                    <SelectTrigger>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="all">All customers</SelectItem>
                      <SelectItem value="ids">Specific customer IDs</SelectItem>
                    </SelectContent>
                  </Select>
                  <p className="text-xs text-muted-foreground">
                    "All" iterates every customer for the tenant (capped server-side
                    at 500 to keep one run safe). Pre-scope from the Customers page
                    to operate on a hand-picked list.
                  </p>
                </div>

                {filterMode === 'ids' && (
                  <div className="space-y-2">
                    <Label>Customer IDs (one per line, or comma-separated)</Label>
                    <textarea
                      value={customerIdsInput}
                      onChange={(e) => setCustomerIdsInput(e.target.value)}
                      placeholder="vlx_cus_abc&#10;vlx_cus_def"
                      rows={4}
                      className="w-full rounded-md border bg-background px-3 py-2 text-sm font-mono"
                    />
                    {cohortIds.length > 0 && (
                      <p className="text-xs text-muted-foreground">
                        {cohortIds.length} customer{cohortIds.length === 1 ? '' : 's'}{' '}
                        selected.
                      </p>
                    )}
                  </div>
                )}

                {/* Apply coupon — coupon picker */}
                {tab === 'apply_coupon' && (
                  <div className="space-y-2">
                    <Label>Coupon</Label>
                    <Select
                      value={couponCode}
                      onValueChange={setCouponCode}
                      disabled={couponsLoading}
                    >
                      <SelectTrigger>
                        <SelectValue placeholder="Select an active coupon" />
                      </SelectTrigger>
                      <SelectContent>
                        {coupons
                          .filter((c) => !c.archived_at)
                          .map((c) => (
                            <SelectItem key={c.id} value={c.code}>
                              <span className="font-mono mr-2">{c.code}</span>
                              <span className="text-muted-foreground">{c.name}</span>
                            </SelectItem>
                          ))}
                      </SelectContent>
                    </Select>
                    {coupons.length === 0 && !couponsLoading && (
                      <p className="text-xs text-muted-foreground">
                        No active coupons. Create one on the{' '}
                        <a href="/coupons" className="underline">
                          Coupons page
                        </a>
                        .
                      </p>
                    )}
                  </div>
                )}

                {/* Schedule cancel — at-period-end / explicit cancel_at */}
                {tab === 'schedule_cancel' && (
                  <>
                    <div className="flex items-center justify-between rounded-md border p-3">
                      <div>
                        <Label>Cancel at end of current period</Label>
                        <p className="text-xs text-muted-foreground mt-1">
                          Most natural for end-of-life flows — keeps service through the
                          paid period, then stops.
                        </p>
                      </div>
                      <Switch
                        checked={atPeriodEnd}
                        onCheckedChange={(v) => setAtPeriodEnd(!!v)}
                      />
                    </div>

                    {!atPeriodEnd && (
                      <div className="space-y-2">
                        <Label>Cancel at (UTC)</Label>
                        <Input
                          type="datetime-local"
                          value={cancelAt}
                          onChange={(e) => setCancelAt(e.target.value)}
                        />
                        <p className="text-xs text-muted-foreground">
                          Must be at or after each subscription's current
                          billing_period_end; the server rejects earlier
                          timestamps with code <span className="font-mono">cancel_at_too_early</span>.
                        </p>
                      </div>
                    )}
                  </>
                )}

                <div className="flex items-center justify-end pt-2">
                  <Button onClick={onSubmit} disabled={!canSubmit || isPending}>
                    {isPending ? (
                      <>
                        <Loader2 className="h-4 w-4 mr-2 animate-spin" />
                        Running…
                      </>
                    ) : tab === 'apply_coupon' ? (
                      'Apply coupon'
                    ) : (
                      'Schedule cancel'
                    )}
                  </Button>
                </div>
              </CardContent>
            </Card>

            {/* Result panel */}
            {committed && (
              <Card>
                <CardContent className="p-6">
                  <div className="flex items-start gap-3">
                    {committed.failed_count === 0 ? (
                      <CheckCircle2 className="h-6 w-6 text-green-600 mt-0.5" />
                    ) : (
                      <AlertTriangle className="h-6 w-6 text-amber-500 mt-0.5" />
                    )}
                    <div className="flex-1">
                      <h2 className="text-lg font-semibold">
                        {committed.idempotent_replay
                          ? 'Replayed prior bulk action'
                          : committed.failed_count === 0
                            ? 'Bulk action completed'
                            : 'Bulk action partially completed'}
                      </h2>
                      <p className="text-sm text-muted-foreground mt-1">
                        {committed.succeeded_count}/{committed.target_count} customers
                        succeeded
                        {committed.failed_count > 0 && (
                          <>
                            , {committed.failed_count} failed
                          </>
                        )}
                        . Per-customer outcomes are written to the audit log.
                      </p>
                      <div className="mt-3 text-xs font-mono text-muted-foreground">
                        Bulk action ID: {committed.bulk_action_id}
                      </div>
                      {committed.errors.length > 0 && (
                        <div className="mt-4">
                          <h3 className="text-sm font-medium mb-2">Errors</h3>
                          <div className="rounded-md border max-h-48 overflow-auto">
                            <Table>
                              <TableHeader>
                                <TableRow>
                                  <TableHead className="text-xs">Customer</TableHead>
                                  <TableHead className="text-xs">Error</TableHead>
                                </TableRow>
                              </TableHeader>
                              <TableBody>
                                {committed.errors.map((e, i) => (
                                  <TableRow key={`${e.customer_id}-${i}`}>
                                    <TableCell className="font-mono text-xs">
                                      {e.customer_id}
                                    </TableCell>
                                    <TableCell className="text-xs">{e.error}</TableCell>
                                  </TableRow>
                                ))}
                              </TableBody>
                            </Table>
                          </div>
                        </div>
                      )}
                    </div>
                  </div>
                </CardContent>
              </Card>
            )}
          </div>

          {/* Recent runs sidebar */}
          <div>
            <Card>
              <CardContent className="p-4">
                <h3 className="text-sm font-semibold mb-3 flex items-center gap-2">
                  <History className="h-4 w-4" />
                  Recent bulk actions
                </h3>
                {recent.length === 0 ? (
                  <EmptyState
                    title="No bulk actions yet"
                    description="The first run lands here."
                  />
                ) : (
                  <ul className="space-y-3">
                    {recent.map((r) => (
                      <li
                        key={r.bulk_action_id}
                        className="border-b last:border-b-0 pb-3 last:pb-0"
                      >
                        <div className="flex items-center gap-2 mb-1">
                          <Badge variant="outline" className="text-[10px]">
                            {actionTypeLabel(r.action_type)}
                          </Badge>
                          <Badge variant={statusVariant(r.status)} className="text-[10px]">
                            {r.status}
                          </Badge>
                        </div>
                        <div className="text-xs text-muted-foreground">
                          {r.succeeded_count}/{r.target_count} succeeded
                          {r.failed_count > 0 && (
                            <>
                              , {r.failed_count} failed
                            </>
                          )}
                        </div>
                        <div className="text-[11px] text-muted-foreground mt-1">
                          {formatDateTime(r.created_at)}
                        </div>
                      </li>
                    ))}
                  </ul>
                )}
              </CardContent>
            </Card>
          </div>
        </div>
      </div>

      {/* Confirm modal */}
      <Dialog open={showConfirm} onOpenChange={setShowConfirm}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              {tab === 'apply_coupon' ? 'Apply coupon to cohort?' : 'Schedule cancel on cohort?'}
            </DialogTitle>
            <DialogDescription>
              {tab === 'apply_coupon' ? (
                <>
                  Attach <span className="font-mono">{couponCode}</span> to{' '}
                  {filterMode === 'all'
                    ? 'every customer'
                    : `${cohortIds.length} customer${cohortIds.length === 1 ? '' : 's'}`}
                  . Per-customer assignments are idempotent on the bulk-action key plus
                  the customer id.
                </>
              ) : (
                <>
                  Schedule cancel on every active subscription for{' '}
                  {filterMode === 'all'
                    ? 'every customer'
                    : `${cohortIds.length} customer${cohortIds.length === 1 ? '' : 's'}`}
                  ,{' '}
                  {atPeriodEnd
                    ? 'at the end of each subscription\u2019s current billing period'
                    : `on ${cancelAt} UTC`}
                  .
                </>
              )}
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-2">
            <Label>Idempotency key</Label>
            <Input
              value={idempotencyKey}
              onChange={(e) => setIdempotencyKey(e.target.value)}
              className="font-mono text-xs"
            />
            <p className="text-xs text-muted-foreground">
              Re-firing this exact key returns the prior result without re-applying.
              Auto-generated; edit only if you're retrying a specific request.
            </p>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setShowConfirm(false)}>
              Cancel
            </Button>
            <Button onClick={onConfirm} disabled={isPending || !idempotencyKey.trim()}>
              {isPending ? (
                <>
                  <Loader2 className="h-4 w-4 mr-2 animate-spin" />
                  Running…
                </>
              ) : (
                'Confirm'
              )}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Layout>
  )
}
