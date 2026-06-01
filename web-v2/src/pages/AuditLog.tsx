import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { formatInTimeZone } from 'date-fns-tz'
import { api, formatDateTime, formatRate, getTenantTimezone } from '@/lib/api'
import { startOfDayInTZ, endOfDayInTZ } from '@/lib/dates'
import type { AuditEntry } from '@/lib/api'
import { downloadCSV } from '@/lib/csv'
import { Layout } from '@/components/Layout'
import { EmptyState } from '@/components/EmptyState'
import { useUrlState } from '@/hooks/useUrlState'
import { cn } from '@/lib/utils'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { DatePicker } from '@/components/ui/date-picker'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent } from '@/components/ui/card'
import {
  Pagination,
  PaginationContent,
  PaginationItem,
  PaginationLink,
  PaginationNext,
  PaginationPrevious,
} from '@/components/ui/pagination'
import { FeedSkeleton } from '@/components/ui/TableSkeleton'

import { Download, ChevronRight, History } from 'lucide-react'

const PAGE_SIZE = 50

function describeAction(entry: AuditEntry): string {
  const label = entry.resource_label || ''
  // Item-level audit rows carry the meaningful discriminator in
  // metadata.action (item_plan_changed / item_quantity_changed) —
  // surface it cleanly instead of dumping the raw dotted action.
  const metaAction = (entry.metadata?.action as string) || ''
  switch (entry.action) {
    case 'create':
      if (entry.resource_type === 'payment_method') return `Added ${label || 'card'}`
      if (entry.resource_type === 'api_key') return `Created API key${label ? ` "${label}"` : ''}`
      if (entry.resource_type === 'webhook_endpoint') return `Created webhook endpoint${label ? ` ${label}` : ''}`
      if (entry.resource_type === 'test_clock') return `Created test clock${label ? ` "${label}"` : ''}`
      if (entry.resource_type === 'stripe_credentials') return `Connected Stripe (${(entry.metadata?.livemode as boolean) ? 'live' : 'test'})`
      return `Created ${label || entry.resource_type}`
    case 'update':
      // Sub-action discriminator for the update bucket: surface a
      // descriptive label per metadata.action when the bucket carries
      // a known transition. Falls through to generic "Updated X" for
      // anything not enumerated.
      if (entry.resource_type === 'invoice' && metaAction === 'marked_uncollectible') return `Marked ${label || 'invoice'} uncollectible`
      if (entry.resource_type === 'invoice' && metaAction === 'payment_recorded') return `Recorded offline payment on ${label || 'invoice'}`
      if (entry.resource_type === 'invoice' && metaAction === 'portal_pay_attempted') return `Customer paid ${label || 'invoice'} via portal`
      if (entry.resource_type === 'customer' && metaAction === 'profile_updated') return `Updated profile${label ? ` for ${label}` : ''}`
      if (entry.resource_type === 'customer' && metaAction === 'billing_profile_upserted') return `Updated billing profile${label ? ` for ${label}` : ''}`
      if (entry.resource_type === 'payment_method' && metaAction === 'default_changed') return `Set ${label || 'card'} as default`
      if (entry.resource_type === 'subscription' && metaAction === 'cancel_cleared') return `Cleared scheduled cancellation${label ? ` on ${label}` : ''}`
      if (entry.resource_type === 'dunning_run' && metaAction === 'resolved') return `Resolved dunning run (${entry.metadata?.resolution})`
      if (entry.resource_type === 'dunning_policy' && metaAction === 'set_default') return `Set dunning policy as default${label ? ` (${label})` : ''}`
      if (entry.resource_type === 'test_clock' && metaAction === 'advanced') return `Advanced test clock${label ? ` "${label}"` : ''}`
      if (entry.resource_type === 'test_clock' && metaAction === 'retry_advance') return `Retried clock advance${label ? ` on "${label}"` : ''}`
      if (entry.resource_type === 'webhook_event' && metaAction === 'replayed') return `Replayed webhook event`
      if (entry.resource_type === 'stripe_credentials' && metaAction === 'webhook_secret_set') return `Set Stripe webhook secret (${(entry.metadata?.livemode as boolean) ? 'live' : 'test'})`
      return `Updated ${label || entry.resource_type}`
    case 'delete':
      if (entry.resource_type === 'payment_method') return `Removed ${label || 'card'}`
      if (entry.resource_type === 'webhook_endpoint') return `Deleted webhook endpoint`
      if (entry.resource_type === 'test_clock') return `Deleted test clock`
      if (entry.resource_type === 'dunning_policy') return `Deleted dunning policy`
      if (entry.resource_type === 'stripe_credentials') return `Disconnected Stripe (${(entry.metadata?.livemode as boolean) ? 'live' : 'test'})`
      return `Deleted ${label || entry.resource_type}`
    case 'activate': return `Activated ${label || 'subscription'}`
    case 'cancel': return `Canceled ${label || 'subscription'}`
    case 'pause': return `Paused ${label || 'subscription'}`
    case 'resume': return `Resumed ${label || 'subscription'}`
    case 'finalize': return `Finalized ${label || 'invoice'}`
    case 'void': return `Voided ${label || 'invoice'}`
    case 'issue': return `Issued ${label || 'credit note'}`
    case 'resolve': return `Resolved ${label || 'dunning run'}`
    case 'grant': return `Granted credits${label ? ` to ${label}` : ''}`
    case 'adjust': return `Adjusted credits${label ? ` for ${label}` : ''}`
    case 'credit.adjustment': return `Adjusted credits${label ? ` for ${label}` : ''}`
    case 'credit.deduction': return `Deducted credits${label ? ` from ${label}` : ''}`
    case 'credit_note.issued': return `Issued credit note ${label}`
    case 'subscription.plan_changed': return `Changed plan${label ? ` for ${label}` : ''}`
    case 'subscription.item_updated':
      if (metaAction === 'item_plan_changed') return `Changed plan${label ? ` for ${label}` : ''}`
      if (metaAction === 'item_quantity_changed') return `Changed quantity${label ? ` for ${label}` : ''}`
      return `Updated item${label ? ` on ${label}` : ''}`
    case 'subscription.proration_failed': return `Proration failed${label ? ` on ${label}` : ''}`
    case 'revoke': return `Revoked API key${label ? ` "${label}"` : ''}`
    case 'rotate':
      if (entry.resource_type === 'api_key') return `Rotated API key${label ? ` "${label}"` : ''}`
      if (entry.resource_type === 'webhook_endpoint') return `Rotated webhook secret`
      if (entry.resource_type === 'stripe_credentials') return `Rotated Stripe webhook secret`
      return `Rotated ${label || entry.resource_type}`
    case 'run': return 'Billing cycle executed'
    case 'change_plan': return `Changed plan${label ? ` for ${label}` : ''}`
    default: return `${entry.action.replace(/_/g, ' ')} ${label || entry.resource_type}`
  }
}

