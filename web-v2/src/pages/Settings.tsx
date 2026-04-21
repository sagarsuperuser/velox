import { useEffect, useState } from 'react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, setActiveCurrency, formatCents, type StripeProviderCredentials } from '@/lib/api'
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

import { Building2, CreditCard, FileText, Receipt, Check, AlertCircle, Loader2, Trash2, Copy } from 'lucide-react'

const settingsSchema = z.object({
  company_name: z.string(),
  company_email: z.string().email('Invalid email address').or(z.literal('')),
  company_phone: z.string().regex(/^[\+\d\s\-\(\)]{7,20}$/, 'Invalid phone number').or(z.literal('')),
  company_address: z.string(),
  logo_url: z.string(),
  invoice_prefix: z.string(),
  net_payment_terms: z.number().min(0).max(365),
  tax_provider: z.enum(['none', 'manual']),
  tax_rate_bp: z.number().min(0).max(10000),
  tax_name: z.string(),
  tax_inclusive: z.boolean(),
  default_currency: z.string(),
  timezone: z.string(),
})

type SettingsFormData = z.infer<typeof settingsSchema>

// Prefer the deployment-configured public API origin (VITE_API_URL) over
// window.location.origin — in dev the browser loads from Vite at :5173 while
// the Go API runs on :8080, so window.location.origin produces a misleading
// value for both the "API Endpoint" display and the Stripe Webhook URL
// (Stripe can't reach a dev-server port). Falls back to the current origin
// so production deployments that don't set the env var still render.
const API_ORIGIN = import.meta.env.VITE_API_URL || window.location.origin

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
      invoice_prefix: '', net_payment_terms: 0,
      tax_provider: 'manual', tax_rate_bp: 0, tax_name: '', tax_inclusive: false,
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
        tax_provider: (s.tax_provider as 'none' | 'manual') || 'manual',
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
        tax_provider: (updated.tax_provider as 'none' | 'manual') || 'manual',
        tax_rate_bp: updated.tax_rate_bp || 0, tax_name: updated.tax_name || '', tax_inclusive: updated.tax_inclusive || false,
        default_currency: updated.default_currency || '', timezone: updated.timezone || '',
      }
      reset(f)
      if (updated.default_currency) setActiveCurrency(updated.default_currency)
      toast.success('Settings saved')
    } catch (err) {
      applyApiError(formMethods, err, [
        'company_name', 'company_email', 'company_phone', 'company_address', 'logo_url',
        'invoice_prefix', 'net_payment_terms', 'tax_provider', 'tax_rate_bp', 'tax_name', 'tax_inclusive',
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
              <p className="text-xs text-muted-foreground">Select a tax backend. Per-customer overrides available on billing profiles.</p>
            </div>
          </div>
          <Card>
            <CardContent className="p-6">
              <div>
                <Label>Tax Provider</Label>
                <div className="mt-2 grid grid-cols-1 md:grid-cols-2 gap-3">
                  <button
                    type="button"
                    onClick={() => setValue('tax_provider', 'none', { shouldDirty: true })}
                    className={cn(
                      'text-left p-4 rounded-lg border transition-colors',
                      form.tax_provider === 'none'
                        ? 'border-primary bg-primary/5 ring-1 ring-primary'
                        : 'border-border hover:border-muted-foreground/50'
                    )}
                  >
                    <div className="font-medium text-sm text-foreground">None</div>
                    <div className="text-xs text-muted-foreground mt-1">Don't collect tax. Invoices ship with zero tax lines.</div>
                  </button>
                  <button
                    type="button"
                    onClick={() => setValue('tax_provider', 'manual', { shouldDirty: true })}
                    className={cn(
                      'text-left p-4 rounded-lg border transition-colors',
                      form.tax_provider === 'manual'
                        ? 'border-primary bg-primary/5 ring-1 ring-primary'
                        : 'border-border hover:border-muted-foreground/50'
                    )}
                  >
                    <div className="font-medium text-sm text-foreground">Manual (flat rate)</div>
                    <div className="text-xs text-muted-foreground mt-1">Single rate applied to customers in your home country. Exports zero-rated.</div>
                  </button>
                </div>
              </div>

              {form.tax_provider === 'manual' && (
                <>
                  <div className="mt-6 grid grid-cols-1 md:grid-cols-2 gap-5">
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
                          </span>
                        )}
                      </p>
                    </div>
                  )}
                </>
              )}
            </CardContent>
          </Card>
        </section>

        {/* Payments — per-tenant Stripe credentials */}
        <div className="pb-24">
          <PaymentsSection />
        </div>
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

