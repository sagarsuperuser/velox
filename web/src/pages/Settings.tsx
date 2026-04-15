import { useEffect, useState, useMemo } from 'react'
import { api, setActiveCurrency } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { ErrorState } from '@/components/ErrorState'
import { useToast } from '@/components/Toast'
import { useFormValidation, rules } from '@/hooks/useFormValidation'
import { Check } from 'lucide-react'

const CURRENCIES = [
  { value: 'USD', label: 'USD', symbol: '$' },
  { value: 'EUR', label: 'EUR', symbol: '\u20AC' },
  { value: 'GBP', label: 'GBP', symbol: '\u00A3' },
  { value: 'INR', label: 'INR', symbol: '\u20B9' },
  { value: 'CAD', label: 'CAD', symbol: 'CA$' },
  { value: 'AUD', label: 'AUD', symbol: 'A$' },
  { value: 'JPY', label: 'JPY', symbol: '\u00A5' },
  { value: 'CHF', label: 'CHF', symbol: 'CHF' },
  { value: 'SGD', label: 'SGD', symbol: 'S$' },
  { value: 'BRL', label: 'BRL', symbol: 'R$' },
  { value: 'MXN', label: 'MXN', symbol: 'MX$' },
  { value: 'KRW', label: 'KRW', symbol: '\u20A9' },
]

