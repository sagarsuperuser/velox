# Design-Partner Readiness Plan

Living document. Iterate freely. Mirrors the style of `phase2-hardening-plan.md`.

**The bar:** A design partner (Series A SaaS CTO) runs real billing through Velox for 30–60 days. They invest engineering time because they trust Velox won't lose money, won't embarrass them in front of their customers, and will be responsive when something goes wrong. The test is *"does it feel like a real company stands behind this product?"* — not feature parity with Stripe.

**Status legend:** ⏳ TODO · 🚧 In progress · ✅ Done · ⏭️ Deferred · ❓ Verify

---

## Strengths (already shipped — do not rebuild)

Quick orientation for what's *already* design-partner grade so we don't over-plan.

- **Correct billing engine** — subs, metered usage, coupons v2 (customer + subscription + invoice scopes), credit notes, dunning, tax (manual/inclusive/Stripe Tax), proration, end-of-period plan change
- **Stripe integration** — PaymentIntent-only pattern (no 0.5% fee), encrypted creds at rest, webhook ingestion idempotent via `(tenant_id, stripe_event_id)` UNIQUE
- **Multi-tenant security** — RLS on every tenant table, API keys hashed (SHA-256 + salt), webhook secrets AES-256-GCM encrypted, customer email AES-256-GCM encrypted + blind-index
- **Operator auth** — email+password via Argon2id, cookie sessions with TTL + multi-device revocation, password reset with 1h token expiry (no WorkOS yet — not blocking)
- **Observability** — full audit log (actor, action, resource, IP, request_id, metadata) with RLS + CSV export; webhook delivery log with request/response bodies, latency, retries, **replay button**
- **API contract** — OpenAPI spec shipped, Idempotency-Key middleware Stripe-compatible (incl. 422 on body mismatch), rate limiting with `X-RateLimit-*` headers + `Retry-After`
- **Security headers** — HSTS, X-Frame-Options:DENY, X-Content-Type-Options, Referrer-Policy, Cache-Control:no-store
- **GDPR primitives** — `GET /customers/{id}/export` and `POST /customers/{id}/delete-data` endpoints already exist
- **Dashboard UX** — CMD+K palette, URL-state persistence, test/live toggle, per-field form errors, consistent empty states, audit log UI, onboarding checklist widget on Dashboard
- **Invoice PDF** — tenant-customizable header (company name, address, contact, tax ID), logo, footer, multi-page, tax breakdown, 40+ currency symbols

---

## Phase 1 — Manual testing pass (you · ~2 days)

Run before anyone else touches the product. Human sanity check across every flow in both test and live modes.

### Coverage checklist

**Core billing flows**
- [ ] Fresh tenant sign-up → land on dashboard
- [ ] Stripe connect (test) → status reflects correctly + disconnect/reconnect
- [ ] Create plan (flat / metered / graduated tiers / tax code)
- [ ] Create customer → edit billing profile → archive → unarchive → GDPR export → GDPR delete
- [ ] Create subscription → activate → pause → resume → cancel (immediate + at-period-end)
- [ ] Change plan on active subscription (immediate + scheduled)
- [ ] Add/remove/change items + quantities with proration
- [ ] Issue draft invoice → apply coupon → finalize → collect → Stripe PI confirm
- [ ] Credit note against paid invoice → partial refund via Stripe
- [ ] Fail a payment (card `4000 0000 0000 0002`) → dunning runs → recover with valid card
- [ ] Assign customer-scoped coupon → next invoice picks it up
- [ ] Send invoice email → inbox check (Gmail + Outlook)
- [ ] Download invoice PDF → brand / totals / tax correct
- [x] Customer portal: login → invoices + PDF + cancel + branding (T0-8 ✅ 2026-04-23)
- [ ] Create webhook endpoint → trigger → verify signature on partner side → replay → rotate secret
- [ ] API key (platform / secret / publishable) → request → revoke → 401

**Cross-cutting**
- [ ] Every list page: filter, sort, search, paginate, export → all respect intended scope
- [ ] Every empty state renders correctly with CTA
- [ ] Deliberate 400/404/409/422/500 — error UX is actionable
- [ ] Test-mode isolation airtight (no live data leak to test view)
- [ ] Audit log captures every mutation
- [ ] CMD+K finds reasonable results
- [ ] Dark mode on every page
- [ ] Idempotency-Key replay works (send same POST twice → 200 + `Idempotent-Replayed: true`)

**Deliverable:** Track findings in `docs/manual-testing-pass-$(date).md` categorized **Blocker** (fix before invite), **Ugly** (fix within Tier 1), **Polish** (Tier 2/3). Fix blockers before proceeding to Phase 3.

---

## Phase 2 — Tier 0: pre-invite essentials (~5–7 days)

Must exist before the first partner receives credentials. These are the "signs someone's minding the shop."

### Trust signals & content pages

#### [T0-1] Public marketing + docs site (3 pages minimum) — L — ⏳
Serve at `velox.dev`. Minimum: `/product`, `/docs`, `/pricing` (or "open source" if not monetized yet). Clean, modern (Next.js or similar). **Not built yet.**
- Why: Partner does a 60-second vibe-check on your homepage before deciding to even try.

#### [T0-2] `/docs` — API reference + quickstart guides — M — ✅
Render OpenAPI via **Scalar** or **Redoc** (picks up `docs/openapi.yaml` automatically). Plus 3 hand-written guides:
1. Quickstart: subscribe a customer in 5 API calls
2. Webhooks: handle signed events
3. Idempotency + rate limits + error handling
- Why: Dev-first billing tools stand or fall on docs quality.

#### [T0-3] `/security` page — S — ✅
Public page listing: encryption at rest (AES-256-GCM on secrets + customer email), RLS isolation, API key hashing, webhook signing (HMAC-SHA256), session security (Argon2id + httpOnly cookies + multi-device revocation), security headers (HSTS, X-Frame-Options, etc.), responsible-disclosure email. Mention SOC2 roadmap.
- Why: Procurement will ask. Pre-empt the questionnaire.

#### [T0-4] `/status` page — S — ✅ (placeholder; hosted service TBD)
Hosted (Instatus / BetterStack / Statuspage) or minimal self-hosted. Start with components: API, Dashboard, Webhooks, Stripe Integration. 30 days of "no incidents" builds trust faster than any feature.

