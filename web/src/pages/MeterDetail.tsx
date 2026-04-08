import { useEffect, useState } from 'react'
import { useParams, Link, useNavigate } from 'react-router-dom'
import { api, formatCents, formatDate, type Meter, type Plan, type RatingRule } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Breadcrumbs } from '@/components/Breadcrumbs'
import { StatCard } from '@/components/StatCard'
import { Badge } from '@/components/Badge'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { ErrorState } from '@/components/ErrorState'
import { EmptyState } from '@/components/EmptyState'

export function MeterDetailPage() {
  const { id } = useParams<{ id: string }>()
  const [meter, setMeter] = useState<Meter | null>(null)
  const [ratingRule, setRatingRule] = useState<RatingRule | null>(null)
  const [plans, setPlans] = useState<Plan[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const navigate = useNavigate()

  const loadData = () => {
    if (!id) return
    setLoading(true)
    setError(null)
    api.getMeter(id).then(async (m) => {
      setMeter(m)

      const promises: Promise<void>[] = []

      // Fetch rating rule if linked
      if (m.rating_rule_version_id) {
        promises.push(
          api.getRatingRule(m.rating_rule_version_id)
            .then(r => setRatingRule(r))
            .catch(() => {})
        )
      }

      // Fetch plans that use this meter
      promises.push(
        api.listPlans()
          .then(res => {
            setPlans(res.data.filter(p => p.meter_ids?.includes(id)))
          })
          .catch(() => {})
      )

      await Promise.all(promises)
      setLoading(false)
    }).catch(err => { setError(err instanceof Error ? err.message : 'Failed to load meter'); setLoading(false) })
  }

  useEffect(() => { loadData() }, [id])

  if (loading) {
    return (
      <Layout>
        <Breadcrumbs items={[{ label: 'Pricing', to: '/meters' }, { label: 'Loading...' }]} />
        <div className="bg-white rounded-xl shadow-card">
          <LoadingSkeleton rows={8} columns={3} />
        </div>
      </Layout>
    )
  }

  if (error) return <Layout><ErrorState message={error} onRetry={loadData} /></Layout>

  if (!meter) return <Layout><p>Meter not found</p></Layout>

  return (
    <Layout>
      <Breadcrumbs items={[{ label: 'Pricing', to: '/meters' }, { label: meter.name }]} />

      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900">{meter.name}</h1>
          <div className="flex items-center gap-2 mt-1">
            <span className="text-sm text-gray-500 font-mono bg-gray-100 px-2 py-0.5 rounded">{meter.key}</span>
            <Badge status={meter.aggregation} />
          </div>
        </div>
      </div>

      {/* Stat cards */}
      <div className="grid grid-cols-4 gap-4 mt-6">
        <StatCard title="Key" value={meter.key} />
        <StatCard title="Unit" value={meter.unit} />
        <StatCard title="Aggregation" value={meter.aggregation} />
        <StatCard title="Created" value={formatDate(meter.created_at)} />
      </div>

      {/* Two-column grid */}
      <div className="grid grid-cols-2 gap-6 mt-6">
        {/* Meter Configuration */}
        <div className="bg-white rounded-xl shadow-card">
          <div className="px-6 py-4 border-b border-gray-100">
            <h2 className="text-sm font-semibold text-gray-900">Meter Configuration</h2>
          </div>
          <div className="px-6 py-4 grid grid-cols-2 gap-4">
            <div>
              <p className="text-xs text-gray-500">Key</p>
              <p className="text-sm text-gray-900 mt-0.5 font-mono">{meter.key}</p>
            </div>
            <div>
              <p className="text-xs text-gray-500">Unit</p>
              <p className="text-sm text-gray-900 mt-0.5">{meter.unit}</p>
            </div>
            <div>
              <p className="text-xs text-gray-500">Aggregation</p>
              <p className="text-sm text-gray-900 mt-0.5">{meter.aggregation}</p>
            </div>
            <div>
              <p className="text-xs text-gray-500">Created</p>
              <p className="text-sm text-gray-900 mt-0.5">{formatDate(meter.created_at)}</p>
            </div>
          </div>
        </div>

        {/* Linked Rating Rule */}
        <div className="bg-white rounded-xl shadow-card">
          <div className="px-6 py-4 border-b border-gray-100">
            <h2 className="text-sm font-semibold text-gray-900">Linked Rating Rule</h2>
          </div>
          {ratingRule ? (
            <div className="px-6 py-4">
              <div className="grid grid-cols-2 gap-4">
                <div>
                  <p className="text-xs text-gray-500">Name</p>
                  <p className="text-sm text-gray-900 mt-0.5">{ratingRule.name}</p>
                </div>
                <div>
                  <p className="text-xs text-gray-500">Mode</p>
                  <p className="text-sm text-gray-900 mt-0.5">{ratingRule.mode}</p>
                </div>
                <div>
                  <p className="text-xs text-gray-500">Currency</p>
                  <p className="text-sm text-gray-900 mt-0.5">{ratingRule.currency?.toUpperCase() || '\u2014'}</p>
                </div>
                <div>
                  <p className="text-xs text-gray-500">Version</p>
                  <p className="text-sm text-gray-900 mt-0.5">{ratingRule.version}</p>
                </div>
              </div>

              {/* Pricing details */}
              <div className="mt-4 pt-4 border-t border-gray-100">
                <p className="text-xs text-gray-500 mb-2">Pricing</p>
                {ratingRule.mode === 'flat' && (
                  <p className="text-sm text-gray-900">{formatCents(ratingRule.flat_amount_cents)} per unit</p>
                )}
                {ratingRule.mode === 'graduated' && ratingRule.graduated_tiers && (
                  <div className="overflow-x-auto">
                    <table className="w-full">
                      <thead>
                        <tr className="border-b border-gray-100">
                          <th className="text-left text-xs font-medium text-gray-500 py-2 pr-4">Up to</th>
                          <th className="text-right text-xs font-medium text-gray-500 py-2">Price/unit</th>
                        </tr>
                      </thead>
                      <tbody className="divide-y divide-gray-50">
                        {ratingRule.graduated_tiers.map((tier, i) => (
                          <tr key={i}>
                            <td className="py-2 pr-4 text-sm text-gray-900">
                              {tier.up_to === 0 || tier.up_to === -1 ? '\u221E' : tier.up_to.toLocaleString()}
                            </td>
                            <td className="py-2 text-sm font-medium text-gray-900 text-right">
                              {formatCents(tier.unit_amount_cents)}
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                )}
                {ratingRule.mode === 'package' && (
                  <p className="text-sm text-gray-900">
                    {ratingRule.package_size.toLocaleString()} units for {formatCents(ratingRule.package_amount_cents)}
                  </p>
                )}
              </div>
            </div>
          ) : (
            <EmptyState title="No rating rule linked" description="Link a rating rule to define how usage is priced" />
          )}
        </div>
      </div>

      {/* Plans using this meter */}
      <div className="bg-white rounded-xl shadow-card mt-6">
        <div className="px-6 py-4 border-b border-gray-100">
          <h2 className="text-sm font-semibold text-gray-900">Plans Using This Meter</h2>
        </div>
        {plans.length > 0 ? (
          <div className="overflow-x-auto">
            <table className="w-full">
              <thead>
                <tr className="border-b border-gray-100">
                  <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Name</th>
                  <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Code</th>
                  <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Interval</th>
                  <th className="text-right text-xs font-medium text-gray-500 px-6 py-3">Base Price</th>
                  <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Status</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-50">
                {plans.map(plan => (
                  <tr key={plan.id} className="hover:bg-gray-50 cursor-pointer transition-colors group" onClick={(e) => {
                    const target = e.target as HTMLElement
                    if (target.closest('button, a, input, select')) return
                    navigate(`/plans/${plan.id}`)
                  }}>
                    <td className="px-6 py-3">
                      <Link to={`/plans/${plan.id}`} className="text-sm font-medium text-velox-600 group-hover:text-velox-600 transition-colors hover:underline">
                        {plan.name}
                      </Link>
                    </td>
                    <td className="px-6 py-3 text-sm text-gray-500 font-mono">{plan.code}</td>
                    <td className="px-6 py-3 text-sm text-gray-500">{plan.billing_interval}</td>
                    <td className="px-6 py-3 text-sm font-medium text-gray-900 text-right">{formatCents(plan.base_amount_cents)}</td>
                    <td className="px-6 py-3"><Badge status={plan.status} /></td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <EmptyState title="No plans" description="No plans are currently using this meter" />
        )}
      </div>
    </Layout>
  )
}
