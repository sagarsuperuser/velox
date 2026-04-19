import { useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { toast } from 'sonner'
import { api, formatDate, type WebhookEndpoint, type WebhookEvent } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { cn } from '@/lib/utils'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent } from '@/components/ui/card'
import { Checkbox } from '@/components/ui/checkbox'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
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
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { TableSkeleton } from '@/components/ui/TableSkeleton'

import { Loader2 } from 'lucide-react'

const createEndpointSchema = z.object({
  url: z.string().min(1, 'URL is required').refine(v => {
    try { const u = new URL(v); return u.protocol === 'https:' || u.protocol === 'http:' } catch { return false }
  }, 'Must be a valid URL'),
  description: z.string(),
})

type CreateEndpointData = z.infer<typeof createEndpointSchema>

export default function WebhooksPage() {
  const [searchParams, setSearchParams] = useSearchParams()
  const tab = (searchParams.get('tab') === 'events' ? 'events' : 'endpoints') as 'endpoints' | 'events'
  const setTab = (t: string) => setSearchParams(t === 'endpoints' ? {} : { tab: t })

  return (
    <Layout>
      <div>
        <h1 className="text-2xl font-semibold text-foreground">Webhooks</h1>
        <p className="text-sm text-muted-foreground mt-1">Manage outbound webhook endpoints and events</p>
      </div>

      <Tabs value={tab} onValueChange={setTab} className="mt-6">
        <TabsList>
          <TabsTrigger value="endpoints">Endpoints</TabsTrigger>
          <TabsTrigger value="events">Events</TabsTrigger>
        </TabsList>

        <TabsContent value="endpoints">
          <EndpointsTab />
        </TabsContent>
        <TabsContent value="events">
          <EventsTab />
        </TabsContent>
      </Tabs>
    </Layout>
  )
}

/* ─── Endpoints Tab ─── */

