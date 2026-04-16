import { useEffect, useState, useMemo } from 'react'
import { useParams, Link, useNavigate } from 'react-router-dom'
import { api, formatCents, formatDate, formatDateTime, type Subscription, type Customer, type Plan, type Invoice, type InvoicePreview } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'
import { Modal } from '@/components/Modal'
import { SearchSelect } from '@/components/SearchSelect'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { Breadcrumbs } from '@/components/Breadcrumbs'
import { toast } from 'sonner'
import { useFormValidation, rules } from '@/hooks/useFormValidation'
import { CopyButton } from '@/components/CopyButton'

export function SubscriptionDetailPage() {
  const { id } = useParams<{ id: string }>()
  const [sub, setSub] = useState<Subscription | null>(null)
  const [customer, setCustomer] = useState<Customer | null>(null)
  const [plan, setPlan] = useState<Plan | null>(null)
  const [allPlans, setAllPlans] = useState<Plan[]>([])
  const [invoices, setInvoices] = useState<Invoice[]>([])
  const [preview, setPreview] = useState<InvoicePreview | null>(null)
  const [previewError, setPreviewError] = useState('')
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [acting, setActing] = useState(false)
  const [showCancelConfirm, setShowCancelConfirm] = useState(false)
  const [showPauseConfirm, setShowPauseConfirm] = useState(false)
  const [showChangePlan, setShowChangePlan] = useState(false)
  const navigate = useNavigate()

  const loadData = () => {
    if (!id) return
    setLoading(true)
    setError(null)
    api.getSubscription(id).then(async (s) => {
      setSub(s)

      const promises: Promise<void>[] = []

      // Fetch customer
      promises.push(
        api.getCustomer(s.customer_id)
          .then(c => setCustomer(c))
          .catch(() => {})
      )

      // Fetch plans
      promises.push(
        api.listPlans()
          .then(res => {
            setAllPlans(res.data)
            const found = res.data.find(p => p.id === s.plan_id)
            if (found) setPlan(found)
          })
          .catch(() => {})
      )

      // Fetch invoices
      promises.push(
        api.listInvoices('subscription_id=' + id)
          .then(res => setInvoices(res.data))
          .catch(() => {})
      )

      // Fetch invoice preview
      promises.push(
        api.invoicePreview(id)
          .then(p => setPreview(p))
          .catch(err => setPreviewError(err instanceof Error ? err.message : 'Preview unavailable'))
      )

      await Promise.all(promises)
      setLoading(false)
    }).catch(err => { setError(err instanceof Error ? err.message : 'Failed to load subscription'); setLoading(false) })
  }

  useEffect(() => { loadData() }, [id])

  const handlePause = async () => {
    if (!id || !sub) return
    setActing(true)
    try {
      const updated = await api.pauseSubscription(id)
      setSub(updated)
      toast.success('Subscription paused')
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to pause')
    } finally {
      setActing(false)
    }
  }

  const handleResume = async () => {
    if (!id || !sub) return
    setActing(true)
    try {
      const updated = await api.resumeSubscription(id)
      setSub(updated)
      toast.success('Subscription resumed')
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to resume')
    } finally {
      setActing(false)
    }
  }

  const handleCancel = async () => {
    if (!id || !sub) return
    setActing(true)
    try {
      const updated = await api.cancelSubscription(id)
      setSub(updated)
      toast.success('Subscription canceled')
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to cancel')
    } finally {
      setActing(false)
      setShowCancelConfirm(false)
    }
  }

  if (loading) {
    return (
      <Layout>
        <Breadcrumbs items={[{ label: 'Subscriptions', to: '/subscriptions' }, { label: 'Loading...' }]} />
        <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card">
          <LoadingSkeleton rows={8} columns={4} />
        </div>
      </Layout>
    )
  }

  if (error) return <Layout><ErrorState message={error} onRetry={loadData} /></Layout>

  if (!sub) return <Layout><p>Subscription not found</p></Layout>

  return (
    <Layout>
      <Breadcrumbs items={[{ label: 'Subscriptions', to: '/subscriptions' }, { label: sub.display_name }]} />

      {/* Header */}
      <div className="sticky top-0 z-10 bg-white dark:bg-gray-950 pb-4 -mx-4 px-4 md:-mx-8 md:px-8 pt-2 border-b border-gray-100 dark:border-gray-800">
        <div className="flex items-center justify-between">
          <div>
            <h1 className="text-2xl font-semibold text-gray-900 dark:text-gray-100">{sub.display_name}</h1>
            <div className="flex items-center gap-2 mt-1">
              <span className="text-xs text-gray-500 dark:text-gray-500 font-mono bg-gray-50 dark:bg-gray-800 border border-gray-100 dark:border-gray-700 px-2 py-0.5 rounded">{sub.id}</span>
              <CopyButton text={sub.id} />
              <span className="text-xs font-mono text-gray-500 bg-gray-50 dark:bg-gray-800 border border-gray-100 dark:border-gray-700 px-2 py-0.5 rounded">{sub.code}</span>
            </div>
          </div>
          <div className="flex items-center gap-3">
            {sub.status === 'draft' && (
              <button onClick={async () => {
                if (!id) return; setActing(true)
                try { const updated = await api.activateSubscription(id); setSub(updated); toast.success('Subscription activated'); loadData() }
                catch (err) { toast.error(err instanceof Error ? err.message : 'Failed to activate') }
                finally { setActing(false) }
              }} disabled={acting}
                className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm disabled:opacity-50 transition-colors">
                Activate
              </button>
            )}
            {sub.status === 'active' && (
              <>
                <button onClick={() => setShowChangePlan(true)} disabled={acting}
                  className="px-4 py-2 border border-velox-300 text-velox-600 rounded-lg text-sm font-medium hover:bg-velox-50 disabled:opacity-50 transition-colors">
                  Change Plan
                </button>
                <button onClick={() => setShowPauseConfirm(true)} disabled={acting}
                  className="px-4 py-2 border border-amber-300 text-amber-600 rounded-lg text-sm font-medium hover:bg-amber-50 disabled:opacity-50 transition-colors">
                  Pause
                </button>
                <button onClick={() => setShowCancelConfirm(true)} disabled={acting}
                  className="px-4 py-2 border border-red-300 text-red-600 rounded-lg text-sm font-medium hover:bg-red-50 disabled:opacity-50 transition-colors">
                  Cancel
                </button>
              </>
            )}
            {sub.status === 'paused' && (
              <>
                <button onClick={handleResume} disabled={acting}
                  className="px-4 py-2 border border-emerald-300 text-emerald-600 rounded-lg text-sm font-medium hover:bg-emerald-50 disabled:opacity-50 transition-colors">
                  Resume
                </button>
                <button onClick={() => setShowCancelConfirm(true)} disabled={acting}
                  className="px-4 py-2 border border-red-300 text-red-600 rounded-lg text-sm font-medium hover:bg-red-50 disabled:opacity-50 transition-colors">
                  Cancel
                </button>
              </>
            )}
            <Badge status={sub.status} />
          </div>
        </div>
      </div>

      {/* Key metrics row */}
      <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card flex divide-x divide-gray-100 dark:divide-gray-800 mt-6">
        <div className="flex-1 px-6 py-4">
          <p className="text-sm text-gray-600 dark:text-gray-400">Customer</p>
          {customer ? (
            <Link to={`/customers/${customer.id}`} className="text-lg font-semibold text-velox-600 hover:text-velox-700 mt-1 block transition-colors">
              {customer.display_name}
            </Link>
          ) : (
            <p className="text-lg font-semibold text-gray-900 dark:text-gray-100 mt-1">{sub.customer_id.slice(0, 8)}...</p>
          )}
        </div>
        <div className="flex-1 px-6 py-4">
          <p className="text-sm text-gray-600 dark:text-gray-400">Plan</p>
          {plan ? (
            <Link to={`/plans/${plan.id}`} className="text-lg font-semibold text-velox-600 hover:text-velox-700 mt-1 block transition-colors">
              {plan.name}
            </Link>
          ) : (
            <p className="text-lg font-semibold text-gray-900 dark:text-gray-100 mt-1">{sub.plan_id.slice(0, 8)}...</p>
          )}
          {plan && (
            <p className="text-xs text-gray-500 dark:text-gray-500 mt-0.5">
              {formatCents(plan.base_amount_cents)}/{plan.billing_interval === 'yearly' ? 'yr' : 'mo'}
            </p>
          )}
        </div>
        <div className="flex-1 px-6 py-4">
          <p className="text-sm text-gray-600 dark:text-gray-400">Billing Period</p>
          <p className="text-lg font-semibold text-gray-900 dark:text-gray-100 mt-1">
            {sub.current_billing_period_start && sub.current_billing_period_end
              ? `${formatDate(sub.current_billing_period_start)} \u2014 ${formatDate(sub.current_billing_period_end)}`
              : '\u2014'}
          </p>
        </div>
        <div className="flex-1 px-6 py-4">
          <p className="text-sm text-gray-600 dark:text-gray-400">Status</p>
          <div className="mt-1.5">
            <Badge status={sub.status} />
          </div>
        </div>
      </div>

      {/* Properties card */}
      <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card mt-6">
        <div className="px-6 py-4 border-b border-gray-100 dark:border-gray-800">
          <h2 className="text-sm font-semibold text-gray-900 dark:text-gray-100">Properties</h2>
        </div>
        <div className="divide-y divide-gray-100 dark:divide-gray-800">
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-sm text-gray-600 w-40 shrink-0">Code</span>
            <span className="text-sm text-gray-900 dark:text-gray-100 font-mono">{sub.code}</span>
          </div>
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-sm text-gray-600 w-40 shrink-0">Customer</span>
            {customer ? (
              <Link to={`/customers/${customer.id}`} className="text-sm font-medium text-velox-600 hover:text-velox-700 hover:underline transition-colors">
                {customer.display_name}
              </Link>
            ) : (
              <span className="text-sm text-gray-900 dark:text-gray-100 font-mono">{sub.customer_id}</span>
            )}
          </div>
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-sm text-gray-600 w-40 shrink-0">Plan</span>
            {plan ? (
              <span className="text-sm">
                <Link to={`/plans/${plan.id}`} className="font-medium text-velox-600 hover:text-velox-700 hover:underline transition-colors">
                  {plan.name}
                </Link>
                <span className="text-gray-400 ml-1.5">
                  {formatCents(plan.base_amount_cents)}/{plan.billing_interval === 'yearly' ? 'yr' : 'mo'}
                </span>
              </span>
            ) : (
              <span className="text-sm text-gray-900 dark:text-gray-100 font-mono">{sub.plan_id}</span>
            )}
          </div>
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-sm text-gray-600 w-40 shrink-0">Status</span>
            <Badge status={sub.status} />
          </div>
          {sub.billing_time && (
            <div className="flex items-center justify-between px-6 py-3">
              <span className="text-sm text-gray-600 w-40 shrink-0">Billing Time</span>
              <span className="text-sm text-gray-900 dark:text-gray-100">{sub.billing_time === 'anniversary' ? 'Anniversary' : 'Calendar'}</span>
            </div>
          )}
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-sm text-gray-600 w-40 shrink-0">Billing Period</span>
            <span className="text-sm text-gray-900 dark:text-gray-100">
              {sub.current_billing_period_start && sub.current_billing_period_end
                ? `${formatDate(sub.current_billing_period_start)} \u2014 ${formatDate(sub.current_billing_period_end)}`
                : '\u2014'}
            </span>
          </div>
          {sub.usage_cap_units != null && (
            <div className="flex items-center justify-between px-6 py-3">
              <span className="text-sm text-gray-600 w-40 shrink-0">Usage Cap</span>
              <span className="text-sm text-gray-900 dark:text-gray-100">
                {sub.usage_cap_units.toLocaleString()} units / period
                <span className="text-xs text-gray-500 ml-2">({sub.overage_action === 'block' ? 'hard cap' : 'charge overage'})</span>
              </span>
            </div>
          )}
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-sm text-gray-600 w-40 shrink-0">Created</span>
            <span className="text-sm text-gray-900 dark:text-gray-100">{formatDateTime(sub.created_at)}</span>
          </div>
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-sm text-gray-600 w-40 shrink-0">ID</span>
            <div className="flex items-center gap-2">
              <span className="text-sm text-gray-900 dark:text-gray-100 font-mono">{sub.id}</span>
              <CopyButton text={sub.id} />
            </div>
          </div>
        </div>
      </div>

      {/* Invoice Preview */}
      <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card mt-6">
        <div className="px-6 py-4 border-b border-gray-100 dark:border-gray-800">
          <h2 className="text-sm font-semibold text-gray-900 dark:text-gray-100">Next Invoice Preview</h2>
        </div>
        {preview ? (
          <>
            <div className="overflow-x-auto">
            <table className="w-full">
              <thead>
                <tr className="border-b border-gray-100 dark:border-gray-800 bg-gray-50 dark:bg-gray-800/50">
                  <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Description</th>
                  <th className="text-right text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Quantity</th>
                  <th className="text-right text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Amount</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
                {preview.lines.map((line, i) => (
                  <tr key={i}>
                    <td className="px-6 py-3 text-sm text-gray-900 dark:text-gray-100">{line.description}</td>
                    <td className="px-6 py-3 text-sm text-gray-600 text-right">{line.quantity}</td>
                    <td className="px-6 py-3 text-sm font-medium text-gray-900 dark:text-gray-100 text-right">{formatCents(line.amount_cents)}</td>
                  </tr>
                ))}
              </tbody>
              <tfoot>
                <tr className="border-t border-gray-200 dark:border-gray-700">
                  <td colSpan={2} className="px-6 py-3 text-sm font-semibold text-gray-900 dark:text-gray-100 text-right">Subtotal</td>
                  <td className="px-6 py-3 text-sm font-semibold text-gray-900 dark:text-gray-100 text-right">{formatCents(preview.subtotal_cents)}</td>
                </tr>
              </tfoot>
            </table>
            </div>
          </>
        ) : previewError ? (
          <div className="px-6 py-8 text-center">
            <p className="text-sm text-gray-600 dark:text-gray-400">Preview not available</p>
            <p className="text-sm text-gray-400 mt-1">Activate the subscription and set a billing period to see a preview</p>
          </div>
        ) : (
          <EmptyState title="No preview available" description="Invoice preview will appear once a billing period is set" />
        )}
      </div>

      {/* Related Invoices (show latest 5) */}
      <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card mt-6">
        <div className="px-6 py-4 border-b border-gray-100 flex items-center justify-between">
          <h2 className="text-sm font-semibold text-gray-900 dark:text-gray-100">Invoices ({invoices.length})</h2>
          {invoices.length > 5 && (
            <Link to="/invoices" className="text-xs text-velox-600 hover:underline">View all</Link>
          )}
        </div>
        {invoices.length > 0 ? (
          <div className="overflow-x-auto">
          <table className="w-full">
            <thead>
              <tr className="border-b border-gray-100 dark:border-gray-800 bg-gray-50 dark:bg-gray-800/50">
                <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Invoice</th>
                <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Status</th>
                <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Payment</th>
                <th className="text-right text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Amount</th>
                <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Date</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
              {invoices.slice(0, 5).map(inv => (
                <tr key={inv.id} className="hover:bg-gray-50 dark:hover:bg-gray-800/50 cursor-pointer transition-colors group" onClick={(e) => {
                  const target = e.target as HTMLElement
                  if (target.closest('button, a, input, select')) return
                  navigate(`/invoices/${inv.id}`)
                }}>
                  <td className="px-6 py-3">
                    <Link to={`/invoices/${inv.id}`} className="text-sm font-medium text-gray-900 dark:text-gray-100 group-hover:text-velox-600 transition-colors">
                      {inv.invoice_number}
                    </Link>
                  </td>
                  <td className="px-6 py-3"><Badge status={inv.status} /></td>
                  <td className="px-6 py-3"><Badge status={inv.payment_status} /></td>
                  <td className="px-6 py-3 text-sm font-medium text-gray-900 dark:text-gray-100 text-right">{formatCents(inv.total_amount_cents)}</td>
                  <td className="px-6 py-3 text-sm text-gray-600 dark:text-gray-400">{formatDateTime(inv.created_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
          </div>
        ) : (
          <EmptyState title="No invoices" description="Invoices will appear here after billing runs" />
        )}
      </div>

      <ConfirmDialog
        open={showPauseConfirm}
        title="Pause Subscription"
        message="Pausing will stop billing for this subscription. Usage will not be metered while paused. You can resume at any time."
        confirmLabel="Pause Subscription"
        onConfirm={() => { setShowPauseConfirm(false); handlePause() }}
        onCancel={() => setShowPauseConfirm(false)}
      />

      <ConfirmDialog
        open={showCancelConfirm}
        title="Cancel Subscription"
        message="Are you sure you want to cancel this subscription? This action cannot be undone."
        confirmLabel="Cancel Subscription"
        variant="danger"
        onConfirm={handleCancel}
        onCancel={() => setShowCancelConfirm(false)}
      />

      {showChangePlan && (
        <ChangePlanModal
          subscriptionId={sub.id}
          currentPlanId={sub.plan_id}
          currentPlanName={plan?.name || 'Unknown'}
          plans={allPlans}
          onClose={() => setShowChangePlan(false)}
          onChanged={(updated, proration) => {
            setSub(updated)
            const newPlan = allPlans.find(p => p.id === updated.plan_id)
            if (newPlan) setPlan(newPlan)
            setShowChangePlan(false)
            if (proration) {
              if (proration.type === 'upgrade') {
                toast.success(`Proration invoice created for ${formatCents(proration.amount_cents)}`)
              } else if (proration.type === 'downgrade') {
                toast.success(`${formatCents(Math.abs(proration.amount_cents))} credited to customer balance`)
              } else {
                toast.success('Plan changed successfully')
              }
            } else {
              toast.success('Plan changed successfully')
            }
            loadData()
          }}
        />
      )}
    </Layout>
  )
}

function ChangePlanModal({ subscriptionId, currentPlanId, currentPlanName, plans, onClose, onChanged }: {
  subscriptionId: string
  currentPlanId: string
  currentPlanName: string
  plans: Plan[]
  onClose: () => void
  onChanged: (sub: Subscription, proration?: { type: string; amount_cents: number; invoice_id?: string }) => void
}) {
  const [newPlanId, setNewPlanId] = useState('')
  const [immediate, setImmediate] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  const fieldRules = useMemo(() => ({
    plan_id: [rules.required('Plan')],
  }), [])
  const { onBlur, validateAll, fieldError } = useFormValidation(fieldRules)

  const availablePlans = plans.filter(p => p.id !== currentPlanId && p.status === 'active')

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!validateAll({ plan_id: newPlanId })) return
    setSaving(true); setError('')
    try {
      const res = await api.changePlan(subscriptionId, { new_plan_id: newPlanId, immediate })
      onChanged(res.subscription, res.proration)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to change plan')
    } finally {
      setSaving(false)
    }
  }

  return (
    <Modal open onClose={onClose} title="Change Plan">
      <form onSubmit={handleSubmit} noValidate className="space-y-4">
        <div>
          <p className="text-sm text-gray-600 dark:text-gray-400">Current plan</p>
          <p className="text-sm font-semibold text-gray-900 dark:text-gray-100 mt-0.5">{currentPlanName}</p>
        </div>

        <SearchSelect label="New Plan" required value={newPlanId} error={fieldError('plan_id')}
          onChange={(v) => { setNewPlanId(v); onBlur('plan_id', v) }}
          placeholder="Select a plan..."
          options={availablePlans.map(p => ({ value: p.id, label: `${p.name} — ${formatCents(p.base_amount_cents)}/${p.billing_interval}`, sublabel: p.code }))} />

        <label className="flex items-start gap-2 text-sm">
          <input type="checkbox" checked={immediate} onChange={e => setImmediate(e.target.checked)}
            className="mt-0.5" />
          <div>
            <span className="font-medium text-gray-700">Apply immediately (with proration)</span>
            {immediate && (
              <p className="text-xs text-gray-500 mt-1">
                The remaining time on the current billing period will be prorated. A credit or charge will be applied based on the price difference between plans.
              </p>
            )}
          </div>
        </label>

        {error && <p className="text-sm text-red-600 bg-red-50 border border-red-200 rounded-lg px-3 py-2">{error}</p>}
        <div className="flex justify-end gap-3 pt-4 border-t border-gray-100 dark:border-gray-800 mt-2">
          <button type="button" onClick={onClose} className="px-4 py-2 border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-300 rounded-lg text-sm font-medium hover:bg-gray-50 dark:hover:bg-gray-800 transition-colors">Cancel</button>
          <button type="submit" disabled={saving || !newPlanId}
            className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow disabled:opacity-50">
            {saving ? 'Changing...' : 'Change Plan'}
          </button>
        </div>
      </form>
    </Modal>
  )
}
