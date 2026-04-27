import { PublicLayout } from '@/components/PublicLayout'
import {
  DocsShell,
  DocsH1,
  DocsH2,
  DocsLead,
  Prose,
} from '@/components/DocsShell'

type Term = { term: string; def: string }

const concepts: Term[] = [
  {
    term: 'Tenant',
    def: 'A top-level billing organisation in Velox — typically the SaaS company using the API. All resources (customers, plans, invoices, keys) live under a tenant and are isolated by row-level security.',
  },
  {
    term: 'Customer',
    def: 'An end-user of a tenant\'s product. Carries an external_id (your own stable reference), a billing profile (address, tax ID), payment methods, and zero or more subscriptions.',
  },
  {
    term: 'Livemode',
    def: 'A boolean on every tenant-scoped row. true means production data and real Stripe charges; false means test mode. API keys are mode-locked — a test key cannot read live resources, and vice versa.',
  },
  {
    term: 'Plan',
    def: 'A priced billing item with a base amount and optional usage components. Has an interval (monthly or yearly) and a status (draft, active, archived). Plans bind one or more meters via pricing rules.',
  },
  {
    term: 'Subscription',
    def: 'A recurring contract between a customer and one or more plans. Statuses progress draft → trialing → active → canceled. Pause via pause_collection without breaking the timeline.',
  },
  {
    term: 'Meter',
    def: 'A dimension along which usage is measured (API calls, GB stored, GPU hours). A meter receives usage_events and is interpreted by pricing rules. Aggregation modes: sum, count, max, last_during_period, last_ever.',
  },
  {
    term: 'Pricing rule',
    def: 'Binds a meter to a rating rule via an optional dimension match. Multiple rules on the same meter resolve by priority, so the same usage stream can be priced differently per tier or region without claiming the same event twice.',
  },
  {
    term: 'Rating rule',
    def: 'How a quantity becomes money. Modes: flat ($X per unit), graduated (tiered), or package ($X per N units). One rating rule can be referenced by many pricing rules.',
  },
  {
    term: 'Dimension / dimension_match',
    def: 'Usage events can carry an arbitrary dimensions object (for example {"model": "gpt-4", "tier": 1}). A pricing rule\'s dimension_match is a JSON subset filter — empty object matches all events. This is how one meter prices multi-axis pricing without exploding into combinations.',
  },
  {
    term: 'Recipe',
    def: 'A pre-built billing graph (products + prices + meters + dunning policy + webhook endpoint) shipped as YAML and instantiated atomically on a tenant. Used for the fastest path from "binary running" to "first invoice billed".',
  },
]

const billing: Term[] = [
  {
    term: 'Invoice',
    def: 'The bill issued for a billing period. Statuses: draft, open, paid, void, uncollectible. Finalising an invoice creates a Stripe PaymentIntent for the total.',
  },
  {
    term: 'PaymentIntent (direct)',
    def: 'Velox sends invoice totals to Stripe as PaymentIntents — no Stripe Billing layer in between. This is what avoids the 0.5% Stripe Billing fee.',
  },
  {
    term: 'Billing threshold',
    def: 'Subscription-level cap that triggers early invoice finalisation. usage_gte is per-item (decimal quantity); amount_gte is on the in-cycle subtotal in cents.',
  },
  {
    term: 'Billing alert',
    def: 'A spend or usage trip-wire that emits a webhook and a dashboard notification. Recurrence one_time fires once across the alert\'s lifetime; per_period fires at most once per cycle and resets on rollover.',
  },
  {
    term: 'Dunning policy',
    def: 'How to recover when an invoice charge fails. A retry schedule (hours between attempts), a max retry count, and a final action (manual_review, pause, or write_off_later).',
  },
  {
    term: 'Pause collection',
    def: 'Temporary stop on charging without changing subscription status. Invoices still generate as drafts; nothing is sent to Stripe until the resume date.',
  },
  {
    term: 'Plan migration',
    def: 'Operator-initiated bulk move of subscriptions from one plan to another, with optional proration. Preview shows per-customer impact; commit applies atomically and writes one audit row per affected subscription.',
  },
  {
    term: 'Credit ledger',
    def: 'Per-customer append-only log of credit movement (grant, usage, expiry, adjustment). Balance is always reconstructible by replaying the ledger; ledger entries are immutable.',
  },
  {
    term: 'Credit note',
    def: 'A formal document issued after invoice dispute. Can refund to Stripe or apply as a credit grant. Statuses: draft, issued, voided.',
  },
]