function EndpointsTab() {
  const [showCreate, setShowCreate] = useState(false)
  const [createdSecret, setCreatedSecret] = useState<string | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<WebhookEndpoint | null>(null)
  const queryClient = useQueryClient()

  const { data, isLoading: loading, error: loadError, refetch } = useQuery({
    queryKey: ['webhook-endpoints'],
    queryFn: () => Promise.all([
      api.listWebhookEndpoints(),
      api.getWebhookEndpointStats(),
    ]).then(([epRes, statsRes]) => {
      const map: Record<string, { total_deliveries: number; succeeded: number; failed: number; success_rate: number }> = {}
      for (const s of (statsRes.data || [])) {
        map[s.endpoint_id] = s
      }
      return { endpoints: epRes.data || [], stats: map }
    }),
  })

  const endpoints = data?.endpoints ?? []
  const stats = data?.stats ?? {}
  const errorMsg = loadError instanceof Error ? loadError.message : loadError ? String(loadError) : null

  const handleDelete = async () => {
    if (!deleteTarget) return
    try {
      await api.deleteWebhookEndpoint(deleteTarget.id)
      toast.success('Endpoint deleted')
      setDeleteTarget(null)
      queryClient.invalidateQueries({ queryKey: ['webhook-endpoints'] })
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to delete endpoint')
    }
  }

  return (
    <>
      {endpoints.length > 0 && (
        <div className="flex justify-end mt-4">
          <Button size="sm" onClick={() => setShowCreate(true)}>Add Endpoint</Button>
        </div>
      )}

      <Card className="mt-4">
        <CardContent className="p-0">
          {errorMsg ? (
            <div className="p-8 text-center">
              <p className="text-sm text-destructive mb-3">{errorMsg}</p>
              <Button variant="outline" size="sm" onClick={() => refetch()}>Retry</Button>
            </div>
          ) : loading ? (
            <TableSkeleton columns={7} />
          ) : endpoints.length === 0 ? (
            <div className="p-12 text-center">
              <p className="text-sm font-medium text-foreground">No webhook endpoints</p>
              <p className="text-sm text-muted-foreground mt-1">Add an endpoint to receive event notifications</p>
              <Button size="sm" className="mt-4" onClick={() => setShowCreate(true)}>Add Endpoint</Button>
            </div>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>URL</TableHead>
                  <TableHead>Description</TableHead>
                  <TableHead>Events</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead>Success Rate</TableHead>
                  <TableHead>Created</TableHead>
                  <TableHead className="text-right"></TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {endpoints.map(ep => (
                  <TableRow key={ep.id}>
                    <TableCell className="font-mono text-sm max-w-xs truncate" title={ep.url}>{ep.url}</TableCell>
                    <TableCell className="text-muted-foreground">{ep.description || '\u2014'}</TableCell>
                    <TableCell>
                      <div className="flex flex-wrap gap-1">
                        {(ep.events || []).map(ev => (
                          <Badge key={ev} variant="outline">{ev}</Badge>
                        ))}
                        {(!ep.events || ep.events.length === 0) && <span className="text-xs text-muted-foreground">all</span>}
                      </div>
                    </TableCell>
                    <TableCell>
                      <Badge variant={ep.active ? 'success' : 'secondary'}>{ep.active ? 'active' : 'paused'}</Badge>
                    </TableCell>
                    <TableCell className="font-medium">
                      {(() => {
                        const s = stats[ep.id]
                        if (!s || s.total_deliveries === 0) return <span className="text-muted-foreground">{'\u2014'}</span>
                        const rate = s.success_rate
                        const color = rate >= 95 ? 'text-green-600' : rate >= 70 ? 'text-amber-600' : 'text-red-600'
                        return <span className={color}>{rate.toFixed(1)}%</span>
                      })()}
                    </TableCell>
                    <TableCell className="text-muted-foreground">{formatDate(ep.created_at)}</TableCell>
                    <TableCell className="text-right">
                      <div className="flex items-center justify-end gap-1">
                        <Button variant="outline" size="sm" className="h-7 text-xs"
                          onClick={async () => {
                            try {
                              const res = await api.rotateWebhookSecret(ep.id)
                              setCreatedSecret(res.secret)
                              toast.success('Secret rotated')
                            } catch (err) {
                              toast.error(err instanceof Error ? err.message : 'Failed to rotate secret')
                            }
                          }}>
                          Rotate Secret
                        </Button>
                        <Button variant="outline" size="sm" className="h-7 text-xs text-destructive hover:text-destructive"
                          onClick={() => setDeleteTarget(ep)}>
                          Delete
                        </Button>
                      </div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      {showCreate && (
        <CreateEndpointDialog
          onClose={() => setShowCreate(false)}
          onCreated={(secret) => {
            setShowCreate(false)
            setCreatedSecret(secret)
            queryClient.invalidateQueries({ queryKey: ['webhook-endpoints'] })
            toast.success('Endpoint created')
          }}
        />
      )}

      {/* Secret display dialog */}
      {createdSecret && (
        <Dialog open onOpenChange={() => setCreatedSecret(null)}>
          <DialogContent className="sm:max-w-md">
            <DialogHeader>
              <DialogTitle>Signing Secret</DialogTitle>
            </DialogHeader>
            <div className="space-y-3">
              <p className="text-sm text-muted-foreground">
                Save this signing secret now. It will not be shown again.
              </p>
              <div className="bg-amber-50 dark:bg-amber-500/10 border border-amber-200 dark:border-amber-500/20 rounded-lg p-4">
                <p className="text-xs text-amber-700 dark:text-amber-400 font-medium mb-1">Signing Secret</p>
                <div className="flex items-start gap-2">
                  <p className="font-mono text-sm text-amber-900 dark:text-amber-300 break-all select-all flex-1">{createdSecret}</p>
                  <Button variant="outline" size="sm" className="shrink-0"
                    onClick={() => {
                      navigator.clipboard.writeText(createdSecret)
                      toast.success('Copied to clipboard')
                    }}>
                    Copy
                  </Button>
                </div>
              </div>
            </div>
            <DialogFooter>
              <Button onClick={() => setCreatedSecret(null)}>Done</Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      )}

      {/* Delete confirmation */}
      <AlertDialog open={!!deleteTarget} onOpenChange={(open) => { if (!open) setDeleteTarget(null) }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete Endpoint</AlertDialogTitle>
            <AlertDialogDescription>
              {deleteTarget ? `Are you sure you want to delete the endpoint "${deleteTarget.url}"?` : ''}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel onClick={() => setDeleteTarget(null)}>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={handleDelete} className="bg-destructive text-destructive-foreground hover:bg-destructive/90">
              Delete
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}

/* ─── Event Groups ─── */

const EVENT_GROUPS: { label: string; events: { type: string; description: string }[] }[] = [
  {
    label: 'Invoice',
    events: [
      { type: 'invoice.created', description: 'Invoice created' },
      { type: 'invoice.finalized', description: 'Invoice finalized and ready for payment' },
      { type: 'invoice.paid', description: 'Invoice marked as paid' },
      { type: 'invoice.voided', description: 'Invoice voided' },
    ],
  },
  {
    label: 'Payment',
    events: [
      { type: 'payment.succeeded', description: 'Payment collected successfully' },
      { type: 'payment.failed', description: 'Payment attempt failed' },
    ],
  },
  {
    label: 'Subscription',
    events: [
      { type: 'subscription.created', description: 'Subscription created' },
      { type: 'subscription.activated', description: 'Subscription activated' },
      { type: 'subscription.canceled', description: 'Subscription canceled' },
      { type: 'subscription.paused', description: 'Subscription paused' },
      { type: 'subscription.resumed', description: 'Subscription resumed' },
    ],
  },
  {
    label: 'Customer',
    events: [
      { type: 'customer.created', description: 'Customer created' },
    ],
  },
  {
    label: 'Dunning',
    events: [
      { type: 'dunning.started', description: 'Dunning process started' },
      { type: 'dunning.escalated', description: 'Dunning escalated after retries exhausted' },
      { type: 'dunning.resolved', description: 'Dunning resolved (payment recovered)' },
    ],
  },
  {
    label: 'Credit',
    events: [
      { type: 'credit.granted', description: 'Credit granted to customer' },
      { type: 'credit_note.issued', description: 'Credit note issued' },
    ],
  },
]

const ALL_EVENT_TYPES = EVENT_GROUPS.flatMap(g => g.events.map(e => e.type))

/* ─── Create Endpoint Dialog ─── */

function CreateEndpointDialog({ onClose, onCreated }: { onClose: () => void; onCreated: (secret: string) => void }) {
  const [listenMode, setListenMode] = useState<'all' | 'specific'>('all')
  const [selectedEvents, setSelectedEvents] = useState<Set<string>>(new Set())
  const [error, setError] = useState('')

  const form = useForm<CreateEndpointData>({
    resolver: zodResolver(createEndpointSchema),
    defaultValues: { url: '', description: '' },
  })

  const toggleEvent = (eventType: string) => {
    setSelectedEvents(prev => {
      const next = new Set(prev)
      if (next.has(eventType)) next.delete(eventType)
      else next.add(eventType)
      return next
    })
  }

  const toggleGroup = (group: typeof EVENT_GROUPS[number]) => {
    const groupTypes = group.events.map(e => e.type)
    const allSelected = groupTypes.every(t => selectedEvents.has(t))
    setSelectedEvents(prev => {
      const next = new Set(prev)
      groupTypes.forEach(t => allSelected ? next.delete(t) : next.add(t))
      return next
    })
  }

  const onSubmit = form.handleSubmit(async (data) => {
    setError('')
    try {
      const eventList = listenMode === 'all' ? undefined : Array.from(selectedEvents)
      if (listenMode === 'specific' && (!eventList || eventList.length === 0)) {
        setError('Select at least one event')
        return
      }
      const res = await api.createWebhookEndpoint({
        url: data.url,
        description: data.description || undefined,
        events: eventList,
      })
      onCreated(res.secret)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create endpoint')
    }
  })

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose() }}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Add Webhook Endpoint</DialogTitle>
          <DialogDescription>Configure a URL to receive event notifications</DialogDescription>
        </DialogHeader>
        <Form {...form}>
        <form onSubmit={onSubmit} noValidate className="space-y-4">
          <FormField
            control={form.control}
            name="url"
            render={({ field }) => (
              <FormItem>
                <FormLabel>URL</FormLabel>
                <FormControl>
                  <Input type="url" placeholder="https://example.com/webhooks" maxLength={2048} {...field} />
                </FormControl>
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
                  <Input placeholder="Production webhook" maxLength={500} {...field} />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />

          {/* Event selection */}
          <div>
            <Label className="mb-2 block">Events to send</Label>
            <div className="flex gap-4 mb-3">
              <label className="flex items-center gap-2 cursor-pointer">
                <input type="radio" name="listenMode" checked={listenMode === 'all'}
                  onChange={() => setListenMode('all')}
                  className="w-4 h-4 text-primary" />
                <span className="text-sm">Listen to all events</span>
              </label>
              <label className="flex items-center gap-2 cursor-pointer">
                <input type="radio" name="listenMode" checked={listenMode === 'specific'}
                  onChange={() => setListenMode('specific')}
                  className="w-4 h-4 text-primary" />
                <span className="text-sm">Select specific events</span>
              </label>
            </div>

            {listenMode === 'specific' && (
              <div className="border border-border rounded-lg max-h-72 overflow-y-auto">
                {/* Select all bar */}
                <div className="flex items-center justify-between px-4 py-2.5 bg-muted border-b border-border sticky top-0 z-10">
                  <label className="flex items-center gap-2 cursor-pointer">
                    <Checkbox
                      checked={selectedEvents.size === ALL_EVENT_TYPES.length}
                      onCheckedChange={() => {
                        if (selectedEvents.size === ALL_EVENT_TYPES.length) setSelectedEvents(new Set())
                        else setSelectedEvents(new Set(ALL_EVENT_TYPES))
                      }}
                    />
                    <span className="text-sm font-medium">Select all</span>
                  </label>
                  <span className="text-xs text-muted-foreground">{selectedEvents.size} of {ALL_EVENT_TYPES.length} selected</span>
                </div>

                {EVENT_GROUPS.map(group => {
                  const groupTypes = group.events.map(e => e.type)
                  const selectedCount = groupTypes.filter(t => selectedEvents.has(t)).length
                  const allSelected = selectedCount === groupTypes.length

                  return (
                    <div key={group.label} className="border-b border-border last:border-b-0">
                      {/* Group header */}
                      <div className="flex items-center gap-2 px-4 py-2 bg-background">
                        <Checkbox
                          checked={allSelected}
                          onCheckedChange={() => toggleGroup(group)}
                        />
                        <span className="text-sm font-semibold text-foreground">{group.label}</span>
                        <span className="text-xs text-muted-foreground ml-auto">{selectedCount}/{groupTypes.length}</span>
                      </div>

                      {/* Individual events */}
                      {group.events.map(ev => (
                        <label key={ev.type}
                          className="flex items-start gap-2 px-4 py-1.5 pl-10 cursor-pointer hover:bg-accent transition-colors">
                          <Checkbox
                            checked={selectedEvents.has(ev.type)}
                            onCheckedChange={() => toggleEvent(ev.type)}
                            className="mt-0.5 shrink-0"
                          />
                          <div className="min-w-0">
                            <p className="text-sm font-mono">{ev.type}</p>
                            <p className="text-xs text-muted-foreground">{ev.description}</p>
                          </div>
                        </label>
                      ))}
                    </div>
                  )
                })}
              </div>
            )}
          </div>

          {error && (
            <div className="px-3 py-2 rounded-md bg-destructive/10 border border-destructive/20">
              <p className="text-destructive text-sm">{error}</p>
            </div>
          )}

          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
            <Button type="submit" disabled={form.formState.isSubmitting}>
              {form.formState.isSubmitting ? (
                <><Loader2 size={14} className="animate-spin mr-2" /> Creating...</>
              ) : (
                'Create Endpoint'
              )}
            </Button>
          </DialogFooter>
        </form>
        </Form>
      </DialogContent>
    </Dialog>
  )
}

/* ─── Events Tab ─── */

function EventsTab() {
  const { data: eventsData, isLoading: loading, error: loadError, refetch } = useQuery({
    queryKey: ['webhook-events'],
    queryFn: () => api.listWebhookEvents(),
  })

  const events = eventsData?.data ?? []
  const errorMsg = loadError instanceof Error ? loadError.message : loadError ? String(loadError) : null

  const handleReplay = async (id: string) => {
    try {
      await api.replayWebhookEvent(id)
      toast.success('Event replayed')
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to replay event')
    }
  }

  return (
    <Card className="mt-4">
      <CardContent className="p-0">
        {errorMsg ? (
          <div className="p-8 text-center">
            <p className="text-sm text-destructive mb-3">{errorMsg}</p>
            <Button variant="outline" size="sm" onClick={() => refetch()}>Retry</Button>
          </div>
        ) : loading ? (
          <TableSkeleton columns={4} />
        ) : events.length === 0 ? (
          <div className="p-12 text-center">
            <p className="text-sm font-medium text-foreground">No webhook events</p>
            <p className="text-sm text-muted-foreground mt-1">Events will appear here as they are sent to your endpoints</p>
          </div>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Event Type</TableHead>
                <TableHead>Event ID</TableHead>
                <TableHead>Created</TableHead>
                <TableHead className="text-right"></TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {events.map(ev => (
                <TableRow key={ev.id}>
                  <TableCell><Badge variant="outline">{ev.event_type}</Badge></TableCell>
                  <TableCell className="font-mono text-muted-foreground">{ev.id.slice(0, 12)}...</TableCell>
                  <TableCell className="text-muted-foreground">{formatDate(ev.created_at)}</TableCell>
                  <TableCell className="text-right">
                    <Button variant="outline" size="sm" className="h-7 text-xs" onClick={() => handleReplay(ev.id)}>
                      Replay
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  )
}
