import { useState, useEffect } from 'react'
import { useParams, Link } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { toast } from 'sonner'
import { api, downloadPDF, formatCents, formatDate, formatDateTime, getCurrencySymbol, type Invoice, type LineItem, type Customer, type Subscription, type CreditNote, type TimelineEvent } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { cn } from '@/lib/utils'

import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Separator } from '@/components/ui/separator'
import {
  Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle,
} from '@/components/ui/dialog'
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from '@/components/ui/table'
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from '@/components/ui/select'

import { ArrowLeft, Copy, Check, Loader2, Mail, CreditCard } from 'lucide-react'

function CopyId({ text }: { text: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <button
      onClick={() => { navigator.clipboard.writeText(text); setCopied(true); setTimeout(() => setCopied(false), 1500) }}
      className="inline-flex items-center gap-1 text-muted-foreground hover:text-foreground transition-colors"
    >
      {copied ? <Check size={12} /> : <Copy size={12} />}
    </button>
  )
}

function statusVariant(status: string): 'default' | 'secondary' | 'destructive' | 'outline' {
  switch (status) {
    case 'paid': case 'succeeded': case 'active': return 'default'
    case 'voided': case 'failed': case 'canceled': return 'destructive'
    case 'draft': case 'pending': return 'outline'
    case 'finalized': return 'secondary'
    default: return 'outline'
  }
}

const LINE_TYPE_LABELS: Record<string, string> = {
  base_fee: 'Base Fee',
  usage: 'Usage',
  add_on: 'Add-On',
  discount: 'Discount',
  tax: 'Tax',
}

