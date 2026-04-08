import { useEffect, useState, useMemo } from 'react'
import { useParams, Link, useNavigate } from 'react-router-dom'
import { api, formatCents, formatDate, type Plan, type Meter, type Subscription, type RatingRule } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'
import { StatCard } from '@/components/StatCard'
import { Modal } from '@/components/Modal'
import { FormField, FormSelect } from '@/components/FormField'
import { Breadcrumbs } from '@/components/Breadcrumbs'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { useToast } from '@/components/Toast'
import { useFormValidation, rules } from '@/hooks/useFormValidation'

export function PlanDetailPage() {
  const { id } = useParams<{ id: string }>()
  const [plan, setPlan] = useState<Plan | null>(null)
  const [meters, setMeters] = useState<Meter[]>([])
  const [ratingRules, setRatingRules] = useState<RatingRule[]>([])
  const [subscriptions, setSubscriptions] = useState<Subscription[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [showEdit, setShowEdit] = useState(false)
  const toast = useToast()
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
    ]).then(([p, metersRes, rulesRes, subsRes]) => {
      setPlan(p)
      setMeters(metersRes.data)
      setRatingRules(rulesRes.data)
      setSubscriptions(subsRes.data.filter(s => s.plan_id === id))
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

  if (loading) {
    return (
      <Layout>
        <Breadcrumbs items={[{ label: 'Pricing', to: '/plans' }, { label: 'Loading...' }]} />
        <div className="bg-white rounded-xl shadow-card">
          <LoadingSkeleton rows={8} columns={3} />
        </div>
      </Layout>
    )
  }

  if (error) return <Layout><ErrorState message={error} onRetry={loadData} /></Layout>

  if (!plan) return <Layout><p>Plan not found</p></Layout>

  return (
    <Layout>
      <Breadcrumbs items={[{ label: 'Pricing', to: '/plans' }, { label: plan.name }]} />

      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900">{plan.name}</h1>
          <p className="text-sm text-gray-500 mt-0.5 font-mono">{plan.code}</p>
        </div>
        <div className="flex items-center gap-3">
          <button
            onClick={() => setShowEdit(true)}
            className="px-3 py-1.5 border border-gray-300 text-gray-700 rounded-lg text-xs font-medium hover:bg-gray-50 transition-colors"
          >
            Edit
          </button>
          <Badge status={plan.status} />
        </div>
      </div>

      {/* Stat cards */}
      <div className="grid grid-cols-4 gap-4 mt-6">
        <StatCard title="Base Price" value={formatCents(plan.base_amount_cents)} />
        <StatCard title="Billing Interval" value={plan.billing_interval === 'yearly' ? 'Yearly' : 'Monthly'} />
        <StatCard title="Currency" value={plan.currency.toUpperCase()} />
        <StatCard title="Active Subscriptions" value={String(activeSubscriptions.length)} />
      </div>

      {/* Two-column grid */}
      <div className="grid grid-cols-2 gap-6 mt-6">
        {/* Plan Details */}
        <div className="bg-white rounded-xl shadow-card">
          <div className="px-6 py-4 border-b border-gray-100">
            <h2 className="text-sm font-semibold text-gray-900">Plan Details</h2>
          </div>
          <div className="px-6 py-4 space-y-3">
            <div>
              <p className="text-xs text-gray-500">Code</p>
              <p className="text-sm text-gray-900 mt-0.5 font-mono">{plan.code}</p>
            </div>
            <div>
              <p className="text-xs text-gray-500">Status</p>
              <div className="mt-0.5"><Badge status={plan.status} /></div>
            </div>
            <div>
              <p className="text-xs text-gray-500">Created</p>
              <p className="text-sm text-gray-900 mt-0.5">{formatDate(plan.created_at)}</p>
            </div>
          </div>
        </div>

        {/* Attached Meters */}
        <div className="bg-white rounded-xl shadow-card">
          <div className="px-6 py-4 border-b border-gray-100">
            <h2 className="text-sm font-semibold text-gray-900">Attached Meters</h2>
          </div>
          {attachedMeters.length > 0 ? (
            <div className="divide-y divide-gray-50">
              {attachedMeters.map(meter => {
                const rule = meter.rating_rule_version_id ? ruleMap[meter.rating_rule_version_id] : null
                return (
                  <Link key={meter.id} to={`/meters/${meter.id}`} className="flex items-center justify-between px-6 py-3 hover:bg-gray-50 transition-colors">
                    <div>
                      <p className="text-sm font-medium text-gray-900">{meter.name}</p>
                      <p className="text-xs text-gray-400">{meter.key} &middot; {meter.aggregation} &middot; {meter.unit}</p>
                    </div>
                    {rule && (
                      <span className="text-xs text-gray-500 bg-gray-100 px-2 py-0.5 rounded-full">{rule.name}</span>
                    )}
                  </Link>
                )
              })}
            </div>
          ) : (
            <EmptyState title="No meters attached" description="This plan has no meters linked to it" />
          )}
        </div>
      </div>

      {/* Subscriptions on this plan */}
      <div className="bg-white rounded-xl shadow-card mt-6">
        <div className="px-6 py-4 border-b border-gray-100">
          <h2 className="text-sm font-semibold text-gray-900">Subscriptions on this Plan</h2>
        </div>
        {subscriptions.length > 0 ? (
          <div className="overflow-x-auto">
            <table className="w-full">
              <thead>
                <tr className="border-b border-gray-100">
                  <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Name</th>
                  <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Customer</th>
                  <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Status</th>
                  <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Billing Period</th>
                  <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Created</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-50">
                {subscriptions.map(sub => (
                  <tr key={sub.id} className="hover:bg-gray-50 cursor-pointer transition-colors group" onClick={(e) => {
                    const target = e.target as HTMLElement
                    if (target.closest('button, a, input, select')) return
                    navigate(`/subscriptions/${sub.id}`)
                  }}>
                    <td className="px-6 py-3">
                      <Link to={`/subscriptions/${sub.id}`} className="text-sm font-medium text-velox-600 group-hover:text-velox-600 transition-colors hover:underline">
                        {sub.display_name}
                      </Link>
                      <p className="text-xs text-gray-400 font-mono">{sub.code}</p>
                    </td>
                    <td className="px-6 py-3">
                      <Link to={`/customers/${sub.customer_id}`} className="text-sm text-velox-600 hover:underline">
                        {sub.customer_id.slice(0, 8)}...
                      </Link>
                    </td>
                    <td className="px-6 py-3"><Badge status={sub.status} /></td>
                    <td className="px-6 py-3 text-sm text-gray-500">
                      {sub.current_billing_period_start && sub.current_billing_period_end
                        ? `${formatDate(sub.current_billing_period_start)} - ${formatDate(sub.current_billing_period_end)}`
                        : '\u2014'}
                    </td>
                    <td className="px-6 py-3 text-sm text-gray-400">{formatDate(sub.created_at)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <EmptyState title="No subscriptions" description="No subscriptions are using this plan yet" />
        )}
      </div>

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
  const [form, setForm] = useState({
    name: plan.name,
    base_amount_cents: plan.base_amount_cents,
    status: plan.status,
  })
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  const fieldRules = useMemo(() => ({
    name: [rules.required('Name')],
  }), [])
  const { onBlur, validateAll, fieldError, registerRef } = useFormValidation(fieldRules)

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!validateAll(form)) return
    setSaving(true); setError('')
    try {
      const updated = await api.updatePlan(plan.id, {
        name: form.name,
        base_amount_cents: form.base_amount_cents,
        status: form.status,
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
      <form onSubmit={handleSubmit} noValidate className="space-y-3">
        <FormField label="Name" required value={form.name} maxLength={255}
          ref={registerRef('name')} error={fieldError('name')}
          onChange={e => setForm(f => ({ ...f, name: e.target.value }))}
          onBlur={() => onBlur('name', form.name)} />
        <FormField label="Base Price (dollars)" type="number" value={String(form.base_amount_cents / 100)}
          onChange={e => setForm(f => ({ ...f, base_amount_cents: Math.round(parseFloat(e.target.value || '0') * 100) }))} />
        <FormSelect label="Status" value={form.status}
          onChange={e => setForm(f => ({ ...f, status: e.target.value }))}
          options={[
            { value: 'active', label: 'Active' },
            { value: 'archived', label: 'Archived' },
          ]} />
        {error && <p className="text-sm text-red-600 bg-red-50 border border-red-200 rounded-lg px-3 py-2">{error}</p>}
        <div className="flex justify-end gap-3 pt-4 border-t border-gray-100 mt-1">
          <button type="button" onClick={onClose} className="px-4 py-2 border border-gray-300 text-gray-700 rounded-lg text-sm font-medium hover:bg-gray-50 transition-colors">Cancel</button>
          <button type="submit" disabled={saving}
            className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow disabled:opacity-50">
            {saving ? 'Saving...' : 'Save'}
          </button>
        </div>
      </form>
    </Modal>
  )
}
