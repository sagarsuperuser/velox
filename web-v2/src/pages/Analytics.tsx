import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api, formatCents } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { cn } from '@/lib/utils'
import { Card, CardContent } from '@/components/ui/card'
import { Separator } from '@/components/ui/separator'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Button } from '@/components/ui/button'
import { Loader2 } from 'lucide-react'
import {
  AreaChart, Area, BarChart, Bar, PieChart, Pie, Cell,
  XAxis, YAxis, Tooltip, ResponsiveContainer, CartesianGrid,
} from 'recharts'

type Period = '30d' | '90d' | '12m'

const COLORS = { purple: '#635BFF', emerald: '#10B981', red: '#EF4444', amber: '#F59E0B', slate: '#94A3B8' }

function chartTheme() {
  const isDark = typeof document !== 'undefined' && document.documentElement.classList.contains('dark')
  return {
    grid: isDark ? 'rgba(255,255,255,0.06)' : 'rgba(0,0,0,0.06)',
    tick: isDark ? '#a1a1aa' : '#71717a',
    tooltip: { bg: isDark ? '#18181b' : '#ffffff', border: isDark ? '#27272a' : '#e4e4e7', text: isDark ? '#fafafa' : '#09090b' },
  }
}

function formatCompactAmount(value: number): string {
  if (value >= 100_000_00) return `$${Math.round(value / 100_00)}K`
  if (value >= 1_000_00) return `$${(value / 100_00).toFixed(1)}K`
  return `$${Math.round(value / 100)}`
}

function formatShortDate(dateStr: string): string {
  const d = new Date(dateStr)
  return d.toLocaleDateString('en-US', { month: 'short', day: 'numeric' })
}

