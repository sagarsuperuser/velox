# ADR-030: Simulated time everywhere on clock-pinned entities

**Status:** Accepted
**Date:** 2026-05-08
**Implemented**: 2026-05-08
**Supersedes**: closes the deferred-decoupling section ADR-029 left open for non-load-bearing timestamps

## Context

After ADR-029 shipped catchup correctness for the six load-bearing
catchup paths (period gen, auto-charge, tax retry, threshold scan,
credit expiry, dunning, reminders), a cross-flow audit found wall-clock
leakage at the per-callsite level:

- `dunning.Service` stamped `next_action_at` via `s.clock.Now()` even
  when called from catchup against a clock-pinned invoice.
- `subscription.Service.Create / Activate / ChangeItem` stamped
  `next_billing_at`, period bounds, `trial_end_at` in wall-clock for
  brand-new subs of clock-pinned customers.
- `invoice.Service.Create` stamped `due_at` in wall-clock for one-off
  invoices on clock-pinned customers.
- `payment.Stripe.handlePaymentSucceeded` stamped `paid_at` in
  wall-clock when the Stripe webhook fired (real network = real time)
  even though the invoice belonged to a simulated 2024-04 timeline.
- Postgres stores wrote `created_at` / `updated_at` via
  `time.Now().UTC()` regardless of the entity's pin.
- PDF "Generated on" footers, audit log entries, and dashboard
  "X minutes ago" formatting all read wall-clock.

The bug class: wall-clock timestamps written onto rows that catchup
queries later compare against `frozen_time`. The dominant trigger is
the **idle-clock-then-advance pattern** — operator creates a clock,
walks away for hours/days/weeks, comes back and advances. Wall-clock
has drifted ahead of `frozen_time` by the idle duration; any timestamp
stamped wall-clock during the advance now lands outside the catchup
window and is invisible to subsequent advance scans.

Per-callsite resolver patches (the post-ADR-029 fixes for dunning,
subscription, invoice, customer-flush) plugged the load-bearing leaks
but didn't scale: every new code path is one forgotten resolver call
away from re-introducing the bug.

## Stripe's published model

Stripe TestClock's "no semantic change in the presentation of Billing
objects in the API" guarantee, from their engineering blog:

> *"We made a no-op change to our internal logic to remove dependencies
> on real-world time, and instead retrieve timestamps from an abstract
> 'time provider' backed either by a real-world clock or a test clock."*

Two architectural rules that follow:

1. **Time-provider abstraction**: every business-logic timestamp is
   read from an injected provider, not from `time.Now()`. The provider
   resolves to the entity's pin (frozen_time on pinned, wall-clock
   otherwise).
2. **Scheduler carve-out**: pinned rows are filtered out of every
   wall-clock scheduler query. The test clock's catchup is the only
   thing that scans pinned rows. Without this, even simulated
   timestamps wouldn't help — the cron would still pick up the row at
   the wrong time.

Stripe's deliberate exception: **PaymentIntent / Charge / Refund** and
the actual Stripe payment webhooks stay wall-clock. Their docs are
explicit: *"Test clock advancement currently doesn't support
collecting payments through bank debits…Stripe collects payments after
the test clock advances."* They drew the line at "what the time
provider abstraction reaches" — Billing engine got the refactor;
Payments did not.

## Decision

Adopt Stripe's model end-to-end:

1. **Time provider is ctx-aware.** Replace `clock.Clock.Now() time.Time`
   with `clock.Clock.Now(ctx context.Context) time.Time`. Add
   `clock.WithEffectiveNow(ctx, t)`, `clock.EffectiveNow(ctx)`, and a
   package-level `clock.Now(ctx)` helper for postgres-store and
   render-layer code that doesn't own a Clock struct field.

