import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { toast } from 'sonner'
import { UserPlus, Users, Mail, Clock, Loader2, ShieldCheck } from 'lucide-react'

import { Layout } from '@/components/Layout'
import { useAuth } from '@/contexts/AuthContext'
import { membersApi, type MemberView, type InvitationView } from '@/lib/members'
import { formatDate, formatRelativeTime } from '@/lib/api'
import { applyApiError, showApiError } from '@/lib/formErrors'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent } from '@/components/ui/card'
import { CardListSkeleton } from '@/components/ui/TableSkeleton'
import { EmptyState } from '@/components/EmptyState'
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

const inviteSchema = z.object({
  email: z.string().trim().email('Enter a valid email address'),
})
type InviteData = z.infer<typeof inviteSchema>

function roleBadgeVariant(role: string): 'default' | 'secondary' | 'outline' {
  return role === 'owner' ? 'default' : 'secondary'
}

export default function MembersPage() {
  const [showInvite, setShowInvite] = useState(false)
  const [removeTarget, setRemoveTarget] = useState<MemberView | null>(null)
  const [revokeTarget, setRevokeTarget] = useState<InvitationView | null>(null)
  const queryClient = useQueryClient()
  const { user } = useAuth()

  const { data, isLoading, error, refetch } = useQuery({
    queryKey: ['members'],
    queryFn: () => membersApi.list(),
  })

  const members = data?.members ?? []
  const pending = (data?.invitations ?? []).filter(i => i.status === 'pending')
  const errMsg = error instanceof Error ? error.message : error ? String(error) : null

  const invalidate = () => queryClient.invalidateQueries({ queryKey: ['members'] })

  const handleRemove = async () => {
    if (!removeTarget) return
    try {
      await membersApi.removeMember(removeTarget.user_id)
      toast.success(`Removed ${removeTarget.email} from the workspace`)
      setRemoveTarget(null)
      invalidate()
    } catch (err) {
      showApiError(err, 'Failed to remove member')
    }
  }

  const handleRevoke = async () => {
    if (!revokeTarget) return
    try {
      await membersApi.revokeInvitation(revokeTarget.id)
      toast.success(`Revoked invitation for ${revokeTarget.email}`)
      setRevokeTarget(null)
      invalidate()
    } catch (err) {
      showApiError(err, 'Failed to revoke invitation')
    }
  }

  return (
    <Layout>
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold text-foreground">Members</h1>
          <p className="text-sm text-muted-foreground mt-1">
            Invite teammates and manage who has access to this workspace
            {members.length > 0 ? ` · ${members.length} active` : ''}
          </p>
        </div>
        <Button size="sm" onClick={() => setShowInvite(true)}>
          <UserPlus size={16} className="mr-2" />
          Invite member
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
      ) : (
        <>
          {/* Active members */}
          <section className="mt-6">
            <h2 className="text-xs uppercase tracking-wider text-muted-foreground mb-3">
              Active members
            </h2>
            {members.length === 0 ? (
              <Card>
                <CardContent className="p-0">
                  <EmptyState
                    icon={Users}
                    title="No members yet"
                    description="Invite your first teammate to start collaborating."
                    action={{
                      label: 'Invite member',
                      icon: UserPlus,
                      onClick: () => setShowInvite(true),
                    }}
                  />
                </CardContent>
              </Card>
            ) : (
              <div className="space-y-3">
                {members.map(m => {
                  const isSelf = m.user_id === user?.user_id
                  return (
                    <Card key={m.user_id}>
                      <CardContent className="px-6 py-4">
                        <div className="flex items-start justify-between">
                          <div className="flex items-start gap-3">
                            <div className="w-9 h-9 rounded-lg flex items-center justify-center shrink-0 bg-muted mt-0.5">
                              {m.role === 'owner'
                                ? <ShieldCheck size={16} className="text-primary" />
                                : <Users size={16} className="text-muted-foreground" />}
                            </div>
                            <div>
                              <div className="flex items-center gap-2">
                                <p className="text-sm font-medium text-foreground">
                                  {m.display_name || m.email}
                                </p>
                                {isSelf && <Badge variant="info" className="text-[10px]">You</Badge>}
                                <Badge variant={roleBadgeVariant(m.role)}>{m.role}</Badge>
                              </div>
                              {m.display_name && (
                                <p className="text-xs text-muted-foreground mt-0.5">{m.email}</p>
                              )}
                              <p className="text-xs text-muted-foreground mt-1">
                                Joined {formatRelativeTime(m.joined_at)}
                              </p>
                            </div>
                          </div>
                          {!isSelf && (
                            <Button
                              variant="outline"
                              size="sm"
                              className="shrink-0 text-destructive hover:text-destructive"
                              onClick={() => setRemoveTarget(m)}
                            >
                              Remove
                            </Button>
                          )}
                        </div>
                      </CardContent>
                    </Card>
                  )
                })}
              </div>
            )}
          </section>

          {/* Pending invitations */}
          {pending.length > 0 && (
            <section className="mt-8">
              <h2 className="text-xs uppercase tracking-wider text-muted-foreground mb-3">
                Pending invitations · {pending.length}
              </h2>
              <div className="space-y-3">
                {pending.map(inv => (
                  <Card key={inv.id}>
                    <CardContent className="px-6 py-4">
                      <div className="flex items-start justify-between">
                        <div className="flex items-start gap-3">
                          <div className="w-9 h-9 rounded-lg flex items-center justify-center shrink-0 bg-amber-50 dark:bg-amber-500/10 mt-0.5">
                            <Mail size={16} className="text-amber-600" />
                          </div>
                          <div>
                            <div className="flex items-center gap-2">
                              <p className="text-sm font-medium text-foreground">{inv.email}</p>
                              <Badge variant="outline">pending</Badge>
                              <Badge variant={roleBadgeVariant(inv.role)}>{inv.role}</Badge>
                            </div>
                            <div className="flex items-center gap-4 mt-1.5 text-xs text-muted-foreground">
                              {inv.invited_by_email && (
                                <span>Invited by {inv.invited_by_email}</span>
                              )}
                              <span className="flex items-center gap-1">
                                <Clock size={11} />
                                Expires {formatDate(inv.expires_at)}
                              </span>
                              <span>Sent {formatRelativeTime(inv.created_at)}</span>
                            </div>
                          </div>
                        </div>
                        <Button
                          variant="outline"
                          size="sm"
                          className="shrink-0 text-destructive hover:text-destructive"
                          onClick={() => setRevokeTarget(inv)}
                        >
                          Revoke
                        </Button>
                      </div>
                    </CardContent>
                  </Card>
                ))}
              </div>
            </section>
          )}
        </>
      )}

      {/* Invite dialog */}
      {showInvite && (
        <InviteDialog
          onClose={() => setShowInvite(false)}
          onSent={() => {
            setShowInvite(false)
            invalidate()
          }}
        />
      )}

      {/* Remove member confirm */}
      <AlertDialog open={!!removeTarget} onOpenChange={(open) => { if (!open) setRemoveTarget(null) }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Remove member</AlertDialogTitle>
            <AlertDialogDescription>
              {removeTarget
                ? `Remove ${removeTarget.display_name || removeTarget.email} from the workspace? They'll lose access immediately and any active sessions will be revoked.`
                : ''}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel onClick={() => setRemoveTarget(null)}>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={handleRemove}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              Remove member
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* Revoke invite confirm */}
      <AlertDialog open={!!revokeTarget} onOpenChange={(open) => { if (!open) setRevokeTarget(null) }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Revoke invitation</AlertDialogTitle>
            <AlertDialogDescription>
              {revokeTarget
                ? `Revoke the pending invitation for ${revokeTarget.email}? The existing link will stop working and you can send a fresh invite any time.`
                : ''}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel onClick={() => setRevokeTarget(null)}>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={handleRevoke}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              Revoke invitation
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </Layout>
  )
}

