import { useEffect, useState } from 'react'
import { useParams, Link, useNavigate } from 'react-router-dom'
import { api, formatCents, formatDate, type Meter, type Plan, type RatingRule } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Breadcrumbs } from '@/components/Breadcrumbs'
import { Badge } from '@/components/Badge'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { ErrorState } from '@/components/ErrorState'
import { EmptyState } from '@/components/EmptyState'
import { CopyButton } from '@/components/CopyButton'

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

      // Fetch rating rule — resolve to latest version by key
      if (m.rating_rule_version_id) {
        promises.push(
          api.getRatingRule(m.rating_rule_version_id)
            .then(async (linkedRule) => {
              // Find latest version with the same rule_key
              try {
                const allRules = await api.listRatingRules()
                const sameKey = allRules.data.filter(r => r.rule_key === linkedRule.rule_key)
                if (sameKey.length > 0) {
                  // Latest = highest version number
                  const latest = sameKey.reduce((a, b) => b.version > a.version ? b : a)
                  setRatingRule(latest)
                } else {
                  setRatingRule(linkedRule)
                }
              } catch {
                setRatingRule(linkedRule)
              }
            })
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
        <Breadcrumbs items={[{ label: 'Pricing', to: '/pricing' }, { label: 'Loading...' }]} />
        <div className="bg-white rounded-xl shadow-card">
          <LoadingSkeleton rows={8} columns={3} />
        </div>
      </Layout>
    )
  }

  if (error) return <Layout><ErrorState message={error} onRetry={loadData} /></Layout>

  if (!meter) return <Layout><p>Meter not found</p></Layout>

  const graduatedTierLabel = (tier: { up_to: number }, index: number, tiers: { up_to: number }[]) => {
    const prev = index > 0 ? tiers[index - 1].up_to : 0
    if (tier.up_to === 0 || tier.up_to === -1) {
      return `Beyond ${prev.toLocaleString()} units`
    }
    if (index === 0) {
      return `First ${tier.up_to.toLocaleString()} units`
    }
    return `Next ${(tier.up_to - prev).toLocaleString()} units`
  }

  return (
    <Layout>
      <Breadcrumbs items={[{ label: 'Pricing', to: '/pricing' }, { label: meter.name }]} />

      {/* Header row */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900">{meter.name}</h1>
          <div className="flex items-center gap-2 mt-1">
            <span className="text-sm text-gray-500 font-mono">{meter.id}</span>
            <CopyButton text={meter.id} />
          </div>
        </div>
        <Badge status={meter.aggregation} />
      </div>

      {/* Properties card */}
      <div className="bg-white rounded-xl shadow-card mt-6">
        <div className="px-6 py-4 border-b border-gray-100">
          <h2 className="text-sm font-semibold text-gray-900">Properties</h2>
        </div>
        <div className="px-6">
          <div className="flex items-center justify-between py-3 border-b border-gray-50">
            <span className="text-sm text-gray-500">Key</span>
            <span className="text-sm text-gray-900 font-medium font-mono">{meter.key}</span>
          </div>
          <div className="flex items-center justify-between py-3 border-b border-gray-50">
            <span className="text-sm text-gray-500">Unit</span>
            <span className="text-sm text-gray-900 font-medium">{meter.unit}</span>
          </div>
          <div className="flex items-center justify-between py-3 border-b border-gray-50">
            <span className="text-sm text-gray-500">Aggregation</span>
            <Badge status={meter.aggregation} />
          </div>
          <div className="flex items-center justify-between py-3 border-b border-gray-50">
            <span className="text-sm text-gray-500">Created</span>
            <span className="text-sm text-gray-900 font-medium">{formatDate(meter.created_at)}</span>
          </div>
          <div className="flex items-center justify-between py-3">
            <span className="text-sm text-gray-500">ID</span>
            <div className="flex items-center gap-2">
              <span className="text-sm text-gray-900 font-medium font-mono truncate max-w-xs">{meter.id}</span>
              <CopyButton text={meter.id} />
            </div>
          </div>
        </div>
      </div>

      {/* Rating Rule card */}
      <div className="bg-white rounded-xl shadow-card mt-6">
        <div className="px-6 py-4 border-b border-gray-100">
          <div className="flex items-center justify-between">
            <div>
              <h2 className="text-sm font-semibold text-gray-900">Pricing Rule</h2>
              {ratingRule && (
                <p className="text-sm text-gray-500 mt-0.5">{ratingRule.name}</p>
              )}
            </div>
            {ratingRule && (
              <Badge status={ratingRule.mode} label={`v${ratingRule.version}`} />
            )}
          </div>
        </div>
        {ratingRule ? (
          <div className="px-6 py-5">
            <div className="flex items-center gap-2 mb-4">
              <Badge status={ratingRule.mode} />
              {ratingRule.currency && (
                <span className="text-xs text-gray-400 font-medium uppercase">{ratingRule.currency}</span>
              )}
            </div>

            {ratingRule.mode === 'flat' && (
              <div>
                <span className="text-3xl font-semibold text-gray-900">{formatCents(ratingRule.flat_amount_cents)}</span>
                <span className="text-sm text-gray-500 ml-2">per unit</span>
              </div>
            )}

            {ratingRule.mode === 'graduated' && ratingRule.graduated_tiers && (
              <div className="overflow-x-auto">
                <table className="w-full">
                  <thead>
                    <tr className="border-b border-gray-100 bg-gray-50">
                      <th className="text-left text-xs font-medium text-gray-500 py-2 pr-4">Tier</th>
                      <th className="text-right text-xs font-medium text-gray-500 py-2">Price / unit</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-gray-50">
                    {ratingRule.graduated_tiers.map((tier, i) => (
                      <tr key={i}>
                        <td className="py-2.5 pr-4 text-sm text-gray-900">
                          {graduatedTierLabel(tier, i, ratingRule.graduated_tiers!)}
                        </td>
                        <td className="py-2.5 text-sm font-medium text-gray-900 text-right">
                          {formatCents(tier.unit_amount_cents)}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}

            {ratingRule.mode === 'package' && (
              <div>
                <span className="text-3xl font-semibold text-gray-900">{ratingRule.package_size.toLocaleString()}</span>
                <span className="text-sm text-gray-500 ml-2">units per package at</span>
                <span className="text-lg font-semibold text-gray-900 ml-1">{formatCents(ratingRule.package_amount_cents)}</span>
              </div>
            )}
          </div>
        ) : (
          <EmptyState title="No pricing rule linked" description="Link a pricing rule to define how usage is priced" />
        )}
      </div>

      {/* Plans table */}
      <div className="bg-white rounded-xl shadow-card mt-6">
        <div className="px-6 py-4 border-b border-gray-100">
          <h2 className="text-sm font-semibold text-gray-900">
            Used by {plans.length} plan{plans.length !== 1 ? 's' : ''}
          </h2>
        </div>
        {plans.length > 0 ? (
          <div className="overflow-x-auto">
            <table className="w-full">
              <thead>
                <tr className="border-b border-gray-100 bg-gray-50">
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
