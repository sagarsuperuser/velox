import { useEffect, useState } from 'react'
import { api } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { ErrorState } from '@/components/ErrorState'
import { useToast } from '@/components/Toast'

export function SettingsPage() {
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [savedForm, setSavedForm] = useState<string>('')
  const toast = useToast()

  const [companyForm, setCompanyForm] = useState({
    company_name: '',
    company_email: '',
    company_phone: '',
    company_address: '',
  })

  const [invoiceForm, setInvoiceForm] = useState({
    invoice_prefix: '',
    net_payment_terms: 0,
    default_currency: '',
    timezone: '',
  })

  const hasChanges = savedForm !== '' && JSON.stringify({ ...companyForm, ...invoiceForm }) !== savedForm

  const loadSettings = () => {
    setLoading(true)
    setError(null)
    api.getSettings().then(s => {
      const cf = {
        company_name: s.company_name || '',
        company_email: s.company_email || '',
        company_phone: s.company_phone || '',
        company_address: s.company_address || '',
      }
      const inf = {
        invoice_prefix: s.invoice_prefix || '',
        net_payment_terms: s.net_payment_terms || 0,
        default_currency: s.default_currency || '',
        timezone: s.timezone || '',
      }
      setCompanyForm(cf)
      setInvoiceForm(inf)
      setSavedForm(JSON.stringify({ ...cf, ...inf }))
      setLoading(false)
    }).catch(err => { setError(err instanceof Error ? err.message : 'Failed to load settings'); setLoading(false) })
  }

  useEffect(() => { loadSettings() }, [])

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault()
    setSaving(true)
    try {
      const updated = await api.updateSettings({
        ...companyForm,
        ...invoiceForm,
      })
      const cf = {
        company_name: updated.company_name || '',
        company_email: updated.company_email || '',
        company_phone: updated.company_phone || '',
        company_address: updated.company_address || '',
      }
      const inf = {
        invoice_prefix: updated.invoice_prefix || '',
        net_payment_terms: updated.net_payment_terms || 0,
        default_currency: updated.default_currency || '',
        timezone: updated.timezone || '',
      }
      setCompanyForm(cf)
      setInvoiceForm(inf)
      setSavedForm(JSON.stringify({ ...cf, ...inf }))
      toast.success('Settings saved')
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to save settings')
    } finally {
      setSaving(false)
    }
  }

  const fieldClass = "w-full px-3 py-2 border border-gray-200 rounded-lg shadow-sm text-sm focus:outline-none focus:ring-2 focus:ring-velox-500"

  if (loading) {
    return (
      <Layout>
        <h1 className="text-2xl font-semibold text-gray-900">Settings</h1>
        <div className="bg-white rounded-xl shadow-card mt-6">
          <LoadingSkeleton rows={6} columns={2} />
        </div>
      </Layout>
    )
  }

  if (error) {
    return (
      <Layout>
        <h1 className="text-2xl font-semibold text-gray-900">Settings</h1>
        <div className="bg-white rounded-xl shadow-card mt-6">
          <ErrorState message={error} onRetry={loadSettings} />
        </div>
      </Layout>
    )
  }

  return (
    <Layout>
      <h1 className="text-2xl font-semibold text-gray-900">Settings</h1>
      <p className="text-sm text-gray-500 mt-1">Configure your billing tenant</p>

      <form onSubmit={handleSave}>
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-6 mt-6">
          {/* Company Information */}
          <div className="bg-white rounded-xl shadow-card">
            <div className="px-6 py-4 border-b border-gray-100">
              <h2 className="text-sm font-semibold text-gray-900">Company Information</h2>
            </div>
            <div className="px-6 py-4 space-y-4">
              <div>
                <label className="block text-sm font-medium text-gray-700 mb-1">Company Name</label>
                <input type="text" value={companyForm.company_name}
                  onChange={e => setCompanyForm(f => ({ ...f, company_name: e.target.value }))}
                  className={fieldClass} placeholder="Acme Inc." />
              </div>
              <div>
                <label className="block text-sm font-medium text-gray-700 mb-1">Email</label>
                <input type="email" value={companyForm.company_email}
                  onChange={e => setCompanyForm(f => ({ ...f, company_email: e.target.value }))}
                  className={fieldClass} placeholder="billing@acme.com" />
              </div>
              <div>
                <label className="block text-sm font-medium text-gray-700 mb-1">Phone</label>
                <input type="text" value={companyForm.company_phone}
                  onChange={e => setCompanyForm(f => ({ ...f, company_phone: e.target.value }))}
                  className={fieldClass} placeholder="+1 (555) 123-4567" />
              </div>
              <div>
                <label className="block text-sm font-medium text-gray-700 mb-1">Address</label>
                <textarea value={companyForm.company_address}
                  onChange={e => setCompanyForm(f => ({ ...f, company_address: e.target.value }))}
                  className={fieldClass} rows={3} placeholder="123 Main St, Suite 100&#10;San Francisco, CA 94105" />
              </div>
            </div>
          </div>

          {/* Invoice Configuration */}
          <div className="bg-white rounded-xl shadow-card">
            <div className="px-6 py-4 border-b border-gray-100">
              <h2 className="text-sm font-semibold text-gray-900">Invoice Configuration</h2>
            </div>
            <div className="px-6 py-4 space-y-4">
              <div>
                <label className="block text-sm font-medium text-gray-700 mb-1">Invoice Prefix</label>
                <input type="text" value={invoiceForm.invoice_prefix}
                  onChange={e => setInvoiceForm(f => ({ ...f, invoice_prefix: e.target.value }))}
                  className={fieldClass + ' font-mono'} placeholder="INV-" />
              </div>
              <div>
                <label className="block text-sm font-medium text-gray-700 mb-1">Net Payment Terms (days)</label>
                <input type="number" value={invoiceForm.net_payment_terms}
                  onChange={e => setInvoiceForm(f => ({ ...f, net_payment_terms: parseInt(e.target.value) || 0 }))}
                  className={fieldClass} min={0} />
                <p className="text-xs text-gray-400 mt-1">Invoices will be due this many days after issue date</p>
              </div>
              <div>
                <label className="block text-sm font-medium text-gray-700 mb-1">Default Currency</label>
                <select value={invoiceForm.default_currency}
                  onChange={e => setInvoiceForm(f => ({ ...f, default_currency: e.target.value }))}
                  className={fieldClass + ' bg-white'}>
                  <option value="">Select currency...</option>
                  {['USD', 'EUR', 'GBP', 'CAD', 'AUD', 'JPY', 'INR', 'CHF'].map(c => (
                    <option key={c} value={c}>{c}</option>
                  ))}
                </select>
              </div>
              <div>
                <label className="block text-sm font-medium text-gray-700 mb-1">Timezone</label>
                <select value={invoiceForm.timezone}
                  onChange={e => setInvoiceForm(f => ({ ...f, timezone: e.target.value }))}
                  className={fieldClass + ' bg-white'}>
                  <option value="">Select timezone...</option>
                  {['UTC', 'America/New_York', 'America/Chicago', 'America/Denver', 'America/Los_Angeles', 'Europe/London', 'Europe/Berlin', 'Europe/Paris', 'Asia/Tokyo', 'Asia/Shanghai', 'Asia/Kolkata', 'Australia/Sydney'].map(tz => (
                    <option key={tz} value={tz}>{tz}</option>
                  ))}
                </select>
              </div>
            </div>
          </div>
        </div>

        <div className="flex items-center justify-end gap-3 mt-6">
          {savedForm && !hasChanges && (
            <span className="text-xs text-emerald-600 flex items-center gap-1">
              <svg className="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M5 13l4 4L19 7" /></svg>
              Settings saved
            </span>
          )}
          {hasChanges && (
            <span className="text-xs text-amber-600">Unsaved changes</span>
          )}
          <button type="submit" disabled={saving || !hasChanges}
            className="px-6 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow disabled:opacity-50 transition-colors">
            {saving ? 'Saving...' : hasChanges ? 'Save Changes' : 'Saved'}
          </button>
        </div>
      </form>
    </Layout>
  )
}
