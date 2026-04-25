import { useMemo } from 'react'
import { useSearchParams, Link } from 'react-router-dom'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { cn } from '@/lib/utils'
import { Check, ArrowRight } from 'lucide-react'

interface Step {
  key: string
  title: string
  description: string
  body: React.ReactNode
}

const STEPS: Step[] = [
  {
    key: 'template',
    title: 'Pick a pricing template',
    description: 'Start from one of the recipes that match common usage-billing shapes — or skip and build from scratch.',
    body: (
      <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
        {[
          { name: 'anthropic_style', tag: 'AI inference', detail: 'Multi-dim tokens (model × operation × cached) + base subscription' },
          { name: 'openai_style', tag: 'AI inference', detail: 'Per-token rates per model with prompt-cache discount' },
          { name: 'replicate_style', tag: 'AI inference', detail: 'Per-second compute billed by GPU class' },
          { name: 'b2b_saas_pro', tag: 'SaaS', detail: 'Per-seat base + usage overage with credit grant' },
          { name: 'marketplace_gmv', tag: 'Marketplace', detail: 'Take rate on GMV with platform-fee floor' },
          { name: 'blank', tag: 'Custom', detail: 'Empty product catalog. Build it yourself in the dashboard.' },
        ].map(r => (
          <button
            key={r.name}
            className="text-left rounded-lg border border-input p-4 hover:border-primary/40 hover:bg-muted/30 transition-colors"
            disabled
          >
            <div className="flex items-center justify-between">
              <span className="font-mono text-sm text-foreground">{r.name}</span>
              <span className="text-xs text-muted-foreground">{r.tag}</span>
            </div>
            <p className="text-xs text-muted-foreground mt-1.5 leading-relaxed">{r.detail}</p>
          </button>
        ))}
      </div>
    ),
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
        <p className="text-xs text-muted-foreground">
          Curl-only path: see the <Link to="/docs/quickstart" className="underline underline-offset-2 hover:text-foreground">CLI quickstart</Link>.
        </p>
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
