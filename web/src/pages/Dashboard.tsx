import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api, formatCents, formatDate } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { StatCard } from '@/components/StatCard'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { ErrorState } from '@/components/ErrorState'
import { AlertTriangle, Wallet, ChevronDown, ChevronUp } from 'lucide-react'
import { BarChart, Bar, XAxis, YAxis, Tooltip, ResponsiveContainer, CartesianGrid } from 'recharts'

type Period = '30d' | '90d' | '12m'

const periodLabels: Record<Period, string> = {
  '30d': 'Last 30 days',
  '90d': 'Last 90 days',
  '12m': 'Last 12 months',
}

export function DashboardPage() {
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

  const runningBilling = billingMutation.isPending

  return (
    <Layout>
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900 dark:text-gray-100">Dashboard</h1>
          <p className="text-sm text-gray-600 dark:text-gray-400 mt-1">Billing analytics overview</p>
        </div>
        <div className="flex items-center gap-3">
          {billingResult && (
            <span className="text-xs text-gray-500">{billingResult}</span>
          )}
          <button
            onClick={handleTriggerBilling}
            disabled={runningBilling}
            title="Run billing cycle: meter usage, generate invoices, apply credits"
            className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm disabled:opacity-50 transition-colors"
          >
            {runningBilling ? 'Running...' : 'Run Billing'}
          </button>
        </div>
      </div>

      {error ? (
        <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card mt-6">
          <ErrorState message={error} onRetry={() => refetchOverview()} />
        </div>
      ) : loading || !overview ? (
        <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card mt-6">
          <LoadingSkeleton rows={6} columns={4} />
        </div>
      ) : (
        <>
          {/* KPI Cards */}
          <div className="grid grid-cols-2 lg:grid-cols-4 gap-4 mt-6">
            <StatCard title="MRR" value={formatCents(overview.mrr)} subtitle="Monthly recurring revenue" />
            <StatCard title="Active Customers" value={String(overview.active_customers)} />
            <StatCard title="Outstanding AR" value={formatCents(overview.outstanding_ar)} subtitle="Unpaid invoices" />
            <StatCard title="Paid Invoices (30d)" value={String(overview.paid_invoices_30d)} subtitle={`${overview.failed_payments_30d} failed`} />
          </div>

          {/* Revenue Chart */}
          <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card mt-6 p-6">
            <div className="flex items-center justify-between mb-4">
              <div>
                <h2 className="text-sm font-semibold text-gray-900 dark:text-gray-100">Revenue Over Time</h2>
                <p className="text-xs text-gray-500 dark:text-gray-500 mt-0.5">Total revenue: {formatCents(overview.total_revenue)}</p>
              </div>
              <div className="flex gap-1 bg-gray-100 dark:bg-gray-800 rounded-lg p-0.5">
                {(Object.keys(periodLabels) as Period[]).map(p => (
                  <button
                    key={p}
                    onClick={() => setPeriod(p)}
                    className={`px-3 py-1.5 text-xs font-medium rounded-md transition-colors ${
                      period === p
                        ? 'bg-white dark:bg-gray-700 text-gray-900 dark:text-gray-100 shadow-sm'
                        : 'text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-200'
                    }`}
                  >
                    {periodLabels[p]}
                  </button>
                ))}
              </div>
            </div>

            {chartData.length > 0 ? (
              <ResponsiveContainer width="100%" height={280}>
                <BarChart data={chartData} margin={{ top: 5, right: 5, left: 5, bottom: 5 }}>
                  <CartesianGrid strokeDasharray="3 3" stroke="#f0f0f0" className="dark:stroke-gray-800" />
                  <XAxis
                    dataKey="date"
                    tickFormatter={(d) => formatDate(d)}
                    tick={{ fontSize: 12 }}
                    className="dark:fill-gray-400"
                  />
                  <YAxis
                    tickFormatter={(v) => formatCents(v)}
                    tick={{ fontSize: 12 }}
                    width={80}
                    className="dark:fill-gray-400"
                  />
                  <Tooltip
                    formatter={(value) => [formatCents(Number(value)), 'Revenue']}
                    labelFormatter={(label) => formatDate(String(label))}
                    contentStyle={{
                      backgroundColor: document.documentElement.classList.contains('dark') ? '#1f2937' : 'white',
                      border: `1px solid ${document.documentElement.classList.contains('dark') ? '#374151' : '#e5e7eb'}`,
                      borderRadius: '8px',
                      fontSize: '13px',
                      color: document.documentElement.classList.contains('dark') ? '#f3f4f6' : '#111827',
                    }}
                  />
                  <Bar dataKey="revenue_cents" fill="#635BFF" radius={[4, 4, 0, 0]} />
                </BarChart>
              </ResponsiveContainer>
            ) : (
              <div className="h-48 flex items-center justify-center text-sm text-gray-400 dark:text-gray-500">
                No revenue data for this period
              </div>
            )}
          </div>

          {/* Dunning & Credits row */}
          <div className="grid grid-cols-1 lg:grid-cols-2 gap-6 mt-6">
            <Link to="/dunning?tab=runs" className="bg-white dark:bg-gray-900 rounded-xl shadow-card hover:shadow-card-hover transition-shadow p-6 block">
              <div className="flex items-center gap-3">
                <div className={`w-10 h-10 rounded-lg flex items-center justify-center ${
                  overview.dunning_active > 0 ? 'bg-amber-50 text-amber-500' : 'bg-gray-100 text-gray-400'
                }`}>
                  <AlertTriangle size={18} />
                </div>
                <div>
                  <p className="text-sm font-medium text-gray-500">Active Dunning</p>
                  <p className="text-2xl font-semibold text-gray-900 dark:text-gray-100">{overview.dunning_active}</p>
                </div>
              </div>
              {overview.dunning_active > 0 && (
                <p className="text-xs text-amber-600 mt-3">
                  {overview.dunning_active} customer{overview.dunning_active !== 1 ? 's' : ''} with failed payments requiring attention
                </p>
              )}
            </Link>

            <Link to="/credits" className="bg-white dark:bg-gray-900 rounded-xl shadow-card hover:shadow-card-hover transition-shadow p-6 block">
              <div className="flex items-center gap-3">
                <div className="w-10 h-10 rounded-lg bg-emerald-50 text-emerald-500 flex items-center justify-center">
                  <Wallet size={18} />
                </div>
                <div>
                  <p className="text-sm font-medium text-gray-500">Credit Balance</p>
                  <p className="text-2xl font-semibold text-gray-900 dark:text-gray-100">{formatCents(overview.credit_balance_total)}</p>
                </div>
              </div>
              <p className="text-xs text-gray-500 mt-3">Total outstanding credits across all customers</p>
            </Link>
          </div>

          {/* Additional stats */}
          <div className="grid grid-cols-2 lg:grid-cols-3 gap-4 mt-6">
            <StatCard title="Active Subscriptions" value={String(overview.active_subscriptions)} />
            <StatCard title="Total Revenue" value={formatCents(overview.total_revenue)} subtitle="All-time paid invoices" />
            <StatCard title="Avg Invoice Value" value={formatCents(overview.avg_invoice_value)} />
          </div>

          {/* Get Started — collapsible, shows progress */}
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
              <div className="bg-velox-50/50 rounded-xl shadow-card mt-6">
                <button
                  onClick={() => setGetStartedOpen(!getStartedOpen)}
                  className="w-full px-6 py-4 flex items-center justify-between text-left"
                >
                  <div>
                    <h3 className="text-sm font-semibold text-velox-900">Get Started</h3>
                    <p className="text-sm text-velox-700 mt-0.5">{completedCount} of {steps.length} steps completed</p>
                  </div>
                  <div className="flex items-center gap-3">
                    <div className="w-24 h-1.5 bg-velox-200 rounded-full overflow-hidden">
                      <div className="h-full bg-velox-600 rounded-full transition-all" style={{ width: `${(completedCount / steps.length) * 100}%` }} />
                    </div>
                    {getStartedOpen ? <ChevronUp size={16} className="text-velox-600" /> : <ChevronDown size={16} className="text-velox-600" />}
                  </div>
                </button>
                {getStartedOpen && (
                  <div className="px-6 pb-6">
                    <ol className="space-y-2 text-sm">
                      {steps.map((step, i) => (
                        <li key={i} className="flex items-center gap-3">
                          {step.done ? (
                            <span className="w-5 h-5 rounded-full bg-emerald-500 text-white flex items-center justify-center shrink-0">
                              <svg className="w-3 h-3" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth={3} d="M5 13l4 4L19 7" /></svg>
                            </span>
                          ) : (
                            <span className="w-5 h-5 rounded-full bg-velox-600 text-white flex items-center justify-center text-xs font-bold shrink-0">{i + 1}</span>
                          )}
                          <span className={step.done ? 'text-gray-400 line-through' : 'text-velox-700'}>
                            {step.to ? <Link to={step.to} className="font-medium hover:underline">{step.label}</Link> : <span className="font-medium">{step.label}</span>}
                            {' '}<span className={step.done ? 'text-gray-400' : 'text-velox-600'}>— {step.desc}</span>
                          </span>
                        </li>
                      ))}
                    </ol>
                  </div>
                )}
              </div>
            )
          })()}
        </>
      )}
    </Layout>
  )
}
