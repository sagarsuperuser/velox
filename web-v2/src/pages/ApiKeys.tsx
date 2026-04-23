import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { toast } from 'sonner'
import { api, formatDate, formatRelativeTime, type ApiKeyInfo } from '@/lib/api'
import { applyApiError, showApiError } from '@/lib/formErrors'
import { ExpiryBadge } from '@/components/ExpiryBadge'
import { Layout } from '@/components/Layout'
import { cn } from '@/lib/utils'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { DatePicker } from '@/components/ui/date-picker'
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
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { CardListSkeleton } from '@/components/ui/TableSkeleton'

import { Plus, Key, Shield, Eye, ChevronDown, Loader2, Clock } from 'lucide-react'
import { EmptyState } from '@/components/EmptyState'

const createApiKeySchema = z.object({
  name: z.string().min(1, 'Name is required'),
})

type CreateApiKeyData = z.infer<typeof createApiKeySchema>

function relativeTime(dateStr: string): string {
  const seconds = Math.floor((Date.now() - new Date(dateStr).getTime()) / 1000)
  const days = Math.floor(seconds / 86400)
  if (days < 7) return formatRelativeTime(dateStr)
  return formatDate(dateStr)
}

function keyTypeVariant(type: string): 'default' | 'secondary' | 'outline' {
  switch (type) {
    case 'secret': return 'default'
    case 'publishable': return 'secondary'
    default: return 'outline'
  }
}

function isExpired(key: ApiKeyInfo): boolean {
  return !!key.expires_at && new Date(key.expires_at) < new Date()
}

