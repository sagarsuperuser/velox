import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { useInfiniteQuery } from '@tanstack/react-query'
import { History, Wand2, ArrowRight, ChevronRight } from 'lucide-react'

import { Layout } from '@/components/Layout'
import { EmptyState } from '@/components/EmptyState'
import { CopyButton } from '@/components/CopyButton'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent } from '@/components/ui/card'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { TableSkeleton } from '@/components/ui/TableSkeleton'

import {
  api,
  formatCents,
  formatDateTime,
  type PlanMigrationListItem,
} from '@/lib/api'

const PAGE_SIZE = 25

// Filter the always-snake_case wire shape into a UI-friendly status label.
// The list endpoint doesn't carry a literal "status" column today — every
// row is a committed migration (preview is in-memory only on the server).
// We surface "committed" as the primary state and reserve "partial" for
// rows where applied_count is zero against a non-empty cohort, which is
// the visible signal for an item-level failure (per service.go's commit
// path: per-customer errors don't abort the row, they only depress
// applied_count). This keeps the filter dropdown honest — "failed" can
// re-enter the surface the moment the backend grows a status column
// without rewriting this UI.
function migrationStatus(m: PlanMigrationListItem): 'committed' | 'partial' {
  // applied_count == 0 with totals present means the cohort resolved but
  // no item swap succeeded — the operator should investigate via the
  // detail page's audit trail.
  if (m.applied_count === 0 && (m.totals?.length ?? 0) > 0) return 'partial'
  return 'committed'
}

function statusLabel(s: 'committed' | 'partial'): string {
  return s === 'committed' ? 'Committed' : 'Partial'
}

function statusVariant(s: 'committed' | 'partial'): 'success' | 'warning' {
  return s === 'committed' ? 'success' : 'warning'
}

// Effective is the schedule mode picked at commit; treated as the row's
// "action_type" for filter purposes since the list endpoint only stores
// committed migrations (preview never persists).
function effectiveLabel(e: string): string {
  if (e === 'immediate') return 'Immediate'
  if (e === 'next_period') return 'Next period'
  return e
}

// Truncate idempotency key for table display while keeping enough prefix
// for visual disambiguation. Full key is copyable via the CopyButton.
function truncateKey(key: string, head = 8, tail = 4): string {
  if (!key) return '—'
  if (key.length <= head + tail + 1) return key
  return `${key.slice(0, head)}…${key.slice(-tail)}`
}

// Cohort delta is reported per-currency by the backend; pick the first
// row for the table (multi-currency cohorts are rare since the service
// rejects cross-currency from→to plans). The detail page surfaces every
// currency line.
function primaryDelta(m: PlanMigrationListItem): { currency: string; delta: number } | null {
  if (!m.totals || m.totals.length === 0) return null
  const t = m.totals[0]
  return { currency: t.currency, delta: t.delta_amount_cents }
}