function PaymentsSection() {
  const queryClient = useQueryClient()
  const { data, isLoading, error } = useQuery({
    queryKey: ['stripe-credentials'],
    queryFn: () => api.listStripeCredentials(),
  })

  const byMode = (rows: StripeProviderCredentials[] | undefined, live: boolean) =>
    rows?.find(r => r.livemode === live)

  const rows = data?.data ?? []

  return (
    <section>
      <div className="flex items-center gap-2 mb-4">
        <div className="w-8 h-8 rounded-lg bg-violet-50 dark:bg-violet-500/10 flex items-center justify-center">
          <CreditCard size={16} className="text-violet-600 dark:text-violet-400" />
        </div>
        <div>
          <h2 className="text-sm font-semibold text-foreground">Payments</h2>
          <p className="text-xs text-muted-foreground">Connect your own Stripe account — Velox charges invoices through your keys, money flows straight to you.</p>
        </div>
      </div>

      {isLoading ? (
        <Card><CardContent className="p-6 flex justify-center"><Loader2 className="h-5 w-5 animate-spin text-muted-foreground" /></CardContent></Card>
      ) : error ? (
        <Card><CardContent className="p-6"><p className="text-sm text-destructive">{error instanceof Error ? error.message : 'Failed to load Stripe credentials'}</p></CardContent></Card>
      ) : (
        <div className="space-y-4">
          <StripeModeCard
            mode="test"
            current={byMode(rows, false)}
            onChanged={() => queryClient.invalidateQueries({ queryKey: ['stripe-credentials'] })}
          />
          <StripeModeCard
            mode="live"
            current={byMode(rows, true)}
            onChanged={() => queryClient.invalidateQueries({ queryKey: ['stripe-credentials'] })}
          />
        </div>
      )}
    </section>
  )
}

// Prefix shape validated client-side before the request so operators see
// field-level errors without waiting for the server. Mirrors the backend's
// validateKeyShape — both must stay in sync.
const stripeConnectSchema = z.object({
  secret_key: z.string().min(1, 'Secret key is required'),
  publishable_key: z.string().min(1, 'Publishable key is required'),
  webhook_secret: z.string().optional(),
})
type StripeConnectForm = z.infer<typeof stripeConnectSchema>

