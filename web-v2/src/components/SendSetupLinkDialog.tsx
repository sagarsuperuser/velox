import { useState } from 'react'
import { toast } from 'sonner'
import { api } from '@/lib/api'
import { showApiError } from '@/lib/formErrors'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Separator } from '@/components/ui/separator'
import {
  Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle,
} from '@/components/ui/dialog'

// SendSetupLinkDialog is the shared "Add a payment method" dialog used
// by CustomerDetail (operator-initiated, no invoice context) and
// InvoiceAttention (operator-initiated, invoice context — the
// customer's invoice is in attention and we want to nudge them).
//
// Two paths (matches Stripe / Chargebee convergence):
//   - PRIMARY: Send email — server mints session + ships branded
//     email atomically. URL never leaves the backend on this path.
//   - SECONDARY: Copy link — server mints session on demand; URL
//     auto-copies + stays visible in the dialog. For Slack / SMS /
//     custom channels.
//
// The customer's email is pre-filled from the customer record.
// When absent, Send Email is disabled with a tooltip hint; Copy
// Link stays usable.
//
// PCI: card capture happens browser → Stripe-hosted iframe →
// Stripe. The dashboard never sees PAN. Setup-session URL is
// single-use, ~24h TTL per Stripe's session policy.

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  customerId: string
  customerEmail: string | undefined
  // Optional context when the dialog is opened from an invoice-attention
  // banner. Prefills the operator note with a sensible default and
  // (server-side, after the unified-template work) embeds invoice
  // facts into the email so the customer reads "for invoice X, $Y."
  invoiceContext?: { invoiceNumber: string; amountDueLabel: string }
}

export function SendSetupLinkDialog({ open, onOpenChange, customerId, customerEmail, invoiceContext }: Props) {
  const [note, setNote] = useState(invoiceContext
    ? `We couldn't process payment for invoice ${invoiceContext.invoiceNumber} (${invoiceContext.amountDueLabel}). Please add a payment method using the secure link below.`
    : '')
  const [loading, setLoading] = useState<'send' | 'copy' | null>(null)
  const [setupLinkUrl, setSetupLinkUrl] = useState('')

  const close = () => {
    onOpenChange(false)
    setSetupLinkUrl('')
    setNote(invoiceContext
      ? `We couldn't process payment for invoice ${invoiceContext.invoiceNumber} (${invoiceContext.amountDueLabel}). Please add a payment method using the secure link below.`
      : '')
  }

  const handleSendEmail = async () => {
    setLoading('send')
    try {
      const res = await api.sendCustomerSetupEmail(customerId, note.trim() || undefined)
      toast.success(`Setup link sent to ${res.to}`)
      close()
    } catch (err) {
      showApiError(err)
    } finally {
      setLoading(null)
    }
  }

  const handleMintCopyLink = async () => {
    setLoading('copy')
    try {
      const res = await api.createCustomerSetupSession(customerId)
      setSetupLinkUrl(res.url)
      try {
        await navigator.clipboard.writeText(res.url)
        toast.success('Setup link copied')
      } catch {
        // Clipboard API unavailable — URL stays visible for manual copy.
      }
    } catch (err) {
      showApiError(err)
    } finally {
      setLoading(null)
    }
  }

  const copyExisting = async () => {
    try {
      await navigator.clipboard.writeText(setupLinkUrl)
      toast.success('Setup link copied')
    } catch {
      toast.error('Copy failed — select the link manually')
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => { if (!o) close() }}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Add a payment method</DialogTitle>
        </DialogHeader>
        <div className="space-y-4">
          <p className="text-sm text-muted-foreground">
            The customer enters their card details on Stripe's hosted form. No card data touches this dashboard.
          </p>

          <div className="space-y-2">
            <Label className="text-xs">Recipient</Label>
            <Input
              value={customerEmail || ''}
              readOnly
              className="text-sm"
              placeholder="No email on file"
            />
            <Label className="text-xs pt-1">Optional note</Label>
            <textarea
              value={note}
              onChange={(e) => setNote(e.target.value)}
              placeholder="e.g. Your card on file expired last week — here's a link to update it."
              maxLength={2000}
              rows={3}
              className="w-full text-sm px-3 py-2 border border-input rounded-md bg-background resize-none"
            />
            <Button
              className="w-full"
              onClick={() => void handleSendEmail()}
              disabled={loading === 'send' || !customerEmail}
              title={!customerEmail ? 'Add an email on the customer record first' : undefined}
            >
              {loading === 'send' ? 'Sending…' : 'Send email'}
            </Button>
          </div>

          <div className="relative">
            <Separator />
            <span className="absolute left-1/2 -translate-x-1/2 -top-2.5 px-2 bg-background text-xs text-muted-foreground">
              or
            </span>
          </div>

          <div className="space-y-2">
            <p className="text-xs text-muted-foreground">
              Share via Slack, SMS, or another channel.
            </p>
            {setupLinkUrl ? (
              <div className="flex gap-2">
                <Input
                  value={setupLinkUrl}
                  readOnly
                  onClick={(e) => (e.target as HTMLInputElement).select()}
                  className="font-mono text-xs"
                />
                <Button size="sm" variant="outline" onClick={copyExisting}>Copy</Button>
              </div>
            ) : (
              <Button
                className="w-full"
                variant="outline"
                onClick={() => void handleMintCopyLink()}
                disabled={loading === 'copy'}
              >
                {loading === 'copy' ? 'Generating…' : 'Copy link'}
              </Button>
            )}
            <p className="text-xs text-muted-foreground">
              Link is single-use and expires in ~24 hours.
            </p>
          </div>
        </div>
        <DialogFooter>
          <Button variant="ghost" onClick={close}>Close</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
