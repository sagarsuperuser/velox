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

- **Trial extension — Stripe parity** (2026-04-25) — operators can now
  push a trialing subscription's `trial_end_at` later via `POST
  /v1/subscriptions/{id}/extend-trial` with `{trial_end:<RFC3339>}`. The
  store atomic enforces `status='trialing'` (closing the race against
  the cycle-scan auto-flip), and the service guards against past
  timestamps and shrinks (use `end-trial` to shorten — `extend-trial`
  is extension-only by design). Fires `subscription.trial_extended`
  with `triggered_by:"operator"`. Dashboard `SubscriptionDetail`
  surfaces an "Extend trial" button next to "End trial now" on
  trialing subs; the dialog seeds with current + 7 days.

- **Trial state machine — Stripe parity** (2026-04-25) — subscriptions
  with `trial_days > 0` now enter a real `status='trialing'` state on
  `Create` (previously they went straight to `active` and the engine
  inferred trial-skip from `trial_end_at` alone). New status added to the
  `subscriptions.status` CHECK constraint and to `domain.SubscriptionStatus`.
  Billing engine runs a three-branch state machine on each cycle visit:
  (a) trialing AND `now < trial_end_at` → skip billing, advance the cycle;
  (b) trialing AND `now >= trial_end_at` → atomically flip to `active`,
  stamp `activated_at`, fire `subscription.trial_ended`
  (`triggered_by:"schedule"`), then bill normally; (c) any other status →
  bill normally. `GetDueBilling` now sweeps `IN ('active', 'trialing')` so
  the cycle scan actually visits trialing subs. New endpoint `POST
  /v1/subscriptions/{id}/end-trial` lets sales/ops end a trial early; it
  fires `subscription.trial_ended` with `triggered_by:"operator"` so
  analytics can split scheduled trial-ends from manual ones. Dashboard
  `SubscriptionDetail` shows an "End trial now" button on trialing subs.
  The atomic `'trialing' → 'active'` UPDATE-WHERE-status closes the race
  between scheduler auto-flip and operator early-end.

- **Pause collection — Stripe parity** (2026-04-25) — distinct from a hard
  pause: the cycle keeps running, but invoices generate as drafts and skip
  finalize/charge/dunning until resumed. New nullable composite
  `pause_collection = {behavior, resumes_at}` on `subscriptions`. Two new
  endpoints: `PUT /v1/subscriptions/{id}/pause-collection` accepts
  `{behavior:"keep_as_draft", resumes_at?:<RFC3339>}`; `DELETE
  /v1/subscriptions/{id}/pause-collection` clears the pause. v1 only
  supports `keep_as_draft` (the `mark_uncollectible` and `void` Stripe
  modes need an `uncollectible` invoice status that doesn't exist yet).
  Billing engine forces invoice status to draft, skips
  `credits.ApplyToInvoice` and auto-charge, and auto-resumes when
  `resumes_at` passes (cycle scan checks at cycle time, fires
  `subscription.collection_resumed` with `triggered_by:"schedule"` so
  analytics can distinguish from operator-triggered resume). Webhook
  events: `subscription.collection_paused` /
  `subscription.collection_resumed`. Dashboard `SubscriptionDetail` gets a
  blue "Collection paused" banner with one-click Resume and a Stripe-style
  choice dialog ("Pause subscription" hard freeze vs "Pause collection
  only") on the Pause action.

- **Scheduled subscription cancellation — Stripe parity** (2026-04-25) —
  `cancel_at_period_end` (soft, reversible) and `cancel_at` (timestamp
  schedule) on `subscriptions`. Two new endpoints: `POST /v1/subscriptions/
  {id}/schedule-cancel` accepts `{at_period_end:true}` xor
  `{cancel_at:<RFC3339>}`; `DELETE /v1/subscriptions/{id}/scheduled-cancel`
  clears any prior schedule. Billing engine fires the cancel atomically at
  the period boundary after the final invoice generates, mirrors test-clock
  time for `canceled_at`, and emits `subscription.cancel_scheduled` /
  `subscription.cancel_cleared` / `subscription.canceled` (with
  `triggered_by:"schedule"`). v1 only accepts `cancel_at` >= current period
  end; mid-period proration is a follow-up. Dashboard `SubscriptionDetail`
  gets a "Cancellation scheduled" banner with one-click Undo and a Stripe-
  style choice dialog ("at period end" vs "immediately") on the Cancel
  action.

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
  - **SMTP bounce capture** (T0-20, **pipeline only**) — schema,
    webhook event, and UI badge ready for bounce signal; synchronous
    SMTP 5xx detection catches a minority of real-world bounces because
    most MX providers emit bounces as async NDRs, not synchronous `RCPT
    TO` rejections. Full coverage ships with T1-8 SES / SendGrid /
    Postmark webhook handlers, which plug into the same
    `customer.MarkEmailBounced` seam. Deployments without
    `VELOX_EMAIL_BIDX_KEY` get graceful degradation — bounces are
    logged but `email_status` stays `unknown`.

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
- `0051_subscription_scheduled_cancel` — adds
  `subscriptions.cancel_at_period_end` (NOT NULL, default false) +
  `cancel_at` (nullable timestamptz, partial index for the cycle scan).

---

Historical entries (pre-Addendum) are summarised in
`web-v2/src/pages/Changelog.tsx`. When the next release is cut, the
contents of `[Unreleased]` above will move under a new
`## [0.X.0] - YYYY-MM-DD` heading here, and a matching entry will be
curated into the public changelog page.
