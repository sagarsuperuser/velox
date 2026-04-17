import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api, formatCents, formatDate } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { cn } from '@/lib/utils'
import { Card, CardContent } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { ChevronDown, ChevronUp, Loader2, Zap } from 'lucide-react'
import { BarChart, Bar, XAxis, YAxis, Tooltip, ResponsiveContainer, CartesianGrid, AreaChart, Area } from 'recharts'

type Period = '30d' | '90d' | '12m'

const periodLabels: Record<Period, string> = {
  '30d': 'Last 30 days',
  '90d': 'Last 90 days',
  '12m': 'Last 12 months',
}

function StatCard({ title, value, subtitle, valueClass }: { title: string; value: string; subtitle?: string; valueClass?: string }) {
  return (
    <Card>
      <CardContent className="pt-6">
        <p className="text-xs uppercase tracking-wider text-muted-foreground">{title}</p>
        <p className={cn('text-xl font-semibold tabular-nums mt-1', valueClass ?? 'text-foreground')}>{value}</p>
        {subtitle && <p className="text-xs text-muted-foreground mt-1">{subtitle}</p>}
      </CardContent>
    </Card>
  )
}

export default function DashboardPage() {
  const [period, setPeriod] = useState<Period>('30d')
  const [billingResult, setBillingResult] = useState<string | null>(null)
  const [getStartedOpen, setGetStartedOpen] = useState(true)
  const queryClient = useQueryClient()

  const { data: overview, isLoading: overviewLoading, error: overviewError, refetch: refetchOverview } = useQuery({
    queryKey: ['dashboard-overview'],
    queryFn: () => api.getAnalyticsOverview(),
  })

  const { data: chartRes } = useQuery({
    queryKey: ['dashboard-chart', period],
    queryFn: () => api.getRevenueChart(period),
  })

  const chartData = chartRes?.data ?? []
  const loading = overviewLoading
  const error = overviewError instanceof Error ? overviewError.message : overviewError ? String(overviewError) : null

  const billingMutation = useMutation({
    mutationFn: () => api.triggerBilling(),
    onSuccess: (res) => {
      const now = new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
      setBillingResult(`${res.invoices_generated} invoice(s) generated at ${now}`)
      queryClient.invalidateQueries({ queryKey: ['dashboard-overview'] })
      queryClient.invalidateQueries({ queryKey: ['dashboard-chart'] })
    },
    onError: (err) => {
      setBillingResult('Failed: ' + (err instanceof Error ? err.message : 'unknown error'))
    },
  })

  const handleTriggerBilling = () => {
    setBillingResult(null)
    billingMutation.mutate()
  }

  return (
    <Layout>
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold text-foreground">Dashboard</h1>
          <p className="text-sm text-muted-foreground mt-1">Billing analytics overview</p>
        </div>
        <div className="flex items-center gap-3">
          {billingResult && (
            <span className="text-xs text-muted-foreground">{billingResult}</span>
          )}
          <Button
            onClick={handleTriggerBilling}
            disabled={billingMutation.isPending}
            variant="outline"
          >
            {billingMutation.isPending ? (
              <>
                <Loader2 size={16} className="animate-spin mr-2" />
                Running cycle...
              </>
            ) : (
              <>
                <Zap size={16} className="mr-2" />
                Trigger Billing Cycle
              </>
            )}
          </Button>
        </div>
      </div>

      {error ? (
        <Card className="mt-6">
          <CardContent className="p-8 text-center">
            <p className="text-sm text-destructive mb-3">{error}</p>
            <Button variant="outline" size="sm" onClick={() => refetchOverview()}>Retry</Button>
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
          {/* Hero MRR Section */}
          <Card className="mt-6">
            <CardContent className="p-6">
              <div className="flex flex-col md:flex-row md:items-start md:justify-between gap-4">
                <div className="flex-1">
                  <p className="text-xs uppercase tracking-wider text-muted-foreground">Monthly Recurring Revenue</p>
                  <div className="flex items-baseline gap-3 mt-1">
                    <p className="text-4xl font-bold tabular-nums text-foreground">{formatCents(overview.mrr)}</p>
                    {chartData.length >= 2 && (() => {
                      const recent = chartData[chartData.length - 1].revenue_cents
                      const prev = chartData[chartData.length - 2].revenue_cents
                      if (prev === 0) return null
                      const pct = Math.round(((recent - prev) / prev) * 100)
                      return (
                        <span className={cn(
                          'text-sm font-medium',
                          pct >= 0 ? 'text-emerald-600 dark:text-emerald-400' : 'text-destructive'
                        )}>
                          {pct >= 0 ? '\u25B2' : '\u25BC'} {Math.abs(pct)}% vs last period
                        </span>
                      )
                    })()}
                  </div>
                  {/* Mini sparkline */}
                  {chartData.length > 1 && (
                    <div className="mt-3 w-48 h-[40px]">
                      <ResponsiveContainer width="100%" height={40}>
                        <AreaChart data={chartData} margin={{ top: 0, right: 0, left: 0, bottom: 0 }}>
                          <defs>
                            <linearGradient id="sparkFill" x1="0" y1="0" x2="0" y2="1">
                              <stop offset="0%" stopColor="#635BFF" stopOpacity={0.3} />
                              <stop offset="100%" stopColor="#635BFF" stopOpacity={0.05} />
                            </linearGradient>
                          </defs>
                          <Area type="monotone" dataKey="revenue_cents" stroke="#635BFF" strokeWidth={2} fill="url(#sparkFill)" dot={false} />
                        </AreaChart>
                      </ResponsiveContainer>
                    </div>
                  )}
                </div>
              </div>
              <div className="grid grid-cols-3 gap-6 mt-6 pt-5 border-t border-border">
                <div>
                  <p className="text-xs uppercase tracking-wider text-muted-foreground">Active Customers</p>
                  <p className="text-2xl font-semibold tabular-nums text-foreground mt-1">{overview.active_customers}</p>
                </div>
                <div>
                  <p className="text-xs uppercase tracking-wider text-muted-foreground">Active Subscriptions</p>
                  <p className="text-2xl font-semibold tabular-nums text-foreground mt-1">{overview.active_subscriptions}</p>
                </div>
                <div>
                  <p className="text-xs uppercase tracking-wider text-muted-foreground">Paid Invoices (30d)</p>
                  <p className="text-2xl font-semibold tabular-nums text-foreground mt-1">{overview.paid_invoices_30d}</p>
                </div>
              </div>
            </CardContent>
          </Card>

          {/* Secondary Stats Grid */}
          <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4 mt-6">
            <StatCard
              title="Outstanding AR"
              value={formatCents(overview.outstanding_ar)}
              subtitle="Unpaid invoices"
              valueClass={overview.outstanding_ar > 0 ? 'text-amber-600 dark:text-amber-400' : undefined}
            />
            <StatCard title="Open Invoices" value={String(overview.open_invoices)} />
            <StatCard
              title="Failed Payments (30d)"
              value={String(overview.failed_payments_30d)}
              valueClass={overview.failed_payments_30d > 0 ? 'text-destructive' : undefined}
            />
            <StatCard
              title="Dunning Active"
              value={String(overview.dunning_active)}
              valueClass={overview.dunning_active > 0 ? 'text-destructive' : undefined}
            />
            <StatCard title="Credit Balance" value={formatCents(overview.credit_balance_total)} subtitle="Total across all customers" />
            <StatCard title="Avg Invoice Value" value={formatCents(overview.avg_invoice_value)} />
          </div>

          {/* Revenue Chart */}
          <Card className="mt-6">
            <CardContent className="p-6">
              <div className="flex items-center justify-between mb-4">
                <div>
                  <h2 className="text-sm font-semibold text-foreground">Revenue Over Time</h2>
                  <p className="text-xs text-muted-foreground mt-0.5">
                    Total revenue: {formatCents(overview.total_revenue)}
                  </p>
                </div>
                <div className="flex gap-1 bg-muted rounded-lg p-0.5">
                  {(Object.keys(periodLabels) as Period[]).map(p => (
                    <button
                      key={p}
                      onClick={() => setPeriod(p)}
                      className={cn(
                        'px-3 py-1.5 text-xs font-medium rounded-md transition-colors',
                        period === p
                          ? 'bg-background text-foreground shadow-sm'
                          : 'text-muted-foreground hover:text-foreground'
                      )}
                    >
                      {periodLabels[p]}
                    </button>
                  ))}
                </div>
              </div>

              {chartData.length > 0 ? (() => {
                const isDark = document.documentElement.classList.contains('dark')
                const gridStroke = isDark ? 'rgba(255,255,255,0.08)' : 'rgba(0,0,0,0.08)'
                const tickColor = isDark ? '#a1a1aa' : '#71717a'
                const tooltipBg = isDark ? '#27272a' : '#ffffff'
                const tooltipBorder = isDark ? 'rgba(255,255,255,0.1)' : 'rgba(0,0,0,0.08)'
                const tooltipColor = isDark ? '#fafafa' : '#09090b'
                return (
                <ResponsiveContainer width="100%" height={280}>
                  <BarChart data={chartData} margin={{ top: 5, right: 5, left: 5, bottom: 5 }}>
                    <CartesianGrid strokeDasharray="3 3" stroke={gridStroke} />
                    <XAxis
                      dataKey="date"
                      tickFormatter={(d) => formatDate(d)}
                      tick={{ fontSize: 12, fill: tickColor }}
                    />
                    <YAxis
                      tickFormatter={(v) => formatCents(v)}
                      tick={{ fontSize: 12, fill: tickColor }}
                      width={80}
                    />
                    <Tooltip
                      formatter={(value) => [formatCents(Number(value)), 'Revenue']}
                      labelFormatter={(label) => formatDate(String(label))}
                      contentStyle={{
                        backgroundColor: tooltipBg,
                        border: `1px solid ${tooltipBorder}`,
                        borderRadius: '8px',
                        fontSize: '13px',
                        color: tooltipColor,
                      }}
                    />
                    <Bar dataKey="revenue_cents" fill="#635BFF" radius={[4, 4, 0, 0]} />
                  </BarChart>
                </ResponsiveContainer>
                )
              })() : (
                <div className="h-48 flex items-center justify-center text-sm text-muted-foreground">
                  No revenue data for this period
                </div>
              )}
            </CardContent>
          </Card>

          {/* Get Started */}
          {(() => {
            const steps = [
              { done: overview.active_subscriptions > 0 || overview.total_revenue > 0, label: 'Configure pricing', desc: 'create meters, rating rules, and plans', to: '/pricing' },
              { done: overview.active_customers > 0, label: 'Add customers', desc: 'create your first customer', to: '/customers' },
              { done: overview.active_subscriptions > 0, label: 'Create subscriptions', desc: 'subscribe customers to plans', to: '/subscriptions' },
              { done: overview.total_revenue > 0, label: 'Generate revenue', desc: 'ingest usage events and run billing', to: undefined },
            ]
            const completedCount = steps.filter(s => s.done).length
            if (completedCount >= steps.length) return null
            return (
              <Card className="mt-6 bg-accent/30">
                <button
                  onClick={() => setGetStartedOpen(!getStartedOpen)}
                  className="w-full px-6 py-4 flex items-center justify-between text-left"
                >
                  <div>
                    <h3 className="text-sm font-semibold text-foreground">Get Started</h3>
                    <p className="text-sm text-muted-foreground mt-0.5">
                      {completedCount} of {steps.length} steps completed
                    </p>
                  </div>
                  <div className="flex items-center gap-3">
                    <div className="w-24 h-1.5 bg-border rounded-full overflow-hidden">
                      <div
                        className="h-full bg-primary rounded-full transition-all"
                        style={{ width: `${(completedCount / steps.length) * 100}%` }}
                      />
                    </div>
                    {getStartedOpen ? <ChevronUp size={16} /> : <ChevronDown size={16} />}
                  </div>
                </button>
                {getStartedOpen && (
                  <CardContent className="pb-6 pt-0">
                    <ol className="space-y-2 text-sm">
                      {steps.map((step, i) => (
                        <li key={i} className="flex items-center gap-3">
                          {step.done ? (
                            <span className="w-5 h-5 rounded-full bg-emerald-500 text-white flex items-center justify-center shrink-0">
                              <svg className="w-3 h-3" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                                <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={3} d="M5 13l4 4L19 7" />
                              </svg>
                            </span>
                          ) : (
                            <span className="w-5 h-5 rounded-full bg-primary text-primary-foreground flex items-center justify-center text-xs font-bold shrink-0">
                              {i + 1}
                            </span>
                          )}
                          <span className={step.done ? 'text-muted-foreground line-through' : 'text-foreground'}>
                            {step.to ? (
                              <Link to={step.to} className="font-medium hover:underline">{step.label}</Link>
                            ) : (
                              <span className="font-medium">{step.label}</span>
                            )}
                            {' '}
                            <span className="text-muted-foreground">-- {step.desc}</span>
                          </span>
                        </li>
                      ))}
                    </ol>
                  </CardContent>
                )}
              </Card>
            )
          })()}
        </>
      )}
    </Layout>
  )
}
