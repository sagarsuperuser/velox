import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api, formatCents, formatRelativeTime } from '@/lib/api'
import type { Invoice } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { cn } from '@/lib/utils'
import { Card, CardContent } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { ChevronDown, ChevronUp, Loader2, Zap, Check, ArrowRight, Tag, Users as UsersIcon, CreditCard, BarChart3 } from 'lucide-react'
import { AreaChart, Area, BarChart, Bar, ResponsiveContainer } from 'recharts'
import { statusBadgeVariant } from '@/lib/status'

export default function DashboardPage() {
  const [billingResult, setBillingResult] = useState<string | null>(null)
  const [getStartedOpen, setGetStartedOpen] = useState(true)
  const queryClient = useQueryClient()

  const { data: overview, isLoading: loading, error: errorObj, refetch } = useQuery({
    queryKey: ['dashboard-overview'],
    queryFn: () => api.getAnalyticsOverview(),
  })

  const { data: chartRes } = useQuery({
    queryKey: ['dashboard-chart'],
    queryFn: () => api.getRevenueChart('30d'),
  })
  const chartData = chartRes?.data ?? []

  const { data: recentInvoices } = useQuery({
    queryKey: ['dashboard-recent-invoices'],
    queryFn: () => api.listInvoices('limit=5'),
  })

  const error = errorObj instanceof Error ? errorObj.message : errorObj ? String(errorObj) : null

  const billingMutation = useMutation({
    mutationFn: () => api.triggerBilling(),
    onSuccess: (res) => {
      const now = new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
      setBillingResult(`${res.invoices_generated} invoice(s) at ${now}`)
      queryClient.invalidateQueries({ queryKey: ['dashboard-overview'] })
      queryClient.invalidateQueries({ queryKey: ['dashboard-recent-invoices'] })
    },
    onError: (err) => {
      setBillingResult('Failed: ' + (err instanceof Error ? err.message : 'unknown'))
    },
  })

  return (
    <Layout>
      {/* Header */}
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-semibold text-foreground">Dashboard</h1>
        <div className="flex items-center gap-3">
          {billingResult && <span className="text-xs text-muted-foreground">{billingResult}</span>}
          <Button variant="outline" onClick={() => { setBillingResult(null); billingMutation.mutate() }} disabled={billingMutation.isPending}>
            {billingMutation.isPending ? <Loader2 size={16} className="animate-spin mr-2" /> : <Zap size={16} className="mr-2" />}
            {billingMutation.isPending ? 'Running...' : 'Trigger Billing'}
          </Button>
        </div>
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
          {/* Key Numbers */}
          <div className="grid grid-cols-2 lg:grid-cols-4 gap-4 mt-6">
            <Card>
              <CardContent className="p-5">
                <p className="text-xs uppercase tracking-wider text-muted-foreground">MRR</p>
                <div className="flex items-baseline gap-2 mt-1">
                  <p className="text-2xl font-bold tabular-nums text-foreground">{formatCents(overview.mrr)}</p>
                  {chartData.length >= 2 && (() => {
                    const recent = chartData[chartData.length - 1].revenue_cents
                    const prev = chartData[chartData.length - 2].revenue_cents
                    if (prev === 0) return null
                    const pct = Math.round(((recent - prev) / prev) * 100)
                    return <span className={cn('text-[11px] font-medium', pct >= 0 ? 'text-emerald-600' : 'text-destructive')}>{pct >= 0 ? '↑' : '↓'}{Math.abs(pct)}%</span>
                  })()}
                </div>
                {chartData.length > 1 && (
                  <div className="mt-2 h-[28px]">
                    <ResponsiveContainer width="100%" height={28}>
                      <AreaChart data={chartData.slice(-10)} margin={{ top: 0, right: 0, left: 0, bottom: 0 }}>
                        <defs><linearGradient id="mrrFill" x1="0" y1="0" x2="0" y2="1"><stop offset="0%" stopColor="#635BFF" stopOpacity={0.2} /><stop offset="100%" stopColor="#635BFF" stopOpacity={0} /></linearGradient></defs>
                        <Area type="monotone" dataKey="revenue_cents" stroke="#635BFF" strokeWidth={1.5} fill="url(#mrrFill)" dot={false} />
                      </AreaChart>
                    </ResponsiveContainer>
                  </div>
                )}
              </CardContent>
            </Card>
            <Card>
              <CardContent className="p-5">
                <p className="text-xs uppercase tracking-wider text-muted-foreground">Customers</p>
                <p className="text-2xl font-bold tabular-nums text-foreground mt-1">{overview.active_customers}</p>
                <p className="text-xs text-muted-foreground mt-0.5">{overview.active_subscriptions} subscriptions</p>
              </CardContent>
            </Card>
            <Card>
              <CardContent className="p-5">
                <p className="text-xs uppercase tracking-wider text-muted-foreground">Outstanding</p>
                <p className={cn('text-2xl font-bold tabular-nums mt-1', overview.outstanding_ar > 0 ? 'text-amber-600' : 'text-foreground')}>
                  {formatCents(overview.outstanding_ar)}
                </p>
                <p className="text-xs text-muted-foreground mt-0.5">{overview.open_invoices} open invoices</p>
              </CardContent>
            </Card>
            <Card>
              <CardContent className="p-5">
                <p className="text-xs uppercase tracking-wider text-muted-foreground">Paid (30d)</p>
                <p className="text-2xl font-bold tabular-nums text-foreground mt-1">{overview.paid_invoices_30d}</p>
                <p className="text-xs text-muted-foreground mt-0.5">{formatCents(overview.total_revenue)} revenue</p>
              </CardContent>
            </Card>
          </div>

          {/* Alerts — only if issues exist */}
          {(overview.failed_payments_30d > 0 || overview.dunning_active > 0) && (
            <div className="flex flex-wrap gap-3 mt-4">
              {overview.failed_payments_30d > 0 && (
                <Link to="/invoices?payment_status=failed" className="flex items-center gap-2 px-3 py-1.5 bg-destructive/10 text-destructive rounded-lg text-sm hover:bg-destructive/15 transition-colors">
                  <span className="w-1.5 h-1.5 rounded-full bg-destructive" />
                  {overview.failed_payments_30d} failed payment{overview.failed_payments_30d > 1 ? 's' : ''}
                  <ArrowRight size={12} />
                </Link>
              )}
              {overview.dunning_active > 0 && (
                <Link to="/dunning?tab=runs" className="flex items-center gap-2 px-3 py-1.5 bg-amber-500/10 text-amber-700 dark:text-amber-400 rounded-lg text-sm hover:bg-amber-500/15 transition-colors">
                  <span className="w-1.5 h-1.5 rounded-full bg-amber-500" />
                  {overview.dunning_active} active dunning
                  <ArrowRight size={12} />
                </Link>
              )}
            </div>
          )}

          {/* Revenue Chart (compact, no controls — Analytics has the full version) */}
          <Card className="mt-4">
            <CardContent className="p-5">
              <div className="flex items-center justify-between mb-3">
                <p className="text-sm font-medium text-foreground">Revenue</p>
                <Link to="/analytics" className="text-xs text-primary hover:underline">Analytics →</Link>
              </div>
              {chartData.length > 0 ? (
                <ResponsiveContainer width="100%" height={120}>
                  <BarChart data={chartData} margin={{ top: 0, right: 0, left: 0, bottom: 0 }}>
                    <Bar dataKey="revenue_cents" fill="#635BFF" radius={[2, 2, 0, 0]} />
                  </BarChart>
                </ResponsiveContainer>
              ) : (
                <div className="h-[120px] flex items-center justify-center text-sm text-muted-foreground">
                  Revenue data will appear after your first billing cycle
                </div>
              )}
            </CardContent>
          </Card>

          {/* Recent Invoices + Financial Summary */}
          <div className="grid grid-cols-1 lg:grid-cols-3 gap-4 mt-6">
          <Card className="lg:col-span-2">
            <CardContent className="p-0">
              <div className="flex items-center justify-between px-5 py-3 border-b border-border">
                <p className="text-sm font-medium text-foreground">Recent Invoices</p>
                <Link to="/invoices" className="text-xs text-primary hover:underline">View all →</Link>
              </div>
              {recentInvoices?.data && recentInvoices.data.length > 0 ? (
                <div className="divide-y divide-border">
                  {recentInvoices.data.slice(0, 5).map((inv: Invoice) => (
                    <Link key={inv.id} to={`/invoices/${inv.id}`} className="flex items-center justify-between px-5 py-3 hover:bg-muted/50 transition-colors">
                      <div className="flex items-center gap-3 min-w-0">
                        <span className={cn('w-1.5 h-1.5 rounded-full shrink-0',
                          inv.payment_status === 'succeeded' ? 'bg-emerald-500' :
                          inv.payment_status === 'failed' ? 'bg-destructive' :
                          inv.payment_status === 'processing' ? 'bg-blue-500' : 'bg-amber-500'
                        )} />
                        <span className="text-sm font-mono text-foreground">{inv.invoice_number}</span>
                        <Badge variant={statusBadgeVariant(inv.payment_status)} className="text-[10px]">{inv.payment_status}</Badge>
                      </div>
                      <div className="flex items-center gap-4 shrink-0">
                        <span className="text-sm font-mono tabular-nums text-foreground">{formatCents(inv.amount_due_cents, inv.currency)}</span>
                        <span className="text-xs text-muted-foreground w-16 text-right">{formatRelativeTime(inv.created_at)}</span>
                      </div>
                    </Link>
                  ))}
                </div>
              ) : (
                <div className="px-5 py-8 text-center text-sm text-muted-foreground">
                  No invoices yet. Run billing to generate your first invoice.
                </div>
              )}
            </CardContent>
          </Card>

          {/* Financial Summary */}
          <Card>
            <CardContent className="p-0">
              <div className="px-5 py-3 border-b border-border">
                <p className="text-sm font-medium text-foreground">Financial Summary</p>
              </div>
              <div className="divide-y divide-border">
                <div className="flex items-center justify-between px-5 py-2.5">
                  <span className="text-sm text-muted-foreground">Outstanding AR</span>
                  <span className={cn('text-sm font-medium tabular-nums font-mono', overview.outstanding_ar > 0 ? 'text-amber-600' : 'text-foreground')}>
                    {formatCents(overview.outstanding_ar)}
                  </span>
                </div>
                <div className="flex items-center justify-between px-5 py-2.5">
                  <span className="text-sm text-muted-foreground">Total Revenue</span>
                  <span className="text-sm font-medium tabular-nums font-mono text-foreground">{formatCents(overview.total_revenue)}</span>
                </div>
                <div className="flex items-center justify-between px-5 py-2.5">
                  <span className="text-sm text-muted-foreground">Avg Invoice</span>
                  <span className="text-sm font-medium tabular-nums font-mono text-foreground">{formatCents(overview.avg_invoice_value)}</span>
                </div>
                <div className="flex items-center justify-between px-5 py-2.5">
                  <span className="text-sm text-muted-foreground">Credit Balance</span>
                  <span className="text-sm font-medium tabular-nums font-mono text-foreground">{formatCents(overview.credit_balance_total)}</span>
                </div>
                <div className="flex items-center justify-between px-5 py-2.5">
                  <span className="text-sm text-muted-foreground">Open Invoices</span>
                  <span className="text-sm font-medium tabular-nums text-foreground">{overview.open_invoices}</span>
                </div>
              </div>
            </CardContent>
          </Card>
          </div>

          {/* Get Started (new users only) */}
          {(() => {
            const steps = [
              { done: overview.active_subscriptions > 0 || overview.total_revenue > 0, label: 'Configure pricing', desc: 'create meters, rating rules, and plans', to: '/pricing' },
              { done: overview.active_customers > 0, label: 'Add customers', desc: 'create your first customer', to: '/customers' },
              { done: overview.active_subscriptions > 0, label: 'Create subscriptions', desc: 'subscribe customers to plans', to: '/subscriptions' },
              { done: overview.total_revenue > 0, label: 'Generate revenue', desc: 'ingest usage events and run billing', to: undefined as string | undefined },
            ]
            const completedCount = steps.filter(s => s.done).length
            if (completedCount >= steps.length) return null
            return (
              <Card className="mt-4 bg-accent/50">
                <CardContent className="p-0">
                  <button onClick={() => setGetStartedOpen(!getStartedOpen)} className="w-full px-5 py-3 flex items-center justify-between text-left">
                    <div className="flex items-center gap-3">
                      <span className="text-sm font-medium text-foreground">Get Started</span>
                      <span className="text-xs text-muted-foreground">{completedCount}/{steps.length}</span>
                    </div>
                    {getStartedOpen ? <ChevronUp size={14} className="text-muted-foreground" /> : <ChevronDown size={14} className="text-muted-foreground" />}
                  </button>
                  {getStartedOpen && (
                    <div className="px-5 pb-4">
                      <ol className="space-y-2 text-sm">
                        {steps.map((step, i) => (
                          <li key={i} className="flex items-center gap-2.5">
                            {step.done ? <Check size={14} className="text-emerald-500 shrink-0" /> : <span className="w-[14px] text-xs font-bold text-primary text-center shrink-0">{i + 1}</span>}
                            <span className={step.done ? 'text-muted-foreground line-through' : 'text-foreground'}>
                              {step.to ? <Link to={step.to} className="font-medium hover:underline">{step.label}</Link> : <span className="font-medium">{step.label}</span>}
                              <span className="text-muted-foreground"> — {step.desc}</span>
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
