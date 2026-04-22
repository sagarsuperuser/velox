import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api, formatCents, formatRelativeTime } from '@/lib/api'
import type { Invoice } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { cn } from '@/lib/utils'
import { Card, CardContent } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { AlertTriangle, Check, ChevronDown, ChevronUp, ArrowRight, X } from 'lucide-react'
import {
  Area, AreaChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis,
} from 'recharts'
import { statusBadgeVariant } from '@/lib/status'
import { TrendCard } from '@/components/analytics/TrendCard'
import { TrendCardSkeleton, ChartCardSkeleton } from '@/components/analytics/CardSkeleton'
import { PeriodTabs } from '@/components/analytics/PeriodTabs'
import { PERIOD_LABELS, type Period } from '@/components/analytics/period'
import { tooltipStyle, useChartTheme } from '@/lib/chartTheme'

function formatCompactAmount(cents: number): string {
  const abs = Math.abs(cents)
  if (abs >= 100_000_00) return `$${Math.round(cents / 100_00)}K`
  if (abs >= 1_000_00) return `$${(cents / 100_00).toFixed(1)}K`
  return `$${Math.round(cents / 100)}`
}

function formatShortDate(dateStr: string): string {
  const d = new Date(dateStr)
  if (Number.isNaN(d.getTime())) return dateStr
  return d.toLocaleDateString('en-US', { month: 'short', day: 'numeric' })
}