export default function PlanMigrationsHistoryPage() {
  // Filters live in component state (not URL) for v1. URL-state is on the
  // backlog once the backend adds server-side filter params; today's
  // list endpoint only accepts limit / cursor, so these filters narrow
  // an already-fetched page client-side.
  const [statusFilter, setStatusFilter] = useState<'' | 'committed' | 'partial'>('')
  const [actionFilter, setActionFilter] = useState<'' | 'immediate' | 'next_period'>('')

  // Cursor pagination — the server returns next_cursor; we keep a stack of
  // previous cursors so the operator can step backwards through history
  // without a separate page-index API.
  const {
    data,
    isLoading,
    error,
    isFetchingNextPage,
    fetchNextPage,
    hasNextPage,
    refetch,
  } = useInfiniteQuery({
    queryKey: ['plan-migrations', 'history'],
    queryFn: ({ pageParam }) => {
      const params: { limit: number; cursor?: string } = { limit: PAGE_SIZE }
      if (pageParam) params.cursor = pageParam
      return api.listPlanMigrations(params)
    },
    initialPageParam: '' as string,
    getNextPageParam: (last) => (last.next_cursor ? last.next_cursor : undefined),
  })

  const allMigrations = useMemo(
    () => (data?.pages ?? []).flatMap((p) => p.migrations ?? []),
    [data],
  )

  const filtered = useMemo(() => {
    let rows = allMigrations
    if (statusFilter) {
      rows = rows.filter((m) => migrationStatus(m) === statusFilter)
    }
    if (actionFilter) {
      rows = rows.filter((m) => m.effective === actionFilter)
    }
    return rows
  }, [allMigrations, statusFilter, actionFilter])

  const hasFilters = !!statusFilter || !!actionFilter
  const loadError = error instanceof Error ? error.message : error ? String(error) : null

  const selectClass =
    'flex h-9 rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-xs transition-colors focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring'

  return (
    <Layout>
      {/* Header */}
      <div className="flex justify-between items-center">
        <div>
          <h1 className="text-2xl font-semibold text-foreground flex items-center gap-2">
            <History size={22} />
            Plan migration history
          </h1>
          <p className="text-sm text-muted-foreground mt-1">
            Audit trail of every committed bulk plan swap. Newest first.
          </p>
        </div>
        <Link to="/plan-migrations">
          <Button>
            <Wand2 size={16} className="mr-2" />
            New migration
          </Button>
        </Link>
      </div>

      {/* Filters */}
      <div className="flex flex-wrap items-center gap-3 mt-6">
        <select
          value={statusFilter}
          onChange={(e) => setStatusFilter(e.target.value as '' | 'committed' | 'partial')}
          className={selectClass}
          aria-label="Status filter"
        >
          <option value="">All statuses</option>
          <option value="committed">Committed</option>
          <option value="partial">Partial</option>
        </select>
        <select
          value={actionFilter}
          onChange={(e) => setActionFilter(e.target.value as '' | 'immediate' | 'next_period')}
          className={selectClass}
          aria-label="Action type filter"
        >
          <option value="">All schedules</option>
          <option value="immediate">Immediate</option>
          <option value="next_period">Next period</option>
        </select>
        {hasFilters && (
          <Button
            variant="ghost"
            size="sm"
            onClick={() => {
              setStatusFilter('')
              setActionFilter('')
            }}
          >
            Clear filters
          </Button>
        )}
      </div>

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
          ) : isLoading ? (
            <TableSkeleton columns={7} />
          ) : allMigrations.length === 0 ? (
            <EmptyState
              icon={History}
              title="No plan migrations yet"
              description="When you bulk-swap a cohort of subscribers from one plan to another, every commit lands here with its cohort summary, idempotency key, and audit trail."
              action={{
                label: 'Start a migration',
                to: '/plan-migrations',
                icon: Wand2,
              }}
              secondaryAction={{
                label: 'API reference',
                to: '/docs/api',
                variant: 'outline',
              }}
            />
          ) : filtered.length === 0 ? (
            <div className="px-6 py-12 text-center">
              <p className="text-sm text-muted-foreground mb-3">
                No migrations match the selected filters.
              </p>
              <Button
                variant="outline"
                size="sm"
                onClick={() => {
                  setStatusFilter('')
                  setActionFilter('')
                }}
              >
                Clear filters
              </Button>
            </div>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="text-xs font-medium">When</TableHead>
                  <TableHead className="text-xs font-medium">Schedule</TableHead>
                  <TableHead className="text-xs font-medium">From → To</TableHead>
                  <TableHead className="text-xs font-medium text-right">Items</TableHead>
                  <TableHead className="text-xs font-medium text-right">Cohort delta</TableHead>
                  <TableHead className="text-xs font-medium">Status</TableHead>
                  <TableHead className="text-xs font-medium">Idempotency key</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {filtered.map((m) => {
                  const status = migrationStatus(m)
                  const delta = primaryDelta(m)
                  return (
                    <TableRow key={m.migration_id} className="cursor-pointer">
                      <TableCell>
                        <Link
                          to={`/plan-migrations/${m.migration_id}`}
                          className="text-sm text-foreground hover:text-primary hover:underline whitespace-nowrap"
                        >
                          {formatDateTime(m.applied_at)}
                        </Link>
                      </TableCell>
                      <TableCell>
                        <Badge variant="secondary">{effectiveLabel(m.effective)}</Badge>
                      </TableCell>
                      <TableCell>
                        <div className="flex items-center gap-1.5 font-mono text-xs">
                          <span className="text-muted-foreground truncate max-w-[150px]" title={m.from_plan_id}>
                            {m.from_plan_id}
                          </span>
                          <ArrowRight size={12} className="text-muted-foreground shrink-0" />
                          <span className="text-foreground truncate max-w-[150px]" title={m.to_plan_id}>
                            {m.to_plan_id}
                          </span>
                        </div>
                      </TableCell>
                      <TableCell className="text-right tabular-nums text-sm">
                        {m.applied_count}
                      </TableCell>
                      <TableCell className="text-right tabular-nums text-sm">
                        {delta ? (
                          <span
                            className={
                              delta.delta > 0
                                ? 'text-green-600'
                                : delta.delta < 0
                                  ? 'text-red-600'
                                  : 'text-foreground'
                            }
                          >
                            {delta.delta > 0 ? '+' : ''}
                            {formatCents(delta.delta, delta.currency)}
                          </span>
                        ) : (
                          <span className="text-muted-foreground">—</span>
                        )}
                      </TableCell>
                      <TableCell>
                        <Badge variant={statusVariant(status)}>{statusLabel(status)}</Badge>
                      </TableCell>
                      <TableCell>
                        <div className="flex items-center gap-1.5">
                          <span
                            className="text-xs font-mono text-muted-foreground"
                            title={m.idempotency_key}
                          >
                            {truncateKey(m.idempotency_key)}
                          </span>
                          <CopyButton text={m.idempotency_key} />
                        </div>
                      </TableCell>
                    </TableRow>
                  )
                })}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      {/* Pagination — useInfiniteQuery accumulates rather than swaps the
          window, so "Load more" is the natural affordance. We render the
          control even when filters narrow the visible rows because the
          next page may include rows that match. */}
      {!isLoading && !loadError && allMigrations.length > 0 && (
        <div className="flex items-center justify-between mt-4">
          <p className="text-xs text-muted-foreground">
            Showing {filtered.length} of {allMigrations.length} loaded
            {hasNextPage ? ' (more available)' : ''}
          </p>
          <Button
            variant="outline"
            size="sm"
            onClick={() => fetchNextPage()}
            disabled={!hasNextPage || isFetchingNextPage}
          >
            {isFetchingNextPage ? 'Loading…' : hasNextPage ? 'Load more' : 'All loaded'}
            {hasNextPage && <ChevronRight size={16} className="ml-1" />}
          </Button>
        </div>
      )}
    </Layout>
  )
}