const platform: Term[] = [
  {
    term: 'API key types',
    def: 'platform (admin / tenant management — rare), secret (full API access; vlx_secret_…), publishable (restricted; vlx_pub_…). Each key is bound to one tenant + one mode (live or test).',
  },
  {
    term: 'Idempotency-Key',
    def: 'Optional header on POST / PUT / PATCH that lets you retry safely. Same key + same body returns the cached response with Idempotent-Replayed: true. Same key + different body returns 422 idempotency_error. Cached for 24 hours per tenant + livemode.',
  },
  {
    term: 'RLS (row-level security)',
    def: 'Postgres policy that filters every tenant-scoped query by the current tenant_id. Velox sets app.tenant_id at the start of each transaction; the policy makes cross-tenant reads impossible at the database layer.',
  },
  {
    term: 'Webhook endpoint',
    def: 'A registered URL to which Velox POSTs JSON event payloads. Signed with HMAC-SHA256 in the Velox-Signature header (Stripe-style). Each endpoint has its own subscribed event list.',
  },
  {
    term: 'Webhook secret rotation',
    def: 'Generates a new signing secret and keeps the old one valid for a 72-hour grace period. During the window both signatures are emitted, so the receiver can stage a verifier upgrade without dropping events.',
  },
  {
    term: 'Hosted invoice page',
    def: 'Public, unauthenticated URL where a customer can view and pay an invoice. Token-protected (vlx_inv_pub_…), rate-limited per IP, and rotates on demand if a link leaks.',
  },
  {
    term: 'Cost dashboard embed',
    def: 'Public iframe URL showing a customer their in-cycle usage and projected total. Token-protected (vlx_pcd_…), customisable via theme and accent query params, rate-limited per IP.',
  },
  {
    term: 'Test clock',
    def: 'Time simulator scoped to a single test-mode subscription. Advance it to jump through billing cycles, trial expiry, dunning retries — without waiting in real time. Disabled in livemode.',
  },
  {
    term: 'Tax provider',
    def: 'Per-tenant choice of tax engine. none disables tax. manual applies a flat rate from settings. stripe_tax delegates calculation to Stripe (account-level support required).',
  },
]

function TermList({ items }: { items: Term[] }) {
  return (
    <dl className="space-y-4 my-4">
      {items.map((t) => (
        <div key={t.term}>
          <dt className="font-medium text-foreground">{t.term}</dt>
          <dd className="text-muted-foreground">{t.def}</dd>
        </div>
      ))}
    </dl>
  )
}

export default function DocsGlossaryPage() {
  return (
    <PublicLayout>
      <DocsShell>
        <Prose>
          <DocsH1>Glossary</DocsH1>
          <DocsLead>
            Velox uses a few terms that have a specific meaning in this product, even if they
            sound generic. This page is the canonical short definition for each — bookmark it
            and refer back when something in another doc uses a word you'd like pinned down.
          </DocsLead>

          <DocsH2>Core concepts</DocsH2>
          <TermList items={concepts} />

          <DocsH2>Billing &amp; lifecycle</DocsH2>
          <TermList items={billing} />

          <DocsH2>Platform &amp; security</DocsH2>
          <TermList items={platform} />
        </Prose>
      </DocsShell>
    </PublicLayout>
  )
}
