import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api, formatCents } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { cn } from '@/lib/utils'
import { Card, CardContent } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { ChevronDown, ChevronUp, Loader2, Zap, Check, ArrowRight, Rocket, Tag, Users as UsersIcon, CreditCard, BarChart3 } from 'lucide-react'
import { AreaChart, Area, ResponsiveContainer } from 'recharts'

export default function DashboardPage() {
  const [billingResult, setBillingResult] = useState<string | null>(null)
  const [getStartedOpen, setGetStartedOpen] = useState(true)
  const queryClient = useQueryClient()

  const { data: overview, isLoading: overviewLoading, error: overviewError, refetch: refetchOverview } = useQuery({
    queryKey: ['dashboard-overview'],
    queryFn: () => api.getAnalyticsOverview(),
  })

  const { data: chartRes } = useQuery({
    queryKey: ['dashboard-chart', '30d'],
    queryFn: () => api.getRevenueChart('30d'),
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
          <p className="text-sm text-muted-foreground mt-1">Operational overview</p>
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

          {/* Attention Items */}
          {(overview.failed_payments_30d > 0 || overview.dunning_active > 0 || overview.open_invoices > 0) && (
            <Card className="mt-6">
              <CardContent className="p-0 divide-y divide-border">
                {overview.failed_payments_30d > 0 && (
                  <Link to="/invoices?payment_status=failed" className="flex items-center justify-between px-5 py-3 hover:bg-muted/50 transition-colors">
                    <div className="flex items-center gap-3">
                      <div className="w-2 h-2 rounded-full bg-destructive" />
                      <span className="text-sm text-foreground">{overview.failed_payments_30d} failed payment{overview.failed_payments_30d > 1 ? 's' : ''} in the last 30 days</span>
                    </div>
                    <ArrowRight size={14} className="text-muted-foreground" />
                  </Link>
                )}
                {overview.dunning_active > 0 && (
                  <Link to="/dunning?tab=runs" className="flex items-center justify-between px-5 py-3 hover:bg-muted/50 transition-colors">
                    <div className="flex items-center gap-3">
                      <div className="w-2 h-2 rounded-full bg-amber-500" />
                      <span className="text-sm text-foreground">{overview.dunning_active} active dunning run{overview.dunning_active > 1 ? 's' : ''}</span>
                    </div>
                    <ArrowRight size={14} className="text-muted-foreground" />
                  </Link>
                )}
                {overview.open_invoices > 0 && (
                  <Link to="/invoices?status=finalized" className="flex items-center justify-between px-5 py-3 hover:bg-muted/50 transition-colors">
                    <div className="flex items-center gap-3">
                      <div className="w-2 h-2 rounded-full bg-blue-500" />
                      <span className="text-sm text-foreground">{overview.open_invoices} open invoice{overview.open_invoices > 1 ? 's' : ''} awaiting payment</span>
                    </div>
                    <ArrowRight size={14} className="text-muted-foreground" />
                  </Link>
                )}
              </CardContent>
            </Card>
          )}
          {overview.failed_payments_30d === 0 && overview.dunning_active === 0 && overview.open_invoices === 0 && (
            <Card className="mt-6">
              <CardContent className="py-4 px-5 flex items-center gap-3">
                <Check size={16} className="text-emerald-500" />
                <span className="text-sm text-muted-foreground">All clear — no failed payments, no active dunning, no overdue invoices</span>
              </CardContent>
            </Card>
          )}

          {/* Get Started with Velox */}
          {(() => {
            const steps = [
              { done: overview.active_subscriptions > 0 || overview.total_revenue > 0, label: 'Configure pricing', desc: 'Create meters, rating rules, and plans', to: '/pricing', icon: Tag, cta: 'Go to Pricing' },
              { done: overview.active_customers > 0, label: 'Add customers', desc: 'Create your first customer', to: '/customers', icon: UsersIcon, cta: 'Go to Customers' },
              { done: overview.active_subscriptions > 0, label: 'Create subscriptions', desc: 'Subscribe customers to plans', to: '/subscriptions', icon: CreditCard, cta: 'Go to Subscriptions' },
              { done: overview.total_revenue > 0, label: 'Generate revenue', desc: 'Ingest usage events and run billing', to: undefined, icon: BarChart3, cta: 'Run Billing' },
            ]
            const completedCount = steps.filter(s => s.done).length
            const totalSteps = steps.length
            if (completedCount >= totalSteps) return null
            return (
              <Card className="mt-6 bg-accent/50">
                <CardContent className="p-0">
                  <button
                    onClick={() => setGetStartedOpen(!getStartedOpen)}
                    className="w-full px-6 py-4 flex items-center justify-between text-left"
                  >
                    <div>
                      <h3 className="text-sm font-semibold text-foreground">Get Started</h3>
                      <p className="text-sm text-muted-foreground mt-0.5">{completedCount} of {totalSteps} steps completed</p>
                    </div>
                    <div className="flex items-center gap-3">
                      <div className="w-24 h-1.5 bg-border rounded-full overflow-hidden">
                        <div className="h-full bg-primary rounded-full transition-all" style={{ width: `${(completedCount / totalSteps) * 100}%` }} />
                      </div>
                      {getStartedOpen ? <ChevronUp size={16} className="text-muted-foreground" /> : <ChevronDown size={16} className="text-muted-foreground" />}
                    </div>
                  </button>
                  {getStartedOpen && (
                    <div className="px-6 pb-5">
                      <ol className="space-y-2.5 text-sm">
                        {steps.map((step, i) => (
                          <li key={i} className="flex items-center gap-3">
                            {step.done ? (
                              <span className="w-5 h-5 rounded-full bg-emerald-500 text-white flex items-center justify-center shrink-0">
                                <Check size={12} strokeWidth={3} />
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
                              {' '}<span className={step.done ? 'text-muted-foreground' : 'text-muted-foreground'}>— {step.desc}</span>
                            </span>
                          </li>
                        ))}
                      </ol>
                    </div>
                  )}
                </CardContent>
              </Card>
            )
          })()}
        </>
      )}
    </Layout>
  )
}
