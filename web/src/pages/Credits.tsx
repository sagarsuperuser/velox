import { useEffect, useState, useMemo } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { api, formatCents, formatDateTime, getCurrencySymbol, type Customer, type CreditBalance, type CreditLedgerEntry } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'
import { SortableHeader } from '@/components/SortableHeader'
import { Modal } from '@/components/Modal'
import { FormField } from '@/components/FormField'
import { SearchSelect } from '@/components/SearchSelect'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { toast } from 'sonner'
import { useSortable } from '@/hooks/useSortable'
import { DatePicker } from '@/components/DatePicker'
import { Plus, Minus, Download, ChevronRight, ArrowLeft, Loader2 } from 'lucide-react'
import { downloadCSV } from '@/lib/csv'
import { Pagination } from '@/components/Pagination'
import { Breadcrumbs } from '@/components/Breadcrumbs'

const creditSchema = z.object({
  amount: z.string().min(1, 'Amount is required').refine(v => parseFloat(v) >= 0.01, 'Must be at least 0.01'),
  description: z.string().min(1, 'Description is required'),
  expiresAt: z.string(),
})

type CreditFormData = z.infer<typeof creditSchema>

const ENTRY_TYPES = ['All', 'grant', 'usage', 'adjustment'] as const

export function CreditsPage() {
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

  const loadData = () => {
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
  }

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

  // ── Detail View ──
  if (selectedCustomerId) {

    return (
      <Layout>
        {/* Header with back, customer info, balance, and actions */}
        <div className="flex items-start gap-3 mb-6">
          <button onClick={closeDetail} className="p-1.5 mt-1 rounded-lg hover:bg-gray-100 transition-colors">
            <ArrowLeft size={18} className="text-gray-500" />
          </button>
          <div className="flex-1">
            <div className="flex items-center gap-3">
              <h1 className="text-2xl font-semibold text-gray-900 dark:text-gray-100">
                {selectedCustomer?.display_name || 'Customer'}
              </h1>
              <Link to={`/customers/${selectedCustomerId}`} className="text-xs text-velox-600 hover:underline">
                View customer
              </Link>
            </div>
            <p className="text-sm text-gray-600 mt-0.5">Credit Balance</p>
            <p className={`text-3xl font-semibold mt-1 ${(selectedBalance?.balance_cents || 0) > 0 ? 'text-emerald-600' : 'text-gray-400'}`}>
              {formatCents(selectedBalance?.balance_cents || 0)}
            </p>
          </div>
          <div className="flex items-center gap-2 mt-1">
            <button onClick={() => { setModalCustomerId(selectedCustomerId); setShowDeduct(true) }}
              className="flex items-center gap-2 px-4 py-2 border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-300 rounded-lg text-sm font-medium hover:bg-gray-50 dark:hover:bg-gray-800 shadow-sm transition-colors">
              <Minus size={16} /> Deduct
            </button>
            <button onClick={() => { setModalCustomerId(selectedCustomerId); setShowGrant(true) }}
              className="flex items-center gap-2 px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow transition-colors">
              <Plus size={16} /> Grant
            </button>
          </div>
        </div>

        {/* Transaction History */}
        <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card">
          <div className="px-6 py-4 border-b border-gray-100 flex items-center justify-between">
            <h2 className="text-sm font-semibold text-gray-900 dark:text-gray-100">Transaction History</h2>
            <div className="flex items-center gap-2">
              <select value={entryTypeFilter} onChange={e => { setEntryTypeFilter(e.target.value); setLedgerPage(1) }}
                className="px-3 py-1.5 border border-gray-200 dark:border-gray-700 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-velox-500 bg-white dark:bg-gray-800">
                {ENTRY_TYPES.map(t => (
                  <option key={t} value={t}>{t === 'All' ? 'All types' : t.charAt(0).toUpperCase() + t.slice(1)}</option>
                ))}
              </select>
              {ledger.length > 0 && (
                <button onClick={() => {
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
                }}
                  className="flex items-center gap-1.5 px-3 py-1.5 border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-300 rounded-lg text-sm font-medium hover:bg-gray-50 dark:hover:bg-gray-800 transition-colors">
                  <Download size={14} /> CSV
                </button>
              )}
            </div>
          </div>

          {ledgerLoading ? (
            <LoadingSkeleton rows={5} columns={5} />
          ) : filteredLedger.length === 0 ? (
            <div className="px-6 py-12 text-center">
              <p className="text-sm text-gray-900 dark:text-gray-100">No transactions{entryTypeFilter !== 'All' ? ` of type "${entryTypeFilter}"` : ''}</p>
              <p className="text-sm text-gray-600 dark:text-gray-400 mt-1">Grant credits to get started</p>
            </div>
          ) : (
            <>
            <div className="overflow-x-auto">
            <table className="w-full">
              <thead>
                <tr className="border-b border-gray-100 dark:border-gray-800 bg-gray-50 dark:bg-gray-800/50">
                  <SortableHeader label="Date" sortKey="created_at" activeSortKey={ledgerSortKey} sortDir={ledgerSortDir} onSort={onLedgerSort} />
                  <SortableHeader label="Type" sortKey="entry_type" activeSortKey={ledgerSortKey} sortDir={ledgerSortDir} onSort={onLedgerSort} />
                  <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Description</th>
                  <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Invoice</th>
                  <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Expires</th>
                  <SortableHeader label="Amount" sortKey="amount_cents" activeSortKey={ledgerSortKey} sortDir={ledgerSortDir} onSort={onLedgerSort} align="right" />
                  <th className="text-right text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Balance</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
                {ledgerPaginated.map(entry => (
                  <tr key={entry.id} className="hover:bg-gray-50/50 dark:hover:bg-gray-800/50 transition-colors">
                    <td className="px-6 py-3 text-sm text-gray-600 whitespace-nowrap">{formatDateTime(entry.created_at)}</td>
                    <td className="px-6 py-3"><Badge status={entry.entry_type} /></td>
                    <td className="px-6 py-3 text-sm text-gray-900 dark:text-gray-100">{entry.description || '—'}</td>
                    <td className="px-6 py-3 text-sm">
                      {entry.invoice_id ? (
                        <Link to={`/invoices/${entry.invoice_id}`} className="text-velox-600 hover:underline">
                          View invoice
                        </Link>
                      ) : (
                        <span className="text-gray-300">—</span>
                      )}
                    </td>
                    <td className="px-6 py-3 text-sm text-gray-400 whitespace-nowrap">
                      {entry.expires_at ? formatDateTime(entry.expires_at) : '—'}
                    </td>
                    <td className={`px-6 py-3 text-sm font-medium text-right tabular-nums ${entry.amount_cents >= 0 ? 'text-emerald-600' : 'text-red-600'}`}>
                      {entry.amount_cents >= 0 ? '+' : ''}{formatCents(entry.amount_cents)}
                    </td>
                    <td className="px-6 py-3 text-sm text-gray-900 text-right tabular-nums">{formatCents(entry.balance_after)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            </div>
            <Pagination page={ledgerCurrentPage} totalPages={ledgerTotalPages} onPageChange={setLedgerPage} pageSize={ledgerPageSize} total={sortedLedger.length} />
            </>
          )}
        </div>

        {showGrant && (
          <CreditModal mode="grant" customerId={modalCustomerId}
            customerName={customerMap[modalCustomerId]?.display_name || ''}
            onClose={() => setShowGrant(false)}
            onDone={() => { setShowGrant(false); loadLedger(selectedCustomerId); loadData(); toast.success('Credits granted') }} />
        )}
        {showDeduct && (
          <CreditModal mode="deduct" customerId={modalCustomerId}
            customerName={customerMap[modalCustomerId]?.display_name || ''}
            onClose={() => setShowDeduct(false)}
            onDone={() => { setShowDeduct(false); loadLedger(selectedCustomerId); loadData(); toast.success('Credits deducted') }} />
        )}
      </Layout>
    )
  }

  // ── List View ──
  // Only show customers with credit activity (have entries in the ledger)
  const balMap: Record<string, CreditBalance> = {}
  balances.forEach(b => { balMap[b.customer_id] = b })
  const customersWithCredits = customers
    .filter(c => balMap[c.id])
    .map(c => ({ customer: c, balance: balMap[c.id] }))
    .sort((a, b) => b.balance.balance_cents - a.balance.balance_cents)

  return (
    <Layout>
      <Breadcrumbs items={[{ label: 'Configuration' }, { label: 'Credits' }]} />
      <div className="flex justify-between items-center">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900 dark:text-gray-100">Credits</h1>
          <p className="text-sm text-gray-600 dark:text-gray-400 mt-1">
            {customersWithCredits.length > 0
              ? `${formatCents(totalOutstanding)} outstanding across ${customersWithCredits.length} customer${customersWithCredits.length !== 1 ? 's' : ''}`
              : 'Manage customer prepaid balances'}
          </p>
        </div>
        {customersWithCredits.length > 0 && (
          <button onClick={() => { setModalCustomerId(''); setShowGrant(true) }}
            className="flex items-center gap-2 px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow transition-colors">
            <Plus size={16} /> Grant Credits
          </button>
        )}
      </div>

      {/* Customer balances table */}
      <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card mt-6">
        {error ? (
          <ErrorState message={error} onRetry={loadData} />
        ) : loading ? (
          <LoadingSkeleton rows={6} columns={3} />
        ) : customersWithCredits.length === 0 ? (
          <EmptyState title="No credit activity" description="Grant credits to a customer to get started" actionLabel="Grant Credits" onAction={() => { setModalCustomerId(''); setShowGrant(true) }} />
        ) : (
          <>
          <div className="overflow-x-auto">
          <table className="w-full">
            <thead>
              <tr className="border-b border-gray-100 dark:border-gray-800 bg-gray-50 dark:bg-gray-800/50">
                <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Customer</th>
                <th className="text-right text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Balance</th>
                <th className="text-right text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3"></th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
              {customersWithCredits.map(({ customer, balance }) => (
                <tr key={customer.id} className="hover:bg-gray-50 dark:hover:bg-gray-800/50 cursor-pointer transition-colors group"
                  onClick={() => openCustomerDetail(customer.id)}>
                  <td className="px-6 py-3">
                    <Link to={`/customers/${customer.id}`} onClick={e => e.stopPropagation()} className="text-sm font-medium text-velox-600 dark:text-velox-400 hover:underline transition-colors">{customer.display_name}</Link>
                    <p className="text-xs text-gray-500">{customer.external_id}</p>
                  </td>
                  <td className={`px-6 py-3 text-sm font-semibold text-right tabular-nums ${balance.balance_cents > 0 ? 'text-emerald-600' : balance.balance_cents === 0 ? 'text-gray-400' : 'text-red-600'}`}>
                    {formatCents(balance.balance_cents)}
                  </td>
                  <td className="px-6 py-3 text-right">
                    <ChevronRight size={16} className="text-gray-300 group-hover:text-velox-500 inline transition-colors" />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          </div>
          </>
        )}
      </div>

      {showGrant && (
        <CreditModal mode="grant" customerId={modalCustomerId} customers={customers}
          customerName={customerMap[modalCustomerId]?.display_name || ''}
          onClose={() => setShowGrant(false)}
          onDone={() => { setShowGrant(false); loadData(); toast.success('Credits granted') }} />
      )}
    </Layout>
  )
}

// ── Grant / Deduct Modal ──

function CreditModal({ mode, customerId, customerName, customers, onClose, onDone }: {
  mode: 'grant' | 'deduct'
  customerId: string
  customerName: string
  customers?: Customer[]
  onClose: () => void
  onDone: () => void
}) {
  const [selectedCustomer, setSelectedCustomer] = useState(customerId)
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)
  const [showConfirm, setShowConfirm] = useState(false)

  const isDeduct = mode === 'deduct'

  const { register, handleSubmit, watch, setValue, formState: { errors } } = useForm<CreditFormData>({
    resolver: zodResolver(creditSchema),
    defaultValues: { amount: '', description: '', expiresAt: '' },
  })

  const effectiveCustomerId = selectedCustomer || customerId
  const effectiveCustomerName = customers?.find(c => c.id === effectiveCustomerId)?.display_name || customerName
  const expiresAt = watch('expiresAt')
  const amount = watch('amount')
  const description = watch('description')

  const onFormSubmit = handleSubmit(() => {
    if (!effectiveCustomerId) { setError('Select a customer'); return }
    setShowConfirm(true)
  })

  const handleConfirm = async () => {
    setShowConfirm(false)
    setSaving(true); setError('')
    try {
      const amountCents = Math.round(parseFloat(amount) * 100)
      if (isDeduct) {
        await api.adjustCredits({
          customer_id: effectiveCustomerId,
          amount_cents: -amountCents,
          description,
        })
      } else {
        await api.grantCredits({
          customer_id: effectiveCustomerId,
          amount_cents: amountCents,
          description,
          ...(expiresAt ? { expires_at: new Date(expiresAt).toISOString() } : {}),
        })
      }
      onDone()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to update credits')
    } finally {
      setSaving(false)
    }
  }

  const confirmAmount = parseFloat(amount || '0')

  return (
    <>
      <Modal open onClose={onClose} title={isDeduct ? 'Deduct Credits' : 'Grant Credits'}>
        <form onSubmit={onFormSubmit} noValidate className="space-y-4">
          {!customerId && customers && (
            <SearchSelect label="Customer" required value={selectedCustomer}
              onChange={setSelectedCustomer}
              placeholder="Select customer..."
              options={customers.map(c => ({ value: c.id, label: c.display_name, sublabel: c.external_id }))} />
          )}
          {customerId && (
            <div className="bg-gray-50 dark:bg-gray-800 rounded-lg px-4 py-3">
              <p className="text-sm text-gray-600 dark:text-gray-400">Customer</p>
              <p className="text-sm font-medium text-gray-900 dark:text-gray-100 mt-0.5">{effectiveCustomerName}</p>
            </div>
          )}

          <FormField label={`Amount (${getCurrencySymbol()})`} required type="number" step="0.01" min="0.01" max={999999.99}
            placeholder="50.00"
            error={errors.amount?.message}
            hint={isDeduct ? 'Removed from the customer\'s prepaid balance' : 'Added to the customer\'s prepaid balance'}
            {...register('amount')} />

          <FormField label="Description" required
            placeholder={isDeduct ? 'e.g. Billing correction, clawback' : 'e.g. Welcome credit, compensation'}
            maxLength={500}
            error={errors.description?.message}
            {...register('description')} />

          {!isDeduct && (
            <DatePicker
              value={expiresAt ?? ''}
              onChange={v => setValue('expiresAt', v, { shouldDirty: true })}
              label="Expires At"
              includeTime
              hint="Leave empty for credits that never expire" />
          )}

          {error && <p className="text-sm text-red-600 bg-red-50 border border-red-200 rounded-lg px-3 py-2">{error}</p>}

          <div className="flex justify-end gap-3 pt-4 border-t border-gray-100 dark:border-gray-800 mt-2">
            <button type="button" onClick={onClose} className="px-4 py-2 border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-300 rounded-lg text-sm font-medium hover:bg-gray-50 dark:hover:bg-gray-800 transition-colors">Cancel</button>
            <button type="submit" disabled={saving}
              className={`flex items-center justify-center gap-2 px-4 py-2 text-white rounded-lg text-sm font-medium shadow-sm hover:shadow disabled:opacity-50 transition-colors ${
                isDeduct ? 'bg-red-600 hover:bg-red-700' : 'bg-velox-600 hover:bg-velox-700'
              }`}>
              {saving ? (<><Loader2 size={14} className="animate-spin" /> Processing...</>) : isDeduct ? 'Deduct Credits' : 'Grant Credits'}
            </button>
          </div>
        </form>
      </Modal>
      <ConfirmDialog
        open={showConfirm}
        title={isDeduct ? 'Confirm Deduction' : 'Confirm Credit Grant'}
        message={`${isDeduct ? 'Deduct' : 'Grant'} $${confirmAmount.toFixed(2)} ${isDeduct ? 'from' : 'to'} ${effectiveCustomerName}?`}
        confirmLabel={isDeduct ? 'Deduct' : 'Grant Credits'}
        variant={isDeduct ? 'danger' : undefined}
        onConfirm={handleConfirm}
        onCancel={() => setShowConfirm(false)}
      />
    </>
  )
}
