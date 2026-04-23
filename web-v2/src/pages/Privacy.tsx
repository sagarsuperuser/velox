import { LegalLayout, H2, P } from '@/components/LegalLayout'

export default function PrivacyPage() {
  return (
    <LegalLayout title="Privacy Policy" updated="April 23, 2026">
      <P>
        This Privacy Policy explains how Velox handles personal data submitted to the Service, either
        by you as an operator or by the customers you bill.
      </P>

      <H2>Data we handle</H2>
      <P>
        As an operator, we store the minimum required to authenticate you and run the Service: email
        address, Argon2id-hashed password, session metadata (user-agent, IP at sign-in time), and
        team membership. As a data processor for your billing operations, we store the records you
        create (customers, subscriptions, invoices, coupons, credit notes, usage events, webhooks)
        and their metadata. Customer email addresses are encrypted at rest with AES-256-GCM.
      </P>

      <H2>How we use data</H2>
      <P>
        We use operator data to provide and secure the Service, prevent abuse, and communicate about
        your account. We use your tenant data only to execute billing operations you initiate
        (generating invoices, calling Stripe, sending webhooks). We do not sell data, and we do not
        use your tenant data to train models.
      </P>

      <H2>Data sharing</H2>
      <P>
        We share data with sub-processors strictly necessary to operate the Service: our hosting
        provider, our database provider, our email relay, and Stripe for payment orchestration. A
        current sub-processor list is available on request. We do not share data with third parties
        for their own marketing.
      </P>

      <H2>Retention and deletion</H2>
      <P>
        Your tenant data is retained for as long as your account is active. On account termination
        (operator-initiated or for cause), data is deleted within 30 days unless you request export
        first. For customer-level deletion requests (GDPR Article 17),{' '}
        <code className="font-mono text-[13px] px-1 py-0.5 rounded bg-muted/60 border border-border">
          POST /customers/{'{id}'}/delete-data
        </code>{' '}
        anonymizes PII and archives the customer record.
      </P>

      <H2>Export</H2>
      <P>
        You can export any individual customer's full record via{' '}
        <code className="font-mono text-[13px] px-1 py-0.5 rounded bg-muted/60 border border-border">
          GET /customers/{'{id}'}/export
        </code>
        . A full-tenant archive export endpoint is on the roadmap; in the interim, contact{' '}
        <a href="mailto:support@velox.dev" className="underline underline-offset-2 hover:text-foreground">
          support@velox.dev
        </a>{' '}
        and we will provide a JSON-lines archive within 5 business days.
      </P>

      <H2>Security</H2>
      <P>
        Details are available on the{' '}
        <a href="/security" className="underline underline-offset-2 hover:text-foreground">
          Security
        </a>{' '}
        page. Briefly: encryption at rest (AES-256-GCM) for sensitive fields, TLS 1.2+ in transit,
        Argon2id for passwords, PostgreSQL Row-Level Security for tenant isolation, and a full audit
        log for every mutation.
      </P>

      <H2>Your rights</H2>
      <P>
        Depending on your jurisdiction, you may have the right to access, correct, delete, or port
        personal data we hold about you. To exercise these rights, email{' '}
        <a href="mailto:privacy@velox.dev" className="underline underline-offset-2 hover:text-foreground">
          privacy@velox.dev
        </a>
        . We will respond within 30 days.
      </P>

      <H2>Changes</H2>
      <P>
        Material changes to this Policy will be announced by email and in the changelog at least 30
        days before they take effect.
      </P>

      <H2>Contact</H2>
      <P>
        For questions about this Policy or our data handling practices, email{' '}
        <a href="mailto:privacy@velox.dev" className="underline underline-offset-2 hover:text-foreground">
          privacy@velox.dev
        </a>
        .
      </P>
    </LegalLayout>
  )
}
