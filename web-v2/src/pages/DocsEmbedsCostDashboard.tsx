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

export default function DocsEmbedsCostDashboardPage() {
  return (
    <PublicLayout>
      <DocsShell>
        <Prose>
          <DocsH1>Embed: cost dashboard</DocsH1>
          <DocsLead>
            Show your customers their live usage and the period bill in your own app — no auth
            handoff, no JS bundle to install. The cost dashboard is rendered by Velox at a public
            URL behind a per-customer secret token, so you embed it as a single iframe and
            we keep the data in sync.
          </DocsLead>

          <DocsH2>What is the cost dashboard embed?</DocsH2>
          <p>
            Every customer in Velox has a usage view: cycle-to-date charges per meter, the
            in-progress billing window, any spend alerts they've configured, and (when an active
            subscription exists) a projected total for the period. The embed exposes that same
            view at a token-protected public URL so your app can drop it into a customer-facing
            page without writing a usage UI from scratch. The token IS the auth — there's no
            cookie or Authorization header to manage on the customer side, which means the iframe
            works across origins and survives logged-out / SSO-bridged contexts.
          </p>

          <DocsH2>Mint an embed URL</DocsH2>
          <p>
            Call the operator-authed rotate endpoint to mint a token for a customer. Every call
            returns a fresh URL and invalidates the prior one for that customer — that's how you
            revoke a leaked link.
          </p>
          <Code language="shell">{`curl -X POST $VELOX_API/customers/$CUSTOMER_ID/rotate-cost-dashboard-token \\
  -H "Authorization: Bearer $VELOX_KEY"`}</Code>
          <p>The response carries the token and the full public URL ready to paste:</p>
          <Code language="json">{`{
  "token": "vlx_pcd_4e2c8a1b9f3d6e7c0a5b8d4e1f2c3a6b",
  "public_url": "https://app.velox.dev/public/cost-dashboard/vlx_pcd_4e2c8a1b9f3d6e7c0a5b8d4e1f2c3a6b"
}`}</Code>
          <p>
            From the dashboard you can mint the same URL via the{' '}
            <strong>Embed dashboard</strong> button on the customer detail page — that's a
            one-click wrapper around the same call.
          </p>

          <DocsH2>Embed via iframe</DocsH2>
          <p>
            Drop the URL into an iframe wherever your customer logs in. The page is built to
            adapt to the iframe's width, so size it however your layout wants:
          </p>
          <Code language="html">{`<iframe
  src="https://app.velox.dev/public/cost-dashboard/vlx_pcd_4e2c8a1b9f3d6e7c0a5b8d4e1f2c3a6b"
  width="100%"
  height="600"
  frameborder="0"
  title="Cost dashboard"></iframe>`}</Code>
          <p>
            For best behaviour in modern browsers, also set{' '}
            <InlineCode>loading="lazy"</InlineCode> if the iframe is below the fold and{' '}
            <InlineCode>referrerpolicy="no-referrer"</InlineCode> if your app keeps the embed
            URL out of analytics tooling.
          </p>

          <DocsH2>Token rotation</DocsH2>
          <p>
            Tokens are stable until you rotate them. Calling{' '}
            <InlineCode>POST /v1/customers/{'{id}'}/rotate-cost-dashboard-token</InlineCode>{' '}
            again returns a new token and invalidates the previous one immediately — any iframe
            still pointing at the old URL will start rendering the "link no longer valid" empty
            state on its next refresh. Use this whenever:
          </p>
          <ul className="list-disc pl-6 space-y-1 text-sm">
            <li>A link leaks (e.g. forwarded outside the customer's account, indexed by a search engine).</li>
            <li>A customer requests a rotated link as part of a security review.</li>
            <li>You're cycling tokens on a schedule for defence in depth.</li>
          </ul>
          <p>
            Tokens carry no expiry by themselves — the next rotation is the only thing that
            invalidates them. Plan accordingly if your compliance posture requires periodic
            rotation.
          </p>

          <Callout tone="info">
            The public route lives in the same rate-limit bucket as hosted invoices
            (<InlineCode>hostedInvoiceRL</InlineCode>) so a misbehaving iframe in one tenant
            cannot starve another tenant. If you see 429s in production, throttle the iframe's
            refresh cadence — the page is a snapshot, not a live console.
          </Callout>

          <DocsH2>What's customizable today vs. coming in v1.1</DocsH2>
          <p>
            <strong>Today.</strong> The embed renders against the dashboard's existing theme.
            Light and dark mode track the page's <InlineCode>color-scheme</InlineCode>. There is
            no per-tenant theming surface — if your customer's app uses a brand colour or a
            non-default font, wrap the iframe in your own chrome (a card border, a header) and
            let the dashboard handle the data.
          </p>
          <p>
            <strong>v1.1 roadmap.</strong> CSS-variable theming (brand colour, font family,
            radii) and a dark-mode-by-default render path are tracked on the 90-day plan. When
            those land, the same URL will accept a{' '}
            <InlineCode>?theme=dark</InlineCode> hint and read CSS custom properties from the
            tenant's branding settings without a redeploy. Until then, your iframe is the
            theming boundary.
          </p>

          <Callout tone="info">
            A typed React component (<InlineCode>{'<VeloxCostDashboard />'}</InlineCode>) for
            apps that want to skip the iframe entirely is also on the v1.1 roadmap. The current
            embed is iframe-only by design — it gets you a working integration in five minutes
            without any client-side JavaScript.
          </Callout>
        </Prose>
      </DocsShell>
    </PublicLayout>
  )
}
