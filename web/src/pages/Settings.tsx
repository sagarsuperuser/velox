import { useEffect, useState, useMemo } from 'react'
import { api } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { FormField, FormSelect } from '@/components/FormField'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { ErrorState } from '@/components/ErrorState'
import { useToast } from '@/components/Toast'
import { useFormValidation, rules } from '@/hooks/useFormValidation'

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

  const fieldRules = useMemo(() => ({
    company_email: [rules.email()],
    company_phone: [rules.phone()],
  }), [])
  const { onBlur, validateAll, fieldError, registerRef } = useFormValidation(fieldRules)

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
    if (!validateAll(companyForm)) return
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

      <form onSubmit={handleSave} noValidate>
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-6 mt-6">
          {/* Company Information */}
          <div className="bg-white rounded-xl shadow-card">
            <div className="px-6 py-4 border-b border-gray-100">
              <h2 className="text-sm font-semibold text-gray-900">Company Information</h2>
              <p className="text-sm text-gray-500 mt-0.5">Appears on invoices and customer-facing documents</p>
            </div>
            <div className="px-6 py-4 space-y-4">
              <FormField label="Company Name" value={companyForm.company_name} placeholder="Acme Inc." maxLength={255}
                onChange={e => setCompanyForm(f => ({ ...f, company_name: e.target.value }))}
                hint="Displayed on invoice headers" />
              <FormField label="Email" type="email" value={companyForm.company_email} placeholder="billing@acme.com" maxLength={254}
                ref={registerRef('company_email')} error={fieldError('company_email')}
                onChange={e => setCompanyForm(f => ({ ...f, company_email: e.target.value }))}
                onBlur={() => onBlur('company_email', companyForm.company_email)} />
              <FormField label="Phone" type="tel" value={companyForm.company_phone} placeholder="+1 (555) 123-4567" maxLength={20}
                ref={registerRef('company_phone')} error={fieldError('company_phone')}
                onChange={e => setCompanyForm(f => ({ ...f, company_phone: e.target.value }))}
                onBlur={() => onBlur('company_phone', companyForm.company_phone)} />
              <div>
                <label className="block text-sm font-medium text-gray-700 mb-1">Address</label>
                <textarea value={companyForm.company_address}
                  onChange={e => setCompanyForm(f => ({ ...f, company_address: e.target.value }))}
                  className="w-full px-3 py-2 border border-gray-200 rounded-lg shadow-sm text-sm focus:outline-none focus:ring-2 focus:ring-velox-500"
                  rows={3} placeholder={"123 Main St, Suite 100\nSan Francisco, CA 94105"} maxLength={500} />
                <p className="mt-1 text-sm text-gray-500">Printed on invoices as the sender address</p>
              </div>
            </div>
          </div>

          {/* Invoice Configuration */}
          <div className="bg-white rounded-xl shadow-card">
            <div className="px-6 py-4 border-b border-gray-100">
              <h2 className="text-sm font-semibold text-gray-900">Invoice Configuration</h2>
              <p className="text-sm text-gray-500 mt-0.5">Controls how invoices are generated and numbered</p>
            </div>
            <div className="px-6 py-4 space-y-4">
              <FormField label="Invoice Prefix" value={invoiceForm.invoice_prefix} placeholder="INV" maxLength={20} mono
                onChange={e => setInvoiceForm(f => ({ ...f, invoice_prefix: e.target.value }))}
                hint="Invoice numbers will be PREFIX-000001, PREFIX-000002, etc." />
              <FormField label="Net Payment Terms" type="number" value={String(invoiceForm.net_payment_terms)} min={0} max={365}
                onChange={e => setInvoiceForm(f => ({ ...f, net_payment_terms: parseInt(e.target.value) || 0 }))}
                hint="Number of days after issue date before invoice is due" />
              <FormSelect label="Default Currency" value={invoiceForm.default_currency}
                onChange={e => setInvoiceForm(f => ({ ...f, default_currency: e.target.value }))}
                placeholder="Select currency..."
                options={[
                  { value: 'USD', label: 'USD — US Dollar' },
                  { value: 'EUR', label: 'EUR — Euro' },
                  { value: 'GBP', label: 'GBP — British Pound' },
                  { value: 'CAD', label: 'CAD — Canadian Dollar' },
                  { value: 'AUD', label: 'AUD — Australian Dollar' },
                  { value: 'JPY', label: 'JPY — Japanese Yen' },
                  { value: 'INR', label: 'INR — Indian Rupee' },
                  { value: 'CHF', label: 'CHF — Swiss Franc' },
                ]} />
              <FormSelect label="Timezone" value={invoiceForm.timezone}
                onChange={e => setInvoiceForm(f => ({ ...f, timezone: e.target.value }))}
                placeholder="Select timezone..."
                options={[
                  { value: 'UTC', label: 'UTC' },
                  { value: 'America/New_York', label: 'Eastern Time (US)' },
                  { value: 'America/Chicago', label: 'Central Time (US)' },
                  { value: 'America/Denver', label: 'Mountain Time (US)' },
                  { value: 'America/Los_Angeles', label: 'Pacific Time (US)' },
                  { value: 'Europe/London', label: 'London (GMT/BST)' },
                  { value: 'Europe/Berlin', label: 'Berlin (CET)' },
                  { value: 'Europe/Paris', label: 'Paris (CET)' },
                  { value: 'Asia/Tokyo', label: 'Tokyo (JST)' },
                  { value: 'Asia/Shanghai', label: 'Shanghai (CST)' },
                  { value: 'Asia/Kolkata', label: 'India (IST)' },
                  { value: 'Australia/Sydney', label: 'Sydney (AEST)' },
                ]} />
            </div>
          </div>
        </div>

        <div className="flex items-center justify-end gap-3 mt-6">
          {savedForm && !hasChanges && (
            <span className="text-sm text-emerald-600 flex items-center gap-1">
              <svg className="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M5 13l4 4L19 7" /></svg>
              Settings saved
            </span>
          )}
          {hasChanges && (
            <span className="text-sm text-amber-600">Unsaved changes</span>
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
