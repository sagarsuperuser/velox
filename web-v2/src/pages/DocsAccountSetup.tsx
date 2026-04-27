import { PublicLayout } from '@/components/PublicLayout'
import {
  DocsShell,
  DocsH1,
  DocsH2,
  DocsH3,
  DocsLead,
  Prose,
  Code,
  InlineCode,
  Callout,
} from '@/components/DocsShell'

export default function DocsAccountSetupPage() {
  return (
    <PublicLayout>
      <DocsShell>
        <Prose>
          <DocsH1>Account setup</DocsH1>
          <DocsLead>
            From a freshly deployed Velox to your first invoice charged through Stripe in seven
            steps. The dashboard's Get Started launcher tracks the last five of these — this page
            walks the full path so you know what each step actually does and how to verify it.
          </DocsLead>

          <DocsH2>Before you start</DocsH2>
          <p>
            Velox runs as a single Go process backed by Postgres. You should already have it
            deployed and reachable — see{' '}
            <a href="https://github.com/sagarsuperuser/velox#deploy" className="text-primary underline underline-offset-2">
              the README's deploy section
            </a>{' '}
            for Helm, Docker Compose, and Terraform AWS recipes. You'll also want a Stripe account
            (test-mode is fine for everything below) and a tunnel to localhost (e.g., ngrok) if
            you're running on your laptop and want to receive webhooks.
          </p>

          <DocsH2>1. Bootstrap the first tenant</DocsH2>
          <p>
            A fresh Velox install has zero tenants and zero API keys. The{' '}
            <InlineCode>POST /v1/bootstrap</InlineCode> endpoint creates both — atomically — the
            first time it's called. After the first tenant exists, the endpoint returns 409 and is
            effectively retired; create additional tenants with the{' '}
            <InlineCode>cmd/velox-bootstrap</InlineCode> CLI.
          </p>
          <p>
            Bootstrap is gated by an env var so a public Velox doesn't auto-create tenants for
            anyone who finds the URL. Generate a token, set it on the running process, then call
            the endpoint:
          </p>
          <Code language="shell">{`export VELOX_BOOTSTRAP_TOKEN=$(openssl rand -hex 32)
# restart the velox process so it picks up the env var

curl -X POST $VELOX_API/bootstrap \\
  -H "Content-Type: application/json" \\
  -d '{
    "token": "'"$VELOX_BOOTSTRAP_TOKEN"'",
    "tenant_name": "Acme, Inc."
  }'`}</Code>
          <p>The response carries the tenant and the seeded test-mode keys:</p>
          <Code language="json">{`{
  "tenant": { "id": "vlx_ten_…", "name": "Acme, Inc.", "status": "active" },
  "secret_key": "vlx_secret_test_…",
  "public_key": "vlx_pub_test_…",
  "message": "Bootstrap complete. Save these keys — the secret key will not be shown again."
}`}</Code>
          <Callout tone="warn">
            The secret key is shown once. Store it in your secrets manager <em>before</em> you
            close the terminal. If you lose it, you can mint a new one from the dashboard once
            you're signed in — but the old one is unrecoverable.
          </Callout>

          <DocsH2>2. Sign in and complete the company profile</DocsH2>
          <p>
            Open the dashboard at the URL you deployed it to. Sign in with the credentials you
            configured at deploy time, then go to <strong>Settings → Company</strong> and fill in:
          </p>
          <ul className="list-disc pl-6 space-y-1">
            <li>Legal name and trading name (used on invoice PDFs and the hosted invoice page).</li>
            <li>Billing address and tax identifier(s) (drives invoice headers; tax engine reads them too).</li>
            <li>Support email and support URL (rendered in the customer-facing invoice footer).</li>
            <li>Default currency for new plans (you can override per plan later).</li>
          </ul>
          <p>
            None of this is required for the API to function — but the moment you finalise an
            invoice, these fields appear on the customer's PDF. Filling them in now avoids
            embarrassing reissues later.
          </p>

          <DocsH2>3. Connect Stripe (test mode first)</DocsH2>
          <p>
            Velox sends invoice totals to Stripe as PaymentIntents — there's no Stripe Billing
            layer in the middle. To do that, it needs your Stripe API keys. Go to{' '}
            <strong>Settings → Payments</strong> and paste:
          </p>
          <ul className="list-disc pl-6 space-y-1">
            <li>
              <strong>Test secret key</strong> (<InlineCode>sk_test_…</InlineCode>) — for charging
              and creating PaymentIntents.
            </li>
            <li>
              <strong>Test publishable key</strong> (<InlineCode>pk_test_…</InlineCode>) — for
              client-side payment method collection.
            </li>
            <li>
              <strong>Test webhook signing secret</strong> (<InlineCode>whsec_…</InlineCode>) — set
              this on the Stripe side first by registering{' '}
              <InlineCode>{'{your_velox_url}/v1/stripe/webhooks'}</InlineCode> as an endpoint, then
              paste the secret here.
            </li>
          </ul>
          <p>
            Live keys go in the same place once you're ready to take real money. Test and live
            slots are independent — connecting one mode does not configure the other, and a
            test-mode invoice never touches live keys.
          </p>
          <Callout tone="info">
            Charges fail with 422 <InlineCode>billing_setup_incomplete</InlineCode> until the
            relevant mode has both a secret and a publishable key. The webhook secret is only
            required if you want Stripe → Velox sync (charge succeeded, dispute opened, etc.) —
            recommended, but not blocking for outbound charges.
          </Callout>

          <DocsH2>4. Create your first plan and meter</DocsH2>
          <p>
            A plan defines what a customer is billed for. The simplest plan has a single fixed
            recurring charge; the most complex has multiple meters with tiered, dimensional,
            and graduated rating rules. Start simple — you can layer usage on later.
          </p>
          <DocsH3>4a. Pick a recipe (recommended)</DocsH3>
          <p>
            Velox ships with five pre-built billing graphs you can instantiate with one click —
            see <a href="/docs/recipes" className="text-primary underline underline-offset-2">Pricing recipes</a>.
            They cover the common patterns: per-token AI, per-prediction inference, B2B SaaS Pro,
            marketplace GMV, and OpenAI-style multi-tier. If one fits, click it; you'll get a
            plan, the meters it depends on, and a sensible dunning policy in one transaction.
          </p>
          <DocsH3>4b. Or roll your own</DocsH3>
          <p>
            Otherwise, go to <strong>Pricing → Plans → New plan</strong>. Define the base
            recurring fee, then add usage components by binding a <em>meter</em> to a{' '}
            <em>rating rule</em> via a <em>pricing rule</em>. The Glossary explains how those three
            objects fit together.
          </p>

          <DocsH2>5. Register a webhook endpoint</DocsH2>
          <p>
            Velox emits webhooks for every state change worth reacting to: invoice finalised,
            payment succeeded, subscription paused, dunning attempt failed, and so on. Without an
            endpoint your app won't know any of this happened.
          </p>
          <p>
            Go to <strong>Webhooks → Add endpoint</strong>, paste your HTTPS URL, and pick the
            events you care about (or subscribe to all and filter on your side). Velox returns a
            signing secret — store it; you'll use it to verify the{' '}
            <InlineCode>Velox-Signature</InlineCode> header on every incoming request. See the{' '}
            <a href="/docs/webhooks" className="text-primary underline underline-offset-2">Webhooks</a>{' '}
            doc for verification code in Node, Python, Go, and Ruby.
          </p>
          <Callout tone="warn">
            Plain HTTP endpoints are rejected at registration. For local development, use{' '}
            <InlineCode>ngrok http 3000</InlineCode> (or a similar tunnel) and register the{' '}
            <InlineCode>https://</InlineCode> URL it gives you.
          </Callout>

          <DocsH2>6. Create your first customer and subscription</DocsH2>
          <p>
            With Stripe connected and a plan in place, you can subscribe a customer. From the
            dashboard, <strong>Customers → New customer</strong> takes the same fields the API
            does — most importantly <InlineCode>external_id</InlineCode>, your stable reference
            from the system of record. Then <strong>Subscriptions → New subscription</strong>{' '}
            attaches the customer to a plan.
          </p>
          <p>By API:</p>
          <Code language="shell">{`curl -X POST $VELOX_API/customers \\
  -H "Authorization: Bearer $VELOX_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{
    "external_id": "user_42",
    "display_name": "Acme, Inc.",
    "email": "billing@acme.test"
  }'

curl -X POST $VELOX_API/subscriptions \\
  -H "Authorization: Bearer $VELOX_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{
    "customer_id": "cus_…",
    "plan_id": "plan_…",
    "starts_at": "2026-04-27T00:00:00Z"
  }'`}</Code>
          <p>
            The full flow with usage events and rating is in the{' '}
            <a href="/docs/quickstart" className="text-primary underline underline-offset-2">Quickstart</a>.
          </p>

          <DocsH2>7. Trigger billing and verify end-to-end</DocsH2>
          <p>
            The billing scheduler runs every minute in production and finalises invoices when the
            cycle rolls over. To verify the loop works without waiting, force a run:
          </p>
          <Code language="shell">{`curl -X POST $VELOX_API/billing/run \\
  -H "Authorization: Bearer $VELOX_KEY"`}</Code>
          <p>
            Then check, in order:
          </p>
          <ol className="list-decimal pl-6 space-y-1">
            <li>
              <strong>An invoice exists</strong> on the customer with status{' '}
              <InlineCode>open</InlineCode> or <InlineCode>paid</InlineCode>.
            </li>
            <li>
              <strong>A Stripe PaymentIntent exists</strong> in your Stripe dashboard, in test
              mode, for the invoice total. (If you're not seeing one, the most likely cause is
              <InlineCode>billing_setup_incomplete</InlineCode> — back to step 3.)
            </li>
            <li>
              <strong>Your webhook endpoint received</strong> at minimum{' '}
              <InlineCode>invoice.finalized</InlineCode> and{' '}
              <InlineCode>invoice.payment_succeeded</InlineCode>. The Webhooks → Deliveries page
              shows what was sent and the response your endpoint returned.
            </li>
          </ol>
          <p>
            All three green means your install is end-to-end functional. Switch your Stripe
            credentials to live keys when you're ready to take real money — same flow, different
            mode.
          </p>

          <DocsH2>What's next</DocsH2>
          <ul className="list-disc pl-6 space-y-1">
            <li>
              <a href="/docs/quickstart" className="text-primary underline underline-offset-2">Quickstart</a>{' '}
              — the same path expressed entirely as API calls, for IaC and CI use.
            </li>
            <li>
              <a href="/docs/recipes" className="text-primary underline underline-offset-2">Pricing recipes</a>{' '}
              — pre-built billing graphs (AI tokens, B2B SaaS, marketplace GMV).
            </li>
            <li>
              <a href="/docs/webhooks" className="text-primary underline underline-offset-2">Webhooks</a>,{' '}
              <a href="/docs/idempotency" className="text-primary underline underline-offset-2">Idempotency &amp; retries</a>,{' '}
              and{' '}
              <a href="/docs/errors" className="text-primary underline underline-offset-2">Errors</a>{' '}
              — the three reference pages every integration needs.
            </li>
            <li>
              <a href="/docs/troubleshooting" className="text-primary underline underline-offset-2">Troubleshooting</a>{' '}
              — what to check first when any of the steps above doesn't behave.
            </li>
          </ul>
        </Prose>
      </DocsShell>
    </PublicLayout>
  )
}
