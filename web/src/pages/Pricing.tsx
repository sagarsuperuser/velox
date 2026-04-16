import { useEffect, useState } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { api, formatCents, formatDateTime, getCurrencySymbol, getActiveCurrency, type Meter, type Plan, type RatingRule } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'
import { Modal } from '@/components/Modal'
import { FormField, FormSelect } from '@/components/FormField'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { ErrorState } from '@/components/ErrorState'
import { useToast } from '@/components/Toast'
import { Plus, Trash2, Loader2 } from 'lucide-react'
import { Breadcrumbs } from '@/components/Breadcrumbs'

export function PricingPage() {
  const [meters, setMeters] = useState<Meter[]>([])
  const [plans, setPlans] = useState<Plan[]>([])
  const [rules, setRules] = useState<RatingRule[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [searchParams, setSearchParams] = useSearchParams()
  const tab = (['plans', 'meters', 'rules'].includes(searchParams.get('tab') || '') ? searchParams.get('tab') : 'plans') as 'plans' | 'meters' | 'rules'
  const setTab = (t: 'plans' | 'meters' | 'rules') => setSearchParams(t === 'plans' ? {} : { tab: t })
  const [createFor, setCreateFor] = useState<'plans' | 'meters' | 'rules' | null>(null)
  const toast = useToast()
  const navigate = useNavigate()

  const loadAll = () => {
    setLoading(true)
    setError(null)
    Promise.all([api.listPlans(), api.listMeters(), api.listRatingRules()])
      .then(([p, m, r]) => { setPlans(p.data); setMeters(m.data); setRules(r.data); setLoading(false) })
      .catch(err => { setError(err instanceof Error ? err.message : 'Failed to load pricing data'); setLoading(false) })
  }

  useEffect(() => { loadAll() }, [])

  return (
    <Layout>
      <Breadcrumbs items={[{ label: 'Configuration' }, { label: 'Pricing' }]} />
      <div className="flex justify-between items-center">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900 dark:text-gray-100">Pricing</h1>
          <p className="text-sm text-gray-600 dark:text-gray-400 mt-1">Plans, meters, and rating rules</p>
        </div>
        {((tab === 'plans' && plans.length > 0) || (tab === 'meters' && meters.length > 0) || (tab === 'rules' && rules.length > 0)) && (
          <button onClick={() => setCreateFor(tab)}
            className="flex items-center gap-2 px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow transition-colors">
            <Plus size={16} />
            {tab === 'plans' ? 'Add Plan' : tab === 'meters' ? 'Add Meter' : 'Add Rule'}
          </button>
        )}
      </div>

      <div className="flex gap-1 mt-6 bg-gray-100 dark:bg-gray-800 rounded-lg p-1 w-fit">
        {(['plans', 'meters', 'rules'] as const).map(t => (
          <button key={t} onClick={() => { setTab(t) }}
            className={`px-4 py-1.5 rounded-md text-sm font-medium transition-colors ${
              tab === t ? 'bg-white dark:bg-gray-700 text-gray-900 dark:text-gray-100 shadow-sm' : 'text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-200'
            }`}>
            {t === 'plans' ? `Plans (${plans.length})` : t === 'meters' ? `Meters (${meters.length})` : `Rules (${rules.length})`}
          </button>
        ))}
      </div>

      <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card mt-4">
        {error ? <ErrorState message={error} onRetry={loadAll} />
        : loading ? <LoadingSkeleton rows={5} columns={5} />
        : tab === 'plans' ? (plans.length === 0 ? <Empty label="plans" onAdd={() => setCreateFor('plans')} /> :
          <div className="overflow-x-auto"><table className="w-full"><thead><tr className="border-b border-gray-100 dark:border-gray-800 bg-gray-50 dark:bg-gray-800/50">
            <Th>Name</Th><Th>Code</Th><Th>Interval</Th><Th>Status</Th><Th right>Base Price</Th><Th right>Meters</Th>
          </tr></thead><tbody className="divide-y divide-gray-100 dark:divide-gray-800">
            {plans.map(p => <tr key={p.id} className="hover:bg-gray-50 dark:hover:bg-gray-800/50 cursor-pointer transition-colors group" onClick={(e) => {
              const target = e.target as HTMLElement
              if (target.closest('button, a, input, select')) return
              navigate(`/plans/${p.id}`)
            }}>
              <Td bold><Link to={`/plans/${p.id}`} className="text-gray-900 dark:text-gray-100 group-hover:text-velox-600 transition-colors">{p.name}</Link></Td>
              <Td mono>{p.code}</Td><Td><Badge status={p.billing_interval} /></Td>
              <Td><Badge status={p.status} /></Td><Td right bold>{formatCents(p.base_amount_cents)}</Td>
              <Td right>{p.meter_ids?.length || 0}</Td>
            </tr>)}
          </tbody></table></div>)
        : tab === 'meters' ? (meters.length === 0 ? <Empty label="meters" onAdd={() => setCreateFor('meters')} /> :
          <div className="overflow-x-auto"><table className="w-full"><thead><tr className="border-b border-gray-100 dark:border-gray-800 bg-gray-50 dark:bg-gray-800/50">
            <Th>Name</Th><Th>Key</Th><Th>Unit</Th><Th>Aggregation</Th><Th>Created</Th>
          </tr></thead><tbody className="divide-y divide-gray-100 dark:divide-gray-800">
            {meters.map(m => <tr key={m.id} className="hover:bg-gray-50 dark:hover:bg-gray-800/50 cursor-pointer transition-colors group" onClick={(e) => {
              const target = e.target as HTMLElement
              if (target.closest('button, a, input, select')) return
              navigate(`/meters/${m.id}`)
            }}>
              <Td bold><Link to={`/meters/${m.id}`} className="text-gray-900 dark:text-gray-100 group-hover:text-velox-600 transition-colors">{m.name}</Link></Td>
              <Td mono>{m.key}</Td><Td>{m.unit}</Td>
              <Td><Badge status={m.aggregation} /></Td><Td muted>{formatDateTime(m.created_at)}</Td>
            </tr>)}
          </tbody></table></div>)
        : (rules.length === 0 ? <Empty label="rating rules" onAdd={() => setCreateFor('rules')} /> :
          <div className="overflow-x-auto"><table className="w-full"><thead><tr className="border-b border-gray-100 dark:border-gray-800 bg-gray-50 dark:bg-gray-800/50">
            <Th>Name</Th><Th>Rule Key</Th><Th>Mode</Th><Th>Version</Th><Th right>Price</Th>
          </tr></thead><tbody className="divide-y divide-gray-100 dark:divide-gray-800">
            {rules.map(r => <tr key={r.id} className="hover:bg-gray-50/50 dark:hover:bg-gray-800/50 transition-colors">
              <Td bold>{r.name}</Td><Td mono>{r.rule_key}</Td><Td><Badge status={r.mode} /></Td>
              <Td>v{r.version}</Td>
              <Td right bold>{r.mode === 'flat' ? formatCents(r.flat_amount_cents) : r.mode === 'graduated' ? `${r.graduated_tiers?.length || 0} tiers` : `${r.package_size}/pkg`}</Td>
            </tr>)}
          </tbody></table></div>)}
      </div>

      {createFor === 'rules' && <CreateRuleModal onClose={() => setCreateFor(null)}
        onCreated={() => { setCreateFor(null); loadAll(); toast.success('Rating rule created') }} />}
      {createFor === 'meters' && <CreateMeterModal onClose={() => setCreateFor(null)} rules={rules}
        onCreated={() => { setCreateFor(null); loadAll(); toast.success('Meter created') }} />}
      {createFor === 'plans' && <CreatePlanModal onClose={() => setCreateFor(null)} meters={meters}
        onCreated={() => { setCreateFor(null); loadAll(); toast.success('Plan created') }} />}
    </Layout>
  )
}

function Empty({ label, onAdd }: { label: string; onAdd?: () => void }) {
  return (
    <div className="px-6 py-12 text-center">
      <p className="text-sm font-medium text-gray-900 dark:text-gray-100">No {label} yet</p>
      <p className="text-sm text-gray-600 dark:text-gray-400 mt-1">Get started by creating your first {label.replace(/s$/, '')}</p>
      {onAdd && (
        <button onClick={onAdd}
          className="mt-4 inline-flex items-center gap-2 px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm transition-colors">
          <Plus size={16} /> Add {label.replace(/s$/, '').replace(/^./, c => c.toUpperCase())}
        </button>
      )}
    </div>
  )
}

function Th({ children, right }: { children: React.ReactNode; right?: boolean }) {
  return <th className={`${right ? 'text-right' : 'text-left'} text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3`}>{children}</th>
}

function Td({ children, bold, mono, right, muted }: { children: React.ReactNode; bold?: boolean; mono?: boolean; right?: boolean; muted?: boolean }) {
  return <td className={`px-6 py-3 text-sm ${right ? 'text-right' : ''} ${bold ? 'font-medium text-gray-900' : ''} ${mono ? 'font-mono text-gray-500' : ''} ${muted ? 'text-gray-400' : ''} ${!bold && !mono && !muted ? 'text-gray-500' : ''}`}>{children}</td>
}


function Buttons({ onClose, saving, label }: { onClose: () => void; saving: boolean; label: string }) {
  return (<div className="flex justify-end gap-3 pt-4 border-t border-gray-100 dark:border-gray-800 mt-2">
    <button type="button" onClick={onClose} className="px-4 py-2 border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-300 rounded-lg text-sm font-medium hover:bg-gray-50 dark:hover:bg-gray-800 transition-colors">Cancel</button>
    <button type="submit" disabled={saving}
      className="flex items-center justify-center gap-2 px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow disabled:opacity-50">
      {saving ? (<><Loader2 size={14} className="animate-spin" /> Saving...</>) : label}
    </button>
  </div>)
}

function CreateRuleModal({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const [name, setName] = useState('')
  const [ruleKey, setRuleKey] = useState('')
  const [mode, setMode] = useState('flat')
  const [flatAmount, setFlatAmount] = useState('')
  const [tiers, setTiers] = useState([
    { upTo: '100', price: '0.10' },
    { upTo: '', price: '0.05' },
  ])
  const [packageSize, setPackageSize] = useState('100')
  const [packageAmount, setPackageAmount] = useState('10.00')
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)

  const modeDescriptions: Record<string, string> = {
    flat: 'Fixed price per unit',
    graduated: 'Price decreases as usage increases (tiered)',
    package: 'Charge per block of units',
  }

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setSaving(true); setError('')
    try {
      const payload: Record<string, unknown> = { rule_key: ruleKey, name, mode, currency: getActiveCurrency() }
      if (mode === 'flat') {
        payload.flat_amount_cents = Math.round(parseFloat(flatAmount || '0') * 100)
      }
      if (mode === 'graduated') {
        // Validate tier order: each "up to" must be greater than the previous
        for (let i = 0; i < tiers.length - 1; i++) {
          const current = parseInt(tiers[i].upTo) || 0
          const next = tiers[i + 1].upTo === '' ? Infinity : (parseInt(tiers[i + 1].upTo) || 0)
          if (current <= 0) { setError(`Tier ${i + 1}: "Up to" must be greater than 0`); setSaving(false); return }
          if (next !== Infinity && next <= current) { setError(`Tier ${i + 2}: "Up to" must be greater than ${current}`); setSaving(false); return }
        }
        payload.graduated_tiers = tiers.map(t => ({
          up_to: t.upTo === '' ? 0 : parseInt(t.upTo) || 0,
          unit_amount_cents: Math.round(parseFloat(t.price || '0') * 100),
        }))
      }
      if (mode === 'package') {
        payload.package_size = parseInt(packageSize) || 1
        payload.package_amount_cents = Math.round(parseFloat(packageAmount || '0') * 100)
      }
      await api.createRatingRule(payload as Parameters<typeof api.createRatingRule>[0])
      onCreated()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create rating rule')
    } finally {
      setSaving(false)
    }
  }

  return (
    <Modal open onClose={onClose} title="Create Rating Rule" wide dirty={!!(name || ruleKey || flatAmount)}>
      <form onSubmit={submit} noValidate className="space-y-4">
        <FormField label="Name" required value={name} onChange={e => setName(e.target.value)}
          placeholder="API Call Pricing" maxLength={255} />
        <FormField label="Rule Key" required value={ruleKey} onChange={e => setRuleKey(e.target.value)}
          placeholder="api_calls" maxLength={100} mono
          hint="Matches against event names for usage metering" />

        <div className="grid grid-cols-2 gap-3">
          <FormSelect label="Pricing Model" value={mode}
            onChange={e => setMode(e.target.value)}
            options={[
              { value: 'flat', label: 'Flat Rate' },
              { value: 'graduated', label: 'Graduated Tiers' },
              { value: 'package', label: 'Package' },
            ]} />
        </div>
        <p className="text-xs text-gray-500 -mt-2">{modeDescriptions[mode]}</p>

        {mode === 'flat' && (
          <FormField label={`Price per Unit (${getCurrencySymbol()})`} required type="number" step="0.01" min={0} max={999999.99}
            value={flatAmount} onChange={e => setFlatAmount(e.target.value)}
            placeholder="0.01" hint={`e.g. ${getCurrencySymbol()}0.01 per API call`} />
        )}

        {mode === 'graduated' && (
          <div className="space-y-3">
            <label className="block text-sm font-medium text-gray-700">Pricing Tiers</label>
            <div className="bg-gray-50 dark:bg-gray-800 rounded-lg border border-gray-200 dark:border-gray-700 overflow-hidden">
              <div className="grid grid-cols-[1fr_1fr_36px] gap-0 px-3 py-2 border-b border-gray-200 bg-gray-100">
                <span className="text-xs font-medium text-gray-500 dark:text-gray-400 uppercase tracking-wider">Up to (units)</span>
                <span className="text-xs font-medium text-gray-500 dark:text-gray-400 uppercase tracking-wider">Price per unit ($)</span>
                <span />
              </div>
              {tiers.map((tier, idx) => (
                <div key={idx} className="grid grid-cols-[1fr_1fr_36px] gap-0 px-3 py-2 border-b border-gray-100 last:border-b-0 items-center">
                  <div className="pr-2">
                    <input type="number" min={0} value={tier.upTo}
                      onChange={e => setTiers(t => t.map((r, i) => i === idx ? { ...r, upTo: e.target.value } : r))}
                      placeholder={idx === tiers.length - 1 ? 'Unlimited' : '1000'}
                      className="w-full px-2 py-1.5 border border-gray-200 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-velox-500" />
                  </div>
                  <div className="pr-2">
                    <input type="number" step="0.01" min={0} value={tier.price}
                      onChange={e => setTiers(t => t.map((r, i) => i === idx ? { ...r, price: e.target.value } : r))}
                      placeholder="0.01"
                      className="w-full px-2 py-1.5 border border-gray-200 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-velox-500" />
                  </div>
                  <div className="flex justify-center">
                    {tiers.length > 1 && (
                      <button type="button" onClick={() => setTiers(t => t.filter((_, i) => i !== idx))}
                        className="text-gray-300 hover:text-red-500 transition-colors">
                        <Trash2 size={14} />
                      </button>
                    )}
                  </div>
                </div>
              ))}
            </div>
            <button type="button" onClick={() => setTiers(t => [...t, { upTo: '', price: '' }])}
              className="text-sm font-medium text-velox-600 hover:text-velox-700 transition-colors">
              + Add tier
            </button>
            <p className="text-xs text-gray-500">Leave "Up to" empty on the last tier for unlimited. Tiers are evaluated in order.</p>
          </div>
        )}

        {mode === 'package' && (
          <div className="grid grid-cols-2 gap-3">
            <FormField label="Units per Package" required type="number" min={1}
              value={packageSize} onChange={e => setPackageSize(e.target.value)}
              placeholder="100" hint="e.g. 100 API calls per package" />
            <FormField label={`Price per Package (${getCurrencySymbol()})`} required type="number" step="0.01" min={0} max={999999.99}
              value={packageAmount} onChange={e => setPackageAmount(e.target.value)}
              placeholder="10.00" hint={`e.g. ${getCurrencySymbol()}10.00 per 100 calls`} />
          </div>
        )}

        {error && <p className="text-sm text-red-600 bg-red-50 border border-red-200 rounded-lg px-3 py-2">{error}</p>}
        <Buttons onClose={onClose} saving={saving} label="Create Rule" />
      </form>
    </Modal>
  )
}