export default function ApiKeysPage() {
  const [showCreate, setShowCreate] = useState(false)
  const [createdKey, setCreatedKey] = useState<string | null>(null)
  const [revokeTarget, setRevokeTarget] = useState<ApiKeyInfo | null>(null)
  const [isRevokingSelf, setIsRevokingSelf] = useState(false)
  const [showRevoked, setShowRevoked] = useState(false)
  const [showExpired, setShowExpired] = useState(false)
  const queryClient = useQueryClient()

  let currentKeyPrefix = ''
  try { currentKeyPrefix = localStorage.getItem('velox_api_key')?.slice(0, 20) || '' }
  catch { /* Private browsing mode */ }

  const { data: keysData, isLoading: loading, error: loadError, refetch } = useQuery({
    queryKey: ['api-keys'],
    queryFn: () => api.listApiKeys(),
  })

  const keys = keysData?.data ?? []
  const errorMsg = loadError instanceof Error ? loadError.message : loadError ? String(loadError) : null

  const handleRevoke = async () => {
    if (!revokeTarget) return
    try {
      await api.revokeApiKey(revokeTarget.id)
      toast.success('API key revoked')
      setRevokeTarget(null)
      queryClient.invalidateQueries({ queryKey: ['api-keys'] })
    } catch (err) {
      showApiError(err, 'Failed to revoke key')
    }
  }

  const activeKeys = keys.filter(k => !k.revoked_at && !isExpired(k))
  const expiredKeys = keys.filter(k => !k.revoked_at && isExpired(k))
  const revokedKeys = keys.filter(k => !!k.revoked_at)

  return (
    <Layout>
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold text-foreground">API Keys</h1>
          <p className="text-sm text-muted-foreground mt-1">Manage API authentication keys{activeKeys.length > 0 ? ` · ${activeKeys.length} active` : ''}</p>
        </div>
        <Button size="sm" onClick={() => setShowCreate(true)}>
          <Plus size={16} className="mr-2" />
          Create API Key
        </Button>
      </div>

      {errorMsg ? (
        <Card className="mt-6">
          <CardContent className="p-8 text-center">
            <p className="text-sm text-destructive mb-3">{errorMsg}</p>
            <Button variant="outline" size="sm" onClick={() => refetch()}>Retry</Button>
          </CardContent>
        </Card>
      ) : loading ? (
        <div className="mt-6">
          <CardListSkeleton rows={3} />
        </div>
      ) : keys.length === 0 ? (
        <Card className="mt-6">
          <CardContent className="p-0">
            <EmptyState
              icon={Key}
              title="No API keys yet"
              description="Create an API key to start using the Velox API from your backend or CLI."
              action={{
                label: 'Create API Key',
                icon: Plus,
                onClick: () => setShowCreate(true),
              }}
            />
          </CardContent>
        </Card>
      ) : (
        <>
          {/* Active keys */}
          <div className="mt-6 space-y-3">
            {activeKeys.map(k => {
              const isCurrent = currentKeyPrefix && k.key_prefix && currentKeyPrefix.startsWith(k.key_prefix)
              return (
                <Card key={k.id} className={cn(isCurrent && 'ring-2 ring-primary/20')}>
                  <CardContent className="px-6 py-4">
                    <div className="flex items-start justify-between">
                      <div className="flex items-start gap-3">
                        <div className={cn(
                          'w-9 h-9 rounded-lg flex items-center justify-center shrink-0 mt-0.5',
                          k.key_type === 'secret' ? 'bg-violet-50 dark:bg-violet-500/10' : 'bg-blue-50 dark:bg-blue-500/10'
                        )}>
                          {k.key_type === 'secret'
                            ? <Shield size={16} className="text-violet-500" />
                            : <Eye size={16} className="text-blue-500" />}
                        </div>
                        <div>
                          <div className="flex items-center gap-2">
                            <p className="text-sm font-medium text-foreground">{k.name}</p>
                            {isCurrent && (
                              <Badge variant="info" className="text-[10px]">Current session</Badge>
                            )}
                            <ExpiryBadge expiresAt={k.expires_at} />
                          </div>
                          <code className="text-xs font-mono text-muted-foreground bg-muted px-2 py-0.5 rounded mt-1 inline-block">
                            {k.key_prefix}--------
                          </code>
                          <div className="flex items-center gap-4 mt-2">
                            <Badge variant={keyTypeVariant(k.key_type)}>{k.key_type}</Badge>
                            <span className="text-xs text-muted-foreground">Created {relativeTime(k.created_at)}</span>
                            <span className="text-xs text-muted-foreground">
                              {k.last_used_at ? `Last used ${relativeTime(k.last_used_at)}` : 'Never used'}
                            </span>
                            {k.expires_at && (
                              <span className="text-xs text-muted-foreground flex items-center gap-1">
                                <Clock size={11} />
                                Expires {formatDate(k.expires_at)}
                              </span>
                            )}
                          </div>
                          <p className="text-xs text-muted-foreground mt-1.5">
                            {k.key_type === 'secret'
                              ? 'Full access -- use server-side only. Never expose in client code.'
                              : 'Read-only access -- safe for frontend and client-side use.'}
                          </p>
                        </div>
                      </div>
                      <Button variant="outline" size="sm"
                        className="shrink-0 text-destructive hover:text-destructive"
                        onClick={() => {
                          setIsRevokingSelf(!!isCurrent)
                          setRevokeTarget(k)
                        }}>
                        Revoke
                      </Button>
                    </div>
                  </CardContent>
                </Card>
              )
            })}
          </div>

          {/* Expired keys */}
          {expiredKeys.length > 0 && (
            <div className="mt-6">
              <button onClick={() => setShowExpired(!showExpired)}
                className="flex items-center gap-2 text-sm text-muted-foreground hover:text-foreground transition-colors">
                <ChevronDown size={14} className={cn('transition-transform', showExpired && 'rotate-180')} />
                {expiredKeys.length} expired key{expiredKeys.length !== 1 ? 's' : ''}
              </button>
              {showExpired && (
                <div className="mt-3 space-y-2">
                  {expiredKeys.map(k => (
                    <Card key={k.id} className="opacity-60">
                      <CardContent className="px-6 py-3">
                        <div className="flex items-center justify-between">
                          <div className="flex items-center gap-3">
                            <p className="text-sm text-muted-foreground">{k.name}</p>
                            <code className="text-xs font-mono text-muted-foreground">{k.key_prefix}----</code>
                            <Badge variant="destructive">expired</Badge>
                            <span className="text-xs text-muted-foreground">
                              Expired {k.expires_at ? relativeTime(k.expires_at) : ''}
                            </span>
                          </div>
                          <Button variant="outline" size="sm"
                            className="shrink-0 text-destructive hover:text-destructive"
                            onClick={() => {
                              setIsRevokingSelf(false)
                              setRevokeTarget(k)
                            }}>
                            Revoke
                          </Button>
                        </div>
                      </CardContent>
                    </Card>
                  ))}
                </div>
              )}
            </div>
          )}

          {/* Revoked keys */}
          {revokedKeys.length > 0 && (
            <div className="mt-6">
              <button onClick={() => setShowRevoked(!showRevoked)}
                className="flex items-center gap-2 text-sm text-muted-foreground hover:text-foreground transition-colors">
                <ChevronDown size={14} className={cn('transition-transform', showRevoked && 'rotate-180')} />
                {revokedKeys.length} revoked key{revokedKeys.length !== 1 ? 's' : ''}
              </button>
              {showRevoked && (
                <div className="mt-3 space-y-2">
                  {revokedKeys.map(k => (
                    <Card key={k.id} className="opacity-60">
                      <CardContent className="px-6 py-3">
                        <div className="flex items-center justify-between">
                          <div className="flex items-center gap-3">
                            <p className="text-sm text-muted-foreground line-through">{k.name}</p>
                            <code className="text-xs font-mono text-muted-foreground">{k.key_prefix}----</code>
                            <Badge variant="destructive">revoked</Badge>
                          </div>
                          <span className="text-xs text-muted-foreground">Revoked {k.revoked_at ? relativeTime(k.revoked_at) : ''}</span>
                        </div>
                      </CardContent>
                    </Card>
                  ))}
                </div>
              )}
            </div>
          )}
        </>
      )}

      {/* Create key dialog */}
      {showCreate && (
        <CreateKeyDialog
          onClose={() => setShowCreate(false)}
          onCreated={(rawKey) => {
            setShowCreate(false)
            setCreatedKey(rawKey)
            queryClient.invalidateQueries({ queryKey: ['api-keys'] })
            toast.success('API key created')
          }}
        />
      )}

      {/* Show created key */}
      {createdKey && (
        <Dialog open onOpenChange={() => setCreatedKey(null)}>
          <DialogContent className="sm:max-w-md">
            <DialogHeader>
              <DialogTitle>API Key Created</DialogTitle>
            </DialogHeader>
            <div className="space-y-4">
              <div className="bg-amber-50 dark:bg-amber-500/10 border border-amber-200 dark:border-amber-500/20 rounded-xl p-4">
                <div className="flex items-start gap-3">
                  <div className="w-8 h-8 rounded-lg bg-amber-100 dark:bg-amber-500/20 flex items-center justify-center shrink-0">
                    <Key size={16} className="text-amber-600" />
                  </div>
                  <div className="flex-1 min-w-0">
                    <p className="text-xs font-semibold text-amber-800 dark:text-amber-400">Save this key now -- it will not be shown again</p>
                    <div className="flex items-start gap-2 mt-2">
                      <code className="font-mono text-sm text-amber-900 dark:text-amber-300 break-all select-all flex-1 bg-amber-100/50 dark:bg-amber-500/10 rounded px-2 py-1">
                        {createdKey}
                      </code>
                      <Button variant="outline" size="sm" className="shrink-0"
                        onClick={() => {
                          navigator.clipboard.writeText(createdKey)
                          toast.success('Copied to clipboard')
                        }}>
                        Copy
                      </Button>
                    </div>
                  </div>
                </div>
              </div>
              <DialogFooter>
                <Button onClick={() => setCreatedKey(null)}>I've saved this key</Button>
              </DialogFooter>
            </div>
          </DialogContent>
        </Dialog>
      )}

      {/* Revoke confirmation */}
      <AlertDialog open={!!revokeTarget} onOpenChange={(open) => { if (!open) { setRevokeTarget(null); setIsRevokingSelf(false) } }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{isRevokingSelf ? 'Revoke Current Session Key?' : 'Revoke API Key'}</AlertDialogTitle>
            <AlertDialogDescription>
              {revokeTarget
                ? isRevokingSelf
                  ? "This is the API key you're currently logged in with. Revoking it will log you out immediately. Are you sure?"
                  : `Are you sure you want to revoke "${revokeTarget.name}" (${revokeTarget.key_prefix}...)? This action cannot be undone.`
                : ''}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel onClick={() => { setRevokeTarget(null); setIsRevokingSelf(false) }}>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={handleRevoke} className="bg-destructive text-destructive-foreground hover:bg-destructive/90">
              Revoke Key
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </Layout>
  )
}

/* --- Create Key Dialog --- */

type ExpiryPreset = 'never' | '30d' | '90d' | '1y' | 'custom'

function addDays(days: number): string {
  const d = new Date()
  d.setDate(d.getDate() + days)
  return d.toISOString()
}

function CreateKeyDialog({ onClose, onCreated }: { onClose: () => void; onCreated: (rawKey: string) => void }) {
  const [keyType, setKeyType] = useState('secret')
  const [expiryPreset, setExpiryPreset] = useState<ExpiryPreset>('never')
  const [customDate, setCustomDate] = useState('')
  const [customDateError, setCustomDateError] = useState('')

  const form = useForm<CreateApiKeyData>({
    resolver: zodResolver(createApiKeySchema),
    defaultValues: { name: '' },
  })

  function getExpiresAt(): string | undefined {
    switch (expiryPreset) {
      case '30d': return addDays(30)
      case '90d': return addDays(90)
      case '1y': return addDays(365)
      case 'custom': return customDate ? new Date(customDate + 'T23:59:59').toISOString() : undefined
      default: return undefined
    }
  }

  const onSubmit = form.handleSubmit(async (data) => {
    setCustomDateError('')
    if (expiryPreset === 'custom' && !customDate) {
      setCustomDateError('Please select an expiration date')
      return
    }
    try {
      const res = await api.createApiKey({
        name: data.name,
        key_type: keyType,
        expires_at: getExpiresAt(),
      })
      onCreated(res.raw_key)
    } catch (err) {
      applyApiError(form, err, ['name', 'key_type', 'expires_at'], {
        toastTitle: 'Failed to create API key',
      })
    }
  })

  const presets: { value: ExpiryPreset; label: string }[] = [
    { value: 'never', label: 'No expiration' },
    { value: '30d', label: '30 days' },
    { value: '90d', label: '90 days' },
    { value: '1y', label: '1 year' },
    { value: 'custom', label: 'Custom' },
  ]

  // Min date for custom picker = tomorrow
  const tomorrow = new Date()
  tomorrow.setDate(tomorrow.getDate() + 1)

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose() }}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Create API Key</DialogTitle>
          <DialogDescription>Generate a new key for programmatic API access</DialogDescription>
        </DialogHeader>
        <Form {...form}>
          <form onSubmit={onSubmit} noValidate className="space-y-4">
            <FormField
              control={form.control}
              name="name"
              render={({ field }) => (
                <FormItem>
                  <FormLabel>Name</FormLabel>
                  <FormControl>
                    <Input placeholder="e.g. Production, Staging, CI/CD" maxLength={100} {...field} />
                  </FormControl>
                  <FormDescription>A descriptive name to identify this key</FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <div>
              <Label className="mb-2 block">Key Type</Label>
              <div className="grid grid-cols-2 gap-3">
                <button type="button" onClick={() => setKeyType('secret')}
                  className={cn(
                    'flex items-start gap-3 p-3 rounded-xl border-2 text-left transition-colors',
                    keyType === 'secret' ? 'border-primary bg-primary/5' : 'border-border hover:border-border/80'
                  )}>
                  <Shield size={18} className={cn('mt-0.5', keyType === 'secret' ? 'text-primary' : 'text-muted-foreground')} />
                  <div>
                    <p className={cn('text-sm font-medium', keyType === 'secret' ? 'text-primary' : 'text-foreground')}>Secret</p>
                    <p className="text-xs text-muted-foreground mt-0.5">Full access. Server-side only.</p>
                  </div>
                </button>
                <button type="button" onClick={() => setKeyType('publishable')}
                  className={cn(
                    'flex items-start gap-3 p-3 rounded-xl border-2 text-left transition-colors',
                    keyType === 'publishable' ? 'border-primary bg-primary/5' : 'border-border hover:border-border/80'
                  )}>
                  <Eye size={18} className={cn('mt-0.5', keyType === 'publishable' ? 'text-primary' : 'text-muted-foreground')} />
                  <div>
                    <p className={cn('text-sm font-medium', keyType === 'publishable' ? 'text-primary' : 'text-foreground')}>Publishable</p>
                    <p className="text-xs text-muted-foreground mt-0.5">Read-only. Safe for clients.</p>
                  </div>
                </button>
              </div>
            </div>

            <div>
              <Label className="mb-2 block">Expiration</Label>
              <div className="flex flex-wrap gap-2">
                {presets.map(p => (
                  <button
                    key={p.value}
                    type="button"
                    onClick={() => setExpiryPreset(p.value)}
                    className={cn(
                      'px-3 py-1.5 rounded-lg text-sm border transition-colors',
                      expiryPreset === p.value
                        ? 'border-primary bg-primary/5 text-primary font-medium'
                        : 'border-border text-muted-foreground hover:text-foreground hover:border-border/80'
                    )}
                  >
                    {p.label}
                  </button>
                ))}
              </div>
              {expiryPreset === 'custom' && (
                <div className="mt-3">
                  <DatePicker
                    value={customDate}
                    onChange={(d) => { setCustomDate(d); setCustomDateError('') }}
                    placeholder="Select expiration date"
                    className="w-56"
                    minDate={tomorrow}
                  />
                </div>
              )}
              {expiryPreset !== 'never' && expiryPreset !== 'custom' && (
                <p className="text-xs text-muted-foreground mt-2">
                  Key will expire on {new Date(getExpiresAt()!).toLocaleDateString('en-US', { month: 'long', day: 'numeric', year: 'numeric' })}
                </p>
              )}
              {customDateError && <p className="text-destructive text-sm mt-2">{customDateError}</p>}
            </div>

            <DialogFooter>
              <Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
              <Button type="submit" disabled={form.formState.isSubmitting}>
                {form.formState.isSubmitting ? (
                  <><Loader2 size={14} className="animate-spin mr-2" /> Creating...</>
                ) : (
                  'Create Key'
                )}
              </Button>
            </DialogFooter>
          </form>
        </Form>
      </DialogContent>
    </Dialog>
  )
}