2. **Bind effective-now at every operator entry point** on a
   clock-pinned entity. Subscription.Create / Activate / ChangeItem /
   ScheduleCancel / PauseCollection / EndTrial / ExtendTrial; invoice
   Create / Finalize / Void / RecordPayment / RecordPaymentFailure /
   ApplyDiscount / RetryTax / AddLineItem; dunning StartDunning /
   processRun / exhaustRun / ResolveRun / ResolveByInvoice; customer
   Update / UpsertBillingProfile; credit Grant; payment.Stripe
   handlePaymentSucceeded / handlePaymentFailed. Each binds via
   `clock.BindEffectiveNow(ctx, resolver, Pin{...})` at the top.

3. **Postgres stores read directly from ctx.** Every
   `time.Now().UTC()` in a store layer becomes `clock.Now(ctx)` —
   simulated for clock-pinned ctx-bound writes, wall-clock for the
   rest. No store-side Clock struct field needed.

4. **Render layer (PDF, email) takes ctx.** PDF "Generated on" footer,
   email date interpolations all read `clock.Now(ctx)` so a
   clock-pinned invoice's PDF shows simulated time consistently with
   its body.

5. **Webhook handlers bind from the invoice they're processing.**
   `payment.Stripe.handle*` resolves the invoice's effective-now via
   the engine and binds ctx before any state-machine call.

6. **Unified `clock.Resolver` interface.** Replaces the per-service
   `dunning.ClockResolver` / `subscription.ClockResolver` /
   `invoice.ClockResolver` interfaces shipped during the ADR-029
   patches. `*billing.Engine` implements `clock.Resolver` via three
   methods (`EffectiveNowForCustomer`, `EffectiveNowForSubscription`,
   `EffectiveNowForInvoice`), wired in `api/router.go` to every
   service that needs binding.

7. **Stripe's PaymentIntent/Charge carve-out is preserved.**
   `payment.Stripe` writes `paid_at` via `clock.Now(ctx)` (which is
   simulated when the invoice is clock-pinned), but the Stripe
   PaymentIntent / Charge resources Stripe itself owns remain
   wall-clock — that's outside Velox's database. Mirrors Stripe.

## The line we're drawing

| Surface | Domain | Why |
|---|---|---|
| Subscription, Invoice, DunningRun, CreditGrant, Customer state-machine timestamps | simulated when clock-pinned | Catchup correctness + dashboard consistency |
| Postgres `created_at` on every clock-pinned row | simulated | "No semantic change" — all visible timestamps in the simulation domain |
| Postgres `updated_at` on every clock-pinned row | simulated (via the same `clock.Now(ctx)` helper) | Same; no separate forensics column |
| PDF "Generated on" footer | simulated | Operator viewing a simulated 2024 invoice sees 2024 in the footer |
| Outbound webhook event `occurred_at` for billing events | simulated | Stripe `event.created` for billing events tracks frozen_time |
| Cron tick scheduler timestamp | wall-clock | The scheduler IS wall-clock; carve-out filters out pinned rows |
| Stripe webhook delivery (`Stripe-Signature` t=, retry timing) | wall-clock | Real network; replay protection requires real timestamps |
| Audit log row `recorded_at` (when added) | wall-clock | Forensics; never on the public API |
| Webhook delivery audit (delivered_at, failed_at) | wall-clock | Real HTTP timing |

