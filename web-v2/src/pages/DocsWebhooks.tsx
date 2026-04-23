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

export default function DocsWebhooksPage() {
  return (
    <PublicLayout>
      <DocsShell>
        <Prose>
          <DocsH1>Webhooks</DocsH1>
          <DocsLead>
            Velox sends signed HTTP POST requests to your endpoints whenever a billing state changes.
            Use webhooks to keep downstream systems in sync — your CRM, data warehouse, revenue
            recognition, or notifications.
          </DocsLead>

          <DocsH2>Creating an endpoint</DocsH2>
          <p>
            Add an endpoint in the dashboard under <InlineCode>Webhooks → Endpoints</InlineCode> or
            via the API:
          </p>
          <Code language="shell">{`curl -X POST $VELOX_API/webhook_endpoints \\
  -H "Authorization: Bearer $VELOX_KEY" \\
  -d '{
    "url": "https://app.example.com/webhooks/velox",
    "event_types": ["*"],
    "description": "Production billing events"
  }'`}</Code>
          <p>
            The response includes a <InlineCode>secret</InlineCode> beginning with{' '}
            <InlineCode>whsec_…</InlineCode>. It is shown <strong>once only</strong>. Store it in your
            secrets manager; you'll use it to verify incoming requests.
          </p>

          <DocsH2>Signature verification</DocsH2>
          <p>
            Every delivery includes two headers:
          </p>
          <ul className="list-disc pl-6 space-y-1 text-sm">
            <li>
              <InlineCode>Velox-Signature</InlineCode> — HMAC-SHA256 of{' '}
              <InlineCode>{'{timestamp}.{raw_body}'}</InlineCode>, hex-encoded.
            </li>
            <li>
              <InlineCode>Velox-Timestamp</InlineCode> — Unix seconds. Reject requests older than 5
              minutes to prevent replay.
            </li>
          </ul>

          <Code language="go">{`import (
  "crypto/hmac"
  "crypto/sha256"
  "encoding/hex"
  "strconv"
  "time"
)

func verify(body []byte, timestamp, signature, secret string) bool {
  ts, err := strconv.ParseInt(timestamp, 10, 64)
  if err != nil || time.Since(time.Unix(ts, 0)) > 5*time.Minute {
    return false
  }
  mac := hmac.New(sha256.New, []byte(secret))
  mac.Write([]byte(timestamp + "." + string(body)))
  expected := hex.EncodeToString(mac.Sum(nil))
  return hmac.Equal([]byte(expected), []byte(signature))
}`}</Code>

          <Code language="node">{`import crypto from 'node:crypto'

export function verify(body, timestamp, signature, secret) {
  if (Math.abs(Date.now() / 1000 - Number(timestamp)) > 300) return false
  const expected = crypto
    .createHmac('sha256', secret)
    .update(\`\${timestamp}.\${body}\`)
    .digest('hex')
  return crypto.timingSafeEqual(Buffer.from(expected), Buffer.from(signature))
}`}</Code>

          <Callout tone="warn">
            Always verify against the <strong>raw request body</strong> (bytes as received), not a
            parsed-and-re-serialized JSON. Re-serialization changes whitespace and breaks the
            signature.
          </Callout>

          <DocsH2>Retry schedule</DocsH2>
          <p>
            If your endpoint returns a non-2xx response or times out (10s), Velox retries up to five
            times with increasing backoff:
          </p>
          <div className="border border-border rounded-lg overflow-hidden my-4">
            <table className="w-full text-sm">
              <thead className="bg-muted/40">
                <tr>
                  <th className="text-left p-3 font-medium">Attempt</th>
                  <th className="text-left p-3 font-medium">Delay after previous</th>
                  <th className="text-left p-3 font-medium">Total elapsed</th>
                </tr>
              </thead>
              <tbody className="font-mono text-[13px]">
                <tr className="border-t border-border"><td className="p-3">1</td><td className="p-3">—</td><td className="p-3">0</td></tr>
                <tr className="border-t border-border"><td className="p-3">2</td><td className="p-3">1 minute</td><td className="p-3">1 min</td></tr>
                <tr className="border-t border-border"><td className="p-3">3</td><td className="p-3">5 minutes</td><td className="p-3">6 min</td></tr>
                <tr className="border-t border-border"><td className="p-3">4</td><td className="p-3">30 minutes</td><td className="p-3">36 min</td></tr>
                <tr className="border-t border-border"><td className="p-3">5</td><td className="p-3">2 hours</td><td className="p-3">~2.5 hr</td></tr>
                <tr className="border-t border-border"><td className="p-3">6 (final)</td><td className="p-3">24 hours</td><td className="p-3">~26.5 hr</td></tr>
              </tbody>
            </table>
          </div>
          <p>
            After the final attempt, delivery is marked <InlineCode>failed</InlineCode>. You can replay
            any event from the dashboard <InlineCode>Webhooks → Events</InlineCode> view.
          </p>

          <DocsH2>Idempotent handling</DocsH2>
          <p>
            Every event has a stable <InlineCode>id</InlineCode> (e.g.,{' '}
            <InlineCode>evt_01HX…</InlineCode>). Due to retries, your endpoint can receive the same
            event multiple times. Dedupe on <InlineCode>id</InlineCode>:
          </p>
          <Code language="sql">{`-- Postgres: table + unique constraint
CREATE TABLE received_velox_events (id TEXT PRIMARY KEY);

-- In your handler:
INSERT INTO received_velox_events(id) VALUES ($1)
  ON CONFLICT (id) DO NOTHING;
-- If affected rows == 0, you've already processed this event. Return 200.`}</Code>

          <DocsH2>Event catalogue</DocsH2>
          <p>A non-exhaustive list of commonly-subscribed events:</p>
          <div className="grid grid-cols-1 md:grid-cols-2 gap-x-6 gap-y-2 text-sm font-mono my-4">
            {[
              'customer.created',
              'customer.updated',
              'customer.archived',
              'subscription.created',
              'subscription.updated',
              'subscription.paused',
              'subscription.resumed',
              'subscription.canceled',
              'invoice.created',
              'invoice.finalized',
              'invoice.voided',
              'invoice.paid',
              'invoice.payment_failed',
              'invoice.coupon.applied',
              'credit_note.issued',
              'coupon.assigned',
              'coupon.unassigned',
              'webhook_endpoint.secret_rotated',
            ].map((ev) => (
              <code key={ev} className="text-muted-foreground">{ev}</code>
            ))}
          </div>

          <DocsH2>Rotating the signing secret</DocsH2>
          <p>
            From <InlineCode>Webhooks → Endpoints → …</InlineCode>, click <strong>Rotate secret</strong>.
            The new secret is shown once; the previous secret becomes invalid immediately.
          </p>
          <Callout tone="warn">
            A grace-period dual-signing mode (both old and new secret accepted for 72h) is on the
            roadmap — until it ships, plan to deploy the new secret within a short window.
          </Callout>
        </Prose>
      </DocsShell>
    </PublicLayout>
  )
}