#### [T0-5] `/changelog` — S — ✅
One entry per release. Backfill first 3 entries from recent phase2 work (outbox, coupons v2, tax). Partners watch this to know the product is alive.

#### [T0-6] Legal: Terms / Privacy / DPA — S — ✅ (pre-counsel drafts)
Boilerplate from iubenda/Termly initially; refine with counsel for revenue phase. Link from dashboard footer.

### In-product essentials

#### [T0-7] Request ID surfaced in all error responses + UI — S — ✅
Already captured in audit_log via `X-Request-ID`. Needs:
- Included in error response body envelope (`{error: {message, code, request_id}}`).
- Rendered in every toast/form-error/page-error with copy button: "Error: … · `req_abc123` [copy]".
- Why: Every support ticket starts with this ID.
- **Shipped 2026-04-23:** `showApiError()` helper in `web-v2/src/lib/formErrors.ts` emits a `toast.error()` with `description: "Request ID: req_…"` and a "Copy ID" action. `apiRequest` in `web-v2/src/lib/api.ts` also falls back to the `Velox-Request-Id` response header when the envelope is missing. Rolled out across 16 pages/components covering every TanStack mutation + setup-session + disconnect flows.

#### [T0-8] Customer portal completion — M → invite blocker — ✅
**Original gap (audit finding):** portal only supported payment method management. Missing: invoice list, PDF download, cancel subscription.
- **Shipped 2026-04-23:** New `internal/portalapi` package introduces one-way-coupled narrow interfaces (`InvoiceService`, `SubscriptionService`, `CustomerGetter`, `SettingsGetter`, `CreditNoteLister`) and five routes mounted under `/v1/me`: `GET /invoices`, `GET /invoices/{id}/pdf`, `GET /subscriptions`, `POST /subscriptions/{id}/cancel`, `GET /branding`. Every handler pulls identity from `customerportal.TenantID(ctx)` + `customerportal.CustomerID(ctx)` — body never supplies tenant/customer. Cross-customer URL guessing returns 404 (not 403, to avoid confirming existence); drafts are filtered from list + PDF. Customer-initiated cancels fire `subscription.canceled` with `payload.canceled_by = "customer"` so partners can distinguish portal vs operator cancels. Branding exposes only `company_name`, `logo_url`, `support_url` (no tax IDs, no invoice prefixes).
- **UI (`CustomerPortal.tsx` rewrite):** Header shows partner logo (with fallback) + company name + optional support link. Three tabs — Invoices, Subscriptions, Payment Methods. Invoices tab: list with payment-status badge + PDF download (fetched as blob because the endpoint is bearer-auth protected — can't rely on `<a href>`). Subscriptions tab: list with `TypedConfirmDialog` gating cancel with `CANCEL` word. Payment Methods tab: preserves existing add/set-default/remove flows.
- **Test coverage:** 9 tests in `internal/portalapi/handler_test.go` via in-memory fakes: cross-customer 404s, draft filtering, cancel event `canceled_by` payload, branding safe-projection, 401 on missing identity. `WithTestIdentity` test helper added to `customerportal/middleware.go`.
- **Explicit non-goals (v1):** no cancel-policy gate (partners who want to block cancel simply don't mint portal sessions); no audit log for portal actions (webhook event is sufficient; `auth.KeyID(ctx)` would resolve empty and write misleading `actor_type="system"`).

#### [T0-9] Destructive-action typed confirmations — S — ✅
Replace simple AlertDialog click on: Void Invoice, Cancel Subscription, Revoke API Key, Archive Customer, Delete Webhook Endpoint, Delete Tenant. Require typed text (e.g., "type VOID to confirm") for truly destructive ops. Keep simple confirm for reversible actions.
- **Shipped 2026-04-23:** `TypedConfirmDialog` component at `web-v2/src/components/TypedConfirmDialog.tsx` wired into Void Invoice (`VOID`), Void Credit Note (`VOID`), Cancel Subscription (`CANCEL`), Delete Webhook Endpoint (`DELETE`). Match is case-insensitive (caps-lock tolerant). Reversible flows (pause subscription, archive coupon, revoke invitation, revoke API key) intentionally keep the plain AlertDialog.

#### [T0-10] In-app support channel — S — ✅ (mailto); Slack Connect out-of-scope
- **Shipped 2026-04-23:** Sidebar "Report an issue" (in the account menu) now builds the mailto body at click-time with `tenant_id`, current URL, user agent, and the most recent `request_id`. Trace handle is tracked by a new `lib/lastRequestId.ts` module; `apiRequest` writes every `Velox-Request-Id` header it sees (including success responses), so "report" covers "something looked wrong even though the UI said OK" scenarios, not only error toasts. Login page also gained a `Contact support` link underneath the sign-in card for users who can't authenticate — URL + user agent only (no tenant_id available pre-login).
- **Out of scope (ops, not code):** "Dedicated Slack Connect channel per design partner" is an onboarding-checklist action for the founder, not a dashboard change. Tracked separately; does not gate the readiness milestone.

#### [T0-11] Onboarding checklist polish — S — ✅
**Shipped 2026-04-23:** `GetStarted` in `Dashboard.tsx` extended from 4 steps to the 6 canonical ones, each tracked automatically from server state:
1. **Connect Stripe** — `api.listStripeCredentials().data.length > 0`. CTA → `/settings?tab=payments`.
2. **Create your first plan** — `api.listPlans().data.length > 0`. CTA → `/pricing`.
3. **Add your first customer** — `overview.active_customers > 0`. CTA → `/customers`.
4. **Create a subscription** — `overview.active_subscriptions > 0`. CTA → `/subscriptions`.
5. **Set up a webhook endpoint** — `api.listWebhookEndpoints().data.length > 0`. CTA → `/webhooks`.
6. **Complete your company profile** — `settings.company_name && settings.company_email` (replaces the plan's "Verify email SMTP" bullet; Velox has no per-tenant SMTP config since email is platform-level, but company profile gates professional invoices/credit-notes/emails, which is the underlying intent).

Dismiss is persisted in `localStorage` under `velox:getstarted-dismissed:${tenantID}` — scoped per-tenant so multi-tenant operators don't mute one tenant by dismissing in another. Checklist self-hides at 100% complete. Each gating query runs only while the checklist is visible (`enabled: !getStartedDismissed`), so we don't re-hit these endpoints on every dashboard visit after dismissal. "Skip to API-first flow" link added beneath the list, routing to `/docs/quickstart`.

#### [T0-12] Logo + brand color upload — S — ✅
Shipped 2026-04-23. Chose **(b) URL-only** guidance over S3/R2 upload per "don't over-engineer" — no new infra, partners paste a public HTTPS logo URL (Cloudinary / S3 public object / CDN). Help text now references those example hosts.
- Brand accent color: new `brand_color` column on `tenant_settings` (migration 0046). Strict `^#[0-9a-f]{6}$` validation server + client; lowercased on save.
- Settings UI: native `<input type="color">` + hex text input + Clear button, alongside the existing live logo thumbnail.
- Applied to invoice PDF: company name tinted + 2px accent bar under header. Byte-identical output for tenants with empty `brand_color` (no accent bar drawn).
- **Deferred:** email brand color — `internal/email/sender.go` sends plain-text bodies only. HTML email templating is a separate scope item, not in T0-12.

### Operational prerequisites

#### [T0-13] Monitoring + alerts on your side — M — ✅ (rules shipped; 3 instrumentation gaps tracked)
- **Shipped 2026-04-23:** Prometheus alert rules at `ops/alerts/*.yaml`, one file per subsystem (api, billing, payments, webhooks, audit, scheduler) + `ops/alerts/README.md` documenting import into self-hosted Prometheus, Grafana Cloud/Mimir, and the Datadog porting note. Every rule carries a `severity:` label (`page` / `ticket` / `info`) for Alertmanager routing and a `runbook:` annotation deep-linking the corresponding playbook in `docs/ops/runbook.md`.
- **Coverage vs plan:**
  - ✅ 5xx rate > 0.5% over 5m → `VeloxAPIErrorBudgetBurn` (page)
  - ✅ Webhook delivery failure rate > 10% over 15m → `VeloxWebhookFailureRate` (ticket)
  - ✅ PaymentIntent failure rate > 5% over 15m → `VeloxPaymentIntentFailureRate` (ticket, new)
  - ✅ Scheduler heartbeat missing > 2m → `VeloxSchedulerStale` (page, via `blackbox_exporter` probe of `/health/ready` — documented in README)
  - ⏳ DB pool saturation — workaround via `postgres_exporter`'s `pg_stat_database_numbackends` until `sql.DBStats` is instrumented as `velox_db_pool_*` gauges
  - ⏳ Outbox backlog > N for > 5m — workaround via `postgres_exporter` custom query on `webhook_outbox.status='pending'` until `velox_webhook_outbox_backlog` gauge is instrumented
- **Not bundled into this ship:** (a) `velox_scheduler_last_run_timestamp_seconds` gauge, (b) `velox_db_pool_*` gauges from `*sql.DB.Stats()`, (c) `velox_webhook_outbox_backlog` periodic gauge. The rationale: adding these requires touching handler/worker code paths during the user's manual testing pass (Phase 1), and the blackbox/postgres_exporter workarounds are good enough to catch real outages today. Each gap is called out in `ops/alerts/README.md` §"Instrumentation gaps" so the next engineer finds them without spelunking.
- **Validation:** every YAML parses (`python3 -c "import yaml; yaml.safe_load(...)"`); `promtool check rules` is the final gate before merging into a production Prometheus instance.
- Why: **Never let the partner notice an outage before you do.**

#### [T0-14] Backup + restore drill — S — ✅
- **Shipped 2026-04-23:** `scripts/restore-drill.sh` runs the full `backup.sh` → ephemeral-Postgres → `restore.sh` → row-count-diff loop as a single command, suitable for cron/CI. History is appended to `~/.velox/drill.log` so backup/restore duration trend-over-time is observable; this is what catches "the drill is now 10× slower than last month" before it bites RTO during a real incident. Explicitly avoids S3 uploads and never touches prod backup rotation.
- `docs/ops/backup-recovery.md` §4.4 documents the script, its exit-code contract, and a sample cron scheduled against a read-replica (not the primary) so drill load doesn't touch the write path.
- `/security` now carries a **Backup & recovery** card with the RPO (5 min), RTO (1 hour), drill cadence, encryption posture, and retention summary — exactly the three questions a procurement reviewer asks about backups.

#### [T0-15] Incident runbook — S — ✅
Extended `docs/ops/runbook.md` (the pre-existing metrics + alert catalog already lived here; the plan's `docs/runbooks/incidents.md` path never existed — co-locating under `ops/` keeps detection and response in one file).
- **Shipped 2026-04-23:** Added explicit **severity definitions** (SEV-1/SEV-2/SEV-3 with response times). Added 4 missing playbooks: `VeloxPaymentIntentFailureRate` (15m/5% window, distinct from the 30m/10% breaker-adjacent alert), `VeloxSchedulerStale` (alerted via `/health/ready` blackbox probe because the heartbeat isn't a Prom gauge yet), `VeloxDBPoolSaturated` (alerted via `postgres_exporter` until `sql.DBStats` is wired), `VeloxOutboxBacklog` (alerted via SQL count until a gauge lands). Added **communication templates** for status page (investigating/identified/resolved), partner notification (Slack Connect + email), and internal `#incidents`. Added **rollback procedures**: app rollback (kubectl/helm), migration rollback (forward-only with compensating-migration pattern), feature-flag rollback. Added a **post-mortem template** including timeline, root cause, why-not-caught-earlier, and specific-actionable-prevention sections. New alerts for `VeloxPaymentIntentFailureRate` and `VeloxSchedulerStale` added to the alert catalog alongside existing rules.
- **Instrumentation gaps flagged** (not blocking T0-15, tracked separately): (a) scheduler heartbeat as `velox_scheduler_last_run_timestamp_seconds` gauge, (b) `velox_db_pool_*` gauges from `*sql.DB.Stats()`, (c) `velox_webhook_outbox_backlog` gauge. Each playbook includes an "Alerting note (current gap)" section pointing to the blackbox/postgres_exporter workaround until instrumentation lands.

---

## Phase 2 Addendum — Second-audit pre-invite blockers (~8 days)

Added 2026-04-24 after a re-audit against a stricter pre-invite bar: *"nothing embarrasses the partner in front of their customers, nothing loses money, nothing breaks silently."* Five items were pulled forward from T1 or added where no coverage existed.

#### [T0-16] HTML + branded emails — M — ✅
**Current gap:** `internal/email/sender.go` sends plain-text bodies only (invoice-ready, payment receipt, dunning warning, dunning escalation). Brand color + logo ship in the PDF attachment but are invisible in the email body itself. Phrasing is hardcoded "Velox Billing" / "Dear %s".
- Minimum viable: HTML bodies (MJML or `html/template`) with tenant logo, `brand_color` accent, tenant `from_name`, support link, primary CTA button. Multipart plain-text fallback preserved for deliverability.
- Primary CTA links to hosted invoice page (T0-17) for invoice/receipt emails; portal for account emails.
- Why pre-invite (vs T1-2): partner's customers see the email first. Plain-text "Velox Billing" from their account is a first-impression loss that doesn't recover by shipping HTML in Week 2.
- Subsumes and replaces T1-2.
- **Shipped 2026-04-24** (`dc7396e`): new `internal/email/templates.go` holds a shared layout with inline styles (Outlook-safe table layout, optional brand-color accent bar, tenant logo + company name header, support link, "Powered by Velox Billing" footer). Six customer-facing emails converted to multipart/alternative (text + HTML) or multipart/mixed for invoice with PDF attachment: invoice-ready, payment receipt, dunning warning, dunning escalation, payment failed, payment update request. CTAs point at the hosted invoice page (T0-17) when `HOSTED_INVOICE_BASE_URL` is set and the invoice carries a `public_token` — graceful no-CTA fallback otherwise. Signatures gained a trailing `publicToken` param; 4 consumer interfaces (`invoice.EmailSender`, `dunning.EmailNotifier`, `payment.EmailReceipt`, `email.EmailDeliverer`) and call sites updated atomically. Operator/security-token emails (password reset, member invite, portal magic link) deliberately stay plain text. `outboxMessage` gained `PublicToken` (jsonb payload; no schema migration). Wired via `emailSender.SetSettingsGetter(settingsStore)` in `router.go`. Tests: `TestRenderInvoiceHTML` covers layout + branding + safe-projection (no raw `<script>` when customer name is hostile); `TestHostedInvoiceURL` covers URL assembly edge cases.

#### [T0-17] Hosted invoice page (public tokenized URL) — M — ✅
**Current gap:** `/v1/public/payment-updates/{token}` exists but is a payment-method-update surface, not a Stripe-style `hosted_invoice_url`. Invoices are otherwise reachable only via `/v1/invoices/{id}` (API-key) or `/v1/me/invoices/{id}/pdf` (portal-login-gated). No public URL an end customer can click from an email to pay.
- Minimum viable: `invoices.public_token` column (nullable, generated on finalize, rotatable). Public route `GET /invoice/{token}` — unauthenticated, rate-limited. Renders: tenant branding, line items, totals, tax breakdown, Pay button (Stripe Checkout), PDF download. Respects paid / voided / expired states.
- Email CTA from T0-16 points here.
- Why pre-invite: for SaaS partners whose customers pay from email links (the common case), there is no paying surface without this.
- Defer-path: if the partner's flow is AR-rep-managed (bank transfer + manual reconciliation), this can ship Week 1. Confirm flow at partner intake.
- **Shipped 2026-04-24** (7 commits, `a656b18` → `1535e64`, design anchored to Stripe's `hosted_invoice_url`): migration 0048 adds `invoices.public_token` (TEXT, partial UNIQUE index). `GeneratePublicToken()` produces `vlx_pinv_` + 32-byte hex (256 bits entropy). `Service.Finalize` mints the token atomically after `UpdateStatus` succeeds — non-fatal on persist failure, recoverable via the rotate endpoint. New `internal/hostedinvoice/` package mirrors the `portalapi` narrow-interface-coordinator pattern with three routes mounted at `/v1/public/invoices`: `GET /{token}` (JSON invoice + branding + `pay_enabled`), `POST /{token}/checkout` (Stripe Checkout Session in payment mode — `automatic_tax` off because Velox owns tax; metadata duplicated onto `PaymentIntentData` so the existing `payment_intent.succeeded` webhook routes the charge to the invoice), `GET /{token}/pdf` (reuses `invoice.RenderPDF`). Cross-tenant lookup via `TxBypass` on the token — 256-bit entropy + UNIQUE index makes probing infeasible. Draft invoices never leak (belt-and-suspenders 404 in `resolveInvoice`). Safe projection audited in test: no `tenant_id`, `subscription_id`, `tax_id`, `stripe_payment_intent_id` surfaces in the JSON. Dedicated rate-limit bucket `hostedInvoiceRL` at 60/min per IP (vs global 100/min) because payment surfaces deserve tighter limits — industry precedent (Stripe, Paddle). Operator `POST /v1/invoices/{id}/rotate-public-token` endpoint with `AuditActionRotate` + plaintext-token-never-logged. Mobile-first React page at `/invoice/:token` (web-v2/src/pages/HostedInvoice.tsx): tenant logo + company name + optional brand-color accent bar, status banners (paid / voided / Processing-after-Stripe-return), bill-to/from columns, line-items table with tabular numerals, totals with optional discount / tax / reverse-charge note, primary Pay button (tenant brand color via inline style), Download PDF secondary button. Accessibility: role=status on banners, sr-only headings, aria-hidden decorative icons. Dashboard `InvoiceDetail` gains "Copy Link" + "Rotate" buttons (Rotate behind `TypedConfirmDialog` with `ROTATE` keyword). 11 handler tests via in-memory fakes + service-level rotate tests.

#### [T0-18] Activity timeline on Subscription detail — S — ✅
**Current gap:** Invoice timeline exists (`internal/invoice/handler.go::paymentTimeline()`). Subscription detail has none.
- Minimum viable: refactor the invoice aggregator into a reusable `internal/timeline/` builder. Scope to `subscription_id`. Sources: `audit_log` + webhook deliveries for subscription events + plan changes + dunning runs attached via invoice.
- Why pre-invite (vs T1-1): subscription-change tickets dominate partner CS volume ("why was my plan changed," "why was I cancelled"). Invoice-only timeline doesn't answer these.
- Customer timeline (the other half of T1-1) remains in T1 — lower support pressure.
- **Shipped 2026-04-24** (2 commits, `6540372` + `95816e7`): scope tightened on implementation — the invoice timeline and subscription timeline have different source sets (Stripe-webhook-driven vs audit-log-driven), so extracting a generic `internal/timeline/` aggregator would have added complexity without payoff. Went with a subscription-specific handler that consumes the audit log directly. `GET /v1/subscriptions/{id}/timeline` on the operator surface — resolves the sub first (404 on miss) to avoid silent-empty responses, then `h.auditLogger.Query(ResourceType="subscription", ResourceID=id)`. `describeSubscriptionAction` maps action + metadata to human sentence + status tag (info / succeeded / warning / canceled), including `canceled_by="customer"` pass-through for portal-initiated cancels. Event wire shape (`timelineEvent`) mirrors the invoice payment-timeline's so one React component renders both. SPA panel on `SubscriptionDetail.tsx` reads `getSubscriptionTimeline`, renders an "Activity" card with the same visual language as the invoice panel (colored dot per status, timestamp right-aligned, "by {actor_name}" attribution underneath when present). Dunning aggregation + webhook deliveries deliberately stay on the invoice timeline where they belong.

#### [T0-19] Webhook secret rotation grace period — S — ✅
**Current gap:** `internal/webhook/service.go::RotateSecret()` immediately replaces the old secret. No `secondary_secret` column.
- Minimum viable: add `secondary_secret` + `secondary_secret_expires_at` columns on `webhook_endpoints`. Dispatcher verifies against both for 72h post-rotation; expiry path drops secondary.
- UI: rotate shows new secret once with "old secret valid until {now+72h}" banner.
- Why pre-invite (vs T1-4): if the partner rotates in production (expected inside 30 days), immediate invalidation is a production incident.
- Subsumes and replaces T1-4.
- **Shipped 2026-04-24** (2 commits, `099a515` + `1bbf169`, design anchored to Stripe's multi-v1 `Stripe-Signature` format): migration 0049 adds `secondary_secret_encrypted` + `secondary_secret_last4` + `secondary_secret_expires_at` to `webhook_endpoints`. `Store.UpdateEndpointSecret` replaced by `Store.RotateEndpointSecret(tenantID, id, newSecret, gracePeriod)` — single transaction with `FOR UPDATE` reads the current secret, writes it to the secondary slot with 72h expiry, installs the new primary. `gracePeriod=0` gives a hard-replace escape hatch. `Service.SecretRotationGracePeriod = 72 * time.Hour`. `buildSignatureHeader()` emits `Velox-Signature: t=<ts>,v1=<sigPrimary>[,v1=<sigSecondary>]` — exactly Stripe's multi-signature format so partners whose verifier passes on "any v1= matches" deploy the new key at their own pace. Secondary skipped when `SecondarySecretExpiresAt` is nil or past (nil treated as expired — defensive against migration drift). `Service.RotateSecret` returns `{secret, secondary_valid_until}` so the handler response + dashboard can show the grace window. `TestBuildSignatureHeader` covers single-secret / fresh-rotation / expired-secondary / nil-expiry cases; `TestRotateSecret_GracePeriod` end-to-end asserts two v1= entries during the window and one after forced expiry. SPA: post-rotation dialog shows a green "Previous secret valid until {timestamp}" card; endpoints table shows a subtle "Dual-signing until {time}" hint under the URL when a rotation is active.

#### [T0-20] SMTP bounce/complaint handling (minimum viable) — S — ✅
**Current gap:** no bounce capture. Silent delivery failures mean partner's customers never receive invoices → collections go sideways → partner blames Velox.
- Minimum viable for pre-invite: capture SMTP-level bounces (or provider webhook if SES/Sendgrid), mark `customers.email_status = bounced`, surface as badge on customer detail, emit `customer.email_bounced` webhook event.
- Full handling (auto-suppression, retry decay, complaint-vs-bounce differentiation) remains T1-8.
- Why pre-invite: invisible delivery failures erode partner trust in the first week. Even visibility-only is a big upgrade over silent.
- **Shipped 2026-04-24** (`6421340`): migration 0050 adds `customers.email_status` (NOT NULL, default 'unknown', CHECK in unknown/ok/bounced/complained) + `email_last_bounced_at` + `email_bounce_reason`. Pipeline: SMTP send → `net/smtp` returns 5xx → `Sender.isPermanentSMTPBounce` → `BounceReporter.ReportBounce(tenantID, email, reason)` → blind-index lookup → `customer.MarkEmailBounced` → UPDATE → `customer.email_bounced` webhook event (new `domain.EventCustomerEmailBounced` constant). `isPermanentSMTPBounce` is a heuristic 5yz parser anchored on digit boundaries (avoids false positives like zip codes or wrapped transient errors); 9-case test covers classic bounces + 4xx transients + look-alikes. `bounceReporterAdapter` in `internal/api/adapters.go` bridges email → customer via the blind index; wired inside the existing `VELOX_EMAIL_BIDX_KEY` branch so deployments without the key get graceful degradation (bounces logged, badges stay 'unknown'). Customer `Get` + `GetByExternalID` scans widened to return the new fields; `List` / `Update` deliberately skipped (list views don't render the badge). SPA: `CustomerDetail` shows a red `Bounced` badge next to the email and a `Bounced · {date}` badge in the Details section with the bounce reason surfaced on hover. Async NDR parsing, SES/SendGrid provider webhooks, auto-suppression, and complaint-vs-bounce differentiation stay in T1-8 and all plug into the same `MarkEmailBounced` seam.

---

## Phase 3 — Invitation readiness gate

Before sending the invite email, confirm all of:

**Shipped:**
- [ ] All Phase 1 testing-pass **Blocker** items fixed
- [ ] All T0-1 to T0-15 shipped or ⏭️ consciously deferred
- [ ] All T0-16 to T0-20 (second-audit additions) shipped or ⏭️ consciously deferred

**Verified at runtime (shipped ≠ working):**
- [ ] Phase 1 manual testing checklist actually executed end-to-end (not just listed). Blockers fixed.
- [ ] OpenAPI spec at `web-v2/public/openapi.yaml` covers every exposed endpoint (second audit spot-checked only the Customers section — confirm full coverage).
- [ ] T0-13 alerts firing in a real Prometheus with on-call routing, 24h clean (stricter than "alerting in-flight for >24h").
- [ ] T0-14 backup drill scheduled as cron AND completed one full round-trip; `~/.velox/drill.log` shows entry.
- [ ] End-to-end Stripe connect → PaymentIntent → webhook ingest → refund tested in a live Stripe test environment.
- [ ] 24h of synthetic production-like traffic through a staging tenant with no anomalies.
- [ ] T0-15 runbook reviewed by a second pair of eyes (even another AI).

**Partner operations:**
- [ ] You have: named partner contact, Slack Connect channel, mutual NDA (if applicable), clear scope of pilot (what they'll use, for how long, success criteria).

---

## Phase 4 — Tier 1: operational hygiene during first 2 weeks (~1 week)

Things a partner will hit in their first week of real use.

#### [T1-1] Activity timeline on Customer + Subscription detail pages — M — ⏳
*Subscription half pulled to T0-18 pre-invite (second audit). Customer timeline remains here.*

Unified chronological feed per resource combining: audit_log entries + webhook deliveries + payment attempts + plan changes. Already exists on Invoice (payment timeline). Extend.
- Why: When a partner's CS rep gets "my subscription got cancelled unexpectedly," this is the first page they open.

#### [T1-2] Email brandability — M — ⏳ → superseded by T0-16
*Pulled to T0-16 pre-invite (second audit). Plain-text "Velox Billing" emails from the partner's account are a first-impression loss that doesn't recover post-invite.*

**Current gap (audit finding):** Email templates hardcoded with "Velox Billing" branding. Not partner-configurable.
- Per-tenant settings: `email_from_name`, `email_reply_to`, `email_footer_text`, `email_accent_color`.
- Templates use tenant logo + accent color.
- Keep default fallback to "Velox" if unset (eases onboarding).
- Why: Partners will not let emails from their customers be branded "Velox."

#### [T1-3] Rate-limit documentation — XS — ⏳
Already in response headers. Just publish the limits + semantics in `/docs/rate-limits`. Note fail-open posture (or switch to fail-closed for production — decide + document).

#### [T1-4] Webhook secret rotation with grace period — S — ⏳ → superseded by T0-19
*Pulled to T0-19 pre-invite (second audit). Immediate invalidation is a production incident when the partner rotates inside the 30-day pilot.*

**Current gap (audit finding):** rotate → old secret immediately invalidated. Breaks partner deploys.
- Add `secondary_secret` column; both verify for 72h after rotation; after expiry, secondary is dropped.
- UI flow: Rotate → shows new secret once → "Old secret valid until {now+72h}".
- Why: Stripe-standard. Without it, rotation is a production risk.

#### [T1-5] Changelog discipline — ongoing — ⏳
One entry per notable release. Automate via GitHub Actions from PR labels (`user-facing`), or write manually on ship. Aim for weekly cadence during partner engagement.

#### [T1-6] Partner feedback intake — ongoing — ⏳
Every issue raised by partner lands in Linear/GitHub issues same-day, tagged `design-partner`. Weekly sync (30 min) to review.

#### [T1-7] Content-Security-Policy header — S — ⏳
**Current gap (audit finding):** CSP not set. Other security headers are present.
- Add CSP matching Stripe.js iframe requirements + dashboard self-hosted assets.
- Test in report-only mode first.

#### [T1-8] Auth-email bounce/complaint handling — S — ⏳
*Minimum-viable pulled to T0-20 pre-invite (second audit). Full handling — auto-suppression, retry decay, complaint-vs-bounce differentiation — remains here for Week 2.*

**Current gap (audit finding):** No bounce handling on SMTP. A partner with bad customer email addresses will have silent delivery failures.
- Minimum: listen for SMTP bounce responses, mark `customers.email_status = bounced`, surface in customer detail.
- If using SES/Sendgrid: wire their webhook for bounces + complaints.

#### [T1-9] Email deliverability setup guide — S — ⏳
`/docs/email-setup` page explaining DKIM/SPF/DMARC for partners using custom domains. Even if Velox sends from a shared domain today, partners need to know how to graduate to custom-domain sending.

---

## Phase 5 — Tier 2: UX polish during first month (~2 weeks)

Safe to ship incrementally while the partner is using the product. These are the "industry-grade UX" items from the earlier Stripe-parity audit, prioritized.

#### [T2-1] 2-column detail layout primitive — M — ⏳
Sidebar card on right ~35%: resource ID (truncated + copy), created/updated timestamps, status badge, related-resource links, metadata. Apply to Invoice/Customer/Subscription/Plan/Coupon/CreditNote. Shared `<DetailLayout>` component.

#### [T2-2] ID truncation + hover-reveal + copy — S — ⏳
Global component: `cus_abc…def` with click-to-copy, hover → full ID. ~30 replacement sites.

#### [T2-3] Tabular numerals everywhere — XS — ⏳
Single utility class `.amount` applied to all amount columns and totals. One CSS utility, global rollout.

#### [T2-4] Metadata key/value on every resource — M — ⏳
Schema: `metadata JSONB` on customer/subscription/invoice/plan/coupon. UI: editor in sidebar card (add/edit/remove rows). API: accept `metadata` on create/update. Indexable on keys for later search.

#### [T2-5] View-as-JSON slide-over on detail pages — S — ⏳
`{...}` button → slide-over showing raw API response for current resource. Copy-all + "Open in API docs" link.

#### [T2-6] Global search by ID + email + external_id — M — ⏳
CMD+K currently searches 14 nav items + top-10 of 4 entities. Extend to: any ID format (auto-detect prefix → jump), email, external_id, invoice_number, coupon_code. Backend: `GET /v1/search?q=...` cross-resource.

#### [T2-7] Developer panel on detail pages — M — ⏳
Collapsible "Developer" section showing: API endpoint for this resource + curl example, last 5 webhook events targeting this resource, idempotency keys seen, link to full webhook inspector.

#### [T2-8] Keyboard shortcuts + `?` cheatsheet — M — ⏳
Standard set: `g c` customers, `g i` invoices, `g s` subscriptions, `c` create (contextual), `e` export, `/` focus search, `?` show cheatsheet overlay, `j/k` table row nav, `Enter` open selected.

#### [T2-9] Date-range presets + relative+absolute time — S — ⏳
Preset chips: Today / Last 7d / Last 30d / MTD / YTD / Custom. Dates show absolute with tooltip showing relative ("Apr 19, 2026 · 4 days ago"). Configurable per user in profile.

#### [T2-10] Customer detail tabs — S — ⏳
Tabs: Overview / Subscriptions / Invoices / Payments / Activity / Metadata. Lazy-load per tab.

#### [T2-11] Saved views / filter presets — M — ⏳
Serialize current filter+sort+columns to named tabs at top of list: "Past Due", "Enterprise", "Failed Payments Last 7d". Default presets shipped per resource.

#### [T2-12] Row kebab menu + bulk actions (Invoices pilot) — M — ⏳
Checkbox column + kebab per row. Action bar appears when ≥1 selected: "3 invoices selected — Void / Email / Export". Ship Invoices first, roll out to other tables once proven.

---

## Deferred (Phase 3+ — not design-partner blocking)

- SDKs (Python/Node/Go) — OpenAPI + curl is sufficient; SDKs arrive after revenue starts
- Mobile responsiveness — operators use desktop
- 2FA for operator login — nice, not blocking; ship with WorkOS migration later
- Granular RBAC beyond owner/member — Admin/Finance/Read-only is Tier 2 for a paying tenant
- Notification preferences (email me on payment failure) — Tier 3
- Accounting integration (QuickBooks/Xero/NetSuite) — after first paying partner
- Revenue recognition (ASC 606) — Phase 3
- Multiple Stripe accounts per tenant — enterprise ask
- Custom subdomain for customer portal (`billing.partner.com`) — after logo branding lands
- ACH/SEPA/wire rails — after core is stable
- Multi-language (non-English UI + invoices) — Phase 3+
- Demo tenant (`demo.velox.dev`) — marketing asset, post-first-partner
- Per-API-key request log UI (à la Stripe Logs per key) — Tier 2 stretch
- Rich empty-state illustrations — icons are fine; don't build the illustration pipeline
- Seed-data button — anti-pattern, never

---

## Explicit non-goals for this plan

- **Feature parity with Stripe.** We are *design-partner grade*, not Stripe-grade. Ship trust + core flows, iterate on UX based on real feedback.
- **Premature scale optimization.** First 3 partners total < 10k customers combined. Don't re-architect for millions.
- **Marketing site polish.** Three clean pages at `velox.dev` is enough. Don't nerd-snipe on landing-page animation.

---

## Execution sequence (condensed)

```
Week 0 — SHIPPED 2026-04-23
└─ T0-2 through T0-15 per revision history.
   Still open: T0-1 velox.dev site, Phase 1 manual testing pass execution.

Week 0a — Second-audit pre-invite additions — SHIPPED 2026-04-24
├─ ✅ T0-16 HTML + branded emails           (commit dc7396e)
├─ ✅ T0-17 Hosted invoice page             (7 commits, a656b18…1535e64)
├─ ✅ T0-18 Subscription timeline           (commits 6540372, 95816e7)
├─ ✅ T0-19 Webhook secret rotation grace   (commits 099a515, 1bbf169)
├─ ✅ T0-20 Bounce handling minimum viable  (commit 6421340)
├─ ⏳ T0-1  velox.dev marketing site (3 pages)   ← STILL PENDING
└─ ⏳ Phase 1 manual testing pass executed        ← STILL PENDING (gates the invite)

Week 0b — Phase 3 gate verification (2 days)
└─ Runtime verification checklist (alerts firing, backup drill round-trip,
   Stripe end-to-end, 24h synthetic traffic, OpenAPI coverage).

Week 1 — INVITE (≈2026-05-12)
├─ Mon: First design partner invited
├─ T1-3 rate-limit docs, T1-7 CSP (report-only), T1-9 deliverability guide
└─ T1-5 changelog discipline (ongoing), T1-6 partner feedback intake (ongoing)

Week 2
├─ T1-1 remainder (Customer timeline) + T1-8 full handling (complaint + suppression)
└─ T2-1 (2-col layout) + T2-2 (ID truncation) + T2-3 (tabular numerals)

Week 3
├─ T2-4 metadata + T2-5 JSON view + T2-6 global search
└─ T2-7 developer panel

Week 4
├─ T2-8 shortcuts + T2-9 date presets + T2-10 customer tabs
└─ T2-11 saved views + T2-12 row menus (Invoices pilot)
```

**Revised total to invite:** ~2.5 weeks from 2026-04-24 (Week 0a + Week 0b). Then ~4 weeks of Week 1–4 polish with the partner using the product.

---

## Revision history

- **2026-04-23** — Initial draft after UI + operator/backend audits. Corrected earlier "seed-data button" suggestion (anti-pattern per Stripe/Linear/Vercel precedent). Moved customer portal completion from Tier 2 to Tier 0 (audit revealed portal is payment-methods-only). Added T1-4 webhook rotation grace period (audit revealed immediate invalidation). Added T1-2 email brandability (audit revealed hardcoded "Velox Billing"). Added T1-7 CSP (audit revealed missing). Confirmed strengths: auth, GDPR, audit log, rate limit headers, idempotency, encryption-at-rest are already Tier 0+ grade.
- **2026-04-23 (afternoon)** — Shipped T0-2 through T0-6. New files: `PublicLayout`, `DocsShell`, `LegalLayout`, pages for Docs landing + Quickstart + Webhooks + Idempotency + API Reference (Scalar-rendered OpenAPI), Security, Status (placeholder), Changelog (6 backfilled entries), Terms, Privacy, DPA. Added operator-menu links (Docs / Changelog / Status / Report issue) and footer Terms/Privacy in dashboard Layout. `tsc --noEmit` clean; `vite build` green in 439ms.
- **2026-04-23 (late afternoon)** — Shipped T0-7 (Request ID in error toasts) and T0-9 (typed destructive confirmations). New `showApiError()` helper in `lib/formErrors.ts` replaces bare `toast.error()` across 16 pages/components; `apiRequest` gained `Velox-Request-Id` header fallback so Request ID is surfaced even when envelope parsing fails. New `TypedConfirmDialog` component wired into Void Invoice, Void Credit Note, Cancel Subscription, Delete Webhook Endpoint (case-insensitive word match). `tsc -b && vite build` green in 442ms after dropping an orphaned `sonner` import from `Invoices.tsx`.
- **2026-04-23 (evening)** — Shipped T0-8 (customer portal completion — the invite blocker). Backend: new `internal/portalapi` package with five routes under `/v1/me/*` (invoices list, invoice PDF, subscriptions list, subscription cancel, branding). Narrow-interface coordinator pattern mirroring `billing` — `portalapi` depends on `invoice`/`subscription`/`customer`/`settings`/`creditnote`, never the reverse. 404-not-403 on cross-customer access; drafts filtered; `canceled_by: customer` in webhook payload. Frontend: `CustomerPortal.tsx` rewritten with tabs (Invoices / Subscriptions / Payment Methods), partner branding in header (logo + company name + support link), PDF blob download, `TypedConfirmDialog` (`CANCEL`) for cancel flow. 9 unit tests for portalapi via in-memory fakes. `tsc --noEmit`, `vite build` (461ms), `go test ./... -short` all green.
- **2026-04-23 (late evening)** — Shipped T0-10 (in-app support channel, code portion). New `lib/lastRequestId.ts` module tracks the most recent `Velox-Request-Id` seen by `apiRequest` (success or error). "Report an issue" in the account menu now builds its mailto body at click-time with `tenant_id`, URL, user agent, and the freshest trace handle. Login page also gained a `Contact support` mailto underneath the sign-in card. Slack-Connect-per-partner is ops work, not a code change.
- **2026-04-23 (night)** — Shipped T0-11 (onboarding checklist polish). `GetStarted` extended from 4 → 6 steps, each auto-tracked from server state (Stripe creds, plans, customers, subscriptions, webhooks, company profile). Dismiss persisted in localStorage per-tenant (`velox:getstarted-dismissed:${tenantID}`). Gating queries run only while the checklist is visible. "Skip to API-first flow" → `/docs/quickstart` link added. Substituted "Verify email SMTP" with "Complete company profile" because Velox email is platform-level (no per-tenant SMTP) — company name + email is the underlying partner-facing blocker.
- **2026-04-23 (late night)** — Shipped T0-12 (logo guidance + brand accent color). Chose URL-only guidance (option b) over S3/R2 upload — Velox is pre-launch, no storage infra exists, and paste-a-CDN-URL is the Stripe/Linear precedent. Migration 0046 adds `brand_color` (TEXT) to `tenant_settings`; server-side pattern `^#[0-9a-f]{6}$` (strict 7-char lowercase hex). Domain/store/handler plumbed through `invoice/pdf.go` (company name tinted + 2px accent bar; byte-identical for empty `brand_color`). Settings UI: native color picker + hex input + Clear button + live logo thumbnail; logo help text now names Cloudinary / S3 public URL / CDN as example hosts. New `TestParseBrandColor` covers edge cases (uppercase, short-form, missing `#`, too short/long, non-hex). Email brand color deferred — `email/sender.go` bodies are plain text; HTML templating is a separate scope item. `tsc --noEmit`, `vite build` (461ms), `go test ./... -short` all green.
- **2026-04-23 (parallel to manual-testing pass)** — Shipped T0-13, T0-14, T0-15 as a bundled ops batch so the user's manual testing wasn't disrupted by handler code changes. T0-15: extended `docs/ops/runbook.md` with severity definitions, 4 missing playbooks (PaymentIntent failure, scheduler stale, DB pool, outbox backlog), communication templates (status page + partner + internal), rollback procedures (app/migration/feature-flag), and a post-mortem template. T0-14: `scripts/restore-drill.sh` wraps `backup.sh → ephemeral-Postgres → restore.sh → row-count-diff` as a single command with `~/.velox/drill.log` history for trend-over-time; `backup-recovery.md` §4.4 documents it; Security public page gained a **Backup & recovery** section (RPO 5 min / RTO 1 hour / encryption / drill cadence / retention). T0-13: `ops/alerts/*.yaml` (api, billing, payments, webhooks, audit, scheduler) + `ops/alerts/README.md` covering self-hosted Prom / Grafana Cloud / Datadog import, the blackbox-exporter config for `VeloxSchedulerStale`, and the three instrumentation gaps (scheduler gauge, DB pool gauges, outbox backlog gauge) with postgres_exporter/blackbox workarounds until the Go-side instrumentation lands. No handler code changed. All YAML parses; `tsc --noEmit` clean.
- **2026-04-24** — Second-audit brutal readiness review (Session B). Surfaced five pre-invite blockers mis-bucketed as T1 or missing entirely, promoted them into a **Phase 2 Addendum** as T0-16 through T0-20: (1) emails are still plain-text with hardcoded "Velox Billing" despite T0-12 shipping brand color only to the PDF attachment — pulled T1-2 → T0-16; (2) no Stripe-style hosted invoice URL — `/v1/public/payment-updates/{token}` is a payment-method-update surface, not a `hosted_invoice_url` — added T0-17; (3) Invoice has a timeline, Subscription does not, and subscription-change tickets dominate CS volume — pulled subscription half of T1-1 → T0-18; (4) webhook secret rotation invalidates immediately with no grace window — pulled T1-4 → T0-19; (5) no SMTP bounce capture, silent delivery failures erode partner trust in the first week — pulled minimum-viable of T1-8 → T0-20. Extended the Phase 3 gate with six runtime-verification items (shipped ≠ working): manual testing pass actually executed, OpenAPI full-coverage confirmed, alerts firing in real Prom with on-call routing, backup drill cron-scheduled and round-tripped, Stripe end-to-end tested in live-test env, 24h synthetic traffic. Revised execution sequence to ~2.5 weeks to invite (Week 0a + Week 0b) from 2026-04-24. Documentation-only pass — no code changed.
- **2026-04-24 (all-day execution, Session B)** — Shipped the entire Phase 2 Addendum (T0-16 through T0-20) on branch `readiness-phase2-addendum`. 14 commits (`832a640` through `6421340`), ~4000 lines across Go + React + migrations + docs. Every feature anchored to an explicit industry reference before code: T0-17 ↔ Stripe `hosted_invoice_url`; T0-19 ↔ Stripe multi-v1 `Stripe-Signature`; T0-16 ↔ multipart/alternative with inline-styled table layout (Outlook-safe); T0-18 ↔ Stripe subscription activity feed; T0-20 ↔ Stripe bounce-handling conceptual model (bounced / complained / ok / unknown). 5 new migrations (0046 through 0050). Build/test/gofmt/vite-build all green. Branch pushed. Remaining Tier 0 blockers before invite: T0-1 velox.dev marketing site (3 pages) + Phase 1 manual testing pass execution + Phase 3 gate runtime verification.
