import { PublicLayout, PublicPageHeader } from '@/components/PublicLayout'

type Tag = 'feature' | 'improvement' | 'fix' | 'security'

const tagClass: Record<Tag, string> = {
  feature: 'bg-primary/10 text-primary',
  improvement: 'bg-blue-500/10 text-blue-600 dark:text-blue-400',
  fix: 'bg-amber-500/10 text-amber-600 dark:text-amber-500',
  security: 'bg-emerald-500/10 text-emerald-600 dark:text-emerald-500',
}

const entries: {
  date: string
  title: string
  tag: Tag
  body: string
  bullets?: string[]
}[] = [
  {
    date: '2026-04-26',
    title: 'Plan migration tool — bulk plan swaps with preview + commit',
    tag: 'feature',
    body: 'Operators can now move a cohort of subscribers from one plan to another in a single guarded action. Three endpoints under /v1/admin/plan_migrations gated by PermSubscriptionWrite: POST /preview reuses billing.PreviewService per-customer to produce a before / after / delta table; POST /commit accepts an idempotency_key plus an effective of "immediate" (proration-aware swap of subscription_items.plan_id, stamps plan_changed_at) or "next_period" (sets pending_plan_id + pending_plan_effective_at), returning migration_id / applied_count / audit_log_id (replays of the same key short-circuit via UNIQUE (tenant_id, idempotency_key) without re-mutating); GET / paginates past migrations in reverse-chronological order. The dashboard ships at /plan-migrations with a three-step flow.',
    bullets: [
      'Cohort selection: customer_filter.type="all" picks every active subscription on from_plan_id; type="ids" narrows to a list. Tag-based filters are reserved (the wire shape accepts them so frontend mocks compile, but the service rejects with code filter_type_unsupported pending a customer-tag schema).',
      'Cross-currency guard: the service rejects a from→to plan pair with mismatched currency at validateCommon time with code currency_mismatch — a before/after delta in different currencies would be apples-to-oranges, and the dashboard surfaces the error inline before the operator hits Preview.',
      'History table plan_migrations (migration 0058) snapshots the cohort summary at commit: customer_filter JSONB always-object, totals JSONB always-array of {currency, before_amount_cents, after_amount_cents, delta_amount_cents}, applied_by + applied_by_type from the auth context, FORCE-RLS on (tenant_id, livemode), BEFORE-INSERT set_livemode trigger so test-mode commits land livemode=false without callers setting the column.',
      'Audit log gains TWO new event types: plan.migration_committed (one cohort summary entry per commit; metadata carries from_plan_id, to_plan_id, effective, customer_filter, applied_count, totals, idempotency_key) and subscription.plan_changed (per-customer entry so CS reps can answer "why did my plan change?" tickets without joining tables).',
      'Three wire-shape regression tests pin the always-snake_case contract: TestWireShape_PlanMigrationPreview_SnakeCase asserts previews[].{customer_id, current_plan_id, target_plan_id, before, after, delta_amount_cents, currency} and totals[].{currency, before_amount_cents, after_amount_cents, delta_amount_cents}; commit and list pin migration_id / applied_count / audit_log_id and migrations[] / next_cursor respectively. No PascalCase leakage; warnings is always an array (never null); customer_filter is always an object.',
      'Three-step UI: configure (pick from / to / filter / effective) → preview (cohort table with delta column highlighting + warnings panel + cohort total) → commit (modal with auto-generated idempotency key from crypto.randomUUID, editable for retry scenarios). A recent-migrations sidebar surfaces the last 10 runs with their effective mode and applied count.',
    ],
  },
  {
    date: '2026-04-26',
    title: 'Self-host paper artifacts — Helm chart + Terraform AWS module',
    tag: 'feature',
    body: 'Two more deploy targets land alongside the Compose path: a Helm chart at deploy/helm/velox/ for users who already operate Kubernetes (kind / k3s / EKS / GKE / AKS), and a Terraform module at deploy/terraform/aws/ for one-shot AWS installs. Both are paper artifacts — structurally validated (helm lint and terraform validate pass clean) but not yet drilled on a real AWS account, which is a separate user-decision lane (cost: ~$30-50/mo at default sizing, ~$1-2 for an apply/destroy validation run). Both pin to the same env-var schema the Compose .env.example exercises; no invented keys.',
    bullets: [
      'Helm chart: single-replica deployment by default (Postgres advisory-lock leader election makes multi-replica safe but not required), Service / ConfigMap / Secret / ServiceAccount, optional Ingress and HorizontalPodAutoscaler templates gated behind ingress.enabled and autoscaling.enabled (both default off). No bundled Postgres — bring your own (RDS / Cloud SQL / Supabase / Neon) via externalDatabase.url or the split DB_HOST/DB_PORT/DB_NAME/DB_USER shape with password in the Secret.',
      'Helm chart supports External Secrets Operator / sealed-secrets users via secrets.existingSecret — point at a pre-existing Secret and the chart skips its own Secret template entirely. helm template renders 5 manifests under defaults, 7 with ingress + autoscaling on; both parse through yaml.safe_load_all clean.',
      'Terraform AWS module: single-VPC + single-EC2 (t3.small) + RDS Postgres (db.t3.small) + S3 backup bucket. Architecture is locked at NOT EKS, NOT autoscaling, NOT multi-AZ — boring + cheap is the v1 posture. Two AZ subnet layout because RDS demands ≥2 AZs in a subnet group; EC2 lives in public-a only, RDS in the private subnet group, RDS SG ingress restricted to the EC2 SG.',
      'S3 backup bucket ships hardened: versioning + AES256 SSE + Block Public Access on + lifecycle rule tiering base/ to Glacier Instant Retrieval at 30 days, Deep Archive at 90 days, expires at 365 days; wal/ expires at 14 days. EC2 IAM instance profile has read/write to the bucket plus AmazonSSMManagedInstanceCore for Session Manager fallback. IMDSv2-required, encrypted root volume.',
      'EC2 cloud-init installs Docker + the Compose plugin, clones the repo at velox_repo_ref, generates a .env with random VELOX_ENCRYPTION_KEY and VELOX_BOOTSTRAP_TOKEN plus a DATABASE_URL pointing at the RDS endpoint, then runs docker compose up -d nginx velox-api from deploy/compose/. The Terraform module reuses the Compose lane\'s work — does not reinvent the stack.',
      'docs/self-host.md replaces the earlier "coming soon" placeholders with real cross-links; docs/self-host/postgres-backup.md gains a section on wiring the Terraform-provisioned S3 bucket as the WAL archive target via the user-data-installed /usr/local/bin/velox-wal-ship.sh wrapper. Both new modules ship with copy-pasteable READMEs walking install / upgrade / destroy + cost estimate + the explicit "what this does NOT do" list (no Route 53, no TLS, no multi-AZ — all v2 follow-ups).',
    ],
  },
  {
    date: '2026-04-26',
    title: 'Self-host quickstart — Docker Compose stack + Postgres backup recipe',
    tag: 'feature',
    body: 'Five minutes from a fresh VM to a working Velox tenant. deploy/compose/ ships a three-service stack (postgres + velox-api + nginx) with RUN_MIGRATIONS_ON_BOOT=true so the first boot applies all migrations and starts serving — no separate setup step. The .env.example mirrors the binary\'s real env-var schema (internal/config/config.go plus 18 per-package os.Getenv callsites); three required keys (POSTGRES_PASSWORD, VELOX_ENCRYPTION_KEY, VELOX_BOOTSTRAP_TOKEN), everything else is commented with safe defaults. APP_ENV=production is the default so the encryption-key fatal check, secure cookies, and HSTS are on the moment a real operator runs it. Backup story is a real PITR recipe (pg_basebackup + WAL archiving) with a quarterly restore drill — not pg_dump-and-pray.',
    bullets: [
      'docs/self-host.md is the new top-level landing page: pick the install shape (Compose today; Helm + Terraform AWS module flagged for the follow-up Week 9 lane, no fake links), required env-vars, sizing table for eval / single-tenant prod / multi-replica, TLS guidance.',
      'nginx is the reverse proxy (battle-tested over bespoke), HTTP-only on :80 so the local quickstart works with curl. Production TLS guidance: managed LB (ALB / Cloudflare) in front, or certbot on the host — both routes documented in the README.',
      'postgres-init.sql creates the non-superuser velox_app runtime role so RLS is actually enforced. Superusers and DB owners bypass policies; without the role, the binary falls back to admin with a loud warning per cmd/velox/main.go:openAppPool. Documented troubleshooting steps cover the upgrade-existing-volume case where init.d won\'t re-run.',
      'docs/self-host/postgres-backup.md is a tested PITR recipe — pg_basebackup + WAL archiving, S3 layout, retention recommendation (7 daily / 4 weekly / 12 monthly across hot / cool / cold S3 tiers), and an explicit quarterly restore-drill procedure. Every Postgres reference links to the real postgresql.org/docs/16/ manual chapter.',
      'velox-api healthcheck calls the binary\'s own version subcommand because the image is distroless (no shell, no curl). nginx in front does the HTTP-level liveness probe via /health; readiness through /health/ready surfaces DB + scheduler health.',
      'Helm chart, Terraform AWS module, and a non-Velox-engineer cold-install drill on real AWS are deferred to a follow-up Week 9 lane — explicitly called out in docs/self-host.md so operators don\'t hunt for missing pages.',
    ],
  },
  {
    date: '2026-04-26',
    title: 'Billing alerts — Stripe Tier 1 alert thresholds with webhook delivery',
    tag: 'feature',
    body: 'Operator-configurable thresholds that fire a webhook + dashboard notification when a customer\'s cycle spend (or per-meter usage) crosses a limit. Four endpoints under /v1/billing/alerts (create, get, list, archive); a background evaluator leader-elects via Postgres advisory lock, scans armed alerts, aggregates the customer\'s current cycle through the same LATERAL JOIN the cycle scan uses (so alert math = invoice math by construction), and on threshold cross fires billing.alert.triggered through the webhook outbox in the same tx as the alert state mutation. UNIQUE (alert_id, period_from) gives per-period idempotency across replica races. Mounted under PermInvoiceRead / PermInvoiceWrite.',
    bullets: [
      'Two recurrence modes: one_time transitions to a terminal triggered status (fires once, ever); per_period transitions to triggered_for_period each cycle and re-arms when the next cycle begins.',
      'Threshold contract: exactly one of amount_gte (BIGINT cents) or usage_gte (decimal-as-string per ADR-005) — DB CHECK + service validation both enforce. Wire shape always emits both keys with one as null so the dashboard reads both fields without a conditional null guard.',
      'Filter contract: optional meter_id (alert fires on aggregate when omitted), plus optional dimensions object (always-object {} idiom — no null guard) for multi-dim meters. Service-layer validation: ≤16 dimension keys, scalar values only, dimensions only valid when meter_id is set.',
      'Atomicity guarantee: trigger insert + alert state mutation + outbox enqueue all commit in one tx. If the outbox enqueue fails (simulated in TestEvaluator_AtomicityOnRollback), the entire tx rolls back so no double-emission and no half-fired state survives a partial failure.',
      'Mode-aware tables (billing_alerts, billing_alert_triggers) ship with the standard tenant + livemode RLS policy from migration 0020 and the BEFORE INSERT livemode-from-session trigger from migration 0021. New regression entries in TestRLSIsolation_AllModeAwareTablesHaveLivemodePredicate.',
      '24 unit tests + 9 integration tests against real Postgres pin the behavior: one-time-fire, per-period-fire-and-rearm, double-fire-idempotent (UNIQUE constraint), archived-skipped, below-threshold-no-fire, no-subscription-warning, multi-tenant RLS isolation, atomicity-on-rollback, and a TestWireShape_SnakeCase merge gate.',
    ],
  },
  {
    date: '2026-04-26',
    title: 'Billing thresholds — Stripe Tier 1 hard-cap mid-cycle finalize',
    tag: 'feature',
    body: 'PATCH /v1/subscriptions/{id}/billing-thresholds configures a per-subscription hard cap on the running cycle subtotal (amount_gte, integer cents) and/or per-item quantity caps (item_thresholds[] with usage_gte as a NUMERIC(38,12) decimal string). When usage pushes the running total past any configured cap, the engine emits an early-finalize invoice with billing_reason=threshold mid-cycle — same charge-and-dunning chain a natural-cycle invoice goes through, just before the period boundary. reset_billing_cycle (default true, matching Stripe) controls whether the cycle resets after fire so the next bill starts from fire-time, or whether the original cycle continues with residual usage.',
    bullets: [
      'Engine path reuses the create_preview previewWithWindow over the partial cycle — running subtotal is the same figure the cycle scan would bill (preview math == invoice math, by construction).',
      'Per-item caps sum quantities across each item\'s plan meters via usage.AggregateByPricingRules — the same priority+claim LATERAL JOIN the cycle scan and customer-usage already use, so multi-dim tenants get the canonical aggregation.',
      'Idempotency seam: a partial unique index on invoices(tenant, sub, billing_period_start) WHERE billing_reason=threshold makes a re-tick after a transient failure a no-op — the second insert lands on errs.ErrAlreadyExists and short-circuits without losing the row.',
      'Skips terminal/trialing subs and pause_collection rows so the scan doesn\'t emit a draft that can\'t be charged. Webhook event subscription.threshold_crossed fires before the optional cycle reset.',
      'Service rejects multi-currency subs at the handler layer (only layer with a PlanReader), terminal subs at the service layer, and foreign / duplicate / blank subscription_item_id and non-numeric / negative usage_gte across both — the store sees only validated input.',
      '12 service-validation unit cases + 7 evaluateThresholds/ScanThresholds unit cases + 6 integration cases against real Postgres (amount-cross fires early, item-cross fires, re-tick idempotent, below-threshold no-fire, no-config-skipped, reset_billing_cycle=false) + 3 wire-shape cases on JSON contracts.',
    ],
  },
  {
    date: '2026-04-26',
    title: 'Create-preview endpoint — Stripe Tier 1 invoice.upcoming parity',
    tag: 'feature',
    body: 'POST /v1/invoices/create_preview returns the same totals the cycle scan would bill — without writing a row. Multi-dim aware by construction: the preview path runs through usage.AggregateByPricingRules (LATERAL JOIN with priority + claim across the five aggregation modes), so a meter with cached-input vs uncached vs output rules previews into the same buckets the invoice will land in. Mounted under PermInvoiceRead. Both the in-app debug surface (/v1/billing/preview/{id}) and the new Tier 1 surface return one shape so TypeScript clients and dashboards share one type.',
    bullets: [
      'No-writes guarantee: preview never inserts an invoice or line; integration test counts invoice + invoice_line rows before/after and asserts zero diff.',
      'Period defaults to the customer\'s active subscription cycle; explicit ?from=&to= bounds (RFC 3339) override; partial bounds are rejected (must be both or neither). customer_has_no_subscription returns as a coded error so the UI prompts for an explicit window.',
      'Subscription resolution: explicit subscription_id is honored and cross-customer subscription IDs are rejected with 404; otherwise the engine picks the customer\'s active/trialing subscription with the latest cycle start.',
      'Always-array totals shape: response carries totals: [{currency, cents}] even for single-currency tenants, with multi-currency just adding entries — same wire shape as customer-usage so dashboards iterate uniformly.',
      'Route ordering: /invoices/create_preview is mounted before /invoices/{id} so chi picks the more-specific pattern (otherwise "create_preview" would be captured as an invoice ID).',
      '16 unit tests + 7 integration tests pin the contract: single-meter parity (1000c for 100 events × qty=10 × 1¢), multi-dim dimension echo (3500c across two rules), no-writes row-count diff, cross-tenant 404 via RLS, no-sub coded error, cross-customer subscription rejection, plus a TestWireShape_SnakeCase regression test that asserts all 9 top-level keys are snake_case and lines/totals/warnings always marshal as arrays (never null).',
    ],
  },
  {
    date: '2026-04-26',
    title: 'Customer usage endpoint — one call answers "what did this customer use?"',
    tag: 'feature',
    body: 'GET /v1/customers/{id}/usage composes customer + active subscriptions + pricing into a single response: per-meter quantities, per-rule cents, multi-currency totals, and the period that produced them. Same code path the cycle scan uses to bill — dashboard math is invoice math, by construction. Default period follows the customer\'s current cycle; explicit ?from=&to= bounds (RFC 3339) override, capped at one year. Mounts as a sibling under /v1/customers/{id}, behind PermUsageRead.',
    bullets: [
      'Per-meter aggregate echoes the rule\'s match expression (canonical pricing identity), not the observed event values — so dimension_match in the response is the bucket the cycle scan would price into.',
      'Always-array totals shape: response carries totals: [{currency, cents}] even for single-currency tenants, so clients iterate uniformly. Multi-currency subs (rare today, real for cross-region tenants) just add entries.',
      'Period resolution honors the customer\'s active subscription cycle by default; partial bounds (only from or only to) anchor on it; missing-cycle + missing-bounds returns customer_has_no_subscription so the dashboard can prompt for an explicit window.',
      'Cross-tenant isolation by RLS: tenant B\'s key against tenant A\'s customer ID 404s at the customer lookup — no leak via the usage scan.',
      'Narrow per-domain interfaces (CustomerLookup, SubscriptionLister, PricingReader) keep the new CustomerUsageService composable from the existing usage.Service without breaking its callers; tests fake the seams without touching the DB.',
      '9 unit tests + 4 integration tests pin the contracts: single-meter parity with the cycle scan (1000c for 100 events × qty=10 × 1¢), multi-dim dimension echo (3500c across two rules), cross-tenant 404, and no-sub explicit-window recovery.',
    ],
  },
  {
    date: '2026-04-26',
    title: 'Recipes — point-and-click pricing installation',
    tag: 'feature',
    body: 'The recipes feature gets a discoverable UI. /recipes lists the five built-in templates with a creates-summary on each card; the configure dialog walks operators through overrides → preview → install in three clicks. Same atomic POST /v1/recipes/{key}/instantiate under the hood.',
    bullets: [
      'Recipe cards show key + version, summary, and a "creates" chip strip (e.g. 1 meter · 9 pricing rules · 1 plan). An "Installed" badge surfaces tenants who have already instantiated.',
      'Configure dialog renders the overrides form from the recipe\'s own schema — string / number / boolean inputs, with enum, max_length, and pattern honored from the YAML.',
      'Preview button hits POST /v1/recipes/{key}/preview and renders generated objects + warnings inline so the operator sees exactly what will land before committing.',
      'Install navigates to the first created plan so the new catalog is one click away.',
      'Sidebar entry under Configuration with the Sparkles icon. Onboarding wizard step 1 also fetches /v1/recipes live and deep-links into the picker.',
    ],
  },
  {
    date: '2026-04-26',
    title: 'Recipes API wire shape — snake_case + creates summary + preview wrapper',
    tag: 'fix',
    body: 'Three drifts between the recipes API design doc and the Week 3 implementation are fixed so the picker UI lights up cleanly: PascalCase JSON keys are now snake_case (matching the rest of /v1/*), each list/detail entry carries a creates: {meters, rating_rules, pricing_rules, plans, dunning_policies, webhook_endpoints} count summary, and POST /v1/recipes/{key}/preview now wraps its response as {key, version, objects: {…}, warnings: []} per the spec. Data shape only — no behavior change to instantiate / uninstall.',
    bullets: [
      'JSON tags added to domain.Recipe — keys are key, version, name, summary, description, overridable, meters, rating_rules, pricing_rules, plans, dunning_policy, webhook (was PascalCase). Optional sections use omitempty so wire output stays tight.',
      'GET /v1/recipes and GET /v1/recipes/{key} now include a creates summary so the picker UI renders "1 meter · 9 pricing rules · monthly billing" chips without a follow-up preview call.',
      'POST /v1/recipes/{key}/preview returns {key, version, objects: {meters, rating_rules, pricing_rules, plans, dunning_policies, webhook_endpoints}, warnings: []} per the spec — previously inlined every array at the top level. dunning_policies and webhook_endpoints are 0-or-1-length slices for uniform iteration.',
      'New TestWireShape_SnakeCase regression test pins all three contracts so future drift trips CI before reaching the dashboard.',
    ],
  },
  {
    date: '2026-04-26',
    title: 'Multi-dimensional meters in the dashboard',
    tag: 'feature',
    body: 'Two operator surfaces light up the multi-dim meter engine. Meter detail pages now expose dimension-matched pricing rules end-to-end, and the usage events log surfaces dimensions inline with key=value chips and a dimension filter. Operators can build, inspect, and audit AI-style multi-rule pricing without touching the API.',
    bullets: [
      'Meter detail (/meters/:id) gets a "Dimension-matched rules" section with a chips-table view of each rule\'s dimension_match, aggregation mode, priority, and rating rule.',
      '"Add rule" dialog walks operators through dimension key/value pairs, rating-rule selector, and one of five aggregation modes (sum, count, last_during_period, last_ever, max). Typed-confirm delete on each rule.',
      'Usage events (/usage) gets a Dimensions column that conditionally appears when at least one event in view carries dimensions. New key=value text filter narrows the list.',
      'CSV export now includes the full dimensions JSON column, and stats / meter-breakdowns read the decimal-precision quantity field with no truncation.',
      'Default pricing rule on each meter is now labeled clearly as the fallback for events not claimed by a higher-priority dimension-matched rule.',
    ],
  },
  {
    date: '2026-04-26',
    title: 'Pricing recipes — one-call billing setup',
    tag: 'feature',
    body: 'Five built-in recipes (anthropic_style, openai_style, replicate_style, b2b_saas_pro, marketplace_gmv) collapse a multi-day Stripe-Billing-style onboarding into a single API call. POST /v1/recipes/{key}/instantiate atomically builds the full graph — meters, multi-dim pricing rules, plan, dunning policy, webhook endpoint — under one transaction; partial state never reaches the tenant. Designed to make the multi-dimensional meter engine immediately usable: a 12-rule anthropic_style setup goes from ~30 manual API calls to one POST.',
    bullets: [
      'Five v1 recipes: anthropic_style (Claude 3.5 Sonnet / Opus / Sonnet / Haiku, input/output, cached-input via priority=200 rule), openai_style (GPT-4 / 4o / 4o-mini / 3.5-turbo + embeddings — 14 rules), replicate_style (per-second GPU billing for a100/a40/t4/cpu), b2b_saas_pro (seats with included tier + storage GB), marketplace_gmv (package-billing GMV take rate + per-transaction fee).',
      'POST /v1/recipes/{key}/preview renders the full would-be graph with zero DB writes — cheap enough to call on every override-form keystroke. Powers the dashboard\'s review-and-instantiate dialog.',
      'Atomic graph build under one tenant-scoped transaction: rating rules → meters → multi-dim pricing rules → plan → optional dunning policy → optional webhook endpoint → instance row. Mid-graph failure rolls back every cross-domain write — no orphan meters or rules to clean up by hand.',
      'Idempotent on (tenant_id, recipe_key): a second instantiate returns the existing instance ID via 409 ErrAlreadyExists. Force re-instantiation reserved for v2 — the API accepts the field today and returns InvalidState, keeping the contract stable when force lands.',
      'Per-instantiation overrides (currency, plan_name, plan_code, plus recipe-specific knobs like included_seats) flow through templated YAML with strict missing-key handling — typos fail at preview rather than silently drop.',
      'Uninstall removes the recipe-instance row only; the resources the recipe created (plans, meters, dunning policy, webhook endpoint) stay — operators own them once they exist, and silent cascade could lose live billing data.',
    ],
  },
  {
    date: '2026-04-25',
    title: 'Trial extension (Stripe parity)',
    tag: 'feature',
    body: 'Operators can now push a trialing subscription\'s trial_end_at later — useful when sales/ops grant a customer extra trial time before the auto-flip-and-bill fires. Pairs with End trial now (the early-end direction): together they cover both sides of the trial-window adjustment that Stripe exposes through subscription.update.',
    bullets: [
      'POST /v1/subscriptions/{id}/extend-trial with {trial_end:<timestamp>}; new value must be in the future and strictly after the current trial_end_at.',
      'Atomic UPDATE WHERE status=\'trialing\' closes the race between the operator extension and the cycle-scan auto-flip — only one wins.',
      'Extension-only by design: shrinking would bypass the operator-intent that End trial now captures, so the service rejects values at-or-before the current trial_end_at.',
      'New webhook: subscription.trial_extended with triggered_by="operator".',
      'Dashboard surfaces an "Extend trial" button on trialing subs; the dialog seeds with current + 7 days.',
    ],
  },
  {
    date: '2026-04-25',
    title: 'Trial state machine (Stripe parity)',
    tag: 'feature',
    body: 'Subscriptions started with a trial now enter a real status="trialing" state, distinct from active. The billing engine runs a proper trial state machine: while the trial is active it skips billing and advances the cycle; when the trial elapses it atomically flips to active, stamps activated_at, and bills the period. Operators can also end the trial early from the dashboard.',
    bullets: [
      'New status value "trialing" on subscriptions; Service.Create routes any sub with trial_days > 0 into trialing instead of active.',
      'Cycle scan now sweeps both active and trialing — auto-flip to active fires subscription.trial_ended with triggered_by="schedule" the first cycle visit on or after trial_end_at.',
      'POST /v1/subscriptions/{id}/end-trial flips trialing → active immediately and fires subscription.trial_ended with triggered_by="operator" so analytics can split scheduled vs sales-driven trial-ends.',
      'Atomic UPDATE WHERE status=\'trialing\' closes the race between scheduler auto-flip and operator early-end — only one wins, and activated_at is COALESCE\'d so the first-set value is preserved on retries.',
      'Dashboard SubscriptionDetail surfaces an "End trial now" button on trialing subs.',
    ],
  },
  {
    date: '2026-04-25',
    title: 'Pause collection (Stripe parity)',
    tag: 'feature',
    body: 'Subscriptions can now have collection paused as a state distinct from a hard pause: the cycle keeps running, but invoices generate as drafts and skip finalize/charge/dunning until resumed. Use this for collections holds, payment-method updates, or temporary courtesy without losing usage continuity. Stripe-equivalent pause_collection field; v1 supports the keep_as_draft mode.',
    bullets: [
      'PUT /v1/subscriptions/{id}/pause-collection with {behavior:"keep_as_draft", resumes_at?:<timestamp>}; DELETE /v1/subscriptions/{id}/pause-collection clears it.',
      'Auto-resume: when resumes_at passes, the cycle scan clears the pause and fires subscription.collection_resumed with triggered_by="schedule" so analytics can distinguish from operator-triggered resume.',
      'Dashboard pause button now opens a "Pause subscription" (hard freeze) vs "Pause collection only" choice; a blue banner with one-click Resume surfaces any active collection pause.',
      'New webhooks: subscription.collection_paused and subscription.collection_resumed.',
      'Distinct from the hard pause (status=paused): hard pause halts metering and billing entirely; collection pause keeps the cycle but suppresses charges. Pick by intent — usage hold vs collections hold.',
    ],
  },
  {
    date: '2026-04-25',
    title: 'Scheduled subscription cancellation (Stripe parity)',
    tag: 'feature',
    body: 'Subscriptions can now be canceled at the end of the current billing period instead of immediately, matching Stripe\'s cancel_at_period_end and cancel_at fields. The current period bills as normal; the engine flips the sub to canceled at the boundary and emits subscription.canceled with triggered_by="schedule". The action is reversible until it fires.',
    bullets: [
      'POST /v1/subscriptions/{id}/schedule-cancel with {at_period_end:true} or {cancel_at:<timestamp>}; DELETE /v1/subscriptions/{id}/scheduled-cancel undoes it.',
      'Dashboard cancel button now opens a "at period end" vs "immediately" choice; a banner with one-click Undo surfaces any active schedule.',
      'New webhooks: subscription.cancel_scheduled and subscription.cancel_cleared. The terminal subscription.canceled event fires with triggered_by="schedule" so analytics can distinguish scheduled from immediate cancels.',
      'Test-clock parity: canceled_at honors the subscription\'s test clock so time-travel tests land deterministic timestamps.',
    ],
  },
  {
    date: '2026-04-24',
    title: 'Design-partner readiness: hosted invoice page, branded emails, webhook rotation grace',
    tag: 'feature',
    body: 'Five pre-invite blockers shipped as one batch, each anchored to an explicit industry reference (Stripe hosted_invoice_url, Stripe-Signature multi-v1 rotation, multipart/alternative branded email). Velox is now a credible substrate for a design partner to run real billing through.',
    bullets: [
      'Hosted invoice page at a public tokenized URL — Stripe-equivalent hosted_invoice_url. Mobile-first, tenant-branded, Stripe Checkout for Pay, PDF download, state-gated Pay for paid/voided invoices. Operator rotate endpoint + "Copy Link" / "Rotate" dashboard actions.',
      'Customer-facing emails render as multipart/alternative with tenant logo, brand color, support link, and a primary CTA pointing at the hosted invoice page. Six emails covered: invoice-ready, receipt, dunning warning, dunning escalation, payment failed, payment update request.',
      'Webhook signing-secret rotation now runs with a 72-hour dual-signing window — outbound events carry both the new and previous signatures in Velox-Signature (Stripe multi-v1 format) so receivers can stage a verifier deploy without a production outage.',
      'Subscription detail page gains an Activity timeline sourced from the audit log — lifecycle events (create, activate, pause, resume, cancel, plan/quantity changes) in one chronological feed. Matches the invoice payment-activity panel.',
      'SMTP permanent-failure (5xx) responses flag the customer\'s email_status as bounced, surface a red Bounced badge on the customer page, and fire a customer.email_bounced webhook event. Async NDR / SES / SendGrid webhooks plug into the same seam later.',
    ],
  },
  {
    date: '2026-04-23',
    title: 'Coupons v2: customer-scoped discounts and apply-to-draft',
    tag: 'feature',
    body: 'Coupons can now be attached to a customer and auto-apply to new invoices, matching the Stripe customer.discount model. Operators can also apply a coupon to an already-issued draft invoice; Velox recomputes tax atomically against the new subtotal and emits invoice.coupon.applied.',
    bullets: [
      'New customer_discounts table with one-active-at-a-time invariant per customer.',
      'POST /customers/{id}/coupons and DELETE /customers/{id}/coupons/{code}.',
      'POST /invoices/{id}/apply-coupon with header-or-body Idempotency-Key.',
      'Precedence rule when both subscription and customer have a coupon: subscription wins (Stripe parity).',
    ],
  },
  {
    date: '2026-04-18',
    title: 'Phase 2 hardening complete',
    tag: 'improvement',
    body: 'All items across Waves 0–5 of the phase2 hardening plan shipped: security (RLS strengthening, secret encryption), correctness (tax at finalize, real end-of-period plan change, idempotency 4xx/5xx caching), reliability (transactional outbox, scheduler advisory lock, dunning breaker), and UI (skeletons, empty states, URL state, form error injection, expiry badges, shared primitives).',
    bullets: [
      'Transactional outbox for webhook and email dispatch — no lost events on crash.',
      'Scheduler advisory locks prevent double-runs across horizontally-scaled deployments.',
      'Audit log append-only trigger enforces tamper-evidence at the database level.',
      'Online-safe migrations with round-trip (up/down) test coverage.',
    ],
  },
  {
    date: '2026-04-10',
    title: 'Webhook secret encryption at rest',
    tag: 'security',
    body: 'Webhook signing secrets are now encrypted with AES-256-GCM before persistence. Only the last-4 suffix is shown in the dashboard once the secret is revealed at creation. Rotation issues a new plaintext secret once and immediately invalidates the old one.',
  },
  {
    date: '2026-04-05',
    title: 'Credit notes with PDF',
    tag: 'feature',
    body: 'Operators can issue credit notes against finalized invoices with reason codes (duplicate, fraudulent, product_unsatisfactory, order_change, other). Partial and full refunds are supported; applied credits flow back to amount_due and optionally trigger a Stripe refund.',
  },
  {
    date: '2026-03-28',
    title: 'Dunning with retry policy and breaker',
    tag: 'feature',
    body: 'Configurable dunning policies per tenant: retry cadence (hours/days), escalation (email, pause, cancel), maximum attempts. A circuit breaker halts retries for tenants with persistently failing payment providers to avoid log-spam and quota exhaustion.',
  },
  {
    date: '2026-03-20',
    title: 'Tax at finalize (manual and Stripe Tax)',
    tag: 'feature',
    body: 'Invoice finalization now runs tax calculation against (subtotal − discount) rather than gross subtotal. Supports three tax modes per tenant: none, manual (flat rate with inclusive/exclusive toggle), and Stripe Tax (upstream calculation committed at finalize). Tax breakdown per jurisdiction is persisted and rendered on invoice PDFs.',
  },
]

