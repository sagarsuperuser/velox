import { PublicLayout } from '@/components/PublicLayout'
import {
  DocsShell,
  DocsH1,
  DocsH2,
  DocsLead,
  Prose,
  Code,
  InlineCode,
  Callout,
} from '@/components/DocsShell'

export default function DocsQuickstartPage() {
  return (
    <PublicLayout>
      <DocsShell>
        <Prose>
          <DocsH1>Quickstart</DocsH1>
          <DocsLead>
            This guide takes you from a fresh account to an invoiced subscription in five API calls.
            Every request can be run with <InlineCode>curl</InlineCode> — no SDK required.
          </DocsLead>

          <DocsH2>Prerequisites</DocsH2>
          <p>
            Sign in to the dashboard and create a <strong>secret key</strong> in{' '}
            <InlineCode>Settings → API Keys</InlineCode>. Secret keys start with{' '}
            <InlineCode>sk_test_…</InlineCode> in test mode and <InlineCode>sk_live_…</InlineCode> in
            live mode. Keep them out of client-side code.
          </p>
          <Code language="shell">{`export VELOX_KEY="sk_test_abc123..."
export VELOX_API="https://api.velox.dev/v1"`}</Code>

          <DocsH2>1. Create a customer</DocsH2>
          <p>
            Customers are the entities you bill. The only required fields are{' '}
            <InlineCode>external_id</InlineCode> (your stable identifier, typically the customer ID in
            your own system) and <InlineCode>display_name</InlineCode>.
          </p>
          <Code language="shell">{`curl -X POST $VELOX_API/customers \\
  -H "Authorization: Bearer $VELOX_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{
    "external_id": "user_42",
    "display_name": "Acme, Inc.",
    "email": "billing@acme.test"
  }'`}</Code>
          <p>
            The response includes a Velox-assigned <InlineCode>id</InlineCode> (e.g.,{' '}
            <InlineCode>cus_01HX…</InlineCode>). Save it — you'll reference customers by this ID in
            later calls.
          </p>

          <DocsH2>2. Create a plan</DocsH2>
          <p>
            A plan describes what a customer pays for. It has one or more <em>rating rules</em> (flat
            fee, per-unit, tiered, etc.) and a billing cadence.
          </p>
          <Code language="shell">{`curl -X POST $VELOX_API/plans \\
  -H "Authorization: Bearer $VELOX_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{
    "code": "pro-monthly",
    "name": "Pro (Monthly)",
    "currency": "USD",
    "billing_period": "monthly",
    "rating_rules": [
      { "type": "flat", "amount_cents": 4900, "description": "Pro subscription" }
    ]
  }'`}</Code>

          <DocsH2>3. Create a subscription</DocsH2>
          <p>
            A subscription links a customer to one or more plans. Subscriptions auto-generate invoices
            at the end of each billing period.
          </p>
          <Code language="shell">{`curl -X POST $VELOX_API/subscriptions \\
  -H "Authorization: Bearer $VELOX_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{
    "customer_id": "cus_01HX...",
    "items": [
      { "plan_code": "pro-monthly", "quantity": 1 }
    ],
    "start_at": "2026-04-23T00:00:00Z"
  }'`}</Code>
          <Callout tone="info">
            Pass the header{' '}
            <InlineCode>Idempotency-Key: &lt;your-uuid&gt;</InlineCode> on every mutating request.
            It's safe to retry — Velox returns the cached response and sets{' '}
            <InlineCode>Idempotent-Replayed: true</InlineCode>. See{' '}
            <a href="/docs/idempotency" className="underline underline-offset-2 hover:text-foreground">
              Idempotency &amp; retries
            </a>
            .
          </Callout>

          <DocsH2>4. Retrieve the invoice</DocsH2>
          <p>
            When a billing period closes, Velox generates a draft invoice, finalizes it (locking totals
            and computing tax), and attempts payment via the tenant's connected Stripe account. You can
            list invoices for a customer at any time:
          </p>
          <Code language="shell">{`curl $VELOX_API/invoices?customer_id=cus_01HX... \\
  -H "Authorization: Bearer $VELOX_KEY"`}</Code>
          <p>
            Each invoice object includes <InlineCode>status</InlineCode>,{' '}
            <InlineCode>amount_due_cents</InlineCode>, <InlineCode>discount_cents</InlineCode>,{' '}
            <InlineCode>tax_cents</InlineCode>, line items, and a <InlineCode>pdf_url</InlineCode>.
          </p>

          <DocsH2>5. Listen for webhooks</DocsH2>
          <p>
            Add a webhook endpoint in the dashboard (or via{' '}
            <InlineCode>POST /webhook_endpoints</InlineCode>) to receive signed events —{' '}
            <InlineCode>invoice.finalized</InlineCode>, <InlineCode>invoice.paid</InlineCode>,{' '}
            <InlineCode>subscription.canceled</InlineCode>, and ~20 others. Every delivery is signed
            HMAC-SHA256 with your endpoint secret. See the{' '}
            <a href="/docs/webhooks" className="underline underline-offset-2 hover:text-foreground">
              Webhooks guide
            </a>{' '}
            for verification code.
          </p>

          <DocsH2>What's next</DocsH2>
          <ul className="list-disc pl-6 space-y-1 text-muted-foreground">
            <li>Add <InlineCode>metered</InlineCode> rating rules for usage-based pricing.</li>
            <li>Apply <InlineCode>coupons</InlineCode> to customers or subscriptions.</li>
            <li>Issue <InlineCode>credit_notes</InlineCode> for refunds or corrections.</li>
            <li>Configure <InlineCode>dunning policies</InlineCode> for automated retry on payment failure.</li>
          </ul>
        </Prose>
      </DocsShell>
    </PublicLayout>
  )
}
