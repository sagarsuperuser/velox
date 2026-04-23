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

export default function DocsIdempotencyPage() {
  return (
    <PublicLayout>
      <DocsShell>
        <Prose>
          <DocsH1>Idempotency &amp; retries</DocsH1>
          <DocsLead>
            Any mutating request (POST / PUT / PATCH) can be safely retried by including an{' '}
            <InlineCode>Idempotency-Key</InlineCode> header. Velox caches the response for 24 hours
            and returns the same body on every replay.
          </DocsLead>

          <DocsH2>When to use it</DocsH2>
          <p>
            Always. Network timeouts, client crashes, and load-balancer failovers can all cause a
            client to retry a request that already succeeded on the server. Without an idempotency
            key, you risk double-charging a customer or duplicating an invoice. With one, retries are
            safe by construction.
          </p>

          <DocsH2>How to use it</DocsH2>
          <p>
            Generate a UUID (v4 or v7) per logical operation, and send it on every attempt for that
            operation:
          </p>
          <Code language="shell">{`curl -X POST $VELOX_API/invoices \\
  -H "Authorization: Bearer $VELOX_KEY" \\
  -H "Idempotency-Key: $(uuidgen)" \\
  -H "Content-Type: application/json" \\
  -d '{ "customer_id": "cus_01HX...", "line_items": [...] }'`}</Code>

          <DocsH2>Behavior matrix</DocsH2>
          <div className="border border-border rounded-lg overflow-hidden my-4">
            <table className="w-full text-sm">
              <thead className="bg-muted/40">
                <tr>
                  <th className="text-left p-3 font-medium">Scenario</th>
                  <th className="text-left p-3 font-medium">Response</th>
                </tr>
              </thead>
              <tbody>
                <tr className="border-t border-border">
                  <td className="p-3">First request with key <InlineCode>K</InlineCode></td>
                  <td className="p-3">Processed normally. Response cached for 24h.</td>
                </tr>
                <tr className="border-t border-border">
                  <td className="p-3">Retry with same key <InlineCode>K</InlineCode> and same body</td>
                  <td className="p-3">
                    Same response replayed. Header{' '}
                    <InlineCode>Idempotent-Replayed: true</InlineCode> is set.
                  </td>
                </tr>
                <tr className="border-t border-border">
                  <td className="p-3">Retry with same key <InlineCode>K</InlineCode> but different body</td>
                  <td className="p-3">
                    <InlineCode>422</InlineCode>{' '}
                    <InlineCode>{`{ "code": "idempotency_error" }`}</InlineCode>. Use a new key for a
                    new operation.
                  </td>
                </tr>
                <tr className="border-t border-border">
                  <td className="p-3">Key reused after 24h</td>
                  <td className="p-3">Treated as a new request (cache expired).</td>
                </tr>
                <tr className="border-t border-border">
                  <td className="p-3">GET / HEAD / OPTIONS with key</td>
                  <td className="p-3">Key ignored; reads are already idempotent.</td>
                </tr>
              </tbody>
            </table>
          </div>

          <DocsH2>Detecting a replay</DocsH2>
          <p>
            If the response includes <InlineCode>Idempotent-Replayed: true</InlineCode>, the original
            request was already processed. You can use this to decide whether to emit follow-up side
            effects on your own side:
          </p>
          <Code language="node">{`const res = await fetch(\`\${api}/invoices\`, {
  method: 'POST',
  headers: {
    'Authorization': \`Bearer \${key}\`,
    'Idempotency-Key': key,
    'Content-Type': 'application/json',
  },
  body: JSON.stringify(payload),
})

if (res.headers.get('Idempotent-Replayed') === 'true') {
  // This request was cached. Skip any client-side side effects
  // (analytics events, audit log entries) that already fired.
}`}</Code>

          <DocsH2>Retry strategy</DocsH2>
          <p>
            When a request fails with a network error or a 5xx response, retry with the <em>same</em>{' '}
            idempotency key. For 4xx responses, do not retry — the request is invalid and retries
            will fail identically.
          </p>
          <Code language="node">{`async function postWithRetry(url, body, key, maxAttempts = 4) {
  for (let i = 0; i < maxAttempts; i++) {
    try {
      const res = await fetch(url, {
        method: 'POST',
        headers: {
          'Authorization': \`Bearer \${apiKey}\`,
          'Idempotency-Key': key,
          'Content-Type': 'application/json',
        },
        body: JSON.stringify(body),
      })
      if (res.ok) return res.json()
      if (res.status >= 400 && res.status < 500) throw new Error(await res.text())
      // 5xx: backoff then retry
    } catch (e) {
      if (i === maxAttempts - 1) throw e
    }
    await sleep(2 ** i * 1000) // 1s, 2s, 4s, 8s
  }
}`}</Code>

          <DocsH2>Key lifecycle</DocsH2>
          <p>
            A good rule of thumb: <strong>one key per user-intent</strong>. If your user clicks "Pay"
            and you retry internally, use the same key. If your user clicks "Pay" a second time
            (different intent), use a new key.
          </p>
          <Callout tone="info">
            Idempotency-Key works across all mutating endpoints:{' '}
            <InlineCode>/customers</InlineCode>, <InlineCode>/subscriptions</InlineCode>,{' '}
            <InlineCode>/invoices</InlineCode>, <InlineCode>/coupons</InlineCode>,{' '}
            <InlineCode>/credit_notes</InlineCode>, <InlineCode>/webhook_endpoints</InlineCode>, and
            sub-resource actions like <InlineCode>/invoices/:id/finalize</InlineCode>.
          </Callout>
        </Prose>
      </DocsShell>
    </PublicLayout>
  )
}
