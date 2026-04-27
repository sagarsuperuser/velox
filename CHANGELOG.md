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

- **Stripe importer Phase 1 — products + prices** (2026-04-27) —
  `velox-import` now accepts `--resource=products,prices` in addition to the
  Phase 0 customers slice. Stripe `Product` rows map onto Velox `plans`
  (plan `code` = Stripe Product ID for idempotent lookup; plan name +
  description copied verbatim; pricing fields default to USD/monthly/0 and
  are filled in by the price step). Stripe `Price` rows map onto Velox
  `rating_rule_versions` (rule_key = Stripe Price ID; mode = flat;
  flat_amount_cents = Stripe `unit_amount`); the price importer also
  patches the parent plan's `base_amount_cents` to match. Phase 1 only
  handles the simple `billing_scheme=per_unit`, `type=recurring`,
  `usage_type=licensed`, monthly/yearly case — graduated/volume tiered
  prices, one-time prices, metered usage, and day/week intervals all
  produce explanatory `error` rows in the CSV report so operators can
  recreate them manually rather than getting silent partial data.
  Resources run in dependency order regardless of CLI input order:
  customers → products → prices. Same per-row outcome model as Phase 0:
  `insert` / `skip-equivalent` / `skip-divergent` / `error`. New code:
  `internal/importstripe/mapper_product.go`, `mapper_price.go`,
  `product_importer.go`, `price_importer.go` plus matching unit tests
  and integration tests against real Postgres. CLI orchestration in
  `cmd/velox-import/main.go` extended; resources flag now lists the
  three supported values. Phase 1 subscriptions slice (the missing piece
  before fully migrating from Stripe Billing) is unblocked by this ship
  and lands next.
- **SOC 2 Trust Services Criteria control mapping — Week 10 compliance docs** (2026-04-27) —
  Third of the Week 10 compliance docs ships at
  `docs/compliance/soc2-mapping.md`, the audit-prep companion to the
  audit-log-retention and encryption-at-rest guides. Maps all five
  Common Criteria families plus the optional Availability /
  Confidentiality / Processing Integrity / Privacy categories onto the
  Velox surface. Each criterion is laid out as plain-English
  requirement → how Velox addresses it with code-level evidence
  pointers (`internal/...path/file.go:line` format) → explicit gaps →
  artifacts an auditor would request. Scope-and-shared-responsibility
  intro establishes the three-layer model (Velox application
  controls, tenant deployment controls, downstream sub-service
  organisations Stripe + cloud provider under the carve-out method)
  so the rest of the doc stays anchored. CC1 Control Environment
  notes the project's `CONTRIBUTING.md` and PR-review surface plus
  the missing `CODE_OF_CONDUCT.md` and `CODEOWNERS` (cheap closes).
  CC2 Communication and Information walks the public changelog
  surface (`CHANGELOG.md` + `web-v2/src/pages/Changelog.tsx`),
  GitHub Issues as the bug surface, and flags the highest-impact
  gap: no `SECURITY.md` and no `security@<domain>` alias for
  responsible disclosure. CC3 Risk Assessment cites the SLO doc,
  the migration-safety findings doc, and the ADR set as risk-register
  artifacts; flags the missing formal threat model (STRIDE / LINDDUN)
  as an auditor ask. CC4 Monitoring walks the Prometheus metric
  inventory, health checks, audit log per request, CI gate, and the
  quarterly verification cadence for encryption-at-rest + restore
  drill; flags missing default SIEM-aggregation reference config and
  no annual pen test. CC5 Control Activities cites the per-domain
  package architecture (ADR-002), `db.BeginTx(ctx, postgres.TxTenant,
  tenantID)` as the tenant boundary, branch protection + PR review +
  CI as the change gate; flags `govulncheck` non-blocking
  (`continue-on-error: true` in `.github/workflows/ci.yml`) and no
  SAST as gaps. **CC6 Logical and Physical Access** is the longest
  section — it walks the three API key types from
  `internal/auth/permission.go` (platform / secret / publishable, with
  publishable browser-safe and read-only by policy) plus session-
  cookie auth, Argon2id password hashing
  (`internal/user/password.go`), SHA-256 session-ID hashing
  (`internal/session/token.go:16-31`), RLS with `FORCE ROW LEVEL
  SECURITY` on 66 tables (search `internal/platform/migrate/sql/0001_schema.up.sql`),
  livemode separation, the security-headers middleware
  (`internal/api/middleware/security.go`), the GCRA rate limiter
  with fail-closed-capable Redis backing
  (`internal/api/middleware/ratelimit.go`), HMAC-SHA256 webhook
  signature verification (`internal/webhook/service.go:444`), CORS
  with credentials enforcement (`internal/api/middleware/cors.go`),
  and the AES-256-GCM application-layer encryption surface plus the
  email blind index from the encryption-at-rest sibling doc; flags
  the missing MFA / SSO (tracked under WorkOS / Clerk integration),
  coarse API key scopes, and the **single largest CC6.7 gap** —
  no key-rotation tooling for `VELOX_ENCRYPTION_KEY` or
  `VELOX_EMAIL_BIDX_KEY` (envelope-encryption rebuild needed). CC7
  System Operations walks the runbook severity / alert /
  playbook structure, backup-recovery RPO=5min RTO=1h, and the
  audit-log archive restore-into-side-table pattern; flags missing
  quarterly tabletop schedule and customer-notification template.
  CC8 Change Management cites the migration runner with no-tx
  support (`internal/platform/migrate/migrate.go`), the up+down
  migration roundtrip test, branch protection and ADR cadence; no
  major code-level gaps. CC9 Risk Mitigation has the **vendor
  inventory table** with mitigation per dependency: Stripe
  (PaymentIntent-only ADR-001 keeps Velox out of PCI cardholder-
  data scope, circuit breaker on outbound calls, per-tenant
  credentials so tenant-A's Stripe issue doesn't cascade),
  PostgreSQL (PITR + replica), Redis (rate-limiter fail-closed),
  GHCR / External Secrets Operator / WorkOS-Clerk-planned. Sub-
  service-organisation reliance documented: when Velox does its own
  SOC 2, Stripe and the cloud provider are carve-outs whose own
  SOC 2 Type 2 reports cover what Velox doesn't. Additional-
  categories section covers Availability A1.1-A1.3 (capacity-
  planning + backup-recovery + drill cadence), Confidentiality
  C1.1-C1.2 (the encryption-at-rest data-classification table +
  GDPR right-to-erasure flow at `internal/customer/gdpr.go`),
  Processing Integrity PI1.1-PI1.5 (validation middleware,
  idempotency keys + proration dedup + subscription active-
  uniqueness invariant + webhook event replay protection, signed
  outbound webhooks, permission-gated routes, audit-log paper
  trail), and Privacy P-series (notice/choice tenant-owned,
  collection limited to what API calls carry, retention by regime
  per audit-log-retention.md, GDPR access via export and erasure
  via gdpr.delete audit row). Closes with **top gaps to close
  before a Type 1**, ranked by audit impact: 1. key rotation
  tooling, 2. `SECURITY.md`, 3. MFA on dashboard login, 4.
  `govulncheck` blocking in CI, 5. SAST in CI, 6.
  `CODE_OF_CONDUCT.md`, 7. `CODEOWNERS`, 8. status page, 9. image
  signing, 10. threat model doc, 11. customer-breach-notification
  template, 12. quarterly tabletop schedule, 13. vendor
  risk-review log, 14. annual pen test (Velox-as-a-company
  activity), 15. dormant-key sweeper, 16. anomaly detection on
  audit log volumes, 17. fine-grained API key scopes; items 1-9
  are the priority list before pursuing a Type 1 walkthrough.
  Final evidence index is a flat table mapping each
  auditor-likely-to-request artifact to its file path / runbook
  section, copy-pasteable for the engagement-letter scoping
  exercise. Cross-refs added from `docs/ops/runbook.md`
  (Compliance section now carries the SOC 2 mapping entry next to
  audit-log-retention and encryption-at-rest) and `docs/self-host.md`
  (Compliance posture section lists all three shipped Week 10 docs).
  All four Week 10 readiness items now closed.
- **GDPR data export verified for multi-dim usage events** (2026-04-27) —
  Last open Week 10 readiness checkbox closed.
  `GET /v1/customers/{id}/export` now returns the customer's raw `usage_events`
  rows (capped at 10,000 most-recent, with `usage_events_truncated=true` when
  the cap fires) including the per-event `dimensions` JSONB payload that
  multi-dim meters use to dispatch pricing rules. The previous shape carried
  only an unpopulated `usage_summary` map and dropped dimensions entirely —
  meaning a reissued export could not be reconciled against the original
  invoice line items, which is precisely the right GDPR Art. 20 codifies.
  Tenant-level pricing metadata (`meter_pricing_rules`, `billing_alerts`,
  meter definitions) remains intentionally out of scope: it is the operator's
  commercial pricing strategy, not the data subject's personal data.
  New focused integration test `TestGDPR_ExportCustomerData_MultiDimUsageEvents`
  in `internal/customer/gdpr_multidim_integration_test.go` seeds two events
  with mixed string/bool dimensions and asserts exact-match round-trip
  through the export.