export default function DashboardPage() {
  const [period, setPeriod] = useState<Period>('30d')
  const [getStartedOpen, setGetStartedOpen] = useState(true)
  const [getStartedDismissed, setGetStartedDismissed] = useState(false)
  const theme = useChartTheme()

  const { data: overview, isLoading: overviewLoading, error: errorObj, refetch, dataUpdatedAt } = useQuery({
    queryKey: ['dashboard-overview', period],
    queryFn: () => api.getAnalyticsOverview(period),
  })
  const { data: chartRes, isLoading: chartLoading } = useQuery({
    queryKey: ['dashboard-chart', period],
    queryFn: () => api.getRevenueChart(period),
  })
  const { data: recentInvoices } = useQuery({
    queryKey: ['dashboard-recent-invoices'],
    queryFn: () => api.listInvoices('limit=5'),
  })
  // Pricing-configured detection for the Get Started checklist. A plan
  // requires meters + rating rules upstream, so presence of any plan covers
  // step 1. Previous logic keyed off active_subscriptions/revenue which
  // stayed false even after pricing was fully set up.
  const { data: plansList } = useQuery({
    queryKey: ['dashboard-plans'],
    queryFn: () => api.listPlans(),
  })

  const chartData = useMemo(() => chartRes?.data ?? [], [chartRes])
  const error = errorObj instanceof Error ? errorObj.message : errorObj ? String(errorObj) : null

  const sparklineRevenue = useMemo(
    () => chartData.map(d => d.revenue_cents).slice(-12),
    [chartData],
  )

  // Attention items surface things the operator should act on. Empty list = quiet state.
  const alerts = useMemo(() => {
    if (!overview) return []
    const items: { severity: 'warning' | 'danger'; text: string; to?: string }[] = []
    if (overview.failed_payments > 0) {
      items.push({
        severity: 'danger',
        text: `${overview.failed_payments} failed ${overview.failed_payments === 1 ? 'payment' : 'payments'} in the last ${PERIOD_LABELS[period].toLowerCase()}`,
        to: '/invoices?payment_status=failed',
      })
    }
    if (overview.dunning_active > 0) {
      items.push({
        severity: 'warning',
        text: `${overview.dunning_active} active dunning ${overview.dunning_active === 1 ? 'run' : 'runs'}`,
        to: '/dunning',
      })
    }
    if (overview.open_invoices > 0 && overview.outstanding_ar > 0) {
      items.push({
        severity: 'warning',
        text: `${overview.open_invoices} open ${overview.open_invoices === 1 ? 'invoice' : 'invoices'} · ${formatCents(overview.outstanding_ar)} outstanding`,
        to: '/invoices?status=finalized',
      })
    }
    return items
  }, [overview, period])

  return (
    <Layout>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold text-foreground">Dashboard</h1>
          {dataUpdatedAt > 0 && (
            <p className="text-xs text-muted-foreground mt-1" aria-live="polite">
              Updated {formatRelativeTime(new Date(dataUpdatedAt).toISOString())}
            </p>
          )}
        </div>
        <div className="flex items-center gap-2">
          <PeriodTabs value={period} onChange={setPeriod} />
        </div>
      </div>

      {error ? (
        <Card className="mt-6">
          <CardContent className="p-8 text-center">
            <p className="text-sm text-destructive mb-3">{error}</p>
            <Button variant="outline" size="sm" onClick={() => refetch()}>Retry</Button>
          </CardContent>
        </Card>
      ) : overviewLoading || !overview ? (
        <div className="grid grid-cols-2 lg:grid-cols-4 gap-4 mt-6">
          {[0, 1, 2, 3].map(i => <TrendCardSkeleton key={i} />)}
        </div>
      ) : (
        <>
          {/* Get Started — promoted to top so new tenants see the #1 CTA
              immediately, not buried under empty KPIs. Self-hides when the
              checklist is complete or the user dismisses. */}
          {!getStartedDismissed && (
            <GetStarted
              overview={overview}
              hasPlans={(plansList?.data?.length ?? 0) > 0}
              open={getStartedOpen}
              onToggle={() => setGetStartedOpen(o => !o)}
              onDismiss={() => setGetStartedDismissed(true)}
            />
          )}

          {/* Alerts row */}
          {alerts.length > 0 && (
            <div className="mt-6 space-y-2" role="region" aria-label="Attention required">
              {alerts.map((a, i) => (
                <AlertRow key={i} severity={a.severity} text={a.text} to={a.to} />
              ))}
            </div>
          )}

          {/* KPI row */}
          <div className="grid grid-cols-2 lg:grid-cols-4 gap-4 mt-6">
            <TrendCard
              label="MRR"
              value={formatCents(overview.mrr)}
              current={overview.mrr}
              previous={overview.mrr_prev}
              sparkline={sparklineRevenue}
              hint={`ARR ${formatCents(overview.arr)}`}
            />
            <TrendCard
              label="Revenue"
              value={formatCents(overview.revenue)}
              current={overview.revenue}
              previous={overview.revenue_prev}
              hint={`${overview.paid_invoices} ${overview.paid_invoices === 1 ? 'invoice' : 'invoices'} paid`}
            />
            <TrendCard
              label="Active Customers"
              value={overview.active_customers}
              current={overview.active_customers}
              previous={overview.active_customers - overview.new_customers}
              hint={
                <>
                  +{overview.new_customers} new · {overview.active_subscriptions} subs
                  {overview.trialing_subscriptions > 0 && ` · ${overview.trialing_subscriptions} trialing`}
                </>
              }
            />
            <TrendCard
              label="Failed Payments"
              value={overview.failed_payments}
              current={overview.failed_payments}
              previous={0}
              inverseDelta
              valueTone={overview.failed_payments > 0 ? 'danger' : 'default'}
              hint={`${PERIOD_LABELS[period]}`}
              sparklineTone="danger"
            />
          </div>

          {/* Revenue chart */}
          {chartLoading ? (
            <div className="mt-6"><ChartCardSkeleton height={220} /></div>
          ) : (
            <Card className="mt-6">
              <CardContent className="p-5">
                <div className="flex items-center justify-between mb-4">
                  <div>
                    <p className="text-sm font-medium text-foreground">Revenue</p>
                    <p className="text-xs text-muted-foreground mt-0.5">Last {PERIOD_LABELS[period].toLowerCase()}</p>
                  </div>
                  <Link to="/analytics" className="text-xs text-muted-foreground hover:text-foreground transition-colors">
                    Open analytics →
                  </Link>
                </div>
                {chartData.length > 0 ? (
                  <div role="img" aria-label={`Revenue trend over the last ${PERIOD_LABELS[period].toLowerCase()}`}>
                  <ResponsiveContainer width="100%" height={220}>
                    <AreaChart data={chartData} margin={{ top: 5, right: 5, left: 5, bottom: 5 }}>
                      <defs>
                        <linearGradient id="dashRev" x1="0" y1="0" x2="0" y2="1">
                          <stop offset="0%" stopColor={theme.primary} stopOpacity={0.18} />
                          <stop offset="100%" stopColor={theme.primary} stopOpacity={0} />
                        </linearGradient>
                      </defs>
                      <CartesianGrid strokeDasharray="3 3" vertical={false} stroke={theme.grid} />
                      <XAxis
                        dataKey="date"
                        tickFormatter={formatShortDate}
                        tick={{ fontSize: 11, fill: theme.tick }}
                        axisLine={false}
                        tickLine={false}
                      />
                      <YAxis
                        tickFormatter={formatCompactAmount}
                        tick={{ fontSize: 11, fill: theme.tick }}
                        axisLine={false}
                        tickLine={false}
                        width={55}
                      />
                      <Tooltip
                        formatter={(value) => [formatCents(Number(value)), 'Revenue']}
                        labelFormatter={(label) => formatShortDate(String(label))}
                        contentStyle={tooltipStyle(theme)}
                        itemStyle={{ color: theme.tooltipText }}
                        cursor={{ fill: theme.grid }}
                      />
                      <Area
                        type="monotone"
                        dataKey="revenue_cents"
                        stroke={theme.primary}
                        strokeWidth={2}
                        fill="url(#dashRev)"
                        dot={false}
                        activeDot={{ r: 4, fill: theme.tooltipBg, stroke: theme.primary, strokeWidth: 2 }}
                      />
                    </AreaChart>
                  </ResponsiveContainer>
                  </div>
                ) : (
                  <div className="h-[220px] flex items-center justify-center text-sm text-muted-foreground">
                    Revenue data will appear after your first billing cycle.
                  </div>
                )}
              </CardContent>
            </Card>
          )}

          {/* Recent activity */}
          <Card className="mt-6">
            <CardContent className="p-0">
              <div className="flex items-center justify-between px-5 py-3 border-b border-border">
                <p className="text-sm font-medium text-foreground">Recent Invoices</p>
                <Link to="/invoices" className="text-xs text-muted-foreground hover:text-foreground transition-colors">
                  View all →
                </Link>
              </div>
              {recentInvoices?.data && recentInvoices.data.length > 0 ? (
                <div className="divide-y divide-border">
                  {recentInvoices.data.slice(0, 5).map((inv: Invoice) => (
                    <Link
                      key={inv.id}
                      to={`/invoices/${inv.id}`}
                      className="flex items-center justify-between px-5 py-3 hover:bg-muted/50 transition-colors"
                    >
                      <div className="flex items-center gap-3 min-w-0">
                        <PaymentStatusDot status={inv.payment_status} />
                        <span className="text-sm font-mono text-foreground">{inv.invoice_number}</span>
                        <Badge variant={statusBadgeVariant(inv.payment_status)} className="text-[10px]">
                          {inv.payment_status}
                        </Badge>
                      </div>
                      <div className="flex items-center gap-4 shrink-0">
                        <span className="text-sm tabular-nums text-foreground">
                          {formatCents(inv.amount_due_cents, inv.currency)}
                        </span>
                        <span className="text-xs text-muted-foreground w-16 text-right">
                          {formatRelativeTime(inv.created_at)}
                        </span>
                      </div>
                    </Link>
                  ))}
                </div>
              ) : (
                <div className="px-5 py-8 text-center text-sm text-muted-foreground">
                  No invoices yet. Trigger a billing run from Settings → Operations.
                </div>
              )}
            </CardContent>
          </Card>

        </>
      )}
    </Layout>
  )
}

