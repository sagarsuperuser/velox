import { useEffect, useState, useMemo } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { api, formatDate, type Customer } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'
import { Modal } from '@/components/Modal'
import { FormField } from '@/components/FormField'
import { ErrorState } from '@/components/ErrorState'
import { useToast } from '@/components/Toast'
import { useFormValidation, rules } from '@/hooks/useFormValidation'
import { Plus, Search, Download } from 'lucide-react'
import { downloadCSV } from '@/lib/csv'
import { Pagination } from '@/components/Pagination'

export function CustomersPage() {
  const [customers, setCustomers] = useState<Customer[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [search, setSearch] = useState('')
  const [showCreate, setShowCreate] = useState(false)
  const [creating, setCreating] = useState(false)
  const [form, setForm] = useState({ external_id: '', display_name: '', email: '' })
  const [error, setError] = useState('')
  const [page, setPage] = useState(1)
  const pageSize = 25
  const toast = useToast()
  const navigate = useNavigate()

  const fieldRules = useMemo(() => ({
    display_name: [rules.required('Display name')],
    external_id: [rules.required('External ID'), rules.slug()],
    email: [rules.email()],
  }), [])
  const { onBlur, validateAll, fieldError, registerRef, clearErrors } = useFormValidation(fieldRules)

  const loadCustomers = () => {
    setLoading(true)
    setLoadError(null)
    api.listCustomers().then(res => {
      setCustomers(res.data)
      setTotal(res.total)
      setLoading(false)
    }).catch(err => { setLoadError(err instanceof Error ? err.message : 'Failed to load customers'); setLoading(false) })
  }

  useEffect(() => { loadCustomers() }, [])

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
      <div className="flex justify-between items-center">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900">Customers</h1>
          <p className="text-sm text-gray-500 mt-1">{total} total</p>
        </div>
        <div className="flex items-center gap-2">
          {customers.length > 0 && (
            <button
              onClick={() => {
                const rows = customers.map(c => [
                  c.display_name,
                  c.external_id,
                  c.email || '',
                  c.status,
                  formatDate(c.created_at),
                ])
                downloadCSV('customers.csv', ['Name', 'External ID', 'Email', 'Status', 'Created'], rows)
              }}
              className="flex items-center gap-2 px-4 py-2 border border-gray-300 text-gray-700 rounded-lg text-sm font-medium hover:bg-gray-50 shadow-sm transition-colors"
            >
              <Download size={16} />
              Export CSV
            </button>
          )}
          <button
            onClick={() => setShowCreate(true)}
            className="flex items-center gap-2 px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow transition-colors"
          >
            <Plus size={16} />
            Add Customer
          </button>
        </div>
      </div>

      {/* Search */}
      {customers.length > 0 && (
        <div className="relative mt-6">
          <Search size={16} className="absolute left-3 top-2.5 text-gray-400" />
          <input
            type="text"
            value={search}
            onChange={e => setSearch(e.target.value)}
            placeholder="Search customers..."
            className="w-full pl-9 pr-4 py-2 border border-gray-200 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-velox-500 focus:border-transparent bg-white"
          />
        </div>
      )}

      <div className="bg-white rounded-xl shadow-card mt-4">
        {loadError ? (
          <ErrorState message={loadError} onRetry={loadCustomers} />
        ) : loading ? (
          <div className="p-8 text-gray-400 animate-pulse">Loading...</div>
        ) : customers.length === 0 ? (
          <div className="p-12 text-center">
            <p className="text-gray-400 text-sm">No customers yet</p>
            <button onClick={() => setShowCreate(true)} className="mt-3 text-sm text-velox-600 hover:underline">
              Create your first customer
            </button>
          </div>
        ) : (
          (() => {
            const filtered = customers.filter(c => {
              if (!search) return true
              const q = search.toLowerCase()
              return c.display_name.toLowerCase().includes(q) ||
                c.external_id.toLowerCase().includes(q) ||
                (c.email && c.email.toLowerCase().includes(q))
            })
            const totalPages = Math.ceil(filtered.length / pageSize)
            const currentPage = Math.min(page, totalPages || 1)
            const paginated = filtered.slice((currentPage - 1) * pageSize, currentPage * pageSize)
            return filtered.length === 0 ? (
              <p className="px-6 py-8 text-sm text-gray-400 text-center">No customers match &lsquo;{search}&rsquo;</p>
            ) : (
              <>
              <div className="overflow-x-auto">
              <table className="w-full">
                <thead>
                  <tr className="border-b border-gray-100">
                    <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Name</th>
                    <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">External ID</th>
                    <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Email</th>
                    <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Status</th>
                    <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Created</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-gray-50">
                  {paginated.map(c => (
                    <tr key={c.id} className="hover:bg-gray-50 cursor-pointer transition-colors group" onClick={(e) => {
                      const target = e.target as HTMLElement
                      if (target.closest('button, a, input, select')) return
                      navigate(`/customers/${c.id}`)
                    }}>
                      <td className="px-6 py-3">
                        <Link to={`/customers/${c.id}`} className="text-sm font-medium text-gray-900 group-hover:text-velox-600 transition-colors">
                          {c.display_name}
                        </Link>
                      </td>
                      <td className="px-6 py-3 text-sm text-gray-500 font-mono">{c.external_id}</td>
                      <td className="px-6 py-3 text-sm text-gray-500">{c.email || '\u2014'}</td>
                      <td className="px-6 py-3"><Badge status={c.status} /></td>
                      <td className="px-6 py-3 text-sm text-gray-400">{formatDate(c.created_at)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
              </div>
              <Pagination page={currentPage} totalPages={totalPages} onPageChange={setPage} />
              </>
            )
          })()
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
          <div className="flex justify-end gap-3 pt-4 border-t border-gray-100 mt-1">
            <button type="button" onClick={() => setShowCreate(false)} className="px-4 py-2 border border-gray-300 text-gray-700 rounded-lg text-sm font-medium hover:bg-gray-50 transition-colors">Cancel</button>
            <button type="submit" disabled={creating}
              className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow disabled:opacity-50">
              {creating ? 'Creating...' : 'Create Customer'}
            </button>
          </div>
        </form>
      </Modal>
    </Layout>
  )
}
