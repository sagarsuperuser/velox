import { useState, useMemo } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { toast } from 'sonner'
import { api, formatDate, formatDateTime } from '@/lib/api'
import type { Customer, Subscription } from '@/lib/api'
import { applyApiError } from '@/lib/formErrors'
import { downloadCSV } from '@/lib/csv'
import { Layout } from '@/components/Layout'
import { useSortable, type SortDir } from '@/hooks/useSortable'
import { useUrlState } from '@/hooks/useUrlState'
import { cn } from '@/lib/utils'
import { statusBadgeVariant, statusBorderColor } from '@/lib/status'
import { InitialsAvatar } from '@/components/InitialsAvatar'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent } from '@/components/ui/card'
import { Checkbox } from '@/components/ui/checkbox'
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
import { Separator } from '@/components/ui/separator'
import { TableSkeleton } from '@/components/ui/TableSkeleton'

import { Plus, Search, Download, Loader2, ArrowUpDown, ArrowUp, ArrowDown, Repeat } from 'lucide-react'
import { EmptyState } from '@/components/EmptyState'
import { ExpiryBadge } from '@/components/ExpiryBadge'

const createSubSchema = z.object({
  code: z.string().min(1, 'Code is required').regex(/^[a-zA-Z0-9_\-]+$/, 'Only letters, numbers, hyphens, and underscores'),
  display_name: z.string().min(1, 'Display name is required'),
  customer_id: z.string().min(1, 'Customer is required'),
  plan_id: z.string().min(1, 'Plan is required'),
  start_now: z.boolean(),
  billing_time: z.string(),
  trial_days: z.string(),
  usage_cap_units: z.string(),
  overage_action: z.string(),
})

type CreateSubData = z.infer<typeof createSubSchema>

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

