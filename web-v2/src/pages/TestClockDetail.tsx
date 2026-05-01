import { useEffect, useMemo, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'

import { api, formatDateTime, getTenantTimezone, type TestClock } from '@/lib/api'
import { formatInTimeZone, fromZonedTime } from 'date-fns-tz'
import { Layout } from '@/components/Layout'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { DatePicker } from '@/components/ui/date-picker'
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from '@/components/ui/dialog'
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { useAuth } from '@/contexts/AuthContext'
import { ApiError } from '@/lib/api'
import { ChevronLeft, FastForward, Loader2, Trash2, AlertTriangle, Clock as ClockIcon } from 'lucide-react'

function StatusBadge({ status }: { status: TestClock['status'] }) {
  switch (status) {
    case 'ready':
      return <Badge variant="secondary">Ready</Badge>
    case 'advancing':
      return (
        <Badge variant="outline" className="border-blue-500/30 bg-blue-500/10 text-blue-700 dark:text-blue-400">
          <Loader2 size={10} className="animate-spin mr-1" /> Advancing
        </Badge>
      )
    case 'internal_failure':
      return <Badge variant="destructive">Failed</Badge>
  }
}

export default function TestClockDetailPage() {
  const { id } = useParams<{ id: string }>()
  const { user } = useAuth()
  const navigate = useNavigate()
  const queryClient = useQueryClient()

  const [showAdvance, setShowAdvance] = useState(false)
  const [showDelete, setShowDelete] = useState(false)

  // Test-mode-only — same redirect guard as the index page.
  useEffect(() => {
    if (user?.livemode) navigate('/', { replace: true })
  }, [user?.livemode, navigate])

  const clockQ = useQuery({
    queryKey: ['test-clocks', id],
    queryFn: () => api.getTestClock(id!),
    enabled: !!id && !!user && !user.livemode,
    // Poll while the clock is advancing so the UI catches the back-to-ready
    // transition without a manual refresh. Status field drives the cadence.
    refetchInterval: q => (q.state.data?.status === 'advancing' ? 1500 : false),
  })

  const subsQ = useQuery({
    queryKey: ['test-clocks', id, 'subscriptions'],
    queryFn: () => api.listSubscriptionsOnClock(id!),
    enabled: !!id && !!user && !user.livemode,
  })

  const clock = clockQ.data
  const subs = subsQ.data?.data ?? []

  const handleDelete = async () => {
    if (!id) return
    try {
      await api.deleteTestClock(id)
      toast.success('Test clock deleted')
      queryClient.invalidateQueries({ queryKey: ['test-clocks'] })
      navigate('/test-clocks')
    } catch (err) {
      const msg = err instanceof Error ? err.message : 'Failed to delete'
      toast.error(msg)
    }
  }

  if (clockQ.isLoading) {
    return (
      <Layout>
        <div className="flex items-center justify-center py-20">
          <Loader2 className="animate-spin" />
        </div>
      </Layout>
    )
  }
  if (clockQ.error || !clock) {
    return (
      <Layout>
        <Card className="mt-6">
          <CardContent className="p-8 text-center">
            <p className="text-sm text-destructive mb-3">
              {clockQ.error instanceof Error ? clockQ.error.message : 'Test clock not found'}
            </p>
            <Link to="/test-clocks">
              <Button variant="outline" size="sm">Back to Test Clocks</Button>
            </Link>
          </CardContent>
        </Card>
      </Layout>
    )
  }

  return (
    <Layout>
      <div>
        <Link to="/test-clocks" className="inline-flex items-center text-xs text-muted-foreground hover:text-foreground transition-colors">
          <ChevronLeft size={14} /> Back to Test Clocks
        </Link>
      </div>

      <div className="mt-2 flex items-start justify-between gap-4">
        <div className="min-w-0">
          <div className="flex items-center gap-3">
            <h1 className="text-2xl font-semibold text-foreground truncate">
              {clock.name || 'Unnamed clock'}
            </h1>
            <StatusBadge status={clock.status} />
          </div>
          <p className="text-xs text-muted-foreground font-mono mt-1">{clock.id}</p>
        </div>
        <div className="flex items-center gap-2 shrink-0">
          <Button
            size="sm"
            disabled={clock.status !== 'ready'}
            onClick={() => setShowAdvance(true)}
          >
            <FastForward size={14} className="mr-2" />
            Advance
          </Button>
          <Button size="sm" variant="outline" className="text-destructive hover:text-destructive" onClick={() => setShowDelete(true)}>
            <Trash2 size={14} />
          </Button>
        </div>
      </div>

      {clock.status === 'internal_failure' && (
        <Card className="mt-4 border-destructive/30 bg-destructive/5">
          <CardContent className="px-6 py-4 flex items-start gap-3">
            <AlertTriangle size={16} className="text-destructive shrink-0 mt-0.5" />
            <div className="text-sm">
              <p className="font-medium text-destructive">Catchup failed during last advance.</p>
              <p className="text-muted-foreground mt-1">
                Some invoices may have been generated before the failure. Inspect billing
                state, then delete this clock to start a new simulation.
              </p>
            </div>
          </CardContent>
        </Card>
      )}

      {/* Current clock time — the whole point of this page. */}
      <Card className="mt-6">
        <CardContent className="px-6 py-5">
          <div className="flex items-center gap-3">
            <ClockIcon size={20} className="text-violet-500" />
            <div>
              <p className="text-xs uppercase tracking-wider text-muted-foreground">Current clock time</p>
              <p className="text-lg font-mono mt-0.5">{formatDateTime(clock.frozen_time)}</p>
            </div>
          </div>
        </CardContent>
      </Card>

      {/* Subscriptions on this clock */}
      <div className="mt-8">
        <div className="flex items-center justify-between mb-3">
          <h2 className="text-sm font-semibold text-foreground">Subscriptions on this clock</h2>
          <p className="text-xs text-muted-foreground">{subs.length} attached</p>
        </div>
        {subsQ.isLoading ? (
          <Card><CardContent className="p-8 text-center text-sm text-muted-foreground">Loading…</CardContent></Card>
        ) : subs.length === 0 ? (
          <Card>
            <CardContent className="p-8 text-center">
              <p className="text-sm text-muted-foreground mb-1">No subscriptions pinned yet.</p>
              <p className="text-xs text-muted-foreground">
                Create one via API:&nbsp;
                <code className="font-mono text-foreground">POST /v1/subscriptions</code>&nbsp;
                with <code className="font-mono text-foreground">test_clock_id="{clock.id}"</code>.
              </p>
            </CardContent>
          </Card>
        ) : (
          <div className="space-y-2">
            {subs.map(s => (
              <Link key={s.id} to={`/subscriptions/${s.id}`}>
                <Card className="hover:bg-accent/30 transition-colors">
                  <CardContent className="px-6 py-3 flex items-center gap-4">
                    <div className="flex-1 min-w-0">
                      <div className="flex items-center gap-2">
                        <p className="text-sm font-medium text-foreground truncate">{s.display_name || s.code}</p>
                        <Badge variant="outline">{s.status}</Badge>
                      </div>
                      <p className="text-xs text-muted-foreground mt-0.5 font-mono">{s.id}</p>
                    </div>
                    <div className="text-xs text-muted-foreground shrink-0 text-right">
                      {s.next_billing_at ? <>Next bill: {formatDateTime(s.next_billing_at)}</> : 'No next-bill'}
                    </div>
                  </CardContent>
                </Card>
              </Link>
            ))}
          </div>
        )}
      </div>

      {showAdvance && (
        <AdvanceClockDialog
          clock={clock}
          subs={subs}
          onClose={() => setShowAdvance(false)}
        />
      )}

      <AlertDialog open={showDelete} onOpenChange={open => { if (!open) setShowDelete(false) }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete test clock?</AlertDialogTitle>
            <AlertDialogDescription>
              This deletes the clock and any subscriptions pinned to it. Invoices and
              other downstream data already generated stay in place.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel onClick={() => setShowDelete(false)}>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={handleDelete} className="bg-destructive text-destructive-foreground hover:bg-destructive/90">
              Delete clock
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </Layout>
  )
}

type Sub = NonNullable<ReturnType<typeof api.listSubscriptionsOnClock> extends Promise<{ data: infer T }> ? T : never>[number]

function AdvanceClockDialog({
  clock,
  subs,
  onClose,
}: {
  clock: TestClock
  subs: Sub[]
  onClose: () => void
}) {
  const queryClient = useQueryClient()
  // Times shown / picked in tenant TZ so the operator-picked "5pm
  // tomorrow" matches the tenant's clock and the displayed clock-time
  // stays internally consistent. ADR-010.
  const tz = getTenantTimezone() || 'UTC'
  // Date + time edited independently — branded DatePicker for the date
  // and a small text input for HH:mm. Defaults to "current frozen + 1h"
  // in tenant TZ so the picker isn't blank and isn't accidentally past.
  const defaultDate = useMemo(() => {
    const d = new Date(clock.frozen_time)
    d.setHours(d.getHours() + 1)
    return formatInTimeZone(d, tz, 'yyyy-MM-dd')
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [clock.frozen_time, tz])
  const defaultTime = useMemo(() => {
    const d = new Date(clock.frozen_time)
    d.setHours(d.getHours() + 1)
    return formatInTimeZone(d, tz, 'HH:mm')
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [clock.frozen_time, tz])
  const [datePart, setDatePart] = useState(defaultDate)
  const [timePart, setTimePart] = useState(defaultTime)
  const [submitting, setSubmitting] = useState(false)

  const presets: { label: string; ms: number }[] = [
    { label: '+1h', ms: 60 * 60 * 1000 },
    { label: '+1d', ms: 24 * 60 * 60 * 1000 },
    { label: '+1mo', ms: 31 * 24 * 60 * 60 * 1000 },
  ]
  const applyPreset = (ms: number) => {
    const d = new Date(new Date(clock.frozen_time).getTime() + ms)
    setDatePart(formatInTimeZone(d, tz, 'yyyy-MM-dd'))
    setTimePart(formatInTimeZone(d, tz, 'HH:mm'))
  }

  // Compose date + time in tenant TZ, then re-ground to UTC for both
  // the soft warning and the submit. Single source of truth for the
  // target instant.
  const targetIso = useMemo(() => {
    if (!datePart || !/^\d{2}:\d{2}$/.test(timePart)) return null
    return fromZonedTime(`${datePart}T${timePart}:00`, tz).toISOString()
  }, [datePart, timePart, tz])

  // Soft warning when the jump exceeds typical sub interval — Stripe caps
  // this hard, we just nudge.
  const overlongWarning = subs.length > 0 && targetIso !== null && (() => {
    const jumpMs = new Date(targetIso).getTime() - new Date(clock.frozen_time).getTime()
    return jumpMs > 31 * 24 * 60 * 60 * 1000
  })()

  const handleSubmit = async () => {
    if (!targetIso) {
      toast.error('Pick a valid date and time')
      return
    }
    if (new Date(targetIso) <= new Date(clock.frozen_time)) {
      toast.error('Target time must be after the current clock time')
      return
    }
    setSubmitting(true)
    try {
      await api.advanceTestClock(clock.id, targetIso)
      toast.success('Clock advancing — billing catchup running')
      queryClient.invalidateQueries({ queryKey: ['test-clocks', clock.id] })
      queryClient.invalidateQueries({ queryKey: ['test-clocks', clock.id, 'subscriptions'] })
      queryClient.invalidateQueries({ queryKey: ['invoices'] })
      onClose()
    } catch (err) {
      const msg = err instanceof ApiError ? err.message : err instanceof Error ? err.message : 'Failed to advance clock'
      toast.error(msg)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog open onOpenChange={open => { if (!open && !submitting) onClose() }}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Advance clock</DialogTitle>
          <DialogDescription>
            Move the clock forward and run billing catchup for every subscription pinned to it.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <div>
            <Label className="text-xs text-muted-foreground">Current clock time</Label>
            <p className="text-sm font-mono mt-1">{formatDateTime(clock.frozen_time)}</p>
          </div>

          <div>
            <Label className="mb-2 block">Quick jumps</Label>
            <div className="flex gap-2">
              {presets.map(p => (
                <Button key={p.label} type="button" size="sm" variant="outline" onClick={() => applyPreset(p.ms)}>
                  {p.label}
                </Button>
              ))}
            </div>
          </div>

          <div>
            <Label>Target clock time</Label>
            <div className="flex gap-2 mt-1.5">
              <DatePicker value={datePart} onChange={setDatePart} className="flex-1" />
              <Input
                type="time"
                value={timePart}
                onChange={e => setTimePart(e.target.value)}
                className="w-28"
              />
            </div>
            <p className="text-xs text-muted-foreground mt-1.5">Times in tenant timezone ({tz}).</p>
          </div>

          {overlongWarning && (
            <div className="px-3 py-2 rounded-md bg-amber-500/10 border border-amber-500/30 text-xs text-amber-900 dark:text-amber-200">
              <strong>Heads up:</strong> jumping more than ~1 month closes many billing cycles in a single
              run. Catchup may take a moment.
            </div>
          )}
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={onClose} disabled={submitting}>Cancel</Button>
          <Button onClick={handleSubmit} disabled={submitting}>
            {submitting ? <><Loader2 size={14} className="animate-spin mr-2" />Advancing…</> : 'Advance clock'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