- **Encryption-at-rest verification guide — Week 10 compliance docs** (2026-04-27) —
  Second of the Week 10 compliance docs ships at `docs/ops/encryption-at-rest.md`,
  the operator-facing reference for what Velox encrypts at rest, with which
  keys, and how to prove on a running install that the encryption is in effect.
  Covers the application-layer encryption surface — customer email +
  display name, billing-profile legal name + email + phone + tax id (all
  AES-256-GCM under `VELOX_ENCRYPTION_KEY` via `internal/customer/postgres.go`),
  outbound webhook signing secrets primary + secondary
  (`webhook_endpoints.secret_encrypted` / `secondary_secret_encrypted`),
  per-tenant Stripe secret API key + webhook signing secret
  (`stripe_provider_credentials.secret_key_encrypted` /
  `webhook_secret_encrypted`), and the `customers.email_bidx` HMAC-SHA256
  blind index under the separate `VELOX_EMAIL_BIDX_KEY` that lets the
  magic-link flow look up a customer by email without decrypting the
  ciphertext column. Companion sections enumerate what's hashed (API
  keys SHA-256 + 16-byte per-key salt, passwords Argon2id PHC m=64MiB
  t=3 p=4, sessions / password-reset / customer-portal magic-link /
  payment-update tokens all SHA-256), what's plaintext on purpose
  (hosted-invoice public_token by URL-share design, Stripe publishable
  key by Stripe's data classification, key prefix / last4 columns for
  dashboard display), and the two crypto primitives in
  `internal/platform/crypto/crypto.go` (`Encryptor` AES-256-GCM with
  `enc:<base64(nonce||ciphertext)>` envelope and permissive read-side
  passthrough for migration; `Blinder` HMAC-SHA256 deterministic keyed
  hash). Seven copy-pasteable verification recipes: customer-PII
  ciphertext sample, plaintext-row scan across customers + billing
  profiles, webhook-secret + Stripe-credential envelope check, blind
  index population check, end-to-end API round-trip via
  `POST /v1/customers` then `psql` to confirm the row on disk is
  ciphertext, noop-encryptor detection from log lines + recent-rows
  SQL, and operator-side storage-layer probes for AWS RDS / GCP
  Cloud SQL / self-hosted LUKS. Key management section is honest about
  the gap — **no rotation tooling exists today**, rotating
  `VELOX_ENCRYPTION_KEY` breaks decryption of every existing `enc:`
  row with `decrypt: cipher: message authentication failed`, rotating
  `VELOX_EMAIL_BIDX_KEY` silently changes the HMAC output and breaks
  magic-link lookup, the proper fix is envelope encryption (DEK/KEK
  split) and is tracked as long-term work — and exposure-response
  playbook by threat model (SEV-1 for `VELOX_ENCRYPTION_KEY` leak,
  SEV-2 for `VELOX_EMAIL_BIDX_KEY` leak alone, SEV-1 for both). What's
  NOT encrypted section documents Postgres rows generally (covered by
  operator's storage-layer encryption — RDS / Cloud SQL CMEK / LUKS),
  audit log entries (plaintext at the application layer with the
  append-only trigger from migration `0011_audit_append_only` and
  storage-layer encryption as the second defense; `audit_log.actor_id`
  may resolve to a person's name and is treated as personal data per
  the audit-log-retention guide), idempotency keys (shared with the
  client by design, SHA-256 fingerprint exists from migration `0004`
  for collision detection only), Stripe customer / payment-method ids
  (opaque tokens, not PCI cardholder data — card lives in Stripe), IP
  addresses (`audit_log.ip_address` plaintext for accountability;
  retention window is the GDPR mitigation), and key prefix / last4
  columns (display-only fragments for the dashboard). Compliance
  mapping covers SOC 2 CC6.1 / CC6.7 (encryption in transit + at
  rest), PCI-DSS Requirement 3 / 3.5 (Velox holds tokens not PANs;
  the gap is the missing key-rotation tooling), GDPR Article 32
  (security of processing, with the email blind index cited as
  textbook **pseudonymisation** in the Art. 4(5) sense), and HIPAA
  §164.312(a)(2)(iv) for tenants whose own customers are covered
  entities. Configuration knobs section documents the two implemented
  env vars (`VELOX_ENCRYPTION_KEY` fatal in production via
  `internal/config/config.go::validateFatal`, `VELOX_EMAIL_BIDX_KEY`
  currently `slog.Warn` only — recommended to make fatal in production
  once magic-link adoption is non-zero) plus four future env vars that
  the envelope-encryption rebuild would unlock
  (`VELOX_ENCRYPTION_KEY_ID`, `VELOX_KMS_KEK_ARN`,
  `VELOX_BLINDER_KEY_ID`, `VELOX_FORCE_ENCRYPTION_PRODUCTION`).
  Cross-refs added from `docs/ops/runbook.md` (Compliance section now
  carries the encryption-at-rest entry next to audit-log-retention)
  and `docs/self-host.md` (Compliance posture now lists both Week 10
  docs that have shipped). Two more Week 10 docs still pending:
  SOC 2 control mapping and GDPR data export + deletion.
- **Stripe importer — Phase 0 (customers)** (2026-04-26) —
  Week 7 risk-mitigation called for starting the importer in parallel rather
  than waiting until Week 11; this is the catch-up slice that pins down the
  surface and ships the customer importer end-to-end. New CLI `velox-import`
  reads a source Stripe account via `--api-key=sk_...` and writes to a Velox
  tenant via `DATABASE_URL` — never the other direction. New domain package
  `internal/importstripe/` with `Source` (Stripe SDK iterator), pure
  `mapCustomer` (`stripe.Customer` → `domain.Customer` + `CustomerBillingProfile`),
  `CustomerImporter` driver, and a CSV `Report` writer. Each row resolves to
  one of four outcomes: `insert`, `skip-equivalent`, `skip-divergent`, or
  `error`; the same Stripe id rerun produces only `skip-equivalent` so the
  CLI is safe to invoke nightly during a parallel-run cutover. `--dry-run`
  walks the full pipeline (mapping, lookup, diff) but skips DB writes;
  `--livemode-default=true|false` overrides the auto-derived mode for
  restricted keys without the standard `sk_live_/sk_test_` prefix.
  Multi-tax-ID Stripe customers import the first entry and surface a note in
  the CSV (Phase 2 may extend the Velox model). Payment methods and
  subscriptions on the customer are deferred — Phase 2 imports payment
  methods (Stripe Connect-blocker), Phase 1 imports subscriptions. Coverage:
  10 unit tests (mapper variants + driver outcomes), 2 integration tests
  against real Postgres (insert / skip-equivalent / skip-divergent /
  dry-run paths through RLS). Design lives in `docs/design-stripe-importer.md`
  with sketches for Phases 1–2 (subscriptions, products+prices, finalized
  invoices) and the Phase 4 cutover playbook outline.
- **Audit log retention guide — compliance posture for Week 10** (2026-04-26) —
  Week 10 compliance docs kicking off with `docs/ops/audit-log-retention.md`,
  the operator-facing reference for what the audit log captures, how long
  to keep it, and how to prune + archive without locking the hot table.
  Covers the full `audit_log` schema (every column documented including the
  `request_id` added in migration `0030_audit_request_context` and the
  immutability trigger from migration `0011_audit_append_only`), the live
  inventory of recorded event types from the catch-all middleware
  (`internal/api/middleware/audit.go`) plus every handler-explicit
  `auditLogger.Log(...)` call (credit / coupon / subscription / invoice /
  credit-note / GDPR / plan-migration / bulk-action), and the explicit
  list of what's NOT recorded (bootstrap, inbound Stripe webhooks, GETs,
  failed mutations) with rationale. Regime-by-regime retention table with
  reasoning: SOC 2 Type 2 (12-18 months covering a Type 2 cycle), GDPR
  (storage-limitation balance against accountability principle, with the
  audit log as personal-data nuance), PCI-DSS (1 year minimum / 3 months
  immediate per Requirement 10.5.1, applied to API-key-rotation /
  payment-method audit rows that touch the boundary), HIPAA (6 years for
  flow-through tenants), SOX / financial (7 years archived). Velox default
  is **18 months in the live `audit_log` table, indefinite archived to S3**
  with a 7-year bucket-lifecycle expiry that covers the conservative SOC 2
  / SOX upper bound. Operational sections: a partition-vs-batched-DELETE
  decision (Velox ships unpartitioned; revisit at ~5M rows/month sustained),
  a copy-pasteable batched-DELETE prune script using the same NOT-VALID +
  VALIDATE-style "don't lock the hot table" discipline that migration 0015
  used (10k-row batches, `FOR UPDATE SKIP LOCKED`, `pg_sleep(0.1)` between
  batches, `DROP TRIGGER` / `CREATE TRIGGER` bracketing the prune so the
  immutability invariant is restored automatically), a cron line for
  monthly cadence, the `COPY (... WHERE created_at < ...) TO STDOUT |
  gzip | aws s3 cp` archive pattern with content-MD5 verification, an
  S3 lifecycle JSON moving objects to Glacier IR at 90 days and Deep
  Archive at 365 days with a 7-year (2555-day) expiration, and a side
  `audit_log_archive` table restore path for auditor windows that doesn't
  touch the live table. Querying section cites the existing dashboard API
  (`GET /v1/audit-log` + `/filters`, `internal/audit/handler.go`,
  `web-v2/src/lib/api.ts::listAuditLog`) and provides ad-hoc SQL recipes:
  every action by actor X in the last 30 days, every change to subscription
  Y, every API-key rotation in the last 90 days (PCI-relevant), every
  `gdpr.delete` ever, and tracing a customer's `Velox-Request-Id` header
  back to a row via the `request_id` column. Configuration knobs section
  documents the only knob that exists today (`tenant_settings.audit_fail_closed`)
  and flags the future ones as not-implemented (`VELOX_AUDIT_RETENTION_DAYS`,
  `VELOX_AUDIT_ARCHIVE_BUCKET`, `tenant_settings.audit_retention_days`).
  Cross-references added: `docs/ops/runbook.md` gains a Compliance section
  in the table of contents pointing at the new doc; `docs/self-host.md`
  links the new doc from its Compliance posture section and now mentions
  the append-only DB trigger and per-tenant fail-closed posture as live
  facts. Three more Week 10 docs (encryption-at-rest verification, SOC 2
  control mapping, GDPR data export + deletion) still pending.

