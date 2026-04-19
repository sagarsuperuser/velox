import { useEffect, useState } from 'react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { api, setActiveCurrency, formatCents } from '@/lib/api'
import { applyApiError } from '@/lib/formErrors'
import { Layout } from '@/components/Layout'
import { toast } from 'sonner'
import { cn } from '@/lib/utils'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { Card, CardContent } from '@/components/ui/card'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from '@/components/ui/alert-dialog'
import { Switch } from '@/components/ui/switch'

import { Building2, FileText, Receipt, Globe, Check, AlertCircle, Loader2, Zap, Wrench } from 'lucide-react'

const settingsSchema = z.object({
  company_name: z.string(),
  company_email: z.string().email('Invalid email address').or(z.literal('')),
  company_phone: z.string().regex(/^[\+\d\s\-\(\)]{7,20}$/, 'Invalid phone number').or(z.literal('')),
  company_address: z.string(),
  logo_url: z.string(),
  invoice_prefix: z.string(),
  net_payment_terms: z.number().min(0).max(365),
  tax_rate_bp: z.number().min(0).max(10000),
  tax_name: z.string(),
  tax_inclusive: z.boolean(),
  default_currency: z.string(),
  timezone: z.string(),
})

type SettingsFormData = z.infer<typeof settingsSchema>

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

