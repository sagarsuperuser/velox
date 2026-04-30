import { useState, useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api, formatCents, formatDate } from '@/lib/api'
import type { CustomerUsageMeter, CustomerUsageRule } from '@/lib/api'
import { cn } from '@/lib/utils'

import { Card, CardContent } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { ChevronDown, ChevronRight, Activity, AlertTriangle, Loader2 } from 'lucide-react'

type PeriodPreset = 'current' | 'last_30d' | 'last_90d'

const PRESETS: { value: PeriodPreset; label: string }[] = [
  { value: 'current', label: 'Current cycle' },
  { value: 'last_30d', label: 'Last 30 days' },
  { value: 'last_90d', label: 'Last 90 days' },
]

function presetToParams(p: PeriodPreset): { from?: string; to?: string } | undefined {
  if (p === 'current') return undefined
  const now = new Date()
  const days = p === 'last_30d' ? 30 : 90
  const from = new Date(now.getTime() - days * 24 * 60 * 60 * 1000).toISOString()
  return { from, to: now.toISOString() }
}

export function CostDashboard({ customerId }: { customerId: string }) {
  const [preset, setPreset] = useState<PeriodPreset>('current')
  // Multi-sub customers get one cycle card per subscription, but most
  // operators only need the primary (the sub whose cycle drives the
  // displayed period — server-side: latest current_period_start).
  // Default collapsed, expand on click. Only matters for customers with
  // 2+ subs; single-sub customers don't see a "show more" affordance.
  const [subsExpanded, setSubsExpanded] = useState(false)

  const { data, isLoading, error, refetch } = useQuery({
    queryKey: ['customer-usage', customerId, preset],
    queryFn: async () => {
      try {
        return await api.customerUsage(customerId, presetToParams(preset))
      } catch (err) {
        // The backend's "no current billing cycle" path returns 422
        // with code=customer_has_no_subscription (errs.Invalid →
        // ErrValidation → 422 in respond.FromError). Surface it as an
        // empty result so the dashboard renders the empty-state card
        // instead of a red error toast — the customer just doesn't
        // have a subscription with a current cycle yet, which is a
        // valid state, not a failure. 404 (customer not found) gets
        // the same treatment because the parent page already 404s in
        // that case. Real network / auth / unexpected-422 errors with
        // other codes still fall through to the error UI.
        if (err && typeof err === 'object' && 'status' in err && 'code' in err) {
          const s = (err as { status: number }).status
          const c = (err as { code?: string }).code
          if (s === 404) return null
          if (s === 422 && c === 'customer_has_no_subscription') return null
        }
        throw err
      }
    },
  })

  // Projected bill — Stripe Tier 1 create_preview powers a "what will the
  // current cycle invoice look like if we finalized it now?" row. Only
  // meaningful when preset === 'current' (the engine is cycle-aware; an
  // explicit 30/90-day window doesn't have an in-progress cycle to
  // project). Failures are silent — preview is auxiliary signal, the
  // primary usage view shouldn't error because the engine warned.
  const { data: projected } = useQuery({
    queryKey: ['customer-create-preview', customerId, preset],
    enabled: preset === 'current',
    queryFn: async () => {
      try {
        return await api.createInvoicePreview({ customer_id: customerId })
      } catch {
        return null
      }
    },
  })

  // Cross-window hint — when the operator is on Current cycle, fetch the
  // last-30-days total in parallel so we can surface "1,400 events in the
  // last 30 days" alongside the cycle-to-date number. This is what closes
  // the cycle-rollover gap: a sub that just rolled over shows $0.00
  // cycle-to-date but the operator's mental model expects to see usage —
  // the hint redirects them to the broader window where the closed
  // cycle's events still show.
  const { data: thirtyDay } = useQuery({
    queryKey: ['customer-usage', customerId, 'last_30d_compare'],
    enabled: preset === 'current',
    queryFn: async () => {
      try {
        return await api.customerUsage(customerId, presetToParams('last_30d'))
      } catch {
        return null
      }
    },
  })

  if (error) {
    return (
      <Card>
        <CardContent className="p-6 text-center">
          <p className="text-sm text-destructive mb-3">{(error as Error).message}</p>
          <Button variant="outline" size="sm" onClick={() => refetch()}>Retry</Button>
        </CardContent>
      </Card>
    )
  }

  return (
    <div className="space-y-4">
      {/* Header + period switcher */}
      <div className="flex items-center justify-between gap-4">
        <div>
          <h2 className="text-base font-semibold text-foreground">Usage this period</h2>
          {data?.period && (
            <p className="text-xs text-muted-foreground mt-0.5">
              {formatDate(data.period.from)} → {formatDate(data.period.to)} · {data.period.source === 'current_billing_cycle' ? 'current billing cycle' : 'explicit window'}
            </p>
          )}
        </div>
        <Tabs value={preset} onValueChange={(v) => setPreset(v as PeriodPreset)}>
          <TabsList>
            {PRESETS.map(p => (
              <TabsTrigger key={p.value} value={p.value} className="text-xs">{p.label}</TabsTrigger>
            ))}
          </TabsList>
        </Tabs>
      </div>

      {isLoading ? (
        <Card><CardContent className="p-8 flex justify-center"><Loader2 size={20} className="animate-spin text-muted-foreground" /></CardContent></Card>
      ) : !data ? (
        <Card>
          <CardContent className="p-8 text-center">
            <Activity size={20} className="mx-auto text-muted-foreground mb-3" />
            <p className="text-sm text-muted-foreground">
              No usage to show for this period. {preset === 'current'
                ? "Customer doesn't have an active subscription, or the cycle hasn't started yet."
                : 'Try a different window.'}
            </p>
          </CardContent>
        </Card>
      ) : (
        <>
          {/* Subscription / cycle context. Multi-sub customers default to
              showing the primary (subs[0], the one driving the displayed
              period) and collapse the rest behind a "+N more" toggle. */}
          {data.subscriptions.length > 0 && (
            <Card>
              <CardContent className="p-4">
                <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
                  {(subsExpanded ? data.subscriptions : data.subscriptions.slice(0, 1)).map(sub => (
                    <CycleHeader key={sub.id} sub={sub} />
                  ))}
                </div>
                {data.subscriptions.length > 1 && (
                  <button
                    type="button"
                    onClick={() => setSubsExpanded(!subsExpanded)}
                    className="text-xs text-muted-foreground hover:text-foreground mt-3 inline-flex items-center gap-1"
                  >
                    {subsExpanded ? (
                      <>Hide other subscriptions</>
                    ) : (
                      <>+ {data.subscriptions.length - 1} more subscription{data.subscriptions.length - 1 === 1 ? '' : 's'}</>
                    )}
                  </button>
                )}
              </CardContent>
            </Card>
          )}

          {/* Big number — totals (one per currency) */}
          <Card>
            <CardContent className="px-6 py-5">
              <p className="text-xs uppercase tracking-wide text-muted-foreground">Cycle-to-date charges</p>
              <div className="mt-2 flex flex-wrap items-baseline gap-x-6 gap-y-2">
                {data.totals.length === 0 ? (
                  <p className="text-3xl font-semibold text-foreground tabular-nums">{formatCents(0)}</p>
                ) : (
                  data.totals.map(t => (
                    <div key={t.currency} className="flex items-baseline gap-2">
                      <span className="text-3xl font-semibold text-foreground tabular-nums">{formatCents(t.amount_cents, t.currency)}</span>
                      {data.totals.length > 1 && <span className="text-xs text-muted-foreground uppercase">{t.currency}</span>}
                    </div>
                  ))
                )}
              </div>
              {/* Projected total — surfaced from /v1/invoices/create_preview.
                  Render only if the engine returned a non-empty totals array
                  for the current cycle; absent for explicit windows or when
                  the engine warned (we silently swallowed the error above). */}
              {preset === 'current' && projected && projected.totals.length > 0 && (
                <p className="text-xs text-muted-foreground mt-3">
                  Projected total this period:{' '}
                  {projected.totals.map((t, i) => (
                    <span key={t.currency} className="text-foreground font-medium tabular-nums">
                      {i > 0 && ' · '}
                      {formatCents(t.amount_cents, t.currency)}
                      {projected.totals.length > 1 && <span className="ml-1 text-muted-foreground uppercase">{t.currency}</span>}
                    </span>
                  ))}
                </p>
              )}

              {/* Cross-window hint — shown only on the Current cycle tab
                  when the broader 30-day window has more activity than the
                  current cycle. Closes the cycle-rollover gap: a sub that
                  rolled over an hour ago shows $0.00 here but the operator
                  expects to see the customer's recent usage. The link
                  redirects to the Last 30 days tab without a page reload.
                  Threshold is "30d total > current cycle total" (any gap)
                  rather than a percentage — the operator who's looking
                  for "where did my events go?" is helped by ANY non-zero
                  gap, not just large ones. */}
              {preset === 'current' && thirtyDay && (() => {
                const currentCents = data.totals.reduce((sum, t) => sum + t.amount_cents, 0)
                const thirtyCents = thirtyDay.totals.reduce((sum, t) => sum + t.amount_cents, 0)
                const thirtyEvents = thirtyDay.meters.reduce((sum, m) => sum + Number(m.total_quantity), 0)
                if (thirtyCents <= currentCents || thirtyEvents <= 0) return null
                return (
                  <p className="text-xs text-muted-foreground mt-3 pt-3 border-t border-border">
                    Last 30 days:{' '}
                    <button
                      type="button"
                      onClick={() => setPreset('last_30d')}
                      className="text-foreground font-medium tabular-nums underline-offset-2 hover:underline"
                    >
                      {formatCents(thirtyCents)} ({thirtyEvents.toLocaleString()} event{thirtyEvents === 1 ? '' : 's'})
                    </button>
                    {currentCents === 0 && ' — cycle just started? Switch to view recent activity.'}
                  </p>
                )
              })()}
            </CardContent>
          </Card>

          {/* Warnings */}
          {data.warnings.length > 0 && (
            <Card className="border-amber-200 dark:border-amber-900">
              <CardContent className="p-4">
                <div className="flex items-center gap-2 text-amber-700 dark:text-amber-300 text-xs font-medium mb-2">
                  <AlertTriangle size={12} />
                  {data.warnings.length} warning{data.warnings.length !== 1 ? 's' : ''}
                </div>
                <ul className="text-xs text-amber-700 dark:text-amber-300 space-y-1">
                  {data.warnings.map((w, i) => (
                    <li key={i}>{w}</li>
                  ))}
                </ul>
              </CardContent>
            </Card>
          )}

          {/* Per-meter cards */}
          {data.meters.length === 0 ? (
            <Card>
              <CardContent className="p-8 text-center">
                <p className="text-sm text-muted-foreground">No metered usage in this period.</p>
              </CardContent>
            </Card>
          ) : (
            <div className="space-y-2">
              {data.meters.map(m => <MeterRow key={m.meter_id} meter={m} />)}
            </div>
          )}
        </>
      )}
    </div>
  )
}

