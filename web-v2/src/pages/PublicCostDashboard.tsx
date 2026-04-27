import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import {
  api,
  ApiError,
  formatCents,
  formatDate,
  type CustomerUsageMeter,
  type CustomerUsageRule,
  type PublicCostDashboard,
} from '@/lib/api'
import { cn } from '@/lib/utils'
import { Card, CardContent } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import {
  Activity,
  AlertTriangle,
  ChevronDown,
  ChevronRight,
  Clock,
  Loader2,
} from 'lucide-react'

// PublicCostDashboardPage renders the customer-facing cost dashboard at
// /public/cost-dashboard/:token. It mirrors what CostDashboard
// (internal authed) shows, but takes its data from the unauthenticated
// /v1/public/cost-dashboard/{token} route — token IS the auth, so no
// cookies, no Authorization header.
//
// Decision: thin presentation-only fork of CostDashboard rather than
// refactor the existing component. The authed one is coupled to
// react-query + api.customerUsage + a customerId-scoped query key;
// untangling that would touch the working operator surface for a
// surface that needs a different cache key (the token itself) and a
// minimal layout (no PublicLayout chrome — the page is built to live
// inside an iframe). Embed surfaces should be lightweight; running the
// marketing nav + footer inside an iframe is wrong.

export default function PublicCostDashboardPage() {
  const { token = '' } = useParams<{ token: string }>()

  const { data, isLoading, error } = useQuery({
    queryKey: ['public-cost-dashboard', token],
    queryFn: () => api.getPublicCostDashboard(token),
    enabled: !!token,
    // No periodic refetch — the embed is a snapshot, not a live console;
    // operators who want live data refresh the iframe on their side. We
    // still refetch on focus so a customer flipping back to the tab
    // after a payment finalization sees the latest cycle state.
    refetchOnWindowFocus: true,
    retry: (failureCount, err) => {
      // 404 / 410 mean the token is gone for good — never retry. Other
      // errors get the default single retry from the query client.
      if (err instanceof ApiError && (err.status === 404 || err.status === 410)) {
        return false
      }
      return failureCount < 1
    },
  })

  // Set the document title for the iframe's accessibility name. Falls
  // back to a generic label until the payload arrives.
  useEffect(() => {
    document.title = 'Cost dashboard'
  }, [])

  if (!token) {
    return <DashboardError message="This dashboard link is no longer valid." />
  }

  if (isLoading) {
    return (
      <DashboardShell>
        <div className="flex flex-col items-center gap-3 py-16 text-muted-foreground">
          <Loader2 className="h-6 w-6 animate-spin" aria-hidden="true" />
          <p className="text-sm">Loading dashboard…</p>
        </div>
      </DashboardShell>
    )
  }

  if (error || !data) {
    // 404 / 410 → token rotated or never existed. Show a clean,
    // user-actionable message; never leak the server's error text since
    // an embedded iframe is the worst place for a stack trace.
    return <DashboardError message="This dashboard link is no longer valid." />
  }

  return (
    <DashboardShell>
      <DashboardBody data={data} />
    </DashboardShell>
  )
}

function DashboardShell({ children }: { children: ReactNode }) {
  return (
    <div className="min-h-screen bg-background text-foreground">
      <div className="mx-auto w-full max-w-3xl p-4 sm:p-6">
        {children}
        <p className="text-xs text-muted-foreground/70 text-center mt-6">
          Powered by Velox Billing
        </p>
      </div>
    </div>
  )
}

function DashboardError({ message }: { message: string }) {
  return (
    <DashboardShell>
      <Card className="mt-12">
        <CardContent className="p-8 text-center space-y-3">
          <div className="w-12 h-12 rounded-full bg-muted flex items-center justify-center mx-auto">
            <Clock size={22} className="text-muted-foreground" aria-hidden="true" />
          </div>
          <h1 className="text-base font-semibold text-foreground">Dashboard unavailable</h1>
          <p className="text-sm text-muted-foreground">{message}</p>
          <p className="text-xs text-muted-foreground">
            Please contact your billing provider for a new link.
          </p>
        </CardContent>
      </Card>
    </DashboardShell>
  )
}

