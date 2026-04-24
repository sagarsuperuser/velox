# Changelog

All notable changes to Velox are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

**Pre-1.0 versioning:** we're on `0.MINOR.PATCH`. MINOR bumps for new
features, PATCH for fixes. The public API is stabilising but not yet
frozen; breaking changes land on MINOR until `1.0.0`.

Two surfaces mirror this file:

- `web-v2/src/pages/Changelog.tsx` — customer-facing public changelog,
  curated rollups (not every bug fix).
- `docs/design-partner-readiness-plan.md` — internal planning log, not a
  release log.

## [Unreleased]

### Added

- **Phase 2 Addendum shipped — pre-invite design-partner readiness** (2026-04-24)
  - **Hosted invoice page** (T0-17) — Stripe-equivalent `hosted_invoice_url`.
    `invoices.public_token` + three public routes at `/v1/public/invoices/*`
    (view, checkout via Stripe Checkout, PDF). Mobile-first React page at
    `/invoice/:token` with tenant branding. Operator rotate-public-token
    endpoint. Dashboard "Copy Link" + "Rotate" actions on invoice detail.
  - **Branded HTML emails** (T0-16) — 6 customer-facing emails converted
    to multipart/alternative with tenant logo, brand color, support link,
    CTA to hosted invoice page. Operator emails (password reset, member
    invite, portal magic link) stay plain text.
  - **Webhook secret rotation grace period** (T0-19) — 72h dual-signing
    window via Stripe-style multi-v1 `Velox-Signature` header. Dashboard
    shows "Dual-signing until {time}" during the window.
  - **Subscription activity timeline** (T0-18) — `GET /v1/subscriptions/
    {id}/timeline` + SPA Activity panel. CS reps get a chronological feed
    of lifecycle events (create, activate, pause, resume, cancel, item
    changes) sourced from the audit log.
  - **SMTP bounce capture** (T0-20) — permanent-failure (5xx) SMTP errors
    flip `customers.email_status` to `bounced` and fire a
    `customer.email_bounced` webhook event. Red "Bounced" badge on
    customer detail page. Async NDR / provider-webhook handling stays in
    T1-8 and plugs into the same `MarkEmailBounced` seam.

### Changed

- **Manual test runbook** updated for the Phase 2 Addendum surfaces.
  Existing flows I6 (emails), W2 (webhook rotation), CU6 (brand color)
  expanded with branded-HTML / grace-period / email-brand checks. New
  flows: I10 hosted invoice page (token mint + public render + Stripe
  Checkout + state-gated variants + security audit), B12 subscription
  activity timeline, CU7 email bounce capture + badge.
- **API shape:** 5 customer-facing email interfaces (`invoice.EmailSender`,
  `dunning.EmailNotifier`, `payment.EmailReceipt`, `email.EmailDeliverer`)
  gained a trailing `publicToken` parameter. All callers updated
  atomically — breaking change for any out-of-tree email implementations.
- **Webhook Store interface:** `UpdateEndpointSecret` replaced by
  `RotateEndpointSecret(tenantID, id, newSecret, gracePeriod)`.
  Hard-replace semantics preserved via `gracePeriod=0`.
- **Customer JSON** now surfaces `email_status`, `email_last_bounced_at`,
  `email_bounce_reason` when populated.
- **Webhook endpoint JSON** now surfaces `secondary_secret_last4` and
  `secondary_secret_expires_at` during a rotation's grace window.

### Fixed

- **Hosted invoice Checkout metadata** — `velox_invoice_id` is now
  propagated to both the Checkout Session and the underlying
  PaymentIntent, so `payment_intent.succeeded` webhooks route hosted-
  invoice payments to the right invoice. Caught during T0-17.3 review.

### Migrations

- `0048_invoice_public_token` — adds `invoices.public_token` with a
  partial unique index. Existing finalized invoices stay NULL until
  rotated; drafts never get a token.
- `0049_webhook_secondary_secret` — adds
  `webhook_endpoints.secondary_secret_encrypted` +
  `secondary_secret_last4` + `secondary_secret_expires_at`.
- `0050_customer_email_status` — adds `customers.email_status` (NOT NULL,
  default `unknown`) + `email_last_bounced_at` + `email_bounce_reason`.

---

Historical entries (pre-Addendum) are summarised in
`web-v2/src/pages/Changelog.tsx`. When the next release is cut, the
contents of `[Unreleased]` above will move under a new
`## [0.X.0] - YYYY-MM-DD` heading here, and a matching entry will be
curated into the public changelog page.