function CycleHeader({ sub }: { sub: { plan_name: string; current_period_start: string; current_period_end: string } }) {
  const { plan_name, current_period_start, current_period_end } = sub
  const start = new Date(current_period_start).getTime()
  const end = new Date(current_period_end).getTime()
  const now = Date.now()
  const totalDays = Math.max(1, Math.round((end - start) / 86_400_000))
  const daysIn = Math.max(0, Math.min(totalDays, Math.round((now - start) / 86_400_000)))
  const pct = totalDays > 0 ? Math.min(100, (daysIn / totalDays) * 100) : 0
  return (
    <div className="flex items-center gap-3 min-w-0">
      <div className="min-w-0">
        <p className="text-sm font-medium text-foreground truncate">{plan_name}</p>
        <p className="text-xs text-muted-foreground tabular-nums">
          Day {daysIn} of {totalDays} · {pct.toFixed(0)}% through
        </p>
      </div>
      <div className="w-24 h-1.5 rounded-full bg-muted overflow-hidden">
        <div className="h-full bg-primary rounded-full" style={{ width: `${pct}%` }} />
      </div>
    </div>
  )
}

function MeterRow({ meter }: { meter: CustomerUsageMeter }) {
  const [expanded, setExpanded] = useState(false)
  const hasMultipleRules = meter.rules.length > 1
  const totalQty = useMemo(() => Number(meter.total_quantity), [meter.total_quantity])

  return (
    <Card>
      <CardContent className="p-0">
        <button
          type="button"
          className={cn(
            'w-full px-5 py-4 flex items-center gap-4 text-left transition-colors',
            hasMultipleRules ? 'cursor-pointer hover:bg-muted/30' : 'cursor-default',
          )}
          onClick={() => hasMultipleRules && setExpanded(e => !e)}
          disabled={!hasMultipleRules}
        >
          {hasMultipleRules ? (
            expanded ? <ChevronDown size={14} className="text-muted-foreground shrink-0" /> : <ChevronRight size={14} className="text-muted-foreground shrink-0" />
          ) : <span className="w-3.5" />}
          <div className="flex-1 min-w-0">
            <p className="text-sm font-medium text-foreground truncate">{meter.meter_name}</p>
            <p className="text-xs text-muted-foreground font-mono">
              {totalQty.toLocaleString(undefined, { maximumFractionDigits: 6 })} {meter.unit}
              {hasMultipleRules && <span className="ml-2 text-muted-foreground">· {meter.rules.length} rules</span>}
            </p>
          </div>
          <p className="text-sm font-semibold text-foreground tabular-nums shrink-0">
            {formatCents(meter.total_amount_cents, meter.currency)}
          </p>
        </button>

        {expanded && hasMultipleRules && (
          <div className="border-t border-border bg-muted/20">
            {meter.rules.map(r => <RuleRow key={r.rating_rule_version_id} rule={r} unit={meter.unit} currency={meter.currency} />)}
          </div>
        )}
      </CardContent>
    </Card>
  )
}

function RuleRow({ rule, unit, currency }: { rule: CustomerUsageRule; unit: string; currency: string }) {
  const qty = Number(rule.quantity)
  return (
    <div className="px-5 py-2.5 flex items-center gap-4 text-xs border-b border-border last:border-b-0">
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2 flex-wrap">
          <Badge variant="secondary" className="font-mono">{rule.rule_key}</Badge>
          {rule.dimension_match && Object.entries(rule.dimension_match).map(([k, v]) => (
            <span key={k} className="inline-flex items-center font-mono bg-muted px-1.5 py-0.5 rounded text-foreground/80">
              {k}=<span className="text-foreground">{String(v)}</span>
            </span>
          ))}
        </div>
        <p className="text-muted-foreground font-mono mt-1">
          {qty.toLocaleString(undefined, { maximumFractionDigits: 6 })} {unit}
        </p>
      </div>
      <p className="font-semibold text-foreground tabular-nums shrink-0">
        {formatCents(rule.amount_cents, currency)}
      </p>
    </div>
  )
}
