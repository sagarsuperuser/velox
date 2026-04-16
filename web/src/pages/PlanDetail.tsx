import { useEffect, useState, useMemo } from 'react'
import { useParams, Link, useNavigate } from 'react-router-dom'
import { api, formatCents, formatDate, formatDateTime, type Plan, type Meter, type Subscription, type RatingRule, type Customer } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'
import { Modal } from '@/components/Modal'
import { FormField, FormSelect } from '@/components/FormField'
import { Breadcrumbs } from '@/components/Breadcrumbs'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { toast } from 'sonner'
import { useFormValidation, rules } from '@/hooks/useFormValidation'
import { Plus, Pencil } from 'lucide-react'
import { CopyButton } from '@/components/CopyButton'

export function PlanDetailPage() {
  const { id } = useParams<{ id: string }>()
  const [plan, setPlan] = useState<Plan | null>(null)
  const [meters, setMeters] = useState<Meter[]>([])
  const [ratingRules, setRatingRules] = useState<RatingRule[]>([])
  const [subscriptions, setSubscriptions] = useState<Subscription[]>([])
  const [customerMap, setCustomerMap] = useState<Record<string, Customer>>({})
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [showEdit, setShowEdit] = useState(false)
  const [showAttachMeter, setShowAttachMeter] = useState(false)
  const [detachTarget, setDetachTarget] = useState<Meter | null>(null)
  const [updatingMeters, setUpdatingMeters] = useState(false)
  const navigate = useNavigate()

  const loadData = () => {
    if (!id) return
    setLoading(true)
    setError(null)
    Promise.all([
      api.getPlan(id),
      api.listMeters().catch(() => ({ data: [] as Meter[] })),
      api.listRatingRules().catch(() => ({ data: [] as RatingRule[] })),
      api.listSubscriptions().catch(() => ({ data: [] as Subscription[] })),
      api.listCustomers().catch(() => ({ data: [] as Customer[], total: 0 })),
    ]).then(([p, metersRes, rulesRes, subsRes, custRes]) => {
      setPlan(p)
      setMeters(metersRes.data)
      setRatingRules(rulesRes.data)
      setSubscriptions(subsRes.data.filter(s => s.plan_id === id))
      const cMap: Record<string, Customer> = {}
      custRes.data.forEach(c => { cMap[c.id] = c })
      setCustomerMap(cMap)
      setLoading(false)
    }).catch(err => { setError(err instanceof Error ? err.message : 'Failed to load plan'); setLoading(false) })
  }

  useEffect(() => { loadData() }, [id])

  const attachedMeters = useMemo(() => {
    if (!plan?.meter_ids) return []
    return plan.meter_ids
      .map(mid => meters.find(m => m.id === mid))
      .filter((m): m is Meter => !!m)
  }, [plan, meters])

  const ruleMap = useMemo(() => {
    const map: Record<string, RatingRule> = {}
    ratingRules.forEach(r => { map[r.id] = r })
    return map
  }, [ratingRules])

  const activeSubscriptions = useMemo(() =>
    subscriptions.filter(s => s.status === 'active'),
  [subscriptions])

  const unattachedMeters = useMemo(() =>
    meters.filter(m => !plan?.meter_ids?.includes(m.id)),
  [meters, plan])

  const handleAttachMeter = async (meterId: string) => {
    if (!plan || !id) return
    setUpdatingMeters(true)
    try {
      const updated = await api.updatePlan(id, { meter_ids: [...(plan.meter_ids || []), meterId] })
      setPlan(updated)
      setShowAttachMeter(false)
      toast.success('Meter attached')
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to attach meter')
    } finally {
      setUpdatingMeters(false)
    }
  }

  const handleDetachMeter = async (meterId: string) => {
    if (!plan || !id) return
    setUpdatingMeters(true)
    try {
      const updated = await api.updatePlan(id, { meter_ids: (plan.meter_ids || []).filter(mid => mid !== meterId) })
      setPlan(updated)
      toast.success('Meter detached')
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to detach meter')
    } finally {
      setUpdatingMeters(false)
    }
  }

  if (loading) {
    return (
      <Layout>
        <Breadcrumbs items={[{ label: 'Pricing', to: '/pricing' }, { label: 'Loading...' }]} />
        <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card">
          <LoadingSkeleton rows={8} columns={3} />
        </div>
      </Layout>
    )
  }

  if (error) return <Layout><ErrorState message={error} onRetry={loadData} /></Layout>

  if (!plan) return <Layout><p>Plan not found</p></Layout>

  return (
    <Layout>
      <Breadcrumbs items={[{ label: 'Pricing', to: '/pricing' }, { label: plan.name }]} />

      {/* Header */}
      <div className="sticky top-0 z-10 bg-white dark:bg-gray-950 pb-4 -mx-4 px-4 md:-mx-8 md:px-8 pt-2 border-b border-gray-100 dark:border-gray-800">
        <div className="flex items-center justify-between">
          <div>
            <h1 className="text-2xl font-semibold text-gray-900 dark:text-gray-100">{plan.name}</h1>
            <div className="flex items-center gap-2 mt-1">
              <span className="text-xs text-gray-500 dark:text-gray-500 font-mono bg-gray-50 dark:bg-gray-800 border border-gray-100 dark:border-gray-700 px-2 py-0.5 rounded">{plan.id}</span>
              <CopyButton text={plan.id} />
              <span className="text-xs font-mono text-gray-500 bg-gray-50 dark:bg-gray-800 border border-gray-100 dark:border-gray-700 px-2 py-0.5 rounded">{plan.code}</span>
            </div>
          </div>
          <div className="flex items-center gap-3">
            <Badge status={plan.status} />
            <button
              onClick={() => setShowEdit(true)}
              className="inline-flex items-center gap-1.5 px-4 py-2 border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-300 rounded-lg text-sm font-medium hover:bg-gray-50 dark:hover:bg-gray-800 hover:border-gray-400 transition-colors"
            >
              <Pencil size={14} />
              Edit
            </button>
          </div>
        </div>
      </div>

      {/* Key metrics row */}
      <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card flex divide-x divide-gray-100 dark:divide-gray-800 mt-6">
        <div className="flex-1 px-6 py-4">
          <p className="text-sm text-gray-600 dark:text-gray-400">Base Price</p>
          <p className="text-lg font-semibold text-gray-900 dark:text-gray-100 mt-1">{formatCents(plan.base_amount_cents)}</p>
        </div>
        <div className="flex-1 px-6 py-4">
          <p className="text-sm text-gray-600 dark:text-gray-400">Interval</p>
          <p className="text-lg font-semibold text-gray-900 dark:text-gray-100 mt-1">{plan.billing_interval === 'yearly' ? 'Yearly' : 'Monthly'}</p>
        </div>
        <div className="flex-1 px-6 py-4">
          <p className="text-sm text-gray-600 dark:text-gray-400">Currency</p>
          <p className="text-lg font-semibold text-gray-900 dark:text-gray-100 mt-1">{plan.currency.toUpperCase()}</p>
        </div>
        <div className="flex-1 px-6 py-4">
          <p className="text-sm text-gray-600 dark:text-gray-400">Active Subscriptions</p>
          <p className="text-lg font-semibold text-gray-900 dark:text-gray-100 mt-1">{activeSubscriptions.length}</p>
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
            <span className="text-sm text-gray-900 dark:text-gray-100 font-mono">{plan.code}</span>
          </div>
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-sm text-gray-600 w-40 shrink-0">Billing Interval</span>
            <Badge status={plan.billing_interval === 'yearly' ? 'yearly' : 'monthly'} />
          </div>
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-sm text-gray-600 w-40 shrink-0">Currency</span>
            <span className="text-sm text-gray-900 dark:text-gray-100 font-medium">{plan.currency.toUpperCase()}</span>
          </div>
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-sm text-gray-600 w-40 shrink-0">Status</span>
            <Badge status={plan.status} />
          </div>
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-sm text-gray-600 w-40 shrink-0">Created</span>
            <span className="text-sm text-gray-900 dark:text-gray-100">{formatDateTime(plan.created_at)}</span>
          </div>
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-sm text-gray-600 w-40 shrink-0">ID</span>
            <div className="flex items-center gap-2">
              <span className="text-sm text-gray-900 dark:text-gray-100 font-mono">{plan.id}</span>
              <CopyButton text={plan.id} />
            </div>
          </div>
        </div>
      </div>

      {/* Attached Meters */}
      <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card mt-6">
        <div className="px-6 py-4 border-b border-gray-100 flex items-center justify-between">
          <h2 className="text-sm font-semibold text-gray-900 dark:text-gray-100">Meters ({attachedMeters.length})</h2>
          {unattachedMeters.length > 0 && (
            <button
              onClick={() => setShowAttachMeter(true)}
              disabled={updatingMeters}
              className="inline-flex items-center gap-1.5 px-3 py-1.5 bg-velox-600 text-white rounded-lg text-xs font-medium hover:bg-velox-700 shadow-sm transition-colors disabled:opacity-50"
            >
              <Plus size={14} />
              Attach Meter
            </button>
          )}
        </div>
        {attachedMeters.length > 0 ? (
          <div className="divide-y divide-gray-100 dark:divide-gray-800">
            {attachedMeters.map(meter => {
              const rule = meter.rating_rule_version_id ? ruleMap[meter.rating_rule_version_id] : null
              return (
                <div key={meter.id} className="flex items-center justify-between px-6 py-3.5 hover:bg-gray-50 transition-colors group">
                  <Link to={`/meters/${meter.id}`} className="flex-1 min-w-0">
                    <p className="text-sm font-medium text-gray-900 dark:text-gray-100 group-hover:text-velox-600 transition-colors">{meter.name}</p>
                    <p className="text-xs text-gray-500 dark:text-gray-500 font-mono mt-0.5">{meter.key}</p>
                  </Link>
                  <div className="flex items-center gap-2">
                    <Badge status={meter.aggregation} />
                    <span className="text-xs text-gray-500">{meter.unit}</span>
                    {rule && (
                      <span className="text-xs text-gray-500 bg-gray-100 px-2 py-0.5 rounded-full">{rule.name}</span>
                    )}
                    <button
                      onClick={() => setDetachTarget(meter)}
                      disabled={updatingMeters}
                      className="ml-2 text-xs font-medium text-red-600 hover:text-red-700 bg-red-50 hover:bg-red-100 px-2.5 py-1 rounded-md transition-colors disabled:opacity-50"
                    >
                      Detach
                    </button>
                  </div>
                </div>
              )
            })}
          </div>
        ) : (
          <EmptyState title="No meters attached" description="Attach meters to track usage on this plan" />
        )}
      </div>

      {/* Attach Meter Picker */}
      {showAttachMeter && (
        <Modal open onClose={() => setShowAttachMeter(false)} title="Attach Meter">
          <p className="text-sm text-gray-600 mb-4">Select a meter to attach to this plan.</p>
          {unattachedMeters.length > 0 ? (
            <div className="divide-y divide-gray-100 dark:divide-gray-800 border border-gray-100 dark:border-gray-700 rounded-lg overflow-hidden">
              {unattachedMeters.map(meter => (
                <button
                  key={meter.id}
                  onClick={() => handleAttachMeter(meter.id)}
                  disabled={updatingMeters}
                  className="w-full flex items-center justify-between px-4 py-3 hover:bg-gray-50 transition-colors text-left disabled:opacity-50"
                >
                  <div>
                    <p className="text-sm font-medium text-gray-900 dark:text-gray-100">{meter.name}</p>
                    <p className="text-xs text-gray-500 dark:text-gray-500 font-mono mt-0.5">{meter.key}</p>
                  </div>
                  <div className="flex items-center gap-2">
                    <Badge status={meter.aggregation} />
                    <span className="text-xs text-gray-500">{meter.unit}</span>
                  </div>
                </button>
              ))}
            </div>
          ) : (
            <p className="text-sm text-gray-400 text-center py-4">All meters are already attached</p>
          )}
        </Modal>
      )}

      {/* Subscriptions */}
      <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card mt-6">
        <div className="px-6 py-4 border-b border-gray-100 flex items-center justify-between">
          <h2 className="text-sm font-semibold text-gray-900 dark:text-gray-100">Subscriptions ({subscriptions.length})</h2>
          {subscriptions.length > 0 && (
            <Link to="/subscriptions" className="text-sm text-gray-600 hover:text-gray-700 transition-colors">
              View all
            </Link>
          )}
        </div>
        {subscriptions.length > 0 ? (
          <div className="overflow-x-auto">
            <table className="w-full">
              <thead>
                <tr className="border-b border-gray-100 dark:border-gray-800 bg-gray-50 dark:bg-gray-800/50">
                  <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Name</th>
                  <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Customer</th>
                  <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Status</th>
                  <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Billing Period</th>
                  <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Created</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
                {subscriptions.map(sub => (
                  <tr key={sub.id} className="hover:bg-gray-50 dark:hover:bg-gray-800/50 cursor-pointer transition-colors group" onClick={(e) => {
                    const target = e.target as HTMLElement
                    if (target.closest('button, a, input, select')) return
                    navigate(`/subscriptions/${sub.id}`)
                  }}>
                    <td className="px-6 py-3">
                      <Link to={`/subscriptions/${sub.id}`} className="text-sm font-medium text-velox-600 group-hover:text-velox-600 transition-colors hover:underline">
                        {sub.display_name}
                      </Link>
                      <p className="text-xs text-gray-500 dark:text-gray-500 font-mono">{sub.code}</p>
                    </td>
                    <td className="px-6 py-3">
                      <Link to={`/customers/${sub.customer_id}`} className="text-sm text-velox-600 hover:underline">
                        {customerMap[sub.customer_id]?.display_name || sub.customer_id.slice(0, 8) + '...'}
                      </Link>
                    </td>
                    <td className="px-6 py-3"><Badge status={sub.status} /></td>
                    <td className="px-6 py-3 text-sm text-gray-600 dark:text-gray-400">
                      {sub.current_billing_period_start && sub.current_billing_period_end
                        ? `${formatDate(sub.current_billing_period_start)} — ${formatDate(sub.current_billing_period_end)}`
                        : '\u2014'}
                    </td>
                    <td className="px-6 py-3 text-sm text-gray-600 dark:text-gray-400">{formatDateTime(sub.created_at)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <EmptyState title="No subscriptions" description="No subscriptions are using this plan yet" />
        )}
      </div>

      <ConfirmDialog
        open={!!detachTarget}
        title="Detach Meter"
        message={`Are you sure you want to detach "${detachTarget?.name}" from this plan? Usage for this meter will no longer be tracked.`}
        confirmLabel="Detach"
        variant="danger"
        onConfirm={() => {
          if (detachTarget) {
            handleDetachMeter(detachTarget.id)
            setDetachTarget(null)
          }
        }}
        onCancel={() => setDetachTarget(null)}
      />

      {/* Edit Modal */}
      {showEdit && (
        <EditPlanModal
          plan={plan}
          onClose={() => setShowEdit(false)}
          onSaved={(updated) => {
            setPlan(updated)
            setShowEdit(false)
            toast.success('Plan updated')
          }}
        />
      )}
    </Layout>
  )
}

function EditPlanModal({ plan, onClose, onSaved }: {
  plan: Plan; onClose: () => void; onSaved: (p: Plan) => void
}) {
  const [name, setName] = useState(plan.name)
  const [basePrice, setBasePrice] = useState((plan.base_amount_cents / 100).toFixed(2))
  const [status, setStatus] = useState(plan.status)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  const hasChanges = name !== plan.name ||
    basePrice !== (plan.base_amount_cents / 100).toFixed(2) ||
    status !== plan.status

  const fieldRules = useMemo(() => ({
    name: [rules.required('Name')],
  }), [])
  const { onBlur, validateAll, fieldError, registerRef } = useFormValidation(fieldRules)

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!validateAll({ name })) return
    setSaving(true); setError('')
    try {
      const updated = await api.updatePlan(plan.id, {
        name,
        base_amount_cents: Math.round(parseFloat(basePrice || '0') * 100),
        status,
      })
      onSaved(updated)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to update plan')
    } finally {
      setSaving(false)
    }
  }

  return (
    <Modal open onClose={onClose} title="Edit Plan">
      <form onSubmit={handleSubmit} noValidate className="space-y-4">
        <div className="bg-gray-50 dark:bg-gray-800 rounded-lg px-4 py-3 flex items-center justify-between">
          <div>
            <p className="text-xs text-gray-500">Code</p>
            <p className="text-sm text-gray-900 dark:text-gray-100 font-mono mt-0.5">{plan.code}</p>
          </div>
          <div className="text-right">
            <p className="text-xs text-gray-500">Interval</p>
            <p className="text-sm text-gray-900 mt-0.5">{plan.billing_interval === 'yearly' ? 'Yearly' : 'Monthly'}</p>
          </div>
        </div>
        <FormField label="Plan Name" required value={name} maxLength={255}
          ref={registerRef('name')} error={fieldError('name')}
          onChange={e => setName(e.target.value)}
          onBlur={() => onBlur('name', name)} />
        <FormField label={`Base Price (${plan.currency.toUpperCase()})`} type="number" step="0.01" min={0} max={999999.99}
          value={basePrice} placeholder="49.00"
          onChange={e => setBasePrice(e.target.value)}
          hint={`${plan.billing_interval === 'yearly' ? 'Yearly' : 'Monthly'} recurring charge`} />
        <FormSelect label="Status" value={status}
          onChange={e => setStatus(e.target.value)}
          options={[
            { value: 'active', label: 'Active' },
            { value: 'archived', label: 'Archived' },
          ]} />
        {error && <p className="text-sm text-red-600 bg-red-50 border border-red-200 rounded-lg px-3 py-2">{error}</p>}
        <div className="flex justify-end gap-3 pt-4 border-t border-gray-100 dark:border-gray-800 mt-2">
          <button type="button" onClick={onClose} className="px-4 py-2 border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-300 rounded-lg text-sm font-medium hover:bg-gray-50 dark:hover:bg-gray-800 transition-colors">Cancel</button>
          <button type="submit" disabled={saving || !hasChanges}
            className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow disabled:opacity-50">
            {saving ? 'Saving...' : hasChanges ? 'Save' : 'No changes'}
          </button>
        </div>
      </form>
    </Modal>
  )
}
