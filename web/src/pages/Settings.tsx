import { useEffect, useState, useMemo } from 'react'
import { api, setActiveCurrency, formatCents } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { ErrorState } from '@/components/ErrorState'
import { toast } from 'sonner'
import { useFormValidation, rules } from '@/hooks/useFormValidation'
import { Building2, FileText, Receipt, Globe, Check, AlertCircle, Loader2 } from 'lucide-react'
import { Breadcrumbs } from '@/components/Breadcrumbs'

const CURRENCIES = [
  { value: 'USD', label: 'US Dollar', symbol: '$' },
  { value: 'EUR', label: 'Euro', symbol: '\u20AC' },
  { value: 'GBP', label: 'British Pound', symbol: '\u00A3' },
  { value: 'INR', label: 'Indian Rupee', symbol: '\u20B9' },
  { value: 'CAD', label: 'Canadian Dollar', symbol: 'CA$' },
  { value: 'AUD', label: 'Australian Dollar', symbol: 'A$' },
  { value: 'JPY', label: 'Japanese Yen', symbol: '\u00A5' },
  { value: 'CHF', label: 'Swiss Franc', symbol: 'CHF' },
  { value: 'SGD', label: 'Singapore Dollar', symbol: 'S$' },
  { value: 'BRL', label: 'Brazilian Real', symbol: 'R$' },
  { value: 'MXN', label: 'Mexican Peso', symbol: 'MX$' },
  { value: 'KRW', label: 'Korean Won', symbol: '\u20A9' },
]

const TIMEZONES = [
  'UTC', 'America/New_York', 'America/Chicago', 'America/Denver', 'America/Los_Angeles',
  'America/Toronto', 'America/Sao_Paulo', 'Europe/London', 'Europe/Paris', 'Europe/Berlin',
  'Asia/Tokyo', 'Asia/Shanghai', 'Asia/Kolkata', 'Asia/Singapore', 'Asia/Dubai',
  'Australia/Sydney', 'Pacific/Auckland',
]

