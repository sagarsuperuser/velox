import { useState, useEffect } from 'react'
import { useParams, Link } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { toast } from 'sonner'
import { api, downloadPDF, formatCents, formatDate, formatDateTime, getCurrencySymbol, type Invoice, type LineItem, type TenantSettings } from '@/lib/api'
import { applyApiError, showApiError } from '@/lib/formErrors'
import { ExpiryBadge } from '@/components/ExpiryBadge'
import { Layout } from '@/components/Layout'
import { cn } from '@/lib/utils'
import { statusBadgeVariant } from '@/lib/status'

import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Separator } from '@/components/ui/separator'
import {
  Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle,
} from '@/components/ui/dialog'
import { TypedConfirmDialog } from '@/components/TypedConfirmDialog'
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from '@/components/ui/table'
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from '@/components/ui/select'

import { Loader2, Mail, CreditCard, Link2, RotateCw } from 'lucide-react'
import { CopyButton } from '@/components/CopyButton'
import { DetailBreadcrumb } from '@/components/DetailBreadcrumb'

const statusVariant = statusBadgeVariant

const LINE_TYPE_LABELS: Record<string, string> = {
  base_fee: 'Base Fee',
  usage: 'Usage',
  add_on: 'Add-On',
  discount: 'Discount',
  tax: 'Tax',
}

function formatCompanyAddressLines(s?: TenantSettings | null): string[] {
  if (!s) return []
  const lines: string[] = []
  if (s.company_address_line1) lines.push(s.company_address_line1)
  if (s.company_address_line2) lines.push(s.company_address_line2)
  const cityStatePostal = [s.company_city, s.company_state].filter(Boolean).join(', ')
    + (s.company_postal_code ? ` ${s.company_postal_code}` : '')
  if (cityStatePostal.trim()) lines.push(cityStatePostal.trim())
  if (s.company_country) lines.push(s.company_country)
  return lines
}

function formatLineType(raw: string): string {
  return LINE_TYPE_LABELS[raw] || raw.replace(/_/g, ' ').replace(/\b\w/g, c => c.toUpperCase())
}

// aggregateTaxByJurisdiction rolls up per-line-item tax amounts into a single
// row per jurisdiction, preserving insertion order so the UI is deterministic
// when two jurisdictions have identical contributions. Mirrors the PDF
// breakdown so invoice totals match between web view and PDF.
function aggregateTaxByJurisdiction(items: LineItem[]): { label: string; amount: number }[] {
  const order: string[] = []
  const totals = new Map<string, number>()
  for (const item of items) {
    const amount = item.tax_amount_cents || 0
    if (amount <= 0) continue
    const label = item.tax_jurisdiction || ''
    if (!label) continue
    if (!totals.has(label)) order.push(label)
    totals.set(label, (totals.get(label) || 0) + amount)
  }
  return order.map(label => ({ label, amount: totals.get(label) || 0 }))
}

const emailSchema = z.object({
  email: z.string().min(1, 'Email is required').email('Invalid email address'),
})
type EmailFormData = z.infer<typeof emailSchema>

const creditModalSchema = z.object({
  reason: z.string().min(1, 'Reason is required'),
  amount: z.string().min(1, 'Amount is required').refine(v => parseFloat(v) >= 0.01, 'Must be at least 0.01').refine(v => parseFloat(v) <= 999999.99, 'Must be at most 999999.99'),
})
type CreditModalData = z.infer<typeof creditModalSchema>

