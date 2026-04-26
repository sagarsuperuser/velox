import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { ArrowRight, Loader2, Users, Wand2, AlertTriangle, CheckCircle2, History } from 'lucide-react'

import { Layout } from '@/components/Layout'
import {
  api,
  formatCents,
  formatDateTime,
  type Plan,
  type PlanMigrationCustomerFilter,
  type PlanMigrationListItem,
  type PlanMigrationPreviewResponse,
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
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { EmptyState } from '@/components/EmptyState'

// Step labels mirror the operator's mental model: pick the move, see the
// projected impact, then commit. Each step gates the next so a half-filled
// form can't trigger a commit by accident.
type Step = 'configure' | 'preview' | 'committed'

function genIdempotencyKey(): string {
  // crypto.randomUUID is available in every browser the dashboard targets.
  // Fall back to timestamp+random for ancient browsers (defence in depth —
  // the backend still requires a non-blank key, so any value is fine here
  // as long as the operator can re-fire the same key on a network retry).
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return `pmig_${crypto.randomUUID()}`
  }
  return `pmig_${Date.now()}_${Math.random().toString(36).slice(2, 10)}`
}

export default function PlanMigrationPage() {
  const queryClient = useQueryClient()

  // --- Form state ---------------------------------------------------
  const [step, setStep] = useState<Step>('configure')
  const [fromPlanId, setFromPlanId] = useState<string>('')
  const [toPlanId, setToPlanId] = useState<string>('')
  const [filterType, setFilterType] = useState<'all' | 'ids' | 'tag'>('all')
  const [customerIds, setCustomerIds] = useState<string>('')
  const [tagValue, setTagValue] = useState<string>('')
  const [effective, setEffective] = useState<'immediate' | 'next_period'>('next_period')
  const [idempotencyKey, setIdempotencyKey] = useState<string>(() => genIdempotencyKey())
  const [showConfirm, setShowConfirm] = useState(false)
  const [committedResult, setCommittedResult] = useState<{
    migration_id: string
    applied_count: number
    idempotent_replay?: boolean
  } | null>(null)

  // --- Data fetches -------------------------------------------------
  const { data: plansData, isLoading: plansLoading } = useQuery({
    queryKey: ['plans'],
    queryFn: () => api.listPlans(),
  })
  const plans: Plan[] = plansData?.data ?? []
  const fromPlan = plans.find((p) => p.id === fromPlanId)
  const toPlan = plans.find((p) => p.id === toPlanId)

  const { data: migrationsData } = useQuery({
    queryKey: ['plan-migrations', 'list'],
    queryFn: () => api.listPlanMigrations({ limit: 10 }),
  })
  const recent: PlanMigrationListItem[] = migrationsData?.migrations ?? []

  // --- Server interactions -----------------------------------------
  const buildFilter = (): PlanMigrationCustomerFilter => {
    if (filterType === 'ids') {
      return {
        type: 'ids',
        ids: customerIds.split(/[\s,]+/).map((s) => s.trim()).filter(Boolean),
      }
    }
    if (filterType === 'tag') {
      return { type: 'tag', value: tagValue }
    }
    return { type: 'all' }
  }

  const previewMutation = useMutation({
    mutationFn: () =>
      api.previewPlanMigration({
        from_plan_id: fromPlanId,
        to_plan_id: toPlanId,
        customer_filter: buildFilter(),
      }),
    onSuccess: () => setStep('preview'),
    onError: (err) => showApiError(err, 'Failed to load preview'),
  })

  const commitMutation = useMutation({
    mutationFn: () =>
      api.commitPlanMigration({
        from_plan_id: fromPlanId,
        to_plan_id: toPlanId,
        customer_filter: buildFilter(),
        idempotency_key: idempotencyKey,
        effective,
      }),
    onSuccess: (res) => {
      setCommittedResult({
        migration_id: res.migration_id,
        applied_count: res.applied_count,
        idempotent_replay: res.idempotent_replay,
      })
      setShowConfirm(false)
      setStep('committed')
      queryClient.invalidateQueries({ queryKey: ['plan-migrations'] })
      queryClient.invalidateQueries({ queryKey: ['subscriptions'] })
      if (res.idempotent_replay) {
        toast.info('Same idempotency key already applied — replaying prior result.')
      } else {
        toast.success(`Migration committed: ${res.applied_count} subscription items updated`)
      }
    },
    onError: (err) => showApiError(err, 'Failed to commit migration'),
  })

  // --- Render -------------------------------------------------------
  const previewResult: PlanMigrationPreviewResponse | undefined = previewMutation.data

  const cohortSize = previewResult?.previews.length ?? 0
  const totalDelta = useMemo(() => {
    if (!previewResult || previewResult.totals.length === 0) return null
    const t = previewResult.totals[0]
    return { currency: t.currency, delta: t.delta_amount_cents }
  }, [previewResult])

  const fromValid = !!fromPlanId
  const toValid = !!toPlanId && toPlanId !== fromPlanId
  const filterValid =
    filterType === 'all' ||
    (filterType === 'ids' && customerIds.trim().length > 0) ||
    (filterType === 'tag' && tagValue.trim().length > 0)
  const canPreview = fromValid && toValid && filterValid

  const reset = () => {
    setStep('configure')
    setFromPlanId('')
    setToPlanId('')
    setFilterType('all')
    setCustomerIds('')
    setTagValue('')
    setEffective('next_period')
    setIdempotencyKey(genIdempotencyKey())
    setCommittedResult(null)
    previewMutation.reset()
    commitMutation.reset()
  }

  return (
    <Layout>
      <div className="container mx-auto px-4 py-8 max-w-7xl">
        <div className="flex items-start justify-between mb-8">
          <div>
            <h1 className="text-2xl font-semibold flex items-center gap-2">
              <Wand2 className="h-6 w-6" />
              Plan migration
            </h1>
            <p className="text-sm text-muted-foreground mt-1 max-w-2xl">
              Bulk-swap a cohort of subscribers from one plan to another. Preview the projected billing
              impact before committing. Per-customer changes are recorded in the audit log.
            </p>
          </div>
          <div className="flex items-center gap-2">
            <Link to="/plan-migrations/history">
              <Button variant="outline">
                <History className="h-4 w-4 mr-2" />
                View history
              </Button>
            </Link>
            {step !== 'configure' && (
              <Button variant="outline" onClick={reset}>
                Start over
              </Button>
            )}
          </div>
        </div>

        {/* Step indicator */}
        <div className="flex items-center gap-2 mb-8 text-sm">
          <StepDot active={step === 'configure'} done={step !== 'configure'} label="1. Configure" />
          <span className="text-muted-foreground">/</span>
          <StepDot active={step === 'preview'} done={step === 'committed'} label="2. Preview" />
          <span className="text-muted-foreground">/</span>
          <StepDot active={step === 'committed'} done={false} label="3. Commit" />
        </div>

        <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">
          <div className="lg:col-span-2 space-y-6">
            {step === 'configure' && (
              <Card>
                <CardContent className="p-6 space-y-6">
                  <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                    <div className="space-y-2">
                      <Label>From plan</Label>
                      <Select value={fromPlanId} onValueChange={setFromPlanId} disabled={plansLoading}>
                        <SelectTrigger>
                          <SelectValue placeholder="Select source plan" />
                        </SelectTrigger>
                        <SelectContent>
                          {plans.map((p) => (
                            <SelectItem key={p.id} value={p.id}>
                              {p.name} — {formatCents(p.base_amount_cents, p.currency)}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    </div>
                    <div className="space-y-2">
                      <Label>To plan</Label>
                      <Select value={toPlanId} onValueChange={setToPlanId} disabled={plansLoading}>
                        <SelectTrigger>
                          <SelectValue placeholder="Select target plan" />
                        </SelectTrigger>
                        <SelectContent>
                          {plans
                            .filter((p) => p.id !== fromPlanId)
                            .map((p) => (
                              <SelectItem key={p.id} value={p.id}>
                                {p.name} — {formatCents(p.base_amount_cents, p.currency)}
                              </SelectItem>
                            ))}
                        </SelectContent>
                      </Select>
                    </div>
                  </div>

                  {fromPlan && toPlan && fromPlan.currency !== toPlan.currency && (
                    <div className="rounded-md border border-destructive/40 bg-destructive/10 p-3 text-sm flex items-start gap-2">
                      <AlertTriangle className="h-4 w-4 text-destructive mt-0.5" />
                      <div>
                        Source ({fromPlan.currency}) and target ({toPlan.currency}) plans use different
                        currencies. Cross-currency migrations aren't supported — pick plans in the same
                        currency.
                      </div>
                    </div>
                  )}

                  <div className="space-y-2">
                    <Label>Customer filter</Label>
                    <Select value={filterType} onValueChange={(v) => setFilterType(v as 'all' | 'ids' | 'tag')}>
                      <SelectTrigger>
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="all">All customers on the source plan</SelectItem>
                        <SelectItem value="ids">Specific customer IDs</SelectItem>
                        <SelectItem value="tag" disabled>
                          By tag (coming soon)
                        </SelectItem>
                      </SelectContent>
                    </Select>
                  </div>

                  {filterType === 'ids' && (
                    <div className="space-y-2">
                      <Label>Customer IDs (one per line, or comma-separated)</Label>
                      <textarea
                        value={customerIds}
                        onChange={(e) => setCustomerIds(e.target.value)}
                        placeholder="vlx_cus_abc&#10;vlx_cus_def"
                        rows={4}
                        className="w-full rounded-md border bg-background px-3 py-2 text-sm font-mono"
                      />
                    </div>
                  )}

                  {filterType === 'tag' && (
                    <div className="space-y-2">
                      <Label>Tag value</Label>
                      <Input
                        value={tagValue}
                        onChange={(e) => setTagValue(e.target.value)}
                        placeholder="e.g. enterprise"
                      />
                    </div>
                  )}

                  <div className="space-y-2">
                    <Label>Effective</Label>
                    <Select value={effective} onValueChange={(v) => setEffective(v as 'immediate' | 'next_period')}>
                      <SelectTrigger>
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="next_period">
                          Next billing period (no proration)
                        </SelectItem>
                        <SelectItem value="immediate">
                          Immediate (prorate current period)
                        </SelectItem>
                      </SelectContent>
                    </Select>
                  </div>

                  <div className="flex items-center justify-end pt-2">
                    <Button
                      onClick={() => previewMutation.mutate()}
                      disabled={!canPreview || previewMutation.isPending}
                    >
                      {previewMutation.isPending ? (
                        <>
                          <Loader2 className="h-4 w-4 mr-2 animate-spin" />
                          Loading preview…
                        </>
                      ) : (
                        <>
                          Preview impact
                          <ArrowRight className="h-4 w-4 ml-2" />
                        </>
                      )}
                    </Button>
                  </div>
                </CardContent>
              </Card>
            )}

            {step === 'preview' && previewResult && (
              <>
                <Card>
                  <CardContent className="p-6">
                    <div className="flex items-start justify-between mb-4">
                      <div>
                        <h2 className="text-lg font-semibold flex items-center gap-2">
                          <Users className="h-4 w-4" />
                          Cohort preview
                        </h2>
                        <p className="text-sm text-muted-foreground mt-1">
                          {cohortSize} subscription{cohortSize === 1 ? '' : 's'} on{' '}
                          <span className="font-mono">{fromPlan?.name ?? fromPlanId}</span> would move
                          to <span className="font-mono">{toPlan?.name ?? toPlanId}</span>.
                        </p>
                      </div>
                      {totalDelta && (
                        <div className="text-right">
                          <div className="text-xs text-muted-foreground uppercase tracking-wide">
                            Cohort delta / period
                          </div>
                          <div
                            className={
                              'text-2xl font-semibold ' +
                              (totalDelta.delta > 0
                                ? 'text-green-600'
                                : totalDelta.delta < 0
                                ? 'text-red-600'
                                : 'text-foreground')
                            }
                          >
                            {totalDelta.delta > 0 ? '+' : ''}
                            {formatCents(totalDelta.delta, totalDelta.currency)}
                          </div>
                        </div>
                      )}
                    </div>

                    {cohortSize === 0 ? (
                      <EmptyState
                        icon={Users}
                        title="No customers match"
                        description="The selected filter resolves to zero subscriptions on the source plan."
                      />
                    ) : (
                      <Table>
                        <TableHeader>
                          <TableRow>
                            <TableHead>Customer</TableHead>
                            <TableHead>Before</TableHead>
                            <TableHead>After</TableHead>
                            <TableHead className="text-right">Delta</TableHead>
                          </TableRow>
                        </TableHeader>
                        <TableBody>
                          {previewResult.previews.map((row) => {
                            const beforeT = row.before.totals[0]
                            const afterT = row.after.totals[0]
                            return (
                              <TableRow key={row.customer_id}>
                                <TableCell className="font-mono text-xs">{row.customer_id}</TableCell>
                                <TableCell>
                                  {beforeT ? formatCents(beforeT.amount_cents, beforeT.currency) : '—'}
                                </TableCell>
                                <TableCell>
                                  {afterT ? formatCents(afterT.amount_cents, afterT.currency) : '—'}
                                </TableCell>
                                <TableCell
                                  className={
                                    'text-right font-medium ' +
                                    (row.delta_amount_cents > 0
                                      ? 'text-green-600'
                                      : row.delta_amount_cents < 0
                                      ? 'text-red-600'
                                      : '')
                                  }
                                >
                                  {row.delta_amount_cents > 0 ? '+' : ''}
                                  {formatCents(row.delta_amount_cents, row.currency || 'USD')}
                                </TableCell>
                              </TableRow>
                            )
                          })}
                        </TableBody>
                      </Table>
                    )}
                  </CardContent>
                </Card>

                {previewResult.warnings.length > 0 && (
                  <Card>
                    <CardContent className="p-4">
                      <div className="flex items-start gap-2 text-sm">
                        <AlertTriangle className="h-4 w-4 text-amber-600 mt-0.5" />
                        <div>
                          <div className="font-medium mb-1">Warnings</div>
                          <ul className="list-disc pl-5 text-muted-foreground space-y-1">
                            {previewResult.warnings.map((w, i) => (
                              <li key={i}>{w}</li>
                            ))}
                          </ul>
                        </div>
                      </div>
                    </CardContent>
                  </Card>
                )}

                <div className="flex items-center justify-end gap-2">
                  <Button variant="outline" onClick={() => setStep('configure')}>
                    Back
                  </Button>
                  <Button
                    onClick={() => setShowConfirm(true)}
                    disabled={cohortSize === 0}
                  >
                    Commit migration
                    <ArrowRight className="h-4 w-4 ml-2" />
                  </Button>
                </div>
              </>
            )}

            {step === 'committed' && committedResult && (
              <Card>
                <CardContent className="p-6">
                  <div className="flex items-start gap-3">
                    <CheckCircle2 className="h-6 w-6 text-green-600 mt-0.5" />
                    <div>
                      <h2 className="text-lg font-semibold">
                        {committedResult.idempotent_replay
                          ? 'Replayed prior migration'
                          : 'Migration committed'}
                      </h2>
                      <p className="text-sm text-muted-foreground mt-1">
                        {committedResult.applied_count} subscription item
                        {committedResult.applied_count === 1 ? '' : 's'} updated. The cohort summary
                        and per-customer changes are recorded in the audit log.
                      </p>
                      <div className="mt-4 text-xs font-mono text-muted-foreground">
                        Migration ID: {committedResult.migration_id}
                      </div>
                    </div>
                  </div>
                </CardContent>
              </Card>
            )}
          </div>

          {/* Recent migrations sidebar */}
          <div>
            <Card>
              <CardContent className="p-4">
                <h3 className="text-sm font-semibold mb-3 flex items-center gap-2">
                  <History className="h-4 w-4" />
                  Recent migrations
                </h3>
                {recent.length === 0 ? (
                  <p className="text-xs text-muted-foreground">No prior migrations.</p>
                ) : (
                  <>
                    <ul className="space-y-3">
                      {recent.map((m) => (
                        <li key={m.migration_id} className="border-b last:border-b-0 pb-3 last:pb-0">
                          <Link
                            to={`/plan-migrations/${m.migration_id}`}
                            className="block hover:bg-muted/40 -mx-2 px-2 py-1 rounded transition-colors"
                          >
                            <div className="text-xs font-mono text-muted-foreground truncate">
                              {m.from_plan_id} → {m.to_plan_id}
                            </div>
                            <div className="flex items-center gap-2 mt-1">
                              <Badge variant="secondary">{m.effective}</Badge>
                              <span className="text-xs text-muted-foreground">
                                {m.applied_count} item{m.applied_count === 1 ? '' : 's'}
                              </span>
                            </div>
                            <div className="text-xs text-muted-foreground mt-1">
                              {formatDateTime(m.applied_at)}
                            </div>
                          </Link>
                        </li>
                      ))}
                    </ul>
                    <div className="mt-3 pt-3 border-t">
                      <Link
                        to="/plan-migrations/history"
                        className="text-xs text-primary hover:underline flex items-center gap-1"
                      >
                        View all migrations
                        <ArrowRight className="h-3 w-3" />
                      </Link>
                    </div>
                  </>
                )}
              </CardContent>
            </Card>
          </div>
        </div>
      </div>

      {/* Commit confirmation modal */}
      <Dialog open={showConfirm} onOpenChange={setShowConfirm}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Commit plan migration?</DialogTitle>
            <DialogDescription>
              Move {cohortSize} subscription{cohortSize === 1 ? '' : 's'} from{' '}
              <span className="font-mono">{fromPlan?.name ?? fromPlanId}</span> to{' '}
              <span className="font-mono">{toPlan?.name ?? toPlanId}</span>{' '}
              (effective {effective === 'immediate' ? 'immediately' : 'next billing period'}).
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
              Re-firing this exact key returns the prior result without re-applying. Auto-generated;
              edit only if you're retrying a specific request.
            </p>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setShowConfirm(false)}>
              Cancel
            </Button>
            <Button
              onClick={() => commitMutation.mutate()}
              disabled={commitMutation.isPending || !idempotencyKey.trim()}
            >
              {commitMutation.isPending ? (
                <>
                  <Loader2 className="h-4 w-4 mr-2 animate-spin" />
                  Committing…
                </>
              ) : (
                'Confirm commit'
              )}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Layout>
  )
}

function StepDot({ active, done, label }: { active: boolean; done: boolean; label: string }) {
  return (
    <span
      className={
        'inline-flex items-center gap-1.5 px-2 py-1 rounded ' +
        (active
          ? 'bg-primary/10 text-primary font-medium'
          : done
          ? 'text-foreground'
          : 'text-muted-foreground')
      }
    >
      {done ? <CheckCircle2 className="h-3.5 w-3.5" /> : <span className="h-1.5 w-1.5 rounded-full bg-current" />}
      {label}
    </span>
  )
}