export function SettingsPage() {
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [savedForm, setSavedForm] = useState<string>('')
  const toast = useToast()

  const [form, setForm] = useState({
    company_name: '', company_email: '', company_phone: '', company_address: '',
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

  if (loading) return <Layout><h1 className="text-2xl font-semibold text-gray-900 dark:text-gray-100">Settings</h1><div className="bg-white dark:bg-gray-900 rounded-xl shadow-card mt-6"><LoadingSkeleton rows={6} columns={2} /></div></Layout>
  if (error) return <Layout><h1 className="text-2xl font-semibold text-gray-900 dark:text-gray-100">Settings</h1><div className="bg-white dark:bg-gray-900 rounded-xl shadow-card mt-6"><ErrorState message={error} onRetry={loadSettings} /></div></Layout>

  const currencyObj = CURRENCIES.find(c => c.value === form.default_currency)
  const inputCls = 'w-full px-3 py-1.5 border border-gray-200 dark:border-gray-700 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-velox-500 dark:bg-gray-800 dark:text-gray-100'

  return (
    <Layout>
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900 dark:text-gray-100">Settings</h1>
          <p className="text-sm text-gray-600 dark:text-gray-400 mt-1">Configure your billing tenant</p>
        </div>
        {!hasChanges && savedForm && (
          <span className="text-sm text-emerald-600 flex items-center gap-1">
            <Check size={16} /> All changes saved
          </span>
        )}
      </div>

      <div className="max-w-2xl mt-6 space-y-6">

        {/* Business Details */}
        <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card">
          <div className="px-6 py-4 border-b border-gray-100 dark:border-gray-800">
            <h2 className="text-sm font-semibold text-gray-900 dark:text-gray-100">Business Details</h2>
            <p className="text-xs text-gray-500 dark:text-gray-500 mt-0.5">Appears on invoices and customer-facing documents</p>
          </div>
          <div className="divide-y divide-gray-100 dark:divide-gray-800">
            <div className="px-6 py-4 flex items-center justify-between">
              <div>
                <p className="text-sm text-gray-900 dark:text-gray-100">Business Name</p>
                <p className="text-xs text-gray-500">Displayed on invoice headers and emails</p>
              </div>
              <div className="w-48">
                <input type="text" value={form.company_name} placeholder="Acme Inc." maxLength={255}
                  onChange={e => setForm(f => ({ ...f, company_name: e.target.value }))}
                  className={inputCls} />
              </div>
            </div>
            <div className="px-6 py-4 flex items-center justify-between">
              <div>
                <p className="text-sm text-gray-900 dark:text-gray-100">Email</p>
                <p className="text-xs text-gray-500">Reply-to address on invoice emails</p>
              </div>
              <div className="w-48">
                <input type="email" value={form.company_email} placeholder="billing@acme.com" maxLength={254}
                  ref={registerRef('company_email')}
                  onChange={e => setForm(f => ({ ...f, company_email: e.target.value }))}
                  onBlur={() => onBlur('company_email', form.company_email)}
                  className={`${inputCls} ${fieldError('company_email') ? 'border-red-300' : ''}`} />
              </div>
            </div>
            <div className="px-6 py-4 flex items-center justify-between">
              <div>
                <p className="text-sm text-gray-900 dark:text-gray-100">Phone</p>
                <p className="text-xs text-gray-500">Shown on invoice PDFs</p>
              </div>
              <div className="w-48">
                <input type="tel" value={form.company_phone} placeholder="+1 (555) 123-4567" maxLength={20}
                  ref={registerRef('company_phone')}
                  onChange={e => setForm(f => ({ ...f, company_phone: e.target.value }))}
                  onBlur={() => onBlur('company_phone', form.company_phone)}
                  className={`${inputCls} ${fieldError('company_phone') ? 'border-red-300' : ''}`} />
              </div>
            </div>
            <div className="px-6 py-4 flex items-start justify-between">
              <div className="pt-1.5">
                <p className="text-sm text-gray-900 dark:text-gray-100">Address</p>
                <p className="text-xs text-gray-500">Shown in the "From" section on PDFs</p>
              </div>
              <div className="w-48">
                <textarea value={form.company_address}
                  onChange={e => setForm(f => ({ ...f, company_address: e.target.value }))}
                  className={inputCls} rows={2}
                  placeholder={"123 Main St\nSan Francisco, CA 94105"} maxLength={500} />
              </div>
            </div>
          </div>
        </div>

        {/* Billing */}
        <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card">
          <div className="px-6 py-4 border-b border-gray-100 dark:border-gray-800">
            <h2 className="text-sm font-semibold text-gray-900 dark:text-gray-100">Billing</h2>
            <p className="text-xs text-gray-500 dark:text-gray-500 mt-0.5">Currency, invoice numbering, and payment terms</p>
          </div>
          <div className="divide-y divide-gray-100 dark:divide-gray-800">
            <div className="px-6 py-4 flex items-center justify-between">
              <div>
                <p className="text-sm text-gray-900 dark:text-gray-100">Currency</p>
                <p className="text-xs text-gray-500">Used across all invoices, plans, and charges</p>
              </div>
              <div className="w-48">
                <select value={form.default_currency}
                  onChange={e => setForm(f => ({ ...f, default_currency: e.target.value }))}
                  className={inputCls + ' bg-white'}>
                  <option value="">Select...</option>
                  {CURRENCIES.map(c => <option key={c.value} value={c.value}>{c.symbol} {c.value}</option>)}
                </select>
              </div>
            </div>
            <div className="px-6 py-4 flex items-center justify-between">
              <div>
                <p className="text-sm text-gray-900 dark:text-gray-100">Invoice Prefix</p>
                <p className="text-xs text-gray-500">Numbers will be PREFIX-YYYYMM-XXXX</p>
              </div>
              <div className="w-48">
                <input type="text" value={form.invoice_prefix} maxLength={20}
                  onChange={e => setForm(f => ({ ...f, invoice_prefix: e.target.value.toUpperCase() }))}
                  className={inputCls + ' font-mono uppercase'} placeholder="INV" />
                {form.invoice_prefix && (
                  <p className="text-xs text-gray-500 dark:text-gray-500 font-mono mt-1">{form.invoice_prefix}-202604-0001</p>
                )}
              </div>
            </div>
            <div className="px-6 py-4 flex items-center justify-between">
              <div>
                <p className="text-sm text-gray-900 dark:text-gray-100">Payment Terms</p>
                <p className="text-xs text-gray-500">Days after issue before payment is due</p>
              </div>
              <div className="w-48 flex items-center gap-2">
                <span className="text-sm text-gray-600 shrink-0">Net</span>
                <input type="number" min={0} max={365} value={form.net_payment_terms}
                  onChange={e => setForm(f => ({ ...f, net_payment_terms: parseInt(e.target.value) || 0 }))}
                  className={inputCls + ' text-center'} />
                <span className="text-sm text-gray-600 shrink-0">days</span>
              </div>
            </div>
          </div>
        </div>

        {/* Tax */}
        <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card">
          <div className="px-6 py-4 border-b border-gray-100 dark:border-gray-800">
            <h2 className="text-sm font-semibold text-gray-900 dark:text-gray-100">Tax</h2>
            <p className="text-xs text-gray-500 dark:text-gray-500 mt-0.5">Default tax rate applied to all invoices. Per-customer overrides available on billing profiles.</p>
          </div>
          <div className="divide-y divide-gray-100 dark:divide-gray-800">
            <div className="px-6 py-4 flex items-center justify-between">
              <div>
                <p className="text-sm text-gray-900 dark:text-gray-100">Tax Name</p>
                <p className="text-xs text-gray-500">Label shown on invoices (e.g. GST, VAT)</p>
              </div>
              <div className="w-48">
                <input type="text" value={form.tax_name} maxLength={50} placeholder="e.g. GST"
                  onChange={e => setForm(f => ({ ...f, tax_name: e.target.value }))}
                  className={inputCls} />
              </div>
            </div>
            <div className="px-6 py-4 flex items-center justify-between">
              <div>
                <p className="text-sm text-gray-900 dark:text-gray-100">Tax Rate</p>
                <p className="text-xs text-gray-500">Applied to subtotal. Set to 0 to disable.</p>
              </div>
              <div className="w-48 flex items-center gap-2">
                <input type="number" step="0.01" min={0} max={100}
                  value={form.tax_rate || 0}
                  onChange={e => setForm(f => ({ ...f, tax_rate: parseFloat(e.target.value) || 0 }))}
                  className={inputCls + ' text-center'} />
                <span className="text-sm text-gray-600 shrink-0">%</span>
              </div>
            </div>
            {form.tax_rate > 0 && form.tax_name && (
              <div className="px-6 py-3 bg-gray-50 rounded-b-xl">
                <p className="text-xs text-gray-500">
                  Example: {currencyObj?.symbol || '$'}100.00 + {form.tax_name} {form.tax_rate}% = {currencyObj?.symbol || '$'}{(100 + form.tax_rate).toFixed(2)}
                </p>
              </div>
            )}
          </div>
        </div>

      </div>

      {/* Sticky save bar */}
      {hasChanges && (
        <div className="fixed bottom-0 left-0 right-0 z-30 md:left-60">
          <div className="max-w-7xl mx-auto px-4 md:px-8">
            <div className="bg-white dark:bg-gray-900 border-t border-gray-200 dark:border-gray-700 shadow-[0_-4px_12px_rgba(0,0,0,0.05)] rounded-t-xl px-6 py-3 flex items-center justify-between">
              <div className="flex items-center gap-2">
                <span className="w-2 h-2 rounded-full bg-amber-500 animate-pulse" />
                <span className="text-sm text-gray-700 dark:text-gray-300">You have unsaved changes</span>
              </div>
              <div className="flex items-center gap-3">
                <button onClick={loadSettings}
                  className="px-4 py-2 text-sm font-medium text-gray-600 hover:text-gray-900 transition-colors">
                  Discard
                </button>
                <button onClick={handleSave} disabled={saving}
                  className="px-5 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm disabled:opacity-50 transition-colors">
                  {saving ? 'Saving...' : 'Save Changes'}
                </button>
              </div>
            </div>
          </div>
        </div>
      )}
    </Layout>
  )
}