const HIGH_SEVERITY = new Set(['void', 'cancel', 'delete', 'revoke', 'credit.deduction'])
const MEDIUM_SEVERITY = new Set(['finalize', 'grant', 'issue', 'credit_note.issued', 'subscription.plan_changed', 'change_plan', 'subscription.item_updated'])

function resourceLink(entry: AuditEntry): string | null {
  // Guard the empty-resource_id case — some audit rows (e.g. tenant-scope
  // events, or events written before a child resource exists) carry an
  // empty resource_id, and rendering "View" → /customers/ would land the
  // user on a broken page.
  if (!entry.resource_id) return null
  switch (entry.resource_type) {
    case 'invoice': return `/invoices/${entry.resource_id}`
    case 'customer': return `/customers/${entry.resource_id}`
    case 'subscription': return `/subscriptions/${entry.resource_id}`
    case 'plan': return `/plans/${entry.resource_id}`
    case 'meter': return `/meters/${entry.resource_id}`
    default: return null
  }
}

function formatActorName(entry: AuditEntry): string {
  if (entry.actor_type === 'system') return 'System'
  if (entry.actor_type === 'api_key') {
    // Prefer the key's human-readable name (resolved server-side via api_keys
    // join). Falls back to a truncated key id for rows written before the
    // name was set, or for keys that have since been deleted.
    if (entry.actor_name) return entry.actor_name
    return entry.actor_id.startsWith('vlx_') ? `Key ${entry.actor_id.slice(0, 16)}...` : 'API Key'
  }
  if (entry.actor_type === 'customer') {
    // Customer-portal-driven mutations (subscription cancel, profile edit,
    // payment-method changes) ship actor_id = customer.id. Prefer the
    // join-resolved actor_name; otherwise show "Customer" so operators
    // can see at-a-glance that the action came from the customer side.
    if (entry.actor_name) return entry.actor_name
    return 'Customer'
  }
  return entry.actor_type
}