export default function SubscriptionsPage() {
  const [showCreate, setShowCreate] = useState(false)
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
  const navigate = useNavigate()
  const queryClient = useQueryClient()

  const form = useForm<CreateSubData>({
    resolver: zodResolver(createSubSchema),
    defaultValues: {
      code: '', display_name: '', customer_id: '', plan_id: '', start_now: true,
      billing_time: 'calendar', trial_days: '',
      usage_cap_units: '', overage_action: 'charge',
    },
  })

  const queryParams = useMemo(() => {
    const params = new URLSearchParams()
    params.set('limit', String(PAGE_SIZE))
    params.set('offset', String((page - 1) * PAGE_SIZE))
    if (filterStatus) params.set('status', filterStatus)
    return params.toString()
  }, [page, filterStatus])

  const { data: subsData, isLoading: loading, error: loadErrorObj, refetch } = useQuery({
    queryKey: ['subscriptions', page, filterStatus],
    queryFn: () => api.listSubscriptions(queryParams),
  })

  const { data: customersData } = useQuery({
    queryKey: ['customers-ref'],
    queryFn: () => api.listCustomers(),
  })

  const { data: plansData } = useQuery({
    queryKey: ['plans-ref'],
    queryFn: () => api.listPlans(),
  })

  const subs = subsData?.data ?? []
  const total = subsData?.total ?? 0
  const loadError = loadErrorObj instanceof Error ? loadErrorObj.message : loadErrorObj ? String(loadErrorObj) : null
  const customers = customersData?.data ?? []
  const customerMap = useMemo(() => {
    const cMap: Record<string, Customer> = {}
    customers.forEach(c => { cMap[c.id] = c })
    return cMap
  }, [customers])
  const plans = plansData?.data ?? []

  const createMutation = useMutation({
    mutationFn: (data: CreateSubData) => api.createSubscription({
      code: data.code,
      display_name: data.display_name,
      customer_id: data.customer_id,
      plan_id: data.plan_id,
      start_now: data.start_now,
      billing_time: data.billing_time,
      ...(data.trial_days ? { trial_days: parseInt(data.trial_days) } : {}),
      ...(data.usage_cap_units ? { usage_cap_units: parseInt(data.usage_cap_units) } : {}),
      ...(data.overage_action !== 'charge' ? { overage_action: data.overage_action } : {}),
    }),
    onSuccess: (created) => {
      queryClient.invalidateQueries({ queryKey: ['subscriptions'] })
      toast.success(`Subscription "${created.display_name}" created`)
      setShowCreate(false)
      form.reset()
    },
    onError: (err) => {
      applyApiError(form, err, ['code', 'display_name', 'customer_id', 'plan_id', 'billing_time', 'trial_days', 'usage_cap_units', 'overage_action'])
    },
  })

  // Client-side search on current page data
  const filtered = useMemo(() => {
    return subs.filter((sub: Subscription) => {
      if (!search) return true
      const q = search.toLowerCase()
      return sub.display_name.toLowerCase().includes(q) || sub.code.toLowerCase().includes(q)
    })
  }, [subs, search])

  const { sorted, onSort } = useSortable(
    filtered,
    sortKey,
    sortDir,
    (key, dir) => setUrlState({ sort: key, dir }),
  )

  const totalPages = Math.ceil(total / PAGE_SIZE)

  const onSubmit = form.handleSubmit((data: CreateSubData) => {
    createMutation.mutate(data)
  })

  const handleExport = () => {
    api.listSubscriptions().then(res => {
      const rows = res.data.map((sub: Subscription) => [
        sub.display_name,
        customerMap[sub.customer_id]?.display_name || 'Unknown',
        sub.code,
        sub.status,
        sub.current_billing_period_start && sub.current_billing_period_end
          ? `${formatDate(sub.current_billing_period_start)} - ${formatDate(sub.current_billing_period_end)}`
          : '',
        formatDateTime(sub.created_at),
      ])
      downloadCSV('subscriptions.csv', ['Name', 'Customer', 'Code', 'Status', 'Billing Period', 'Created'], rows)
    })
  }

  return (
    <Layout>
      {/* Page header */}
      <div className="flex justify-between items-center">
        <div>
          <h1 className="text-2xl font-semibold text-foreground">Subscriptions</h1>
          <p className="text-sm text-muted-foreground mt-1">Manage subscriptions and billing cycles{total > 0 ? ` · ${total} total` : ''}</p>
        </div>
        <div className="flex items-center gap-2">
          {total > 0 && (
            <Button variant="outline" size="sm" onClick={handleExport}>
              <Download size={16} className="mr-2" />
              Export CSV
            </Button>
          )}
          {(total > 0 || filterStatus) && (
            <div className="flex gap-1 bg-muted rounded-lg p-1">
              {[
                { value: '', label: 'All' },
                { value: 'active', label: 'Active' },
                { value: 'paused', label: 'Paused' },
                { value: 'canceled', label: 'Canceled' },
                { value: 'draft', label: 'Draft' },
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
                </button>
              ))}
            </div>
          )}
          <Button size="sm" onClick={() => setShowCreate(true)}>
            <Plus size={16} className="mr-2" />
            Add Subscription
          </Button>
        </div>
      </div>

      {/* Search */}
      {total > 0 && (
        <div className="flex items-center gap-3 mt-6">
          <div className="relative flex-1">
            <Search size={16} className="absolute left-3 top-2.5 text-muted-foreground" />
            <Input
              value={search}
              onChange={e => setUrlState({ search: e.target.value })}
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
            <TableSkeleton columns={6} />
          ) : total === 0 ? (
            filterStatus ? (
              <EmptyState
                title={`No ${filterStatus} subscriptions`}
                description="Try a different filter to see more results."
                action={{
                  label: 'Clear filter',
                  variant: 'outline',
                  onClick: () => setUrlState({ status: '', page: '1' }),
                }}
              />
            ) : (
              <EmptyState
                icon={Repeat}
                title="No subscriptions yet"
                description="Create a subscription to start billing a customer."
                action={{
                  label: 'Add Subscription',
                  icon: Plus,
                  onClick: () => setShowCreate(true),
                }}
              />
            )
          ) : sorted.length === 0 ? (
            <p className="px-6 py-8 text-sm text-muted-foreground text-center">
              No subscriptions match search on this page
            </p>
          ) : (
            <>
              <Table>
                <TableHeader>
                  <TableRow>
                    <SortableHead label="Name" sortKey="display_name" activeSortKey={sortKey} sortDir={sortDir} onSort={onSort} />
                    <TableHead className="text-xs font-medium">Customer</TableHead>
                    <TableHead className="text-xs font-medium">Plan</TableHead>
                    <TableHead className="text-xs font-medium">Code</TableHead>
                    <SortableHead label="Status" sortKey="status" activeSortKey={sortKey} sortDir={sortDir} onSort={onSort} />
                    <SortableHead label="Next Billing" sortKey="next_billing_at" activeSortKey={sortKey} sortDir={sortDir} onSort={onSort} />
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {sorted.map((sub: Subscription) => (
                    <TableRow
                      key={sub.id}
                      className={cn('cursor-pointer hover:bg-muted/50 transition-colors border-l-[3px]', statusBorderColor(sub.status))}
                      onClick={(e) => {
                        const target = e.target as HTMLElement
                        if (target.closest('button, a, input, select')) return
                        navigate(`/subscriptions/${sub.id}`)
                      }}
                    >
                      <TableCell>
                        <Link
                          to={`/subscriptions/${sub.id}`}
                          className="text-sm font-medium text-foreground hover:text-primary transition-colors truncate block max-w-[180px]"
                          title={sub.display_name}
                        >
                          {sub.display_name}
                        </Link>
                      </TableCell>
                      <TableCell className="text-sm">
                        <div className="flex items-center gap-2.5">
                          <InitialsAvatar name={customerMap[sub.customer_id]?.display_name || 'Unknown'} size="xs" />
                          <Link
                            to={`/customers/${sub.customer_id}`}
                            onClick={e => e.stopPropagation()}
                            className="text-sm font-medium text-foreground hover:text-primary truncate block max-w-[160px]"
                            title={customerMap[sub.customer_id]?.display_name || 'Unknown'}
                          >
                            {customerMap[sub.customer_id]?.display_name || 'Unknown'}
                          </Link>
                        </div>
                      </TableCell>
                      <TableCell className="text-sm text-muted-foreground">
                        {plans.find(p => p.id === sub.plan_id)?.name || '\u2014'}
                      </TableCell>
                      <TableCell className="text-sm text-muted-foreground font-mono truncate max-w-[120px]" title={sub.code}>
                        {sub.code}
                      </TableCell>
                      <TableCell>
                        <div className="flex items-center gap-2">
                          <Badge variant={statusBadgeVariant(sub.status)}>{sub.status}</Badge>
                          <ExpiryBadge expiresAt={sub.trial_end_at} label="Trial ends" warningDays={3} />
                        </div>
                      </TableCell>
                      <TableCell className="text-sm text-muted-foreground">
                        {sub.next_billing_at ? formatDate(sub.next_billing_at) : '\u2014'}
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
                          onClick={() => setUrlState({ page: String(Math.max(1, page - 1)) })}
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
                              onClick={() => setUrlState({ page: String(pageNum) })}
                              isActive={page === pageNum}
                            >
                              {pageNum}
                            </PaginationLink>
                          </PaginationItem>
                        )
                      })}
                      <PaginationItem>
                        <PaginationNext
                          onClick={() => setUrlState({ page: String(Math.min(totalPages, page + 1)) })}
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

      {/* Create Subscription Dialog */}
      <Dialog open={showCreate} onOpenChange={(open) => {
        setShowCreate(open)
        if (!open) { form.reset() }
      }}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>Create Subscription</DialogTitle>
            <DialogDescription>
              Add a new subscription to start billing a customer.
            </DialogDescription>
          </DialogHeader>
          <Form {...form}>
            <form onSubmit={onSubmit} noValidate className="space-y-5">
              {/* Basic info */}
              <div className="grid grid-cols-2 gap-4">
                <FormField
                  control={form.control}
                  name="display_name"
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>Display Name</FormLabel>
                      <FormControl>
                        <Input placeholder="Acme Pro Monthly" maxLength={255} {...field} />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                <FormField
                  control={form.control}
                  name="code"
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>Code</FormLabel>
                      <FormControl>
                        <Input placeholder="acme-pro" maxLength={100} className="font-mono" {...field} />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              </div>

              <div className="grid grid-cols-2 gap-4">
                <FormField
                  control={form.control}
                  name="customer_id"
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>Customer</FormLabel>
                      <FormControl>
                        <select
                          value={field.value}
                          onChange={field.onChange}
                          className="flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-xs transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                        >
                          <option value="">Select customer...</option>
                          {customers.map(c => (
                            <option key={c.id} value={c.id}>{c.display_name}</option>
                          ))}
                        </select>
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                <FormField
                  control={form.control}
                  name="plan_id"
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>Plan</FormLabel>
                      <FormControl>
                        <select
                          value={field.value}
                          onChange={field.onChange}
                          className="flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-xs transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                        >
                          <option value="">Select plan...</option>
                          {plans.map(p => (
                            <option key={p.id} value={p.id}>{p.name}</option>
                          ))}
                        </select>
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              </div>

              {/* Billing config */}
              <Separator />
              <div className="grid grid-cols-2 gap-4">
                <FormField
                  control={form.control}
                  name="billing_time"
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>Billing Cycle</FormLabel>
                      <FormControl>
                        <select
                          value={field.value}
                          onChange={field.onChange}
                          className="flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-xs transition-colors focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                        >
                          <option value="calendar">Calendar (month start)</option>
                          <option value="anniversary">Anniversary (sub start)</option>
                        </select>
                      </FormControl>
                    </FormItem>
                  )}
                />
                <FormField
                  control={form.control}
                  name="trial_days"
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>Trial Period</FormLabel>
                      <FormControl>
                        <Input type="number" min={0} placeholder="0 days" {...field} />
                      </FormControl>
                    </FormItem>
                  )}
                />
              </div>

              <FormField
                control={form.control}
                name="start_now"
                render={({ field }) => (
                  <FormItem className="flex flex-row items-center gap-2 rounded-md border border-input px-3 py-2.5">
                    <FormControl>
                      <Checkbox
                        checked={field.value}
                        onCheckedChange={field.onChange}
                      />
                    </FormControl>
                    <div>
                      <FormLabel className="text-sm font-medium">Start immediately</FormLabel>
                      <p className="text-xs text-muted-foreground">Activate and set the first billing period now</p>
                    </div>
                  </FormItem>
                )}
              />

              {/* Usage limits */}
              <Separator />
              <div className="grid grid-cols-2 gap-4">
                <FormField
                  control={form.control}
                  name="usage_cap_units"
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>Usage Cap</FormLabel>
                      <FormControl>
                        <Input type="number" min={0} placeholder="Unlimited" {...field} />
                      </FormControl>
                      <FormDescription>Max units per period</FormDescription>
                    </FormItem>
                  )}
                />
                <FormField
                  control={form.control}
                  name="overage_action"
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>Over-limit Action</FormLabel>
                      <FormControl>
                        <select
                          value={field.value}
                          onChange={field.onChange}
                          className="flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-xs transition-colors focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                        >
                          <option value="charge">Charge overage</option>
                          <option value="block">Cap at limit</option>
                        </select>
                      </FormControl>
                    </FormItem>
                  )}
                />
              </div>

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
                    'Create Subscription'
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
