import { useState, useMemo, useEffect, useCallback } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { toast } from 'sonner'
import { api, formatCents, formatDate, formatDateTime, getCurrencySymbol } from '@/lib/api'
import type { Customer, CreditBalance, CreditLedgerEntry } from '@/lib/api'
import { downloadCSV } from '@/lib/csv'
import { Layout } from '@/components/Layout'
import { useSortable } from '@/hooks/useSortable'
import { cn } from '@/lib/utils'
import { InitialsAvatar } from '@/components/InitialsAvatar'
import { DatePicker } from '@/components/ui/date-picker'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
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

import { Plus, Minus, Download, ChevronRight, ArrowLeft, Loader2, ArrowUpDown, ArrowUp, ArrowDown } from 'lucide-react'

const creditSchema = z.object({
  amount: z.string().min(1, 'Amount is required').refine(v => parseFloat(v) >= 0.01, 'Must be at least 0.01'),
  description: z.string().min(1, 'Description is required'),
  expiresAt: z.string(),
})

type CreditFormData = z.infer<typeof creditSchema>

const ENTRY_TYPES = ['All', 'grant', 'usage', 'adjustment'] as const

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

function entryTypeVariant(type: string): 'default' | 'secondary' | 'destructive' | 'outline' {
  switch (type) {
    case 'grant': return 'default'
    case 'usage': return 'secondary'
    case 'adjustment': return 'outline'
    default: return 'outline'
  }
}

