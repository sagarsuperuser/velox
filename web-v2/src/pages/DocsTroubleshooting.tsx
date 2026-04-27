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

export default function DocsTroubleshootingPage() {
  return (
    <PublicLayout>
      <DocsShell>
        <Prose>
          <DocsH1>Troubleshooting</DocsH1>
          <DocsLead>
            Common integration-time problems and the specific check that catches each one. If
            you're stuck on something not covered here, the answer is usually in the response
            body — log <InlineCode>error.code</InlineCode> and{' '}
            <InlineCode>Velox-Request-Id</InlineCode>.
          </DocsLead>

          <DocsH2>Webhook events aren't firing</DocsH2>
          <DocsH3>Symptom</DocsH3>
          <p>
            An invoice finalises (you can see it in the dashboard) but no{' '}
            <InlineCode>invoice.finalized</InlineCode> webhook arrives at your endpoint.
          </p>
          <DocsH3>Check, in order</DocsH3>
          <ol className="list-decimal pl-6 space-y-1">
            <li>
              Endpoint is <InlineCode>active=true</InlineCode> and subscribed to that event type
              (Webhooks → endpoint detail).
            </li>
            <li>
              The global flag isn't off:{' '}
              <InlineCode>GET /v1/feature-flags/webhooks.enabled</InlineCode> should return{' '}
              <InlineCode>{`{"enabled": true}`}</InlineCode>.
            </li>
            <li>
              The endpoint URL is <InlineCode>https://</InlineCode>. Plain HTTP is rejected at
              registration. For local dev, use ngrok or a tunnel.
            </li>
            <li>
              Webhooks → Deliveries shows recent attempts. A delivery in{' '}
              <InlineCode>failed</InlineCode> with response status 5xx means your endpoint is
              receiving them but erroring. A missing delivery means it never dispatched.
            </li>
          </ol>

          <DocsH2>Decimal usage quantity is rejected</DocsH2>
          <DocsH3>Symptom</DocsH3>
          <p>
            <InlineCode>POST /v1/usage-events</InlineCode> with{' '}
            <InlineCode>{`{"value": 0.5}`}</InlineCode> returns 422.
          </p>
          <DocsH3>Why</DocsH3>
          <p>
            Decimal quantities must be sent as strings to preserve precision (this matches
            Stripe's <InlineCode>quantity_decimal</InlineCode> contract). JSON numbers are read
            as floats and risk rounding for ratios like <InlineCode>0.1</InlineCode>.
          </p>
          <DocsH3>Fix</DocsH3>
          <Code language="json">{`{ "event_name": "tokens", "value": "0.5", "external_customer_id": "..." }`}</Code>

          <DocsH2>Subscription stuck in <code>trialing</code></DocsH2>
          <DocsH3>Symptom</DocsH3>
          <p>
            The trial end has passed but the subscription is still{' '}
            <InlineCode>trialing</InlineCode> and no invoice has been generated.
          </p>
          <DocsH3>Check</DocsH3>
          <ul className="list-disc pl-6 space-y-1">
            <li>
              Confirm <InlineCode>trial_end_at</InlineCode> is actually in the past — server
              time, not your local clock.
            </li>
            <li>
              The billing scheduler runs every minute. If you just crossed the trial end, give
              it 60 seconds, or trigger manually with{' '}
              <InlineCode>POST /v1/billing/run</InlineCode>.
            </li>
            <li>
              The customer must have a payment method attached. Without one, billing creates a
              draft invoice but won't transition <InlineCode>trialing → active</InlineCode>.
            </li>
          </ul>

          <DocsH2>Usage event accepted but not on the invoice</DocsH2>
          <DocsH3>Symptom</DocsH3>
          <p>
            <InlineCode>POST /v1/usage-events</InlineCode> returned 201, but the next invoice
            doesn't reflect the quantity.
          </p>
          <DocsH3>Walk this list</DocsH3>
          <ol className="list-decimal pl-6 space-y-1">
            <li>
              <strong>Wrong meter</strong>. The event's <InlineCode>event_name</InlineCode> must
              match a meter <InlineCode>key</InlineCode> on the subscription's plan, not just
              any meter on the tenant.
            </li>
            <li>
              <strong>Dimensions don't match a pricing rule</strong>. If your event has{' '}
              <InlineCode>{`{"tier": 2}`}</InlineCode> but every pricing rule on that meter has{' '}
              <InlineCode>dimension_match: {`{"tier": 1}`}</InlineCode>, the event is ingested
              but rated to nothing. Add a fallback rule with empty{' '}
              <InlineCode>{`dimension_match: {}`}</InlineCode>.
            </li>
            <li>
              <strong>Timestamp outside the cycle</strong>. Events whose{' '}
              <InlineCode>timestamp</InlineCode> falls before{' '}
              <InlineCode>current_billing_period_start</InlineCode> are accepted but slotted to
              the prior period — already-finalised invoices don't get retroactive line items.
            </li>
            <li>
              <strong>Wrong tenant or mode</strong>. A live API key cannot post events to a test
              subscription, and vice versa. RLS will return 201 silently if the customer{' '}
              <InlineCode>external_id</InlineCode> happens to exist in the key's tenant under a
              different mode — the event is recorded, just on a different customer.
            </li>
          </ol>

          <DocsH2>422 idempotency_error on retry</DocsH2>
          <DocsH3>Symptom</DocsH3>
          <p>
            Second call to a write endpoint with the same{' '}
            <InlineCode>Idempotency-Key</InlineCode> returns 422{' '}
            <InlineCode>idempotency_error</InlineCode>.
          </p>
          <DocsH3>Why</DocsH3>
          <p>
            The body of the second request differs from the first. Velox treats this as a key
            reuse bug rather than silently overwriting — the most common cause is a timestamp
            or generated UUID inside the body that you didn't intend to be part of the request
            shape.
          </p>
          <DocsH3>Fix</DocsH3>
          <p>
            Either use a fresh idempotency key for the new operation, or pin the variable parts
            of the body so retries are byte-identical to the first attempt. See the{' '}
            <a href="/docs/idempotency" className="text-primary underline underline-offset-2">
              Idempotency &amp; retries
            </a>{' '}
            page for the full contract.
          </p>

          <DocsH2>billing_setup_incomplete on invoice charge</DocsH2>
          <DocsH3>Symptom</DocsH3>
          <p>
            <InlineCode>POST /v1/invoices/:id/charge</InlineCode> returns 422{' '}
            <InlineCode>billing_setup_incomplete</InlineCode>.
          </p>
          <DocsH3>Fix</DocsH3>
          <p>
            The tenant has no Stripe credentials configured for the relevant mode. Connect them
            in Settings → Payments. Test-mode invoices need test keys; live-mode invoices need
            live keys. Connecting one mode does not configure the other.
          </p>

          <DocsH2>Coupon won't apply</DocsH2>
          <DocsH3>Symptom</DocsH3>
          <p>
            <InlineCode>POST /v1/invoices/:id/apply-coupon</InlineCode> returns 422 with a
            <InlineCode>coupon_*</InlineCode> code.
          </p>
          <DocsH3>What the codes mean</DocsH3>
          <ul className="list-disc pl-6 space-y-1">
            <li><InlineCode>coupon_expired</InlineCode> — past <InlineCode>expires_at</InlineCode>.</li>
            <li><InlineCode>coupon_currency_mismatch</InlineCode> — coupon currency ≠ invoice currency.</li>
            <li><InlineCode>coupon_min_amount_not_met</InlineCode> — invoice subtotal below the coupon's minimum.</li>
            <li><InlineCode>coupon_plan_mismatch</InlineCode> — restricted to plans not on this invoice.</li>
            <li><InlineCode>coupon_max_redemptions_reached</InlineCode> or <InlineCode>coupon_per_customer_limit_reached</InlineCode> — redemption budget exhausted.</li>
          </ul>
          <p>
            See <a href="/docs/errors" className="text-primary underline underline-offset-2">Errors</a>{' '}
            for the full list.
          </p>

          <DocsH2>Bootstrap returns 403 "bootstrap disabled"</DocsH2>
          <DocsH3>Symptom</DocsH3>
          <p>
            <InlineCode>POST /v1/bootstrap</InlineCode> returns 403 with the message{' '}
            <em>bootstrap disabled — set VELOX_BOOTSTRAP_TOKEN env var to enable</em>.
          </p>
          <DocsH3>Fix</DocsH3>
          <p>
            The endpoint is gated by an env var, deliberately. Generate a token, set it on the
            running process, then retry:
          </p>
          <Code language="shell">{`export VELOX_BOOTSTRAP_TOKEN=$(openssl rand -hex 32)
# restart the velox process so it picks up the env var
curl -X POST $VELOX_API/bootstrap \\
  -H "Content-Type: application/json" \\
  -d '{"token": "'"$VELOX_BOOTSTRAP_TOKEN"'", "tenant_name": "My Company"}'`}</Code>
          <p>
            If you get 409 <InlineCode>already_exists</InlineCode> instead, a tenant has already
            been created. Create additional tenants with the{' '}
            <InlineCode>cmd/velox-bootstrap</InlineCode> CLI, not this endpoint.
          </p>

          <DocsH2>Webhook signature fails verification</DocsH2>
          <DocsH3>Common causes</DocsH3>
          <ul className="list-disc pl-6 space-y-1">
            <li>
              You're verifying with the wrong secret. After rotating, both the old and new
              secrets are valid for 72 hours — accept either during the window.
            </li>
            <li>
              You parsed the body as JSON before computing the HMAC. Verification must run on
              the <em>raw</em> request bytes; <InlineCode>JSON.parse</InlineCode> +{' '}
              <InlineCode>JSON.stringify</InlineCode> changes whitespace and breaks the signature.
            </li>
            <li>
              Clock skew &gt; 5 minutes between Velox and your receiver. Replay protection
              rejects timestamps too far from <InlineCode>now</InlineCode>.
            </li>
          </ul>

          <Callout tone="info">
            Every error response carries a <InlineCode>request_id</InlineCode>. Log it on the
            client side and quote it when filing a support ticket — it lets us correlate your
            failure with the exact server-side trace.
          </Callout>
        </Prose>
      </DocsShell>
    </PublicLayout>
  )
}