No `physical_created_at` forensics column on any business-logic
table. Stripe deliberately doesn't have one — exposing wall-clock on
pinned objects forces every read path to choose, which is the bug
surface we're escaping. If forensics ever becomes a concrete need, it
goes in a separate audit table, not on the row.

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│  Operator HTTP request                                           │
│      ↓                                                            │
│  Service entry point (Subscription.Create, Invoice.Finalize, …)  │
│      ↓ clock.BindEffectiveNow(ctx, engine, Pin{...})              │
│      ↓ ctx now carries effective-now                              │
│  Service body                                                     │
│      ↓ s.clock.Now(ctx) → simulated time                          │
│      ↓ s.store.Update(ctx, …)                                     │
│  Postgres store                                                   │
│      ↓ clock.Now(ctx) → same simulated time                       │
│      ↓ INSERT … created_at = simulated, updated_at = simulated    │
│  Render (PDF, email)                                              │
│      ↓ clock.Now(ctx) → same simulated time                       │
└─────────────────────────────────────────────────────────────────┘
```

The chain is one bind at the boundary, ctx propagation everywhere
below. New code that calls `s.clock.Now(ctx)` is automatically
correct; new code that calls `time.Now()` directly is automatically
wrong, and the linter (Phase 7, deferred — see below) catches it.

## Consequences

- **Clean abstraction**: operators never need to know about
  wall-clock when working with test clocks. The "advance to past"
  question becomes irrelevant — every timestamp Just Works in the
  simulation timeline.
- **Compositional correctness**: nested service calls inherit the ctx
  binding. No per-callsite resolver discipline.
- **One-time refactor cost**: ~80 sites changed for the Clock
  interface signature; ~20 entry points wire the binding; ~50
  postgres stores read from ctx. Mechanical, all caught at compile
  time by the Go compiler.
- **PaymentIntent / Charge stay wall-clock**: matches Stripe; ours is
  a thin wrapper around their real API anyway.
- **The `updated_at` decision flipped from "wall-clock forensics" to
  "simulated"** during implementation. Reasoning: keeping `updated_at`
  wall-clock would require splitting every postgres-store write into
  two timestamps in different domains, doubling complexity for a
  forensics use case nobody has asked for. Stripe doesn't expose
  physical-write time on the public API; we don't either. If forensics
  is needed, audit logs cover it.

## Deferred to a follow-up ADR

- **Per-cause test_clock webhook events** (`test_clock.created`,
  `.advancing`, `.ready`, `.internal_failure`, `.deleted`): Stripe
  exposes these; Velox doesn't yet. Add when first design partner
  asks for clock-state change notifications.
- **`status_details.advancing.target_frozen_time` field on the test
  clock JSON**: Stripe surfaces this; lets dashboards render advance
  progress. Velox stores the target internally but doesn't expose it.
- **Auto-delete after 30 days** + per-clock entity caps (3 customers,
  3 subs/customer, 10 quotes): pre-launch we don't have a sprawl
  problem. Documented as future-enforce.
- **Advance cap** (≤2 intervals from shortest sub or ≤2 years if no
  sub): Stripe caps to prevent runaway catchup loops. Velox already
  caps at +1 year per call (FLOW TC4); could tighten to match Stripe
  per-sub interval count.
- **List-API filtering** (default-hide test-clock objects from
  `GET /v1/customers` etc., require `?test_clock=...` to surface):
  Stripe does this; Velox today returns mixed results. Defer until a
  customer asks.
- **Linter rule** that flags bare `time.Now()` in `internal/{service,
  store,api}` packages. Locks in the architecture against
  regression. Can ship as a `go vet` analyzer or a CI grep job; ~50
  LOC. Treat as a follow-up commit.

## Test coverage

Unit tests added at the platform/clock level cover the binding
mechanics (`WithEffectiveNow` / `EffectiveNow` / `BindEffectiveNow`
edge cases). Each domain that gained a `SetResolver` method has a
test that asserts simulated time stamping via a `stubClockResolver`
under a clock-pinned entity, and a wall-clock fallback test for the
unwired path. The cross-flow integration ("create clock-pinned
customer, advance, assert all timestamps simulated") is captured in
MANUAL_TEST FLOW TC4 — a real-DB assertion is the only way to verify
the postgres-store-layer stamps end-to-end.

## References

- [Stripe Test Clocks engineering blog](https://stripe.dev/blog/test-clocks-how-we-made-it-easier-to-test-stripe-billing-integrations)
- [Stripe TestClock API reference](https://docs.stripe.com/api/test_clocks)
- ADR-027 (customer-level test clock pin)
- ADR-028 (billing engine period loop + disjoint flows)
- ADR-029 (fully disjoint test-clock flows across every time-aware
  engine path)
