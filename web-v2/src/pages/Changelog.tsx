import { PublicLayout, PublicPageHeader } from '@/components/PublicLayout'

type Tag = 'feature' | 'improvement' | 'fix' | 'security'

const tagClass: Record<Tag, string> = {
  feature: 'bg-primary/10 text-primary',
  improvement: 'bg-blue-500/10 text-blue-600 dark:text-blue-400',
  fix: 'bg-amber-500/10 text-amber-600 dark:text-amber-500',
  security: 'bg-emerald-500/10 text-emerald-600 dark:text-emerald-500',
}

const entries: {
  date: string
  title: string
  tag: Tag
  body: string
  bullets?: string[]
}[] = [
  {
    date: '2026-04-26',
    title: 'Recipes API wire shape — snake_case + creates summary + preview wrapper',
    tag: 'fix',
    body: 'Three drifts between the recipes API design doc and the Week 3 implementation are fixed so the picker UI lights up cleanly: PascalCase JSON keys are now snake_case (matching the rest of /v1/*), each list/detail entry carries a creates: {meters, rating_rules, pricing_rules, plans, dunning_policies, webhook_endpoints} count summary, and POST /v1/recipes/{key}/preview now wraps its response as {key, version, objects: {…}, warnings: []} per the spec. Data shape only — no behavior change to instantiate / uninstall.',
    bullets: [
      'JSON tags added to domain.Recipe — keys are key, version, name, summary, description, overridable, meters, rating_rules, pricing_rules, plans, dunning_policy, webhook (was PascalCase). Optional sections use omitempty so wire output stays tight.',
      'GET /v1/recipes and GET /v1/recipes/{key} now include a creates summary so the picker UI renders "1 meter · 9 pricing rules · monthly billing" chips without a follow-up preview call.',
      'POST /v1/recipes/{key}/preview returns {key, version, objects: {meters, rating_rules, pricing_rules, plans, dunning_policies, webhook_endpoints}, warnings: []} per the spec — previously inlined every array at the top level. dunning_policies and webhook_endpoints are 0-or-1-length slices for uniform iteration.',
      'New TestWireShape_SnakeCase regression test pins all three contracts so future drift trips CI before reaching the dashboard.',
    ],
  },
  {
    date: '2026-04-26',
    title: 'Pricing recipes — one-call billing setup',
    tag: 'feature',
    body: 'Five built-in recipes (anthropic_style, openai_style, replicate_style, b2b_saas_pro, marketplace_gmv) collapse a multi-day Stripe-Billing-style onboarding into a single API call. POST /v1/recipes/{key}/instantiate atomically builds the full graph — meters, multi-dim pricing rules, plan, dunning policy, webhook endpoint — under one transaction; partial state never reaches the tenant. Designed to make the multi-dimensional meter engine immediately usable: a 12-rule anthropic_style setup goes from ~30 manual API calls to one POST.',
    bullets: [
      'Five v1 recipes: anthropic_style (Claude 3.5 Sonnet / Opus / Sonnet / Haiku, input/output, cached-input via priority=200 rule), openai_style (GPT-4 / 4o / 4o-mini / 3.5-turbo + embeddings — 14 rules), replicate_style (per-second GPU billing for a100/a40/t4/cpu), b2b_saas_pro (seats with included tier + storage GB), marketplace_gmv (package-billing GMV take rate + per-transaction fee).',
      'POST /v1/recipes/{key}/preview renders the full would-be graph with zero DB writes — cheap enough to call on every override-form keystroke. Powers the dashboard\'s review-and-instantiate dialog.',
      'Atomic graph build under one tenant-scoped transaction: rating rules → meters → multi-dim pricing rules → plan → optional dunning policy → optional webhook endpoint → instance row. Mid-graph failure rolls back every cross-domain write — no orphan meters or rules to clean up by hand.',
      'Idempotent on (tenant_id, recipe_key): a second instantiate returns the existing instance ID via 409 ErrAlreadyExists. Force re-instantiation reserved for v2 — the API accepts the field today and returns InvalidState, keeping the contract stable when force lands.',
      'Per-instantiation overrides (currency, plan_name, plan_code, plus recipe-specific knobs like included_seats) flow through templated YAML with strict missing-key handling — typos fail at preview rather than silently drop.',
      'Uninstall removes the recipe-instance row only; the resources the recipe created (plans, meters, dunning policy, webhook endpoint) stay — operators own them once they exist, and silent cascade could lose live billing data.',
    ],
  },
  {
    date: '2026-04-25',
    title: 'Trial extension (Stripe parity)',
    tag: 'feature',
    body: 'Operators can now push a trialing subscription\'s trial_end_at later — useful when sales/ops grant a customer extra trial time before the auto-flip-and-bill fires. Pairs with End trial now (the early-end direction): together they cover both sides of the trial-window adjustment that Stripe exposes through subscription.update.',
    bullets: [
      'POST /v1/subscriptions/{id}/extend-trial with {trial_end:<timestamp>}; new value must be in the future and strictly after the current trial_end_at.',
      'Atomic UPDATE WHERE status=\'trialing\' closes the race between the operator extension and the cycle-scan auto-flip — only one wins.',
      'Extension-only by design: shrinking would bypass the operator-intent that End trial now captures, so the service rejects values at-or-before the current trial_end_at.',
      'New webhook: subscription.trial_extended with triggered_by="operator".',
      'Dashboard surfaces an "Extend trial" button on trialing subs; the dialog seeds with current + 7 days.',
    ],
  },
  {
    date: '2026-04-25',
    title: 'Trial state machine (Stripe parity)',
    tag: 'feature',
    body: 'Subscriptions started with a trial now enter a real status="trialing" state, distinct from active. The billing engine runs a proper trial state machine: while the trial is active it skips billing and advances the cycle; when the trial elapses it atomically flips to active, stamps activated_at, and bills the period. Operators can also end the trial early from the dashboard.',
    bullets: [
      'New status value "trialing" on subscriptions; Service.Create routes any sub with trial_days > 0 into trialing instead of active.',
      'Cycle scan now sweeps both active and trialing — auto-flip to active fires subscription.trial_ended with triggered_by="schedule" the first cycle visit on or after trial_end_at.',
      'POST /v1/subscriptions/{id}/end-trial flips trialing → active immediately and fires subscription.trial_ended with triggered_by="operator" so analytics can split scheduled vs sales-driven trial-ends.',
      'Atomic UPDATE WHERE status=\'trialing\' closes the race between scheduler auto-flip and operator early-end — only one wins, and activated_at is COALESCE\'d so the first-set value is preserved on retries.',
      'Dashboard SubscriptionDetail surfaces an "End trial now" button on trialing subs.',
    ],
  },
  {
    date: '2026-04-25',
    title: 'Pause collection (Stripe parity)',
    tag: 'feature',
    body: 'Subscriptions can now have collection paused as a state distinct from a hard pause: the cycle keeps running, but invoices generate as drafts and skip finalize/charge/dunning until resumed. Use this for collections holds, payment-method updates, or temporary courtesy without losing usage continuity. Stripe-equivalent pause_collection field; v1 supports the keep_as_draft mode.',
    bullets: [
      'PUT /v1/subscriptions/{id}/pause-collection with {behavior:"keep_as_draft", resumes_at?:<timestamp>}; DELETE /v1/subscriptions/{id}/pause-collection clears it.',
      'Auto-resume: when resumes_at passes, the cycle scan clears the pause and fires subscription.collection_resumed with triggered_by="schedule" so analytics can distinguish from operator-triggered resume.',
      'Dashboard pause button now opens a "Pause subscription" (hard freeze) vs "Pause collection only" choice; a blue banner with one-click Resume surfaces any active collection pause.',
      'New webhooks: subscription.collection_paused and subscription.collection_resumed.',
      'Distinct from the hard pause (status=paused): hard pause halts metering and billing entirely; collection pause keeps the cycle but suppresses charges. Pick by intent — usage hold vs collections hold.',
    ],
  },
  {
    date: '2026-04-25',
    title: 'Scheduled subscription cancellation (Stripe parity)',
    tag: 'feature',
    body: 'Subscriptions can now be canceled at the end of the current billing period instead of immediately, matching Stripe\'s cancel_at_period_end and cancel_at fields. The current period bills as normal; the engine flips the sub to canceled at the boundary and emits subscription.canceled with triggered_by="schedule". The action is reversible until it fires.',
    bullets: [
      'POST /v1/subscriptions/{id}/schedule-cancel with {at_period_end:true} or {cancel_at:<timestamp>}; DELETE /v1/subscriptions/{id}/scheduled-cancel undoes it.',
      'Dashboard cancel button now opens a "at period end" vs "immediately" choice; a banner with one-click Undo surfaces any active schedule.',
      'New webhooks: subscription.cancel_scheduled and subscription.cancel_cleared. The terminal subscription.canceled event fires with triggered_by="schedule" so analytics can distinguish scheduled from immediate cancels.',
      'Test-clock parity: canceled_at honors the subscription\'s test clock so time-travel tests land deterministic timestamps.',
    ],
  },
  {
    date: '2026-04-24',
    title: 'Design-partner readiness: hosted invoice page, branded emails, webhook rotation grace',
    tag: 'feature',
    body: 'Five pre-invite blockers shipped as one batch, each anchored to an explicit industry reference (Stripe hosted_invoice_url, Stripe-Signature multi-v1 rotation, multipart/alternative branded email). Velox is now a credible substrate for a design partner to run real billing through.',
    bullets: [
      'Hosted invoice page at a public tokenized URL — Stripe-equivalent hosted_invoice_url. Mobile-first, tenant-branded, Stripe Checkout for Pay, PDF download, state-gated Pay for paid/voided invoices. Operator rotate endpoint + "Copy Link" / "Rotate" dashboard actions.',
      'Customer-facing emails render as multipart/alternative with tenant logo, brand color, support link, and a primary CTA pointing at the hosted invoice page. Six emails covered: invoice-ready, receipt, dunning warning, dunning escalation, payment failed, payment update request.',
      'Webhook signing-secret rotation now runs with a 72-hour dual-signing window — outbound events carry both the new and previous signatures in Velox-Signature (Stripe multi-v1 format) so receivers can stage a verifier deploy without a production outage.',
      'Subscription detail page gains an Activity timeline sourced from the audit log — lifecycle events (create, activate, pause, resume, cancel, plan/quantity changes) in one chronological feed. Matches the invoice payment-activity panel.',
      'SMTP permanent-failure (5xx) responses flag the customer\'s email_status as bounced, surface a red Bounced badge on the customer page, and fire a customer.email_bounced webhook event. Async NDR / SES / SendGrid webhooks plug into the same seam later.',
    ],
  },
  {
    date: '2026-04-23',
    title: 'Coupons v2: customer-scoped discounts and apply-to-draft',
    tag: 'feature',
    body: 'Coupons can now be attached to a customer and auto-apply to new invoices, matching the Stripe customer.discount model. Operators can also apply a coupon to an already-issued draft invoice; Velox recomputes tax atomically against the new subtotal and emits invoice.coupon.applied.',
    bullets: [
      'New customer_discounts table with one-active-at-a-time invariant per customer.',
      'POST /customers/{id}/coupons and DELETE /customers/{id}/coupons/{code}.',
      'POST /invoices/{id}/apply-coupon with header-or-body Idempotency-Key.',
      'Precedence rule when both subscription and customer have a coupon: subscription wins (Stripe parity).',
    ],
  },
  {
    date: '2026-04-18',
    title: 'Phase 2 hardening complete',
    tag: 'improvement',
    body: 'All items across Waves 0–5 of the phase2 hardening plan shipped: security (RLS strengthening, secret encryption), correctness (tax at finalize, real end-of-period plan change, idempotency 4xx/5xx caching), reliability (transactional outbox, scheduler advisory lock, dunning breaker), and UI (skeletons, empty states, URL state, form error injection, expiry badges, shared primitives).',
    bullets: [
      'Transactional outbox for webhook and email dispatch — no lost events on crash.',
      'Scheduler advisory locks prevent double-runs across horizontally-scaled deployments.',
      'Audit log append-only trigger enforces tamper-evidence at the database level.',
      'Online-safe migrations with round-trip (up/down) test coverage.',
    ],
  },
  {
    date: '2026-04-10',
    title: 'Webhook secret encryption at rest',
    tag: 'security',
    body: 'Webhook signing secrets are now encrypted with AES-256-GCM before persistence. Only the last-4 suffix is shown in the dashboard once the secret is revealed at creation. Rotation issues a new plaintext secret once and immediately invalidates the old one.',
  },
  {
    date: '2026-04-05',
    title: 'Credit notes with PDF',
    tag: 'feature',
    body: 'Operators can issue credit notes against finalized invoices with reason codes (duplicate, fraudulent, product_unsatisfactory, order_change, other). Partial and full refunds are supported; applied credits flow back to amount_due and optionally trigger a Stripe refund.',
  },
  {
    date: '2026-03-28',
    title: 'Dunning with retry policy and breaker',
    tag: 'feature',
    body: 'Configurable dunning policies per tenant: retry cadence (hours/days), escalation (email, pause, cancel), maximum attempts. A circuit breaker halts retries for tenants with persistently failing payment providers to avoid log-spam and quota exhaustion.',
  },
  {
    date: '2026-03-20',
    title: 'Tax at finalize (manual and Stripe Tax)',
    tag: 'feature',
    body: 'Invoice finalization now runs tax calculation against (subtotal − discount) rather than gross subtotal. Supports three tax modes per tenant: none, manual (flat rate with inclusive/exclusive toggle), and Stripe Tax (upstream calculation committed at finalize). Tax breakdown per jurisdiction is persisted and rendered on invoice PDFs.',
  },
]