function prettyLabel(value: string): string {
  return value
    .replace(/[_.]/g, ' ')
    .replace(/-/g, ' ')
    .replace(/\b\w/g, c => c.toUpperCase())
}

// Fallbacks for an empty tenant: without any audit rows the /filters endpoint
// returns [], leaving the dropdowns blank. These lists give a new tenant the
// common vocabulary (it's a hint, not a contract — merged with whatever the
// server returns).
const DEFAULT_RESOURCE_TYPES = [
  'customer', 'subscription', 'invoice', 'plan', 'meter',
  'credit', 'credit_note', 'api_key', 'billing', 'billing_profile',
  'payment_method', 'dunning_policy', 'dunning_run', 'test_clock',
  'webhook_endpoint', 'webhook_event', 'stripe_credentials',
  'price_override', 'rating_rule', 'meter_pricing_rule',
]
const DEFAULT_ACTIONS = [
  'create', 'update', 'delete', 'activate', 'cancel', 'pause', 'resume',
  'finalize', 'void', 'run', 'grant', 'revoke',
]

// Test-clock sim-context keys auto-added to metadata by audit-callers
// when the affected entity is clock-pinned (ADR-030 amendment
// 2026-05-28). Excluded from generic metadata rendering — surfaced as
// a dedicated subline + chip via simContext() below so the operator
// reads "wall-clock click + simulated effect-time" coherently instead
// of seeing the keys mixed in with business metadata.
const SIM_CONTEXT_KEYS = new Set(['sim_effective_at', 'test_clock_id'])

function simContext(meta: Record<string, unknown> | undefined): { simEffectiveAt?: string; testClockID?: string } {
  if (!meta) return {}
  const simEffectiveAt = typeof meta.sim_effective_at === 'string' ? meta.sim_effective_at : undefined
  const testClockID = typeof meta.test_clock_id === 'string' ? meta.test_clock_id : undefined
  return { simEffectiveAt, testClockID }
}

function formatMetadata(meta: Record<string, unknown> | undefined): { label: string; value: string }[] {
  if (!meta) return []
  const items: { label: string; value: string }[] = []
  for (const [key, val] of Object.entries(meta)) {
    if (key === 'resource_label') continue
    if (SIM_CONTEXT_KEYS.has(key)) continue
    const label = key.replace(/_/g, ' ').replace(/\b\w/g, c => c.toUpperCase())
    if (typeof val === 'number' && key.includes('cents')) {
      items.push({ label: label.replace(' Cents', ''), value: `$${(val / 100).toFixed(2)}` })
    } else if (typeof val === 'string' && key.includes('cents') && val !== '' && Number.isFinite(Number(val))) {
      // Decimal per-unit rates serialize as strings (e.g. "0.0003" cents).
      // toFixed(2) would collapse sub-cent rates to $0.00 — render at full
      // precision instead.
      items.push({ label: label.replace(' Cents', ''), value: formatRate(val) })
    } else if (val !== null && val !== undefined && val !== '') {
      items.push({ label, value: String(val) })
    }
  }
  return items
}

function groupByDate(entries: AuditEntry[]): { date: string; entries: AuditEntry[] }[] {
  // Group by tenant-TZ date so the section headers match the
  // dashboard's tenant-TZ display elsewhere. Around UTC-day
  // boundaries the prior browser-local grouping disagreed with the
  // entries' rendered timestamps. ADR-010.
  const tz = getTenantTimezone() || undefined
  const fmtDate = (iso: string) =>
    tz
      ? formatInTimeZone(new Date(iso), tz, 'MMMM d, yyyy')
      : new Date(iso).toLocaleDateString('en-US', { year: 'numeric', month: 'long', day: 'numeric' })

  const groups: { date: string; entries: AuditEntry[] }[] = []
  let currentDate = ''
  for (const entry of entries) {
    const date = fmtDate(entry.created_at)
    if (date !== currentDate) {
      groups.push({ date, entries: [] })
      currentDate = date
    }
    groups[groups.length - 1].entries.push(entry)
  }
  return groups
}

function actionVariant(action: string): 'default' | 'secondary' | 'destructive' | 'outline' | 'success' | 'info' | 'warning' | 'danger' {
  if (HIGH_SEVERITY.has(action)) return 'danger'
  if (MEDIUM_SEVERITY.has(action)) return 'warning'
  // Green for positive actions
  if (['create', 'activate', 'resume', 'grant', 'resolve'].includes(action)) return 'success'
  // Blue for updates/changes
  if (['update', 'finalize', 'run', 'subscription.plan_changed', 'credit_note.issued'].includes(action)) return 'info'
  return 'secondary'
}