function CreateMeterModal({ onClose, onCreated, rules }: { onClose: () => void; onCreated: () => void; rules: RatingRule[] }) {
  const [form, setForm] = useState({ key: '', name: '', unit: 'unit', aggregation: 'sum', rating_rule_version_id: '' })
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault(); setSaving(true); setError('')
    try { await api.createMeter(form); onCreated() }
    catch (err) { setError(err instanceof Error ? err.message : 'Failed to create meter') }
    finally { setSaving(false) }
  }

  const aggregationDescriptions: Record<string, string> = {
    sum: 'Add up all values in the billing period',
    count: 'Count the number of events',
    max: 'Use the highest value seen',
    last: 'Use the most recent value',
  }

  return (
    <Modal open onClose={onClose} title="Create Meter">
      <form onSubmit={submit} noValidate className="space-y-4">
        <FormField label="Name" required value={form.name} onChange={e => setForm(f => ({ ...f, name: e.target.value }))}
          placeholder="API Calls" maxLength={255} />
        <FormField label="Key" required value={form.key} onChange={e => setForm(f => ({ ...f, key: e.target.value }))}
          placeholder="api_calls" maxLength={100} mono
          hint="Used to match incoming usage events (e.g. api_calls, messages_sent)" />
        <div className="grid grid-cols-2 gap-3">
          <FormField label="Unit Label" value={form.unit} onChange={e => setForm(f => ({ ...f, unit: e.target.value }))}
            placeholder="unit" hint="e.g. call, request, GB" />
          <div>
            <FormSelect label="Aggregation" value={form.aggregation}
              onChange={e => setForm(f => ({ ...f, aggregation: e.target.value }))}
              options={[
                { value: 'sum', label: 'Sum' },
                { value: 'count', label: 'Count' },
                { value: 'max', label: 'Maximum' },
                { value: 'last', label: 'Latest Value' },
              ]} />
            <p className="text-xs text-gray-500 mt-1">{aggregationDescriptions[form.aggregation]}</p>
          </div>
        </div>
        {rules.length > 0 && (
          <FormSelect label="Rating Rule" value={form.rating_rule_version_id}
            onChange={e => setForm(f => ({ ...f, rating_rule_version_id: e.target.value }))}
            placeholder="None (assign later)"
            options={rules.map(r => ({ value: r.id, label: `${r.name} (${r.mode})` }))} />
        )}
        {error && <p className="text-sm text-red-600 bg-red-50 border border-red-200 rounded-lg px-3 py-2">{error}</p>}
        <Buttons onClose={onClose} saving={saving} label="Create Meter" />
      </form>
    </Modal>
  )
}

