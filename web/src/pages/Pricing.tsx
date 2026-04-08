import { useEffect, useState } from 'react'
import { api, formatCents, formatDate, type Meter, type Plan, type RatingRule } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'
import { Modal } from '@/components/Modal'
import { ErrorState } from '@/components/ErrorState'
import { useToast } from '@/components/Toast'
import { Plus } from 'lucide-react'

export function PricingPage() {
  const [meters, setMeters] = useState<Meter[]>([])
  const [plans, setPlans] = useState<Plan[]>([])
  const [rules, setRules] = useState<RatingRule[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [tab, setTab] = useState<'plans' | 'meters' | 'rules'>('plans')
  const [showCreate, setShowCreate] = useState(false)
  const toast = useToast()

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
      <div className="flex justify-between items-center">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900">Pricing</h1>
          <p className="text-sm text-gray-500 mt-1">Plans, meters, and rating rules</p>
        </div>
        <button onClick={() => setShowCreate(true)}
          className="flex items-center gap-2 px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow transition-colors">
          <Plus size={16} />
          {tab === 'plans' ? 'Add Plan' : tab === 'meters' ? 'Add Meter' : 'Add Rule'}
        </button>
      </div>

      <div className="flex gap-1 mt-6 bg-gray-100 rounded-lg p-1 w-fit">
        {(['plans', 'meters', 'rules'] as const).map(t => (
          <button key={t} onClick={() => { setTab(t); setShowCreate(false) }}
            className={`px-4 py-1.5 rounded-md text-sm font-medium transition-colors ${
              tab === t ? 'bg-white text-gray-900 shadow-sm' : 'text-gray-500 hover:text-gray-700'
            }`}>
            {t === 'plans' ? `Plans (${plans.length})` : t === 'meters' ? `Meters (${meters.length})` : `Rules (${rules.length})`}
          </button>
        ))}
      </div>

      <div className="bg-white rounded-xl shadow-card mt-4">
        {error ? <ErrorState message={error} onRetry={loadAll} />
        : loading ? <div className="p-8 text-gray-400 animate-pulse">Loading...</div>
        : tab === 'plans' ? (plans.length === 0 ? <Empty label="plans" /> :
          <div className="overflow-x-auto"><table className="w-full"><thead><tr className="border-b border-gray-100">
            <Th>Name</Th><Th>Code</Th><Th>Interval</Th><Th>Status</Th><Th right>Base Price</Th><Th right>Meters</Th>
          </tr></thead><tbody className="divide-y divide-gray-50">
            {plans.map(p => <tr key={p.id} className="hover:bg-gray-50">
              <Td bold>{p.name}</Td><Td mono>{p.code}</Td><Td><Badge status={p.billing_interval} /></Td>
              <Td><Badge status={p.status} /></Td><Td right bold>{formatCents(p.base_amount_cents)}</Td>
              <Td right>{p.meter_ids?.length || 0}</Td>
            </tr>)}
          </tbody></table></div>)
        : tab === 'meters' ? (meters.length === 0 ? <Empty label="meters" /> :
          <div className="overflow-x-auto"><table className="w-full"><thead><tr className="border-b border-gray-100">
            <Th>Name</Th><Th>Key</Th><Th>Unit</Th><Th>Aggregation</Th><Th>Created</Th>
          </tr></thead><tbody className="divide-y divide-gray-50">
            {meters.map(m => <tr key={m.id} className="hover:bg-gray-50">
              <Td bold>{m.name}</Td><Td mono>{m.key}</Td><Td>{m.unit}</Td>
              <Td><Badge status={m.aggregation} /></Td><Td muted>{formatDate(m.created_at)}</Td>
            </tr>)}
          </tbody></table></div>)
        : (rules.length === 0 ? <Empty label="rating rules" /> :
          <div className="overflow-x-auto"><table className="w-full"><thead><tr className="border-b border-gray-100">
            <Th>Name</Th><Th>Rule Key</Th><Th>Mode</Th><Th>Version</Th><Th right>Price</Th>
          </tr></thead><tbody className="divide-y divide-gray-50">
            {rules.map(r => <tr key={r.id} className="hover:bg-gray-50">
              <Td bold>{r.name}</Td><Td mono>{r.rule_key}</Td><Td><Badge status={r.mode} /></Td>
              <Td>v{r.version}</Td>
              <Td right bold>{r.mode === 'flat' ? formatCents(r.flat_amount_cents) : r.mode === 'graduated' ? `${r.graduated_tiers?.length || 0} tiers` : `${r.package_size}/pkg`}</Td>
            </tr>)}
          </tbody></table></div>)}
      </div>

      {showCreate && tab === 'rules' && <CreateRuleModal onClose={() => setShowCreate(false)}
        onCreated={() => { setShowCreate(false); loadAll(); toast.success('Rating rule created') }} />}
      {showCreate && tab === 'meters' && <CreateMeterModal onClose={() => setShowCreate(false)} rules={rules}
        onCreated={() => { setShowCreate(false); loadAll(); toast.success('Meter created') }} />}
      {showCreate && tab === 'plans' && <CreatePlanModal onClose={() => setShowCreate(false)} meters={meters}
        onCreated={() => { setShowCreate(false); loadAll(); toast.success('Plan created') }} />}
    </Layout>
  )
}

