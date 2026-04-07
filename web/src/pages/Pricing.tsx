import { useEffect, useState } from 'react'
import { api, formatCents, formatDate, type Meter, type Plan, type RatingRule } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'

export function PricingPage() {
  const [meters, setMeters] = useState<Meter[]>([])
  const [plans, setPlans] = useState<Plan[]>([])
  const [rules, setRules] = useState<RatingRule[]>([])
  const [loading, setLoading] = useState(true)
  const [tab, setTab] = useState<'plans' | 'meters' | 'rules'>('plans')

  useEffect(() => {
    Promise.all([
      api.listPlans(),
      api.listMeters(),
      api.listRatingRules(),
    ]).then(([p, m, r]) => {
      setPlans(p.data)
      setMeters(m.data)
      setRules(r.data)
      setLoading(false)
    })
  }, [])

  return (
    <Layout>
      <h1 className="text-2xl font-semibold text-gray-900">Pricing</h1>
      <p className="text-sm text-gray-500 mt-1">Plans, meters, and rating rules</p>

      {/* Tabs */}
      <div className="flex gap-1 mt-6 bg-gray-100 rounded-lg p-1 w-fit">
        {(['plans', 'meters', 'rules'] as const).map(t => (
          <button
            key={t}
            onClick={() => setTab(t)}
            className={`px-4 py-1.5 rounded-md text-sm font-medium transition-colors ${
              tab === t ? 'bg-white text-gray-900 shadow-sm' : 'text-gray-500 hover:text-gray-700'
            }`}
          >
            {t === 'plans' ? `Plans (${plans.length})` :
             t === 'meters' ? `Meters (${meters.length})` :
             `Rating Rules (${rules.length})`}
          </button>
        ))}
      </div>

      <div className="bg-white rounded-xl border border-gray-200 mt-4">
        {loading ? (
          <div className="p-8 text-gray-400 animate-pulse">Loading...</div>
        ) : tab === 'plans' ? (
          <table className="w-full">
            <thead>
              <tr className="border-b border-gray-100">
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Name</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Code</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Interval</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Status</th>
                <th className="text-right text-xs font-medium text-gray-500 px-6 py-3">Base Price</th>
                <th className="text-right text-xs font-medium text-gray-500 px-6 py-3">Meters</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-50">
              {plans.map(p => (
                <tr key={p.id} className="hover:bg-gray-50">
                  <td className="px-6 py-3 text-sm font-medium text-gray-900">{p.name}</td>
                  <td className="px-6 py-3 text-sm text-gray-500 font-mono">{p.code}</td>
                  <td className="px-6 py-3"><Badge status={p.billing_interval} /></td>
                  <td className="px-6 py-3"><Badge status={p.status} /></td>
                  <td className="px-6 py-3 text-sm font-medium text-gray-900 text-right">{formatCents(p.base_amount_cents)}</td>
                  <td className="px-6 py-3 text-sm text-gray-500 text-right">{p.meter_ids?.length || 0}</td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : tab === 'meters' ? (
          <table className="w-full">
            <thead>
              <tr className="border-b border-gray-100">
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Name</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Key</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Unit</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Aggregation</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Created</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-50">
              {meters.map(m => (
                <tr key={m.id} className="hover:bg-gray-50">
                  <td className="px-6 py-3 text-sm font-medium text-gray-900">{m.name}</td>
                  <td className="px-6 py-3 text-sm text-gray-500 font-mono">{m.key}</td>
                  <td className="px-6 py-3 text-sm text-gray-500">{m.unit}</td>
                  <td className="px-6 py-3"><Badge status={m.aggregation} /></td>
                  <td className="px-6 py-3 text-sm text-gray-400">{formatDate(m.created_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : (
          <table className="w-full">
            <thead>
              <tr className="border-b border-gray-100">
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Name</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Rule Key</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Mode</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Version</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Currency</th>
                <th className="text-right text-xs font-medium text-gray-500 px-6 py-3">Price</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-50">
              {rules.map(r => (
                <tr key={r.id} className="hover:bg-gray-50">
                  <td className="px-6 py-3 text-sm font-medium text-gray-900">{r.name}</td>
                  <td className="px-6 py-3 text-sm text-gray-500 font-mono">{r.rule_key}</td>
                  <td className="px-6 py-3"><Badge status={r.mode} /></td>
                  <td className="px-6 py-3 text-sm text-gray-500">v{r.version}</td>
                  <td className="px-6 py-3 text-sm text-gray-500">{r.currency}</td>
                  <td className="px-6 py-3 text-sm font-medium text-gray-900 text-right">
                    {r.mode === 'flat' ? formatCents(r.flat_amount_cents) :
                     r.mode === 'graduated' ? `${r.graduated_tiers?.length || 0} tiers` :
                     `${r.package_size} per pkg`}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        {!loading && (
          (tab === 'plans' && plans.length === 0) ||
          (tab === 'meters' && meters.length === 0) ||
          (tab === 'rules' && rules.length === 0)
        ) && (
          <p className="px-6 py-8 text-sm text-gray-400 text-center">
            No {tab} configured yet
          </p>
        )}
      </div>
    </Layout>
  )
}
