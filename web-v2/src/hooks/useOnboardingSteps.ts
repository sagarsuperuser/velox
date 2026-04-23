import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { useAuth } from '@/contexts/AuthContext'

export interface OnboardingStep {
  key: 'stripe' | 'plan' | 'customer' | 'subscription' | 'webhook'
  label: string
  desc: string
  to: string
  cta: string
  done: boolean
}

// Per-tenant so a user admin-ing multiple tenants doesn't mute the launcher
// globally. Clearing localStorage un-dismisses.
const dismissStorageKey = (tenantID: string) => `velox:getstarted-dismissed:${tenantID}`

function readDismissed(tenantID: string): boolean {
  try {
    return localStorage.getItem(dismissStorageKey(tenantID)) === '1'
  } catch {
    // private-mode Safari throws on localStorage access — treat as not-dismissed
    return false
  }
}

export function useOnboardingSteps() {
  const { user } = useAuth()
  // Bump counter forces a re-read of localStorage after setDismissed. Reading
  // during render (instead of syncing via useEffect) avoids React's
  // set-state-in-effect lint warning and keeps the value derived from a
  // single source of truth.
  const [bump, setBump] = useState(0)
  void bump
  const dismissed = user ? readDismissed(user.tenant_id) : false

  const setDismissed = () => {
    if (!user) return
    try {
      localStorage.setItem(dismissStorageKey(user.tenant_id), '1')
    } catch {
      // see above
    }
    setBump(b => b + 1)
  }

  // Stripe creds are queried even when the launcher is dismissed because the
  // live-mode hard blocker (rendered in Layout) depends on the same signal.
  const { data: stripeCreds } = useQuery({
    queryKey: ['onboarding-stripe-creds'],
    queryFn: () => api.listStripeCredentials(),
    enabled: !!user,
  })

  const enabled = !!user && !dismissed
  const { data: overview } = useQuery({
    queryKey: ['onboarding-overview'],
    queryFn: () => api.getAnalyticsOverview(),
    enabled,
  })
  const { data: plansList } = useQuery({
    queryKey: ['onboarding-plans'],
    queryFn: () => api.listPlans(),
    enabled,
  })
  const { data: webhookEndpoints } = useQuery({
    queryKey: ['onboarding-webhook-endpoints'],
    queryFn: () => api.listWebhookEndpoints(),
    enabled,
  })

  const hasAnyStripe = (stripeCreds?.data?.length ?? 0) > 0
  // Tri-state: undefined = still loading. Callers (live-mode blocker strip)
  // must wait for a definitive false before raising an alert.
  const hasLiveStripe: boolean | undefined = stripeCreds
    ? stripeCreds.data.some(c => c.livemode)
    : undefined
  const hasPlans = (plansList?.data?.length ?? 0) > 0
  const hasWebhook = (webhookEndpoints?.data?.length ?? 0) > 0
  const hasCustomer = (overview?.active_customers ?? 0) > 0
  const hasSubscription = (overview?.active_subscriptions ?? 0) > 0

  const steps: OnboardingStep[] = [
    {
      key: 'stripe',
      label: 'Connect Stripe',
      desc: 'Secret + publishable keys so you can charge cards',
      to: '/settings?tab=payments',
      cta: 'Connect Stripe',
      done: hasAnyStripe,
    },
    {
      key: 'plan',
      label: 'Create your first plan',
      desc: 'Meters + rate cards that drive invoicing',
      to: '/pricing',
      cta: 'Set up pricing',
      done: hasPlans,
    },
    {
      key: 'customer',
      label: 'Add your first customer',
      desc: 'Who are you billing?',
      to: '/customers',
      cta: 'Add a customer',
      done: hasCustomer,
    },
    {
      key: 'subscription',
      label: 'Create a subscription',
      desc: 'Subscribe the customer to a plan',
      to: '/subscriptions',
      cta: 'New subscription',
      done: hasSubscription,
    },
    {
      key: 'webhook',
      label: 'Set up a webhook endpoint',
      desc: 'Receive invoice + subscription events in your app',
      to: '/webhooks',
      cta: 'Add endpoint',
      done: hasWebhook,
    },
  ]

  const complete = steps.filter(s => s.done).length
  const total = steps.length
  // Don't flash the launcher before queries settle — avoids "0 of 5" pills
  // flickering for users who've actually completed everything.
  const loaded = !!overview && !!plansList && !!stripeCreds && !!webhookEndpoints
  const show = enabled && loaded && complete < total

  return {
    steps,
    complete,
    total,
    dismissed,
    setDismissed,
    show,
    hasLiveStripe,
  }
}