function Empty({ label }: { label: string }) {
  return <p className="px-6 py-8 text-sm text-gray-400 text-center">No {label} yet</p>
}

function Th({ children, right }: { children: React.ReactNode; right?: boolean }) {
  return <th className={`${right ? 'text-right' : 'text-left'} text-xs font-medium text-gray-500 px-6 py-3`}>{children}</th>
}

function Td({ children, bold, mono, right, muted }: { children: React.ReactNode; bold?: boolean; mono?: boolean; right?: boolean; muted?: boolean }) {
  return <td className={`px-6 py-3 text-sm ${right ? 'text-right' : ''} ${bold ? 'font-medium text-gray-900' : ''} ${mono ? 'font-mono text-gray-500' : ''} ${muted ? 'text-gray-400' : ''} ${!bold && !mono && !muted ? 'text-gray-500' : ''}`}>{children}</td>
}

function Field({ label, value, onChange, placeholder, required, mono, type, maxLength, min, max, step }: {
  label: string; value: string; onChange: (v: string) => void; placeholder?: string; required?: boolean; mono?: boolean; type?: string;
  maxLength?: number; min?: number; max?: number; step?: string
}) {
  return (<div>
    <label className="block text-sm font-medium text-gray-700 mb-1">{label}{required && <span className="text-red-500"> *</span>}</label>
    <input type={type || 'text'} value={value} onChange={e => onChange(e.target.value)}
      className={`w-full px-3 py-2 border border-gray-200 rounded-lg shadow-sm text-sm focus:outline-none focus:ring-2 focus:ring-velox-500 ${mono ? 'font-mono' : ''}`}
      placeholder={placeholder} required={required} maxLength={maxLength} min={min} max={max} step={step} />
  </div>)
}

function Select({ label, value, onChange, options }: {
  label: string; value: string; onChange: (v: string) => void; options: string[][]
}) {
  return (<div>
    <label className="block text-sm font-medium text-gray-700 mb-1">{label}</label>
    <select value={value} onChange={e => onChange(e.target.value)}
      className="w-full px-3 py-2 border border-gray-200 rounded-lg shadow-sm text-sm focus:outline-none focus:ring-2 focus:ring-velox-500 bg-white">
      {options.map(([v, l]) => <option key={v} value={v}>{l}</option>)}
    </select>
  </div>)
}

function Buttons({ onClose, saving, label }: { onClose: () => void; saving: boolean; label: string }) {
  return (<div className="flex justify-end gap-3 pt-4 border-t border-gray-100 mt-1">
    <button type="button" onClick={onClose} className="px-4 py-2 border border-gray-300 text-gray-700 rounded-lg text-sm font-medium hover:bg-gray-50 transition-colors">Cancel</button>
    <button type="submit" disabled={saving}
      className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow disabled:opacity-50">
      {saving ? 'Saving...' : label}
    </button>
  </div>)
}

