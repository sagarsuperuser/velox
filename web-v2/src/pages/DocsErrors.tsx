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

export default function DocsErrorsPage() {
  return (
    <PublicLayout>
      <DocsShell>
        <Prose>
          <DocsH1>Errors</DocsH1>
          <DocsLead>
            Every error response from the Velox API has the same shape. The HTTP status tells you
            what category of failure it is; the <InlineCode>code</InlineCode> tells you the
            specific reason.
          </DocsLead>

          <DocsH2>Response shape</DocsH2>
          <p>
            Errors are returned as JSON with a single <InlineCode>error</InlineCode> envelope:
          </p>
          <Code language="json">{`{
  "error": {
    "type": "invalid_request_error",
    "code": "validation_error",
    "message": "currency must be a 3-letter ISO code",
    "param": "currency",
    "request_id": "req_01HXABCDEFGHJKMNPQRSTVWXYZ"
  }
}`}</Code>
          <ul className="list-disc pl-6 space-y-1">
            <li>
              <InlineCode>type</InlineCode> — one of{' '}
              <InlineCode>invalid_request_error</InlineCode>,{' '}
              <InlineCode>authentication_error</InlineCode>,{' '}
              <InlineCode>rate_limit_error</InlineCode>, or <InlineCode>api_error</InlineCode>.
            </li>
            <li>
              <InlineCode>code</InlineCode> — a stable, machine-readable identifier (see table
              below). Branch on this, not on <InlineCode>message</InlineCode>.
            </li>
            <li>
              <InlineCode>message</InlineCode> — a human-readable explanation. Safe to surface
              in dashboards, never safe to parse.
            </li>
            <li>
              <InlineCode>param</InlineCode> — when the failure is tied to a single field, the
              field name. Use this to highlight form inputs.
            </li>
            <li>
              <InlineCode>request_id</InlineCode> — also returned in the{' '}
              <InlineCode>Velox-Request-Id</InlineCode> response header. Quote this when filing
              a support ticket.
            </li>
          </ul>

          <DocsH2>HTTP status codes</DocsH2>
          <div className="border border-border rounded-lg overflow-hidden my-4">
            <table className="w-full text-sm">
              <thead className="bg-muted/40">
                <tr>
                  <th className="text-left p-3 font-medium">Status</th>
                  <th className="text-left p-3 font-medium">Type</th>
                  <th className="text-left p-3 font-medium">When</th>
                </tr>
              </thead>
              <tbody>
                <tr className="border-t border-border">
                  <td className="p-3 align-top"><InlineCode>400</InlineCode></td>
                  <td className="p-3 align-top">invalid_request_error</td>
                  <td className="p-3">Malformed JSON, unparseable input, missing required header.</td>
                </tr>
                <tr className="border-t border-border">
                  <td className="p-3 align-top"><InlineCode>401</InlineCode></td>
                  <td className="p-3 align-top">authentication_error</td>
                  <td className="p-3">Missing or invalid API key.</td>
                </tr>
                <tr className="border-t border-border">
                  <td className="p-3 align-top"><InlineCode>403</InlineCode></td>
                  <td className="p-3 align-top">authentication_error</td>
                  <td className="p-3">Key is valid but lacks permission, or RLS blocked a cross-tenant read.</td>
                </tr>
                <tr className="border-t border-border">
                  <td className="p-3 align-top"><InlineCode>404</InlineCode></td>
                  <td className="p-3 align-top">invalid_request_error</td>
                  <td className="p-3">Resource doesn't exist, or exists in another tenant.</td>
                </tr>
                <tr className="border-t border-border">
                  <td className="p-3 align-top"><InlineCode>409</InlineCode></td>
                  <td className="p-3 align-top">invalid_request_error</td>
                  <td className="p-3">Duplicate <InlineCode>external_id</InlineCode>, coupon code collision, conflicting state.</td>
                </tr>
                <tr className="border-t border-border">
                  <td className="p-3 align-top"><InlineCode>412</InlineCode></td>
                  <td className="p-3 align-top">invalid_request_error</td>
                  <td className="p-3"><InlineCode>If-Match</InlineCode> ETag mismatch (optimistic concurrency).</td>
                </tr>
                <tr className="border-t border-border">
                  <td className="p-3 align-top"><InlineCode>422</InlineCode></td>
                  <td className="p-3 align-top">invalid_request_error</td>
                  <td className="p-3">Semantic validation failed (bad format, out of range, invalid state). <InlineCode>param</InlineCode> identifies the field.</td>
                </tr>
                <tr className="border-t border-border">
                  <td className="p-3 align-top"><InlineCode>429</InlineCode></td>
                  <td className="p-3 align-top">rate_limit_error</td>
                  <td className="p-3">Rate limit exceeded. See <InlineCode>Retry-After</InlineCode> header and the rate limits page.</td>
                </tr>
                <tr className="border-t border-border">
                  <td className="p-3 align-top"><InlineCode>500</InlineCode></td>
                  <td className="p-3 align-top">api_error</td>
                  <td className="p-3">Unhandled server failure. Safe to retry with the same idempotency key.</td>
                </tr>
              </tbody>
            </table>
          </div>

          <DocsH2>Common error codes</DocsH2>
          <p>
            The <InlineCode>code</InlineCode> field is the stable identifier for an error. New
            codes may be added; existing ones do not change meaning. Branch on these:
          </p>
          <div className="border border-border rounded-lg overflow-hidden my-4">
            <table className="w-full text-sm">
              <thead className="bg-muted/40">
                <tr>
                  <th className="text-left p-3 font-medium">Code</th>
                  <th className="text-left p-3 font-medium">Status</th>
                  <th className="text-left p-3 font-medium">What it means</th>
                </tr>
              </thead>
              <tbody>
                <tr className="border-t border-border">
                  <td className="p-3 align-top"><InlineCode>invalid_request</InlineCode></td>
                  <td className="p-3 align-top">400</td>
                  <td className="p-3">Body could not be parsed as JSON, or a required header is missing.</td>
                </tr>
                <tr className="border-t border-border">
                  <td className="p-3 align-top"><InlineCode>unauthorized</InlineCode></td>
                  <td className="p-3 align-top">401</td>
                  <td className="p-3">No <InlineCode>Authorization</InlineCode> header, or the API key is unrecognised.</td>
                </tr>
                <tr className="border-t border-border">
                  <td className="p-3 align-top"><InlineCode>forbidden</InlineCode></td>
                  <td className="p-3 align-top">403</td>
                  <td className="p-3">Key type lacks permission for this endpoint (e.g. publishable key calling a secret-only route).</td>
                </tr>
                <tr className="border-t border-border">
                  <td className="p-3 align-top"><InlineCode>not_found</InlineCode></td>
                  <td className="p-3 align-top">404</td>
                  <td className="p-3">Resource ID does not exist in this tenant + livemode scope.</td>
                </tr>
                <tr className="border-t border-border">
                  <td className="p-3 align-top"><InlineCode>already_exists</InlineCode></td>
                  <td className="p-3 align-top">409</td>
                  <td className="p-3">Unique constraint violation (e.g. <InlineCode>external_id</InlineCode> already used).</td>
                </tr>
                <tr className="border-t border-border">
                  <td className="p-3 align-top"><InlineCode>idempotency_error</InlineCode></td>
                  <td className="p-3 align-top">422</td>
                  <td className="p-3">Same <InlineCode>Idempotency-Key</InlineCode> reused with a different request body.</td>
                </tr>
                <tr className="border-t border-border">
                  <td className="p-3 align-top"><InlineCode>validation_error</InlineCode></td>
                  <td className="p-3 align-top">422</td>
                  <td className="p-3">A field is present but invalid. <InlineCode>param</InlineCode> names the field.</td>
                </tr>
                <tr className="border-t border-border">
                  <td className="p-3 align-top"><InlineCode>billing_setup_incomplete</InlineCode></td>
                  <td className="p-3 align-top">422</td>
                  <td className="p-3">Action requires Stripe credentials. Connect Stripe in Settings → Payments.</td>
                </tr>
                <tr className="border-t border-border">
                  <td className="p-3 align-top"><InlineCode>currency_mismatch</InlineCode></td>
                  <td className="p-3 align-top">422</td>
                  <td className="p-3">Two referenced resources disagree on currency (e.g. coupon currency ≠ invoice currency).</td>
                </tr>
                <tr className="border-t border-border">
                  <td className="p-3 align-top"><InlineCode>subscription_invalid</InlineCode></td>
                  <td className="p-3 align-top">422</td>
                  <td className="p-3">State transition is not allowed (e.g. cancelling an already-cancelled subscription).</td>
                </tr>
                <tr className="border-t border-border">
                  <td className="p-3 align-top"><InlineCode>rate_limited</InlineCode></td>
                  <td className="p-3 align-top">429</td>
                  <td className="p-3">Per-key, per-tenant, or per-IP bucket exhausted. Wait <InlineCode>Retry-After</InlineCode> seconds.</td>
                </tr>
                <tr className="border-t border-border">
                  <td className="p-3 align-top"><InlineCode>internal_error</InlineCode></td>
                  <td className="p-3 align-top">500</td>
                  <td className="p-3">Unexpected server failure. Retry with the same idempotency key.</td>
                </tr>
              </tbody>
            </table>
          </div>

          <DocsH2>Coupon-specific codes</DocsH2>
          <p>
            <InlineCode>POST /v1/invoices/:id/apply-coupon</InlineCode> and friends can return any
            of these in addition to the generic codes above. They all use status{' '}
            <InlineCode>422</InlineCode>:
          </p>
          <ul className="list-disc pl-6 space-y-1">
            <li><InlineCode>coupon_not_found</InlineCode>, <InlineCode>coupon_archived</InlineCode>, <InlineCode>coupon_expired</InlineCode></li>
            <li><InlineCode>coupon_max_redemptions_reached</InlineCode>, <InlineCode>coupon_per_customer_limit_reached</InlineCode></li>
            <li><InlineCode>coupon_first_time_only</InlineCode>, <InlineCode>coupon_plan_mismatch</InlineCode></li>
            <li><InlineCode>coupon_currency_mismatch</InlineCode>, <InlineCode>coupon_min_amount_not_met</InlineCode></li>
            <li><InlineCode>coupon_code_taken</InlineCode>, <InlineCode>coupon_already_assigned</InlineCode></li>
          </ul>

          <DocsH2>Handling errors in code</DocsH2>
          <p>
            Branch on <InlineCode>error.code</InlineCode>, not on the HTTP status alone — many
            different codes share the same status (especially <InlineCode>422</InlineCode>):
          </p>
          <Code language="node">{`const res = await fetch(\`\${api}/invoices\`, { method: 'POST', headers, body })

if (!res.ok) {
  const { error } = await res.json()

  switch (error.code) {
    case 'idempotency_error':
      // The retry used the same key but a different body. Generate a new key.
      throw new Error('Internal: idempotency key reused')
    case 'billing_setup_incomplete':
      // Surface to operator: Stripe not connected.
      return showBanner('Connect Stripe in Settings to continue.')
    case 'rate_limited': {
      const retryAfter = Number(res.headers.get('Retry-After') ?? 1)
      await sleep(retryAfter * 1000)
      return retry()
    }
    case 'validation_error':
      // Surface error.message next to the field named in error.param.
      return highlightField(error.param, error.message)
    default:
      // Log error.request_id and surface a generic message.
      logger.error({ requestId: error.request_id, code: error.code })
      return showToast('Something went wrong.')
  }
}`}</Code>

          <Callout tone="info">
            The <InlineCode>Velox-Request-Id</InlineCode> response header carries the same value as{' '}
            <InlineCode>error.request_id</InlineCode> and is also set on successful responses. Log
            it on every request so you can correlate client-side incidents with server logs.
          </Callout>
        </Prose>
      </DocsShell>
    </PublicLayout>
  )
}
