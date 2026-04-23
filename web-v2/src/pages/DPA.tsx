import { LegalLayout, H2, P } from '@/components/LegalLayout'

export default function DPAPage() {
  return (
    <LegalLayout title="Data Processing Addendum" updated="April 23, 2026">
      <P>
        This Data Processing Addendum ("DPA") supplements our Terms of Service when you process
        personal data that is subject to the EU General Data Protection Regulation (GDPR), the UK
        GDPR, the California Consumer Privacy Act (CCPA), or comparable data protection laws. When
        you use Velox to process such data, Velox acts as your data processor and you remain the
        data controller.
      </P>

      <H2>1. Roles</H2>
      <P>
        You (the customer) are the controller of personal data you submit to the Service. Velox is
        the processor and processes personal data only on your documented instructions (your API
        calls, dashboard actions, and configurations) and as required by applicable law.
      </P>

      <H2>2. Subject matter and duration</H2>
      <P>
        Subject matter: the billing operations you initiate. Duration: the term of your use of the
        Service, plus up to 30 days for deletion unless a longer period is required by law.
      </P>

      <H2>3. Categories of data and data subjects</H2>
      <P>
        Data subjects: your end customers and their authorized representatives. Categories of
        personal data: name, email address (encrypted at rest), billing address, tax ID, payment
        method metadata (last-4 and expiry only; full card data is held by Stripe, not Velox),
        usage events, and any metadata you attach.
      </P>

      <H2>4. Security measures</H2>
      <P>
        Velox implements the technical and organizational measures described on the{' '}
        <a href="/security" className="underline underline-offset-2 hover:text-foreground">
          Security
        </a>{' '}
        page. These are designed to protect the confidentiality, integrity, and availability of
        personal data.
      </P>

      <H2>5. Sub-processors</H2>
      <P>
        You authorize Velox to engage sub-processors for hosting, database, email, and payment
        orchestration. A current list is provided on request. Velox will give 30 days notice before
        adding or replacing a sub-processor, and you may object in writing; absent agreement, you
        may terminate for material breach.
      </P>

      <H2>6. International transfers</H2>
      <P>
        Where personal data is transferred outside the EEA, UK, or Switzerland, Velox relies on the
        EU Standard Contractual Clauses (Commission Decision (EU) 2021/914) and the UK IDTA, as
        applicable. A signed copy is available on request.
      </P>

      <H2>7. Data subject requests</H2>
      <P>
        Velox provides API endpoints for access (
        <code className="font-mono text-[13px] px-1 py-0.5 rounded bg-muted/60 border border-border">
          GET /customers/{'{id}'}/export
        </code>
        ) and deletion (
        <code className="font-mono text-[13px] px-1 py-0.5 rounded bg-muted/60 border border-border">
          POST /customers/{'{id}'}/delete-data
        </code>
        ) to help you fulfill data-subject requests. If you cannot self-serve, Velox will assist
        within 5 business days.
      </P>

      <H2>8. Incident notification</H2>
      <P>
        Velox will notify you without undue delay, and in any event within 72 hours, of any personal
        data breach affecting your tenant. Notifications will include available facts, likely
        consequences, and remediation steps taken or proposed.
      </P>

      <H2>9. Audit</H2>
      <P>
        Velox will make available information reasonably necessary to demonstrate compliance with
        this DPA. During the design-partner phase, this takes the form of written responses to
        security questionnaires and shared telemetry; formal third-party audit reports (e.g., SOC 2)
        are in progress.
      </P>

      <H2>10. Return or deletion</H2>
      <P>
        On termination, Velox will delete or return personal data to you, at your choice, within 30
        days, unless retention is required by law.
      </P>

      <H2>Signature</H2>
      <P>
        To countersign this DPA, email{' '}
        <a href="mailto:legal@velox.dev" className="underline underline-offset-2 hover:text-foreground">
          legal@velox.dev
        </a>{' '}
        with your organization's legal name and the name and title of the signatory. A PDF version
        will be returned for electronic signature.
      </P>
    </LegalLayout>
  )
}
