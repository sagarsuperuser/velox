import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api, formatCents, formatDate } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { cn } from '@/lib/utils'
import { Card, CardContent } from '@/components/ui/card'
import { Separator } from '@/components/ui/separator'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import {
  AreaChart, Area, BarChart, Bar, PieChart, Pie, Cell,
  XAxis, YAxis, Tooltip, ResponsiveContainer, CartesianGrid,
} from 'recharts'

type Period = '30d' | '90d' | '12m'

const COLORS = {
  purple: '#635BFF',
  emerald: '#10B981',
  red: '#EF4444',
  amber: '#F59E0B',
  slate: '#94A3B8',
}

function chartTheme() {
  const isDark = document.documentElement.classList.contains('dark')
  return {
    grid: isDark ? 'rgba(255,255,255,0.08)' : 'rgba(0,0,0,0.08)',
    tick: isDark ? '#a1a1aa' : '#71717a',
    tooltipBg: isDark ? '#27272a' : '#ffffff',
    tooltipBorder: isDark ? 'rgba(255,255,255,0.1)' : 'rgba(0,0,0,0.08)',
    tooltipColor: isDark ? '#fafafa' : '#09090b',
  }
}

function StatCard({ title, value, subtitle, valueClass }: { title: string; value: string; subtitle?: string; valueClass?: string }) {
  return (
    <Card>
      <CardContent className="p-5">
        <p className="text-xs uppercase tracking-wider text-muted-foreground">{title}</p>
        <p className={cn('text-xl font-semibold tabular-nums mt-1', valueClass ?? 'text-foreground')}>{value}</p>
        {subtitle && <p className="text-xs text-muted-foreground mt-1">{subtitle}</p>}
      </CardContent>
    </Card>
  )
}

