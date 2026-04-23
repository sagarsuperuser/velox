import { LegalLayout, H2, P } from '@/components/LegalLayout'

export default function TermsPage() {
  return (
    <LegalLayout title="Terms of Service" updated="April 23, 2026">
      <P>
        These Terms of Service ("Terms") govern your access to and use of Velox, a usage-based
        billing platform provided by the Velox maintainers ("Velox", "we", "us"). By creating an
        account or using the Service, you agree to these Terms on behalf of yourself or the
        organization you represent.
      </P>

      <H2>1. The service</H2>
      <P>
        Velox provides APIs and a dashboard for managing subscriptions, invoices, usage metering,
        coupons, credit notes, and related billing operations. Velox is not a payment processor; all
        card transactions are orchestrated through Stripe using credentials you provide.
      </P>

      <H2>2. Your account</H2>
      <P>
        You are responsible for maintaining the confidentiality of your API keys and dashboard
        credentials, and for all activity that occurs under your account. Notify us immediately at{' '}
        security@velox.dev of any unauthorized access.
      </P>

      <H2>3. Acceptable use</H2>
      <P>
        You will not use Velox to process transactions for illegal goods or services, to charge
        customers without their authorization, or in a manner that violates applicable law or
        Stripe's terms of service. You will not attempt to circumvent tenant isolation, reverse
        engineer the service, or perform security testing without prior written consent.
      </P>

      <H2>4. Data ownership</H2>
      <P>
        You retain all rights in the data you submit to Velox. We process your data solely to provide
        the Service. You can export your tenant data at any time via the API or the dashboard, and
        you can terminate your account to have your data deleted per our Privacy Policy.
      </P>

      <H2>5. Fees</H2>
      <P>
        Pricing for paid plans is described on our pricing page. During the design-partner phase,
        commercial terms may be individually negotiated. Fees are non-refundable except where
        required by law.
      </P>

      <H2>6. Service availability</H2>
      <P>
        We will use commercially reasonable efforts to keep the Service available, but we do not
        guarantee uninterrupted operation. See{' '}
        <a href="/status" className="underline underline-offset-2 hover:text-foreground">
          /status
        </a>{' '}
        for current state and historical uptime.
      </P>

      <H2>7. Termination</H2>
      <P>
        Either party may terminate this agreement at any time. Upon termination, your access to the
        Service will be disabled and your data will be deleted in accordance with our Privacy Policy
        unless you request export first.
      </P>

      <H2>8. Liability</H2>
      <P>
        To the maximum extent permitted by law, Velox's aggregate liability arising out of or
        relating to these Terms is limited to the greater of (a) fees paid by you in the twelve
        months preceding the claim, or (b) $1,000 USD.
      </P>

      <H2>9. Changes</H2>
      <P>
        We may update these Terms from time to time. Material changes will be announced by email and
        in the changelog at least 30 days before they take effect.
      </P>

      <H2>10. Contact</H2>
      <P>
        Questions about these Terms can be directed to{' '}
        <a href="mailto:legal@velox.dev" className="underline underline-offset-2 hover:text-foreground">
          legal@velox.dev
        </a>
        .
      </P>
    </LegalLayout>
  )
}