function AlertRow({ severity, text, to }: { severity: 'warning' | 'danger'; text: string; to?: string }) {
  const bg = severity === 'danger' ? 'bg-destructive/5 border-destructive/20' : 'bg-amber-500/5 border-amber-500/20'
  const iconColor = severity === 'danger' ? 'text-destructive' : 'text-amber-600 dark:text-amber-400'
  const content = (
    <div className={cn('flex items-center gap-2 px-4 py-2 rounded-md border text-sm', bg)}>
      <AlertTriangle size={14} className={iconColor} aria-hidden />
      <span className="text-foreground flex-1">{text}</span>
      {to && <span className="text-xs text-muted-foreground">Open →</span>}
    </div>
  )
  return to ? <Link to={to}>{content}</Link> : content
}

function PaymentStatusDot({ status }: { status: string }) {
  const color =
    status === 'succeeded' ? 'bg-emerald-500'
    : status === 'failed' ? 'bg-destructive'
    : status === 'processing' ? 'bg-blue-500'
    : 'bg-amber-500'
  return (
    <span className={cn('w-2 h-2 rounded-full shrink-0', color)} aria-label={`Payment ${status}`} title={status} />
  )
}

interface GetStartedProps {
  overview: {
    active_subscriptions: number
    active_customers: number
    revenue: number
  }
  hasPlans: boolean
  open: boolean
  onToggle: () => void
  onDismiss: () => void
}

