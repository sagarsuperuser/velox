# Architecture Decision Records

This directory holds Velox's Architecture Decision Records (ADRs) —
short docs capturing one architectural decision each. Format follows
[Michael Nygard's 2011 convention](https://cognitect.com/blog/2011/11/15/documenting-architecture-decisions):
Title / Date / Status / Context / Decision / Alternatives / Consequences.

ADRs are written when a decision is **worth re-litigating later** —
either because reasonable alternatives exist, because the constraints
behind the choice will likely shift, or because the decision shapes
multiple downstream design choices. They are **not** prose summaries
of every change; routine refactors and bug fixes belong in commit
messages + CHANGELOG.md, not here.

## Conventions

- **Numbering** is sequential; never re-used. Superseded ADRs stay in
  the directory with their `**Status:** Superseded by ADR-XXX` line —
  the historical context is the whole point.
- **Status** is one of: `Proposed`, `Accepted`, `Superseded by ADR-XXX`,
  `Deprecated`. Amendments to an accepted ADR are noted in the status
  line (e.g. `Accepted (amended YYYY-MM-DD — short reason)`).
- **Date** is when the ADR was first written. Amendment dates go in
  the status line and inline section headers.
- **Multi-platform claims** ("Stripe-parity", "industry standard")
  must quote verified source lines from at least 2-4 reference
  platforms (per `feedback_verify_stripe_parity_claims` in
  `~/.claude/.../memory/`). Single-platform spot-checks aren't research.
- New ADRs start from [TEMPLATE.md](TEMPLATE.md).
- **Deferred work** an ADR deliberately scopes out — with a written revisit
  trigger in its *Consequences* — gets a thin pointer row in
  [Open follow-ups](#open-follow-ups-deferred) below. The rationale stays in the
  ADR (no duplication → nothing to drift); the table only makes open deferrals
  discoverable in one place. Remove the row when the follow-up ships.

## Index

| # | Date | Status | Title |
|---|---|---|---|
| [001](001-paymentintent-only-stripe.md) | 2026-04-15 | Accepted | PaymentIntent-Only Stripe Integration |
| [002](002-per-domain-package-architecture.md) | 2026-04-15 | Accepted | Per-Domain Package Architecture |
| [003](003-postgresql-rls-multi-tenancy.md) | 2026-04-15 | Accepted | PostgreSQL Row-Level Security for Multi-Tenancy |
| [004](004-event-sourced-credit-ledger.md) | 2026-04-15 | Accepted | Event-Sourced Credit Ledger |
| [005](005-integer-cents-for-money.md) | 2026-04-15 | Accepted | Integer Cents for Money |
| [006](006-background-scheduler-vs-message-queue.md) | 2026-04-15 | Accepted | Background Scheduler vs. Message Queue |
| [007](007-revert-to-api-key-dashboard-auth.md) | 2026-04-29 | **Superseded by ADR-011** | Revert Dashboard to API-Key Auth |
| [008](008-session-from-api-key.md) | 2026-04-29 | **Superseded by ADR-011** | Dashboard Session Cookies Minted From API Keys |
| [009](009-invoice-attention.md) | 2026-04-30 | Accepted | Unified Invoice Attention Surface |
| [010](010-tenant-timezone-model.md) | 2026-05-01 | Accepted | Tenant Timezone Model |
| [011](011-email-password-auth-and-clean-api-keys.md) | 2026-05-01 | Accepted | Email/Password Dashboard Auth, Pure-CRUD API Keys |
| [012](012-day-grade-calendar-billing.md) | 2026-05-01 | Accepted | Day-Grade Calendar Billing & Period Boundary Snapping |
| [013](013-invoice-attention-collection-state.md) | 2026-05-01 | Accepted | Invoice Attention — Honest Collection-State Surface |
| [014](014-sso-direction-embedded-oidc-saml.md) | 2026-05-02 | Accepted | SSO Direction — Embedded OIDC/SAML, No SaaS Auth Vendor |
| [015](015-test-clock-async-catchup.md) | 2026-05-04 | Accepted | Test-clock advance runs catchup asynchronously |
| [016](016-test-clock-soft-delete.md) | 2026-05-04 | Accepted | Test clocks are soft-deleted with cascade-cancel of pinned subs |
| [017](017-tax-retry-and-auto-finalize.md) | 2026-05-04 | Accepted | Background tax-retry reconciler + auto-finalize on success |
| [018](018-test-clock-retry-and-failure-reason.md) | 2026-05-04 | Accepted | Test-clock retry advance + persisted failure reason |
| [019](019-stripe-connect-flushes-stuck-tax.md) | 2026-05-04 | Accepted | Stripe re-connect flushes stuck tax invoices |
| [020](020-invoice-timeline-coalesce-and-card-detail.md) | 2026-05-04 | Accepted | Invoice timeline coalesces redundant rows and surfaces the charged card |
| [021](021-hosted-invoice-pay-flow-saves-pm.md) | 2026-05-03 | Accepted | Hosted-invoice Pay flow saves the card to the customer's Stripe customer |
| [022](022-contextual-checkout-return-urls.md) | 2026-05-04 | Accepted | Stripe Checkout return URLs are contextual, not global |
| [023](023-post-decline-rendering-cleanup.md) | 2026-05-05 | Accepted | Post-decline rendering — one banner, distinct emails, interactive suppression |
| [024](024-activity-timeline-design-deferred.md) | 2026-05-05 | Accepted | Activity timeline — keep multi-source assembly; promote canonical primitives only on trigger |
| [025](025-attention-detail-vs-provider-response.md) | 2026-05-05 | Accepted | Attention banner separates Velox's classification from upstream provider responses |
| [026](026-error-boundary-sanitization.md) | 2026-05-05 | Accepted | HTTP error responses are sanitized at a single boundary |
| [027](027-customer-level-test-clock.md) | 2026-05-05 | Accepted | Test clocks attach at the customer level (Stripe parity) |
| [028](028-billing-engine-period-loop-and-disjoint-flows.md) | 2026-05-05 | Accepted | Billing engine — per-sub period loop + disjoint flows for catchup vs cron |
| [029](029-fully-disjoint-test-clock-flows.md) | 2026-05-08 | Accepted | Fully disjoint test-clock flows across every time-aware engine path |
| [030](030-simulated-time-everywhere-on-clock-pinned-entities.md) | 2026-05-08 | Accepted | Simulated time everywhere on clock-pinned entities |
| [031](031-per-plan-base-bill-timing.md) | 2026-05-14 | Accepted | Per-plan base bill_timing (in_advance vs in_arrears) |
| [032](032-public-cost-dashboard-projection.md) | 2026-05-14 | Accepted | Public cost-dashboard projection — token shape + sanitization contract |
| [033](033-litellm-spend-adapter.md) | 2026-05-14 | Accepted | LiteLLM spend adapter — wedge integration |
| [034](034-plan-billing-field-immutability.md) | 2026-05-15 | Accepted | Plan billing-field immutability once a live sub attaches |
| [035](035-per-fact-simulated-time-anchoring.md) | 2026-05-16 | Accepted | Per-fact simulated-time anchoring under test-clock catchup |
| [036](036-dunning-campaigns-model.md) | 2026-05-16 | Accepted (amended 2026-05-16) | Dunning campaigns model (multi-policy-per-tenant) |
| [037](037-trial-end-and-activation-period-anchoring.md) | 2026-05-18 | Accepted (amended 2026-05-18) | Trial-end and activation period anchoring (centralized helpers + Phase 0.5/0.9 trial-expiry) |
| [038](038-credit-note-three-channel-allocation.md) | 2026-05-24 | Accepted | Credit notes use three explicit allocation channels (Stripe + Lago shape) |
| [039](039-cut-coupons-pre-launch.md) | 2026-05-30 | Accepted | Cut coupons pre-launch |
| [040](040-outbox-always-on.md) | 2026-05-30 | Accepted | Outbox is the only path (webhook + email) |
| [041](041-tax-fallback-manual-removed.md) | 2026-05-30 | Accepted | Remove `tax_on_failure=fallback_manual` (block-only) |
| [042](042-tax-rate-decimal-precision.md) | 2026-05-31 | Accepted | Tax-rate decimal precision + proration integer day-ratio |
| [043](043-drop-tax-rate-bp.md) | 2026-05-31 | Accepted | Drop `tax_rate_bp` immediately (no transition window) |
| [044](044-canonical-ai-token-metering-model.md) | 2026-06-01 | Accepted | Canonical AI token-metering model (one `tokens` meter + `token_type` dimension) |
| [045](045-decimal-per-unit-pricing-rates.md) | 2026-06-01 | Accepted | Decimal per-unit pricing rates + multi-dim cycle billing |
| [046](046-manual-tax-largest-remainder-apportionment.md) | 2026-06-03 | Accepted | Manual tax — document-level total + largest-remainder line apportionment |
| [047](047-invoice-tax-rate-displays-statutory-not-effective.md) | 2026-06-05 | Accepted | Invoice-level `tax_rate` displays the statutory rate, not the effective rate |
| [048](048-credit-clawback-tax-reversal.md) | 2026-06-06 | Accepted | Credit clawbacks reverse proportional tax via the credit-note primitive |
| [049](049-payment-settlement-primitive.md) | 2026-06-07 | Accepted | A single payment-settlement primitive (discover-then-settle) |
| [050](050-unpaid-source-proration-policy.md) | 2026-06-08 | Accepted | Unpaid-source proration — block charges, adjust credits |
| [051](051-remove-customer-self-serve-portal.md) | 2026-06-09 | Accepted | Remove the customer self-serve portal |
| [052](052-customer-tax-status-engine-determined-vs-override.md) | 2026-06-15 | Accepted | Customer tax status — engine-determined by default, manual flag is the override |
| [053](053-explicit-payment-method-at-charge.md) | 2026-06-17 | Accepted | Charge the explicit payment method — Velox owns PM selection |
| [054](054-effective-unit-price-decimal-display.md) | 2026-06-17 | Accepted | Display the per-unit price at full precision — derive on read, don't store |
| [055](055-anniversary-month-end-anchor.md) | 2026-06-18 | Accepted (amends ADR-058 date-math) | Anniversary billing clamps to month-end from a persisted anchor day |
| [056](056-atomic-cross-interval-plan-swap.md) | 2026-06-19 | Accepted | Cross-interval plan swap restructures the cycle atomically |
| [057](057-atomic-recoverable-downgrade-clawback.md) | 2026-06-20 | Accepted | Downgrade/removal clawback credit note — created atomically, issued recoverably |
| [058](058-billing-date-math-tenant-timezone.md) | 2026-06-09 | Accepted | Billing calendar date-math is anchored in the tenant timezone (renumbered from a duplicate 050) |
| [059](059-guard-invoice-mutations-while-payment-in-flight.md) | 2026-06-22 | Accepted | Guard invoice mutations while a payment is in flight — block void/uncollectible/offline-record; single void writer; defer the automated clawback until the source settles |
| [060](060-no-payment-method-dunning-enrollment.md) | 2026-06-23 | Accepted | Card-less invoices enter dunning (no-payment-method enrollment) |
| [061](061-credit-note-issue-atomicity.md) | 2026-06-25 | Built (PR2) | Credit-note `Issue()` atomicity — CAS + internal money effect in one coordinator tx; external effects post-commit + recoverable |
| [062](062-async-obligation-backbone.md) | 2026-06-25 | Decided (design); build deferred | Async obligation backbone — generalise the outbox (not Temporal/River/pgx-now); consolidate the four re-drive sweeps; trigger-gated |
| [063](063-refund-status-webhook-reconciliation.md) | 2026-06-28 | Accepted | Refund status is reconciled from Stripe webhooks (async truth); create-time status recorded faithfully; monotonic terminal-wins |
| [064](064-dunning-run-creation-derived-from-invoice-state.md) | 2026-07-02 | Accepted | Dunning-run creation — triggered-primary + derived-backstop; run existence & schedule are derivable (self-heals by construction), NOT a single-mechanism scalar; full-derive rejected |

> ℹ️ **ADR-058 was renumbered from a duplicate ADR-050** (2026-06-21). Two
> concurrent sessions had each taken `050` — the same hazard the migration-numbering
> rule guards against (pick the next number from origin/main, not a local branch).
> The earlier-dated 050 (`unpaid-source-proration`, 2026-06-08) kept the number;
> the later (`date-math`, 2026-06-09) became **058** and all its references were
> updated. The out-of-date-order number is expected for a renumber.

## Open follow-ups (deferred)

Work an ADR deliberately scoped out, as **thin pointers** — the authoritative
rationale and revisit trigger live in each ADR's *Consequences/Deferred* section;
this table only makes the open items discoverable in one place. Remove a row when
its follow-up ships. (These share one shape: a post-commit side-effect / external
call not yet in-tx or idempotent. None is a regression; each is guarded today and
gated on a named trigger — see `feedback_pre_launch_scoping`.)

| Follow-up | ADR | Code site | Revisit trigger |
|---|---|---|---|
| Clawback **post-flip partial-issue** window — `Issue()` flips status to `issued` then a side-effect fails, leaving the row invisible to the `status='draft'` reconciler (loud ERROR, manual reconcile) | [057](057-atomic-recoverable-downgrade-clawback.md) §"Known gap" | `internal/creditnote/service.go` · `Issue` / `RetryPendingClawbackIssue` | needs an idempotent unpaid-source `ApplyCreditNote` |
| **Bug B** — cross-interval swap refund double-credit on a full crash-retry (the post-commit refund is not idempotent; bounded by the per-invoice credit-note cap) | [056](056-atomic-cross-interval-plan-swap.md) §Consequences | `internal/subscription/service.go` · `FinalizeCrossIntervalSwap` | a real `in_advance` design partner, or idempotency-key middleware lands |
| **Receipt email in-tx** — the payment receipt is enqueued post-commit best-effort; a crash in the sub-ms window before the enqueue drops it (at-least-once-with-retry once enqueued, so this is the *correct* contract for email — upgrade only if a DP needs guaranteed receipts) | settlement.go durability-tiering note (no ADR) | `internal/payment/settlement.go` · `SettleSucceeded` (receipt) | a design partner requiring guaranteed receipt delivery |
| **`SettleFailed` event/email in-tx** — `payment.failed` + the failed-email fire post-commit, gated behind `firstForThisPI` AFTER `MarkPaymentFailedReportingTransition` commits, so a crash in that window loses them and a same-PI redelivery (`firstForThisPI=false`) skips them. Remaining fix: move `payment.failed` **in-tx** (symmetric to the success-path `payment.succeeded` fold via `MarkPaidCardSettlementTransition`); the failed-**email** stays post-commit by design (symmetric to the receipt email — folding it in-tx would drag customer-email + suppression reads under the invoice row lock). NOTE: the **dunning-recovery half is DONE** — the `dunning_backfill` reconciler (`Engine.EnrollFailedWithoutDunning`) now re-drives the idempotent `StartDunning` for `failed` invoices with no run, so collection is no longer lost in the crash/exhausted-retry window (0085 UNIQUE keeps it exactly-once). | settlement.go (no ADR) | `internal/payment/settlement.go` · `SettleFailed` (`payment.failed` ~:251) | next time this path is touched, or guaranteed failure-notification delivery |
| **Stale-deferred-draft alarm** — Part B (ADR-059) defers an automated clawback against an in-flight source until the charge settles. If the charge *never* settles (a wedged `requires_action` PI nobody authenticates/cancels), the deferred draft waits unissued. It is **not lost** (durably captured, auto-issues on settle) and a wedged payment is independently visible (stuck `processing` invoice, tenant unpaid), so this is an *observability* gap, not a correctness one: surface a draft deferred > N days so an operator can cancel/await the PI (which auto-resolves the clawback). | [059](059-guard-invoice-mutations-while-payment-in-flight.md) §Deferred | `internal/creditnote/service.go` · `RetryPendingClawbackIssue` | operability hardening / first ACH-SEPA design partner |
| **`amount_paid` edge — Part C: record from captured** — `MarkPaid` records `amount_paid = amount_due` at settle. Under Velox's PaymentIntent-only **full-capture** model this equals the captured amount, so it is **not reachable today** (Velox exposes no partial/manual-capture flow). If partial capture is ever added, record from the PI's `amount_received` (the processor's captured amount) instead. | [059](059-guard-invoice-mutations-while-payment-in-flight.md) §Consequences | `internal/invoice/postgres.go` · `MarkPaid` | partial-capture support |
| **`DispatchTx` seam — atomic lifecycle-event emission** — `domain.EventDispatcher.Dispatch` carries no `*sql.Tx`, so the ~16 lifecycle webhooks (subscription/invoice/dunning/engine notifications) enqueue in a post-commit tx separate from their state change; a crash in the commit→enqueue window drops the event. The **settlement money events** (`invoice.paid`, `payment.succeeded`) are *already* in-tx via the invoice store, and a dual-write audit confirmed **no money event is fire-and-forget** — the rest are notifications, durable once enqueued, recoverable by consumer reconciliation. The silent-error vector (ignored enqueue failures) was closed (per-service `dispatchEvent` now ERROR-logs). The remaining crash-window atomicity fix = add `DispatchTx(ctx, tx, …)` to the seam + thread each state-change tx up — a ~16-site/6-package refactor, deferred at zero webhook consumers. | dual-write audit (no ADR) | `internal/domain/webhook_outbound.go` · `EventDispatcher` | first webhook-consuming design partner needing guaranteed lifecycle-event delivery |

## Writing a new ADR

```
cp TEMPLATE.md NNN-short-slug.md
```

Pick NNN as the next sequential number — check `ls docs/adr/`, NOT
your local branch (per `feedback_migration_numbering`: numbering is
chosen from origin/main to avoid duplicates that only fail at
integration-test time).