function CreateRuleModal({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const [form, setForm] = useState({
    rule_key: '', name: '', mode: 'flat', currency: 'USD', flat_amount_cents: 0,
    graduated_tiers: [{ up_to: 100, unit_amount_cents: 10 }, { up_to: 0, unit_amount_cents: 5 }] as { up_to: number; unit_amount_cents: number }[],
    package_size: 100, package_amount_cents: 1000,
  })
  const [error, setError] = useState(''); const [saving, setSaving] = useState(false)
  const submit = async (e: React.FormEvent) => {
    e.preventDefault(); setSaving(true); setError('')
    try {
      const payload: Record<string, unknown> = { rule_key: form.rule_key, name: form.name, mode: form.mode, currency: form.currency }
      if (form.mode === 'flat') { payload.flat_amount_cents = form.flat_amount_cents }
      if (form.mode === 'graduated') { payload.graduated_tiers = form.graduated_tiers }
      if (form.mode === 'package') { payload.package_size = form.package_size; payload.package_amount_cents = form.package_amount_cents }
      await api.createRatingRule(payload as Parameters<typeof api.createRatingRule>[0]); onCreated()
    }
    catch (err) { setError(err instanceof Error ? err.message : 'Failed') }
    finally { setSaving(false) }
  }

  const addTier = () => setForm(f => ({ ...f, graduated_tiers: [...f.graduated_tiers, { up_to: 0, unit_amount_cents: 0 }] }))
  const removeTier = (idx: number) => setForm(f => ({ ...f, graduated_tiers: f.graduated_tiers.filter((_, i) => i !== idx) }))
  const updateTier = (idx: number, field: 'up_to' | 'unit_amount_cents', value: number) =>
    setForm(f => ({ ...f, graduated_tiers: f.graduated_tiers.map((t, i) => i === idx ? { ...t, [field]: value } : t) }))

  return (<Modal open onClose={onClose} title="Create Rating Rule"><form onSubmit={submit} noValidate className="space-y-3">
    <Field label="Name" value={form.name} onChange={v => setForm(f => ({ ...f, name: v }))} placeholder="API Call Pricing" required maxLength={255} />
    <Field label="Rule Key" value={form.rule_key} onChange={v => setForm(f => ({ ...f, rule_key: v }))} placeholder="api_calls" mono required maxLength={100} />
    <div className="grid grid-cols-2 gap-3">
      <Select label="Mode" value={form.mode} onChange={v => setForm(f => ({ ...f, mode: v }))} options={[['flat', 'Flat'], ['graduated', 'Graduated'], ['package', 'Package']]} />
      <Field label="Currency" value={form.currency} onChange={v => setForm(f => ({ ...f, currency: v }))} />
    </div>
    {form.mode === 'flat' && <Field label="Amount ($)" value={String((form.flat_amount_cents / 100).toFixed(2))} type="number" onChange={v => setForm(f => ({ ...f, flat_amount_cents: Math.round(parseFloat(v) * 100) || 0 }))} min={0} step="0.01" max={999999.99} />}
    {form.mode === 'graduated' && (
      <div>
        <label className="block text-sm font-medium text-gray-700 mb-2">Graduated Tiers</label>
        <div className="space-y-2">
          {form.graduated_tiers.map((tier, idx) => (
            <div key={idx} className="flex items-center gap-2">
              <div className="flex-1">
                <input type="number" value={tier.up_to} onChange={e => updateTier(idx, 'up_to', parseInt(e.target.value) || 0)}
                  className="w-full px-3 py-2 border border-gray-200 rounded-lg shadow-sm text-sm focus:outline-none focus:ring-2 focus:ring-velox-500"
                  placeholder="Up to (0 = unlimited)" min={0} />
                <span className="text-xs text-gray-400">{tier.up_to === 0 ? 'Unlimited' : `Up to ${tier.up_to}`}</span>
              </div>
              <div className="flex-1">
                <input type="number" step="0.01" min="0" max={999999.99} value={(tier.unit_amount_cents / 100).toFixed(2)} onChange={e => updateTier(idx, 'unit_amount_cents', Math.round(parseFloat(e.target.value) * 100) || 0)}
                  className="w-full px-3 py-2 border border-gray-200 rounded-lg shadow-sm text-sm focus:outline-none focus:ring-2 focus:ring-velox-500"
                  placeholder="Price per unit ($)" />
                <span className="text-xs text-gray-400">$/unit</span>
              </div>
              {form.graduated_tiers.length > 1 && (
                <button type="button" onClick={() => removeTier(idx)} className="text-red-400 hover:text-red-600 text-sm px-1">&times;</button>
              )}
            </div>
          ))}
        </div>
        <button type="button" onClick={addTier} className="mt-2 text-xs text-velox-600 hover:underline">+ Add tier</button>
      </div>
    )}
    {form.mode === 'package' && (
      <div className="space-y-3">
        <Field label="Package size" value={String(form.package_size)} type="number" onChange={v => setForm(f => ({ ...f, package_size: parseInt(v) || 0 }))} placeholder="100" min={1} />
        <Field label="Package amount ($)" value={String((form.package_amount_cents / 100).toFixed(2))} type="number" onChange={v => setForm(f => ({ ...f, package_amount_cents: Math.round(parseFloat(v) * 100) || 0 }))} placeholder="10.00" max={999999.99} step="0.01" />
      </div>
    )}
    {error && <p className="text-red-600 text-xs">{error}</p>}
    <Buttons onClose={onClose} saving={saving} label="Create Rule" />
  </form></Modal>)
}

function CreateMeterModal({ onClose, onCreated, rules }: { onClose: () => void; onCreated: () => void; rules: RatingRule[] }) {
  const [form, setForm] = useState({ key: '', name: '', unit: 'unit', aggregation: 'sum', rating_rule_version_id: '' })
  const [error, setError] = useState(''); const [saving, setSaving] = useState(false)
  const submit = async (e: React.FormEvent) => {
    e.preventDefault(); setSaving(true); setError('')
    try { await api.createMeter(form); onCreated() }
    catch (err) { setError(err instanceof Error ? err.message : 'Failed') }
    finally { setSaving(false) }
  }
  return (<Modal open onClose={onClose} title="Create Meter"><form onSubmit={submit} noValidate className="space-y-3">
    <Field label="Name" value={form.name} onChange={v => setForm(f => ({ ...f, name: v }))} placeholder="API Calls" required maxLength={255} />
    <Field label="Key" value={form.key} onChange={v => setForm(f => ({ ...f, key: v }))} placeholder="api_calls" mono required maxLength={100} />
    <div className="grid grid-cols-2 gap-3">
      <Field label="Unit" value={form.unit} onChange={v => setForm(f => ({ ...f, unit: v }))} />
      <Select label="Aggregation" value={form.aggregation} onChange={v => setForm(f => ({ ...f, aggregation: v }))} options={[['sum', 'Sum'], ['count', 'Count'], ['max', 'Max'], ['last', 'Last']]} />
    </div>
    {rules.length > 0 && <Select label="Rating Rule" value={form.rating_rule_version_id} onChange={v => setForm(f => ({ ...f, rating_rule_version_id: v }))} options={[['', '(none)'], ...rules.map(r => [r.id, `${r.name} (${r.mode})`])]} />}
    {error && <p className="text-red-600 text-xs">{error}</p>}
    <Buttons onClose={onClose} saving={saving} label="Create Meter" />
  </form></Modal>)
}

function CreatePlanModal({ onClose, onCreated, meters }: { onClose: () => void; onCreated: () => void; meters: Meter[] }) {
  const [form, setForm] = useState({ code: '', name: '', currency: 'USD', billing_interval: 'monthly', base_amount_cents: 0, meter_ids: [] as string[] })
  const [error, setError] = useState(''); const [saving, setSaving] = useState(false)
  const submit = async (e: React.FormEvent) => {
    e.preventDefault(); setSaving(true); setError('')
    try { await api.createPlan(form); onCreated() }
    catch (err) { setError(err instanceof Error ? err.message : 'Failed') }
    finally { setSaving(false) }
  }
  return (<Modal open onClose={onClose} title="Create Plan"><form onSubmit={submit} noValidate className="space-y-3">
    <Field label="Name" value={form.name} onChange={v => setForm(f => ({ ...f, name: v }))} placeholder="Pro Plan" required maxLength={255} />
    <Field label="Code" value={form.code} onChange={v => setForm(f => ({ ...f, code: v }))} placeholder="pro" mono required maxLength={100} />
    <div className="grid grid-cols-2 gap-3">
      <Select label="Interval" value={form.billing_interval} onChange={v => setForm(f => ({ ...f, billing_interval: v }))} options={[['monthly', 'Monthly'], ['yearly', 'Yearly']]} />
      <Field label="Base Price ($)" value={String((form.base_amount_cents / 100).toFixed(2))} type="number" onChange={v => setForm(f => ({ ...f, base_amount_cents: Math.round(parseFloat(v) * 100) || 0 }))} min={0} step="0.01" max={999999.99} />
    </div>
    {meters.length > 0 && <div>
      <label className="block text-sm font-medium text-gray-700 mb-1">Meters</label>
      <div className="space-y-1">{meters.map(m => (
        <label key={m.id} className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={form.meter_ids.includes(m.id)}
            onChange={e => setForm(f => ({ ...f, meter_ids: e.target.checked ? [...f.meter_ids, m.id] : f.meter_ids.filter(id => id !== m.id) }))} />
          {m.name} ({m.key})
        </label>
      ))}</div>
    </div>}
    {error && <p className="text-red-600 text-xs">{error}</p>}
    <Buttons onClose={onClose} saving={saving} label="Create Plan" />
  </form></Modal>)
}
