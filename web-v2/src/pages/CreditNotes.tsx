import { useState, useMemo, useEffect } from 'react'
import { Link } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { toast } from 'sonner'
import { api, formatCents, formatDate, formatDateTime, getCurrencySymbol } from '@/lib/api'
import type { CreditNote, Invoice, Customer } from '@/lib/api'
import { downloadCSV } from '@/lib/csv'
import { Layout } from '@/components/Layout'
import { useSortable, type SortDir } from '@/hooks/useSortable'
import { useUrlState } from '@/hooks/useUrlState'
import { cn } from '@/lib/utils'
import { statusBadgeVariant, statusBorderColor } from '@/lib/status'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent } from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import {
  Pagination,
  PaginationContent,
  PaginationItem,
  PaginationLink,
  PaginationNext,
  PaginationPrevious,
} from '@/components/ui/pagination'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { TableSkeleton } from '@/components/ui/TableSkeleton'

import { Plus, Search, Download, Loader2, ArrowUpDown, ArrowUp, ArrowDown, FileMinus } from 'lucide-react'
import { EmptyState } from '@/components/EmptyState'

const creditNoteSchema = z.object({
  invoice_id: z.string().min(1, 'Invoice is required'),
  reason: z.string().min(1, 'Reason is required'),
  amount: z.string().min(1, 'Amount is required').refine(v => parseFloat(v) >= 0.01, 'Must be at least 0.01'),
  refundType: z.string(),
})

type CreditNoteFormData = z.infer<typeof creditNoteSchema>

const PAGE_SIZE = 25

function SortableHead({
  label, sortKey: key, activeSortKey, sortDir, onSort, className,
}: {
  label: string; sortKey: string; activeSortKey: string; sortDir: 'asc' | 'desc'; onSort: (key: string) => void; className?: string
}) {
  const active = key === activeSortKey
  return (
    <TableHead className={className}>
      <button
        onClick={() => onSort(key)}
        className="flex items-center gap-1.5 text-xs font-medium hover:text-foreground transition-colors"
      >
        {label}
        {active ? (
          sortDir === 'asc' ? <ArrowUp size={14} /> : <ArrowDown size={14} />
        ) : (
          <ArrowUpDown size={14} className="opacity-40" />
        )}
      </button>
    </TableHead>
  )
}

