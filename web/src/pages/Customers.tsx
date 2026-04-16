import { useEffect, useState, useMemo, useCallback } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { api, formatDateTime, type Customer } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'
import { SortableHeader } from '@/components/SortableHeader'
import { Modal } from '@/components/Modal'
import { FormField } from '@/components/FormField'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { ErrorState } from '@/components/ErrorState'
import { useToast } from '@/components/Toast'
import { useFormValidation, rules } from '@/hooks/useFormValidation'
import { useSortable } from '@/hooks/useSortable'
import { Plus, Search, Download, Loader2 } from 'lucide-react'
import { downloadCSV } from '@/lib/csv'
import { Pagination } from '@/components/Pagination'
import { Breadcrumbs } from '@/components/Breadcrumbs'
import { DatePicker } from '@/components/DatePicker'

const PAGE_SIZE = 25

export function CustomersPage() {
  const [customers, setCustomers] = useState<Customer[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [search, setSearch] = useState('')
  const [dateFrom, setDateFrom] = useState('')
  const [dateTo, setDateTo] = useState('')
  const [showCreate, setShowCreate] = useState(false)
  const [creating, setCreating] = useState(false)
  const [form, setForm] = useState({ external_id: '', display_name: '', email: '' })
  const [error, setError] = useState('')
  const [filterStatus, setFilterStatus] = useState('')
  const [page, setPage] = useState(1)
  const toast = useToast()
  const navigate = useNavigate()

  const fieldRules = useMemo(() => ({
    display_name: [rules.required('Display name')],
    external_id: [rules.required('External ID'), rules.slug()],
    email: [rules.email()],
  }), [])
  const { onBlur, validateAll, fieldError, registerRef, clearErrors } = useFormValidation(fieldRules)

  // Server-side paginated fetch
  const loadCustomers = useCallback(() => {
    setLoading(true)
    setLoadError(null)
    const params = new URLSearchParams()
    params.set('limit', String(PAGE_SIZE))
    params.set('offset', String((page - 1) * PAGE_SIZE))
    if (filterStatus) params.set('status', filterStatus)
    api.listCustomers(params.toString()).then(res => {
      setCustomers(res.data)
      setTotal(res.total)
      setLoading(false)
    }).catch(err => { setLoadError(err instanceof Error ? err.message : 'Failed to load customers'); setLoading(false) })
  }, [page, filterStatus])

  useEffect(() => { loadCustomers() }, [loadCustomers])

  // Client-side search + date filter on current page data
  const filtered = useMemo(() => {
    return customers.filter(c => {
      if (search) {
        const q = search.toLowerCase()
        if (!c.display_name.toLowerCase().includes(q) &&
            !c.external_id.toLowerCase().includes(q) &&
            !(c.email && c.email.toLowerCase().includes(q))) return false
      }
      if (dateFrom && c.created_at && c.created_at.slice(0, 10) < dateFrom) return false
      if (dateTo && c.created_at && c.created_at.slice(0, 10) > dateTo) return false
      return true
    })
  }, [customers, search, dateFrom, dateTo])

  const { sorted, sortKey, sortDir, onSort } = useSortable(filtered, 'created_at', 'desc')

  const totalPages = Math.ceil(total / PAGE_SIZE)

  const handleCreate = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!validateAll(form)) return
    setError('')
    setCreating(true)
    try {
      const created = await api.createCustomer(form)
      setShowCreate(false)
      setForm({ external_id: '', display_name: '', email: '' })
      clearErrors()
      toast.success(`Customer "${created.display_name}" created`)
      loadCustomers()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create customer')
    } finally {
      setCreating(false)
    }
  }

  return (
    <Layout>
      <Breadcrumbs items={[{ label: 'Billing' }, { label: 'Customers' }]} />
      <div className="flex justify-between items-center">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900 dark:text-gray-100">Customers</h1>
          <p className="text-sm text-gray-600 dark:text-gray-400 mt-1">{total} total</p>
        </div>
        <div className="flex items-center gap-2">
          {total > 0 && (
            <button
              onClick={() => {
                // Export fetches all data for CSV
                api.listCustomers().then(res => {
                  const rows = res.data.map(c => [
                    c.display_name,
                    c.external_id,
                    c.email || '',
                    c.status,
                    formatDateTime(c.created_at),
                  ])
                  downloadCSV('customers.csv', ['Name', 'External ID', 'Email', 'Status', 'Created'], rows)
                })
              }}
              className="flex items-center gap-2 px-4 py-2 border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-300 rounded-lg text-sm font-medium hover:bg-gray-50 dark:hover:bg-gray-800 shadow-sm transition-colors"
            >
              <Download size={16} />
              Export CSV
            </button>
          )}
          {total > 0 && (
            <button
              onClick={() => setShowCreate(true)}
              className="flex items-center gap-2 px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow transition-colors"
            >
              <Plus size={16} />
              Add Customer
            </button>
          )}
        </div>
      </div>

      {/* Filters + search */}
      {total > 0 && (
        <div className="flex items-center gap-3 mt-6">
          <div className="flex gap-1 bg-gray-100 dark:bg-gray-800 rounded-lg p-1">
            {[
              { value: '', label: 'All' },
              { value: 'active', label: 'Active' },
              { value: 'archived', label: 'Archived' },
            ].map(f => (
              <button key={f.value} onClick={() => { setFilterStatus(f.value); setPage(1) }}
                className={`px-3 py-1.5 rounded-md text-xs font-medium transition-colors ${
                  filterStatus === f.value ? 'bg-white dark:bg-gray-700 text-gray-900 dark:text-gray-100 shadow-sm' : 'text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-200'
                }`}>
                {f.label}
              </button>
            ))}
          </div>
          <div className="relative flex-1">
            <Search size={16} className="absolute left-3 top-2.5 text-gray-400" />
            <input
              type="text"
              value={search}
              onChange={e => setSearch(e.target.value)}
              placeholder="Search within page..."
              className="w-full pl-9 pr-4 py-2 border border-gray-200 dark:border-gray-700 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-velox-500 focus:border-transparent bg-white dark:bg-gray-800 dark:text-gray-100"
            />
          </div>
        </div>
      )}

      {/* Date range filter */}
      {total > 0 && (
        <div className="flex items-center gap-3 mt-3">
          <DatePicker value={dateFrom} onChange={v => { setDateFrom(v) }} placeholder="From" clearable />
          <DatePicker value={dateTo} onChange={v => { setDateTo(v) }} placeholder="To" clearable />
          {(search || dateFrom || dateTo) && (
            <span className="text-xs text-gray-400">Filtering within current page</span>
          )}
        </div>
      )}

      <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card mt-4">
        {loadError ? (
          <ErrorState message={loadError} onRetry={loadCustomers} />
        ) : loading ? (
          <LoadingSkeleton rows={6} columns={5} />
        ) : total === 0 ? (
          <div className="p-12 text-center">
            <p className="text-sm text-gray-900 dark:text-gray-100">No customers yet</p>
            <p className="text-sm text-gray-600 dark:text-gray-400 mt-1">Add your first customer to start billing</p>
            <button onClick={() => setShowCreate(true)}
              className="mt-4 inline-flex items-center gap-2 px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm transition-colors">
              <Plus size={16} /> Add Customer
            </button>
          </div>
        ) : sorted.length === 0 ? (
          <p className="px-6 py-8 text-sm text-gray-400 text-center">No customers match filters on this page</p>
        ) : (
          <>
          <div className="overflow-x-auto">
          <table className="w-full">
            <thead>
              <tr className="border-b border-gray-100 dark:border-gray-800 bg-gray-50 dark:bg-gray-800/50">
                <SortableHeader label="Name" sortKey="display_name" activeSortKey={sortKey} sortDir={sortDir} onSort={onSort} />
                <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">External ID</th>
                <SortableHeader label="Email" sortKey="email" activeSortKey={sortKey} sortDir={sortDir} onSort={onSort} />
                <SortableHeader label="Status" sortKey="status" activeSortKey={sortKey} sortDir={sortDir} onSort={onSort} />
                <SortableHeader label="Created" sortKey="created_at" activeSortKey={sortKey} sortDir={sortDir} onSort={onSort} />
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
              {sorted.map(c => (
                <tr key={c.id} className="hover:bg-gray-50 dark:hover:bg-gray-800/50 cursor-pointer transition-colors group" onClick={(e) => {
                  const target = e.target as HTMLElement
                  if (target.closest('button, a, input, select')) return
                  navigate(`/customers/${c.id}`)
                }}>
                  <td className="px-6 py-3">
                    <Link to={`/customers/${c.id}`} className="text-sm font-medium text-gray-900 dark:text-gray-100 group-hover:text-velox-600 transition-colors">
                      {c.display_name}
                    </Link>
                  </td>
                  <td className="px-6 py-3 text-sm text-gray-600 dark:text-gray-400 font-mono">{c.external_id}</td>
                  <td className="px-6 py-3 text-sm text-gray-600 dark:text-gray-400">{c.email || '\u2014'}</td>
                  <td className="px-6 py-3"><Badge status={c.status} /></td>
                  <td className="px-6 py-3 text-sm text-gray-600 dark:text-gray-400">{formatDateTime(c.created_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
          </div>
          <Pagination page={page} totalPages={totalPages} onPageChange={setPage} pageSize={PAGE_SIZE} total={total} />
          </>
        )}
      </div>

      <Modal open={showCreate} onClose={() => setShowCreate(false)} title="Create Customer">
        <form onSubmit={handleCreate} noValidate className="space-y-4">
          <FormField label="Display Name" required value={form.display_name} placeholder="Acme Corporation" maxLength={255}
            ref={registerRef('display_name')} error={fieldError('display_name')}
            onChange={e => setForm(f => ({ ...f, display_name: e.target.value }))}
            onBlur={() => onBlur('display_name', form.display_name)} />
          <FormField label="External ID" required value={form.external_id} placeholder="acme_corp" maxLength={100} mono
            ref={registerRef('external_id')} error={fieldError('external_id')}
            onChange={e => setForm(f => ({ ...f, external_id: e.target.value }))}
            onBlur={() => onBlur('external_id', form.external_id)}
            hint="Only letters, numbers, hyphens, and underscores" />
          <FormField label="Email" type="email" value={form.email} placeholder="billing@acme.com" maxLength={254}
            ref={registerRef('email')} error={fieldError('email')}
            onChange={e => setForm(f => ({ ...f, email: e.target.value }))}
            onBlur={() => onBlur('email', form.email)} />
          {error && <p className="text-sm text-red-600 bg-red-50 border border-red-200 rounded-lg px-3 py-2">{error}</p>}
          <div className="flex justify-end gap-3 pt-4 border-t border-gray-100 dark:border-gray-800 mt-2">
            <button type="button" onClick={() => setShowCreate(false)} className="px-4 py-2 border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-300 rounded-lg text-sm font-medium hover:bg-gray-50 dark:hover:bg-gray-800 transition-colors">Cancel</button>
            <button type="submit" disabled={creating}
              className="flex items-center justify-center gap-2 px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow disabled:opacity-50">
              {creating ? (<><Loader2 size={14} className="animate-spin" /> Creating...</>) : 'Create Customer'}
            </button>
          </div>
        </form>
      </Modal>
    </Layout>
  )
}