- **Bulk operations — apply coupon + schedule cancel across cohorts** (2026-04-26) —
  Week 7 ships an operator surface for running an action across many customers
  in a single guarded run. New domain package `internal/bulkaction/` with
  store / service / handler mounted under `/v1/admin/bulk_actions` (gated by
  `PermSubscriptionWrite`). Two action types in v1: `POST /apply_coupon`
  (attaches a coupon to every customer in the cohort) and
  `POST /schedule_cancel` (schedules cancellation on every active subscription
  each customer owns); both share the same `idempotency_key` +
  `customer_filter` shape so the dashboard's drawer renders either outcome
  with one component. `GET /` paginates past runs in reverse-chronological
  order; `GET /{id}` returns the detail row including the full per-target
  `errors[]` array. Idempotency is two-layered: at the cohort level, replay
  of the same key short-circuits via `UNIQUE (tenant_id, idempotency_key)`
  on `bulk_actions`; at the per-target level, each customer's assignment /
  cancel call uses a derived key `<bulk_key>:<customer_id>` so a partial
  failure mid-run is safe to retry without re-applying to already-succeeded
  customers. Migration `0061_bulk_actions` adds the `bulk_actions` table
  (`customer_filter` JSONB, `params` JSONB, `errors` JSONB always-array,
  `target_count` / `succeeded_count` / `failed_count`, `status` CHECK in
  `('pending','running','completed','partial','failed')`, `action_type`
  CHECK in `('apply_coupon','schedule_cancel')`) plus the standard
  FORCE-RLS policy on `(tenant_id, livemode)` and the BEFORE-INSERT
  `set_livemode_from_session` trigger inherited from migration 0021. The
  audit log gains `bulk_action.completed` (one cohort summary per run) and
  per-action `customer.coupon_assigned` / `subscription.cancel_scheduled`
  entries metadata-tagged with `bulk_action_id` so CS reps can answer
  "why was my coupon applied?" without joining tables. The dashboard ships
  at `/bulk-actions` — a tabbed configurator (Apply coupon / Schedule
  cancel) with a shared cohort selector (All / IDs), an idempotency-key
  confirm modal, and a recent-runs sidebar. The Customers page gains a
  multi-select checkbox column plus a "Bulk actions" dropdown that routes
  to `/bulk-actions` pre-scoped to the selection via `location.state` (no
  IDs leak to the URL). `customer_filter.type=tag` is reserved (the wire
  shape accepts it so frontend mocks compile, but the service rejects with
  code `filter_type_unsupported` pending a customer-tag schema). The
  cohort size is capped server-side at 500 per run so a misclick can't
  fan out to a 5,000-tenant deployment. Wire-shape regression tests
  (`TestWireShape_BulkActions_SnakeCase` with subtests for commit / list /
  detail) pin the always-snake_case + always-array errors[] contract;
  service tests cover happy path / partial failure / idempotent replay /
  filter validation / two-mode-switch validation across both actions.