export default function CreditNotesPage() {
  const [showCreate, setShowCreate] = useState(false)
  const [confirmIssue, setConfirmIssue] = useState<string | null>(null)
  const [confirmVoid, setConfirmVoid] = useState<string | null>(null)
  const [urlState, setUrlState] = useUrlState({
    search: '',
    status: '',
    page: '1',
    sort: 'created_at',
    dir: 'desc',
  })
  const { search, status: filterStatus, sort: sortKey } = urlState
  const sortDir = urlState.dir as SortDir
  const page = Math.max(1, parseInt(urlState.page) || 1)
  const queryClient = useQueryClient()

  // Load all data
  const { data: notesData, isLoading: notesLoading, error: notesErrorObj, refetch: refetchNotes } = useQuery({
    queryKey: ['credit-notes'],
    queryFn: () => api.listCreditNotes(),
  })

  const { data: invoicesData } = useQuery({
    queryKey: ['invoices-ref'],
    queryFn: () => api.listInvoices(),
  })

  const { data: customersData } = useQuery({
    queryKey: ['customers-ref'],
    queryFn: () => api.listCustomers(),
  })

  const notes = notesData?.data ?? []
  const loading = notesLoading
  const loadError = notesErrorObj instanceof Error ? notesErrorObj.message : notesErrorObj ? String(notesErrorObj) : null

  const invoiceMap = useMemo(() => {
    const iMap: Record<string, Invoice> = {}
    ;(invoicesData?.data ?? []).forEach(inv => { iMap[inv.id] = inv })
    return iMap
  }, [invoicesData])

  const customerMap = useMemo(() => {
    const cMap: Record<string, Customer> = {}
    ;(customersData?.data ?? []).forEach(c => { cMap[c.id] = c })
    return cMap
  }, [customersData])

  const issueMutation = useMutation({
    mutationFn: (id: string) => api.issueCreditNote(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['credit-notes'] })
      toast.success('Credit note issued')
    },
    onError: (err) => toast.error(err instanceof Error ? err.message : 'Failed to issue'),
  })

  const voidMutation = useMutation({
    mutationFn: (id: string) => api.voidCreditNote(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['credit-notes'] })
      toast.success('Credit note voided')
    },
    onError: (err) => toast.error(err instanceof Error ? err.message : 'Failed to void'),
  })

  // Stats
  const stats = useMemo(() => {
    const draft = notes.filter(n => n.status === 'draft').length
    const issued = notes.filter(n => n.status === 'issued')
    const voided = notes.filter(n => n.status === 'voided').length
    const totalCredited = issued.reduce((sum, n) => sum + n.credit_amount_cents, 0)
    const totalRefunded = issued.reduce((sum, n) => sum + n.refund_amount_cents, 0)
    const totalAmount = issued.reduce((sum, n) => sum + n.total_cents, 0)
    return { draft, issued: issued.length, voided, totalCredited, totalRefunded, totalAmount }
  }, [notes])

  // Filter + search
  const filtered = useMemo(() => notes.filter(n => {
    if (filterStatus && n.status !== filterStatus) return false
    if (search) {
      const q = search.toLowerCase()
      const custName = customerMap[n.customer_id]?.display_name?.toLowerCase() || ''
      const invNum = invoiceMap[n.invoice_id]?.invoice_number?.toLowerCase() || ''
      if (!n.credit_note_number.toLowerCase().includes(q) && !custName.includes(q) && !invNum.includes(q) && !n.reason.toLowerCase().includes(q)) return false
    }
    return true
  }), [notes, filterStatus, search, customerMap, invoiceMap])

  const { sorted, onSort } = useSortable(
    filtered,
    sortKey,
    sortDir,
    (key, dir) => setUrlState({ sort: key, dir }),
  )

  const totalPages = Math.ceil(sorted.length / PAGE_SIZE)
  const currentPage = Math.min(page, totalPages || 1)
  const paginated = sorted.slice((currentPage - 1) * PAGE_SIZE, currentPage * PAGE_SIZE)

  const handleExport = () => {
    const rows = filtered.map(n => [
      n.credit_note_number,
      customerMap[n.customer_id]?.display_name || '',
      invoiceMap[n.invoice_id]?.invoice_number || '',
      n.status,
      n.refund_amount_cents > 0 ? 'Refund' : 'Credit',
      n.reason,
      (n.total_cents / 100).toFixed(2),
      n.issued_at || n.created_at,
    ])
    downloadCSV('credit-notes.csv', ['Number', 'Customer', 'Invoice', 'Status', 'Type', 'Reason', 'Amount', 'Date'], rows)
  }

  return (
    <Layout>
      {/* Header */}
      <div className="flex justify-between items-center">
        <div>
          <h1 className="text-2xl font-semibold text-foreground">Credit Notes</h1>
          <p className="text-sm text-muted-foreground mt-1">Issue refunds and adjustments on invoices{notes.length > 0 ? ` · ${notes.length} total` : ''}</p>
        </div>
        <div className="flex items-center gap-2">
          {notes.length > 0 && (
            <Button variant="outline" size="sm" onClick={handleExport}>
              <Download size={16} className="mr-2" /> Export
            </Button>
          )}
          <Button size="sm" onClick={() => setShowCreate(true)}>
            <Plus size={16} className="mr-2" /> Issue Credit Note
          </Button>
        </div>
      </div>

      {/* Summary cards */}
      {!loading && notes.length > 0 && (
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4 mt-6">
          <Card>
            <CardContent className="px-5 py-4">
              <p className="text-xs font-medium text-muted-foreground">Total Credited</p>
              <p className="text-xl font-semibold text-foreground mt-1 tabular-nums">{formatCents(stats.totalAmount)}</p>
            </CardContent>
          </Card>
          <Card>
            <CardContent className="px-5 py-4">
              <p className="text-xs font-medium text-muted-foreground">Refunded to Card</p>
              <p className="text-xl font-semibold text-primary mt-1 tabular-nums">{formatCents(stats.totalRefunded)}</p>
            </CardContent>
          </Card>
          <Card>
            <CardContent className="px-5 py-4">
              <p className="text-xs font-medium text-muted-foreground">Applied to Balance</p>
              <p className="text-xl font-semibold text-emerald-600 mt-1 tabular-nums">{formatCents(stats.totalCredited)}</p>
            </CardContent>
          </Card>
          <Card>
            <CardContent className="px-5 py-4">
              <p className="text-xs font-medium text-muted-foreground">Issued</p>
              <p className="text-xl font-semibold text-foreground mt-1">{stats.issued}</p>
              {stats.draft > 0 && <p className="text-xs text-amber-600 mt-0.5">{stats.draft} draft</p>}
            </CardContent>
          </Card>
        </div>
      )}

      {/* Filter tabs + search */}
      {(notes.length > 0 || filterStatus) && (
        <div className="flex items-center gap-3 mt-6">
          <div className="flex gap-1 bg-muted rounded-lg p-1">
            {[
              { value: '', label: 'All', count: notes.length },
              { value: 'draft', label: 'Draft', count: stats.draft },
              { value: 'issued', label: 'Issued', count: stats.issued },
              { value: 'voided', label: 'Voided', count: stats.voided },
            ].map(f => (
              <button
                key={f.value}
                onClick={() => setUrlState({ status: f.value, page: '1' })}
                className={cn(
                  'px-3 py-1.5 rounded-md text-xs font-medium transition-colors',
                  filterStatus === f.value
                    ? 'bg-background text-foreground shadow-sm'
                    : 'text-muted-foreground hover:text-foreground'
                )}
              >
                {f.label}
                {f.count > 0 && <span className="ml-1 text-muted-foreground/60">{f.count}</span>}
              </button>
            ))}
          </div>
          <div className="relative flex-1">
            <Search size={16} className="absolute left-3 top-2.5 text-muted-foreground" />
            <Input
              value={search}
              onChange={e => setUrlState({ search: e.target.value, page: '1' })}
              placeholder="Search by number, customer, invoice, or reason..."
              className="pl-9"
            />
          </div>
        </div>
      )}

      {/* Table */}
      <Card className="mt-4">
        <CardContent className="p-0">
          {loadError ? (
            <div className="p-8 text-center">
              <p className="text-sm text-destructive mb-3">{loadError}</p>
              <Button variant="outline" size="sm" onClick={() => refetchNotes()}>
                Retry
              </Button>
            </div>
          ) : loading ? (
            <TableSkeleton columns={7} />
          ) : notes.length === 0 ? (
            filterStatus ? (
              <EmptyState
                title={`No ${filterStatus} credit notes`}
                description="Try a different filter to see more results."
                action={{
                  label: 'Clear filter',
                  variant: 'outline',
                  onClick: () => setUrlState({ status: '', page: '1' }),
                }}
              />
            ) : (
              <EmptyState
                icon={FileMinus}
                title="No credit notes yet"
                description="Issue a credit note to apply credits or refunds against invoices."
                action={{
                  label: 'Issue Credit Note',
                  icon: Plus,
                  onClick: () => setShowCreate(true),
                }}
              />
            )
          ) : filtered.length === 0 ? (
            <p className="px-6 py-8 text-sm text-muted-foreground text-center">
              No credit notes match your search
            </p>
          ) : (
            <>
              <Table>
                <TableHeader>
                  <TableRow>
                    <SortableHead label="Number" sortKey="credit_note_number" activeSortKey={sortKey} sortDir={sortDir} onSort={onSort} />
                    <TableHead className="text-xs font-medium">Customer</TableHead>
                    <TableHead className="text-xs font-medium">Invoice</TableHead>
                    <TableHead className="text-xs font-medium">Reason</TableHead>
                    <SortableHead label="Status" sortKey="status" activeSortKey={sortKey} sortDir={sortDir} onSort={onSort} />
                    <SortableHead label="Amount" sortKey="total_cents" activeSortKey={sortKey} sortDir={sortDir} onSort={onSort} className="text-right" />
                    <SortableHead label="Date" sortKey="created_at" activeSortKey={sortKey} sortDir={sortDir} onSort={onSort} />
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {paginated.map((note: CreditNote) => {
                    const isRefund = note.refund_amount_cents > 0
                    return (
                      <TableRow key={note.id} className={cn('border-l-[3px]', statusBorderColor(note.status))}>
                        <TableCell>
                          <p className="text-sm font-medium text-foreground">{note.credit_note_number}</p>
                          <p className="text-xs text-muted-foreground mt-0.5">{isRefund ? 'Refund to card' : 'Credit to balance'}</p>
                        </TableCell>
                        <TableCell className="text-sm">
                          <Link to={`/customers/${note.customer_id}`} className="text-primary hover:underline">
                            {customerMap[note.customer_id]?.display_name || 'Unknown'}
                          </Link>
                        </TableCell>
                        <TableCell className="text-sm">
                          <Link to={`/invoices/${note.invoice_id}`} className="text-primary hover:underline font-mono">
                            {invoiceMap[note.invoice_id]?.invoice_number || note.invoice_id.slice(0, 12) + '...'}
                          </Link>
                        </TableCell>
                        <TableCell className="text-sm text-muted-foreground max-w-[220px] truncate" title={note.reason}>
                          {note.reason}
                        </TableCell>
                        <TableCell>
                          <div className="flex items-center gap-1.5">
                            <Badge variant={statusBadgeVariant(note.status)}>{note.status}</Badge>
                            {isRefund && note.status === 'issued' && note.refund_status && note.refund_status !== 'none' && (
                              <Badge variant={note.refund_status === 'succeeded' ? 'success' : note.refund_status === 'failed' ? 'danger' : 'warning'}>
                                {note.refund_status === 'succeeded' ? 'refunded' : note.refund_status === 'failed' ? 'refund failed' : 'refund pending'}
                              </Badge>
                            )}
                          </div>
                        </TableCell>
                        <TableCell className="text-right tabular-nums font-mono text-sm">
                          {formatCents(note.total_cents)}
                        </TableCell>
                        <TableCell>
                          <div className="flex items-center justify-between">
                            <span className="text-sm text-muted-foreground whitespace-nowrap">
                              {note.issued_at ? formatDate(note.issued_at) : formatDateTime(note.created_at)}
                            </span>
                            {note.status === 'draft' && (
                              <div className="flex items-center gap-1 ml-3">
                                <Button variant="outline" size="sm" onClick={() => setConfirmIssue(note.id)}>
                                  Issue
                                </Button>
                                <Button variant="outline" size="sm" className="text-destructive hover:text-destructive" onClick={() => setConfirmVoid(note.id)}>
                                  Void
                                </Button>
                              </div>
                            )}
                          </div>
                        </TableCell>
                      </TableRow>
                    )
                  })}
                </TableBody>
              </Table>

              {/* Pagination */}
              {totalPages > 1 && (
                <div className="border-t border-border px-4 py-3 flex items-center justify-between">
                  <p className="text-xs text-muted-foreground">
                    Showing {(currentPage - 1) * PAGE_SIZE + 1}
                    {'\u2013'}
                    {Math.min(currentPage * PAGE_SIZE, sorted.length)} of {sorted.length}
                  </p>
                  <Pagination>
                    <PaginationContent>
                      <PaginationItem>
                        <PaginationPrevious
                          onClick={() => setUrlState({ page: String(Math.max(1, currentPage - 1)) })}
                          className={cn(currentPage <= 1 && 'pointer-events-none opacity-50')}
                        />
                      </PaginationItem>
                      {Array.from({ length: Math.min(totalPages, 5) }, (_, i) => {
                        let pageNum: number
                        if (totalPages <= 5) {
                          pageNum = i + 1
                        } else if (currentPage <= 3) {
                          pageNum = i + 1
                        } else if (currentPage >= totalPages - 2) {
                          pageNum = totalPages - 4 + i
                        } else {
                          pageNum = currentPage - 2 + i
                        }
                        return (
                          <PaginationItem key={pageNum}>
                            <PaginationLink
                              onClick={() => setUrlState({ page: String(pageNum) })}
                              isActive={currentPage === pageNum}
                            >
                              {pageNum}
                            </PaginationLink>
                          </PaginationItem>
                        )
                      })}
                      <PaginationItem>
                        <PaginationNext
                          onClick={() => setUrlState({ page: String(Math.min(totalPages, currentPage + 1)) })}
                          className={cn(currentPage >= totalPages && 'pointer-events-none opacity-50')}
                        />
                      </PaginationItem>
                    </PaginationContent>
                  </Pagination>
                </div>
              )}
            </>
          )}
        </CardContent>
      </Card>

      {/* Create Credit Note Dialog */}
      <CreateCreditNoteDialog
        open={showCreate}
        onOpenChange={setShowCreate}
        customerMap={customerMap}
        onCreated={() => {
          setShowCreate(false)
          queryClient.invalidateQueries({ queryKey: ['credit-notes'] })
          toast.success('Credit note issued')
        }}
      />

      {/* Issue Confirm */}
      <AlertDialog open={confirmIssue !== null} onOpenChange={(open) => { if (!open) setConfirmIssue(null) }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Issue Credit Note</AlertDialogTitle>
            <AlertDialogDescription>
              Issue this credit note? This will apply the credit or initiate the refund. Once issued, it cannot be reversed.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={() => { if (confirmIssue) issueMutation.mutate(confirmIssue); setConfirmIssue(null) }}>
              Issue Credit Note
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* Void Confirm */}
      <AlertDialog open={confirmVoid !== null} onOpenChange={(open) => { if (!open) setConfirmVoid(null) }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Void Credit Note</AlertDialogTitle>
            <AlertDialogDescription>
              Void this draft credit note? This cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => { if (confirmVoid) voidMutation.mutate(confirmVoid); setConfirmVoid(null) }}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              Void
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </Layout>
  )
}

