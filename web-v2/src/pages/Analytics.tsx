import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api, formatCents } from '@/lib/api'
import type { MRRMovementPoint } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Card, CardContent } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import {
  Area, AreaChart, Bar, BarChart, Cell, ComposedChart, Line, Pie, PieChart,
  ReferenceLine, ResponsiveContainer, Tooltip, XAxis, YAxis, CartesianGrid, Legend,
} from 'recharts'
import { TrendCard } from '@/components/analytics/TrendCard'
import { TrendCardSkeleton, ChartCardSkeleton } from '@/components/analytics/CardSkeleton'
import { PeriodTabs } from '@/components/analytics/PeriodTabs'
import { PERIOD_LABELS, type Period } from '@/components/analytics/period'
import { ExportButton } from '@/components/analytics/ExportButton'
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

function formatPct(v: number): string {
  if (!isFinite(v)) return '—'
  return `${(v * 100).toFixed(1)}%`
}

function formatCount(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}K`
  return n.toLocaleString()
}

export default function AnalyticsPage() {
  const [period, setPeriod] = useState<Period>('30d')
  const theme = useChartTheme()

  const { data: overview, isLoading: overviewLoading, error: errorObj, refetch } = useQuery({
    queryKey: ['analytics-overview', period],
    queryFn: () => api.getAnalyticsOverview(period),
  })
  const { data: chartRes, isLoading: chartLoading } = useQuery({
    queryKey: ['analytics-chart', period],
    queryFn: () => api.getRevenueChart(period),
  })
  const { data: mrrMv, isLoading: mrrMvLoading } = useQuery({
    queryKey: ['analytics-mrr-movement', period],
    queryFn: () => api.getMRRMovement(period),
  })
  const { data: usage, isLoading: usageLoading } = useQuery({
    queryKey: ['analytics-usage', period],
    queryFn: () => api.getUsageAnalytics(period),
  })

  const chartData = chartRes?.data ?? []
  const error = errorObj instanceof Error ? errorObj.message : errorObj ? String(errorObj) : null

  // Prep MRR movement for the stacked chart: churned & contraction render below zero.
  const mrrMvChartData = useMemo(() => {
    return (mrrMv?.data ?? []).map((p: MRRMovementPoint) => ({
      date: p.date,
      newMrr: p.new,
      expansion: p.expansion,
      contraction: -p.contraction,
      churned: -p.churned,
      net: p.net,
    }))
  }, [mrrMv])

  // Payment success (period-aware via overview)
  const paid = overview?.paid_invoices ?? 0
  const failed = overview?.failed_payments ?? 0
  const totalPayments = paid + failed
  const successRate = totalPayments > 0 ? Math.round((paid / totalPayments) * 1000) / 10 : 0
  const paymentDonut = [
    { name: 'Paid', value: paid, color: theme.success },
    { name: 'Failed', value: failed, color: theme.danger },
  ]

  // Invoice-status snapshot (not period-aware — labeled accordingly)
  const invoiceSummary = useMemo(() => ([
    { name: 'Paid', count: paid, fill: theme.success },
    { name: 'Open', count: overview?.open_invoices ?? 0, fill: theme.warning },
    { name: 'Failed', count: failed, fill: theme.danger },
    { name: 'Dunning', count: overview?.dunning_active ?? 0, fill: theme.neutral },
  ]), [overview, paid, failed, theme])

  return (
    <Layout>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold text-foreground">Analytics</h1>
          <p className="text-sm text-muted-foreground mt-1">Revenue, retention, and usage insights</p>
        </div>
        <PeriodTabs value={period} onChange={setPeriod} />
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
          {/* Headline KPIs */}
          <div className="grid grid-cols-2 lg:grid-cols-4 gap-4 mt-6">
            <TrendCard
              label="MRR"
              value={formatCents(overview.mrr)}
              current={overview.mrr}
              previous={overview.mrr_prev}
              hint={`Net Δ ${formatCents(overview.mrr_movement.net)}`}
            />
            <TrendCard
              label="ARR"
              value={formatCents(overview.arr)}
              current={overview.arr}
              previous={overview.arr_prev}
              hint="Annualized run rate"
            />
            <TrendCard
              label="NRR"
              value={formatPct(overview.nrr)}
              hint="Net revenue retention"
              valueTone={overview.nrr >= 1 ? 'default' : overview.nrr === 0 ? 'default' : 'warning'}
            />
            <TrendCard
              label="Logo Churn"
              value={formatPct(overview.logo_churn_rate)}
              inverseDelta
              valueTone={overview.logo_churn_rate > 0.05 ? 'danger' : 'default'}
              hint={`${formatPct(overview.revenue_churn_rate)} revenue churn`}
              sparklineTone="danger"
            />
          </div>

          {/* Revenue trend */}
          {chartLoading ? (
            <div className="mt-6"><ChartCardSkeleton height={280} /></div>
          ) : (
            <Card className="mt-6">
              <CardContent className="p-5">
                <div className="flex items-center justify-between mb-4 gap-3 flex-wrap">
                  <div>
                    <p className="text-sm font-medium text-foreground">Revenue Trend</p>
                    <p className="text-xs text-muted-foreground mt-0.5">Paid invoices · {PERIOD_LABELS[period].toLowerCase()}</p>
                  </div>
                  <ExportButton
                    filename={`revenue-${period}.csv`}
                    rows={chartData}
                  />
                </div>
                {chartData.length > 0 ? (
                  <div role="img" aria-label={`Revenue area chart for ${PERIOD_LABELS[period].toLowerCase()}`}>
                  <ResponsiveContainer width="100%" height={280}>
                    <AreaChart data={chartData} margin={{ top: 5, right: 5, left: 5, bottom: 5 }}>
                      <defs>
                        <linearGradient id="revFill" x1="0" y1="0" x2="0" y2="1">
                          <stop offset="0%" stopColor={theme.primary} stopOpacity={0.18} />
                          <stop offset="100%" stopColor={theme.primary} stopOpacity={0} />
                        </linearGradient>
                      </defs>
                      <CartesianGrid strokeDasharray="3 3" vertical={false} stroke={theme.grid} />
                      <XAxis dataKey="date" tickFormatter={formatShortDate} tick={{ fontSize: 11, fill: theme.tick }} axisLine={false} tickLine={false} />
                      <YAxis tickFormatter={formatCompactAmount} tick={{ fontSize: 11, fill: theme.tick }} axisLine={false} tickLine={false} width={55} />
                      <Tooltip
                        formatter={(value) => [formatCents(Number(value)), 'Revenue']}
                        labelFormatter={(label) => formatShortDate(String(label))}
                        contentStyle={tooltipStyle(theme)}
                        itemStyle={{ color: theme.tooltipText }}
                        cursor={{ fill: theme.grid }}
                      />
                      <Area type="monotone" dataKey="revenue_cents" stroke={theme.primary} strokeWidth={2} fill="url(#revFill)" dot={false}
                        activeDot={{ r: 4, fill: theme.tooltipBg, stroke: theme.primary, strokeWidth: 2 }} />
                    </AreaChart>
                  </ResponsiveContainer>
                  </div>
                ) : (
                  <div className="h-[280px] flex items-center justify-center text-sm text-muted-foreground">
                    No revenue data for this period.
                  </div>
                )}
              </CardContent>
            </Card>
          )}

          {/* MRR movement */}
          {mrrMvLoading ? (
            <div className="mt-6"><ChartCardSkeleton height={280} /></div>
          ) : (
            <Card className="mt-6">
              <CardContent className="p-5">
                <div className="flex items-center justify-between mb-4 gap-3 flex-wrap">
                  <div>
                    <p className="text-sm font-medium text-foreground">MRR Movement</p>
                    <p className="text-xs text-muted-foreground mt-0.5">
                      New + expansion − contraction − churn · {PERIOD_LABELS[period].toLowerCase()}
                    </p>
                  </div>
                  <ExportButton filename={`mrr-movement-${period}.csv`} rows={mrrMv?.data ?? []} />
                </div>
                {/* Totals strip */}
                <div className="grid grid-cols-2 md:grid-cols-5 gap-3 mb-4">
                  <MovementTotal label="New" value={overview.mrr_movement.new} color={theme.success} />
                  <MovementTotal label="Expansion" value={overview.mrr_movement.expansion} color={theme.primary} />
                  <MovementTotal label="Contraction" value={-overview.mrr_movement.contraction} color={theme.warning} />
                  <MovementTotal label="Churned" value={-overview.mrr_movement.churned} color={theme.danger} />
                  <MovementTotal label="Net" value={overview.mrr_movement.net} color={theme.tick} bold />
                </div>
                {mrrMvChartData.length > 0 ? (
                  <div role="img" aria-label={`MRR movement stacked bar chart for ${PERIOD_LABELS[period].toLowerCase()}`}>
                  <ResponsiveContainer width="100%" height={280}>
                    <ComposedChart data={mrrMvChartData} margin={{ top: 5, right: 5, left: 5, bottom: 5 }} stackOffset="sign">
                      <CartesianGrid strokeDasharray="3 3" vertical={false} stroke={theme.grid} />
                      <XAxis dataKey="date" tickFormatter={formatShortDate} tick={{ fontSize: 11, fill: theme.tick }} axisLine={false} tickLine={false} />
                      <YAxis tickFormatter={formatCompactAmount} tick={{ fontSize: 11, fill: theme.tick }} axisLine={false} tickLine={false} width={55} />
                      <ReferenceLine y={0} stroke={theme.tick} strokeWidth={1} />
                      <Tooltip
                        formatter={(value, name) => [formatCents(Math.abs(Number(value))), String(name)]}
                        labelFormatter={(label) => formatShortDate(String(label))}
                        contentStyle={tooltipStyle(theme)}
                        itemStyle={{ color: theme.tooltipText }}
                        cursor={{ fill: theme.grid }}
                      />
                      <Legend wrapperStyle={{ fontSize: 12, color: theme.tick }} />
                      <Bar dataKey="newMrr" name="New" stackId="mrr" fill={theme.success} radius={[2, 2, 0, 0]} />
                      <Bar dataKey="expansion" name="Expansion" stackId="mrr" fill={theme.primary} />
                      <Bar dataKey="contraction" name="Contraction" stackId="mrr" fill={theme.warning} />
                      <Bar dataKey="churned" name="Churned" stackId="mrr" fill={theme.danger} radius={[0, 0, 2, 2]} />
                      <Line type="monotone" dataKey="net" name="Net" stroke={theme.tick} strokeWidth={2} dot={false} />
                    </ComposedChart>
                  </ResponsiveContainer>
                  </div>
                ) : (
                  <div className="h-[280px] flex items-center justify-center text-sm text-muted-foreground">
                    No subscription activity yet.
                  </div>
                )}
              </CardContent>
            </Card>
          )}

          {/* Payment success + invoice summary */}
          <div className="grid grid-cols-1 md:grid-cols-2 gap-4 mt-6">
            <Card>
              <CardContent className="p-5">
                <p className="text-sm font-medium text-foreground">Payment Success Rate</p>
                <p className="text-xs text-muted-foreground mt-0.5">{PERIOD_LABELS[period]}</p>
                {totalPayments > 0 ? (
                  <div className="flex flex-col items-center mt-4">
                    <div className="relative" role="img" aria-label={`Payment success rate donut: ${successRate}% of ${totalPayments} payments succeeded`}>
                      <ResponsiveContainer width={180} height={180}>
                        <PieChart>
                          <Pie data={paymentDonut} cx="50%" cy="50%" innerRadius={55} outerRadius={75} dataKey="value" stroke="none">
                            {paymentDonut.map((d, i) => <Cell key={i} fill={d.color} />)}
                          </Pie>
                        </PieChart>
                      </ResponsiveContainer>
                      <div className="absolute inset-0 flex items-center justify-center pointer-events-none">
                        <p className="text-2xl font-bold tabular-nums text-foreground">{successRate}%</p>
                      </div>
                    </div>
                    <div className="flex items-center gap-6 mt-2">
                      <LegendDot color={theme.success} label={`Paid ${paid}`} />
                      <LegendDot color={theme.danger} label={`Failed ${failed}`} />
                    </div>
                  </div>
                ) : (
                  <div className="h-[200px] flex items-center justify-center text-sm text-muted-foreground">
                    No payment data in this period.
                  </div>
                )}
              </CardContent>
            </Card>

            <Card>
              <CardContent className="p-5">
                <p className="text-sm font-medium text-foreground">Invoice Status</p>
                <p className="text-xs text-muted-foreground mt-0.5">
                  Paid/Failed: {PERIOD_LABELS[period].toLowerCase()} · Open/Dunning: current
                </p>
                {invoiceSummary.some(d => d.count > 0) ? (
                  <div className="mt-4" role="img" aria-label="Invoice status breakdown bar chart">
                    <ResponsiveContainer width="100%" height={200}>
                      <BarChart data={invoiceSummary} margin={{ top: 5, right: 5, left: 5, bottom: 5 }}>
                        <CartesianGrid strokeDasharray="3 3" stroke={theme.grid} vertical={false} />
                        <XAxis dataKey="name" tick={{ fontSize: 11, fill: theme.tick }} axisLine={false} tickLine={false} />
                        <YAxis tick={{ fontSize: 11, fill: theme.tick }} axisLine={false} tickLine={false} allowDecimals={false} />
                        <Tooltip contentStyle={tooltipStyle(theme)} itemStyle={{ color: theme.tooltipText }} cursor={{ fill: theme.grid }} />
                        <Bar dataKey="count" radius={[3, 3, 0, 0]}>
                          {invoiceSummary.map((entry, i) => <Cell key={i} fill={entry.fill} />)}
                        </Bar>
                      </BarChart>
                    </ResponsiveContainer>
                  </div>
                ) : (
                  <div className="h-[200px] flex items-center justify-center text-sm text-muted-foreground">No invoice data yet.</div>
                )}
              </CardContent>
            </Card>
          </div>

          {/* Usage analytics */}
          {usageLoading ? (
            <div className="mt-6"><ChartCardSkeleton height={240} /></div>
          ) : (
            <Card className="mt-6">
              <CardContent className="p-5">
                <div className="flex items-center justify-between mb-4 gap-3 flex-wrap">
                  <div>
                    <p className="text-sm font-medium text-foreground">Usage Events</p>
                    <p className="text-xs text-muted-foreground mt-0.5">
                      {formatCount(usage?.totals.events ?? 0)} events · {formatCount(usage?.totals.quantity ?? 0)} units · {PERIOD_LABELS[period].toLowerCase()}
                    </p>
                  </div>
                  <ExportButton filename={`usage-${period}.csv`} rows={usage?.data ?? []} />
                </div>
                <div className="grid grid-cols-1 lg:grid-cols-3 gap-5">
                  <div className="lg:col-span-2">
                    {(usage?.data ?? []).length > 0 ? (
                      <div role="img" aria-label={`Usage events bar chart for ${PERIOD_LABELS[period].toLowerCase()}`}>
                      <ResponsiveContainer width="100%" height={240}>
                        <BarChart data={usage?.data ?? []} margin={{ top: 5, right: 5, left: 5, bottom: 5 }}>
                          <CartesianGrid strokeDasharray="3 3" vertical={false} stroke={theme.grid} />
                          <XAxis dataKey="date" tickFormatter={formatShortDate} tick={{ fontSize: 11, fill: theme.tick }} axisLine={false} tickLine={false} />
                          <YAxis tickFormatter={formatCount} tick={{ fontSize: 11, fill: theme.tick }} axisLine={false} tickLine={false} width={45} />
                          <Tooltip
                            formatter={(value) => [formatCount(Number(value)), 'Events']}
                            labelFormatter={(label) => formatShortDate(String(label))}
                            contentStyle={tooltipStyle(theme)}
                            itemStyle={{ color: theme.tooltipText }}
                            cursor={{ fill: theme.grid }}
                          />
                          <Bar dataKey="events" fill={theme.info} radius={[2, 2, 0, 0]} />
                        </BarChart>
                      </ResponsiveContainer>
                      </div>
                    ) : (
                      <div className="h-[240px] flex items-center justify-center text-sm text-muted-foreground">No usage events in this period.</div>
                    )}
                  </div>
                  <div>
                    <p className="text-xs uppercase tracking-wider text-muted-foreground mb-2">Top meters</p>
                    {(usage?.top_meters ?? []).length > 0 ? (
                      <ul className="space-y-2">
                        {usage!.top_meters.slice(0, 6).map(m => (
                          <li key={m.meter_id} className="flex items-center justify-between text-sm gap-3">
                            <span className="text-foreground truncate" title={m.meter_name}>{m.meter_name}</span>
                            <span className="text-xs text-muted-foreground tabular-nums shrink-0">{formatCount(m.events)}</span>
                          </li>
                        ))}
                      </ul>
                    ) : (
                      <p className="text-sm text-muted-foreground">No meter activity.</p>
                    )}
                  </div>
                </div>
              </CardContent>
            </Card>
          )}

          {/* Financial + customer trend cards */}
          <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4 mt-6">
            <TrendCard label="Outstanding AR" value={formatCents(overview.outstanding_ar)}
              hint={`${overview.open_invoices} open ${overview.open_invoices === 1 ? 'invoice' : 'invoices'}`}
              valueTone={overview.outstanding_ar > 0 ? 'warning' : 'default'} />
            <TrendCard label="Avg Invoice Value" value={formatCents(overview.avg_invoice_value)} hint="All-time paid invoices" />
            <TrendCard label="Credit Balance" value={formatCents(overview.credit_balance_total)} hint="Total outstanding across customers" />
            <TrendCard label="Dunning Recovery" value={formatPct(overview.dunning_recovery_rate)}
              hint={`${overview.dunning_active} active ${overview.dunning_active === 1 ? 'run' : 'runs'}`}
              valueTone={overview.dunning_recovery_rate < 0.5 && overview.dunning_active > 0 ? 'warning' : 'default'} />
          </div>
        </>
      )}
    </Layout>
  )
}

function MovementTotal({ label, value, color, bold }: { label: string; value: number; color: string; bold?: boolean }) {
  return (
    <div className="flex flex-col gap-1">
      <span className="text-xs uppercase tracking-wider text-muted-foreground flex items-center gap-1.5">
        <span className="w-2 h-2 rounded-full" style={{ backgroundColor: color }} aria-hidden />
        {label}
      </span>
      <span className={bold ? 'text-sm font-semibold tabular-nums text-foreground' : 'text-sm font-medium tabular-nums text-foreground'}>
        {value >= 0 ? '+' : ''}{formatCents(value)}
      </span>
    </div>
  )
}

function LegendDot({ color, label }: { color: string; label: string }) {
  return (
    <span className="flex items-center gap-1.5 text-sm text-muted-foreground">
      <span className="w-2 h-2 rounded-full" style={{ backgroundColor: color }} aria-hidden />
      {label}
    </span>
  )
}