function CreatePlanModal({ onClose, onCreated, meters }: { onClose: () => void; onCreated: () => void; meters: Meter[] }) {
  const [name, setName] = useState('')
  const [code, setCode] = useState('')
  const [interval, setInterval] = useState('monthly')
  const [basePrice, setBasePrice] = useState('')
  const [meterIds, setMeterIds] = useState<string[]>([])
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault(); setSaving(true); setError('')
    try {
      await api.createPlan({
        name, code, currency: getActiveCurrency(), billing_interval: interval,
        base_amount_cents: Math.round(parseFloat(basePrice || '0') * 100),
        meter_ids: meterIds,
      })
      onCreated()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create plan')
    } finally {
      setSaving(false)
    }
  }

  return (
    <Modal open onClose={onClose} title="Create Plan">
      <form onSubmit={submit} noValidate className="space-y-4">
        <FormField label="Plan Name" required value={name} onChange={e => setName(e.target.value)}
          placeholder="Pro Plan" maxLength={255} />
        <FormField label="Code" required value={code} onChange={e => setCode(e.target.value)}
          placeholder="pro" maxLength={100} mono
          hint="Unique identifier used in API calls" />
        <div className="grid grid-cols-2 gap-3">
          <FormSelect label="Billing Interval" value={interval}
            onChange={e => setInterval(e.target.value)}
            options={[
              { value: 'monthly', label: 'Monthly' },
              { value: 'yearly', label: 'Yearly' },
            ]} />
        </div>
        <FormField label={`Base Price (${getCurrencySymbol()})`} required type="number" step="0.01" min={0} max={999999.99}
          value={basePrice} onChange={e => setBasePrice(e.target.value)}
          placeholder="49.00" hint={`Fixed ${interval} charge before usage fees`} />
        {meters.length > 0 && (
          <div className="border-t border-gray-100 dark:border-gray-800 pt-4">
            <p className="text-xs font-semibold text-gray-400 uppercase tracking-wider mb-3">Usage Meters</p>
            <div className="space-y-0 rounded-lg border border-gray-200 divide-y divide-gray-100 dark:divide-gray-800 overflow-hidden">
              {meters.map(m => (
                <label key={m.id} className="flex items-center gap-3 px-3 py-2.5 text-sm cursor-pointer hover:bg-gray-50 transition-colors">
                  <input type="checkbox" checked={meterIds.includes(m.id)}
                    className="rounded border-gray-300 text-velox-600 focus:ring-velox-500"
                    onChange={e => setMeterIds(e.target.checked ? [...meterIds, m.id] : meterIds.filter(id => id !== m.id))} />
                  <div className="flex-1 min-w-0">
                    <span className="text-gray-900">{m.name}</span>
                    <span className="text-gray-400 font-mono text-xs ml-2">{m.key}</span>
                  </div>
                  <span className="text-xs text-gray-500">{m.aggregation} · {m.unit}</span>
                </label>
              ))}
            </div>
          </div>
        )}
        {error && <p className="text-sm text-red-600 bg-red-50 border border-red-200 rounded-lg px-3 py-2">{error}</p>}
        <Buttons onClose={onClose} saving={saving} label="Create Plan" />
      </form>
    </Modal>
  )
}