export default function CreditsPage() {
  const [customers, setCustomers] = useState<Customer[]>([])
  const [balances, setBalances] = useState<CreditBalance[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  // Detail view state
  const [selectedCustomerId, setSelectedCustomerId] = useState<string | null>(null)
  const [ledger, setLedger] = useState<CreditLedgerEntry[]>([])
  const [ledgerLoading, setLedgerLoading] = useState(false)
  const [entryTypeFilter, setEntryTypeFilter] = useState<string>('All')
  const [ledgerPage, setLedgerPage] = useState(1)
  const ledgerPageSize = 25

  // Modals
  const [showGrant, setShowGrant] = useState(false)
  const [showDeduct, setShowDeduct] = useState(false)
  const [modalCustomerId, setModalCustomerId] = useState('')

  const [searchParams, setSearchParams] = useSearchParams()

  const customerMap = useMemo(() => {
    const m: Record<string, Customer> = {}
    customers.forEach(c => { m[c.id] = c })
    return m
  }, [customers])

  const loadData = useCallback(() => {
    setLoading(true)
    setError(null)
    Promise.all([
      api.listBalances(),
      api.listCustomers(),
    ]).then(([balRes, custRes]) => {
      setBalances(balRes.data || [])
      setCustomers(custRes.data || [])
      setLoading(false)
    }).catch(err => {
      setError(err instanceof Error ? err.message : 'Failed to load credits')
      setLoading(false)
    })
  }, [])

  useEffect(() => {
    loadData()
    const customerParam = searchParams.get('customer')
    if (customerParam) {
      openCustomerDetail(customerParam)
    }
  }, [])

  const loadLedger = (customerId: string) => {
    setLedgerLoading(true)
    api.listLedger(customerId).then(res => {
      setLedger(res.data || [])
      setLedgerLoading(false)
    }).catch(() => {
      setLedger([])
      setLedgerLoading(false)
    })
  }

  const openCustomerDetail = (customerId: string) => {
    setSelectedCustomerId(customerId)
    setEntryTypeFilter('All')
    setLedgerPage(1)
    setSearchParams({ customer: customerId })
    loadLedger(customerId)
  }

  const closeDetail = () => {
    setSelectedCustomerId(null)
    setLedger([])
    setSearchParams({})
    loadData()
  }

  const totalOutstanding = balances.reduce((sum, b) => sum + b.balance_cents, 0)
  const selectedBalance = balances.find(b => b.customer_id === selectedCustomerId) || null
  const selectedCustomer = selectedCustomerId ? customerMap[selectedCustomerId] : null

  const filteredLedger = useMemo(() => entryTypeFilter === 'All'
    ? ledger
    : ledger.filter(e => e.entry_type === entryTypeFilter), [ledger, entryTypeFilter])

  const { sorted: sortedLedger, sortKey: ledgerSortKey, sortDir: ledgerSortDir, onSort: onLedgerSort } = useSortable(filteredLedger, 'created_at', 'desc')

  const ledgerTotalPages = Math.ceil(sortedLedger.length / ledgerPageSize)
  const ledgerCurrentPage = Math.min(ledgerPage, ledgerTotalPages || 1)
  const ledgerPaginated = sortedLedger.slice((ledgerCurrentPage - 1) * ledgerPageSize, ledgerCurrentPage * ledgerPageSize)

  // ---- Detail View ----
  if (selectedCustomerId) {
    return (
      <Layout>
        {/* Header with back, customer info, balance, and actions */}
        <div className="flex items-start gap-3 mb-6">
          <Button variant="ghost" size="sm" onClick={closeDetail} className="mt-1">
            <ArrowLeft size={18} />
          </Button>
          <div className="flex-1">
            <div className="flex items-center gap-3">
              <h1 className="text-2xl font-semibold text-foreground">
                {selectedCustomer?.display_name || 'Customer'}
              </h1>
              <Link to={`/customers/${selectedCustomerId}`} className="text-xs text-primary hover:underline">
                View customer
              </Link>
            </div>
            <p className="text-sm text-muted-foreground mt-0.5">Credit Balance</p>
            <p className={cn('text-3xl font-semibold mt-1', (selectedBalance?.balance_cents || 0) > 0 ? 'text-emerald-600' : 'text-muted-foreground')}>
              {formatCents(selectedBalance?.balance_cents || 0)}
            </p>
          </div>
          <div className="flex items-center gap-2 mt-1">
            <Button variant="outline" size="sm" onClick={() => { setModalCustomerId(selectedCustomerId); setShowDeduct(true) }}>
              <Minus size={16} className="mr-2" /> Deduct
            </Button>
            <Button size="sm" onClick={() => { setModalCustomerId(selectedCustomerId); setShowGrant(true) }}>
              <Plus size={16} className="mr-2" /> Grant
            </Button>
          </div>
        </div>

        {/* Transaction History */}
        <Card>
          <CardHeader className="flex flex-row items-center justify-between pb-3">
            <CardTitle className="text-sm">Transaction History</CardTitle>
            <div className="flex items-center gap-2">
              <select
                value={entryTypeFilter}
                onChange={e => { setEntryTypeFilter(e.target.value); setLedgerPage(1) }}
                className="flex h-8 rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-xs transition-colors focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
              >
                {ENTRY_TYPES.map(t => (
                  <option key={t} value={t}>{t === 'All' ? 'All types' : t.charAt(0).toUpperCase() + t.slice(1)}</option>
                ))}
              </select>
              {ledger.length > 0 && (
                <Button variant="outline" size="sm" onClick={() => {
                  const rows = filteredLedger.map(e => [
                    formatDateTime(e.created_at),
                    e.entry_type,
                    e.description,
                    (e.amount_cents / 100).toFixed(2),
                    (e.balance_after / 100).toFixed(2),
                    e.invoice_id || '',
                  ])
                  downloadCSV(`credits-${selectedCustomer?.external_id || 'customer'}.csv`,
                    ['Date', 'Type', 'Description', 'Amount', 'Balance', 'Invoice ID'], rows)
                }}>
                  <Download size={14} className="mr-1" /> CSV
                </Button>
              )}
            </div>
          </CardHeader>
          <CardContent className="p-0">
            {ledgerLoading ? (
              <div className="p-8 flex justify-center">
                <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
              </div>
            ) : filteredLedger.length === 0 ? (
              <div className="px-6 py-12 text-center">
                <p className="text-sm font-medium text-foreground">No transactions{entryTypeFilter !== 'All' ? ` of type "${entryTypeFilter}"` : ''}</p>
                <p className="text-sm text-muted-foreground mt-1">Grant credits to get started</p>
              </div>
            ) : (
              <>
                <Table>
                  <TableHeader>
                    <TableRow>
                      <SortableHead label="Date" sortKey="created_at" activeSortKey={ledgerSortKey} sortDir={ledgerSortDir} onSort={onLedgerSort} />
                      <SortableHead label="Type" sortKey="entry_type" activeSortKey={ledgerSortKey} sortDir={ledgerSortDir} onSort={onLedgerSort} />
                      <TableHead className="text-xs font-medium">Description</TableHead>
                      <TableHead className="text-xs font-medium">Invoice</TableHead>
                      <TableHead className="text-xs font-medium">Expires</TableHead>
                      <SortableHead label="Amount" sortKey="amount_cents" activeSortKey={ledgerSortKey} sortDir={ledgerSortDir} onSort={onLedgerSort} className="text-right" />
                      <TableHead className="text-xs font-medium text-right">Balance</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {ledgerPaginated.map(entry => (
                      <TableRow key={entry.id}>
                        <TableCell className="text-sm text-muted-foreground whitespace-nowrap">{formatDate(entry.created_at)}</TableCell>
                        <TableCell><Badge variant={entryTypeVariant(entry.entry_type)}>{entry.entry_type}</Badge></TableCell>
                        <TableCell className="text-sm text-foreground">{entry.description || '\u2014'}</TableCell>
                        <TableCell className="text-sm">
                          {entry.invoice_id ? (
                            <Link to={`/invoices/${entry.invoice_id}`} className="text-primary hover:underline">
                              View invoice
                            </Link>
                          ) : (
                            <span className="text-muted-foreground">\u2014</span>
                          )}
                        </TableCell>
                        <TableCell className="text-sm text-muted-foreground whitespace-nowrap">
                          {entry.expires_at ? formatDate(entry.expires_at) : '\u2014'}
                        </TableCell>
                        <TableCell className={cn('text-right tabular-nums font-mono text-sm', entry.amount_cents >= 0 ? 'text-emerald-600' : 'text-destructive')}>
                          {entry.amount_cents >= 0 ? '+' : ''}{formatCents(entry.amount_cents)}
                        </TableCell>
                        <TableCell className="text-right tabular-nums font-mono text-sm">{formatCents(entry.balance_after)}</TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>

                {/* Pagination */}
                {ledgerTotalPages > 1 && (
                  <div className="border-t border-border px-4 py-3 flex items-center justify-between">
                    <p className="text-xs text-muted-foreground">
                      Showing {(ledgerCurrentPage - 1) * ledgerPageSize + 1}
                      {'\u2013'}
                      {Math.min(ledgerCurrentPage * ledgerPageSize, sortedLedger.length)} of {sortedLedger.length}
                    </p>
                    <Pagination>
                      <PaginationContent>
                        <PaginationItem>
                          <PaginationPrevious
                            onClick={() => setLedgerPage(p => Math.max(1, p - 1))}
                            className={cn(ledgerCurrentPage <= 1 && 'pointer-events-none opacity-50')}
                          />
                        </PaginationItem>
                        {Array.from({ length: Math.min(ledgerTotalPages, 5) }, (_, i) => {
                          let pageNum: number
                          if (ledgerTotalPages <= 5) {
                            pageNum = i + 1
                          } else if (ledgerCurrentPage <= 3) {
                            pageNum = i + 1
                          } else if (ledgerCurrentPage >= ledgerTotalPages - 2) {
                            pageNum = ledgerTotalPages - 4 + i
                          } else {
                            pageNum = ledgerCurrentPage - 2 + i
                          }
                          return (
                            <PaginationItem key={pageNum}>
                              <PaginationLink
                                onClick={() => setLedgerPage(pageNum)}
                                isActive={ledgerCurrentPage === pageNum}
                              >
                                {pageNum}
                              </PaginationLink>
                            </PaginationItem>
                          )
                        })}
                        <PaginationItem>
                          <PaginationNext
                            onClick={() => setLedgerPage(p => Math.min(ledgerTotalPages, p + 1))}
                            className={cn(ledgerCurrentPage >= ledgerTotalPages && 'pointer-events-none opacity-50')}
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

        {showGrant && (
          <CreditDialog mode="grant" customerId={modalCustomerId}
            customerName={customerMap[modalCustomerId]?.display_name || ''}
            open={showGrant}
            onOpenChange={(open) => { if (!open) setShowGrant(false) }}
            onDone={() => { setShowGrant(false); loadLedger(selectedCustomerId); loadData(); toast.success('Credits granted') }} />
        )}
        {showDeduct && (
          <CreditDialog mode="deduct" customerId={modalCustomerId}
            customerName={customerMap[modalCustomerId]?.display_name || ''}
            open={showDeduct}
            onOpenChange={(open) => { if (!open) setShowDeduct(false) }}
            onDone={() => { setShowDeduct(false); loadLedger(selectedCustomerId); loadData(); toast.success('Credits deducted') }} />
        )}
      </Layout>
    )
  }

  // ---- List View ----
  const balMap: Record<string, CreditBalance> = {}
  balances.forEach(b => { balMap[b.customer_id] = b })
  const customersWithCredits = customers
    .filter(c => balMap[c.id])
    .map(c => ({ customer: c, balance: balMap[c.id] }))
    .sort((a, b) => b.balance.balance_cents - a.balance.balance_cents)

  return (
    <Layout>
      <div className="flex justify-between items-center">
        <div>
          <h1 className="text-2xl font-semibold text-foreground">Credits</h1>
          <p className="text-sm text-muted-foreground mt-1">
            Manage prepaid credit balances{customersWithCredits.length > 0 ? ` · ${formatCents(totalOutstanding)} across ${customersWithCredits.length} customer${customersWithCredits.length !== 1 ? 's' : ''}` : ''}
          </p>
        </div>
        {customersWithCredits.length > 0 && (
          <Button size="sm" onClick={() => { setModalCustomerId(''); setShowGrant(true) }}>
            <Plus size={16} className="mr-2" /> Grant Credits
          </Button>
        )}
      </div>

      {/* Customer balances table */}
      <Card className="mt-6">
        <CardContent className="p-0">
          {error ? (
            <div className="p-8 text-center">
              <p className="text-sm text-destructive mb-3">{error}</p>
              <Button variant="outline" size="sm" onClick={loadData}>
                Retry
              </Button>
            </div>
          ) : loading ? (
            <div className="p-8 flex justify-center">
              <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
            </div>
          ) : customersWithCredits.length === 0 ? (
            <div className="p-12 text-center">
              <p className="text-sm font-medium text-foreground">No credit activity</p>
              <p className="text-sm text-muted-foreground mt-1">Grant credits to a customer to get started</p>
              <Button size="sm" className="mt-4" onClick={() => { setModalCustomerId(''); setShowGrant(true) }}>
                <Plus size={16} className="mr-2" /> Grant Credits
              </Button>
            </div>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="text-xs font-medium">Customer</TableHead>
                  <TableHead className="text-xs font-medium text-right">Balance</TableHead>
                  <TableHead className="text-xs font-medium text-right"></TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {customersWithCredits.map(({ customer, balance }) => (
                  <TableRow
                    key={customer.id}
                    className="cursor-pointer hover:bg-muted/50 transition-colors"
                    onClick={() => openCustomerDetail(customer.id)}
                  >
                    <TableCell>
                      <div className="flex items-center gap-2.5">
                        <InitialsAvatar name={customer.display_name} />
                        <div>
                          <Link
                            to={`/customers/${customer.id}`}
                            onClick={e => e.stopPropagation()}
                            className="text-sm font-medium text-foreground hover:text-primary"
                          >
                            {customer.display_name}
                          </Link>
                          <p className="text-xs text-muted-foreground">{customer.external_id}</p>
                        </div>
                      </div>
                    </TableCell>
                    <TableCell className={cn('text-right tabular-nums font-mono text-sm',
                      balance.balance_cents > 0 ? 'text-emerald-600' : balance.balance_cents === 0 ? 'text-muted-foreground' : 'text-destructive'
                    )}>
                      {formatCents(balance.balance_cents)}
                    </TableCell>
                    <TableCell className="text-right">
                      <ChevronRight size={16} className="text-muted-foreground inline" />
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      {showGrant && (
        <CreditDialog mode="grant" customerId={modalCustomerId} customers={customers}
          customerName={customerMap[modalCustomerId]?.display_name || ''}
          open={showGrant}
          onOpenChange={(open) => { if (!open) setShowGrant(false) }}
          onDone={() => { setShowGrant(false); loadData(); toast.success('Credits granted') }} />
      )}
    </Layout>
  )
}

// ---- Grant / Deduct Dialog ----

function CreditDialog({ mode, customerId, customerName, customers, open, onOpenChange, onDone }: {
  mode: 'grant' | 'deduct'
  customerId: string
  customerName: string
  customers?: Customer[]
  open: boolean
  onOpenChange: (open: boolean) => void
  onDone: () => void
}) {
  const [selectedCustomer, setSelectedCustomer] = useState(customerId)
  const [error, setError] = useState('')
  const [showConfirm, setShowConfirm] = useState(false)

  const isDeduct = mode === 'deduct'

  const form = useForm<CreditFormData>({
    resolver: zodResolver(creditSchema),
    defaultValues: { amount: '', description: '', expiresAt: '' },
  })

  const effectiveCustomerId = selectedCustomer || customerId
  const effectiveCustomerName = customers?.find(c => c.id === effectiveCustomerId)?.display_name || customerName

  const saveMutation = useMutation({
    mutationFn: async () => {
      const amountCents = Math.round(parseFloat(form.getValues('amount')) * 100)
      const description = form.getValues('description')
      const expiresAt = form.getValues('expiresAt')
      if (isDeduct) {
        return api.adjustCredits({
          customer_id: effectiveCustomerId,
          amount_cents: -amountCents,
          description,
        })
      } else {
        return api.grantCredits({
          customer_id: effectiveCustomerId,
          amount_cents: amountCents,
          description,
          ...(expiresAt ? { expires_at: new Date(expiresAt).toISOString() } : {}),
        })
      }
    },
    onSuccess: () => {
      form.reset()
      onDone()
    },
    onError: (err) => {
      setError(err instanceof Error ? err.message : 'Failed to update credits')
    },
  })

  const onFormSubmit = form.handleSubmit(() => {
    if (!effectiveCustomerId) { setError('Select a customer'); return }
    setShowConfirm(true)
  })

  const handleConfirm = () => {
    setShowConfirm(false)
    setError('')
    saveMutation.mutate()
  }

  const confirmAmount = parseFloat(form.watch('amount') || '0')

  return (
    <>
      <Dialog open={open} onOpenChange={(o) => {
        onOpenChange(o)
        if (!o) { form.reset(); setError('') }
      }}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>{isDeduct ? 'Deduct Credits' : 'Grant Credits'}</DialogTitle>
          </DialogHeader>
          <Form {...form}>
            <form onSubmit={onFormSubmit} noValidate className="space-y-4">
              {!customerId && customers && (
                <div>
                  <Label className="text-sm font-medium">Customer</Label>
                  <select
                    value={selectedCustomer}
                    onChange={(e) => setSelectedCustomer(e.target.value)}
                    className="flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-xs transition-colors focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring mt-2"
                  >
                    <option value="">Select customer...</option>
                    {customers.map(c => (
                      <option key={c.id} value={c.id}>{c.display_name} ({c.external_id})</option>
                    ))}
                  </select>
                </div>
              )}
              {customerId && (
                <div className="bg-muted rounded-lg px-4 py-3">
                  <p className="text-sm text-muted-foreground">Customer</p>
                  <p className="text-sm font-medium text-foreground mt-0.5">{effectiveCustomerName}</p>
                </div>
              )}

              <FormField
                control={form.control}
                name="amount"
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>Amount ({getCurrencySymbol()})</FormLabel>
                    <FormControl>
                      <Input type="number" step="0.01" min="0.01" max={999999.99} placeholder="50.00" {...field} />
                    </FormControl>
                    <FormDescription>
                      {isDeduct ? "Removed from the customer's prepaid balance" : "Added to the customer's prepaid balance"}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <FormField
                control={form.control}
                name="description"
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>Description</FormLabel>
                    <FormControl>
                      <Input
                        placeholder={isDeduct ? 'e.g. Billing correction, clawback' : 'e.g. Welcome credit, compensation'}
                        maxLength={500}
                        {...field}
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />

              {!isDeduct && (
                <FormField
                  control={form.control}
                  name="expiresAt"
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>Expires At</FormLabel>
                      <DatePicker
                        value={field.value}
                        onChange={field.onChange}
                        placeholder="Never expires"
                      />
                      <FormDescription>Leave empty for credits that never expire</FormDescription>
                    </FormItem>
                  )}
                />
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
                <Button type="submit" disabled={saveMutation.isPending} variant={isDeduct ? 'destructive' : 'default'}>
                  {saveMutation.isPending ? (
                    <>
                      <Loader2 size={14} className="animate-spin mr-2" />
                      Processing...
                    </>
                  ) : isDeduct ? 'Deduct Credits' : 'Grant Credits'}
                </Button>
              </DialogFooter>
            </form>
          </Form>
        </DialogContent>
      </Dialog>

      <AlertDialog open={showConfirm} onOpenChange={(open) => { if (!open) setShowConfirm(false) }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{isDeduct ? 'Confirm Deduction' : 'Confirm Credit Grant'}</AlertDialogTitle>
            <AlertDialogDescription>
              {isDeduct ? 'Deduct' : 'Grant'} ${confirmAmount.toFixed(2)} {isDeduct ? 'from' : 'to'} {effectiveCustomerName}?
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={handleConfirm}
              className={cn(isDeduct && 'bg-destructive text-destructive-foreground hover:bg-destructive/90')}
            >
              {isDeduct ? 'Deduct' : 'Grant Credits'}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}
