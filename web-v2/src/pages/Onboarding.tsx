import { useMemo } from 'react'
import { useSearchParams, Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'
import { Check, ArrowRight, ExternalLink, CheckCircle2 } from 'lucide-react'

interface Step {
  key: string
  title: string
  description: string
  body: React.ReactNode
}

function TemplateStepBody() {
  const { data, isLoading } = useQuery({
    queryKey: ['recipes'],
    queryFn: async () => {
      try {
        return await api.listRecipes()
      } catch {
        return { data: [] }
      }
    },
  })
  const recipes = data?.data ?? []

  if (isLoading) {
    return (
      <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
        {[0, 1, 2, 3].map(i => (
          <div key={i} className="h-24 rounded-lg border border-input bg-muted/30 animate-pulse" />
        ))}
      </div>
    )
  }

  if (recipes.length === 0) {
    return (
      <div className="rounded-lg border border-input p-5 text-center">
        <p className="text-sm text-muted-foreground">
          Pricing recipes ship with the Week 3 release. You can configure pricing manually in the meantime — head to{' '}
          <Link to="/pricing" className="underline underline-offset-2 hover:text-foreground">Pricing</Link> to create meters and rating rules.
        </p>
      </div>
    )
  }

  return (
    <div>
      <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
        {recipes.map(r => {
          const c = r.creates
          const installed = !!r.instantiated
          return (
            <Link
              key={r.key}
              to={`/recipes`}
              className={cn(
                'block text-left rounded-lg border p-4 hover:border-primary/40 hover:bg-muted/30 transition-colors',
                installed ? 'border-primary/40' : 'border-input',
              )}
            >
              <div className="flex items-center justify-between gap-2">
                <span className="font-mono text-sm text-foreground truncate">{r.key}</span>
                {installed ? (
                  <Badge variant="success" className="shrink-0">
                    <CheckCircle2 size={11} className="mr-1" />
                    Installed
                  </Badge>
                ) : (
                  <span className="text-xs text-muted-foreground">v{r.version}</span>
                )}
              </div>
              <p className="text-sm text-foreground mt-1 leading-snug">{r.name}</p>
              <p className="text-xs text-muted-foreground mt-1.5 leading-relaxed line-clamp-2">{r.summary}</p>
              <div className="flex flex-wrap gap-1 mt-2.5 text-xs text-muted-foreground">
                <span>{c.meters} meter{c.meters !== 1 ? 's' : ''}</span>
                <span>·</span>
                <span>{c.pricing_rules} pricing rule{c.pricing_rules !== 1 ? 's' : ''}</span>
                <span>·</span>
                <span>{c.plans} plan{c.plans !== 1 ? 's' : ''}</span>
              </div>
            </Link>
          )
        })}
      </div>
      <Link to="/recipes" className="text-xs text-muted-foreground hover:text-foreground underline underline-offset-2 mt-3 inline-flex items-center">
        Open full recipe picker
        <ExternalLink size={11} className="ml-1" />
      </Link>
    </div>
  )
}

const STEPS: Step[] = [
  {
    key: 'template',
    title: 'Pick a pricing template',
    description: 'Start from one of the recipes that match common usage-billing shapes — or skip and build from scratch in Pricing.',
    body: <TemplateStepBody />,
  },
  {
    key: 'stripe',
    title: 'Connect Stripe (test mode)',
    description: 'Velox uses Stripe for card payments via PaymentIntents. We never store card data — Stripe does that under the hood. Test mode keys are fine to start.',
    body: (
      <div className="space-y-3">
        <p className="text-sm text-muted-foreground">
          Connect Stripe in <Link to="/settings" className="underline underline-offset-2 hover:text-foreground">Settings → Payments</Link>. You can switch from test to live keys later from the same screen.
        </p>
        <p className="text-xs text-muted-foreground">
          Velox supports the PaymentIntents API only. We deliberately don't use Stripe Billing or Stripe Invoices — the 0.5% fee disappears, and you own the invoice lifecycle.
        </p>
      </div>
    ),
  },
  {
    key: 'tax',
    title: 'Tax mode',
    description: 'Pick how your tenant computes tax. Velox is tax-neutral — your Stripe account holds the registration (OIDAR / domestic GST / VAT / etc.).',
    body: (
      <p className="text-sm text-muted-foreground">
        Tax settings live in <Link to="/settings" className="underline underline-offset-2 hover:text-foreground">Settings → Tax</Link>. Default is "no automatic tax" — you can switch to Stripe Tax once your account is registered in the relevant jurisdictions.
      </p>
    ),
  },
  {
    key: 'branding',
    title: 'Branding',
    description: 'Your logo and primary color appear on hosted invoices and dunning emails. Customers see your brand, not Velox.',
    body: (
      <p className="text-sm text-muted-foreground">
        Configure logo, accent color, and from-address in <Link to="/settings" className="underline underline-offset-2 hover:text-foreground">Settings → Branding</Link>.
      </p>
    ),
  },
  {
    key: 'first-invoice',
    title: 'Send your first test invoice',
    description: 'Create a customer, attach a subscription, ingest a usage event, and finalize. Should take under a minute end-to-end.',
    body: (
      <div className="space-y-3">
        <ol className="text-sm text-muted-foreground space-y-2 list-decimal list-inside">
          <li>Create a customer at <Link to="/customers" className="underline underline-offset-2 hover:text-foreground">/customers</Link>.</li>
          <li>Attach a subscription to the plan you instantiated above.</li>
          <li>Ingest a usage event via the API or upload a CSV.</li>
          <li>Trigger a billing run from <Link to="/invoices" className="underline underline-offset-2 hover:text-foreground">/invoices</Link>.</li>
          <li>Open the resulting draft, finalize, and send.</li>
        </ol>
      </div>
    ),
  },
]

export default function OnboardingPage() {
  const [params, setParams] = useSearchParams()
  const stepParam = params.get('step') || STEPS[0].key
  const stepIndex = useMemo(() => {
    const i = STEPS.findIndex(s => s.key === stepParam)
    return i === -1 ? 0 : i
  }, [stepParam])
  const step = STEPS[stepIndex]

  const goTo = (i: number) => {
    const target = STEPS[Math.max(0, Math.min(STEPS.length - 1, i))]
    const next = new URLSearchParams(params)
    next.set('step', target.key)
    setParams(next, { replace: false })
  }

  return (
    <div className="min-h-screen bg-background flex flex-col items-center px-4 py-12">
      <div className="w-full max-w-2xl">
        {/* Top bar */}
        <div className="flex items-center justify-between mb-8">
          <div>
            <p className="text-xs uppercase tracking-wide text-muted-foreground">Get started with Velox</p>
            <p className="text-sm text-muted-foreground mt-0.5">Step {stepIndex + 1} of {STEPS.length}</p>
          </div>
          <Link to="/" className="text-sm text-muted-foreground hover:text-foreground transition-colors">
            Skip for now
          </Link>
        </div>

        {/* Progress indicator */}
        <div className="flex items-center gap-2 mb-8">
          {STEPS.map((s, i) => {
            const done = i < stepIndex
            const active = i === stepIndex
            return (
              <button
                key={s.key}
                onClick={() => goTo(i)}
                className={cn(
                  'flex-1 h-1.5 rounded-full transition-colors',
                  done && 'bg-primary',
                  active && 'bg-primary',
                  !done && !active && 'bg-muted',
                )}
                aria-label={`Go to step ${i + 1}: ${s.title}`}
              />
            )
          })}
        </div>

        {/* Card */}
        <Card>
          <CardContent className="p-8">
            <h1 className="text-2xl font-semibold text-foreground">{step.title}</h1>
            <p className="text-sm text-muted-foreground mt-2 leading-relaxed">{step.description}</p>
            <div className="mt-6">{step.body}</div>
          </CardContent>
        </Card>

        {/* Nav */}
        <div className="flex items-center justify-between mt-6">
          <Button
            variant="ghost"
            onClick={() => goTo(stepIndex - 1)}
            disabled={stepIndex === 0}
          >
            Back
          </Button>
          {stepIndex < STEPS.length - 1 ? (
            <Button onClick={() => goTo(stepIndex + 1)}>
              Continue
              <ArrowRight size={14} className="ml-1.5" />
            </Button>
          ) : (
            <Link to="/">
              <Button>
                <Check size={14} className="mr-1.5" />
                Done — open dashboard
              </Button>
            </Link>
          )}
        </div>

        {/* Step list */}
        <div className="mt-10">
          <p className="text-xs uppercase tracking-wide text-muted-foreground mb-3">All steps</p>
          <ol className="space-y-1.5">
            {STEPS.map((s, i) => {
              const done = i < stepIndex
              const active = i === stepIndex
              return (
                <li key={s.key}>
                  <button
                    onClick={() => goTo(i)}
                    className={cn(
                      'w-full text-left flex items-center gap-3 px-3 py-2 rounded-md text-sm transition-colors',
                      active && 'bg-muted text-foreground',
                      !active && 'text-muted-foreground hover:bg-muted/40 hover:text-foreground',
                    )}
                  >
                    <span className={cn(
                      'flex h-5 w-5 items-center justify-center rounded-full text-xs',
                      done && 'bg-primary text-primary-foreground',
                      active && 'border border-primary text-primary',
                      !done && !active && 'border border-muted-foreground/30 text-muted-foreground',
                    )}>
                      {done ? <Check size={11} /> : i + 1}
                    </span>
                    {s.title}
                  </button>
                </li>
              )
            })}
          </ol>
        </div>
      </div>
    </div>
  )
}