export default function AnalyticsPage() {
  const [period, setPeriod] = useState<Period>('30d')

  const { data: overview, isLoading: loading, error: errorObj, refetch } = useQuery({
    queryKey: ['analytics-overview'],
    queryFn: () => api.getAnalyticsOverview(),
  })
  const { data: chartRes } = useQuery({
    queryKey: ['analytics-chart', period],
    queryFn: () => api.getRevenueChart(period),
  })

  const chartData = chartRes?.data ?? []
  const error = errorObj instanceof Error ? errorObj.message : errorObj ? String(errorObj) : null

  const paidCount = overview?.paid_invoices_30d ?? 0
  const failedCount = overview?.failed_payments_30d ?? 0
  const totalPayments = paidCount + failedCount
  const successRate = totalPayments > 0 ? Math.round((paidCount / totalPayments) * 1000) / 10 : 0

  const donutData = [
    { name: 'Paid', value: paidCount, color: COLORS.emerald },
    { name: 'Failed', value: failedCount, color: COLORS.red },
  ]

  const invoiceSummaryData = [
    { name: 'Paid', count: paidCount, fill: COLORS.emerald },
    { name: 'Open', count: overview?.open_invoices ?? 0, fill: COLORS.amber },
    { name: 'Failed', count: failedCount, fill: COLORS.red },
    { name: 'Dunning', count: overview?.dunning_active ?? 0, fill: COLORS.slate },
  ]

  return (
    <Layout>
      <div>
        <h1 className="text-2xl font-semibold text-foreground">Analytics</h1>
        <p className="text-sm text-muted-foreground mt-1">Revenue insights and billing metrics</p>
      </div>

      {error ? (
        <Card className="mt-6"><CardContent className="p-8 text-center">
          <p className="text-sm text-destructive mb-3">{error}</p>
          <Button variant="outline" size="sm" onClick={() => refetch()}>Retry</Button>
        </CardContent></Card>
      ) : loading || !overview ? (
        <Card className="mt-6"><CardContent className="p-8 flex justify-center">
          <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
        </CardContent></Card>
      ) : (
        <>
          {/* Revenue Trend — full width with period tabs */}
          <Card className="mt-6">
            <CardContent className="p-5">
              <div className="flex items-center justify-between mb-4">
                <p className="text-sm font-medium text-foreground">Revenue Trend</p>
                <Tabs value={period} onValueChange={v => setPeriod(v as Period)}>
                  <TabsList>
                    <TabsTrigger value="30d">30 days</TabsTrigger>
                    <TabsTrigger value="90d">90 days</TabsTrigger>
                    <TabsTrigger value="12m">12 months</TabsTrigger>
                  </TabsList>
                </Tabs>
              </div>
              {chartData.length > 0 ? (() => {
                const theme = chartTheme()
                return (
                  <ResponsiveContainer width="100%" height={280}>
                    <AreaChart data={chartData} margin={{ top: 5, right: 5, left: 5, bottom: 5 }}>
                      <defs>
                        <linearGradient id="revFill" x1="0" y1="0" x2="0" y2="1">
                          <stop offset="0%" stopColor={COLORS.purple} stopOpacity={0.15} />
                          <stop offset="100%" stopColor={COLORS.purple} stopOpacity={0} />
                        </linearGradient>
                      </defs>
                      <CartesianGrid strokeDasharray="3 3" vertical={false} stroke={theme.grid} />
                      <XAxis dataKey="date" tickFormatter={formatShortDate} tick={{ fontSize: 11, fill: theme.tick }} axisLine={false} tickLine={false} />
                      <YAxis tickFormatter={formatCompactAmount} tick={{ fontSize: 11, fill: theme.tick }} axisLine={false} tickLine={false} width={55} />
                      <Tooltip
                        formatter={(value: number) => [formatCents(value), 'Revenue']}
                        labelFormatter={formatShortDate}
                        contentStyle={{ backgroundColor: theme.tooltip.bg, border: `1px solid ${theme.tooltip.border}`, borderRadius: '8px', fontSize: '13px', color: theme.tooltip.text }}
                        itemStyle={{ color: theme.tooltip.text }}
                      />
                      <Area type="monotone" dataKey="revenue_cents" stroke={COLORS.purple} strokeWidth={2} fill="url(#revFill)" dot={false} activeDot={{ r: 4, fill: '#fff', stroke: COLORS.purple, strokeWidth: 2 }} />
                    </AreaChart>
                  </ResponsiveContainer>
                )
              })() : (
                <div className="h-[280px] flex items-center justify-center text-sm text-muted-foreground">
                  No revenue data for this period
                </div>
              )}
            </CardContent>
          </Card>

          {/* Payment Success + Invoice Summary */}
          <div className="grid grid-cols-1 md:grid-cols-2 gap-4 mt-6">
            {/* Payment Success Donut */}
            <Card>
              <CardContent className="p-5">
                <p className="text-sm font-medium text-foreground">Payment Success Rate</p>
                <p className="text-xs text-muted-foreground mt-0.5">Last 30 days</p>
                {totalPayments > 0 ? (
                  <div className="flex flex-col items-center mt-4">
                    <div className="relative">
                      <ResponsiveContainer width={180} height={180}>
                        <PieChart>
                          <Pie data={donutData} cx="50%" cy="50%" innerRadius={55} outerRadius={75} dataKey="value" stroke="none">
                            {donutData.map((d, i) => <Cell key={i} fill={d.color} />)}
                          </Pie>
                        </PieChart>
                      </ResponsiveContainer>
                      <div className="absolute inset-0 flex items-center justify-center pointer-events-none">
                        <p className="text-2xl font-bold tabular-nums text-foreground">{successRate}%</p>
                      </div>
                    </div>
                    <div className="flex items-center gap-6 mt-2">
                      <span className="flex items-center gap-1.5 text-sm text-muted-foreground">
                        <span className="w-2 h-2 rounded-full" style={{ backgroundColor: COLORS.emerald }} /> Paid {paidCount}
                      </span>
                      <span className="flex items-center gap-1.5 text-sm text-muted-foreground">
                        <span className="w-2 h-2 rounded-full" style={{ backgroundColor: COLORS.red }} /> Failed {failedCount}
                      </span>
                    </div>
                  </div>
                ) : (
                  <div className="h-[200px] flex items-center justify-center text-sm text-muted-foreground">No payment data yet</div>
                )}
              </CardContent>
            </Card>

            {/* Invoice Summary Bars */}
            <Card>
              <CardContent className="p-5">
                <p className="text-sm font-medium text-foreground">Invoice Summary</p>
                <p className="text-xs text-muted-foreground mt-0.5">Current status breakdown</p>
                {invoiceSummaryData.some(d => d.count > 0) ? (() => {
                  const theme = chartTheme()
                  return (
                    <div className="mt-4">
                      <ResponsiveContainer width="100%" height={200}>
                        <BarChart data={invoiceSummaryData} margin={{ top: 5, right: 5, left: 5, bottom: 5 }}>
                          <CartesianGrid strokeDasharray="3 3" stroke={theme.grid} vertical={false} />
                          <XAxis dataKey="name" tick={{ fontSize: 11, fill: theme.tick }} axisLine={false} tickLine={false} />
                          <YAxis tick={{ fontSize: 11, fill: theme.tick }} axisLine={false} tickLine={false} allowDecimals={false} />
                          <Tooltip contentStyle={{ backgroundColor: theme.tooltip.bg, border: `1px solid ${theme.tooltip.border}`, borderRadius: '8px', fontSize: '13px', color: theme.tooltip.text }} itemStyle={{ color: theme.tooltip.text }} />
                          <Bar dataKey="count" radius={[3, 3, 0, 0]}>
                            {invoiceSummaryData.map((entry, i) => <Cell key={i} fill={entry.fill} />)}
                          </Bar>
                        </BarChart>
                      </ResponsiveContainer>
                    </div>
                  )
                })() : (
                  <div className="h-[200px] flex items-center justify-center text-sm text-muted-foreground">No invoice data yet</div>
                )}
              </CardContent>
            </Card>
          </div>

          {/* Customer Stats + Financial Summary */}
          <div className="grid grid-cols-1 md:grid-cols-2 gap-4 mt-6">
            <Card>
              <CardContent className="p-5">
                <p className="text-sm font-medium text-foreground mb-4">Customer Stats</p>
                <div className="space-y-3">
                  <div className="flex items-center justify-between">
                    <span className="text-sm text-muted-foreground">Active Customers</span>
                    <span className="text-sm font-semibold tabular-nums text-foreground">{overview.active_customers}</span>
                  </div>
                  <Separator />
                  <div className="flex items-center justify-between">
                    <span className="text-sm text-muted-foreground">Active Subscriptions</span>
                    <span className="text-sm font-semibold tabular-nums text-foreground">{overview.active_subscriptions}</span>
                  </div>
                  <Separator />
                  <div className="flex items-center justify-between">
                    <span className="text-sm text-muted-foreground">Dunning Active</span>
                    <span className={cn('text-sm font-semibold tabular-nums', overview.dunning_active > 0 ? 'text-amber-600' : 'text-foreground')}>{overview.dunning_active}</span>
                  </div>
                  <Separator />
                  <div className="flex items-center justify-between">
                    <span className="text-sm text-muted-foreground">Open Invoices</span>
                    <span className="text-sm font-semibold tabular-nums text-foreground">{overview.open_invoices}</span>
                  </div>
                </div>
              </CardContent>
            </Card>

            <Card>
              <CardContent className="p-5">
                <p className="text-sm font-medium text-foreground mb-4">Financial Summary</p>
                <div className="space-y-3">
                  <div className="flex items-center justify-between">
                    <span className="text-sm text-muted-foreground">MRR</span>
                    <span className="text-sm font-semibold tabular-nums text-foreground">{formatCents(overview.mrr)}</span>
                  </div>
                  <Separator />
                  <div className="flex items-center justify-between">
                    <span className="text-sm text-muted-foreground">Total Revenue</span>
                    <span className="text-sm font-semibold tabular-nums text-foreground">{formatCents(overview.total_revenue)}</span>
                  </div>
                  <Separator />
                  <div className="flex items-center justify-between">
                    <span className="text-sm text-muted-foreground">Outstanding AR</span>
                    <span className={cn('text-sm font-semibold tabular-nums', overview.outstanding_ar > 0 ? 'text-amber-600' : 'text-foreground')}>{formatCents(overview.outstanding_ar)}</span>
                  </div>
                  <Separator />
                  <div className="flex items-center justify-between">
                    <span className="text-sm text-muted-foreground">Avg Invoice</span>
                    <span className="text-sm font-semibold tabular-nums text-foreground">{formatCents(overview.avg_invoice_value)}</span>
                  </div>
                  <Separator />
                  <div className="flex items-center justify-between">
                    <span className="text-sm text-muted-foreground">Credit Balance</span>
                    <span className="text-sm font-semibold tabular-nums text-foreground">{formatCents(overview.credit_balance_total)}</span>
                  </div>
                </div>
              </CardContent>
            </Card>
          </div>
        </>
      )}
    </Layout>
  )
}