function StripeModeCard({ mode, current, onChanged }: {
  mode: 'test' | 'live'
  current: StripeProviderCredentials | undefined
  onChanged: () => void
}) {
  const livemode = mode === 'live'
  const [editing, setEditing] = useState(false)
  const connected = !!current

  const connectedOk = connected && !!current?.verified_at && !current?.last_verified_error
  const connectedFailing = connected && !!current?.last_verified_error

  const methods = useForm<StripeConnectForm>({
    resolver: zodResolver(stripeConnectSchema),
    defaultValues: { secret_key: '', publishable_key: '', webhook_secret: '' },
  })
  const { register, handleSubmit, reset, formState: { errors } } = methods

  const connect = useMutation({
    mutationFn: (data: StripeConnectForm) => api.connectStripe({ ...data, livemode }),
    onSuccess: (row) => {
      if (row.last_verified_error) {
        toast.error(`Saved, but Stripe verify failed: ${row.last_verified_error}`)
      } else {
        toast.success(`Connected ${livemode ? 'live' : 'test'} mode${row.stripe_account_name ? ` as ${row.stripe_account_name}` : ''}`)
      }
      reset()
      setEditing(false)
      onChanged()
    },
    onError: (err) => {
      applyApiError(methods, err, ['secret_key', 'publishable_key', 'webhook_secret'], {
        toastTitle: 'Failed to connect Stripe',
      })
    },
  })

  const remove = useMutation({
    mutationFn: () => api.deleteStripeCredentials(mode),
    onSuccess: () => {
      toast.success(`Disconnected ${mode} mode`)
      onChanged()
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : 'Failed to disconnect')
    },
  })

  const onSubmit = handleSubmit((data) => connect.mutate(data))

  const title = livemode ? 'Live mode' : 'Test mode'
  const titleSub = livemode ? 'Production Stripe keys — real charges.' : 'Safe to experiment — no real money moves.'

  return (
    <Card>
      <CardContent className="p-6 space-y-4">
        <div className="flex items-start justify-between gap-4">
          <div>
            <div className="flex items-center gap-2">
              <h3 className="text-sm font-semibold text-foreground">{title}</h3>
              {connectedOk && (
                <span className="text-[11px] uppercase tracking-wide px-2 py-0.5 rounded-full bg-emerald-50 text-emerald-700 dark:bg-emerald-500/10 dark:text-emerald-400 flex items-center gap-1">
                  <Check size={10} /> Connected
                </span>
              )}
              {connectedFailing && (
                <span className="text-[11px] uppercase tracking-wide px-2 py-0.5 rounded-full bg-amber-50 text-amber-700 dark:bg-amber-500/10 dark:text-amber-400 flex items-center gap-1">
                  <AlertCircle size={10} /> Needs attention
                </span>
              )}
              {!connected && (
                <span className="text-[11px] uppercase tracking-wide px-2 py-0.5 rounded-full bg-muted text-muted-foreground">
                  Not connected
                </span>
              )}
            </div>
            <p className="text-xs text-muted-foreground mt-0.5">{titleSub}</p>
          </div>

          {connected && !editing && (
            <div className="flex items-center gap-2">
              <Button variant="outline" size="sm" onClick={() => setEditing(true)}>Replace keys</Button>
              <AlertDialog>
                <AlertDialogTrigger asChild>
                  <Button variant="ghost" size="sm" className="text-destructive hover:text-destructive">
                    <Trash2 size={14} className="mr-1" /> Disconnect
                  </Button>
                </AlertDialogTrigger>
                <AlertDialogContent>
                  <AlertDialogHeader>
                    <AlertDialogTitle>Disconnect {mode} mode?</AlertDialogTitle>
                    <AlertDialogDescription>
                      Velox will stop charging new invoices through this Stripe account.
                      Existing Stripe charges and refunds are unaffected. You can reconnect with new keys anytime.
                    </AlertDialogDescription>
                  </AlertDialogHeader>
                  <AlertDialogFooter>
                    <AlertDialogCancel>Cancel</AlertDialogCancel>
                    <AlertDialogAction onClick={() => remove.mutate()}>Disconnect</AlertDialogAction>
                  </AlertDialogFooter>
                </AlertDialogContent>
              </AlertDialog>
            </div>
          )}
        </div>

        {connected && !editing && (
          <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
            <ReadOnlyRow label="Stripe account" value={current?.stripe_account_name || current?.stripe_account_id || '—'} />
            <ReadOnlyRow label="Secret key" value={current?.secret_key_last4 ? `•••• ${current.secret_key_last4}` : '—'} mono />
            <ReadOnlyRow label="Publishable key" value={current?.publishable_key || '—'} mono />
            <ReadOnlyRow
              label="Webhook secret"
              value={current?.has_webhook_secret ? `•••• ${current?.webhook_secret_last4 ?? ''}` : 'Not set'}
              mono={current?.has_webhook_secret}
            />
            {current?.has_webhook_secret && <WebhookUrlRow endpointId={current.id} />}
            {connectedFailing && (
              <div className="md:col-span-2 p-3 rounded-lg bg-amber-50 dark:bg-amber-500/10 border border-amber-200 dark:border-amber-500/20">
                <p className="text-xs text-amber-800 dark:text-amber-300 flex items-start gap-2">
                  <AlertCircle size={12} className="shrink-0 mt-0.5" />
                  <span>Last Stripe verify failed: {current?.last_verified_error}</span>
                </p>
              </div>
            )}
          </div>
        )}

        {(!connected || editing) && (
          <form onSubmit={onSubmit} className="space-y-4">
            <div>
              <Label>Secret key</Label>
              <Input
                type="password"
                autoComplete="off"
                placeholder={livemode ? 'sk_live_...' : 'sk_test_...'}
                {...register('secret_key')}
                className={cn('mt-1 font-mono', errors.secret_key && 'border-destructive')}
              />
              {errors.secret_key && <p className="text-xs text-destructive mt-1">{errors.secret_key.message}</p>}
              <p className="text-xs text-muted-foreground mt-1">
                Find it at{' '}
                <a
                  href={livemode ? 'https://dashboard.stripe.com/apikeys' : 'https://dashboard.stripe.com/test/apikeys'}
                  target="_blank" rel="noreferrer"
                  className="underline underline-offset-2 hover:text-foreground"
                >
                  dashboard.stripe.com/{livemode ? '' : 'test/'}apikeys
                </a>. Restricted keys (rk_) are also accepted.
              </p>
            </div>
            <div>
              <Label>Publishable key</Label>
              <Input
                type="text"
                autoComplete="off"
                placeholder={livemode ? 'pk_live_...' : 'pk_test_...'}
                {...register('publishable_key')}
                className={cn('mt-1 font-mono', errors.publishable_key && 'border-destructive')}
              />
              {errors.publishable_key && <p className="text-xs text-destructive mt-1">{errors.publishable_key.message}</p>}
            </div>
            <div>
              <Label>Webhook signing secret <span className="text-muted-foreground font-normal">(optional)</span></Label>
              <Input
                type="password"
                autoComplete="off"
                placeholder="whsec_..."
                {...register('webhook_secret')}
                className={cn('mt-1 font-mono', errors.webhook_secret && 'border-destructive')}
              />
              {errors.webhook_secret && <p className="text-xs text-destructive mt-1">{errors.webhook_secret.message}</p>}
              <p className="text-xs text-muted-foreground mt-1">
                After saving the API keys, Velox shows a webhook URL below — paste it into Stripe Dashboard → Developers → Webhooks,
                then return here with the <code className="font-mono">whsec_...</code> Stripe generates.
              </p>
            </div>
            <div className="flex items-center gap-2 pt-1">
              <Button type="submit" disabled={connect.isPending}>
                {connect.isPending
                  ? <><Loader2 size={14} className="animate-spin mr-2" /> Verifying with Stripe...</>
                  : (connected ? 'Save new keys' : `Connect ${mode} mode`)}
              </Button>
              {editing && (
                <Button variant="ghost" type="button" onClick={() => { reset(); setEditing(false) }}>Cancel</Button>
              )}
            </div>
          </form>
        )}
      </CardContent>
    </Card>
  )
}