function InviteDialog({ onClose, onSent }: { onClose: () => void; onSent: () => void }) {
  const form = useForm<InviteData>({
    resolver: zodResolver(inviteSchema),
    defaultValues: { email: '' },
  })

  const onSubmit = form.handleSubmit(async (data) => {
    try {
      const inv = await membersApi.invite(data.email)
      toast.success(`Invitation sent to ${inv.email}`)
      onSent()
    } catch (err) {
      applyApiError(form, err, ['email'], {
        toastTitle: 'Failed to send invitation',
      })
    }
  })

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose() }}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Invite a member</DialogTitle>
          <DialogDescription>
            They'll receive an email with a link to join this workspace. Invitations expire in 72 hours.
          </DialogDescription>
        </DialogHeader>
        <Form {...form}>
          <form onSubmit={onSubmit} noValidate className="space-y-4">
            <FormField
              control={form.control}
              name="email"
              render={({ field }) => (
                <FormItem>
                  <FormLabel>Email</FormLabel>
                  <FormControl>
                    <Input
                      type="email"
                      placeholder="teammate@company.com"
                      autoComplete="off"
                      autoFocus
                      {...field}
                    />
                  </FormControl>
                  <FormDescription>
                    If they already have a Velox account with this email, they can accept using their existing password.
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
            <DialogFooter>
              <Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
              <Button type="submit" disabled={form.formState.isSubmitting}>
                {form.formState.isSubmitting ? (
                  <><Loader2 size={14} className="animate-spin mr-2" /> Sending...</>
                ) : (
                  'Send invitation'
                )}
              </Button>
            </DialogFooter>
          </form>
        </Form>
      </DialogContent>
    </Dialog>
  )
}
