import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api, formatCents, formatDate } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Card, CardContent } from '@/components/ui/card'
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
          {/* Period Tabs + Revenue Trend */}
          <Card className="mt-6">
            <CardContent className="p-6">
              <div className="flex items-center justify-between mb-4">
                <div>
                  <h2 className="text-sm font-semibold text-foreground">Revenue Trend</h2>
                  <p className="text-xs text-muted-foreground mt-0.5">
                    Total revenue: {formatCents(overview.total_revenue)}
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
                return (
                  <ResponsiveContainer width="100%" height={300}>
                    <AreaChart data={chartData} margin={{ top: 5, right: 5, left: 5, bottom: 5 }}>
                      <defs>
                        <linearGradient id="revGradient" x1="0" y1="0" x2="0" y2="1">
                          <stop offset="0%" stopColor={COLORS.purple} stopOpacity={0.3} />
                          <stop offset="100%" stopColor={COLORS.purple} stopOpacity={0.05} />
                        </linearGradient>
                      </defs>
                      <CartesianGrid strokeDasharray="3 3" stroke={theme.grid} />
                      <XAxis
                        dataKey="date"
                        tickFormatter={d => formatDate(d)}
                        tick={{ fontSize: 12, fill: theme.tick }}
                      />
                      <YAxis
                        tickFormatter={v => formatCents(v)}
                        tick={{ fontSize: 12, fill: theme.tick }}
                        width={80}
                      />
                      <Tooltip
                        formatter={(value) => [formatCents(Number(value)), 'Revenue']}
                        labelFormatter={label => formatDate(String(label))}
                        contentStyle={{
                          backgroundColor: theme.tooltipBg,
                          border: `1px solid ${theme.tooltipBorder}`,
                          borderRadius: '8px',
                          fontSize: '13px',
                          color: theme.tooltipColor,
                        }}
                      />
                      <Area
                        type="monotone"
                        dataKey="revenue_cents"
                        stroke={COLORS.purple}
                        strokeWidth={2}
                        fill="url(#revGradient)"
                        dot={false}
                      />
                    </AreaChart>
                  </ResponsiveContainer>
                )
              })() : (
                <div className="h-[300px] flex items-center justify-center text-sm text-muted-foreground">
                  No revenue data for this period
                </div>
              )}
            </CardContent>
          </Card>

          {/* Middle Row: Revenue Breakdown + Payment Success */}
          <div className="grid grid-cols-1 md:grid-cols-2 gap-4 mt-6">
            {/* Revenue Breakdown */}
            <Card>
              <CardContent className="p-6">
                <h2 className="text-sm font-semibold text-foreground mb-1">Revenue Breakdown</h2>
                <p className="text-xs text-muted-foreground mb-4">Key financial metrics</p>
                <div className="space-y-4">
                  <div className="flex items-center justify-between">
                    <span className="text-sm text-muted-foreground">Monthly Recurring Revenue</span>
                    <span className="text-sm font-semibold tabular-nums">{formatCents(overview.mrr)}</span>
                  </div>
                  <div className="flex items-center justify-between">
                    <span className="text-sm text-muted-foreground">Total Revenue</span>
                    <span className="text-sm font-semibold tabular-nums">{formatCents(overview.total_revenue)}</span>
                  </div>
                  <div className="flex items-center justify-between">
                    <span className="text-sm text-muted-foreground">Outstanding AR</span>
                    <span className="text-sm font-semibold tabular-nums text-amber-600 dark:text-amber-400">
                      {formatCents(overview.outstanding_ar)}
                    </span>
                  </div>
                  <div className="flex items-center justify-between">
                    <span className="text-sm text-muted-foreground">Average Invoice Value</span>
                    <span className="text-sm font-semibold tabular-nums">{formatCents(overview.avg_invoice_value)}</span>
                  </div>
                  <div className="flex items-center justify-between">
                    <span className="text-sm text-muted-foreground">Credit Balance (all customers)</span>
                    <span className="text-sm font-semibold tabular-nums">{formatCents(overview.credit_balance_total)}</span>
                  </div>
                </div>
              </CardContent>
            </Card>

            {/* Payment Success Rate Donut */}
            <Card>
              <CardContent className="p-6">
                <h2 className="text-sm font-semibold text-foreground mb-1">Payment Success Rate</h2>
                <p className="text-xs text-muted-foreground mb-4">Last 30 days</p>
                {totalPayments > 0 ? (
                  <div className="flex items-center justify-center">
                    <div className="relative">
                      <ResponsiveContainer width={180} height={180}>
                        <PieChart>
                          <Pie
                            data={donutData}
                            cx="50%"
                            cy="50%"
                            innerRadius={55}
                            outerRadius={75}
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
                              backgroundColor: chartTheme().tooltipBg,
                              border: `1px solid ${chartTheme().tooltipBorder}`,
                              borderRadius: '8px',
                              fontSize: '13px',
                              color: chartTheme().tooltipColor,
                            }}
                          />
                        </PieChart>
                      </ResponsiveContainer>
                      {/* Center label */}
                      <div className="absolute inset-0 flex items-center justify-center pointer-events-none">
                        <div className="text-center">
                          <p className="text-2xl font-bold tabular-nums text-foreground">{successRate}%</p>
                          <p className="text-[10px] text-muted-foreground uppercase tracking-wider">success</p>
                        </div>
                      </div>
                    </div>
                    <div className="ml-4 space-y-2">
                      <div className="flex items-center gap-2">
                        <span className="w-3 h-3 rounded-full" style={{ backgroundColor: COLORS.emerald }} />
                        <span className="text-sm text-muted-foreground">Paid: {paidCount}</span>
                      </div>
                      <div className="flex items-center gap-2">
                        <span className="w-3 h-3 rounded-full" style={{ backgroundColor: COLORS.red }} />
                        <span className="text-sm text-muted-foreground">Failed: {failedCount}</span>
                      </div>
                    </div>
                  </div>
                ) : (
                  <div className="h-[180px] flex items-center justify-center text-sm text-muted-foreground">
                    No payment data yet
                  </div>
                )}
              </CardContent>
            </Card>
          </div>

          {/* Bottom Row: Invoice Summary + Customer Growth */}
          <div className="grid grid-cols-1 md:grid-cols-2 gap-4 mt-6">
            {/* Invoice Summary Bar Chart */}
            <Card>
              <CardContent className="p-6">
                <h2 className="text-sm font-semibold text-foreground mb-1">Invoice Summary</h2>
                <p className="text-xs text-muted-foreground mb-4">Current invoice status breakdown</p>
                {invoiceSummaryData.some(d => d.count > 0) ? (() => {
                  const theme = chartTheme()
                  return (
                    <ResponsiveContainer width="100%" height={200}>
                      <BarChart data={invoiceSummaryData} margin={{ top: 5, right: 5, left: 5, bottom: 5 }}>
                        <CartesianGrid strokeDasharray="3 3" stroke={theme.grid} />
                        <XAxis dataKey="name" tick={{ fontSize: 12, fill: theme.tick }} />
                        <YAxis allowDecimals={false} tick={{ fontSize: 12, fill: theme.tick }} width={40} />
                        <Tooltip
                          formatter={(value) => [value, 'Invoices']}
                          contentStyle={{
                            backgroundColor: theme.tooltipBg,
                            border: `1px solid ${theme.tooltipBorder}`,
                            borderRadius: '8px',
                            fontSize: '13px',
                            color: theme.tooltipColor,
                          }}
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
                  <div className="h-[200px] flex items-center justify-center text-sm text-muted-foreground">
                    No invoice data yet
                  </div>
                )}
              </CardContent>
            </Card>

            {/* Customer Growth */}
            <Card>
              <CardContent className="p-6">
                <h2 className="text-sm font-semibold text-foreground mb-1">Customer Growth</h2>
                <p className="text-xs text-muted-foreground mb-4">Current customer and subscription counts</p>
                <div className="space-y-5 mt-2">
                  <div>
                    <p className="text-xs uppercase tracking-wider text-muted-foreground">Active Customers</p>
                    <p className="text-3xl font-bold tabular-nums text-foreground mt-1">{overview.active_customers}</p>
                  </div>
                  <div className="h-px bg-border" />
                  <div>
                    <p className="text-xs uppercase tracking-wider text-muted-foreground">Active Subscriptions</p>
                    <p className="text-3xl font-bold tabular-nums text-foreground mt-1">{overview.active_subscriptions}</p>
                  </div>
                  <div className="h-px bg-border" />
                  <div className="flex gap-8">
                    <div>
                      <p className="text-xs uppercase tracking-wider text-muted-foreground">Dunning Active</p>
                      <p className="text-xl font-semibold tabular-nums text-foreground mt-1">
                        {overview.dunning_active}
                      </p>
                    </div>
                    <div>
                      <p className="text-xs uppercase tracking-wider text-muted-foreground">Open Invoices</p>
                      <p className="text-xl font-semibold tabular-nums text-foreground mt-1">
                        {overview.open_invoices}
                      </p>
                    </div>
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