function ReadOnlyRow({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="py-2.5 px-3 bg-muted rounded-lg">
      <p className="text-xs text-muted-foreground mb-0.5">{label}</p>
      <p className={cn('text-sm text-foreground break-all', mono && 'font-mono')}>{value}</p>
    </div>
  )
}

function WebhookUrlRow({ endpointId }: { endpointId: string }) {
  const url = `${API_ORIGIN}/v1/webhooks/stripe/${endpointId}`
  return (
    <div className="md:col-span-2 py-2.5 px-3 bg-muted rounded-lg">
      <div className="flex items-center justify-between gap-3">
        <div className="min-w-0 flex-1">
          <p className="text-xs text-muted-foreground mb-0.5">Stripe webhook URL</p>
          <code className="text-sm font-mono text-foreground break-all">{url}</code>
        </div>
        <Button
          variant="ghost"
          size="sm"
          onClick={() => { navigator.clipboard.writeText(url); toast.success('Webhook URL copied') }}
        >
          <Copy size={14} />
        </Button>
      </div>
      <p className="text-xs text-muted-foreground mt-2">
        Paste into Stripe Dashboard → Developers → Webhooks → Add endpoint. Events:{' '}
        <code className="font-mono">payment_intent.succeeded</code>,{' '}
        <code className="font-mono">payment_intent.payment_failed</code>.
      </p>
    </div>
  )
}
