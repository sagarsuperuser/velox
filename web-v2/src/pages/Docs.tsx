import { Link } from 'react-router-dom'
import { ArrowRight, Rocket, Webhook, Repeat, Code2, Box } from 'lucide-react'
import { PublicLayout } from '@/components/PublicLayout'
import { DocsShell, DocsH1, DocsLead, Prose } from '@/components/DocsShell'

const cards = [
  {
    to: '/docs/quickstart',
    icon: Rocket,
    title: 'Quickstart',
    body: 'Subscribe your first customer in five API calls. Covers keys, plans, subscriptions, and invoice retrieval.',
  },
  {
    to: '/docs/recipes',
    icon: Box,
    title: 'Pricing recipes',
    body: 'Five built-in pricing templates — Anthropic-style tokens, OpenAI, Replicate GPU-time, B2B SaaS, marketplace GMV. Instantiate the full graph in one call.',
  },
  {
    to: '/docs/webhooks',
    icon: Webhook,
    title: 'Webhooks',
    body: 'Receive signed events for every billing state change. Includes HMAC verification, retry schedule, and event catalogue.',
  },
  {
    to: '/docs/idempotency',
    icon: Repeat,
    title: 'Idempotency & retries',
    body: 'Safely retry mutating requests with the Idempotency-Key header. Stripe-compatible semantics.',
  },
  {
    to: '/docs/api',
    icon: Code2,
    title: 'API reference',
    body: 'Full OpenAPI reference with every endpoint, schema, and example. Built from docs/openapi.yaml.',
  },
]

export default function DocsPage() {
  return (
    <PublicLayout>
      <DocsShell>
        <Prose>
          <DocsH1>Velox documentation</DocsH1>
          <DocsLead>
            Velox is an open-source usage-based billing engine. Everything you do in the dashboard is also
            available over a REST API. This is the reference for integrators building against Velox.
          </DocsLead>
          <div className="grid grid-cols-1 md:grid-cols-2 gap-3 my-8">
            {cards.map((card) => (
              <Link
                key={card.to}
                to={card.to}
                className="group border border-border rounded-xl p-5 hover:border-primary/40 hover:bg-muted/40 transition-colors"
              >
                <div className="flex items-start gap-3">
                  <div className="w-9 h-9 rounded-lg bg-primary/10 text-primary flex items-center justify-center shrink-0">
                    <card.icon size={18} />
                  </div>
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center justify-between">
                      <h3 className="font-medium text-foreground">{card.title}</h3>
                      <ArrowRight
                        size={14}
                        className="text-muted-foreground group-hover:text-primary group-hover:translate-x-0.5 transition-all"
                      />
                    </div>
                    <p className="text-sm text-muted-foreground mt-1 leading-relaxed">
                      {card.body}
                    </p>
                  </div>
                </div>
              </Link>
            ))}
          </div>

          <div className="border border-border rounded-xl p-5 bg-muted/30 text-sm">
            <div className="flex items-center gap-2 mb-2">
              <span className="inline-block w-1.5 h-1.5 rounded-full bg-emerald-500" />
              <span className="font-medium text-foreground">Base URL</span>
            </div>
            <code className="font-mono text-[13px]">https://api.velox.dev/v1</code>
            <p className="text-muted-foreground mt-3">
              All endpoints are authenticated with an API key from the dashboard's{' '}
              <Link to="/api-keys" className="underline underline-offset-2 hover:text-foreground">
                API Keys
              </Link>{' '}
              page. Send it as{' '}
              <code className="font-mono text-[12px] px-1 py-0.5 rounded bg-background border border-border">
                Authorization: Bearer sk_live_…
              </code>
              .
            </p>
          </div>
        </Prose>
      </DocsShell>
    </PublicLayout>
  )
}
