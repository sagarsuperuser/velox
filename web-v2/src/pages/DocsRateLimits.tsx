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

export default function DocsRateLimitsPage() {
  return (
    <PublicLayout>
      <DocsShell>
        <Prose>
          <DocsH1>Rate limits</DocsH1>
          <DocsLead>
            Velox uses a smooth-refill GCRA limiter — burst up to the limit, then refill steadily.
            Headers tell you exactly how much budget you have on every response, so a polite client
            never needs to see a 429.
          </DocsLead>

          <DocsH2>Default limits</DocsH2>
          <div className="border border-border rounded-lg overflow-hidden my-4">
            <table className="w-full text-sm">
              <thead className="bg-muted/40">
                <tr>
                  <th className="text-left p-3 font-medium">Bucket</th>
                  <th className="text-left p-3 font-medium">Limit</th>
                  <th className="text-left p-3 font-medium">Applies to</th>
                </tr>
              </thead>
              <tbody>
                <tr className="border-t border-border">
                  <td className="p-3 align-top">Per API key</td>
                  <td className="p-3 align-top">100 req/min</td>
                  <td className="p-3">Any request authenticated with a secret or publishable key. Each key has its own bucket, so two integrations on the same tenant don't compete.</td>
                </tr>
                <tr className="border-t border-border">
                  <td className="p-3 align-top">Per tenant (session)</td>
                  <td className="p-3 align-top">100 req/min</td>
                  <td className="p-3">Dashboard traffic authenticated by session cookie. All dashboard users for one tenant share this bucket.</td>
                </tr>
                <tr className="border-t border-border">
                  <td className="p-3 align-top">Per IP (public)</td>
                  <td className="p-3 align-top">60 req/min</td>
                  <td className="p-3">Hosted invoice page (<InlineCode>/v1/public/invoices/:token</InlineCode>) and cost dashboard embed (<InlineCode>/v1/public/cost-dashboard/:token</InlineCode>).</td>
                </tr>
              </tbody>
            </table>
          </div>
          <p>
            Buckets are independent. Hitting the per-key limit does not affect dashboard traffic
            for the same tenant, and exhausting one tenant's bucket has no effect on any other
            tenant.
          </p>

          <DocsH2>Response headers</DocsH2>
          <p>
            Every request — successful or not — carries three headers describing the relevant
            bucket. <InlineCode>Retry-After</InlineCode> is added when the limit fires:
          </p>
          <Code language="text">{`X-RateLimit-Limit: 100
X-RateLimit-Remaining: 87
X-RateLimit-Reset: 1714240320
Retry-After: 12`}</Code>
          <ul className="list-disc pl-6 space-y-1">
            <li>
              <InlineCode>X-RateLimit-Limit</InlineCode> — the bucket size in requests per minute.
            </li>
            <li>
              <InlineCode>X-RateLimit-Remaining</InlineCode> — how many requests you can still make
              before the next refill tick.
            </li>
            <li>
              <InlineCode>X-RateLimit-Reset</InlineCode> — Unix timestamp (seconds) at which the
              bucket is fully refilled.
            </li>
            <li>
              <InlineCode>Retry-After</InlineCode> — only on <InlineCode>429</InlineCode>; whole
              seconds to wait. Always at least 1.
            </li>
          </ul>

          <DocsH2>The 429 response</DocsH2>
          <Code language="json">{`{
  "error": {
    "type": "rate_limit_error",
    "code": "rate_limited",
    "message": "too many requests — please retry after the rate limit resets",
    "request_id": "req_01HX..."
  }
}`}</Code>
          <p>
            Wait <InlineCode>Retry-After</InlineCode> seconds, then retry with the same{' '}
            <InlineCode>Idempotency-Key</InlineCode> if the request is a write. The middleware
            does not record the request when it 429s, so a retry is not double-charged.
          </p>

          <DocsH2>Exempt endpoints</DocsH2>
          <ul className="list-disc pl-6 space-y-1">
            <li><InlineCode>GET /health</InlineCode> and <InlineCode>GET /health/ready</InlineCode></li>
            <li><InlineCode>GET /metrics</InlineCode></li>
            <li><InlineCode>POST /v1/bootstrap</InlineCode></li>
          </ul>
          <p>
            These endpoints have no limit because they're used by liveness probes and the
            one-time bootstrap flow. Don't build production traffic on top of them.
          </p>

          <DocsH2>Handling 429 in code</DocsH2>
          <p>
            Read <InlineCode>X-RateLimit-Remaining</InlineCode> on every response and slow down
            before you hit zero. When you do see a <InlineCode>429</InlineCode>, honor{' '}
            <InlineCode>Retry-After</InlineCode> exactly — don't multiply or add jitter on top of
            it; the limiter has already accounted for refill smoothness:
          </p>
          <Code language="node">{`async function withRateLimitRetry(fn, maxRetries = 3) {
  for (let i = 0; i <= maxRetries; i++) {
    const res = await fn()
    if (res.status !== 429) return res

    const retryAfter = Number(res.headers.get('Retry-After') ?? 1)
    await sleep(retryAfter * 1000)
  }
  throw new Error('rate-limited after retries')
}`}</Code>

          <DocsH2>Going faster</DocsH2>
          <p>
            For high-throughput integrations, two tactics work well together:
          </p>
          <ol className="list-decimal pl-6 space-y-1">
            <li>
              <strong>Use the batch endpoint</strong> for usage events:{' '}
              <InlineCode>POST /v1/usage-events/batch</InlineCode> accepts up to 1,000 events in
              a single request and counts as one against the limit.
            </li>
            <li>
              <strong>Issue a key per workload.</strong> A separate API key per service or pipeline
              gets its own 100 req/min bucket. Per-key visibility in the dashboard also makes
              auditing easier.
            </li>
          </ol>

          <Callout tone="info">
            The limiter fails open if Redis is unreachable in development, and fails closed in
            production. If you see surprisingly high request volumes succeed in dev, that's why —
            production behaviour is strict.
          </Callout>
        </Prose>
      </DocsShell>
    </PublicLayout>
  )
}