function DashboardBody({ data }: { data: PublicCostDashboard }) {
  const { usage, billing_period, currency, projected_total_amount_cents, thresholds } = data
  const hasUsage =
    usage.meters.length > 0 || usage.totals.length > 0 || usage.subscriptions.length > 0

  return (
    <div className="space-y-4">
      <header>
        <h1 className="text-base font-semibold text-foreground">Usage this period</h1>
        {billing_period?.from && billing_period?.to && (
          <p className="text-xs text-muted-foreground mt-0.5">
            {formatDate(billing_period.from)} → {formatDate(billing_period.to)}
            {billing_period.source === 'current_billing_cycle' ? ' · current billing cycle' : ''}
          </p>
        )}
      </header>

      {/* Subscription / cycle context */}
      {usage.subscriptions.length > 0 && (
        <Card>
          <CardContent className="p-4">
            <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
              {usage.subscriptions.map(sub => (
                <CycleHeader key={sub.id} sub={sub} />
              ))}
            </div>
          </CardContent>
        </Card>
      )}

      {/* Cycle-to-date totals */}
      <Card>
        <CardContent className="px-6 py-5">
          <p className="text-xs uppercase tracking-wide text-muted-foreground">
            Cycle-to-date charges
          </p>
          <div className="mt-2 flex flex-wrap items-baseline gap-x-6 gap-y-2">
            {usage.totals.length === 0 ? (
              <p className="text-3xl font-semibold text-foreground tabular-nums">
                {formatCents(0, currency)}
              </p>
            ) : (
              usage.totals.map(t => (
                <div key={t.currency} className="flex items-baseline gap-2">
                  <span className="text-3xl font-semibold text-foreground tabular-nums">
                    {formatCents(t.amount_cents, t.currency)}
                  </span>
                  {usage.totals.length > 1 && (
                    <span className="text-xs text-muted-foreground uppercase">{t.currency}</span>
                  )}
                </div>
              ))
            )}
          </div>
          {/* Projected total — optional, mirrors the value the operator
              dashboard surfaces from POST /v1/invoices/create_preview. */}
          {typeof projected_total_amount_cents === 'number' && (
            <p className="text-xs text-muted-foreground mt-3">
              Projected total this period:{' '}
              <span className="text-foreground font-medium tabular-nums">
                {formatCents(projected_total_amount_cents, currency)}
              </span>
            </p>
          )}
        </CardContent>
      </Card>

      {/* Threshold alerts (always-array, render only when populated). */}
      {thresholds.length > 0 && (
        <Card>
          <CardContent className="p-4 space-y-2">
            <p className="text-xs uppercase tracking-wide text-muted-foreground">Spend alerts</p>
            <ul className="space-y-1.5">
              {thresholds.map((t, i) => (
                <li
                  key={i}
                  className={cn(
                    'text-xs flex items-center gap-2',
                    t.status === 'triggered_for_period' || t.status === 'triggered'
                      ? 'text-amber-700 dark:text-amber-300'
                      : 'text-muted-foreground',
                  )}
                >
                  {(t.status === 'triggered_for_period' || t.status === 'triggered') && (
                    <AlertTriangle size={12} aria-hidden="true" />
                  )}
                  <span className="font-medium text-foreground">{t.title}</span>
                  <span>
                    —{' '}
                    {t.amount_gte != null
                      ? `at ${formatCents(t.amount_gte, currency)}`
                      : t.usage_gte != null
                      ? `at ${t.usage_gte} units`
                      : ''}
                  </span>
                </li>
              ))}
            </ul>
          </CardContent>
        </Card>
      )}

      {/* Warnings — surfaced verbatim from server, same convention as
          the operator CostDashboard. */}
      {usage.warnings.length > 0 && (
        <Card className="border-amber-200 dark:border-amber-900">
          <CardContent className="p-4">
            <div className="flex items-center gap-2 text-amber-700 dark:text-amber-300 text-xs font-medium mb-2">
              <AlertTriangle size={12} />
              {usage.warnings.length} warning{usage.warnings.length !== 1 ? 's' : ''}
            </div>
            <ul className="text-xs text-amber-700 dark:text-amber-300 space-y-1">
              {usage.warnings.map((w, i) => (
                <li key={i}>{w}</li>
              ))}
            </ul>
          </CardContent>
        </Card>
      )}

      {/* Per-meter cards */}
      {!hasUsage ? (
        <Card>
          <CardContent className="p-8 text-center">
            <Activity size={20} className="mx-auto text-muted-foreground mb-3" />
            <p className="text-sm text-muted-foreground">
              No usage to show for this period.
            </p>
          </CardContent>
        </Card>
      ) : usage.meters.length === 0 ? (
        <Card>
          <CardContent className="p-8 text-center">
            <p className="text-sm text-muted-foreground">No metered usage in this period.</p>
          </CardContent>
        </Card>
      ) : (
        <div className="space-y-2">
          {usage.meters.map(m => (
            <MeterRow key={m.meter_id} meter={m} />
          ))}
        </div>
      )}
    </div>
  )
}

function CycleHeader({
  sub,
}: {
  sub: { plan_name: string; current_period_start: string; current_period_end: string }
}) {
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
            expanded ? (
              <ChevronDown size={14} className="text-muted-foreground shrink-0" />
            ) : (
              <ChevronRight size={14} className="text-muted-foreground shrink-0" />
            )
          ) : (
            <span className="w-3.5" />
          )}
          <div className="flex-1 min-w-0">
            <p className="text-sm font-medium text-foreground truncate">{meter.meter_name}</p>
            <p className="text-xs text-muted-foreground font-mono">
              {totalQty.toLocaleString(undefined, { maximumFractionDigits: 6 })} {meter.unit}
              {hasMultipleRules && (
                <span className="ml-2 text-muted-foreground">
                  · {meter.rules.length} rules
                </span>
              )}
            </p>
          </div>
          <p className="text-sm font-semibold text-foreground tabular-nums shrink-0">
            {formatCents(meter.total_amount_cents, meter.currency)}
          </p>
        </button>

        {expanded && hasMultipleRules && (
          <div className="border-t border-border bg-muted/20">
            {meter.rules.map(r => (
              <RuleRow
                key={r.rating_rule_version_id}
                rule={r}
                unit={meter.unit}
                currency={meter.currency}
              />
            ))}
          </div>
        )}
      </CardContent>
    </Card>
  )
}

function RuleRow({
  rule,
  unit,
  currency,
}: {
  rule: CustomerUsageRule
  unit: string
  currency: string
}) {
  const qty = Number(rule.quantity)
  return (
    <div className="px-5 py-2.5 flex items-center gap-4 text-xs border-b border-border last:border-b-0">
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2 flex-wrap">
          <Badge variant="secondary" className="font-mono">
            {rule.rule_key}
          </Badge>
          {rule.dimension_match &&
            Object.entries(rule.dimension_match).map(([k, v]) => (
              <span
                key={k}
                className="inline-flex items-center font-mono bg-muted px-1.5 py-0.5 rounded text-foreground/80"
              >
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