export default function InvoiceDetailPage() {
  const { id } = useParams<{ id: string }>()
  const queryClient = useQueryClient()
  const [showVoidConfirm, setShowVoidConfirm] = useState(false)
  const [showRotatePublicTokenConfirm, setShowRotatePublicTokenConfirm] = useState(false)
  const [showEmailModal, setShowEmailModal] = useState(false)
  const [showCreditModal, setShowCreditModal] = useState(false)
  const [showAddLineItem, setShowAddLineItem] = useState(false)
  const [showApplyCoupon, setShowApplyCoupon] = useState(false)
  const [pdfPreviewUrl, setPdfPreviewUrl] = useState<string | null>(null)

  useEffect(() => {
    return () => { if (pdfPreviewUrl) URL.revokeObjectURL(pdfPreviewUrl) }
  }, [pdfPreviewUrl])

  const { data: invoiceData, isLoading, error: loadError, refetch } = useQuery({
    queryKey: ['invoice', id],
    queryFn: () => api.getInvoice(id!),
    enabled: !!id,
  })

  const invoice = invoiceData?.invoice
  const lineItems = invoiceData?.line_items ?? []

  const { data: customer } = useQuery({
    queryKey: ['customer', invoice?.customer_id],
    queryFn: () => api.getCustomer(invoice!.customer_id),
    enabled: !!invoice?.customer_id,
  })

  const { data: subscription } = useQuery({
    queryKey: ['subscription', invoice?.subscription_id],
    queryFn: () => api.getSubscription(invoice!.subscription_id),
    enabled: !!invoice?.subscription_id,
  })

  const { data: creditNotesData } = useQuery({
    queryKey: ['invoice-credit-notes', id],
    queryFn: async () => {
      const cn = await api.listCreditNotes(`invoice_id=${id}`)
      return (cn.data || []).filter(n => n.status === 'issued')
    },
    enabled: !!id,
  })
  const creditNotes = creditNotesData ?? []

  const { data: timelineData } = useQuery({
    queryKey: ['invoice-timeline', id],
    queryFn: () => api.getPaymentTimeline(id!).then(r => r.events || []),
    enabled: !!id && invoice?.status !== 'draft',
  })
  const timeline = timelineData ?? []

  const { data: settings } = useQuery({
    queryKey: ['settings'],
    queryFn: () => api.getSettings(),
  })

  const invalidateAll = () => {
    queryClient.invalidateQueries({ queryKey: ['invoice', id] })
    queryClient.invalidateQueries({ queryKey: ['invoice-credit-notes', id] })
    queryClient.invalidateQueries({ queryKey: ['invoice-timeline', id] })
    queryClient.invalidateQueries({ queryKey: ['invoices'] })
  }

  const finalizeMutation = useMutation({
    mutationFn: () => api.finalizeInvoice(id!),
    onSuccess: () => { invalidateAll(); toast.success(`Invoice ${invoice?.invoice_number} finalized`) },
    onError: (err) => showApiError(err, 'Failed to finalize'),
  })

  const voidMutation = useMutation({
    mutationFn: () => api.voidInvoice(id!),
    onSuccess: () => { invalidateAll(); setShowVoidConfirm(false); toast.success(`Invoice ${invoice?.invoice_number} voided`) },
    onError: (err) => showApiError(err, 'Failed to void'),
  })

  const collectMutation = useMutation({
    mutationFn: () => api.collectPayment(id!),
    onSuccess: () => { invalidateAll(); toast.success('Payment initiated') },
    onError: (err) => showApiError(err, 'Payment failed'),
  })

  const rotatePublicTokenMutation = useMutation({
    mutationFn: () => api.rotateInvoicePublicToken(id!),
    onSuccess: () => {
      invalidateAll()
      setShowRotatePublicTokenConfirm(false)
      toast.success('Public link rotated — previous URL no longer works')
    },
    onError: err => showApiError(err, 'Failed to rotate public link'),
  })

  const acting = finalizeMutation.isPending || voidMutation.isPending || collectMutation.isPending || rotatePublicTokenMutation.isPending

  // Build the shareable hosted-invoice URL for clipboard copy. Uses the
  // dashboard origin — deployments that serve the dashboard and the
  // hosted page on separate domains should set VITE_HOSTED_INVOICE_BASE_URL.
  const hostedInvoiceBase = import.meta.env.VITE_HOSTED_INVOICE_BASE_URL || window.location.origin
  const publicInvoiceURL = invoice?.public_token ? `${hostedInvoiceBase}/invoice/${invoice.public_token}` : ''

  const copyPublicLink = async () => {
    if (!publicInvoiceURL) return
    try {
      await navigator.clipboard.writeText(publicInvoiceURL)
      toast.success('Public link copied to clipboard')
    } catch {
      toast.error('Failed to copy — you can select the URL manually')
    }
  }

  const loading = isLoading
  const error = loadError instanceof Error ? loadError.message : loadError ? String(loadError) : null

  if (loading) {
    return (
      <Layout>
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          <Link to="/invoices" className="hover:text-foreground transition-colors">Invoices</Link>
          <span>/</span>
          <span>Loading...</span>
        </div>
        <div className="flex justify-center py-16">
          <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
        </div>
      </Layout>
    )
  }

  if (error) {
    return (
      <Layout>
        <div className="py-16 text-center">
          <p className="text-sm text-destructive mb-3">{error}</p>
          <Button variant="outline" size="sm" onClick={() => refetch()}>Retry</Button>
        </div>
      </Layout>
    )
  }

  if (!invoice) {
    return (
      <Layout>
        <p className="text-sm text-muted-foreground py-16 text-center">Invoice not found</p>
      </Layout>
    )
  }

  return (
    <Layout>
      <DetailBreadcrumb to="/invoices" parentLabel="Invoices" currentLabel={invoice.invoice_number} />

      {/* Header */}
      <div className="flex items-center justify-between mb-6">
        <div>
          <h1 className="text-2xl font-semibold text-foreground">{invoice.invoice_number}</h1>
          <div className="flex items-center gap-1.5 mt-1">
            <span className="text-xs font-mono text-muted-foreground">{invoice.id}</span>
            <CopyButton text={invoice.id} />
          </div>
        </div>
        <div className="flex items-center gap-2">
          {invoice.status === 'draft' && (
            <>
              <Button variant="outline" size="sm" onClick={() => setShowAddLineItem(true)} disabled={acting}>
                Add Line Item
              </Button>
              {invoice.discount_cents === 0 && !invoice.tax_transaction_id && (
                <Button variant="outline" size="sm" onClick={() => setShowApplyCoupon(true)} disabled={acting}>
                  Apply Coupon
                </Button>
              )}
              <Button size="sm" onClick={() => finalizeMutation.mutate()} disabled={acting}>
                Finalize
              </Button>
            </>
          )}

          {invoice.status !== 'voided' && invoice.status !== 'paid' && (
            <Button variant="outline" size="sm" className="border-destructive text-destructive hover:bg-destructive/10" onClick={() => setShowVoidConfirm(true)} disabled={acting}>
              Void
            </Button>
          )}

          {invoice.status !== 'voided' && (
            <Button variant="outline" size="sm" onClick={() => setShowEmailModal(true)} disabled={acting}>
              <Mail size={14} className="mr-1.5" />
              Email
            </Button>
          )}

          {(invoice.status === 'finalized' || invoice.status === 'paid') && (
            <Button variant="outline" size="sm" onClick={() => setShowCreditModal(true)} disabled={acting}>
              <CreditCard size={14} className="mr-1.5" />
              Issue Credit
            </Button>
          )}

          {/* Hosted-invoice URL actions: only visible when the invoice has
              a public token — i.e. it's been finalized (drafts have none)
              and hasn't been pre-addendum-finalized without a rotate. */}
          {invoice.public_token && invoice.status !== 'draft' && (
            <>
              <Button variant="outline" size="sm" onClick={copyPublicLink} disabled={acting} title={publicInvoiceURL}>
                <Link2 size={14} className="mr-1.5" />
                Copy Link
              </Button>
              <Button variant="outline" size="sm" onClick={() => setShowRotatePublicTokenConfirm(true)} disabled={acting} title="Invalidate the current URL and mint a new one">
                <RotateCw size={14} className="mr-1.5" />
                Rotate
              </Button>
            </>
          )}

          {invoice.status === 'finalized' && invoice.payment_status !== 'paid' && invoice.amount_due_cents > 0 && (
            <Button size="sm" onClick={() => collectMutation.mutate()} disabled={acting}>
              Collect Payment
            </Button>
          )}

          <Button
            variant="outline" size="sm"
            onClick={async () => {
              try {
                const res = await fetch(`/v1/invoices/${invoice.id}/pdf`, {
                  credentials: 'same-origin',
                })
                const blob = await res.blob()
                const url = URL.createObjectURL(blob)
                setPdfPreviewUrl(url)
              } catch {
                toast.error('Failed to load PDF preview')
              }
            }}
          >
            Preview PDF
          </Button>
          <Button size="sm" onClick={() => downloadPDF(invoice.id, invoice.invoice_number)}>
            Download PDF
          </Button>
        </div>
      </div>

      {/* Invoice Document */}
      <Card className={cn(
        'overflow-hidden',
        invoice.status === 'voided' && 'border-destructive/30'
      )}>
        <CardContent className="p-8 sm:p-10">
          {/* Document Header */}
          <div className="flex items-start justify-between">
            <div>
              <p className="text-2xl font-bold tracking-tight text-foreground">
                {settings?.company_name || 'VELOX'}
              </p>
              {formatCompanyAddressLines(settings).map((line, i) => (
                <p key={i} className="text-sm text-muted-foreground mt-1">{line}</p>
              ))}
            </div>
            <div className="text-right">
              <p className="text-sm font-medium uppercase tracking-widest text-muted-foreground">Invoice</p>
              <p className="text-xl font-semibold text-foreground mt-0.5">{invoice.invoice_number}</p>
            </div>
          </div>

          {/* Status badges */}
          <div className="flex items-center gap-2 mt-5">
            <Badge variant={statusVariant(invoice.status)}>{invoice.status}</Badge>
            {invoice.status === 'finalized' && (
              <Badge variant={statusVariant(invoice.payment_status)}>{invoice.payment_status}</Badge>
            )}
            {invoice.status === 'voided' && (
              <span className="text-xs font-semibold text-destructive uppercase tracking-wider ml-1">
                {invoice.voided_at ? `Voided ${formatDate(invoice.voided_at)}` : 'Voided'}
              </span>
            )}
            {invoice.status === 'paid' && invoice.paid_at && (
              <span className="text-xs text-muted-foreground ml-1">Paid {formatDate(invoice.paid_at)}</span>
            )}
          </div>

          <Separator className="my-6" />

          {/* FROM / BILL TO */}
          <div className="grid grid-cols-2 gap-8">
            <div>
              <p className="text-xs font-semibold uppercase tracking-wider text-muted-foreground mb-2">From</p>
              <p className="text-sm font-medium text-foreground">{settings?.company_name || 'Your Company'}</p>
              {formatCompanyAddressLines(settings).map((line, i) => (
                <p key={i} className="text-sm text-muted-foreground mt-0.5">{line}</p>
              ))}
              {settings?.company_email && (
                <p className="text-sm text-muted-foreground mt-0.5">{settings.company_email}</p>
              )}
              {settings?.company_phone && (
                <p className="text-sm text-muted-foreground mt-0.5">{settings.company_phone}</p>
              )}
            </div>
            <div>
              <p className="text-xs font-semibold uppercase tracking-wider text-muted-foreground mb-2">Bill To</p>
              {customer ? (
                <>
                  <Link to={`/customers/${customer.id}`} className="text-sm font-medium text-primary hover:underline">
                    {customer.display_name}
                  </Link>
                  {customer.email && (
                    <p className="text-sm text-muted-foreground mt-0.5">{customer.email}</p>
                  )}
                </>
              ) : (
                <p className="text-sm font-mono text-muted-foreground">{invoice.customer_id}</p>
              )}
              {subscription && (
                <p className="text-sm text-muted-foreground mt-0.5">
                  Sub: <Link to={`/subscriptions/${subscription.id}`} className="text-primary hover:underline">{subscription.display_name}</Link>
                </p>
              )}
            </div>
          </div>

          <Separator className="my-6" />

          {/* Dates row */}
          <div className="grid grid-cols-2 sm:grid-cols-4 gap-4">
            <div>
              <p className="text-xs font-semibold uppercase tracking-wider text-muted-foreground mb-1">Issued</p>
              <p className="text-sm text-foreground">{invoice.issued_at ? formatDate(invoice.issued_at) : formatDate(invoice.created_at)}</p>
            </div>
            <div>
              <p className="text-xs font-semibold uppercase tracking-wider text-muted-foreground mb-1">Due</p>
              <div className="flex items-center gap-2">
                <p className="text-sm text-foreground">{invoice.due_at ? formatDate(invoice.due_at) : '\u2014'}</p>
                {invoice.due_at && invoice.payment_status !== 'paid' && (
                  <ExpiryBadge expiresAt={invoice.due_at} label="Due" warningDays={3} />
                )}
              </div>
            </div>
            <div>
              <p className="text-xs font-semibold uppercase tracking-wider text-muted-foreground mb-1">Period</p>
              <p className="text-sm text-foreground">
                {formatDate(invoice.billing_period_start)} – {formatDate(invoice.billing_period_end)}
              </p>
            </div>
            <div>
              <p className="text-xs font-semibold uppercase tracking-wider text-muted-foreground mb-1">Terms</p>
              <p className="text-sm text-foreground">
                {settings?.net_payment_terms ? `Net ${settings.net_payment_terms}` : '\u2014'}
              </p>
            </div>
          </div>

          <Separator className="my-6" />

          {/* Line Items Table */}
          <div className="-mx-2">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-[50%]">Item</TableHead>
                  <TableHead className="text-right">Qty</TableHead>
                  <TableHead className="text-right">Unit Price</TableHead>
                  <TableHead className="text-right">Amount</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {lineItems.map(item => (
                  <TableRow key={item.id} className={cn(invoice.status === 'voided' && 'opacity-50')}>
                    <TableCell>
                      <span className="text-sm text-foreground">{item.description}</span>
                      <span className="ml-2 text-xs text-muted-foreground">({formatLineType(item.line_type)})</span>
                    </TableCell>
                    <TableCell className="text-right font-mono tabular-nums text-sm">{item.quantity.toLocaleString()}</TableCell>
                    <TableCell className="text-right font-mono tabular-nums text-sm">{formatCents(item.unit_amount_cents, invoice.currency)}</TableCell>
                    <TableCell className="text-right font-mono tabular-nums text-sm font-medium">{formatCents(item.amount_cents, invoice.currency)}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>

          {/* Summary section */}
          <div className="flex justify-end mt-4">
            <div className="w-72 space-y-2">
              {/* Subtotal */}
              <div className={cn('flex justify-between text-sm', invoice.status === 'voided' && 'text-muted-foreground')}>
                <span>Subtotal</span>
                <span className="font-mono tabular-nums">{formatCents(invoice.subtotal_cents, invoice.currency)}</span>
              </div>

              {/* Discount */}
              {invoice.discount_cents > 0 && (
                <div className={cn('flex justify-between text-sm', invoice.status === 'voided' && 'text-muted-foreground')}>
                  <span>Discount</span>
                  <span className="font-mono tabular-nums">-{formatCents(invoice.discount_cents, invoice.currency)}</span>
                </div>
              )}

              {/* Tax — three states: standard (amount), reverse charge (legend row), exempt (legend row) */}
              {invoice.tax_amount_cents > 0 && (
                <>
                  <div className={cn('flex justify-between text-sm', invoice.status === 'voided' && 'text-muted-foreground')}>
                    <span>{invoice.tax_name || 'Tax'}{invoice.tax_rate_bp > 0 ? ` (${(invoice.tax_rate_bp / 100).toFixed(2)}%)` : ''}</span>
                    <span className="font-mono tabular-nums">{formatCents(invoice.tax_amount_cents, invoice.currency)}</span>
                  </div>
                  {(() => {
                    const jurisdictionRows = aggregateTaxByJurisdiction(lineItems)
                    if (jurisdictionRows.length < 2) return null
                    return (
                      <div className="pl-4 space-y-0.5">
                        {jurisdictionRows.map(row => (
                          <div key={row.label} className="flex justify-between text-xs text-muted-foreground">
                            <span>{row.label}</span>
                            <span className="font-mono tabular-nums">{formatCents(row.amount, invoice.currency)}</span>
                          </div>
                        ))}
                      </div>
                    )
                  })()}
                </>
              )}
              {invoice.tax_reverse_charge && invoice.tax_amount_cents === 0 && (
                <div className={cn('flex justify-between text-sm text-muted-foreground', invoice.status === 'voided' && 'opacity-60')}>
                  <span>Tax (reverse charge)</span>
                  <span className="font-mono tabular-nums">{formatCents(0, invoice.currency)}</span>
                </div>
              )}
              {invoice.tax_exempt_reason && invoice.tax_amount_cents === 0 && !invoice.tax_reverse_charge && (
                <div className={cn('flex justify-between text-sm text-muted-foreground', invoice.status === 'voided' && 'opacity-60')}>
                  <span>Tax (exempt)</span>
                  <span className="font-mono tabular-nums">{formatCents(0, invoice.currency)}</span>
                </div>
              )}

              {/* Total */}
              {(invoice.discount_cents > 0 || invoice.tax_amount_cents > 0) && (
                <div className={cn('flex justify-between text-sm font-medium', invoice.status === 'voided' && 'text-muted-foreground')}>
                  <span>Total</span>
                  <span className="font-mono tabular-nums">{formatCents(invoice.total_amount_cents, invoice.currency)}</span>
                </div>
              )}

              {/* Settlement waterfall */}
              {invoice.status === 'voided' ? (
                <>
                  <Separator />
                  <div className="flex justify-between text-sm font-semibold">
                    <span>Amount Due</span>
                    <span className="font-mono tabular-nums">$0.00</span>
                  </div>
                </>
              ) : (() => {
                const prePaymentCNs = invoice.status === 'paid'
                  ? creditNotes.filter(cn => cn.refund_amount_cents === 0 && cn.credit_amount_cents === 0)
                  : creditNotes
                const postPaymentCNs = invoice.status === 'paid'
                  ? creditNotes.filter(cn => cn.refund_amount_cents > 0 || cn.credit_amount_cents > 0)
                  : []

                return (
                  <>
                    {prePaymentCNs.map(cn => (
                      <div key={cn.id} className="flex justify-between text-sm text-emerald-600">
                        <span className="truncate mr-2">Credit {cn.credit_note_number}</span>
                        <span className="font-mono tabular-nums shrink-0">-{formatCents(cn.total_cents, invoice.currency)}</span>
                      </div>
                    ))}

                    {invoice.credits_applied_cents > 0 && (
                      <div className="flex justify-between text-sm text-emerald-600">
                        <span>Credits Applied</span>
                        <span className="font-mono tabular-nums">-{formatCents(invoice.credits_applied_cents, invoice.currency)}</span>
                      </div>
                    )}

                    {invoice.amount_paid_cents > 0 && (
                      <div className="flex justify-between text-sm text-muted-foreground">
                        <span>Amount Paid</span>
                        <span className="font-mono tabular-nums">-{formatCents(invoice.amount_paid_cents, invoice.currency)}</span>
                      </div>
                    )}

                    <Separator />
                    <div className="flex justify-between font-semibold text-base pt-1">
                      <span>Amount Due</span>
                      <span className="font-mono tabular-nums">{formatCents(invoice.amount_due_cents, invoice.currency)}</span>
                    </div>

                    {/* Post-payment adjustments */}
                    {(() => {
                      const completedCNs = postPaymentCNs.filter(cn =>
                        cn.credit_amount_cents > 0 ||
                        (cn.refund_amount_cents > 0 && cn.refund_status === 'succeeded')
                      )
                      return completedCNs.length > 0 ? (
                        <div className="mt-3 pt-3 border-t border-dashed border-border space-y-1.5">
                          <p className="text-[10px] font-medium text-muted-foreground uppercase tracking-wider">Post-payment adjustments</p>
                          {completedCNs.map(cn => (
                            <div key={cn.id} className="flex justify-between text-xs text-muted-foreground">
                              <span className="truncate mr-2">
                                {cn.credit_note_number} -- {cn.reason}
                                <span className="ml-1">
                                  {cn.refund_amount_cents > 0 ? '(refunded)' : '(credited)'}
                                </span>
                              </span>
                              <span className="font-mono tabular-nums shrink-0">{formatCents(cn.total_cents, invoice.currency)}</span>
                            </div>
                          ))}
                        </div>
                      ) : null
                    })()}
                  </>
                )
              })()}
            </div>
          </div>

          {/* Tax treatment legend — shown when invoice carries a non-standard tax disposition */}
          {(invoice.tax_reverse_charge || invoice.tax_exempt_reason) && (
            <div className="mt-6 pt-4 border-t border-dashed border-border space-y-1.5 text-xs text-muted-foreground">
              {invoice.tax_reverse_charge && (
                <p><span className="font-medium text-foreground">Reverse charge</span> — VAT to be accounted for by the recipient in their jurisdiction.</p>
              )}
              {invoice.tax_exempt_reason && !invoice.tax_reverse_charge && (
                <p><span className="font-medium text-foreground">Tax-exempt</span> — {invoice.tax_exempt_reason}</p>
              )}
            </div>
          )}

          {/* ID footer */}
          <div className="mt-8 pt-4 border-t border-dashed border-border flex items-center justify-between">
            <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
              <span className="font-mono">{invoice.id}</span>
              <CopyButton text={invoice.id} />
            </div>
            <div className="flex items-center gap-3 text-xs text-muted-foreground">
              {invoice.tax_provider && invoice.tax_provider !== 'none' && (
                <span className="uppercase">
                  Tax: {invoice.tax_provider === 'stripe_tax' ? 'Stripe Tax' : invoice.tax_provider}
                </span>
              )}
              <span className="uppercase">{invoice.currency}</span>
            </div>
          </div>
        </CardContent>
      </Card>

      {/* Payment failed banner */}
      {invoice.payment_status === 'failed' && invoice.status !== 'voided' && (
        <Card className="mt-6 border-destructive/30 bg-destructive/5">
          <CardContent className="py-4">
            <div className="flex items-start justify-between">
              <div className="flex items-start gap-3">
                <div className="w-8 h-8 rounded-lg bg-destructive/10 flex items-center justify-center shrink-0 mt-0.5">
                  <CreditCard size={16} className="text-destructive" />
                </div>
                <div>
                  <p className="text-sm font-semibold text-destructive">Payment failed -- {formatCents(invoice.amount_due_cents, invoice.currency)} outstanding</p>
                  {invoice.last_payment_error && (
                    <p className="text-sm text-destructive/80 mt-1">{invoice.last_payment_error}</p>
                  )}
                  {invoice.stripe_payment_intent_id && (
                    <p className="text-xs text-muted-foreground mt-1 font-mono">PI: {invoice.stripe_payment_intent_id}</p>
                  )}
                </div>
              </div>
              <div className="flex items-center gap-2 ml-4 shrink-0">
                <Button
                  variant="outline" size="sm"
                  className="border-destructive/30 text-destructive"
                  onClick={async () => {
                    try {
                      const res = await api.updatePaymentMethod(invoice.customer_id)
                      window.open(res.url, '_blank')
                      toast.success('Stripe payment update page opened in new tab')
                    } catch {
                      window.location.href = `/customers/${invoice.customer_id}`
                    }
                  }}
                >
                  Update Payment Method
                </Button>
                {invoice.status === 'finalized' && invoice.amount_due_cents > 0 && (
                  <Button variant="destructive" size="sm" onClick={() => collectMutation.mutate()} disabled={collectMutation.isPending}>
                    Retry Payment
                  </Button>
                )}
              </div>
            </div>
          </CardContent>
        </Card>
      )}

      {/* Voided banner */}
      {invoice.status === 'voided' && (
        <Card className="mt-6 border-destructive/30 bg-destructive/5">
          <CardContent className="py-4 flex items-center gap-3">
            <span className="text-destructive font-bold text-lg">VOID</span>
            <div>
              <p className="text-sm font-medium text-foreground">This invoice has been voided</p>
              <p className="text-xs text-muted-foreground mt-0.5">
                {invoice.voided_at ? `Voided on ${formatDate(invoice.voided_at)}` : 'All charges and credits have been reversed'}
              </p>
            </div>
          </CardContent>
        </Card>
      )}

      {/* Payment Activity Timeline */}
      {timeline.length > 0 && invoice.status !== 'draft' && (
        <Card className={cn('mt-6', invoice.status === 'voided' && 'opacity-60')}>
          <CardHeader>
            <CardTitle className="text-sm">Payment Activity</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="relative">
              {timeline.map((event, i) => (
                <div key={i} className="flex gap-4 pb-4 last:pb-0">
                  <div className="flex flex-col items-center">
                    <div className={cn(
                      'w-2.5 h-2.5 rounded-full mt-1.5',
                      event.status === 'succeeded' || event.status === 'resolved' ? 'bg-emerald-500' :
                      event.status === 'failed' || event.status === 'canceled' ? 'bg-destructive' :
                      event.status === 'processing' || event.status === 'scheduled' ? 'bg-blue-500' :
                      event.status === 'escalated' ? 'bg-violet-500' :
                      'bg-amber-500'
                    )} />
                    {i < timeline.length - 1 && <div className="w-px flex-1 bg-border mt-1" />}
                  </div>
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center justify-between">
                      <p className="text-sm text-foreground">{event.description}</p>
                      <span className="text-xs text-muted-foreground ml-4 whitespace-nowrap">{formatDateTime(event.timestamp)}</span>
                    </div>
                    {event.error && event.status === 'failed' && (
                      <p className="text-xs text-destructive mt-0.5">{event.error}</p>
                    )}
                    {event.amount_cents != null && event.amount_cents > 0 && (
                      <p className="text-xs text-muted-foreground mt-0.5">{formatCents(event.amount_cents, invoice.currency)}</p>
                    )}
                  </div>
                </div>
              ))}
            </div>
          </CardContent>
        </Card>
      )}

      {/* Void Confirm */}
      <TypedConfirmDialog
        open={showVoidConfirm}
        onOpenChange={setShowVoidConfirm}
        title="Void Invoice"
        description="Voiding reverses this invoice and marks it uncollectible. This action cannot be undone."
        confirmWord="VOID"
        confirmLabel="Void Invoice"
        onConfirm={() => voidMutation.mutate()}
        loading={voidMutation.isPending}
      />

      {/* Rotate public-token Confirm — defensive affordance for when the
          public URL has been shared where it shouldn't be (wider email
          thread, ticketing-system paste, archive leak). Not destructive
          to the invoice itself, but irreversible for the old URL. */}
      <TypedConfirmDialog
        open={showRotatePublicTokenConfirm}
        onOpenChange={setShowRotatePublicTokenConfirm}
        title="Rotate public link"
        description="This invalidates the current public URL for this invoice and mints a new one. Any email, message, or page still linking to the old URL will stop working. Use this if the current link was shared somewhere it shouldn't have been."
        confirmWord="ROTATE"
        confirmLabel="Rotate Link"
        onConfirm={() => rotatePublicTokenMutation.mutate()}
        loading={rotatePublicTokenMutation.isPending}
      />

      {/* Email Modal */}
      {showEmailModal && (
        <EmailInvoiceDialog
          invoiceId={invoice.id}
          defaultEmail={customer?.email || ''}
          onClose={() => setShowEmailModal(false)}
          onSent={() => { setShowEmailModal(false); toast.success('Invoice email sent') }}
        />
      )}

      {/* Credit Modal */}
      {showCreditModal && (
        <IssueCreditDialog
          invoice={invoice}
          onClose={() => setShowCreditModal(false)}
          onCreated={() => { setShowCreditModal(false); toast.success('Credit note issued'); invalidateAll() }}
        />
      )}

      {/* Add Line Item Modal */}
      {showAddLineItem && (
        <AddLineItemDialog
          invoiceId={invoice.id}
          onClose={() => setShowAddLineItem(false)}
          onAdded={() => { setShowAddLineItem(false); toast.success('Line item added'); invalidateAll() }}
        />
      )}

      {/* Apply Coupon Modal */}
      {showApplyCoupon && (
        <ApplyCouponDialog
          invoiceId={invoice.id}
          invoiceNumber={invoice.invoice_number}
          subtotalCents={invoice.subtotal_cents}
          currency={invoice.currency}
          onClose={() => setShowApplyCoupon(false)}
          onApplied={() => { setShowApplyCoupon(false); toast.success('Coupon applied'); invalidateAll() }}
        />
      )}

      {/* PDF Preview */}
      {pdfPreviewUrl && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 backdrop-blur-sm" onClick={() => { URL.revokeObjectURL(pdfPreviewUrl); setPdfPreviewUrl(null) }}>
          <div className="relative w-full max-w-4xl h-[85vh] bg-background rounded-2xl shadow-2xl overflow-hidden" onClick={e => e.stopPropagation()}>
            <div className="flex items-center justify-between px-6 py-3 border-b border-border">
              <h2 className="text-sm font-semibold text-foreground">Invoice Preview -- {invoice.invoice_number}</h2>
              <Button variant="ghost" size="sm" onClick={() => { URL.revokeObjectURL(pdfPreviewUrl); setPdfPreviewUrl(null) }}>
                Close
              </Button>
            </div>
            <iframe src={pdfPreviewUrl} className="w-full h-full" title="Invoice PDF Preview" />
          </div>
        </div>
      )}
    </Layout>
  )
}

function AddLineItemDialog({ invoiceId, onClose, onAdded }: {
  invoiceId: string; onClose: () => void; onAdded: () => void
}) {
  const [description, setDescription] = useState('')
  const [lineType, setLineType] = useState('add_on')
  const [quantity, setQuantity] = useState('1')
  const [unitAmount, setUnitAmount] = useState('')
  const [saving, setSaving] = useState(false)
  const [descriptionError, setDescriptionError] = useState('')
  const [amountError, setAmountError] = useState('')

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setDescriptionError('')
    setAmountError('')
    if (!description.trim()) { setDescriptionError('Description is required'); return }
    if (!unitAmount || parseFloat(unitAmount) <= 0) { setAmountError('Unit amount must be greater than 0'); return }
    setSaving(true)
    try {
      await api.addInvoiceLineItem(invoiceId, {
        description: description.trim(),
        line_type: lineType,
        quantity: parseInt(quantity) || 1,
        unit_amount_cents: Math.round(parseFloat(unitAmount) * 100),
      })
      onAdded()
    } catch (err) {
      showApiError(err, 'Failed to add line item')
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose() }}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Add Line Item</DialogTitle>
        </DialogHeader>
        <form onSubmit={handleSubmit} noValidate className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="description">Description</Label>
            <Input id="description" value={description} onChange={e => { setDescription(e.target.value); setDescriptionError('') }} placeholder="e.g. Setup fee, Consulting hours" maxLength={500} />
            {descriptionError && <p className="text-xs text-destructive">{descriptionError}</p>}
          </div>
          <div className="space-y-2">
            <Label>Type</Label>
            <Select value={lineType} onValueChange={(v) => setLineType(v ?? '')}>
              <SelectTrigger className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="add_on">Add-On</SelectItem>
                <SelectItem value="base_fee">Base Fee</SelectItem>
                <SelectItem value="usage">Usage</SelectItem>
                <SelectItem value="discount">Discount</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-2">
              <Label htmlFor="quantity">Quantity</Label>
              <Input id="quantity" type="number" min={1} value={quantity} onChange={e => setQuantity(e.target.value)} />
            </div>
            <div className="space-y-2">
              <Label htmlFor="unitAmount">Unit Price ({getCurrencySymbol()})</Label>
              <Input id="unitAmount" type="number" step="0.01" min={0.01} value={unitAmount} onChange={e => { setUnitAmount(e.target.value); setAmountError('') }} placeholder="10.00" />
              {amountError && <p className="text-xs text-destructive">{amountError}</p>}
            </div>
          </div>
          {unitAmount && quantity && (
            <p className="text-sm text-muted-foreground">
              Total: {getCurrencySymbol()}{((parseInt(quantity) || 1) * parseFloat(unitAmount || '0')).toFixed(2)}
            </p>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
            <Button type="submit" disabled={saving}>
              {saving ? <><Loader2 size={14} className="animate-spin mr-2" />Adding...</> : 'Add Line Item'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function EmailInvoiceDialog({ invoiceId, defaultEmail, onClose, onSent }: {
  invoiceId: string; defaultEmail: string; onClose: () => void; onSent: () => void
}) {
  const form = useForm<EmailFormData>({
    resolver: zodResolver(emailSchema),
    defaultValues: { email: defaultEmail },
  })
  const { register, handleSubmit, formState: { errors, isSubmitting } } = form

  const onSubmit = handleSubmit(async (data) => {
    try {
      await api.sendInvoiceEmail(invoiceId, data.email)
      onSent()
    } catch (err) {
      applyApiError(form, err, ['email'], { toastTitle: 'Failed to send email' })
    }
  })

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose() }}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Email Invoice</DialogTitle>
        </DialogHeader>
        <form onSubmit={onSubmit} noValidate className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="email">Recipient Email</Label>
            <Input id="email" type="email" placeholder="customer@example.com" maxLength={254} {...register('email')} />
            {errors.email && <p className="text-xs text-destructive">{errors.email.message}</p>}
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
            <Button type="submit" disabled={isSubmitting}>
              {isSubmitting ? <><Loader2 size={14} className="animate-spin mr-2" />Sending...</> : 'Send Email'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function IssueCreditDialog({ invoice, onClose, onCreated }: {
  invoice: Invoice; onClose: () => void; onCreated: () => void
}) {
  const [type, setType] = useState<string>('credit')

  const form = useForm<CreditModalData>({
    resolver: zodResolver(creditModalSchema),
    defaultValues: { reason: '', amount: '' },
  })
  const { register, handleSubmit, formState: { errors, isSubmitting } } = form

  const onSubmit = handleSubmit(async (data) => {
    try {
      const amountCents = Math.round(parseFloat(data.amount) * 100)
      await api.createCreditNote({
        invoice_id: invoice.id,
        reason: data.reason,
        refund_type: type,
        auto_issue: true,
        lines: [{ description: data.reason, quantity: 1, unit_amount_cents: amountCents }],
      })
      onCreated()
    } catch (err) {
      applyApiError(form, err, {
        reason: 'reason',
        lines: 'amount',
        unit_amount_cents: 'amount',
      }, { toastTitle: 'Failed to create credit note' })
    }
  })

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose() }}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Issue Credit / Refund</DialogTitle>
        </DialogHeader>
        <form onSubmit={onSubmit} noValidate className="space-y-4">
          <div>
            <Label>Invoice</Label>
            <p className="text-sm text-muted-foreground font-mono mt-1">{invoice.invoice_number}</p>
          </div>
          <div className="space-y-2">
            <Label htmlFor="reason">Reason</Label>
            <Input id="reason" placeholder="e.g. Service disruption, billing error" maxLength={500} {...register('reason')} />
            {errors.reason && <p className="text-xs text-destructive">{errors.reason.message}</p>}
          </div>
          <div className="space-y-2">
            <Label htmlFor="amount">Amount ({getCurrencySymbol()})</Label>
            <Input id="amount" type="number" step="0.01" min={0.01} max={999999.99} {...register('amount')} />
            {errors.amount && <p className="text-xs text-destructive">{errors.amount.message}</p>}
          </div>
          <div className="space-y-2">
            <Label>Type</Label>
            <Select value={type} onValueChange={(v) => setType(v ?? '')}>
              <SelectTrigger className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="credit">Credit</SelectItem>
                {invoice.status === 'paid' && (
                  <SelectItem value="refund">Refund -- return to payment method</SelectItem>
                )}
              </SelectContent>
            </Select>
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
            <Button type="submit" disabled={isSubmitting}>
              {isSubmitting ? <><Loader2 size={14} className="animate-spin mr-2" />Creating...</> : 'Create Credit Note'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

// ApplyCouponDialog prompts for a coupon code and applies it to a draft
// invoice. A fresh idempotency key is generated per open so the operator's
// retries after a transient failure don't double-redeem; a fresh key on
// every open deliberately lets the operator re-try a different code if the
// first one was rejected.
function ApplyCouponDialog({ invoiceId, invoiceNumber, subtotalCents, currency, onClose, onApplied }: {
  invoiceId: string
  invoiceNumber: string
  subtotalCents: number
  currency: string
  onClose: () => void
  onApplied: () => void
}) {
  const [code, setCode] = useState('')
  const [codeError, setCodeError] = useState('')
  const [saving, setSaving] = useState(false)
  const [idemKey] = useState(() => crypto.randomUUID())

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    const trimmed = code.trim().toUpperCase()
    if (!trimmed) { setCodeError('Coupon code is required'); return }
    setSaving(true)
    setCodeError('')
    try {
      await api.applyInvoiceCoupon(invoiceId, { code: trimmed, idempotency_key: idemKey })
      onApplied()
    } catch (err) {
      setCodeError(err instanceof Error ? err.message : 'Failed to apply coupon')
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose() }}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Apply Coupon</DialogTitle>
        </DialogHeader>
        <form onSubmit={handleSubmit} noValidate className="space-y-4">
          <div>
            <Label>Invoice</Label>
            <p className="text-sm text-muted-foreground font-mono mt-1">{invoiceNumber}</p>
          </div>
          <div>
            <Label>Subtotal</Label>
            <p className="text-sm text-muted-foreground mt-1">
              {formatCents(subtotalCents, currency)}
            </p>
          </div>
          <div className="space-y-2">
            <Label htmlFor="coupon-code">Coupon Code</Label>
            <Input
              id="coupon-code"
              value={code}
              onChange={e => { setCode(e.target.value); setCodeError('') }}
              placeholder="e.g. SAVE20"
              autoComplete="off"
              autoCapitalize="characters"
              maxLength={100}
              autoFocus
            />
            {codeError && <p className="text-xs text-destructive">{codeError}</p>}
            <p className="text-xs text-muted-foreground">
              Discount and tax recompute atomically against the new subtotal.
            </p>
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
            <Button type="submit" disabled={saving}>
              {saving ? <><Loader2 size={14} className="animate-spin mr-2" />Applying...</> : 'Apply Coupon'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
