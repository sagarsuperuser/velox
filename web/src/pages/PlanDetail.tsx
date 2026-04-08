import { useEffect, useState, useMemo } from 'react'
import { useParams, Link, useNavigate } from 'react-router-dom'
import { api, formatCents, formatDate, type Plan, type Meter, type Subscription, type RatingRule } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'
import { Modal } from '@/components/Modal'
import { FormField, FormSelect } from '@/components/FormField'
import { Breadcrumbs } from '@/components/Breadcrumbs'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { useToast } from '@/components/Toast'
import { useFormValidation, rules } from '@/hooks/useFormValidation'
import { Copy, Check } from 'lucide-react'

function CopyButton({ text }: { text: string }) {
  const [copied, setCopied] = useState(false)

  const handleCopy = () => {
    navigator.clipboard.writeText(text)
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }

  return (
    <button
      onClick={handleCopy}
      className="inline-flex items-center justify-center w-6 h-6 rounded-md text-gray-400 hover:text-gray-600 hover:bg-gray-100 transition-colors"
      title="Copy to clipboard"
    >
      {copied ? <Check className="w-3.5 h-3.5 text-green-500" /> : <Copy className="w-3.5 h-3.5" />}
    </button>
  )
}

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
        <Breadcrumbs items={[{ label: 'Pricing', to: '/pricing' }, { label: 'Loading...' }]} />
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
      <Breadcrumbs items={[{ label: 'Pricing', to: '/pricing' }, { label: plan.name }]} />

      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900">{plan.name}</h1>
          <div className="flex items-center gap-2 mt-1">
            <span className="text-xs text-gray-400 font-mono bg-gray-50 border border-gray-100 px-2 py-0.5 rounded">{plan.id}</span>
            <CopyButton text={plan.id} />
            <span className="text-xs font-medium text-gray-600 bg-gray-100 px-2.5 py-0.5 rounded-full">{plan.code}</span>
          </div>
        </div>
        <div className="flex items-center gap-3">
          <Badge status={plan.status} />
          <button
            onClick={() => setShowEdit(true)}
            className="px-3.5 py-1.5 border border-gray-300 text-gray-700 rounded-lg text-sm font-medium hover:bg-gray-50 transition-colors"
          >
            Edit
          </button>
        </div>
      </div>

      {/* Key metrics row */}
      <div className="bg-white rounded-xl shadow-card flex divide-x divide-gray-100 mt-6">
        <div className="flex-1 px-6 py-4">
          <p className="text-xs text-gray-500">Base Price</p>
          <p className="text-lg font-semibold text-gray-900 mt-1">{formatCents(plan.base_amount_cents)}</p>
        </div>
        <div className="flex-1 px-6 py-4">
          <p className="text-xs text-gray-500">Interval</p>
          <p className="text-lg font-semibold text-gray-900 mt-1">{plan.billing_interval === 'yearly' ? 'Yearly' : 'Monthly'}</p>
        </div>
        <div className="flex-1 px-6 py-4">
          <p className="text-xs text-gray-500">Currency</p>
          <p className="text-lg font-semibold text-gray-900 mt-1">{plan.currency.toUpperCase()}</p>
        </div>
        <div className="flex-1 px-6 py-4">
          <p className="text-xs text-gray-500">Active Subscriptions</p>
          <p className="text-lg font-semibold text-gray-900 mt-1">{activeSubscriptions.length}</p>
        </div>
      </div>

      {/* Properties card */}
      <div className="bg-white rounded-xl shadow-card mt-6">
        <div className="px-6 py-4 border-b border-gray-100">
          <h2 className="text-sm font-semibold text-gray-900">Properties</h2>
        </div>
        <div className="divide-y divide-gray-50">
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-xs text-gray-500 w-40 shrink-0">Code</span>
            <span className="text-sm text-gray-900 font-mono">{plan.code}</span>
          </div>
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-xs text-gray-500 w-40 shrink-0">Billing Interval</span>
            <Badge status={plan.billing_interval === 'yearly' ? 'yearly' : 'monthly'} />
          </div>
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-xs text-gray-500 w-40 shrink-0">Currency</span>
            <span className="text-sm text-gray-900 font-medium">{plan.currency.toUpperCase()}</span>
          </div>
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-xs text-gray-500 w-40 shrink-0">Status</span>
            <Badge status={plan.status} />
          </div>
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-xs text-gray-500 w-40 shrink-0">Created</span>
            <span className="text-sm text-gray-900">{formatDate(plan.created_at)}</span>
          </div>
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-xs text-gray-500 w-40 shrink-0">ID</span>
            <div className="flex items-center gap-2">
              <span className="text-sm text-gray-900 font-mono">{plan.id}</span>
              <CopyButton text={plan.id} />
            </div>
          </div>
        </div>
      </div>

      {/* Attached Meters */}
      <div className="bg-white rounded-xl shadow-card mt-6">
        <div className="px-6 py-4 border-b border-gray-100">
          <h2 className="text-sm font-semibold text-gray-900">Meters ({attachedMeters.length})</h2>
        </div>
        {attachedMeters.length > 0 ? (
          <div className="divide-y divide-gray-50">
            {attachedMeters.map(meter => {
              const rule = meter.rating_rule_version_id ? ruleMap[meter.rating_rule_version_id] : null
              return (
                <Link key={meter.id} to={`/meters/${meter.id}`} className="flex items-center justify-between px-6 py-3.5 hover:bg-gray-50 transition-colors group">
                  <div>
                    <p className="text-sm font-medium text-gray-900 group-hover:text-velox-600 transition-colors">{meter.name}</p>
                    <p className="text-xs text-gray-400 font-mono mt-0.5">{meter.key}</p>
                  </div>
                  <div className="flex items-center gap-2">
                    <Badge status={meter.aggregation} />
                    <span className="text-xs text-gray-500">{meter.unit}</span>
                    {rule && (
                      <span className="text-xs text-gray-500 bg-gray-100 px-2 py-0.5 rounded-full">{rule.name}</span>
                    )}
                  </div>
                </Link>
              )
            })}
          </div>
        ) : (
          <EmptyState title="No meters attached" description="This plan has no meters linked to it" />
        )}
      </div>

      {/* Subscriptions */}
      <div className="bg-white rounded-xl shadow-card mt-6">
        <div className="px-6 py-4 border-b border-gray-100 flex items-center justify-between">
          <h2 className="text-sm font-semibold text-gray-900">Subscriptions ({subscriptions.length})</h2>
          {subscriptions.length > 0 && (
            <Link to="/subscriptions" className="text-xs font-medium text-velox-600 hover:text-velox-700 transition-colors">
              View all
            </Link>
          )}
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
