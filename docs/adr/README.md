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

## Writing a new ADR

```
cp TEMPLATE.md NNN-short-slug.md
```

Pick NNN as the next sequential number — check `ls docs/adr/`, NOT
your local branch (per `feedback_migration_numbering`: numbering is
chosen from origin/main to avoid duplicates that only fail at
integration-test time).