export function SettingsPage() {
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [savedForm, setSavedForm] = useState<string>('')

  const [form, setForm] = useState({
    company_name: '', company_email: '', company_phone: '', company_address: '',
    logo_url: '',
    invoice_prefix: '', net_payment_terms: 0, tax_rate: 0, tax_name: '',
    default_currency: '', timezone: '',
  })

  const hasChanges = savedForm !== '' && JSON.stringify(form) !== savedForm

  useEffect(() => {
    const handleBeforeUnload = (e: BeforeUnloadEvent) => {
      if (hasChanges) { e.preventDefault(); e.returnValue = '' }
    }
    window.addEventListener('beforeunload', handleBeforeUnload)
    return () => window.removeEventListener('beforeunload', handleBeforeUnload)
  }, [hasChanges])

  const fieldRules = useMemo(() => ({
    company_email: [rules.email()],
    company_phone: [rules.phone()],
  }), [])
  const { onBlur, validateAll, fieldError, registerRef } = useFormValidation(fieldRules)

  const loadSettings = () => {
    setLoading(true); setError(null)
    api.getSettings().then(s => {
      const f = {
        company_name: s.company_name || '', company_email: s.company_email || '',
        company_phone: s.company_phone || '', company_address: s.company_address || '',
        logo_url: s.logo_url || '',
        invoice_prefix: s.invoice_prefix || '', net_payment_terms: s.net_payment_terms || 0,
        tax_rate: s.tax_rate || 0, tax_name: s.tax_name || '',
        default_currency: s.default_currency || '', timezone: s.timezone || '',
      }
      setForm(f); setSavedForm(JSON.stringify(f)); setLoading(false)
    }).catch(err => { setError(err instanceof Error ? err.message : 'Failed to load settings'); setLoading(false) })
  }

  useEffect(() => { loadSettings() }, [])

  const handleSave = async () => {
    if (!validateAll(form)) return
    setSaving(true)
    try {
      const updated = await api.updateSettings(form)
      const f = {
        company_name: updated.company_name || '', company_email: updated.company_email || '',
        company_phone: updated.company_phone || '', company_address: updated.company_address || '',
        logo_url: updated.logo_url || '',
        invoice_prefix: updated.invoice_prefix || '', net_payment_terms: updated.net_payment_terms || 0,
        tax_rate: updated.tax_rate || 0, tax_name: updated.tax_name || '',
        default_currency: updated.default_currency || '', timezone: updated.timezone || '',
      }
      setForm(f); setSavedForm(JSON.stringify(f))
      if (updated.default_currency) setActiveCurrency(updated.default_currency)
      toast.success('Settings saved')
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to save settings')
    } finally { setSaving(false) }
  }

  if (loading) return <Layout><h1 className="text-2xl font-semibold text-gray-900 dark:text-gray-100">Settings</h1><div className="mt-6"><LoadingSkeleton variant="detail" /></div></Layout>
  if (error) return <Layout><h1 className="text-2xl font-semibold text-gray-900 dark:text-gray-100">Settings</h1><div className="mt-6"><ErrorState message={error} onRetry={loadSettings} /></div></Layout>

  const currencyObj = CURRENCIES.find(c => c.value === form.default_currency)
  const symbol = currencyObj?.symbol || '$'

  const inputCls = 'w-full px-3 py-2 border border-gray-200 dark:border-gray-700 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-velox-500 focus:border-transparent bg-white dark:bg-gray-800 dark:text-gray-100 transition-colors'
  const labelCls = 'block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1'
  const hintCls = 'text-xs text-gray-500 dark:text-gray-500 mt-1'

  return (
    <Layout>
      <Breadcrumbs items={[{ label: 'System' }, { label: 'Settings' }]} />
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900 dark:text-gray-100">Settings</h1>
          <p className="text-sm text-gray-600 dark:text-gray-400 mt-1">Configure your billing tenant</p>
        </div>
        {!hasChanges && savedForm && (
          <span className="text-sm text-emerald-600 dark:text-emerald-400 flex items-center gap-1.5 bg-emerald-50 dark:bg-emerald-900/20 px-3 py-1.5 rounded-lg">
            <Check size={14} /> Saved
          </span>
        )}
      </div>

      <div className="max-w-3xl mt-6 space-y-8">

        {/* ─── Business Details ─── */}
        <section>
          <div className="flex items-center gap-2 mb-4">
            <div className="w-8 h-8 rounded-lg bg-velox-50 dark:bg-velox-900/20 flex items-center justify-center">
              <Building2 size={16} className="text-velox-600 dark:text-velox-400" />
            </div>
            <div>
              <h2 className="text-sm font-semibold text-gray-900 dark:text-gray-100">Business Details</h2>
              <p className="text-xs text-gray-500">Appears on invoices and customer-facing documents</p>
            </div>
          </div>
          <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card border border-gray-100 dark:border-gray-800 p-6">
            <div className="grid grid-cols-1 md:grid-cols-2 gap-5">
              <div>
                <label className={labelCls}>Business Name</label>
                <input type="text" value={form.company_name} placeholder="Acme Inc." maxLength={255}
                  onChange={e => setForm(f => ({ ...f, company_name: e.target.value }))}
                  className={inputCls} />
                <p className={hintCls}>Displayed on invoice headers</p>
              </div>
              <div>
                <label className={labelCls}>Email</label>
                <input type="email" value={form.company_email} placeholder="billing@acme.com" maxLength={254}
                  ref={registerRef('company_email')}
                  onChange={e => setForm(f => ({ ...f, company_email: e.target.value }))}
                  onBlur={() => onBlur('company_email', form.company_email)}
                  className={`${inputCls} ${fieldError('company_email') ? 'border-red-300 dark:border-red-700' : ''}`} />
                {fieldError('company_email') && <p className="text-xs text-red-600 mt-1">{fieldError('company_email')}</p>}
                <p className={hintCls}>Reply-to address on invoice emails</p>
              </div>
              <div>
                <label className={labelCls}>Phone</label>
                <input type="tel" value={form.company_phone} placeholder="+1 (555) 123-4567" maxLength={20}
                  ref={registerRef('company_phone')}
                  onChange={e => setForm(f => ({ ...f, company_phone: e.target.value }))}
                  onBlur={() => onBlur('company_phone', form.company_phone)}
                  className={`${inputCls} ${fieldError('company_phone') ? 'border-red-300 dark:border-red-700' : ''}`} />
                {fieldError('company_phone') && <p className="text-xs text-red-600 mt-1">{fieldError('company_phone')}</p>}
              </div>
              <div>
                <label className={labelCls}>Logo URL</label>
                <input type="url" value={form.logo_url} placeholder="https://acme.com/logo.png" maxLength={500}
                  onChange={e => setForm(f => ({ ...f, logo_url: e.target.value }))}
                  className={inputCls} />
                <p className={hintCls}>Used on invoice PDFs</p>
              </div>
              <div className="md:col-span-2">
                <label className={labelCls}>Address</label>
                <textarea value={form.company_address}
                  onChange={e => setForm(f => ({ ...f, company_address: e.target.value }))}
                  className={inputCls} rows={2}
                  placeholder={"123 Main St\nSan Francisco, CA 94105"} maxLength={500} />
                <p className={hintCls}>Shown in the "From" section on invoice PDFs</p>
              </div>
            </div>
          </div>
        </section>

        {/* ─── Invoice & Billing ─── */}
        <section>
          <div className="flex items-center gap-2 mb-4">
            <div className="w-8 h-8 rounded-lg bg-blue-50 dark:bg-blue-900/20 flex items-center justify-center">
              <FileText size={16} className="text-blue-600 dark:text-blue-400" />
            </div>
            <div>
              <h2 className="text-sm font-semibold text-gray-900 dark:text-gray-100">Invoice & Billing</h2>
              <p className="text-xs text-gray-500">Currency, invoice numbering, and payment terms</p>
            </div>
          </div>
          <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card border border-gray-100 dark:border-gray-800 p-6">
            <div className="grid grid-cols-1 md:grid-cols-2 gap-5">
              <div>
                <label className={labelCls}>Currency</label>
                <select value={form.default_currency}
                  onChange={e => setForm(f => ({ ...f, default_currency: e.target.value }))}
                  className={inputCls}>
                  <option value="">Select currency...</option>
                  {CURRENCIES.map(c => <option key={c.value} value={c.value}>{c.symbol} {c.label} ({c.value})</option>)}
                </select>
                <p className={hintCls}>Used across all invoices, plans, and charges</p>
              </div>
              <div>
                <label className={labelCls}>Timezone</label>
                <select value={form.timezone}
                  onChange={e => setForm(f => ({ ...f, timezone: e.target.value }))}
                  className={inputCls}>
                  <option value="">Select timezone...</option>
                  {TIMEZONES.map(tz => <option key={tz} value={tz}>{tz.replace(/_/g, ' ')}</option>)}
                </select>
                <p className={hintCls}>Used for billing cycle boundaries and reports</p>
              </div>
              <div>
                <label className={labelCls}>Invoice Prefix</label>
                <input type="text" value={form.invoice_prefix} maxLength={20}
                  onChange={e => setForm(f => ({ ...f, invoice_prefix: e.target.value.toUpperCase() }))}
                  className={inputCls + ' font-mono uppercase'} placeholder="INV" />
                {form.invoice_prefix && (
                  <p className={hintCls + ' font-mono'}>Preview: {form.invoice_prefix}-000001</p>
                )}
              </div>
              <div>
                <label className={labelCls}>Payment Terms</label>
                <div className="relative">
                  <input type="number" min={0} max={365} value={form.net_payment_terms}
                    onChange={e => setForm(f => ({ ...f, net_payment_terms: parseInt(e.target.value) || 0 }))}
                    className={inputCls + ' pr-16'} />
                  <span className="absolute right-3 top-1/2 -translate-y-1/2 text-sm text-gray-400 pointer-events-none">days</span>
                </div>
                <p className={hintCls}>Days after issue before payment is due</p>
              </div>
            </div>
          </div>
        </section>

        {/* ─── Tax ─── */}
        <section>
          <div className="flex items-center gap-2 mb-4">
            <div className="w-8 h-8 rounded-lg bg-amber-50 dark:bg-amber-900/20 flex items-center justify-center">
              <Receipt size={16} className="text-amber-600 dark:text-amber-400" />
            </div>
            <div>
              <h2 className="text-sm font-semibold text-gray-900 dark:text-gray-100">Tax</h2>
              <p className="text-xs text-gray-500">Default tax rate applied to all invoices. Per-customer overrides available on billing profiles.</p>
            </div>
          </div>
          <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card border border-gray-100 dark:border-gray-800 p-6">
            <div className="grid grid-cols-1 md:grid-cols-2 gap-5">
              <div>
                <label className={labelCls}>Tax Name</label>
                <input type="text" value={form.tax_name} maxLength={50} placeholder="e.g. GST, VAT, Sales Tax"
                  onChange={e => setForm(f => ({ ...f, tax_name: e.target.value }))}
                  className={inputCls} />
                <p className={hintCls}>Label shown on invoice line items</p>
              </div>
              <div>
                <label className={labelCls}>Tax Rate</label>
                <div className="relative">
                  <input type="number" step="0.01" min={0} max={100}
                    value={form.tax_rate || ''}
                    onChange={e => setForm(f => ({ ...f, tax_rate: parseFloat(e.target.value) || 0 }))}
                    className={inputCls + ' pr-8'} placeholder="0" />
                  <span className="absolute right-3 top-1/2 -translate-y-1/2 text-sm text-gray-400 pointer-events-none">%</span>
                </div>
                <p className={hintCls}>Set to 0 to disable tax</p>
              </div>
            </div>
            {form.tax_rate > 0 && (
              <div className="mt-4 p-3 bg-amber-50 dark:bg-amber-900/10 border border-amber-100 dark:border-amber-900/20 rounded-lg">
                <p className="text-xs text-amber-800 dark:text-amber-300 flex items-start gap-2">
                  <AlertCircle size={14} className="shrink-0 mt-0.5" />
                  <span>
                    Example: {symbol}100.00 subtotal {form.tax_name ? `+ ${form.tax_name} ` : '+ tax '}
                    {form.tax_rate}% = <strong>{formatCents(10000 + Math.round(form.tax_rate * 100), form.default_currency || 'USD')}</strong> total.
                    For automatic jurisdiction-based tax, enable Stripe Tax in Feature Flags.
                  </span>
                </p>
              </div>
            )}
          </div>
        </section>

        {/* ─── Region ─── */}
        <section className="pb-24">
          <div className="flex items-center gap-2 mb-4">
            <div className="w-8 h-8 rounded-lg bg-emerald-50 dark:bg-emerald-900/20 flex items-center justify-center">
              <Globe size={16} className="text-emerald-600 dark:text-emerald-400" />
            </div>
            <div>
              <h2 className="text-sm font-semibold text-gray-900 dark:text-gray-100">Environment</h2>
              <p className="text-xs text-gray-500">Read-only configuration set by your deployment</p>
            </div>
          </div>
          <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card border border-gray-100 dark:border-gray-800 p-6">
            <div className="space-y-3">
              <div className="py-2.5 px-3 bg-gray-50 dark:bg-gray-800 rounded-lg">
                <p className="text-xs text-gray-500 dark:text-gray-400 mb-1">API Endpoint</p>
                <code className="text-sm font-mono text-gray-900 dark:text-gray-100 break-all">{window.location.origin}/v1</code>
              </div>
              <div className="py-2.5 px-3 bg-gray-50 dark:bg-gray-800 rounded-lg">
                <p className="text-xs text-gray-500 dark:text-gray-400 mb-1">Stripe Webhook URL</p>
                <code className="text-sm font-mono text-gray-900 dark:text-gray-100 break-all">{window.location.origin}/v1/webhooks/stripe</code>
              </div>
            </div>
          </div>
        </section>

      </div>

      {/* Sticky save bar */}
      {hasChanges && (
        <div className="fixed bottom-0 left-0 right-0 z-30 md:left-60">
          <div className="max-w-7xl mx-auto px-4 md:px-8">
            <div className="bg-white dark:bg-gray-900 border-t border-gray-200 dark:border-gray-700 shadow-[0_-4px_12px_rgba(0,0,0,0.05)] rounded-t-xl px-6 py-3 flex items-center justify-between">
              <div className="flex items-center gap-2">
                <span className="w-2 h-2 rounded-full bg-amber-500 animate-pulse" />
                <span className="text-sm text-gray-700 dark:text-gray-300">Unsaved changes</span>
              </div>
              <div className="flex items-center gap-3">
                <button onClick={loadSettings}
                  className="px-4 py-2 text-sm font-medium text-gray-600 dark:text-gray-400 hover:text-gray-900 dark:hover:text-gray-100 transition-colors">
                  Discard
                </button>
                <button onClick={handleSave} disabled={saving}
                  className="flex items-center justify-center gap-2 px-5 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm disabled:opacity-50 transition-colors">
                  {saving ? (<><Loader2 size={14} className="animate-spin" /> Saving...</>) : 'Save Changes'}
                </button>
              </div>
            </div>
          </div>
        </div>
      )}
    </Layout>
  )
}
