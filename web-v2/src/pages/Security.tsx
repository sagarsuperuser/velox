import { Mail, Shield, Lock, Key, Server, FileText, Database, type LucideIcon } from 'lucide-react'
import { PublicLayout, PublicPageHeader } from '@/components/PublicLayout'

const sections: {
  icon: LucideIcon
  title: string
  body: string
  items: { name: string; detail: string }[]
}[] = [
  {
    icon: Lock,
    title: 'Encryption',
    body: 'Sensitive data is encrypted at rest using AES-256-GCM, independent of the database-level TLS and disk encryption provided by our hosting platform.',
    items: [
      { name: 'Webhook signing secrets', detail: 'AES-256-GCM encrypted. Plaintext never persisted.' },
      { name: 'Stripe connection tokens', detail: 'AES-256-GCM encrypted per tenant.' },
      { name: 'Customer email addresses', detail: 'AES-256-GCM encrypted, with blind-index for magic-link lookup.' },
      { name: 'In transit', detail: 'TLS 1.2+ everywhere. HSTS preload enabled.' },
    ],
  },
  {
    icon: Shield,
    title: 'Tenant isolation',
    body: 'Every tenant-scoped row carries a tenant_id and is protected by PostgreSQL Row-Level Security. Application code obtains a session-scoped transaction that sets the current tenant; cross-tenant reads are impossible without explicit escalation.',
    items: [
      { name: 'RLS on every tenant table', detail: 'Not just application-level filtering — enforced by the database.' },
      { name: 'Session-scoped tenancy', detail: 'Tenant context is set per transaction via SET LOCAL, not per connection.' },
      { name: 'Multi-tenant test coverage', detail: 'Integration tests assert cross-tenant access raises database-level errors.' },
    ],
  },
  {
    icon: Key,
    title: 'Authentication',
    body: 'Velox uses API keys for programmatic access and email+password for dashboard login. Both are hashed at rest with algorithms appropriate to their threat model.',
    items: [
      { name: 'API keys', detail: 'SHA-256 with a 16-byte per-key salt. Constant-time comparison on validation.' },
      { name: 'Operator passwords', detail: 'Argon2id (OWASP-recommended parameters). Never logged or emailed.' },
      { name: 'Sessions', detail: 'HttpOnly, Secure (in production), SameSite=Lax cookies. Multi-device revocation on logout and password reset.' },
      { name: 'Password reset tokens', detail: 'Expire 1 hour after issuance. Single-use.' },
    ],
  },
  {
    icon: Server,
    title: 'HTTP security headers',
    body: 'The dashboard and API both set a conservative header baseline.',
    items: [
      { name: 'HSTS', detail: 'max-age=31536000; includeSubDomains' },
      { name: 'X-Frame-Options', detail: 'DENY — no embedding of Velox surfaces.' },
      { name: 'X-Content-Type-Options', detail: 'nosniff — strict MIME handling.' },
      { name: 'Referrer-Policy', detail: 'strict-origin-when-cross-origin' },
      { name: 'Cache-Control', detail: 'no-store on authenticated responses.' },
    ],
  },
  {
    icon: FileText,
    title: 'Audit &amp; observability',
    body: 'Every mutating operation is recorded in an append-only tenant-scoped audit log. Webhook deliveries are persisted with full request/response bodies and replayable from the dashboard.',
    items: [
      { name: 'Audit log', detail: 'Captures actor, action, resource, IP, request ID, metadata. Exportable as CSV.' },
      { name: 'Webhook delivery log', detail: 'Request/response bodies, latency, retry history, signature headers, replay button.' },
      { name: 'Request IDs', detail: 'Every API response includes X-Request-ID for end-to-end tracing.' },
    ],
  },
  {
    icon: Database,
    title: 'Backup & recovery',
    body: 'Financial data is protected by continuous WAL archiving and regular base backups. The restore pipeline is tested end-to-end on a fixed schedule, not just documented.',
    items: [
      { name: 'RPO', detail: '5 minutes — continuous WAL archiving bounds the maximum data loss window in any realistic failure.' },
      { name: 'RTO', detail: '1 hour — base-backup fetch plus WAL replay is expected to complete within this window for production-sized datasets.' },
      { name: 'Encryption', detail: 'Backups are encrypted at rest (libsodium via WAL-G) and transported over TLS to S3/GCS.' },
      { name: 'Drill cadence', detail: 'An automated backup → restore → validate drill runs monthly against a throwaway Postgres instance and asserts row-count parity on critical tables.' },
      { name: 'Retention', detail: 'Financial records kept 7 years. Operational logs pruned at 30–90 days. See the backup runbook for the full matrix.' },
    ],
  },
]

export default function SecurityPage() {
  return (
    <PublicLayout>
      <PublicPageHeader
        eyebrow="Platform"
        title="Security at Velox"
        description="Velox is built to handle real money, which means we take correctness and data protection seriously from day one. This page summarizes the primitives we rely on and the practices we follow. For the threat model behind each choice, or to coordinate a security review, reach out at security@velox.dev."
      />
      <div className="max-w-4xl mx-auto px-6 py-12 space-y-10">
        {sections.map((section) => (
          <section key={section.title}>
            <div className="flex items-center gap-3 mb-3">
              <div className="w-9 h-9 rounded-lg bg-primary/10 text-primary flex items-center justify-center">
                <section.icon size={18} />
              </div>
              <h2 className="text-xl font-semibold tracking-tight text-foreground">{section.title}</h2>
            </div>
            <p className="text-muted-foreground leading-relaxed mb-4 pl-12">{section.body}</p>
            <ul className="pl-12 space-y-2">
              {section.items.map((item) => (
                <li key={item.name} className="text-sm">
                  <span className="font-medium text-foreground">{item.name}</span>
                  <span className="text-muted-foreground"> — {item.detail}</span>
                </li>
              ))}
            </ul>
          </section>
        ))}

        <section className="border-t border-border pt-10">
          <h2 className="text-xl font-semibold tracking-tight text-foreground mb-3">Compliance</h2>
          <p className="text-muted-foreground leading-relaxed">
            Velox is a young product. We are working toward SOC 2 Type I readiness; in the interim, we
            are transparent about what is and is not in place. If you need a detailed security
            questionnaire (e.g., CAIQ, SIG), email{' '}
            <a className="underline underline-offset-2 hover:text-foreground" href="mailto:security@velox.dev">
              security@velox.dev
            </a>{' '}
            and we'll respond with current state.
          </p>
        </section>

        <section className="border-t border-border pt-10">
          <h2 className="text-xl font-semibold tracking-tight text-foreground mb-3">
            Responsible disclosure
          </h2>
          <p className="text-muted-foreground leading-relaxed">
            If you believe you've found a security issue in Velox, please email{' '}
            <a className="underline underline-offset-2 hover:text-foreground" href="mailto:security@velox.dev">
              security@velox.dev
            </a>
            . We commit to an initial response within one business day, and we will credit reporters
            (with permission) once a fix ships. Please do not publicly disclose the issue until we've
            had a chance to investigate and remediate.
          </p>
          <div className="mt-6 inline-flex items-center gap-2 text-sm text-foreground">
            <Mail size={14} />
            <a className="underline underline-offset-2" href="mailto:security@velox.dev">
              security@velox.dev
            </a>
          </div>
        </section>
      </div>
    </PublicLayout>
  )
}
