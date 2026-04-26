import { useMemo } from 'react'
import { useParams, Link, useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { Loader2, ArrowRight, AlertTriangle, History } from 'lucide-react'

import { Layout } from '@/components/Layout'
import { DetailBreadcrumb } from '@/components/DetailBreadcrumb'
import { CopyButton } from '@/components/CopyButton'

import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

import {
  api,
  formatCents,
  formatDateTime,
  type AuditEntry,
  type PlanMigrationListItem,
} from '@/lib/api'

// Local mirrors of the helpers from PlanMigrationsHistory — duplicated to
// keep each page self-contained (no /lib helper module yet for plan-migration
// formatters; both pages are <300 lines and the divergence risk is low).
function migrationStatus(m: PlanMigrationListItem): 'committed' | 'partial' {
  if (m.applied_count === 0 && (m.totals?.length ?? 0) > 0) return 'partial'
  return 'committed'
}

function statusLabel(s: 'committed' | 'partial'): string {
  return s === 'committed' ? 'Committed' : 'Partial'
}

function statusVariant(s: 'committed' | 'partial'): 'success' | 'warning' {
  return s === 'committed' ? 'success' : 'warning'
}

function effectiveLabel(e: string): string {
  if (e === 'immediate') return 'Immediate'
  if (e === 'next_period') return 'Next period'
  return e
}

function actorLabel(m: PlanMigrationListItem): string {
  if (m.applied_by_type === 'system') return 'System'
  if (m.applied_by_type === 'api_key') {
    if (!m.applied_by) return 'API key'
    return m.applied_by.startsWith('vlx_')
      ? `Key ${m.applied_by.slice(0, 16)}…`
      : m.applied_by
  }
  return m.applied_by || m.applied_by_type
}

// Filter description: "all" → friendly phrase; "ids" → list with count;
// "tag" → reserved (the wire shape carries it but the service rejects).
function describeFilter(f: PlanMigrationListItem['customer_filter']): { label: string; detail?: string } {
  if (f.type === 'all') {
    return { label: 'All customers on source plan' }
  }
  if (f.type === 'ids') {
    const count = f.ids?.length ?? 0
    return {
      label: `Specific customer IDs (${count})`,
      detail: (f.ids ?? []).join(', '),
    }
  }
  if (f.type === 'tag') {
    return { label: `Tag: ${f.value ?? '—'}` }
  }
  return { label: f.type }
}

export default function PlanMigrationDetailPage() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()

  const {
    data: migration,
    isLoading,
    error,
    refetch,
  } = useQuery({
    queryKey: ['plan-migration', id],
    queryFn: () => api.getPlanMigration(id!),
    enabled: !!id,
  })

  // Audit trail: the cohort summary entry lives on resource_type=plan_migration
  // / resource_id=<migration_id>, and the per-customer subscription.plan_changed
  // entries reference the same migration via metadata.plan_migration_id (no
  // direct join). The first query is exact; the second is a subscription-scoped
  // walk we filter client-side.
  const { data: cohortAudit } = useQuery({
    queryKey: ['plan-migration', id, 'audit', 'cohort'],
    queryFn: () =>
      api.listAuditLog(
        new URLSearchParams({
          resource_type: 'plan_migration',
          resource_id: id!,
          limit: '5',
        }).toString(),
      ),
    enabled: !!id,
  })

  // Per-customer trail. We pull a wider window of subscription.plan_changed
  // entries and filter to those whose metadata references this migration_id.
  // The list endpoint is reverse-chrono so a typical migration's per-customer
  // entries cluster together, and the cap (200) is well above the v1 cohort
  // size limit (100 subscriptions per page from the planmigration cohort()
  // call). When a future migration exceeds the cap we'll lift this to a
  // server-side filter.
  const { data: perCustomerAudit } = useQuery({
    queryKey: ['plan-migration', id, 'audit', 'per_customer'],
    queryFn: () =>
      api.listAuditLog(
        new URLSearchParams({
          resource_type: 'subscription',
          action: 'subscription.plan_changed',
          limit: '200',
        }).toString(),
      ),
    enabled: !!id,
  })

  const matchedPerCustomer = useMemo<AuditEntry[]>(() => {
    if (!perCustomerAudit?.data || !id) return []
    return perCustomerAudit.data.filter((e) => {
      const meta = e.metadata as Record<string, unknown> | undefined
      return meta?.plan_migration_id === id
    })
  }, [perCustomerAudit, id])

  if (isLoading) {
    return (
      <Layout>
        <DetailBreadcrumb
          to="/plan-migrations/history"
          parentLabel="Plan migrations"
          currentLabel="Loading…"
        />
        <div className="flex justify-center py-16">
          <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
        </div>
      </Layout>
    )
  }

  if (error) {
    const msg = error instanceof Error ? error.message : String(error)
    return (
      <Layout>
        <DetailBreadcrumb
          to="/plan-migrations/history"
          parentLabel="Plan migrations"
          currentLabel="Error"
        />
        <div className="py-16 text-center">
          <p className="text-sm text-destructive mb-3">{msg}</p>
          <Button variant="outline" size="sm" onClick={() => refetch()}>
            Retry
          </Button>
        </div>
      </Layout>
    )
  }

  if (!migration) {
    return (
      <Layout>
        <DetailBreadcrumb
          to="/plan-migrations/history"
          parentLabel="Plan migrations"
          currentLabel="Not found"
        />
        <div className="py-16 text-center">
          <p className="text-sm text-muted-foreground mb-3">
            Migration {id} not found in the recent history window.
          </p>
          <Button variant="outline" size="sm" onClick={() => navigate('/plan-migrations/history')}>
            Back to history
          </Button>
        </div>
      </Layout>
    )
  }

  const status = migrationStatus(migration)
  const filterInfo = describeFilter(migration.customer_filter)
  const cohortEntry = cohortAudit?.data?.[0]
  const itemErrors = (() => {
    const meta = cohortEntry?.metadata as Record<string, unknown> | undefined
    const raw = meta?.item_errors
    if (!Array.isArray(raw)) return []
    return raw.filter((e): e is string => typeof e === 'string')
  })()

  return (
    <Layout>
      <DetailBreadcrumb
        to="/plan-migrations/history"
        parentLabel="Plan migrations"
        currentLabel={migration.migration_id}
      />

      {/* Header */}
      <div className="flex items-start justify-between mb-6">
        <div>
          <h1 className="text-2xl font-semibold text-foreground flex items-center gap-2">
            <History size={20} />
            Migration {migration.migration_id.slice(-12)}
          </h1>
          <div className="flex items-center gap-2 mt-1">
            <span className="text-xs text-muted-foreground font-mono bg-muted px-2 py-0.5 rounded">
              {migration.migration_id}
            </span>
            <CopyButton text={migration.migration_id} />
          </div>
        </div>
        <div className="flex items-center gap-3">
          <Badge variant={statusVariant(status)}>{statusLabel(status)}</Badge>
          <Badge variant="secondary">{effectiveLabel(migration.effective)}</Badge>
        </div>
      </div>

      {/* Key metrics */}
      <Card>
        <CardContent className="p-0">
          <div className="flex divide-x divide-border">
            <div className="flex-1 px-6 py-4">
              <p className="text-sm text-muted-foreground">Applied at</p>
              <p className="text-sm font-medium text-foreground mt-1">
                {formatDateTime(migration.applied_at)}
              </p>
            </div>
            <div className="flex-1 px-6 py-4">
              <p className="text-sm text-muted-foreground">Applied by</p>
              <p className="text-sm font-medium text-foreground mt-1">{actorLabel(migration)}</p>
            </div>
            <div className="flex-1 px-6 py-4">
              <p className="text-sm text-muted-foreground">Items updated</p>
              <p className="text-lg font-semibold text-foreground mt-1 tabular-nums">
                {migration.applied_count}
              </p>
            </div>
            <div className="flex-1 px-6 py-4">
              <p className="text-sm text-muted-foreground">Schedule</p>
              <p className="text-sm font-medium text-foreground mt-1">
                {effectiveLabel(migration.effective)}
              </p>
            </div>
          </div>
        </CardContent>
      </Card>

      {/* Plan move + filter */}
      <Card className="mt-6">
        <CardHeader>
          <CardTitle className="text-sm">Migration parameters</CardTitle>
        </CardHeader>
        <CardContent className="p-0">
          <div className="divide-y divide-border">
            <PropRow label="From plan">
              <span className="text-sm font-mono text-foreground">{migration.from_plan_id}</span>
            </PropRow>
            <PropRow label="To plan">
              <span className="text-sm font-mono text-foreground">{migration.to_plan_id}</span>
            </PropRow>
            <PropRow label="Customer filter">
              <span className="text-sm text-foreground">{filterInfo.label}</span>
            </PropRow>
            {filterInfo.detail && (
              <div className="px-6 py-3">
                <span className="text-xs text-muted-foreground block mb-1">Customer IDs</span>
                <code className="text-xs font-mono text-foreground bg-muted px-2 py-1 rounded block whitespace-pre-wrap break-all">
                  {filterInfo.detail}
                </code>
              </div>
            )}
            <PropRow label="Idempotency key">
              <div className="flex items-center gap-2">
                <span className="text-xs font-mono text-foreground">{migration.idempotency_key}</span>
                <CopyButton text={migration.idempotency_key} />
              </div>
            </PropRow>
          </div>
        </CardContent>
      </Card>

      {/* Cohort totals — always-array shape, one row per currency */}
      {migration.totals && migration.totals.length > 0 && (
        <Card className="mt-6">
          <CardHeader>
            <CardTitle className="text-sm">Cohort totals</CardTitle>
          </CardHeader>
          <CardContent className="p-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="text-xs font-medium">Currency</TableHead>
                  <TableHead className="text-xs font-medium text-right">Before</TableHead>
                  <TableHead className="text-xs font-medium text-right">After</TableHead>
                  <TableHead className="text-xs font-medium text-right">Delta</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {migration.totals.map((t) => (
                  <TableRow key={t.currency}>
                    <TableCell className="text-sm font-medium">{t.currency.toUpperCase()}</TableCell>
                    <TableCell className="text-right tabular-nums text-sm">
                      {formatCents(t.before_amount_cents, t.currency)}
                    </TableCell>
                    <TableCell className="text-right tabular-nums text-sm">
                      {formatCents(t.after_amount_cents, t.currency)}
                    </TableCell>
                    <TableCell
                      className={
                        'text-right tabular-nums font-medium text-sm ' +
                        (t.delta_amount_cents > 0
                          ? 'text-green-600'
                          : t.delta_amount_cents < 0
                            ? 'text-red-600'
                            : '')
                      }
                    >
                      {t.delta_amount_cents > 0 ? '+' : ''}
                      {formatCents(t.delta_amount_cents, t.currency)}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      )}

      {/* Per-item errors surfaced from the cohort audit metadata. The
          service path appends one entry per failed subscription_item swap
          and lets the migration row + audit entry persist regardless,
          so partial application is recoverable from the dashboard. */}
      {itemErrors.length > 0 && (
        <Card className="mt-6 border-amber-500/40">
          <CardHeader>
            <CardTitle className="text-sm flex items-center gap-2">
              <AlertTriangle size={16} className="text-amber-600" />
              Per-item errors ({itemErrors.length})
            </CardTitle>
          </CardHeader>
          <CardContent>
            <ul className="list-disc pl-5 space-y-1 text-sm text-muted-foreground">
              {itemErrors.map((err, i) => (
                <li key={i} className="font-mono text-xs break-all">
                  {err}
                </li>
              ))}
            </ul>
          </CardContent>
        </Card>
      )}

      {/* Audit trail — cohort entry + per-customer plan_changed walk */}
      <Card className="mt-6">
        <CardHeader>
          <CardTitle className="text-sm">Audit trail</CardTitle>
        </CardHeader>
        <CardContent className="p-0">
          {cohortEntry || matchedPerCustomer.length > 0 ? (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="text-xs font-medium">When</TableHead>
                  <TableHead className="text-xs font-medium">Action</TableHead>
                  <TableHead className="text-xs font-medium">Resource</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {cohortEntry && (
                  <TableRow>
                    <TableCell className="text-sm whitespace-nowrap">
                      {formatDateTime(cohortEntry.created_at)}
                    </TableCell>
                    <TableCell>
                      <Badge variant="info">{cohortEntry.action}</Badge>
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {cohortEntry.resource_label || cohortEntry.resource_type}
                    </TableCell>
                  </TableRow>
                )}
                {matchedPerCustomer.map((entry) => {
                  const meta = entry.metadata as Record<string, unknown> | undefined
                  const customerId = meta?.customer_id as string | undefined
                  return (
                    <TableRow key={entry.id}>
                      <TableCell className="text-sm whitespace-nowrap">
                        {formatDateTime(entry.created_at)}
                      </TableCell>
                      <TableCell>
                        <Badge variant="info">subscription.plan_changed</Badge>
                      </TableCell>
                      <TableCell className="text-xs text-muted-foreground">
                        {customerId ? (
                          <Link
                            to={`/customers/${customerId}`}
                            className="font-mono hover:text-primary hover:underline"
                          >
                            {customerId}
                          </Link>
                        ) : (
                          <Link
                            to={`/subscriptions/${entry.resource_id}`}
                            className="font-mono hover:text-primary hover:underline"
                          >
                            {entry.resource_id}
                          </Link>
                        )}
                      </TableCell>
                    </TableRow>
                  )
                })}
              </TableBody>
            </Table>
          ) : (
            <p className="px-6 py-8 text-sm text-muted-foreground text-center">
              No audit entries found for this migration.
            </p>
          )}
        </CardContent>
      </Card>

      <div className="mt-6 flex items-center justify-between">
        <Link
          to="/plan-migrations/history"
          className="text-sm text-muted-foreground hover:text-foreground"
        >
          Back to history
        </Link>
        <Link to="/plan-migrations">
          <Button variant="outline" size="sm">
            Start another migration
            <ArrowRight size={14} className="ml-2" />
          </Button>
        </Link>
      </div>
    </Layout>
  )
}

function PropRow({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-center justify-between px-6 py-3">
      <span className="text-sm text-muted-foreground">{label}</span>
      {children}
    </div>
  )
}