// GetStarted — onboarding checklist for fresh tenants. Promoted to the top
// of the dashboard because the KPIs below read zero until these are done.
// Self-hides when the checklist completes; dismissable for the session.
function GetStarted({ overview, hasPlans, open, onToggle, onDismiss }: GetStartedProps) {
  const steps = [
    {
      done: hasPlans,
      label: 'Configure pricing',
      desc: 'Create meters and plans',
      to: '/pricing',
      cta: 'Set up pricing',
    },
    {
      done: overview.active_customers > 0,
      label: 'Add your first customer',
      desc: 'Who are you billing?',
      to: '/customers',
      cta: 'Add a customer',
    },
    {
      done: overview.active_subscriptions > 0,
      label: 'Create a subscription',
      desc: 'Subscribe them to a plan',
      to: '/subscriptions',
      cta: 'New subscription',
    },
    {
      done: overview.revenue > 0,
      label: 'Send your first invoice',
      desc: 'Kick off a billing run',
      to: '/subscriptions',
      cta: 'Open subscriptions',
    },
  ]
  const total = steps.length
  const completed = steps.filter(s => s.done).length
  if (completed >= total) return null
  const percent = Math.round((completed / total) * 100)
  const next = steps.find(s => !s.done)

  return (
    <Card className="mt-6 border-primary/20 bg-gradient-to-br from-primary/[0.03] to-transparent">
      <CardContent className="p-0">
        {/* Header: title, progress bar, inline next-step hint, controls */}
        <div className="flex items-start gap-3 px-5 py-4">
          <div className="flex-1 min-w-0">
            <div className="flex items-center gap-2">
              <p className="text-sm font-semibold text-foreground">Get Velox ready</p>
              <span className="text-xs text-muted-foreground tabular-nums">{completed}/{total}</span>
            </div>
            <div
              className="mt-2 h-1.5 bg-muted rounded-full overflow-hidden"
              role="progressbar"
              aria-valuenow={completed}
              aria-valuemin={0}
              aria-valuemax={total}
              aria-label="Setup progress"
            >
              <div
                className="h-full bg-primary rounded-full transition-[width] duration-300"
                style={{ width: `${percent}%` }}
              />
            </div>
            {next && !open && (
              <p className="mt-2 text-xs text-muted-foreground">
                Next: <span className="text-foreground font-medium">{next.label}</span>
              </p>
            )}
          </div>
          <div className="flex items-center gap-1 shrink-0">
            <button
              type="button"
              onClick={onToggle}
              aria-expanded={open}
              aria-label={open ? 'Collapse' : 'Expand'}
              className="h-7 w-7 rounded-md text-muted-foreground hover:text-foreground hover:bg-accent flex items-center justify-center transition-colors"
            >
              {open ? <ChevronUp size={14} /> : <ChevronDown size={14} />}
            </button>
            <button
              type="button"
              onClick={onDismiss}
              aria-label="Hide for now"
              className="h-7 w-7 rounded-md text-muted-foreground hover:text-foreground hover:bg-accent flex items-center justify-center transition-colors"
            >
              <X size={14} />
            </button>
          </div>
        </div>

        {open && (
          <ol className="border-t border-border">
            {steps.map((step, i) => {
              const isNext = step === next
              const rowCls = cn(
                'flex items-center gap-3 px-5 py-3 border-b border-border last:border-b-0 transition-colors',
                !step.done && 'hover:bg-accent/40',
                isNext && 'bg-primary/[0.04]',
              )
              const indicator = step.done ? (
                <div
                  className="h-6 w-6 shrink-0 rounded-full bg-emerald-500/15 text-emerald-600 dark:text-emerald-400 flex items-center justify-center"
                  aria-label="done"
                >
                  <Check size={13} />
                </div>
              ) : (
                <div
                  aria-hidden="true"
                  className={cn(
                    'h-6 w-6 shrink-0 rounded-full flex items-center justify-center text-[11px] font-semibold tabular-nums',
                    isNext
                      ? 'bg-primary text-primary-foreground'
                      : 'bg-muted text-muted-foreground ring-1 ring-border',
                  )}
                >
                  {i + 1}
                </div>
              )
              const body = (
                <>
                  {indicator}
                  <div className="flex-1 min-w-0">
                    <p className={cn(
                      'text-sm font-medium',
                      step.done ? 'text-muted-foreground' : 'text-foreground',
                    )}>
                      {step.label}
                    </p>
                    <p className="text-xs text-muted-foreground mt-0.5">{step.desc}</p>
                  </div>
                  {isNext && (
                    <span className="text-xs font-medium text-primary flex items-center gap-1 shrink-0">
                      {step.cta}
                      <ArrowRight size={12} />
                    </span>
                  )}
                </>
              )
              return !step.done && step.to ? (
                <li key={i}>
                  <Link to={step.to} className={rowCls}>{body}</Link>
                </li>
              ) : (
                <li key={i} className={rowCls}>{body}</li>
              )
            })}
          </ol>
        )}
      </CardContent>
    </Card>
  )
}