function formatLineType(raw: string): string {
  return LINE_TYPE_LABELS[raw] || raw.replace(/_/g, ' ').replace(/\b\w/g, c => c.toUpperCase())
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
  const [showEmailModal, setShowEmailModal] = useState(false)
  const [showCreditModal, setShowCreditModal] = useState(false)
  const [showAddLineItem, setShowAddLineItem] = useState(false)
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

  const invalidateAll = () => {
    queryClient.invalidateQueries({ queryKey: ['invoice', id] })
    queryClient.invalidateQueries({ queryKey: ['invoice-credit-notes', id] })
    queryClient.invalidateQueries({ queryKey: ['invoice-timeline', id] })
  }

  const finalizeMutation = useMutation({
    mutationFn: () => api.finalizeInvoice(id!),
    onSuccess: () => { invalidateAll(); toast.success(`Invoice ${invoice?.invoice_number} finalized`) },
    onError: (err) => toast.error(err instanceof Error ? err.message : 'Failed to finalize'),
  })

  const voidMutation = useMutation({
    mutationFn: () => api.voidInvoice(id!),
    onSuccess: () => { invalidateAll(); setShowVoidConfirm(false); toast.success(`Invoice ${invoice?.invoice_number} voided`) },
    onError: (err) => toast.error(err instanceof Error ? err.message : 'Failed to void'),
  })

  const collectMutation = useMutation({
    mutationFn: () => api.collectPayment(id!),
    onSuccess: () => { invalidateAll(); toast.success('Payment initiated') },
    onError: (err) => toast.error(err instanceof Error ? err.message : 'Payment failed'),
  })

  const acting = finalizeMutation.isPending || voidMutation.isPending || collectMutation.isPending

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
      {/* Breadcrumb */}
      <div className="flex items-center gap-2 text-sm text-muted-foreground mb-4">
        <Link to="/invoices" className="hover:text-foreground transition-colors flex items-center gap-1">
          <ArrowLeft size={14} />
          Invoices
        </Link>
        <span>/</span>
        <span className="text-foreground">{invoice.invoice_number}</span>
      </div>

      {/* Header */}
      <div className="flex items-center justify-between mb-6">
        <div>
          <h1 className="text-2xl font-semibold text-foreground">{invoice.invoice_number}</h1>
          <div className="flex items-center gap-1.5 mt-1">
            <span className="text-xs font-mono text-muted-foreground">{invoice.id}</span>
            <CopyId text={invoice.id} />
          </div>
        </div>
        <div className="flex items-center gap-2">
          {invoice.status === 'draft' && (
            <>
              <Button variant="outline" size="sm" onClick={() => setShowAddLineItem(true)} disabled={acting}>
                Add Line Item
              </Button>
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
                  headers: { 'Authorization': `Bearer ${localStorage.getItem('velox_api_key') || ''}` },
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

      {/* Status banner */}
      <Card className={cn(
        'mb-4',
        invoice.status === 'paid' ? 'border-emerald-200 bg-emerald-50 dark:bg-emerald-950/20' :
        invoice.status === 'voided' ? 'border-destructive/30 bg-destructive/5' :
        invoice.status === 'draft' ? '' :
        invoice.payment_status === 'failed' ? 'border-destructive/30 bg-destructive/5' :
        'border-blue-200 bg-blue-50 dark:bg-blue-950/20'
      )}>
        <CardContent className="py-4 flex items-center justify-between">
          <div className="flex items-center gap-3">
            <Badge variant={statusVariant(invoice.status)}>{invoice.status}</Badge>
            {invoice.status === 'finalized' && <Badge variant={statusVariant(invoice.payment_status)}>{invoice.payment_status}</Badge>}
            <span className="text-sm font-medium text-foreground">
              {invoice.status === 'paid' && invoice.paid_at ? `Paid on ${formatDate(invoice.paid_at)}` :
               invoice.status === 'voided' && invoice.voided_at ? `Voided on ${formatDate(invoice.voided_at)}` :
               invoice.status === 'draft' ? 'Draft -- not yet finalized' :
               invoice.payment_status === 'failed' ? `Payment failed -- ${formatCents(invoice.amount_due_cents, invoice.currency)} outstanding` :
               invoice.amount_due_cents > 0 ? `Due on ${invoice.due_at ? formatDate(invoice.due_at) : 'N/A'}` :
               'Finalized'}
            </span>
          </div>
          <span className="text-2xl font-semibold text-foreground tabular-nums">{formatCents(invoice.amount_due_cents, invoice.currency)}</span>
        </CardContent>
      </Card>

      {/* Key metrics */}
      <Card>
        <CardContent className="p-0">
          <div className="flex divide-x divide-border">
            <div className="flex-1 px-6 py-4">
              <p className="text-sm text-muted-foreground">Subtotal</p>
              <p className="text-lg font-semibold text-foreground mt-0.5 tabular-nums">{formatCents(invoice.subtotal_cents, invoice.currency)}</p>
            </div>
            <div className="flex-1 px-6 py-4">
              <p className="text-sm text-muted-foreground">Total</p>
              <p className="text-lg font-semibold text-foreground mt-0.5 tabular-nums">{formatCents(invoice.total_amount_cents, invoice.currency)}</p>
            </div>
            <div className="flex-1 px-6 py-4">
              <p className="text-sm text-muted-foreground">Amount Due</p>
              <p className="text-lg font-semibold text-foreground mt-0.5 tabular-nums">{formatCents(invoice.amount_due_cents, invoice.currency)}</p>
            </div>
            <div className="flex-1 px-6 py-4">
              <p className="text-sm text-muted-foreground">Due Date</p>
              <p className="text-lg font-semibold text-foreground mt-0.5">{invoice.due_at ? formatDate(invoice.due_at) : '\u2014'}</p>
            </div>
          </div>
        </CardContent>
      </Card>

      {/* Properties */}
      <Card className="mt-6">
        <CardHeader>
          <CardTitle className="text-sm">Properties</CardTitle>
        </CardHeader>
        <CardContent className="p-0">
          <div className="divide-y divide-border">
            <div className="flex items-center justify-between px-6 py-3">
              <span className="text-sm text-muted-foreground">Invoice Number</span>
              <span className="text-sm font-medium text-foreground">{invoice.invoice_number}</span>
            </div>
            <div className="flex items-center justify-between px-6 py-3">
              <span className="text-sm text-muted-foreground">Customer</span>
              <span className="text-sm font-medium text-foreground">
                {customer ? (
                  <Link to={`/customers/${customer.id}`} className="text-primary hover:underline">
                    {customer.display_name}
                  </Link>
                ) : (
                  invoice.customer_id
                )}
              </span>
            </div>
            {subscription && (
              <div className="flex items-center justify-between px-6 py-3">
                <span className="text-sm text-muted-foreground">Subscription</span>
                <Link to={`/subscriptions/${subscription.id}`} className="text-sm font-medium text-primary hover:underline">{subscription.display_name}</Link>
              </div>
            )}
            <div className="flex items-center justify-between px-6 py-3">
              <span className="text-sm text-muted-foreground">Billing Period</span>
              <span className="text-sm font-medium text-foreground">
                {formatDate(invoice.billing_period_start)} -- {formatDate(invoice.billing_period_end)}
              </span>
            </div>
            <div className="flex items-center justify-between px-6 py-3">
              <span className="text-sm text-muted-foreground">Status</span>
              <Badge variant={statusVariant(invoice.status)}>{invoice.status}</Badge>
            </div>
            <div className="flex items-center justify-between px-6 py-3">
              <span className="text-sm text-muted-foreground">Payment Status</span>
              <Badge variant={statusVariant(invoice.payment_status)}>{invoice.payment_status}</Badge>
            </div>
            <div className="flex items-center justify-between px-6 py-3">
              <span className="text-sm text-muted-foreground">Currency</span>
              <span className="text-sm font-medium text-foreground uppercase">{invoice.currency}</span>
            </div>
            <div className="flex items-center justify-between px-6 py-3">
              <span className="text-sm text-muted-foreground">Created</span>
              <span className="text-sm font-medium text-foreground">{formatDateTime(invoice.created_at)}</span>
            </div>
            <div className="flex items-center justify-between px-6 py-3">
              <span className="text-sm text-muted-foreground">ID</span>
              <div className="flex items-center gap-1.5">
                <span className="text-sm font-mono text-muted-foreground">{invoice.id}</span>
                <CopyId text={invoice.id} />
              </div>
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

      {/* Line Items */}
      <Card className="mt-6">
        <CardHeader>
          <CardTitle className="text-sm">Line Items</CardTitle>
        </CardHeader>
        <CardContent className="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Description</TableHead>
                <TableHead>Type</TableHead>
                <TableHead className="text-right">Qty</TableHead>
                <TableHead className="text-right">Unit Price</TableHead>
                <TableHead className="text-right">Amount</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {lineItems.map(item => (
                <TableRow key={item.id} className={cn(invoice.status === 'voided' && 'opacity-50')}>
                  <TableCell>{item.description}</TableCell>
                  <TableCell><Badge variant="outline">{formatLineType(item.line_type)}</Badge></TableCell>
                  <TableCell className="text-right">{item.quantity}</TableCell>
                  <TableCell className="text-right">{formatCents(item.unit_amount_cents, invoice.currency)}</TableCell>
                  <TableCell className="text-right font-medium">{formatCents(item.amount_cents, invoice.currency)}</TableCell>
                </TableRow>
              ))}
            </TableBody>
            <tfoot>
              {/* Subtotal */}
              <TableRow className="border-t-2">
                <TableCell colSpan={4} className={cn('text-right', invoice.status === 'voided' ? 'text-muted-foreground' : '')}>Subtotal</TableCell>
                <TableCell className={cn('text-right', invoice.status === 'voided' ? 'text-muted-foreground' : '')}>{formatCents(invoice.subtotal_cents, invoice.currency)}</TableCell>
              </TableRow>

              {/* Discount */}
              {invoice.discount_cents > 0 && (
                <TableRow>
                  <TableCell colSpan={4} className={cn('text-right', invoice.status === 'voided' ? 'text-muted-foreground' : '')}>Discount</TableCell>
                  <TableCell className={cn('text-right', invoice.status === 'voided' ? 'text-muted-foreground' : '')}>-{formatCents(invoice.discount_cents, invoice.currency)}</TableCell>
                </TableRow>
              )}

              {/* Tax */}
              {invoice.tax_amount_cents > 0 && (
                <TableRow>
                  <TableCell colSpan={4} className={cn('text-right', invoice.status === 'voided' ? 'text-muted-foreground' : '')}>
                    {invoice.tax_name || 'Tax'}{invoice.tax_rate > 0 ? ` (${invoice.tax_rate}%)` : ''}
                  </TableCell>
                  <TableCell className={cn('text-right', invoice.status === 'voided' ? 'text-muted-foreground' : '')}>{formatCents(invoice.tax_amount_cents, invoice.currency)}</TableCell>
                </TableRow>
              )}

              {/* Total */}
              <TableRow>
                <TableCell colSpan={4} className={cn('text-right font-medium', invoice.status === 'voided' ? 'text-muted-foreground' : '')}>Total</TableCell>
                <TableCell className={cn('text-right font-medium', invoice.status === 'voided' ? 'text-muted-foreground' : '')}>{formatCents(invoice.total_amount_cents, invoice.currency)}</TableCell>
              </TableRow>

              {/* Settlement waterfall */}
              {invoice.status === 'voided' ? (
                <TableRow className="border-t-2">
                  <TableCell colSpan={4} className="text-right font-semibold">Amount Due</TableCell>
                  <TableCell className="text-right font-semibold">$0.00</TableCell>
                </TableRow>
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
                      <TableRow key={cn.id}>
                        <TableCell colSpan={4} className="text-right text-emerald-600">
                          Credit note {cn.credit_note_number} -- {cn.reason}
                        </TableCell>
                        <TableCell className="text-right text-emerald-600">-{formatCents(cn.total_cents, invoice.currency)}</TableCell>
                      </TableRow>
                    ))}

                    {invoice.credits_applied_cents > 0 && (
                      <TableRow>
                        <TableCell colSpan={4} className="text-right text-emerald-600">Prepaid credits applied</TableCell>
                        <TableCell className="text-right text-emerald-600">-{formatCents(invoice.credits_applied_cents, invoice.currency)}</TableCell>
                      </TableRow>
                    )}

                    {invoice.amount_paid_cents > 0 && (
                      <TableRow>
                        <TableCell colSpan={4} className="text-right text-muted-foreground">Amount Paid</TableCell>
                        <TableCell className="text-right">-{formatCents(invoice.amount_paid_cents, invoice.currency)}</TableCell>
                      </TableRow>
                    )}

                    <TableRow className="border-t-2">
                      <TableCell colSpan={4} className="text-right font-semibold">Amount Due</TableCell>
                      <TableCell className="text-right font-semibold">{formatCents(invoice.amount_due_cents, invoice.currency)}</TableCell>
                    </TableRow>

                    {/* Post-payment adjustments */}
                    {(() => {
                      const completedCNs = postPaymentCNs.filter(cn =>
                        cn.credit_amount_cents > 0 ||
                        (cn.refund_amount_cents > 0 && cn.refund_status === 'succeeded')
                      )
                      return completedCNs.length > 0 ? (
                        <>
                          <TableRow>
                            <TableCell colSpan={5} className="pt-4 pb-2">
                              <span className="text-xs font-medium text-muted-foreground uppercase tracking-wider">Post-payment adjustments</span>
                            </TableCell>
                          </TableRow>
                          {completedCNs.map(cn => (
                            <TableRow key={cn.id}>
                              <TableCell colSpan={4} className="text-right text-muted-foreground">
                                {cn.credit_note_number} -- {cn.reason}
                                <span className="ml-2 text-xs">
                                  {cn.refund_amount_cents > 0 ? '(refunded)' : '(credited to balance)'}
                                </span>
                              </TableCell>
                              <TableCell className="text-right text-muted-foreground">{formatCents(cn.total_cents, invoice.currency)}</TableCell>
                            </TableRow>
                          ))}
                        </>
                      ) : null
                    })()}
                  </>
                )
              })()}
            </tfoot>
          </Table>
        </CardContent>
      </Card>

      {/* Void Confirm */}
      <AlertDialog open={showVoidConfirm} onOpenChange={setShowVoidConfirm}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Void Invoice</AlertDialogTitle>
            <AlertDialogDescription>
              Are you sure you want to void this invoice? This action cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction variant="destructive" onClick={() => voidMutation.mutate()} disabled={voidMutation.isPending}>
              Void Invoice
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

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
  const [error, setError] = useState('')

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!description.trim()) { setError('Description is required'); return }
    if (!unitAmount || parseFloat(unitAmount) <= 0) { setError('Unit amount must be greater than 0'); return }
    setSaving(true); setError('')
    try {
      await api.addInvoiceLineItem(invoiceId, {
        description: description.trim(),
        line_type: lineType,
        quantity: parseInt(quantity) || 1,
        unit_amount_cents: Math.round(parseFloat(unitAmount) * 100),
      })
      onAdded()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to add line item')
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
            <Input id="description" value={description} onChange={e => setDescription(e.target.value)} placeholder="e.g. Setup fee, Consulting hours" maxLength={500} />
          </div>
          <div className="space-y-2">
            <Label>Type</Label>
            <Select value={lineType} onValueChange={setLineType}>
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
              <Input id="unitAmount" type="number" step="0.01" min={0.01} value={unitAmount} onChange={e => setUnitAmount(e.target.value)} placeholder="10.00" />
            </div>
          </div>
          {unitAmount && quantity && (
            <p className="text-sm text-muted-foreground">
              Total: {getCurrencySymbol()}{((parseInt(quantity) || 1) * parseFloat(unitAmount || '0')).toFixed(2)}
            </p>
          )}
          {error && (
            <div className="px-3 py-2 rounded-md bg-destructive/10 border border-destructive/20">
              <p className="text-destructive text-sm">{error}</p>
            </div>
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
  const { register, handleSubmit, formState: { errors, isSubmitting } } = useForm<EmailFormData>({
    resolver: zodResolver(emailSchema),
    defaultValues: { email: defaultEmail },
  })

  const onSubmit = handleSubmit(async (data) => {
    try {
      await api.sendInvoiceEmail(invoiceId, data.email)
      onSent()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to send email')
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

  const { register, handleSubmit, formState: { errors, isSubmitting } } = useForm<CreditModalData>({
    resolver: zodResolver(creditModalSchema),
    defaultValues: { reason: '', amount: '' },
  })

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
      toast.error(err instanceof Error ? err.message : 'Failed to create credit note')
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
            <Select value={type} onValueChange={setType}>
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