export default function ChangelogPage() {
  return (
    <PublicLayout>
      <PublicPageHeader
        eyebrow="Platform"
        title="Changelog"
        description="Everything user-visible we ship, in reverse chronological order. The full engineering log lives in CHANGELOG.md on GitHub; this page curates the rollups worth reading."
      />
      <div className="max-w-3xl mx-auto px-6 py-12 space-y-10">
        {entries.map((entry) => (
          <article key={entry.date + entry.title} className="border-l-2 border-border pl-6 relative">
            <span className="absolute -left-[5px] top-2 w-2 h-2 rounded-full bg-primary" />
            <div className="flex items-center gap-3 mb-2">
              <time className="text-xs text-muted-foreground font-mono">{entry.date}</time>
              <span
                className={`text-[10px] uppercase tracking-wide px-2 py-0.5 rounded font-medium ${tagClass[entry.tag]}`}
              >
                {entry.tag}
              </span>
            </div>
            <h2 className="text-lg font-semibold tracking-tight text-foreground mb-2">{entry.title}</h2>
            <p className="text-muted-foreground leading-relaxed text-[15px]">{entry.body}</p>
            {entry.bullets && (
              <ul className="mt-3 space-y-1 text-sm text-muted-foreground">
                {entry.bullets.map((b, i) => (
                  <li key={i} className="flex gap-2">
                    <span className="text-primary/60">•</span>
                    <span className="leading-relaxed">{b}</span>
                  </li>
                ))}
              </ul>
            )}
          </article>
        ))}
      </div>
    </PublicLayout>
  )
}