// --- Create Credit Note Dialog ---

function CreateCreditNoteDialog({ open, onOpenChange, customerMap, onCreated }: {
  open: boolean
  onOpenChange: (open: boolean) => void
  customerMap: Record<string, Customer>
  onCreated: () => void
}) {
  const [invoices, setInvoices] = useState<Invoice[]>([])
  const [error, setError] = useState('')
  const [loadingInvoices, setLoadingInvoices] = useState(true)

  const form = useForm<CreditNoteFormData>({
    resolver: zodResolver(creditNoteSchema),
    defaultValues: { invoice_id: '', reason: '', amount: '', refundType: 'credit' },
  })

  const invoiceId = form.watch('invoice_id')
  const reason = form.watch('reason')
  const amount = form.watch('amount')
  const refundType = form.watch('refundType')

  useEffect(() => {
    if (open) {
      setLoadingInvoices(true)
      Promise.all([
        api.listInvoices('status=finalized').catch(() => ({ data: [] as Invoice[], total: 0 })),
        api.listInvoices('status=paid').catch(() => ({ data: [] as Invoice[], total: 0 })),
      ]).then(([fin, paid]) => {
        setInvoices([...fin.data, ...paid.data])
        setLoadingInvoices(false)
      })
    }
  }, [open])

  const selectedInvoice = invoices.find(inv => inv.id === invoiceId)
  const isPaid = selectedInvoice?.status === 'paid'
  const maxAmount = selectedInvoice
    ? isPaid ? selectedInvoice.amount_paid_cents : selectedInvoice.amount_due_cents
    : 0

  const createMutation = useMutation({
    mutationFn: async (data: CreditNoteFormData) => {
      const amountCents = Math.round(parseFloat(data.amount) * 100)
      return api.createCreditNote({
        invoice_id: data.invoice_id,
        reason: data.reason,
        refund_type: data.refundType,
        auto_issue: true,
        lines: [{ description: data.reason, quantity: 1, unit_amount_cents: amountCents }],
      })
    },
    onSuccess: () => {
      form.reset()
      onCreated()
    },
    onError: (err) => {
      setError(err instanceof Error ? err.message : 'Failed to create credit note')
    },
  })

  const onSubmit = form.handleSubmit((data) => {
    setError('')
    createMutation.mutate(data)
  })

  return (
    <Dialog open={open} onOpenChange={(o) => {
      onOpenChange(o)
      if (!o) { form.reset(); setError('') }
    }}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Issue Credit Note</DialogTitle>
          <DialogDescription>
            Create a credit note against an invoice.
          </DialogDescription>
        </DialogHeader>

        {loadingInvoices ? (
          <div className="py-8 flex justify-center">
            <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
          </div>
        ) : (
          <Form {...form}>
            <form onSubmit={onSubmit} noValidate className="space-y-4">
              <FormField
                control={form.control}
                name="invoice_id"
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>Invoice</FormLabel>
                    <FormControl>
                      <select
                        value={field.value}
                        onChange={(e) => {
                          field.onChange(e.target.value)
                          const inv = invoices.find(i => i.id === e.target.value)
                          if (inv?.status !== 'paid') form.setValue('refundType', 'credit')
                        }}
                        className="flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-xs transition-colors focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                      >
                        <option value="">Select invoice...</option>
                        {invoices.map(inv => {
                          const custName = customerMap[inv.customer_id]?.display_name || ''
                          return (
                            <option key={inv.id} value={inv.id}>
                              {inv.invoice_number} - {custName} ({formatCents(inv.total_amount_cents)})
                            </option>
                          )
                        })}
                      </select>
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />

              {selectedInvoice && (
                <div className="bg-muted rounded-lg px-4 py-3">
                  <div className="grid grid-cols-3 gap-4 text-sm">
                    <div>
                      <p className="text-muted-foreground">Customer</p>
                      <p className="font-medium text-foreground mt-0.5">{customerMap[selectedInvoice.customer_id]?.display_name || 'Unknown'}</p>
                    </div>
                    <div>
                      <p className="text-muted-foreground">{isPaid ? 'Amount Paid' : 'Amount Due'}</p>
                      <p className="font-medium text-foreground mt-0.5">{formatCents(maxAmount)}</p>
                    </div>
                    <div>
                      <p className="text-muted-foreground">Invoice Status</p>
                      <div className="mt-0.5">
                        <Badge variant={statusBadgeVariant(selectedInvoice.status)}>{selectedInvoice.status}</Badge>
                      </div>
                    </div>
                  </div>
                </div>
              )}

              <div>
                <FormLabel>Reason</FormLabel>
                <div className="flex flex-wrap gap-1.5 my-2">
                  {['Service disruption', 'Billing error', 'Goodwill credit', 'Feature downgrade', 'Contract adjustment'].map(r => (
                    <button
                      key={r}
                      type="button"
                      onClick={() => form.setValue('reason', r, { shouldValidate: true })}
                      className={cn(
                        'px-2.5 py-1 rounded-md text-xs font-medium transition-colors',
                        reason === r
                          ? 'bg-primary/10 text-primary ring-1 ring-primary/30'
                          : 'bg-muted text-muted-foreground hover:bg-muted/80'
                      )}
                    >
                      {r}
                    </button>
                  ))}
                </div>
                <FormField
                  control={form.control}
                  name="reason"
                  render={({ field }) => (
                    <FormItem>
                      <FormControl>
                        <Input placeholder="Or type a custom reason..." maxLength={500} {...field} />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              </div>

              <div className="grid grid-cols-2 gap-4">
                <FormField
                  control={form.control}
                  name="amount"
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>Amount ({getCurrencySymbol()})</FormLabel>
                      <FormControl>
                        <Input
                          type="number"
                          step="0.01"
                          min="0.01"
                          max={maxAmount ? (maxAmount / 100).toFixed(2) : '999999.99'}
                          placeholder="25.00"
                          {...field}
                        />
                      </FormControl>
                      {maxAmount > 0 && <FormDescription>Max: {formatCents(maxAmount)}</FormDescription>}
                      <FormMessage />
                    </FormItem>
                  )}
                />
                {isPaid ? (
                  <FormField
                    control={form.control}
                    name="refundType"
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>Credit Type</FormLabel>
                        <FormControl>
                          <select
                            value={field.value}
                            onChange={field.onChange}
                            className="flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-xs transition-colors focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                          >
                            <option value="credit">Credit to balance</option>
                            <option value="refund">Refund to payment method</option>
                          </select>
                        </FormControl>
                      </FormItem>
                    )}
                  />
                ) : (
                  <div className="pt-8">
                    <p className="text-sm text-muted-foreground">Reduces amount due on invoice</p>
                  </div>
                )}
              </div>

              {amount && selectedInvoice && (
                <div className="bg-blue-50 dark:bg-blue-950/30 border border-blue-100 dark:border-blue-900 rounded-lg px-4 py-3 text-sm">
                  <p className="text-blue-800 dark:text-blue-200">
                    {isPaid && refundType === 'refund'
                      ? `${getCurrencySymbol()}${parseFloat(amount || '0').toFixed(2)} will be refunded to the customer's payment method`
                      : isPaid
                        ? `${getCurrencySymbol()}${parseFloat(amount || '0').toFixed(2)} will be added to the customer's credit balance`
                        : `Invoice amount due will be reduced by ${getCurrencySymbol()}${parseFloat(amount || '0').toFixed(2)}`
                    }
                  </p>
                </div>
              )}

              {error && (
                <div className="px-3 py-2 rounded-md bg-destructive/10 border border-destructive/20">
                  <p className="text-destructive text-sm">{error}</p>
                </div>
              )}

              <DialogFooter>
                <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
                  Cancel
                </Button>
                <Button type="submit" disabled={createMutation.isPending}>
                  {createMutation.isPending ? (
                    <>
                      <Loader2 size={14} className="animate-spin mr-2" />
                      Issuing...
                    </>
                  ) : (
                    'Issue Credit Note'
                  )}
                </Button>
              </DialogFooter>
            </form>
          </Form>
        )}
      </DialogContent>
    </Dialog>
  )
}