export default function AuditLogPage() {
  const [expandedId, setExpandedId] = useState<string | null>(null)
  const [exporting, setExporting] = useState(false)
  const [urlState, setUrlState] = useUrlState({
    resource_type: '',
    action: '',
    actor: '',
    resource_id: '',
    date_from: '',
    date_to: '',
    page: '1',
  })
  const {
    resource_type: resourceType,
    action,
    actor: actorFilter,
    resource_id: resourceIdFilter,
    date_from: dateFrom,
    date_to: dateTo,
  } = urlState
  const page = Math.max(1, parseInt(urlState.page) || 1)

  // Audit log entries query — keyed by every filter dimension so the
  // cache is sharded per-filter-set. Changing any filter triggers a
  // refetch automatically (RQ recognizes a new key). Tenant-TZ
  // grounded date instants (ADR-010) so the same calendar day
  // resolves identically across operator time zones.
  const qs = (() => {
    const params = new URLSearchParams()
    params.set('limit', String(PAGE_SIZE))
    params.set('offset', String((page - 1) * PAGE_SIZE))
    if (resourceType) params.set('resource_type', resourceType)
    if (action) params.set('action', action)
    if (resourceIdFilter) params.set('resource_id', resourceIdFilter)
    if (actorFilter) params.set('actor_id', actorFilter)
    if (dateFrom) params.set('date_from', startOfDayInTZ(dateFrom))
    if (dateTo) params.set('date_to', endOfDayInTZ(dateTo))
    return params.toString()
  })()
  const entriesQuery = useQuery({
    queryKey: ['audit-log', qs],
    queryFn: () => api.listAuditLog(qs),
  })
  const entries = entriesQuery.data?.data ?? []
  const total = entriesQuery.data?.total ?? 0
  const loading = entriesQuery.isLoading
  const error = entriesQuery.error instanceof Error ? entriesQuery.error.message : (entriesQuery.error ? 'Failed to load audit log' : null)
  const loadEntries = () => { void entriesQuery.refetch() }

  // Populate filter dropdowns from what's actually recorded for this tenant.
  // Fall back to defaults on error or empty — keeps the UI usable for new
  // tenants whose audit log hasn't been populated yet.
  const filterOptionsQuery = useQuery({
    queryKey: ['audit-log', 'filters'],
    queryFn: () => api.getAuditFilters(),
  })
  const filterOptions = {
    actions: filterOptionsQuery.data?.actions?.length ? filterOptionsQuery.data.actions : DEFAULT_ACTIONS,
    resourceTypes: filterOptionsQuery.data?.resource_types?.length ? filterOptionsQuery.data.resource_types : DEFAULT_RESOURCE_TYPES,
  }

  const totalPages = Math.ceil(total / PAGE_SIZE)
  const groups = groupByDate(entries)

  // Export walks pages of 100 until either exhausted or the safety cap is
  // reached. A fixed cap (50k) keeps the browser from OOMing on a massive
  // tenant; beyond that, a server-side streaming export should take over.
  const EXPORT_PAGE_SIZE = 100
  const EXPORT_MAX_ROWS = 50_000

  const handleExport = async () => {
    setExporting(true)
    try {
      const filters = new URLSearchParams()
      if (resourceType) filters.set('resource_type', resourceType)
      if (action) filters.set('action', action)
      if (resourceIdFilter) filters.set('resource_id', resourceIdFilter)
      if (actorFilter) filters.set('actor_id', actorFilter)
      if (dateFrom) filters.set('date_from', dateFrom)
      if (dateTo) filters.set('date_to', dateTo)

      const all: AuditEntry[] = []
      let offset = 0
      while (all.length < EXPORT_MAX_ROWS) {
        const params = new URLSearchParams(filters)
        params.set('limit', String(EXPORT_PAGE_SIZE))
        params.set('offset', String(offset))
        const res = await api.listAuditLog(params.toString())
        const batch = res.data || []
        all.push(...batch)
        if (batch.length < EXPORT_PAGE_SIZE) break
        offset += EXPORT_PAGE_SIZE
      }

      const rows = all.slice(0, EXPORT_MAX_ROWS).map(e => [
        formatActorName(e),
        e.actor_id,
        e.action,
        e.resource_type,
        e.resource_id,
        e.resource_label || '',
        e.ip_address || '',
        e.request_id || '',
        formatDateTime(e.created_at),
      ])
      downloadCSV(
        'audit-log.csv',
        ['Actor', 'Actor ID', 'Action', 'Resource Type', 'Resource ID', 'Resource Label', 'IP', 'Request ID', 'Date'],
        rows,
      )
    } finally {
      setExporting(false)
    }
  }

  const selectClass = "flex h-9 rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-xs transition-colors focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"

  const hasFilters = resourceType || action || actorFilter || resourceIdFilter || dateFrom || dateTo

  return (
    <Layout>
      {/* Header */}
      <div className="flex justify-between items-center">
        <div>
          <h1 className="text-2xl font-semibold text-foreground">Audit Log</h1>
          <p className="text-sm text-muted-foreground mt-1">Review all changes and actions{total > 0 ? ` · ${total} total` : ''}</p>
        </div>
        {entries.length > 0 && (
          <Button variant="outline" size="sm" onClick={handleExport} disabled={exporting}>
            <Download size={16} className="mr-2" /> {exporting ? 'Exporting…' : 'Export'}
          </Button>
        )}
      </div>

      {/* Filters */}
      <div className="flex flex-wrap items-center gap-3 mt-6">
        <select
          value={resourceType}
          onChange={e => setUrlState({ resource_type: e.target.value, page: '1' })}
          className={cn(selectClass, 'w-44')}
        >
          <option value="">All resources</option>
          {filterOptions.resourceTypes.map(rt => (
            <option key={rt} value={rt}>{prettyLabel(rt)}</option>
          ))}
        </select>
        <select
          value={action}
          onChange={e => setUrlState({ action: e.target.value, page: '1' })}
          className={cn(selectClass, 'w-44')}
        >
          <option value="">All actions</option>
          {filterOptions.actions.map(a => (
            <option key={a} value={a}>{prettyLabel(a)}</option>
          ))}
        </select>
        <Input
          value={actorFilter}
          onChange={e => setUrlState({ actor: e.target.value, page: '1' })}
          placeholder="Filter by actor..."
          className="w-40"
        />
        <Input
          value={resourceIdFilter}
          onChange={e => setUrlState({ resource_id: e.target.value, page: '1' })}
          placeholder="Filter by resource ID..."
          className="w-44"
        />
        <DatePicker
          value={dateFrom}
          onChange={v => setUrlState({ date_from: v, page: '1' })}
          placeholder="From date"
          className="w-44"
        />
        <DatePicker
          value={dateTo}
          onChange={v => setUrlState({ date_to: v, page: '1' })}
          placeholder="To date"
          className="w-44"
          minDate={dateFrom ? new Date(dateFrom + 'T00:00:00') : undefined}
        />
        {hasFilters && (
          <Button
            variant="outline"
            size="sm"
            onClick={() => setUrlState({
              resource_type: '', action: '', actor: '', resource_id: '',
              date_from: '', date_to: '', page: '1',
            })}
          >
            Clear all
          </Button>
        )}
      </div>

      {/* Timeline */}
      <Card className="mt-4">
        <CardContent className="p-0">
          {error ? (
            <div className="p-8 text-center">
              <p className="text-sm text-destructive mb-3">{error}</p>
              <Button variant="outline" size="sm" onClick={loadEntries}>
                Retry
              </Button>
            </div>
          ) : loading ? (
            <FeedSkeleton rows={10} />
          ) : entries.length === 0 ? (
            <EmptyState
              icon={History}
              title="No audit entries"
              description="Actions will be recorded here automatically as you use the platform"
            />
          ) : (
            <>
              <div>
                {groups.map((group, gi) => (
                  <div key={group.date}>
                    <div className={cn(
                      'px-6 py-2 bg-muted/50 text-xs font-medium text-muted-foreground',
                      gi > 0 && 'border-t border-border'
                    )}>
                      {group.date}
                    </div>
                    <div className="divide-y divide-border">
                      {group.entries.map(entry => {
                        const tz = getTenantTimezone() || undefined
                        const time = tz
                          ? formatInTimeZone(new Date(entry.created_at), tz, 'h:mm a')
                          : new Date(entry.created_at).toLocaleTimeString('en-US', { hour: 'numeric', minute: '2-digit' })
                        const isHigh = HIGH_SEVERITY.has(entry.action)
                        const isMedium = MEDIUM_SEVERITY.has(entry.action)
                        const isExpanded = expandedId === entry.id
                        const link = resourceLink(entry)
                        const meta = formatMetadata(entry.metadata)
                        const sim = simContext(entry.metadata)

                        return (
                          <div key={entry.id}>
                            <div
                              className={cn(
                                'flex items-center px-6 py-2.5 transition-colors cursor-pointer',
                                isExpanded ? 'bg-muted/50' : 'hover:bg-muted/30',
                                isHigh ? 'border-l-2 border-l-destructive' : isMedium ? 'border-l-2 border-l-amber-400' : 'border-l-2 border-l-transparent'
                              )}
                              onClick={() => setExpandedId(isExpanded ? null : entry.id)}
                            >
                              <span className="text-sm text-muted-foreground w-20 shrink-0 tabular-nums">{time}</span>
                              <Badge variant={actionVariant(entry.action)}>{entry.action}</Badge>
                              <span className="text-sm text-foreground ml-2.5 flex-1 truncate" title={describeAction(entry)}>
                                {describeAction(entry)}
                              </span>
                              {sim.simEffectiveAt && (
                                <span
                                  title={`Operator clicked at wall-clock ${formatDateTime(entry.created_at)}; effect landed on test clock ${sim.testClockID || ''} at simulated ${formatDateTime(sim.simEffectiveAt)}`}
                                  className="inline-flex shrink-0 items-center rounded border border-amber-500/30 bg-amber-500/10 px-1.5 py-0.5 text-[10px] font-medium leading-none text-amber-800 dark:text-amber-300 ml-2"
                                >
                                  test clock
                                </span>
                              )}
                              {link && (
                                <Link
                                  to={link}
                                  onClick={e => e.stopPropagation()}
                                  className="text-xs text-primary hover:underline shrink-0 ml-3"
                                >
                                  View
                                </Link>
                              )}
                              <span className="text-xs text-muted-foreground shrink-0 ml-3 w-28 text-right truncate" title={entry.actor_id}>
                                {formatActorName(entry)}
                              </span>
                              <ChevronRight size={14} className={cn('text-muted-foreground ml-2 shrink-0 transition-transform', isExpanded && 'rotate-90')} />
                            </div>

                            {/* Expanded detail */}
                            {isExpanded && (
                              <div className="bg-muted/50 px-6 py-3 border-t border-border">
                                <div className="grid grid-cols-2 lg:grid-cols-4 gap-3 text-sm">
                                  <div>
                                    <p className="text-xs text-muted-foreground">Resource Type</p>
                                    <p className="text-foreground mt-0.5">{entry.resource_type.replace(/_/g, ' ')}</p>
                                  </div>
                                  <div>
                                    <p className="text-xs text-muted-foreground">Resource ID</p>
                                    <p className="text-foreground font-mono text-xs mt-0.5 truncate" title={entry.resource_id}>{entry.resource_id}</p>
                                  </div>
                                  <div>
                                    <p className="text-xs text-muted-foreground">Actor</p>
                                    <p className="text-foreground mt-0.5">{formatActorName(entry)}</p>
                                  </div>
                                  <div>
                                    <p className="text-xs text-muted-foreground">Timestamp</p>
                                    <p className="text-foreground mt-0.5">{formatDateTime(entry.created_at)}</p>
                                    {sim.simEffectiveAt && (
                                      <p className="text-xs text-amber-700 dark:text-amber-400 mt-0.5">
                                        Effect on test clock {sim.testClockID ? <span className="font-mono">{sim.testClockID}</span> : ''} at {formatDateTime(sim.simEffectiveAt)}
                                      </p>
                                    )}
                                  </div>
                                </div>
                                {meta.length > 0 && (
                                  <div className="mt-3 pt-3 border-t border-border">
                                    <p className="text-xs font-medium text-muted-foreground mb-2">Details</p>
                                    <div className="grid grid-cols-2 lg:grid-cols-3 gap-2">
                                      {meta.map(m => (
                                        <div key={m.label} className="bg-background rounded-lg px-3 py-2 border border-border">
                                          <p className="text-xs text-muted-foreground">{m.label}</p>
                                          <p className="text-sm text-foreground mt-0.5 font-mono truncate" title={m.value}>{m.value}</p>
                                        </div>
                                      ))}
                                    </div>
                                  </div>
                                )}
                              </div>
                            )}
                          </div>
                        )
                      })}
                    </div>
                  </div>
                ))}
              </div>

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
    </Layout>
  )
}