export default function ChangelogPage() {
  return (
    <PublicLayout>
      <PublicPageHeader
        eyebrow="Platform"
        title="Changelog"
        description="Everything user-visible we ship, in reverse chronological order. The full engineering log lives in CHANGELOG.md on GitHub; this page curates the rollups worth reading."
      />
      <div className="max-w-3xl mx-auto px-6 py-12 space-y-10">
        {entries.map((entry) => (
          <article key={entry.date + entry.title} className="border-l-2 border-border pl-6 relative">
            <span className="absolute -left-[5px] top-2 w-2 h-2 rounded-full bg-primary" />
            <div className="flex items-center gap-3 mb-2">
              <time className="text-xs text-muted-foreground font-mono">{entry.date}</time>
              <span
                className={`text-[10px] uppercase tracking-wide px-2 py-0.5 rounded font-medium ${tagClass[entry.tag]}`}
              >
                {entry.tag}
              </span>
            </div>
            <h2 className="text-lg font-semibold tracking-tight text-foreground mb-2">{entry.title}</h2>
            <p className="text-muted-foreground leading-relaxed text-[15px]">{entry.body}</p>
            {entry.bullets && (
              <ul className="mt-3 space-y-1 text-sm text-muted-foreground">
                {entry.bullets.map((b, i) => (
                  <li key={i} className="flex gap-2">
                    <span className="text-primary/60">•</span>
                    <span className="leading-relaxed">{b}</span>
                  </li>
                ))}
              </ul>
            )}
          </article>
        ))}
      </div>
    </PublicLayout>
  )
}
