import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api, formatCents, formatDate } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { cn } from '@/lib/utils'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { AlertTriangle, Wallet, ChevronDown, ChevronUp, Loader2, Zap } from 'lucide-react'
import { BarChart, Bar, XAxis, YAxis, Tooltip, ResponsiveContainer, CartesianGrid } from 'recharts'

type Period = '30d' | '90d' | '12m'

const periodLabels: Record<Period, string> = {
  '30d': 'Last 30 days',
  '90d': 'Last 90 days',
  '12m': 'Last 12 months',
}

function StatCard({ title, value, subtitle }: { title: string; value: string; subtitle?: string }) {
  return (
    <Card>
      <CardContent className="pt-6">
        <p className="text-sm font-medium text-muted-foreground">{title}</p>
        <p className="text-2xl font-semibold text-foreground mt-1">{value}</p>
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
          {/* KPI Cards */}
          <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4 mt-6">
            <StatCard title="MRR" value={formatCents(overview.mrr)} subtitle="Monthly recurring revenue" />
            <StatCard title="Active Customers" value={String(overview.active_customers)} />
            <StatCard title="Outstanding AR" value={formatCents(overview.outstanding_ar)} subtitle="Unpaid invoices" />
            <StatCard title="Paid Invoices (30d)" value={String(overview.paid_invoices_30d)} subtitle={`${overview.failed_payments_30d} failed`} />
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

          {/* Dunning & Credits row */}
          <div className="grid grid-cols-1 lg:grid-cols-2 gap-6 mt-6">
            <Link to="/dunning?tab=runs" className="block">
              <Card className="hover:shadow-md transition-shadow h-full">
                <CardContent className="p-6">
                  <div className="flex items-center gap-3">
                    <div className={cn(
                      'w-10 h-10 rounded-lg flex items-center justify-center',
                      overview.dunning_active > 0
                        ? 'bg-amber-50 text-amber-500 dark:bg-amber-950 dark:text-amber-400'
                        : 'bg-muted text-muted-foreground'
                    )}>
                      <AlertTriangle size={18} />
                    </div>
                    <div>
                      <p className="text-sm font-medium text-muted-foreground">Active Dunning</p>
                      <p className="text-2xl font-semibold text-foreground">{overview.dunning_active}</p>
                    </div>
                  </div>
                  {overview.dunning_active > 0 && (
                    <p className="text-xs text-amber-600 dark:text-amber-400 mt-3">
                      {overview.dunning_active} customer{overview.dunning_active !== 1 ? 's' : ''} with failed payments requiring attention
                    </p>
                  )}
                </CardContent>
              </Card>
            </Link>

            <Link to="/credits" className="block">
              <Card className="hover:shadow-md transition-shadow h-full">
                <CardContent className="p-6">
                  <div className="flex items-center gap-3">
                    <div className="w-10 h-10 rounded-lg bg-emerald-50 text-emerald-500 dark:bg-emerald-950 dark:text-emerald-400 flex items-center justify-center">
                      <Wallet size={18} />
                    </div>
                    <div>
                      <p className="text-sm font-medium text-muted-foreground">Credit Balance</p>
                      <p className="text-2xl font-semibold text-foreground">{formatCents(overview.credit_balance_total)}</p>
                    </div>
                  </div>
                  <p className="text-xs text-muted-foreground mt-3">
                    Total outstanding credits across all customers
                  </p>
                </CardContent>
              </Card>
            </Link>
          </div>

          {/* Additional stats */}
          <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4 mt-6">
            <StatCard title="Active Subscriptions" value={String(overview.active_subscriptions)} />
            <StatCard title="Total Revenue" value={formatCents(overview.total_revenue)} subtitle="All-time paid invoices" />
            <StatCard title="Avg Invoice Value" value={formatCents(overview.avg_invoice_value)} />
          </div>

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