- **Plan migrations history dashboard — list + detail with audit trail** (2026-04-26) —
  Week 7 wraps a UI around the already-live `GET /v1/admin/plan_migrations`
  endpoint (PR #36): a history page at `/plan-migrations/history` lists every
  committed bulk plan swap with `applied_at`, schedule (`immediate` /
  `next_period`), source → target plan IDs, items updated, cohort delta
  highlighted by sign, status badge (`Committed` / `Partial` derived
  client-side from `applied_count` against `totals`), and a truncated
  copyable `idempotency_key`. Cursor-based pagination via `useInfiniteQuery`
  accumulates rows on a "Load more" button — matches the existing
  coupon-redemption pattern. Filters: status (`committed` / `partial`) and
  schedule (`immediate` / `next_period`); date range is a backlog item until
  the list endpoint accepts server-side filter params (today's surface only
  takes `limit` / `cursor`). The detail page at `/plan-migrations/:id`
  shows the full record: applied_at + applied_by + items + schedule key
  metrics, migration parameters (from / to plan, customer filter with full
  ID list when `type="ids"`, idempotency key), the always-array `totals`
  table per currency with before / after / delta, per-item errors surfaced
  from the cohort audit metadata's `item_errors[]`, and an audit trail
  combining the `plan.migration_committed` cohort entry with the
  `subscription.plan_changed` per-customer entries (filtered client-side via
  `metadata.plan_migration_id`). Empty state explains what the page is for
  and links to `/plan-migrations` (preview / commit) and `/docs/api`. The
  existing PlanMigration page gains a "View history" button in the header
  and a "View all migrations" link in the recent-migrations sidebar; rows
  in that sidebar deep-link to the new detail page. No backend changes —
  purely a UI wrap. Detail lookup uses a client-side scan of paginated list
  responses (≤5 pages × 100) since the server doesn't yet expose
  `GET /admin/plan_migrations/{id}`; this is fine for v1 cohort-event
  volumes and explicitly flagged in the API client comment for a future
  server-side detail endpoint.

- **One-off invoice composer — 30-second draft + send from customer page** (2026-04-26) —
  Week 7 ships a primary "New invoice" button at the top-right of every
  customer detail page that opens a drawer composing an invoice without a
  parent subscription. Three field groups: a currency selector (defaults to
  the customer's billing-profile currency, falls back to USD); a
  line-items grid with description / type (`add_on` / `base_fee` / `usage`)
  / quantity / `unit_amount_cents` / a computed total + a remove button per
  row; and a memo Textarea. Two terminal actions: "Save draft" creates the
  invoice in `draft` status and routes to the invoice detail page; "Save &
  send" runs the same draft path then immediately calls finalize +
  `send_email` so the customer receives a hosted-invoice link in the same
  click. Validation is inline before submit — at least one line item,
  description required per line, integer `quantity > 0`, integer
  `unit_amount_cents > 0`. Subtotal renders live as the lines change; tax
  is shown as "Calculated at finalize" (carries the v1 PaymentIntent-only
  tax-neutral posture — the tenant's Stripe account holds the
  registration). On success the dashboard toasts the invoice number, the
  customer's invoices tab refetches, and the toast carries a "View
  invoice" action that deep-links to the invoice page. Partial failures
  surface the invoice number alongside the failed step's error so the
  operator can recover from the invoice-detail surface (e.g. "draft
  created INV-0042 but finalize failed: <reason>").
- **Backend: invoices.subscription_id is now optional** —
  Migration 0060 drops the NOT NULL constraint on `invoices.subscription_id`
  so the new composer (and any future ad-hoc charge path) can write a draft
  without a parent subscription. The partial unique idempotency index on
  `(tenant_id, subscription_id, billing_period_start) WHERE
  billing_reason='cycle'` already treats NULLs as distinct, so two one-off
  drafts coexist for the same period without colliding with cycle invoices.
  `Service.Create` no longer rejects empty `subscription_id` and now
  defaults `billing_period_start` / `billing_period_end` to "now" when both
  are zero (one-off invoices have no canonical cycle window). Default
  `line_type` for `AddLineItem` flips from `"manual"` to `add_on` so the
  CHECK constraint on `invoice_line_items.line_type` (added migration 0017,
  set: `base_fee` / `usage` / `add_on` / `discount` / `tax`) accepts the
  default. Backwards-compatible: cycle-invoice creation paths still pass
  `subscription_id` and the column reads identically when present.

- **Plan migration tool — bulk plan swaps with preview + commit** (2026-04-26) —
  Week 6 deliverable #2 ships an operator-facing surface for moving a cohort of
  subscribers from one plan to another. Three endpoints under
  `/v1/admin/plan_migrations` gated by `PermSubscriptionWrite`:
  `POST /preview` runs the existing `billing.PreviewService` per-customer to
  produce a before / after / delta table (cohort scoped to subscriptions on
  `from_plan_id`, optionally narrowed by `customer_filter.type=ids`);
  `POST /commit` accepts an `idempotency_key` and an `effective` of
  `"immediate"` (proration-aware, swaps `subscription_items.plan_id` and stamps
  `plan_changed_at`) or `"next_period"` (sets `pending_plan_id` +
  `pending_plan_effective_at`), returning `migration_id` /
  `applied_count` / `audit_log_id` (replay of the same key short-circuits via
  `UNIQUE (tenant_id, idempotency_key)` without re-mutation); `GET /` paginates
  past migrations in reverse-chronological order. The history table
  `plan_migrations` (migration 0059) snapshots the cohort summary —
  `customer_filter` JSONB, `totals` JSONB always-array of currency-keyed
  before/after/delta cents, `applied_by` + `applied_by_type` from auth
  context, FORCE-RLS on `(tenant_id, livemode)`, BEFORE-INSERT
  `set_livemode` trigger inherited from migration 0021. Audit log gains TWO
  new event types: `plan.migration_committed` (one cohort summary entry per
  commit, links via `metadata.plan_migration_id`) and
  `subscription.plan_changed` (per-customer entry so CS reps can answer
  "why did my plan change?" tickets without joining tables). The dashboard
  ships at `/plan-migrations` — a three-step flow (configure plans + filter
  + effective → preview cohort table with delta highlighting → commit modal
  with auto-generated idempotency key). Cross-currency migrations reject at
  the service layer with code `currency_mismatch`; `customer_filter.type=tag`
  is reserved (the wire shape accepts it so frontend mocks compile but the
  service rejects with code `filter_type_unsupported` pending a
  customer-tag schema). Wire-shape regression tests pin the always-snake_case
  contract for all three endpoints. See `docs/90-day-plan.md` Week 6 + Week 6
  in `docs/parallel-handoff.md` Track A.

- **Real-time webhook event UI with SSE live-tail + replay (Week 6 Track A)** (2026-04-26) —
  the dashboard's Webhook Events page now streams every dispatched event
  in real time via Server-Sent Events at
  `GET /v1/webhook_events/stream`. The handler emits a snapshot of the
  most recent 50 events on connect (so a freshly-opened tab renders the
  current state, not a blank table), then subscribes to an in-process
  `EventBus` so any subsequent `Service.Dispatch` / replay / deliver-
  result publishes a frame within goroutine-scheduling latency. A 15s
  heartbeat comment line keeps idle proxies (nginx 60s default, AWS ALB)
  from dropping the long-lived connection. Tenant scoping holds two
  ways: the snapshot path runs under the caller's RLS tx, and the
  EventBus subscriber map is keyed by tenant_id so cross-tenant frames
  never reach this socket. The frame shape (`event_id`, `event_type`,
  `customer_id`, `status`, `last_attempt_at`, `created_at`, `livemode`,
  `replay_of_event_id`) is pinned by
  `TestWireShape_WebhookEventsStream_SnakeCase` — drift fails CI before
  it can break the dashboard. Critical wiring: the global
  `middleware.Timeout(30s)` was lifted off the router root and applied
  per route block instead, because a 30s cap would kill any open SSE
  socket; the stream route mounts on a sibling path ABOVE `/v1` with
  the same auth (session-or-API-key + rate-limit + `PermAPIKeyRead`)
  but specifically WITHOUT the timeout middleware. The new
  `POST /v1/webhook_events/{id}/replay` endpoint clones an event into a
  fresh `webhook_events` row tagged `replay_of_event_id=<root>` and re-
  dispatches to every matching active endpoint; clones always point at
  the **root** original (single-pivot rule: replaying a clone collapses
  to the original, never chains), so the audit timeline stays a flat
  `WHERE id = $1 OR replay_of_event_id = $1` walk. Replay returns
  `{event_id, replay_of, status: "queued"}` (pinned by
  `TestWireShape_WebhookEventReplay`) so the dashboard can highlight
  the new clone in the live tail and toast the audit pivot. The
  companion `GET /v1/webhook_events/{id}/deliveries` endpoint stitches
  the original event plus every replay clone into one ordered timeline
  (`{root_event_id, deliveries: [{attempt_no, status, status_code,
  response_body, error, request_payload_sha256, attempted_at,
  completed_at, next_retry_at, is_replay, replay_event_id}]}` — pinned
  by `TestWireShape_WebhookEventDeliveries`). `request_payload_sha256`
  on each row drives the dashboard's diff view: Stripe-style replays
  don't mutate the payload, so the verdict collapses to "payload
  identical to previous" in the common case and surfaces an unexpected
  mutation when something goes wrong. Response bodies are truncated to
  4KB before hitting the wire so a misbehaving receiver returning a
  megabyte HTML page can't blow out the deliveries-list payload size.
  Migration `0058_webhook_event_replay` adds the
  `replay_of_event_id TEXT REFERENCES webhook_events(id) ON DELETE SET
  NULL` column plus a partial index `WHERE replay_of_event_id IS NOT
  NULL` for the timeline-walk query — picked next-from-origin/main per
  the migration-numbering policy. The frontend mounts a new page at
  `/webhooks/events` (`web-v2/src/pages/WebhookEvents.tsx`) with a
  connection-status pill (live / connecting / reconnecting /
  disconnected — the browser auto-reconnects EventSource), a row-level
  Replay button, and an expandable per-event timeline that lazy-fetches
  the deliveries on first expand. Buffer caps at 200 frames in memory
  (4× the snapshot) so the table stays snappy on a busy tenant; rows
  are deduped by `event_id` so the `pending → succeeded` transition
  flips a single row in place rather than spawning a duplicate. The
  legacy `/v1/webhook-endpoints/events/*` path stays as-is (still
  returns `{status: "replayed"}` for backwards compatibility) — this
  is purely an additive surface alongside it. v1 sizing is single-
  replica per CLAUDE.md so the in-memory bus is correct; a future
  multi-replica deployment would route SSE through Postgres
  LISTEN/NOTIFY (one-line swap inside the bus) — explicitly noted as
  out-of-scope for v1.

- **Operator CLI — `velox-cli` with `sub list` + `invoice send`** (2026-04-26) —
  Week 7 lane lands a single-binary CLI under `cmd/velox-cli/` that
  hits the same `/v1/*` HTTP surface external integrations use, so
  it's a faithful proxy for the public API rather than a DB-coupled
  back door. Auth is a platform API key from `VELOX_API_KEY` (or
  `--api-key`) — the CLI never writes the key to disk. Two
  subcommands shipped:
  - `velox-cli sub list` — `GET /v1/subscriptions` with `--customer`,
    `--plan`, `--status`, `--limit`, `--output text|json`. Text
    output is hand-rolled aligned columns
    (`ID  CUSTOMER  PLAN  STATUS  CURRENT_PERIOD_END`); JSON
    pass-through for `jq` piping.
  - `velox-cli invoice send` — `POST /v1/invoices/{id}/send` with
    `--invoice`, `--email`, `--dry-run`, `--output text|json`.
    `--dry-run` short-circuits before the network call so an
    operator can verify the request shape against a wrong tenant
    without firing an email.
  - Cobra (`github.com/spf13/cobra v1.10.2`) for command structure;
    no tablewriter or other formatting libs — hand-rolled columns
    are fine for v1. Single static binary; no runtime config files.
  - `make cli` builds `./bin/velox-cli`. README at
    `cmd/velox-cli/README.md` covers install + auth + first-command
    walkthrough.
  - 11 unit tests against `httptest`-backed servers pin behavior:
    text formatting, JSON pass-through, query-param mapping, empty
    list friendly message, 401 surfacing, dry-run-no-network,
    auth-header presence, content-type round-trip, decode-error
    surfacing, query-string encoding, default-base-URL trimming.
  - **Deferred:** `import-from-stripe` (Week 11, after the bigger
    importer RFC); bulk operations and a one-off invoice composer
    (later Week 7 lanes).

- **Self-host paper artifacts — Helm chart + Terraform AWS module** (2026-04-26) —
  the Week 9 follow-up to the Compose lane lands two structurally
  validated deploy targets, both pinned to the env-var schema the
  Compose stack already exercises (no invented keys). `deploy/helm/velox/`
  is a generic-Kubernetes chart (kind / k3s / EKS / GKE / AKS): a
  single-replica deployment by default (the v1 scheduler is
  leader-elected via Postgres advisory locks so multi-replica is safe
  but not required), Service / ConfigMap / Secret / ServiceAccount,
  optional Ingress and HorizontalPodAutoscaler templates gated behind
  `ingress.enabled` / `autoscaling.enabled` (both default off — most
  operators front Velox with their existing ALB/Cloudflare/nginx and
  v1 sizing makes HPA premature). The chart does **not** bundle
  Postgres on purpose — bring your own (RDS / Cloud SQL / Supabase /
  Neon) via either `externalDatabase.url` or the split
  `DB_HOST`/`DB_PORT`/`DB_NAME`/`DB_USER` shape; password always lives
  in the Secret. ESO / sealed-secrets users can point
  `secrets.existingSecret` at a pre-existing Secret and the chart
  skips the Secret template. `helm lint` passes clean; `helm template`
  parses through `yaml.safe_load_all` for both default values (5
  manifests) and full values with ingress + autoscaling on (7
  manifests). `deploy/terraform/aws/` is a single-VPC + single-EC2
  (`t3.small`) + RDS Postgres (`db.t3.small`) + S3 backup bucket
  module. Architecture decision is locked: NOT EKS, NOT autoscaling,
  NOT multi-AZ. Two-AZ public + two-AZ private subnet layout (RDS
  demands ≥2 AZs in a subnet group even for single-AZ instances), EC2
  in public-a only, RDS in the private subnet group with security
  group ingress restricted to the EC2 SG. The S3 bucket ships with
  versioning + AES256 SSE + Block Public Access on + a lifecycle rule
  that tiers `base/` to Glacier Instant Retrieval at 30 days, Deep
  Archive at 90 days, expires at 365 days, and expires `wal/` at 14
  days. EC2 cloud-init installs Docker + the Compose plugin, clones
  the repo at `velox_repo_ref`, generates a `.env` with a random
  `VELOX_ENCRYPTION_KEY` + `VELOX_BOOTSTRAP_TOKEN` and a
  `DATABASE_URL` pointing at the RDS endpoint, then runs `docker
  compose up -d nginx velox-api` from `deploy/compose/` (reuses the
  Compose lane's stack — does not reinvent it). EC2 IAM instance
  profile has read/write to the backup bucket plus
  `AmazonSSMManagedInstanceCore` for Session Manager fallback.
  IMDSv2-required, encrypted root volume, default `ssh_allowed_cidrs`
  is the RFC 5737 documentation block (fail-closed; operator must set
  their own CIDR before apply). `terraform init -backend=false &&
  terraform validate` passes clean; `terraform plan` shows 28
  resources to add and `terraform fmt -check` is silent. Cost
  estimate at default sizing: ~$30-50/mo if left running 24/7, ~$1-2
  for an apply/destroy validation run. Both new modules ship with
  copy-pasteable READMEs that walk install / upgrade / destroy and
  call out the limitations (no Route 53, no TLS, no multi-AZ — all v2
  follow-ups). `docs/self-host.md` replaces the earlier "coming soon"
  placeholders with real cross-links to all three deploy paths;
  `docs/self-host/postgres-backup.md` gains a section on wiring the
  Terraform-provisioned S3 bucket as the WAL archive target via the
  user-data-installed `/usr/local/bin/velox-wal-ship.sh` wrapper.
  Live `terraform apply` against a real AWS account is a separate
  user-decision lane on purpose — paper artifacts ship clean here so
  the cold-install drill picks up against a known-validated module.

- **Self-host quickstart — Docker Compose stack + Postgres PITR guide + landing page** (2026-04-26) —
  the single-VM path of Week 9's self-host playbook ships. `deploy/compose/`
  drops a three-service stack (postgres + velox-api + nginx) wired to
  `RUN_MIGRATIONS_ON_BOOT=true` so a fresh VM is one `docker compose up
  -d` away from a working tenant: nginx terminates HTTP on `:80`, proxies
  to `velox-api:8080` with 35s timeouts matching the binary's
  `WriteTimeout`, and gates `/metrics` to RFC1918 ranges. The
  `postgres-init.sql` creates the non-superuser `velox_app` runtime role
  so the RLS policies are actually enforced (superusers and database
  owners bypass policies; without the role, the binary falls back to
  admin with a loud warning per `cmd/velox/main.go:openAppPool`). The
  `.env.example` mirrors `internal/config/config.go` and every per-package
  `os.Getenv` callsite end-to-end — three required keys
  (`POSTGRES_PASSWORD`, `VELOX_ENCRYPTION_KEY`,
  `VELOX_BOOTSTRAP_TOKEN`), everything else commented with the binary's
  own defaults. The velox-api healthcheck calls the binary's `version`
  subcommand because the image is distroless (no shell, no curl); the
  HTTP-level liveness probe lives on nginx in front. The stack
  defaults to `APP_ENV=production` so the encryption-key fatal check,
  secure cookies, and HSTS protections are on the moment a real
  operator runs it. `docs/self-host/postgres-backup.md` walks
  `pg_basebackup` + WAL archiving for PITR with retention
  recommendations (7 daily / 4 weekly / 12 monthly across hot / cool /
  cold S3 tiers) and a quarterly restore drill — every recipe links to
  the real Postgres 16 manual chapter (no hallucinated URLs). Operator
  guidance includes a copy-pasteable backup script, a tested restore
  procedure, and explicit notes on what's *not* covered (logical
  cross-version dumps, HA, encryption-key escrow). The new
  `docs/self-host.md` landing page picks the install shape (Compose
  today; Helm + Terraform AWS module flagged as a follow-up lane,
  not fake-linked), surfaces the required env-vars, sizing table, TLS
  options, and compliance-posture stub. Helm + Terraform + cold-install
  on real AWS are deferred to a follow-up Week 9 lane per the
  90-day plan.

- **Billing alerts — Stripe Tier 1 parity for "Billing Alerts"** (2026-04-26) —
  the operator-configurable threshold surface that fires a webhook +
  dashboard notification when a customer's cycle spend (or per-meter
  usage) crosses a limit. Four endpoints: `POST /v1/billing/alerts` with
  `{ title, customer_id, filter: { meter_id?, dimensions? }, threshold:
  { amount_gte? | usage_gte? }, recurrence: "one_time" | "per_period" }`,
  `GET /v1/billing/alerts/{id}`, `GET /v1/billing/alerts?customer_id=…
  &status=…&limit=…&offset=…`, and `POST /v1/billing/alerts/{id}/archive`
  for soft-disable. Mounted under `PermInvoiceRead` / `PermInvoiceWrite`
  at `/v1/billing/alerts`; the path is registered before `/billing` so
  chi picks the more-specific pattern. A background evaluator (interval
  configurable via `VELOX_BILLING_ALERTS_INTERVAL`) leader-elects via
  Postgres advisory lock `LockKeyBillingAlertEvaluator`, scans armed
  alerts via the partial index `idx_billing_alerts_evaluator` (predicate
  `status IN ('active','triggered_for_period')`), aggregates the
  customer's current cycle through the same `AggregateByPricingRules`
  LATERAL JOIN the cycle scan and customer-usage already use (so
  alert-firing math == invoice math by construction), and on threshold
  cross fires a `billing.alert.triggered` webhook through the outbox in
  the same tx as the trigger insert + alert state mutation — the
  `UNIQUE (alert_id, period_from)` index gives per-period idempotency
  across replica races and evaluator retries. `recurrence: one_time`
  transitions to a terminal `triggered` status; `recurrence:
  per_period` transitions to `triggered_for_period` and re-arms when
  the next cycle begins. Wire shape is snake_case throughout
  (regression-gated by `TestWireShape_SnakeCase`), `dimensions` is
  always-object `{}` (no null guard needed in dashboard rendering),
  `threshold` always emits both `amount_gte` and `usage_gte` keys with
  one as `null`, and `usage_gte` is decimal-as-string per ADR-005
  (NUMERIC(38,12) round-trip preserved). Service-layer validation
  enforces title required + ≤200 chars, recurrence in
  `{one_time, per_period}`, exactly one threshold field set with
  amount > 0 / quantity > 0, dimensions only valid when meter_id is set,
  ≤ 16 dimension keys, scalar values only (string / number / bool).
  Two new mode-aware tables (`billing_alerts`, `billing_alert_triggers`)
  ship with the standard tenant + livemode RLS policy from migration
  0020 and the BEFORE INSERT livemode-from-session trigger from
  migration 0021 (added to the regression list in
  `TestRLSIsolation_AllModeAwareTablesHaveLivemodePredicate`). Tests:
  24 unit cases (handler validation table, service validation table,
  evaluator dimension-match / should-fire / primary-sub-pick tables,
  payload-build, meter-aggregation map) plus 9 integration cases
  against real Postgres (one-time-fire, per-period-fire-and-rearm,
  double-fire-idempotent, archived-skipped, below-threshold-no-fire,
  no-subscription-warning, multi-tenant-isolation,
  atomicity-on-rollback verifying the alert-state-update is rolled
  back when outbox enqueue fails, RLS isolation). Migration 0057.

- **Billing thresholds — Stripe Tier 1 parity hard-cap mid-cycle finalize** (2026-04-26) —
  the fourth flagship developer-experience surface alongside customer-usage,
  recipes, and create_preview (per `docs/design-billing-thresholds.md`,
  `docs/positioning.md` pillar 1.4). Configures a per-subscription
  hard cap on running cycle subtotal (`amount_gte`, integer cents) and/or
  per-subscription-item quantity caps (`item_thresholds[]` with
  `usage_gte` as a NUMERIC(38,12) decimal string, ADR-005). When usage
  pushes the running total past any configured cap, the engine emits an
  early-finalize invoice with `billing_reason='threshold'` mid-cycle —
  the same charge-and-dunning chain a natural-cycle invoice goes
  through, just before the period boundary. `reset_billing_cycle` (default
  true, matching Stripe) controls whether the cycle resets after fire so
  the next bill starts from fire-time, or whether the original cycle
  continues with residual usage. PATCH `/v1/subscriptions/{id}/billing-thresholds`
  with `{amount_gte, reset_billing_cycle?, item_thresholds[]}` writes the
  configuration; DELETE clears it. Rejects multi-currency subs at the
  handler layer (it's the only layer with a PlanReader), terminal subs at
  the service layer, foreign / duplicate / blank `subscription_item_id` and
  non-numeric / negative `usage_gte` values across both layers — so the
  store sees only validated input. Engine path: a new `Engine.ScanThresholds`
  tick runs in the scheduler between auto-charge retry and the natural
  cycle scan (Step 0.5), pulling candidates via
  `subs.ListWithThresholds(ctx, livemode, batch)` then calling
  `evaluateThresholds` over the partial cycle window. Reuses
  `previewWithWindow` so the running subtotal is the same figure the cycle
  would bill — preview math == invoice math by construction (same
  guarantee as create_preview). Per-item caps sum quantities across each
  item's plan meters via `usage.AggregateByPricingRules` (the same
  priority+claim LATERAL JOIN the cycle scan and customer-usage already
  use), so multi-dim tenants get the same canonical aggregation. Idempotency
  seam: a partial unique index on
  `invoices(tenant_id, subscription_id, billing_period_start) WHERE
  billing_reason='threshold'` makes a re-tick after a transient failure a
  no-op — the second `CreateInvoiceWithLineItems` lands on
  `errs.ErrAlreadyExists` and short-circuits without losing the row.
  Skips terminal/trialing subs and `pause_collection` rows so the scan
  doesn't emit a draft that can't be charged. Webhook event
  `subscription.threshold_crossed` fires before the optional cycle reset.
  Snake-case JSON, always-array slices, decimal-as-string usage_gte
  enforced by `TestWireShape_SnakeCase`. Tests: 12 unit cases on the
  service validation (empty body, negative amount, terminal sub, foreign
  / duplicate / blank `subscription_item_id`, non-numeric / negative
  `usage_gte`, default + explicit `reset_billing_cycle`), 7 unit cases
  on `evaluateThresholds` + `ScanThresholds` (amount-cross, item-cross,
  below-amount, below-item, terminal-sub gate, paused-collection gate,
  no-candidates fast path), 6 integration cases against real Postgres
  (amount-cross fires early with cycle reset, item-usage-cross fires,
  re-tick idempotent, below-threshold no-fire, no-config-skipped,
  reset_billing_cycle=false keeps cycle), plus 3 wire-shape cases on
  the input + domain JSON contracts. Migration `0056_subscription_billing_thresholds`
  adds two columns on `subscriptions` (`billing_threshold_amount_gte`,
  `billing_threshold_reset_cycle`), a `subscription_item_thresholds`
  table with RLS, the `billing_reason` column on `invoices` with a
  CHECK constraint covering `'subscription_cycle' | 'subscription_create' |
  'manual' | 'threshold' | NULL`, and the partial unique index. Cycle-scan
  invoices now stamp `billing_reason='subscription_cycle'` so the
  threshold reason isn't an outlier.

- **Create-preview endpoint — Stripe Tier 1 parity for `Invoice.upcoming`** (2026-04-26) —
  the third flagship developer-experience surface alongside customer-usage
  and recipes (per `docs/design-create-preview.md`, `docs/positioning.md`
  pillar 1.4). `POST /v1/invoices/create_preview` answers "what is my
  next bill going to look like?" with the same line set the cycle scan
  would emit if billing fired right now — so dashboard projected-bill
  math == invoice math by construction. Body is `{customer_id,
  subscription_id?, period?}`; `subscription_id` defaults to the
  customer's primary active or trialing sub (most-recent-cycle wins),
  and `period` defaults to that sub's current billing cycle. Response
  carries one line per `(meter, rule)` pair — multi-dim meters surface
  one line per rule with `dimension_match` echoed from the meter
  pricing rule (the canonical pricing identity), single-rule meters
  keep the one-line-per-meter shape. `quantity` marshals as a precise
  decimal string (NUMERIC(38,12) round-trip per ADR-005) so fractional
  AI-usage primitives (GPU-hours, cached-token ratios) don't lose
  precision; amounts are integer cents. `totals[]` is always-array
  (one entry per distinct currency, even when there's only one) — same
  shape customer-usage uses, so a single TS type set covers both
  surfaces. The big shift here: the existing `Engine.Preview` wired
  against `usage.AggregateForBillingPeriod` (not multi-dim aware) is
  replaced with the priority+claim LATERAL JOIN path
  (`usage.AggregateByPricingRules`) the cycle scan and customer-usage
  already use. Multi-dim tenants were silently looking at wrong
  projected-bill numbers in the in-app debug route; that gap is closed.
  RLS-by-construction: cross-tenant customer IDs return 404 at the
  customer lookup. No DB writes — the integration test asserts row
  counts of `invoices` and `invoice_line_items` are unchanged
  before/after the call. Errors: `404` for unknown customer or
  subscription; `422 invalid_request` for cross-customer subscription
  IDs; `422 customer_has_no_subscription` (coded, symmetric with
  customer-usage) when implicit pick has zero active subs; `422` for
  partial period bounds, `from >= to`, or unparseable RFC 3339.
  Mounted at `/v1/invoices/create_preview` as a sibling of `/invoices`
  (registered first so chi picks the more-specific pattern, otherwise
  `/{id}` would capture `create_preview` as an invoice ID) behind
  `PermInvoiceRead`. Snake-case JSON keys, struct-tag enforced; empty
  list fields emit `[]` not `null` (regression-tested by
  `TestWireShape_SnakeCase` — the merge gate). Service composes
  through three narrow interfaces local to the billing package
  (`CustomerLookup`, `SubscriptionLister`, plus the engine's existing
  `PricingReader`/`UsageAggregator` extended with
  `ListMeterPricingRulesByMeter` + `AggregateByPricingRules`) so the
  preview owns no cross-domain state. Tests: 16 unit cases (period
  resolution table-driven, primary-sub pick table-driven, explicit-ID
  happy path + cross-customer rejection + 404 propagation, blank
  customer ID, customer-not-found, JSON-decode edge cases, totals
  roll-up multi-currency stable order, blank-currency exclusion,
  wire-shape full + empty-slices regression) plus seven integration
  cases against real Postgres (single-meter flat parity matching
  customer-usage exactly, multi-dim parity matching the LATERAL JOIN,
  no-writes assertion via row-count diff, cross-tenant 404,
  `customer_has_no_subscription` coded error, explicit-subscription
  wrong-customer rejection). Existing `/v1/billing/preview/{id}` debug
  route now returns the new shape too (consistent line composition;
  `web-v2` `SubscriptionDetail.tsx` updated to use `totals[]` per-row
  rendering, `lib/api.ts` `InvoicePreview` retyped — `quantity` is now
  a string, top-level `subtotal_cents`/`currency` removed). v2
  deferred: inline `invoice_items` overlay (modeling "+$50 charge"),
  plan-change overlay (Week 5c will own it), coupon/credit/tax
  application (engine still doesn't reproduce these in preview).

- **Customer usage endpoint — one call answers "what did this customer use?"** (2026-04-26) —
  the second flagship developer-experience surface, alongside recipes (per
  `docs/design-customer-usage.md`, `docs/positioning.md` pillar 1.4). `GET
  /v1/customers/{id}/usage` collapses "show me what this customer used and
  owes" into one read. With no params it returns the customer's current
  billing cycle by default; `?from=&to=` (RFC 3339, both required if either
  is supplied, ≤ 1 year) overrides for historical or audit windows.
  Response includes per-meter aggregates (`total_quantity` as a precise
  decimal string, `total_amount_cents` as integer cents), per-rule
  breakdowns for multi-dim meters with `dimension_match` echoed from the
  meter pricing rule (the canonical pricing identity, not the events
  seen), per-currency `totals[]`, and a `subscriptions[]` block carrying
  plan name + cycle bounds so a dashboard renders "Plan: AI API Pro ·
  cycle Apr 1 → May 1" without follow-up calls. The endpoint is
  composition, not new aggregation logic — the priority+claim LATERAL
  JOIN already shipped in Week 2 lives in
  `usage.AggregateByPricingRules`; this surface walks the customer's
  subscribed plans → meters, calls the same store path the cycle scan
  uses, then prices each rule bucket through
  `domain.ComputeAmountCents`. Same code → dashboard math == invoice
  math. RLS-by-construction: cross-tenant customer IDs surface as 404
  via the standard `BeginTx(TxTenant, …)` lookup. Errors:
  `customer_has_no_subscription` (400, coded) when the caller passes no
  period and the customer has zero active subs; `400` for partial
  bounds, `from >= to`, or window > 1 year. Mounted at
  `/v1/customers/{id}/usage` as a sibling of `/customers` (matches the
  `/customers/{id}/coupon` precedent) behind `PermUsageRead` so a
  read-only secret key powers the dashboard without inheriting customer
  write capability. Snake-case JSON keys, struct-tag enforced; empty
  list fields emit `[]` not `null` so clients iterate without null
  guards. Composition uses three narrow interfaces local to the usage
  package (`CustomerLookup`, `SubscriptionLister`, `PricingReader`) so
  the customer-usage code owns no cross-domain state. Tests: nine unit
  cases (period resolution table-driven, single-meter flat parity,
  multi-currency totals roll-up, multi-dim dimension-match echo, flat
  meter omits `dimension_match`, customer-not-found propagation,
  blank-customer validation, aggregation-mode mapping, wire-shape
  empty-arrays regression) plus four integration cases against real
  Postgres (single-meter parity with in-cycle vs. outside-cycle event
  filtering, multi-dim parity matching `usage.AggregateByPricingRules`
  exactly, cross-tenant 404 via RLS, no-sub + explicit window). Track B
  can swap their cost-dashboard scaffold from MSW mocks to the live API
  at integration time. **Note on totals shape:** `totals` always emits
  an array even when there's only one currency (one entry per distinct
  currency), instead of the design RFC's "scalar when single currency"
  form — one consistent shape lets dashboards iterate without branching
  on cardinality.

- **Recipes picker UI — discoverable pricing installation** (2026-04-26) —
  the dashboard surface for the recipes feature (Track A backend
  entry below). New `/recipes` page (`web-v2/src/pages/Recipes.tsx`)
  lists the five built-ins as cards with a creates-summary chip
  strip (`{meters, rating_rules, pricing_rules, plans,
  dunning_policies, webhook_endpoints}`); each card opens a
  configure dialog that renders the overrides form from
  `recipe.overridable[]` (string / number / boolean fields with
  `enum`, `max_length`, and `pattern` honored). Preview button
  posts `/v1/recipes/{key}/preview` and renders the would-be graph
  inline (truncated past 5 items per object class) plus any
  warnings; Install posts `/v1/recipes/instantiate` and navigates
  to the first `created_objects.plan_ids` so the new catalog is
  one click away. Sidebar entry added under Configuration
  (`Sparkles` icon) and the onboarding wizard's step 1
  ("Pick a pricing template") now fetches `/v1/recipes` live and
  deep-links into the picker. New TS types in
  `web-v2/src/lib/api.ts`: `Recipe`, `RecipeDetail`,
  `RecipeOverrideSchema` (`{key, type, default?, enum?, max_length?,
  pattern?}` — collapsed from the design's `string[]` +
  `Record<key, schema>` split into a single self-describing array,
  matches PR #25's actual wire shape), `RecipePreview`,
  `RecipeInstance`, `RecipeCreatesSummary`. Falls back to an empty
  list when the backend endpoint isn't reachable so the page stays
  usable on pre-recipes builds.

- **Multi-dimensional meter dashboard surfaces** (2026-04-26) — the
  operator-side complement to the Week 2 multi-dim meters engine
  (Track A entry below). `web-v2/src/pages/MeterDetail.tsx` gets a
  "Dimension-matched rules" card: chips-table over each rule's
  `dimension_match` keys, the aggregation mode (one of `sum`,
  `count`, `last_during_period`, `last_ever`, `max`), priority,
  and rating-rule reference. "Add rule" dialog walks the operator
  through a dynamic key/value dimension builder + select for the
  five aggregation modes + rating-rule selector + priority input;
  delete is gated through the `TypedConfirmDialog` (type
  `delete` to confirm — same pattern as void-invoice). The
  default-rule card was renamed "Default pricing rule" with a
  fallback-explainer subtitle so operators understand the
  priority+claim relationship. `web-v2/src/pages/UsageEvents.tsx`
  gets a conditional `Dimensions` column (only shown when at least
  one event in view carries them, or a filter is active), a
  `key=value` text filter that flows through to the
  `dimensions=` query param, and a CSV export that now carries the
  dimensions JSON column. The decimal-precision `quantity` field
  is now read as a string-encoded NUMERIC end-to-end —
  `eventQuantity()` coerces to `number` only at chart math; raw
  display preserves trailing-zero precision (`1234.567890123456`).
  TS types added: `MeterPricingRule`, `MeterAggregationMode` union,
  plus `api.{listMeterPricingRules, createMeterPricingRule,
  deleteMeterPricingRule}` client methods (all hyphen paths,
  matching the rest of `/v1/*`).

- **Pricing recipes — one-call billing setup** (2026-04-26) — the
  developer-experience flagship that turns Week 2's multi-dim meter
  engine into a 30-second quickstart (per `docs/design-recipes.md`,
  `docs/positioning.md` pillar 1.3). `POST
  /v1/recipes/{key}/instantiate` atomically builds the full graph
  (rating rules → meters → multi-dim pricing rules → plan → optional
  dunning policy → optional webhook endpoint → instance row) under a
  single tenant-scoped transaction; partial state never reaches the
  tenant. Five built-ins ship in v1 — `anthropic_style` (4 Claude
  models × input/output, cached input via priority=200 rule),
  `openai_style` (14 rules across GPT-4 / GPT-4o / 3.5-turbo /
  embeddings), `replicate_style` (per-second GPU billing across
  a100/a40/t4/cpu), `b2b_saas_pro` (seats-with-included-tier +
  storage), `marketplace_gmv` (package-billing GMV take rate +
  per-transaction fee). Recipes live as embedded YAML under
  `internal/recipe/recipes/*.yaml` (`embed.FS`, loaded at boot — no
  DB recipe table); per-instantiation overrides (`currency`,
  `plan_name`, `plan_code`, plus recipe-specific knobs like
  `included_seats`) flow through `text/template` with
  `Option("missingkey=error")` so a typo fails preview rather than
  silently drops. `POST /{key}/preview` renders the would-be graph
  with zero DB writes — cheap enough to call on every override-form
  keystroke and powering the dashboard's review dialog. Idempotency
  is enforced two ways: `(tenant_id, recipe_key)` UNIQUE in postgres,
  plus a Service-layer pre-check inside the same tx that returns
  `ErrAlreadyExists` with the existing instance ID surfaced through
  the WithCode error. Force re-instantiation is reserved for v2 — v1
  accepts the `force` field and returns `InvalidState` with a clear
  message, keeping the API contract stable when force support lands.
  Uninstall removes the recipe_instance row only; the resources the
  recipe created (plans, meters, dunning policy, webhook endpoint)
  stay — operators own them once they exist, and silent cascade
  could lose live billing data. Cross-domain transactional
  composition is built on `*Tx` writers added to
  `pricing.Service` (CreateRatingRuleTx, CreateMeterTx, CreatePlanTx,
  UpsertMeterPricingRuleTx), `dunning.Service` (UpsertPolicyTx), and
  `webhook.Service` (CreateEndpointTx); `recipe.Service` defines
  narrow `PricingWriter` / `DunningWriter` / `WebhookWriter`
  interfaces it threads the same `*sql.Tx` through. Six integration
  tests verify the contract end-to-end against real Postgres: full
  graph build (counts match the design doc — 1 meter / 9 rating
  rules / 9 pricing rules / 1 plan / 1 dunning policy / 1 webhook
  endpoint for `anthropic_style`), idempotency, mid-graph rollback
  (synthetic failure injected via a `failingPricingWriter` wrapper —
  zero rows survive in every cross-domain table), RLS isolation
  (tenant B never sees tenant A's instance), preview/instantiate
  parity, and uninstall-keeps-resources. Mounted at `/v1/recipes`
  behind `PermPricingWrite`. Dashboard surface is Track B's next
  slice.

- **Multi-dimensional meters foundation — AI-native wedge** (2026-04-25) —
  the runtime engine for Velox's positioning bet (per
  `docs/positioning.md`): one meter receives events with arbitrary
  dimensions, many pricing rules pick out subsets at different rates.
  Migration `0054_multi_dim_meters` widens `usage_events.quantity` to
  `NUMERIC(38,12)` (Stripe `quantity_decimal` parity), adds a GIN index
  over `properties` for JSONB-superset dispatch, and introduces
  `meter_pricing_rules (dimension_match, aggregation_mode, priority)` —
  N rules per meter, claim-based, no double-count. Per-rule
  `aggregation_mode` adds `count`, `last_during_period`, `last_ever`,
  `max` to the existing `sum` (Stripe Tier 1 gap closed). Ingest
  accepts a `dimensions` field (alias for `properties`) capped at 16
  scalar keys, matching the rule-side `dimension_match` cap. The
  priority+claim resolution query lives in
  `usage.Store.AggregateByPricingRules`: rules walked in `priority
  DESC, created_at ASC` order, each in-period event claimed by its
  top-priority match via `LATERAL JOIN`, per-mode aggregation
  dispatched in SQL via a `CASE` over the per-group constant mode;
  `last_ever` runs a separate query that ignores period bounds for
  "current state" billing (e.g. seat counts). Unclaimed events fall
  through to the meter's default `rating_rule_version_id` with
  `meters.aggregation` — backward-compatible for tenants without rules.
  Local benchmark harness (`cmd/velox-bench`) baselines ~2.5k
  events/sec on dev hardware; the design doc's 50k/sec target requires
  cloud-grade Postgres + batched INSERTs, both follow-up work. HTTP
  endpoints for `meter_pricing_rules` CRUD will land in a follow-up PR
  (engine first, surface second). See
  `docs/design-multi-dim-meters.md` for the full algorithm.

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

- **Migration runner — no-transaction primitive + 0054 GIN moved to CONCURRENTLY (`0062`)** (2026-04-26) —
  Phase 3 prep (Week 9). The original 0054 created the
  `idx_usage_events_properties_gin` GIN index inline as
  `CREATE INDEX … USING GIN (properties)` — a non-concurrent build that
  holds an `AccessExclusiveLock` on `usage_events` for the whole
  duration. The populated-DB safety harness
  (`docs/migration-safety-findings.md`) bundled this lock into the
  53.5s 0054 measurement on 5M rows. CONCURRENTLY is the canonical
  fix, but `CREATE INDEX CONCURRENTLY` cannot run inside a transaction
  block, and `golang-migrate`'s postgres driver in v4.19.1 does not
  support a per-file `x-no-tx-wrap` flag (only the sqlite drivers do).
  The runner in `internal/platform/migrate/migrate.go` now detects an
  opt-out header `-- velox:no-transaction` in the first 5 lines of a
  migration file. Files with the header are pulled out of the library
  path and applied via our own `db.ExecContext` after splitting on `;`
  so each statement runs in PG autocommit. golang-migrate's
  `pg_advisory_lock` is held across our applies too, so concurrent
  replicas booting against the same DB serialize through the
  same numeric lock id whether the step is library-driven or no-tx.
  Migrations without the header are unchanged — the hybrid runner is
  inert when no pending file opts in. The GIN portion of 0054 has been
  removed from `0054_multi_dim_meters.up.sql`; new migration
  `0062_usage_events_gin_concurrent.up.sql` carries the header and
  runs `CREATE INDEX CONCURRENTLY IF NOT EXISTS …` so already-deployed
  instances that ran the pre-retrofit shape of 0054 treat 0062 as a
  no-op metadata bump. The matching down (`DROP INDEX CONCURRENTLY IF
  EXISTS …`) is also no-tx. Safety harness re-run at the small preset
  records `up,62,*ms,0` — no `AccessExclusiveLock` observed on
  `usage_events`. The roundtrip test
  (`internal/platform/migrate/roundtrip_test.go`) plus a new focused
  integration test (`notx_integration_test.go`) verify schema parity
  after up+down+up. Unit tests cover the header detector, the SQL
  splitter (single quotes, doubled-quote escapes, double-quoted
  identifiers, dollar-quoted bodies including tagged variants, line
  comments, block comments), and the advisory-lock id derivation.
  Out of scope on this PR: 0054's column rewrite (BIGINT→NUMERIC) and
  0020's 13 UNIQUE swaps — both DEFERRED in
  `docs/migration-safety-findings.md` because they need backfill
  machinery / many-step rewrites and Velox is pre-launch with near-zero
  rows in the affected tables. Re-evaluate during Phase 3 cutover.

- **Migration 0015 (fk_explicit_restrict) — eliminates 8.8s `AccessExclusiveLock` on `audit_log`** (2026-04-26) —
  Phase 3 prep (Week 9). The migration that makes every FK's `ON DELETE`
  policy explicit (60 FKs across ~30 tables) was written for an empty
  database: each `ALTER TABLE T DROP CONSTRAINT c, ADD CONSTRAINT c
  FOREIGN KEY ...` was atomic but validated every existing row under
  `AccessExclusiveLock`. On the populated-DB safety harness
  (`docs/migration-safety-findings.md`) it held an
  `AccessExclusiveLock` on `audit_log` for 8.8s up / 6.7s down at the
  medium scale (5M usage_events, 100k audit_log). Rewritten in-place to
  the standard NOT VALID + VALIDATE two-step on every FK: `DROP
  CONSTRAINT` (fast metadata) → `ADD CONSTRAINT … NOT VALID` (fast
  metadata, new rows checked) → `VALIDATE CONSTRAINT` (verifies
  existing rows under `ShareUpdateExclusiveLock`, PG 9.4+, concurrent
  INSERT/UPDATE/DELETE proceed unblocked). The whole sequence still
  runs inside golang-migrate's outer transaction — no runner changes,
  no migration split, no schema diff (the constraint shape is
  unchanged once VALIDATE completes). Re-running the safety harness at
  the small preset shows 0015 up at 0.26s with no `AccessExclusiveLock`
  observed (down from 8.8s); the down direction is symmetric. The
  roundtrip test (`internal/platform/migrate/roundtrip_test.go`) stays
  green — schema after up+down+up matches the original. The matching
  `0015_fk_explicit_restrict.down.sql` got the same NOT VALID +
  VALIDATE rewrite so rollbacks don't freeze writes either.
  Migration-safety findings doc moves 0015 out of "Top risks" into a
  new "Already fixed" subsection; remaining production blockers are
  0054 (CRITICAL, full table rewrite of `usage_events`) and 0020
  (HIGH, 13 UNIQUE rebuilds across 32 tables) — separate follow-up
  lanes.

- **Recipes API wire shape — snake_case + creates summary + preview wrapper**
  (2026-04-26) — three drifts between `docs/design-recipes.md` and the
  Week 3 implementation surfaced during Track B's first integration
  pass and are now fixed. `domain.Recipe` was missing `json:"…"` tags
  on its top-level fields, so `GET /v1/recipes`, `GET
  /v1/recipes/{key}`, and `POST /v1/recipes/{key}/preview` were
  emitting PascalCase keys (`Key`, `Version`, `Meters`,
  `RatingRules`, `DunningPolicy`, …) inconsistent with the rest of
  `/v1/*`; tags added with `omitempty` on `description` /
  `dunning_policy` / `webhook` to keep wire output tight when
  recipes don't declare those sections. `SampleData` is now `json:"-"`
  — it's a seed hint for `seed_sample_data=true`, not part of the
  public API surface. `RecipeListItem` and the new
  `RecipeDetail` wrapper carry a `creates: {meters, rating_rules,
  pricing_rules, plans, dunning_policies, webhook_endpoints}` count
  object so the picker UI renders summary chips ("1 meter · 9
  pricing rules · monthly billing") without a follow-up preview
  call. `Service.Preview` now returns
  `PreviewResult{key, version, objects: {…}, warnings: []}` per the
  design spec — previously it inlined every object array at the top
  level. `objects.dunning_policies` and `objects.webhook_endpoints`
  are 0-or-1-length slices for uniform iteration, all object slices
  default to non-nil so JSON emits `[]` not `null`, and `warnings`
  is the same shape recipes.preview spec'd for non-fatal conditions
  (currency-vs-Stripe-account mismatch, placeholder webhook URLs) —
  empty array in v1, slot in place. New `TestWireShape_SnakeCase`
  unit test pins all three contracts so future regressions trip CI
  before reaching the dashboard. No behavior change to
  `Instantiate` / `Uninstall`; data shape only.

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
- `0055_recipe_instances` — adds `recipe_instances` (one row per
  installed recipe per tenant) with `UNIQUE (tenant_id, recipe_key)`
  for idempotency and a JSONB `created_objects` blob recording the
  IDs the instantiation created. RLS-enforced via
  `current_setting('app.current_tenant_id')`.

---

Historical entries (pre-Addendum) are summarised in
`web-v2/src/pages/Changelog.tsx`. When the next release is cut, the
contents of `[Unreleased]` above will move under a new
`## [0.X.0] - YYYY-MM-DD` heading here, and a matching entry will be
curated into the public changelog page.
