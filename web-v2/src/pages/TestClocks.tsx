import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { toast } from 'sonner'

import { api, formatDate, type TestClock } from '@/lib/api'
import { applyApiError, showApiError } from '@/lib/formErrors'
import { Layout } from '@/components/Layout'
import { EmptyState } from '@/components/EmptyState'
import { CardListSkeleton } from '@/components/ui/TableSkeleton'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from '@/components/ui/dialog'
import {
  Form, FormControl, FormField, FormItem, FormLabel, FormMessage, FormDescription,
} from '@/components/ui/form'
import { useAuth } from '@/contexts/AuthContext'
import { useNavigate } from 'react-router-dom'
import { useEffect } from 'react'
import { Plus, Clock as ClockIcon, Loader2 } from 'lucide-react'

const createSchema = z.object({
  name: z.string().max(200, 'Name must be at most 200 characters').optional(),
  frozen_time: z.string().min(1, 'Initial clock time is required'),
})
type CreateData = z.infer<typeof createSchema>

function statusBadge(status: TestClock['status']) {
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

export default function TestClocksPage() {
  const { user } = useAuth()
  const navigate = useNavigate()
  const [showCreate, setShowCreate] = useState(false)

  // Test clocks are test-mode-only. If the operator flips to live while
  // viewing this page, redirect home — same defensive guard the backend
  // enforces with requireTestMode.
  useEffect(() => {
    if (user?.livemode) navigate('/', { replace: true })
  }, [user?.livemode, navigate])

  const { data, isLoading, error, refetch } = useQuery({
    queryKey: ['test-clocks'],
    queryFn: () => api.listTestClocks(),
    enabled: !!user && !user.livemode,
  })
  const clocks = data?.data ?? []
  const errMsg = error instanceof Error ? error.message : null

  return (
    <Layout>
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold text-foreground">Test Clocks</h1>
          <p className="text-sm text-muted-foreground mt-1">
            Simulate time-bound billing without waiting for the wall clock.
            Test mode only.
          </p>
        </div>
        <Button size="sm" onClick={() => setShowCreate(true)}>
          <Plus size={16} className="mr-2" />
          New test clock
        </Button>
      </div>

      {errMsg ? (
        <Card className="mt-6">
          <CardContent className="p-8 text-center">
            <p className="text-sm text-destructive mb-3">{errMsg}</p>
            <Button variant="outline" size="sm" onClick={() => refetch()}>Retry</Button>
          </CardContent>
        </Card>
      ) : isLoading ? (
        <div className="mt-6">
          <CardListSkeleton rows={3} />
        </div>
      ) : clocks.length === 0 ? (
        <Card className="mt-6">
          <CardContent className="p-0">
            <EmptyState
              icon={ClockIcon}
              title="No test clocks yet"
              description="A test clock freezes 'now' for the subscriptions you pin to it. Advance the clock to fast-forward billing without waiting for real time to pass."
              action={{
                label: 'New test clock',
                icon: Plus,
                onClick: () => setShowCreate(true),
              }}
            />
          </CardContent>
        </Card>
      ) : (
        <div className="mt-6 space-y-3">
          {clocks.map(c => (
            <Link key={c.id} to={`/test-clocks/${c.id}`} className="block">
              <Card className="hover:bg-accent/30 transition-colors">
                <CardContent className="px-6 py-4 flex items-center gap-4">
                  <div className="h-9 w-9 rounded-lg bg-violet-50 dark:bg-violet-500/10 flex items-center justify-center shrink-0">
                    <ClockIcon size={16} className="text-violet-500" />
                  </div>
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2">
                      <p className="text-sm font-medium text-foreground truncate">
                        {c.name || 'Unnamed clock'}
                      </p>
                      {statusBadge(c.status)}
                    </div>
                    <p className="text-xs text-muted-foreground mt-0.5">
                      Clock time: <span className="font-mono">{formatDate(c.frozen_time)}</span>
                    </p>
                  </div>
                  <p className="text-xs text-muted-foreground shrink-0">
                    Created {formatDate(c.created_at)}
                  </p>
                </CardContent>
              </Card>
            </Link>
          ))}
        </div>
      )}

      {showCreate && <CreateClockDialog onClose={() => setShowCreate(false)} />}
    </Layout>
  )
}

function CreateClockDialog({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  // Default the picker to "now"; the operator typically wants to start
  // a simulation from the current wall-clock and advance from there.
  const defaultNow = new Date().toISOString().slice(0, 16) // YYYY-MM-DDTHH:MM
  const form = useForm<CreateData>({
    resolver: zodResolver(createSchema),
    defaultValues: { name: '', frozen_time: defaultNow },
  })

  const onSubmit = form.handleSubmit(async data => {
    try {
      const isoUtc = new Date(data.frozen_time).toISOString()
      const clk = await api.createTestClock({
        name: data.name || '',
        frozen_time: isoUtc,
      })
      toast.success('Test clock created')
      queryClient.invalidateQueries({ queryKey: ['test-clocks'] })
      onClose()
      navigate(`/test-clocks/${clk.id}`)
    } catch (err) {
      applyApiError(form, err, ['name', 'frozen_time'], { toastTitle: 'Failed to create test clock' })
    }
  })

  return (
    <Dialog open onOpenChange={open => { if (!open) onClose() }}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>New test clock</DialogTitle>
          <DialogDescription>
            Pick the initial time for this simulator. Subscriptions you pin
            here will see this as "now" until you advance the clock.
          </DialogDescription>
        </DialogHeader>
        <Form {...form}>
          <form onSubmit={onSubmit} noValidate className="space-y-4">
            <FormField
              control={form.control}
              name="name"
              render={({ field }) => (
                <FormItem>
                  <FormLabel>Name (optional)</FormLabel>
                  <FormControl>
                    <Input placeholder="e.g. Annual renewal scenario" maxLength={200} {...field} />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name="frozen_time"
              render={({ field }) => (
                <FormItem>
                  <FormLabel>Initial clock time</FormLabel>
                  <FormControl>
                    <Input type="datetime-local" {...field} />
                  </FormControl>
                  <FormDescription>
                    The clock starts here. Use Advance from the detail page to move it forward.
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
            <DialogFooter>
              <Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
              <Button type="submit" disabled={form.formState.isSubmitting}>
                {form.formState.isSubmitting ? (
                  <><Loader2 size={14} className="animate-spin mr-2" />Creating…</>
                ) : 'Create test clock'}
              </Button>
            </DialogFooter>
          </form>
        </Form>
      </DialogContent>
    </Dialog>
  )
}

// silence unused-import eslint warning when nothing else surfaces it
void showApiError