export default function AnalyticsPage() {
  const [period, setPeriod] = useState<Period>('30d')

  const { data: overview, isLoading: overviewLoading, error: overviewError, refetch } = useQuery({
    queryKey: ['analytics-overview'],
    queryFn: () => api.getAnalyticsOverview(),
  })

  const { data: chartRes } = useQuery({
    queryKey: ['analytics-chart', period],
    queryFn: () => api.getRevenueChart(period),
  })

  const chartData = chartRes?.data ?? []
  const loading = overviewLoading
  const error = overviewError instanceof Error ? overviewError.message : overviewError ? String(overviewError) : null

  // Derived data
  const paidCount = overview?.paid_invoices_30d ?? 0
  const failedCount = overview?.failed_payments_30d ?? 0
  const totalPayments = paidCount + failedCount
  const successRate = totalPayments > 0 ? Math.round((paidCount / totalPayments) * 1000) / 10 : 0

  const donutData = [
    { name: 'Paid', value: paidCount, color: COLORS.emerald },
    { name: 'Failed', value: failedCount, color: COLORS.red },
  ]

  const openInvoices = overview?.open_invoices ?? 0
  const invoiceSummaryData = [
    { name: 'Paid', count: paidCount, fill: COLORS.emerald },
    { name: 'Open', count: openInvoices, fill: COLORS.amber },
    { name: 'Failed', count: failedCount, fill: COLORS.red },
    { name: 'Dunning', count: overview?.dunning_active ?? 0, fill: COLORS.slate },
  ]

  return (
    <Layout>
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold text-foreground">Analytics</h1>
          <p className="text-sm text-muted-foreground mt-1">Revenue insights and billing metrics</p>
        </div>
      </div>

      {error ? (
        <Card className="mt-6">
          <CardContent className="p-8 text-center">
            <p className="text-sm text-destructive mb-3">{error}</p>
            <Button variant="outline" size="sm" onClick={() => refetch()}>Retry</Button>
          </CardContent>
        </Card>
      ) : loading || !overview ? (
        <Card className="mt-6">
          <CardContent className="p-8 flex justify-center">
            <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
          </CardContent>
        </Card>
      ) : (
        <>
          {/* Key Financial Metrics */}
          <div className="grid grid-cols-2 lg:grid-cols-4 gap-4 mt-6">
            <StatCard
              title="Outstanding AR"
              value={formatCents(overview.outstanding_ar)}
              subtitle="Unpaid invoices"
              valueClass={overview.outstanding_ar > 0 ? 'text-amber-600' : undefined}
            />
            <StatCard
              title="Avg Invoice Value"
              value={formatCents(overview.avg_invoice_value)}
            />
            <StatCard
              title="Credit Balance"
              value={formatCents(overview.credit_balance_total)}
              subtitle="Total across all customers"
            />
            <StatCard
              title="Open Invoices"
              value={String(overview.open_invoices)}
            />
          </div>

          <Separator className="mt-6" />

          {/* Period Tabs + Revenue Trend — full width */}
          <Card className="mt-6">
            <CardContent className="p-5">
              <div className="flex items-center justify-between mb-4">
                <div>
                  <h2 className="text-sm font-semibold text-foreground">Revenue Trend</h2>
                  <p className="text-xs text-muted-foreground mt-0.5">
                    Total revenue: <span className="tabular-nums font-mono">{formatCents(overview.total_revenue)}</span>
                  </p>
                </div>
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
                const fmtShortDate = (d: string) => {
                  const dt = new Date(d)
                  return dt.toLocaleDateString('en-US', { month: 'short', day: 'numeric' })
                }
                const fmtCompactAmount = (v: number) => {
                  if (v === 0) return '$0'
                  if (v >= 100000) return `$${Math.round(v / 100000)}K`
                  if (v >= 10000) return `$${(v / 100).toFixed(0)}`
                  return formatCents(v)
                }
                return (
                  <ResponsiveContainer width="100%" height={280}>
                    <AreaChart data={chartData} margin={{ top: 5, right: 5, left: 5, bottom: 5 }}>
                      <defs>
                        <linearGradient id="revGradient" x1="0" y1="0" x2="0" y2="1">
                          <stop offset="0%" stopColor={COLORS.purple} stopOpacity={0.3} />
                          <stop offset="100%" stopColor={COLORS.purple} stopOpacity={0.05} />
                        </linearGradient>
                      </defs>
                      <CartesianGrid strokeDasharray="3 3" stroke={theme.grid} vertical={false} />
                      <XAxis
                        dataKey="date"
                        tickFormatter={fmtShortDate}
                        tick={{ fontSize: 11, fill: theme.tick }}
                        axisLine={{ stroke: theme.grid }}
                        tickLine={false}
                      />
                      <YAxis
                        tickFormatter={fmtCompactAmount}
                        tick={{ fontSize: 11, fill: theme.tick }}
                        width={60}
                        axisLine={false}
                        tickLine={false}
                      />
                      <Tooltip
                        formatter={(value) => [formatCents(Number(value)), 'Revenue']}
                        labelFormatter={label => fmtShortDate(String(label))}
                        contentStyle={{
                          backgroundColor: '#18181b',
                          border: '1px solid rgba(255,255,255,0.1)',
                          borderRadius: '8px',
                          fontSize: '13px',
                          color: '#fafafa',
                        }}
                        itemStyle={{ color: '#fafafa' }}
                        labelStyle={{ color: '#a1a1aa', fontSize: '11px', marginBottom: '2px' }}
                      />
                      <Area
                        type="monotone"
                        dataKey="revenue_cents"
                        stroke={COLORS.purple}
                        strokeWidth={2}
                        fill="url(#revGradient)"
                        dot={false}
                        activeDot={{ r: 4, stroke: COLORS.purple, fill: '#fff', strokeWidth: 2 }}
                      />
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

          <Separator className="mt-6" />

          {/* Middle Row: Payment Success (donut) + Invoice Summary (bar) */}
          <div className="grid grid-cols-1 md:grid-cols-2 gap-4 mt-6">
            {/* Payment Success Rate Donut */}
            <Card>
              <CardContent className="p-5">
                <h2 className="text-sm font-semibold text-foreground mb-1">Payment Success Rate</h2>
                <p className="text-xs text-muted-foreground mb-4">Last 30 days</p>
                {totalPayments > 0 ? (
                  <div className="flex flex-col items-center">
                    <div className="relative">
                      <ResponsiveContainer width={200} height={200}>
                        <PieChart>
                          <Pie
                            data={donutData}
                            cx="50%"
                            cy="50%"
                            innerRadius={60}
                            outerRadius={80}
                            dataKey="value"
                            startAngle={90}
                            endAngle={-270}
                            stroke="none"
                          >
                            {donutData.map((d, i) => (
                              <Cell key={i} fill={d.color} />
                            ))}
                          </Pie>
                          <Tooltip
                            formatter={(value, name) => [value, name]}
                            contentStyle={{
                              backgroundColor: '#18181b',
                              border: '1px solid rgba(255,255,255,0.1)',
                              borderRadius: '8px',
                              fontSize: '13px',
                              color: '#fafafa',
                            }}
                            itemStyle={{ color: '#fafafa' }}
                          />
                        </PieChart>
                      </ResponsiveContainer>
                      {/* Center label */}
                      <div className="absolute inset-0 flex items-center justify-center pointer-events-none">
                        <div className="text-center">
                          <p className="text-3xl font-bold tabular-nums font-mono text-foreground">{successRate}%</p>
                          <p className="text-[10px] text-muted-foreground uppercase tracking-wider">success</p>
                        </div>
                      </div>
                    </div>
                    <div className="flex items-center gap-6 mt-2">
                      <div className="flex items-center gap-2">
                        <span className="w-2.5 h-2.5 rounded-full" style={{ backgroundColor: COLORS.emerald }} />
                        <span className="text-sm text-muted-foreground">Paid <span className="font-mono tabular-nums font-medium text-foreground">{paidCount}</span></span>
                      </div>
                      <div className="flex items-center gap-2">
                        <span className="w-2.5 h-2.5 rounded-full" style={{ backgroundColor: COLORS.red }} />
                        <span className="text-sm text-muted-foreground">Failed <span className="font-mono tabular-nums font-medium text-foreground">{failedCount}</span></span>
                      </div>
                    </div>
                  </div>
                ) : (
                  <div className="h-[200px] flex items-center justify-center text-sm text-muted-foreground">
                    No payment data yet
                  </div>
                )}
              </CardContent>
            </Card>

            {/* Invoice Summary Bar Chart */}
            <Card>
              <CardContent className="p-5">
                <h2 className="text-sm font-semibold text-foreground mb-1">Invoice Summary</h2>
                <p className="text-xs text-muted-foreground mb-4">Current invoice status breakdown</p>
                {invoiceSummaryData.some(d => d.count > 0) ? (() => {
                  const theme = chartTheme()
                  return (
                    <ResponsiveContainer width="100%" height={220}>
                      <BarChart data={invoiceSummaryData} margin={{ top: 5, right: 5, left: 5, bottom: 5 }}>
                        <CartesianGrid strokeDasharray="3 3" stroke={theme.grid} vertical={false} />
                        <XAxis dataKey="name" tick={{ fontSize: 11, fill: theme.tick }} axisLine={{ stroke: theme.grid }} tickLine={false} />
                        <YAxis allowDecimals={false} tick={{ fontSize: 11, fill: theme.tick }} width={40} axisLine={false} tickLine={false} />
                        <Tooltip
                          formatter={(value) => [value, 'Invoices']}
                          contentStyle={{
                            backgroundColor: '#18181b',
                            border: '1px solid rgba(255,255,255,0.1)',
                            borderRadius: '8px',
                            fontSize: '13px',
                            color: '#fafafa',
                          }}
                          itemStyle={{ color: '#fafafa' }}
                        />
                        <Bar dataKey="count" radius={[4, 4, 0, 0]}>
                          {invoiceSummaryData.map((entry, i) => (
                            <Cell key={i} fill={entry.fill} />
                          ))}
                        </Bar>
                      </BarChart>
                    </ResponsiveContainer>
                  )
                })() : (
                  <div className="h-[220px] flex items-center justify-center text-sm text-muted-foreground">
                    No invoice data yet
                  </div>
                )}
              </CardContent>
            </Card>
          </div>

          <Separator className="mt-6" />

          {/* Bottom Row: Customer Stats + Revenue Breakdown */}
          <div className="grid grid-cols-1 md:grid-cols-2 gap-4 mt-6">
            {/* Customer Stats */}
            <Card>
              <CardContent className="p-5">
                <h2 className="text-sm font-semibold text-foreground mb-1">Customer Stats</h2>
                <p className="text-xs text-muted-foreground mb-4">Current customer and subscription counts</p>
                <div className="space-y-4 mt-2">
                  <div className="flex items-center justify-between">
                    <span className="text-sm text-muted-foreground">Active Customers</span>
                    <span className="text-2xl font-semibold tabular-nums text-foreground">{overview.active_customers}</span>
                  </div>
                  <Separator />
                  <div className="flex items-center justify-between">
                    <span className="text-sm text-muted-foreground">Active Subscriptions</span>
                    <span className="text-2xl font-semibold tabular-nums text-foreground">{overview.active_subscriptions}</span>
                  </div>
                  <Separator />
                  <div className="flex items-center justify-between">
                    <span className="text-sm text-muted-foreground">Dunning Active</span>
                    <span className={cn('text-xl font-semibold tabular-nums', overview.dunning_active > 0 ? 'text-amber-600' : 'text-foreground')}>{overview.dunning_active}</span>
                  </div>
                  <Separator />
                  <div className="flex items-center justify-between">
                    <span className="text-sm text-muted-foreground">Open Invoices</span>
                    <span className="text-xl font-semibold tabular-nums text-foreground">{overview.open_invoices}</span>
                  </div>
                </div>
              </CardContent>
            </Card>

            {/* Revenue Breakdown */}
            <Card>
              <CardContent className="p-5">
                <h2 className="text-sm font-semibold text-foreground mb-1">Revenue Breakdown</h2>
                <p className="text-xs text-muted-foreground mb-4">Key financial metrics</p>
                <div className="space-y-3">
                  <div className="flex items-center justify-between py-1">
                    <span className="flex items-center gap-2 text-sm text-muted-foreground">
                      <span className="w-1.5 h-1.5 rounded-full bg-primary shrink-0" />
                      Monthly Recurring Revenue
                    </span>
                    <span className="text-sm font-semibold tabular-nums font-mono">{formatCents(overview.mrr)}</span>
                  </div>
                  <div className="flex items-center justify-between py-1">
                    <span className="flex items-center gap-2 text-sm text-muted-foreground">
                      <span className="w-1.5 h-1.5 rounded-full bg-emerald-500 shrink-0" />
                      Total Revenue
                    </span>
                    <span className="text-sm font-semibold tabular-nums font-mono">{formatCents(overview.total_revenue)}</span>
                  </div>
                  <div className="flex items-center justify-between py-1">
                    <span className="flex items-center gap-2 text-sm text-muted-foreground">
                      <span className="w-1.5 h-1.5 rounded-full bg-amber-500 shrink-0" />
                      Outstanding AR
                    </span>
                    <span className="text-sm font-semibold tabular-nums font-mono text-amber-600 dark:text-amber-400">
                      {formatCents(overview.outstanding_ar)}
                    </span>
                  </div>
                  <div className="flex items-center justify-between py-1">
                    <span className="flex items-center gap-2 text-sm text-muted-foreground">
                      <span className="w-1.5 h-1.5 rounded-full bg-blue-500 shrink-0" />
                      Average Invoice Value
                    </span>
                    <span className="text-sm font-semibold tabular-nums font-mono">{formatCents(overview.avg_invoice_value)}</span>
                  </div>
                  <div className="flex items-center justify-between py-1">
                    <span className="flex items-center gap-2 text-sm text-muted-foreground">
                      <span className="w-1.5 h-1.5 rounded-full bg-slate-400 shrink-0" />
                      Credit Balance (all customers)
                    </span>
                    <span className="text-sm font-semibold tabular-nums font-mono">{formatCents(overview.credit_balance_total)}</span>
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