export default function SettingsPage() {
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)

  const formMethods = useForm<SettingsFormData>({
    resolver: zodResolver(settingsSchema),
    defaultValues: {
      company_name: '', company_email: '', company_phone: '', company_address: '',
      logo_url: '',
      invoice_prefix: '', net_payment_terms: 0, tax_rate_bp: 0, tax_name: '', tax_inclusive: false,
      default_currency: '', timezone: '',
    },
  })
  const { register, handleSubmit, watch, reset, setValue, formState: { errors: formErrors, isDirty } } = formMethods

  const form = watch()
  const hasChanges = isDirty

  useEffect(() => {
    const handleBeforeUnload = (e: BeforeUnloadEvent) => {
      if (hasChanges) { e.preventDefault(); e.returnValue = '' }
    }
    window.addEventListener('beforeunload', handleBeforeUnload)
    return () => window.removeEventListener('beforeunload', handleBeforeUnload)
  }, [hasChanges])

  const loadSettings = () => {
    setLoading(true); setError(null)
    api.getSettings().then(s => {
      const f = {
        company_name: s.company_name || '', company_email: s.company_email || '',
        company_phone: s.company_phone || '', company_address: s.company_address || '',
        logo_url: s.logo_url || '',
        invoice_prefix: s.invoice_prefix || '', net_payment_terms: s.net_payment_terms || 0,
        tax_rate_bp: s.tax_rate_bp || 0, tax_name: s.tax_name || '', tax_inclusive: s.tax_inclusive || false,
        default_currency: s.default_currency || '', timezone: s.timezone || '',
      }
      reset(f)
      setLoading(false)
    }).catch(err => { setError(err instanceof Error ? err.message : 'Failed to load settings'); setLoading(false) })
  }

  useEffect(() => { loadSettings() }, [])

  const handleSave = handleSubmit(async (data) => {
    setSaving(true)
    try {
      const updated = await api.updateSettings(data)
      const f = {
        company_name: updated.company_name || '', company_email: updated.company_email || '',
        company_phone: updated.company_phone || '', company_address: updated.company_address || '',
        logo_url: updated.logo_url || '',
        invoice_prefix: updated.invoice_prefix || '', net_payment_terms: updated.net_payment_terms || 0,
        tax_rate_bp: updated.tax_rate_bp || 0, tax_name: updated.tax_name || '', tax_inclusive: updated.tax_inclusive || false,
        default_currency: updated.default_currency || '', timezone: updated.timezone || '',
      }
      reset(f)
      if (updated.default_currency) setActiveCurrency(updated.default_currency)
      toast.success('Settings saved')
    } catch (err) {
      applyApiError(formMethods, err, [
        'company_name', 'company_email', 'company_phone', 'company_address', 'logo_url',
        'invoice_prefix', 'net_payment_terms', 'tax_rate_bp', 'tax_name', 'tax_inclusive',
        'default_currency', 'timezone',
      ], { toastTitle: 'Failed to save settings' })
    } finally { setSaving(false) }
  })

  if (loading) return (
    <Layout>
      <h1 className="text-2xl font-semibold text-foreground">Settings</h1>
      <div className="mt-6 flex justify-center p-8">
        <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
      </div>
    </Layout>
  )

  if (error) return (
    <Layout>
      <h1 className="text-2xl font-semibold text-foreground">Settings</h1>
      <div className="mt-6 p-8 text-center">
        <p className="text-sm text-destructive mb-3">{error}</p>
        <Button variant="outline" size="sm" onClick={loadSettings}>Retry</Button>
      </div>
    </Layout>
  )

  const currencyObj = CURRENCIES.find(c => c.value === form.default_currency)
  const symbol = currencyObj?.symbol || '$'

  return (
    <Layout>
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold text-foreground">Settings</h1>
          <p className="text-sm text-muted-foreground mt-1">Configure your billing tenant</p>
        </div>
        {!hasChanges && !loading && (
          <span className="text-sm text-emerald-600 dark:text-emerald-400 flex items-center gap-1.5 bg-emerald-50 dark:bg-emerald-500/10 px-3 py-1.5 rounded-lg">
            <Check size={14} /> Saved
          </span>
        )}
      </div>

      <div className="max-w-3xl mt-6 space-y-8">

        {/* Business Details */}
        <section>
          <div className="flex items-center gap-2 mb-4">
            <div className="w-8 h-8 rounded-lg bg-primary/10 flex items-center justify-center">
              <Building2 size={16} className="text-primary" />
            </div>
            <div>
              <h2 className="text-sm font-semibold text-foreground">Business Details</h2>
              <p className="text-xs text-muted-foreground">Appears on invoices and customer-facing documents</p>
            </div>
          </div>
          <Card>
            <CardContent className="p-6">
              <div className="grid grid-cols-1 md:grid-cols-2 gap-5">
                <div>
                  <Label>Business Name</Label>
                  <Input type="text" placeholder="Acme Inc." maxLength={255}
                    {...register('company_name')} className="mt-1" />
                  <p className="text-xs text-muted-foreground mt-1">Displayed on invoice headers</p>
                </div>
                <div>
                  <Label>Email</Label>
                  <Input type="email" placeholder="billing@acme.com" maxLength={254}
                    {...register('company_email')}
                    className={cn('mt-1', formErrors.company_email && 'border-destructive')} />
                  {formErrors.company_email && <p className="text-xs text-destructive mt-1">{formErrors.company_email.message}</p>}
                  <p className="text-xs text-muted-foreground mt-1">Reply-to address on invoice emails</p>
                </div>
                <div>
                  <Label>Phone</Label>
                  <Input type="tel" placeholder="+1 (555) 123-4567" maxLength={20}
                    {...register('company_phone')}
                    className={cn('mt-1', formErrors.company_phone && 'border-destructive')} />
                  {formErrors.company_phone && <p className="text-xs text-destructive mt-1">{formErrors.company_phone.message}</p>}
                </div>
                <div>
                  <Label>Logo URL</Label>
                  <Input type="url" placeholder="https://acme.com/logo.png" maxLength={500}
                    {...register('logo_url')} className="mt-1" />
                  <p className="text-xs text-muted-foreground mt-1">Used on invoice PDFs</p>
                </div>
                <div className="md:col-span-2">
                  <Label>Address</Label>
                  <Textarea
                    {...register('company_address')}
                    className="mt-1" rows={2}
                    placeholder={"123 Main St\nSan Francisco, CA 94105"} maxLength={500} />
                  <p className="text-xs text-muted-foreground mt-1">Shown in the "From" section on invoice PDFs</p>
                </div>
              </div>
            </CardContent>
          </Card>
        </section>

        {/* Invoice & Billing */}
        <section>
          <div className="flex items-center gap-2 mb-4">
            <div className="w-8 h-8 rounded-lg bg-blue-50 dark:bg-blue-500/10 flex items-center justify-center">
              <FileText size={16} className="text-blue-600 dark:text-blue-400" />
            </div>
            <div>
              <h2 className="text-sm font-semibold text-foreground">Invoice & Billing</h2>
              <p className="text-xs text-muted-foreground">Currency, invoice numbering, and payment terms</p>
            </div>
          </div>
          <Card>
            <CardContent className="p-6">
              <div className="grid grid-cols-1 md:grid-cols-2 gap-5">
                <div>
                  <Label>Currency</Label>
                  <Select value={form.default_currency} onValueChange={v => setValue('default_currency', v, { shouldDirty: true })}>
                    <SelectTrigger className="w-full mt-1">
                      <SelectValue placeholder="Select currency..." />
                    </SelectTrigger>
                    <SelectContent>
                      {CURRENCIES.map(c => (
                        <SelectItem key={c.value} value={c.value}>{c.symbol} {c.label} ({c.value})</SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  <p className="text-xs text-muted-foreground mt-1">Used across all invoices, plans, and charges</p>
                </div>
                <div>
                  <Label>Timezone</Label>
                  <Select value={form.timezone} onValueChange={v => setValue('timezone', v, { shouldDirty: true })}>
                    <SelectTrigger className="w-full mt-1">
                      <SelectValue placeholder="Select timezone..." />
                    </SelectTrigger>
                    <SelectContent>
                      {TIMEZONES.map(tz => (
                        <SelectItem key={tz} value={tz}>{tz.replace(/_/g, ' ')}</SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  <p className="text-xs text-muted-foreground mt-1">Used for billing cycle boundaries and reports</p>
                </div>
                <div>
                  <Label>Invoice Prefix</Label>
                  <Input type="text" maxLength={20}
                    {...register('invoice_prefix', {
                      onChange: (e) => { e.target.value = e.target.value.toUpperCase() },
                    })}
                    className="mt-1 font-mono uppercase" placeholder="INV" />
                  {form.invoice_prefix && (
                    <p className="text-xs text-muted-foreground mt-1 font-mono">Preview: {form.invoice_prefix}-000001</p>
                  )}
                </div>
                <div>
                  <Label>Payment Terms</Label>
                  <div className="relative mt-1">
                    <Input type="number" min={0} max={365}
                      {...register('net_payment_terms', { valueAsNumber: true })}
                      className="pr-16" />
                    <span className="absolute right-3 top-1/2 -translate-y-1/2 text-sm text-muted-foreground pointer-events-none">days</span>
                  </div>
                  <p className="text-xs text-muted-foreground mt-1">Days after issue before payment is due</p>
                </div>
              </div>
            </CardContent>
          </Card>
        </section>

        {/* Tax */}
        <section>
          <div className="flex items-center gap-2 mb-4">
            <div className="w-8 h-8 rounded-lg bg-amber-50 dark:bg-amber-500/10 flex items-center justify-center">
              <Receipt size={16} className="text-amber-600 dark:text-amber-400" />
            </div>
            <div>
              <h2 className="text-sm font-semibold text-foreground">Tax</h2>
              <p className="text-xs text-muted-foreground">Default tax rate applied to all invoices. Per-customer overrides available on billing profiles.</p>
            </div>
          </div>
          <Card>
            <CardContent className="p-6">
              <div className="grid grid-cols-1 md:grid-cols-2 gap-5">
                <div>
                  <Label>Tax Name</Label>
                  <Input type="text" maxLength={50} placeholder="e.g. GST, VAT, Sales Tax"
                    {...register('tax_name')} className="mt-1" />
                  <p className="text-xs text-muted-foreground mt-1">Label shown on invoice line items</p>
                </div>
                <div>
                  <Label>Tax Rate</Label>
                  <div className="relative mt-1">
                    <Input type="number" step="0.01" min={0} max={100}
                      value={form.tax_rate_bp / 100}
                      onChange={e => setValue('tax_rate_bp', Math.round(parseFloat(e.target.value || '0') * 100), { shouldDirty: true })}
                      className="pr-8" placeholder="0" />
                    <span className="absolute right-3 top-1/2 -translate-y-1/2 text-sm text-muted-foreground pointer-events-none">%</span>
                  </div>
                  <p className="text-xs text-muted-foreground mt-1">Set to 0 to disable tax</p>
                </div>
              </div>
              <div className="mt-5 flex items-start justify-between gap-4 p-4 rounded-lg border border-border bg-muted/30">
                <div>
                  <Label className="font-medium text-foreground">Tax-inclusive pricing</Label>
                  <p className="text-xs text-muted-foreground mt-1">
                    When on, plan and usage prices already include tax. Velox carves tax out of the sticker price.
                    When off (default), tax is added on top of the subtotal.
                  </p>
                </div>
                <Switch
                  checked={form.tax_inclusive ?? false}
                  onCheckedChange={(checked) => setValue('tax_inclusive', !!checked, { shouldDirty: true })}
                  className="shrink-0 mt-0.5"
                />
              </div>
              {form.tax_rate_bp > 0 && (
                <div className="mt-4 p-3 bg-amber-50 dark:bg-amber-500/10 border border-amber-100 dark:border-amber-500/20 rounded-lg">
                  <p className="text-xs text-amber-800 dark:text-amber-300 flex items-start gap-2">
                    <AlertCircle size={14} className="shrink-0 mt-0.5" />
                    {form.tax_inclusive ? (
                      <span>
                        Example: {symbol}100.00 sticker price includes {form.tax_name || 'tax'} at {(form.tax_rate_bp / 100).toFixed(2)}%.
                        Net subtotal = <strong>{formatCents(Math.round(10000 * 10000 / (10000 + form.tax_rate_bp)), form.default_currency || 'USD')}</strong>.
                        Customer pays <strong>{formatCents(10000, form.default_currency || 'USD')}</strong>.
                      </span>
                    ) : (
                      <span>
                        Example: {symbol}100.00 subtotal {form.tax_name ? `+ ${form.tax_name} ` : '+ tax '}
                        {(form.tax_rate_bp / 100).toFixed(2)}% = <strong>{formatCents(10000 + Math.round(10000 * form.tax_rate_bp / 10000), form.default_currency || 'USD')}</strong> total.
                        For automatic jurisdiction-based tax, enable Stripe Tax in Feature Flags.
                      </span>
                    )}
                  </p>
                </div>
              )}
            </CardContent>
          </Card>
        </section>

        {/* Environment */}
        <section className="pb-24">
          <div className="flex items-center gap-2 mb-4">
            <div className="w-8 h-8 rounded-lg bg-emerald-50 dark:bg-emerald-500/10 flex items-center justify-center">
              <Globe size={16} className="text-emerald-600 dark:text-emerald-400" />
            </div>
            <div>
              <h2 className="text-sm font-semibold text-foreground">Environment</h2>
              <p className="text-xs text-muted-foreground">Read-only configuration set by your deployment</p>
            </div>
          </div>
          <Card>
            <CardContent className="p-6">
              <div className="space-y-3">
                <div className="py-2.5 px-3 bg-muted rounded-lg">
                  <p className="text-xs text-muted-foreground mb-1">API Endpoint</p>
                  <code className="text-sm font-mono text-foreground break-all">{window.location.origin}/v1</code>
                </div>
                <div className="py-2.5 px-3 bg-muted rounded-lg">
                  <p className="text-xs text-muted-foreground mb-1">Stripe Webhook URL</p>
                  <code className="text-sm font-mono text-foreground break-all">{window.location.origin}/v1/webhooks/stripe</code>
                </div>
              </div>
            </CardContent>
          </Card>
        </section>

        {/* ─── Operations ────────────────────────────────────────────────── */}
        <OperationsSection />
      </div>

      {/* Sticky save bar */}
      {hasChanges && (
        <div className="fixed bottom-0 left-0 right-0 z-30 md:left-60">
          <div className="max-w-7xl mx-auto px-4 md:px-8">
            <div className="bg-card border-t border-border shadow-[0_-4px_12px_rgba(0,0,0,0.05)] rounded-t-xl px-6 py-3 flex items-center justify-between">
              <div className="flex items-center gap-2">
                <span className="w-2 h-2 rounded-full bg-amber-500 animate-pulse" />
                <span className="text-sm text-muted-foreground">Unsaved changes</span>
              </div>
              <div className="flex items-center gap-3">
                <Button variant="ghost" onClick={() => loadSettings()}>Discard</Button>
                <Button onClick={() => handleSave()} disabled={saving}>
                  {saving ? <><Loader2 size={14} className="animate-spin mr-2" /> Saving...</> : 'Save Changes'}
                </Button>
              </div>
            </div>
          </div>
        </div>
      )}
    </Layout>
  )
}

function OperationsSection() {
  const queryClient = useQueryClient()
  const [result, setResult] = useState<{ generated: number; errors: number; at: string } | null>(null)

  const trigger = useMutation({
    mutationFn: () => api.triggerBilling(),
    onSuccess: (res) => {
      const generated = res.invoices_generated
      const errors = res.errors?.length ?? 0
      setResult({ generated, errors, at: new Date().toISOString() })
      if (errors > 0) {
        toast.error(`${errors} ${errors === 1 ? 'error' : 'errors'} during billing run — check audit log.`)
      } else {
        toast.success(`${generated} ${generated === 1 ? 'invoice' : 'invoices'} generated.`)
      }
      queryClient.invalidateQueries({ queryKey: ['dashboard-overview'] })
      queryClient.invalidateQueries({ queryKey: ['dashboard-chart'] })
      queryClient.invalidateQueries({ queryKey: ['dashboard-recent-invoices'] })
      queryClient.invalidateQueries({ queryKey: ['analytics-overview'] })
      queryClient.invalidateQueries({ queryKey: ['analytics-chart'] })
    },
    onError: (err) => {
      toast.error(`Billing run failed: ${err instanceof Error ? err.message : 'unknown error'}`)
    },
  })

  return (
    <section className="space-y-4">
      <div className="flex items-center gap-3 pb-2 border-b border-border">
        <div className="p-2 rounded-lg bg-primary/10">
          <Wrench size={16} className="text-primary" />
        </div>
        <div>
          <h2 className="text-sm font-semibold text-foreground">Operations</h2>
          <p className="text-xs text-muted-foreground">Manual billing controls — use with care.</p>
        </div>
      </div>
      <Card>
        <CardContent className="p-6">
          <div className="flex flex-col md:flex-row md:items-start md:justify-between gap-4">
            <div className="max-w-lg">
              <p className="text-sm font-medium text-foreground">Run billing cycle</p>
              <p className="text-xs text-muted-foreground mt-1">
                Finalizes invoices for any subscriptions whose billing period has elapsed and attempts to collect
                payment. The scheduler runs this automatically; trigger manually only for testing or after reconciling
                a paused run.
              </p>
              {result && (
                <p className="text-xs text-muted-foreground mt-3">
                  Last run: {new Date(result.at).toLocaleString()} — {result.generated} invoice(s) generated
                  {result.errors > 0 && `, ${result.errors} error(s)`}.
                </p>
              )}
            </div>
            <AlertDialog>
              <AlertDialogTrigger asChild>
                <Button variant="outline" disabled={trigger.isPending}>
                  {trigger.isPending
                    ? <><Loader2 size={14} className="animate-spin mr-2" /> Running...</>
                    : <><Zap size={14} className="mr-2" /> Trigger Billing Run</>
                  }
                </Button>
              </AlertDialogTrigger>
              <AlertDialogContent>
                <AlertDialogHeader>
                  <AlertDialogTitle>Run billing cycle now?</AlertDialogTitle>
                  <AlertDialogDescription>
                    This will finalize invoices for all subscriptions whose billing period has ended and attempt to
                    collect payment from the linked payment methods. Charges are real in production environments.
                  </AlertDialogDescription>
                </AlertDialogHeader>
                <AlertDialogFooter>
                  <AlertDialogCancel>Cancel</AlertDialogCancel>
                  <AlertDialogAction onClick={() => trigger.mutate()}>Run billing</AlertDialogAction>
                </AlertDialogFooter>
              </AlertDialogContent>
            </AlertDialog>
          </div>
        </CardContent>
      </Card>
    </section>
  )
}
