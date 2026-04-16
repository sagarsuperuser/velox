import { useState, useMemo } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { toast } from 'sonner'
import { api, formatDateTime } from '@/lib/api'
import type { Customer } from '@/lib/api'
import { downloadCSV } from '@/lib/csv'
import { Layout } from '@/components/Layout'
import { useSortable } from '@/hooks/useSortable'
import { cn } from '@/lib/utils'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
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

import { Plus, Search, Download, Loader2, ArrowUpDown, ArrowUp, ArrowDown } from 'lucide-react'

const createCustomerSchema = z.object({
  external_id: z.string().min(1, 'External ID is required').regex(/^[a-zA-Z0-9_\-]+$/, 'Only letters, numbers, hyphens, and underscores allowed'),
  display_name: z.string().min(1, 'Display name is required'),
  email: z.string().email('Invalid email address').optional().or(z.literal('')),
})

type CreateCustomerData = z.infer<typeof createCustomerSchema>

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

function statusVariant(status: string): 'default' | 'secondary' | 'destructive' | 'outline' {
  switch (status) {
    case 'active': return 'default'
    case 'archived': return 'secondary'
    default: return 'outline'
  }
}

export default function CustomersPage() {
  const [search, setSearch] = useState('')
  const [showCreate, setShowCreate] = useState(false)
  const [error, setError] = useState('')
  const [filterStatus, setFilterStatus] = useState('')
  const [page, setPage] = useState(1)
  const navigate = useNavigate()
  const queryClient = useQueryClient()

  const form = useForm<CreateCustomerData>({
    resolver: zodResolver(createCustomerSchema),
    defaultValues: { external_id: '', display_name: '', email: '' },
  })

  // Server-side paginated fetch
  const queryParams = useMemo(() => {
    const params = new URLSearchParams()
    params.set('limit', String(PAGE_SIZE))
    params.set('offset', String((page - 1) * PAGE_SIZE))
    if (filterStatus) params.set('status', filterStatus)
    return params.toString()
  }, [page, filterStatus])

  const { data: customersData, isLoading: loading, error: loadErrorObj, refetch } = useQuery({
    queryKey: ['customers', page, filterStatus],
    queryFn: () => api.listCustomers(queryParams),
  })

  const customers = customersData?.data ?? []
  const total = customersData?.total ?? 0
  const loadError = loadErrorObj instanceof Error ? loadErrorObj.message : loadErrorObj ? String(loadErrorObj) : null

  const createMutation = useMutation({
    mutationFn: (data: CreateCustomerData) => api.createCustomer(data),
    onSuccess: (created) => {
      queryClient.invalidateQueries({ queryKey: ['customers'] })
      toast.success(`Customer "${created.display_name}" created`)
      setShowCreate(false)
      form.reset()
    },
    onError: (err) => {
      setError(err instanceof Error ? err.message : 'Failed to create customer')
    },
  })

  // Client-side search filter on current page data
  const filtered = useMemo(() => {
    return customers.filter((c: Customer) => {
      if (search) {
        const q = search.toLowerCase()
        if (!c.display_name.toLowerCase().includes(q) &&
          !c.external_id.toLowerCase().includes(q) &&
          !(c.email && c.email.toLowerCase().includes(q))) return false
      }
      return true
    })
  }, [customers, search])

  const { sorted, sortKey, sortDir, onSort } = useSortable(filtered, 'created_at', 'desc')

  const totalPages = Math.ceil(total / PAGE_SIZE)

  const onSubmit = form.handleSubmit((data: CreateCustomerData) => {
    setError('')
    createMutation.mutate(data)
  })

  const handleExport = () => {
    api.listCustomers().then(res => {
      const rows = res.data.map((c: Customer) => [
        c.display_name,
        c.external_id,
        c.email || '',
        c.status,
        formatDateTime(c.created_at),
      ])
      downloadCSV('customers.csv', ['Name', 'External ID', 'Email', 'Status', 'Created'], rows)
    })
  }

  return (
    <Layout>
      {/* Page header */}
      <div className="flex justify-between items-center">
        <div>
          <h1 className="text-2xl font-semibold text-foreground">Customers</h1>
          <p className="text-sm text-muted-foreground mt-1">{total} total</p>
        </div>
        <div className="flex items-center gap-2">
          {total > 0 && (
            <Button variant="outline" size="sm" onClick={handleExport}>
              <Download size={16} className="mr-2" />
              Export CSV
            </Button>
          )}
          {total > 0 && (
            <Button size="sm" onClick={() => setShowCreate(true)}>
              <Plus size={16} className="mr-2" />
              Add Customer
            </Button>
          )}
        </div>
      </div>

      {/* Filters */}
      {total > 0 && (
        <div className="flex items-center gap-3 mt-6">
          <div className="flex gap-1 bg-muted rounded-lg p-1">
            {[
              { value: '', label: 'All' },
              { value: 'active', label: 'Active' },
              { value: 'archived', label: 'Archived' },
            ].map(f => (
              <button
                key={f.value}
                onClick={() => { setFilterStatus(f.value); setPage(1) }}
                className={cn(
                  'px-3 py-1.5 rounded-md text-xs font-medium transition-colors',
                  filterStatus === f.value
                    ? 'bg-background text-foreground shadow-sm'
                    : 'text-muted-foreground hover:text-foreground'
                )}
              >
                {f.label}
              </button>
            ))}
          </div>
          <div className="relative flex-1">
            <Search size={16} className="absolute left-3 top-2.5 text-muted-foreground" />
            <Input
              value={search}
              onChange={e => setSearch(e.target.value)}
              placeholder="Search within page..."
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
              <Button variant="outline" size="sm" onClick={() => refetch()}>
                Retry
              </Button>
            </div>
          ) : loading ? (
            <div className="p-8 flex justify-center">
              <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
            </div>
          ) : total === 0 ? (
            <div className="p-12 text-center">
              <p className="text-sm font-medium text-foreground">No customers yet</p>
              <p className="text-sm text-muted-foreground mt-1">Add your first customer to start billing</p>
              <Button size="sm" className="mt-4" onClick={() => setShowCreate(true)}>
                <Plus size={16} className="mr-2" />
                Add Customer
              </Button>
            </div>
          ) : sorted.length === 0 ? (
            <p className="px-6 py-8 text-sm text-muted-foreground text-center">
              No customers match filters on this page
            </p>
          ) : (
            <>
              <Table>
                <TableHeader>
                  <TableRow>
                    <SortableHead label="Name" sortKey="display_name" activeSortKey={sortKey} sortDir={sortDir} onSort={onSort} />
                    <TableHead className="text-xs font-medium">External ID</TableHead>
                    <SortableHead label="Email" sortKey="email" activeSortKey={sortKey} sortDir={sortDir} onSort={onSort} />
                    <SortableHead label="Status" sortKey="status" activeSortKey={sortKey} sortDir={sortDir} onSort={onSort} />
                    <SortableHead label="Created" sortKey="created_at" activeSortKey={sortKey} sortDir={sortDir} onSort={onSort} />
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {sorted.map((c: Customer) => (
                    <TableRow
                      key={c.id}
                      className="cursor-pointer"
                      onClick={(e) => {
                        const target = e.target as HTMLElement
                        if (target.closest('button, a, input, select')) return
                        navigate(`/customers/${c.id}`)
                      }}
                    >
                      <TableCell>
                        <Link
                          to={`/customers/${c.id}`}
                          className="text-sm font-medium text-foreground hover:text-primary transition-colors"
                        >
                          {c.display_name}
                        </Link>
                      </TableCell>
                      <TableCell className="text-sm text-muted-foreground font-mono">
                        {c.external_id}
                      </TableCell>
                      <TableCell className="text-sm text-muted-foreground">
                        {c.email || '\u2014'}
                      </TableCell>
                      <TableCell>
                        <Badge variant={statusVariant(c.status)}>{c.status}</Badge>
                      </TableCell>
                      <TableCell className="text-sm text-muted-foreground">
                        {formatDateTime(c.created_at)}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>

              {/* Pagination */}
              {totalPages > 1 && (
                <div className="border-t border-border px-4 py-3 flex items-center justify-between">
                  <p className="text-xs text-muted-foreground">
                    Showing {(page - 1) * PAGE_SIZE + 1}
                    {'\u2013'}
                    {Math.min(page * PAGE_SIZE, total)} of {total}
                  </p>
                  <Pagination>
                    <PaginationContent>
                      <PaginationItem>
                        <PaginationPrevious
                          onClick={() => setPage(p => Math.max(1, p - 1))}
                          className={cn(page <= 1 && 'pointer-events-none opacity-50')}
                        />
                      </PaginationItem>
                      {Array.from({ length: Math.min(totalPages, 5) }, (_, i) => {
                        let pageNum: number
                        if (totalPages <= 5) {
                          pageNum = i + 1
                        } else if (page <= 3) {
                          pageNum = i + 1
                        } else if (page >= totalPages - 2) {
                          pageNum = totalPages - 4 + i
                        } else {
                          pageNum = page - 2 + i
                        }
                        return (
                          <PaginationItem key={pageNum}>
                            <PaginationLink
                              onClick={() => setPage(pageNum)}
                              isActive={page === pageNum}
                            >
                              {pageNum}
                            </PaginationLink>
                          </PaginationItem>
                        )
                      })}
                      <PaginationItem>
                        <PaginationNext
                          onClick={() => setPage(p => Math.min(totalPages, p + 1))}
                          className={cn(page >= totalPages && 'pointer-events-none opacity-50')}
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

      {/* Create Customer Dialog */}
      <Dialog open={showCreate} onOpenChange={(open) => {
        setShowCreate(open)
        if (!open) { form.reset(); setError('') }
      }}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>Create Customer</DialogTitle>
            <DialogDescription>
              Add a new customer to start billing. The external ID is your unique identifier.
            </DialogDescription>
          </DialogHeader>
          <Form {...form}>
            <form onSubmit={onSubmit} noValidate className="space-y-4">
              <FormField
                control={form.control}
                name="display_name"
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>Display Name</FormLabel>
                    <FormControl>
                      <Input placeholder="Acme Corporation" maxLength={255} {...field} />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name="external_id"
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>External ID</FormLabel>
                    <FormControl>
                      <Input placeholder="acme_corp" maxLength={100} className="font-mono" {...field} />
                    </FormControl>
                    <FormDescription>
                      Only letters, numbers, hyphens, and underscores
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name="email"
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>Email</FormLabel>
                    <FormControl>
                      <Input type="email" placeholder="billing@acme.com" maxLength={254} {...field} />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />

              {error && (
                <div className="px-3 py-2 rounded-md bg-destructive/10 border border-destructive/20">
                  <p className="text-destructive text-sm">{error}</p>
                </div>
              )}

              <DialogFooter>
                <Button type="button" variant="outline" onClick={() => setShowCreate(false)}>
                  Cancel
                </Button>
                <Button type="submit" disabled={createMutation.isPending}>
                  {createMutation.isPending ? (
                    <>
                      <Loader2 size={14} className="animate-spin mr-2" />
                      Creating...
                    </>
                  ) : (
                    'Create Customer'
                  )}
                </Button>
              </DialogFooter>
            </form>
          </Form>
        </DialogContent>
      </Dialog>
    </Layout>
  )
}
