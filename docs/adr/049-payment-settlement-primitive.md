# ADR-049: A single payment-settlement primitive (discover-then-settle)

- Status: Accepted
- Date: 2026-06-07
- Relates: ADR-001 (PaymentIntent-only Stripe), ADR-036 (dunning campaigns), ADR-030 (clock-pinned sim-time on financial writes)

## Context

A payment reaching a **terminal state** (`succeeded` / `failed`) is written in ~10 places across four packages, and each fires a *different subset* of the consequences:

**`→ succeeded` (`MarkPaid`)** — webhook `handlePaymentSucceeded` (`payment/stripe.go`) is the only complete one: sim-time `paid_at`, charged-card stamp, `payment.succeeded` event, receipt email. The others fire bare subsets — the reconciler (`payment/reconciler.go`), dunning-retry success (`dunning/handler.go`), out-of-band manual (`invoice/service.go`), and the engine/$0-credit paths (`billing/engine.go`, `billing/threshold_scan.go`).

**`→ failed` (`UpdatePayment(PaymentFailed)`)** — webhook `handlePaymentFailed` is again the only complete one: `payment.failed` event, dunning auto-start, customer email (suppressed for interactive/dunning-retry flows), out-of-order guard. The reconciler writes `failed` with **none** of that (a code comment there even *assumes* "the webhook runs in parallel" — but a **dropped** webhook is the exact case the reconciler-as-backstop exists for, so a backstop-recovered failure strands the invoice with no dunning, no event, no email = **silent under-collection**).

**Charge initiators** — cycle billing, threshold, portal "pay now", operator "charge now", finalize-time auto-charge — all create a PaymentIntent with `Confirm: true, OffSession: true` (so Stripe returns the real outcome **synchronously** in the create response) and then set `payment_status = processing` **unconditionally** (`payment/stripe.go`), discarding `result.Status` and waiting on a webhook that merely repeats what the response already said.

This is the classic overlapping-flows bug: several flows settle the same entity with **non-disjoint, drifted** side-effects. The visible symptom that surfaced it: a **test-clock**-pinned customer's auto-charge sat in `processing` forever — the create call returned `succeeded` synchronously *inside the Advance*, but Velox set `processing` and waited on a `payment_intent.succeeded` webhook that arrives in **wall-clock** time (and, in local dev, only when `stripe listen` is forwarding), fully decoupled from the simulated timeline. The reconciler can't help: it runs only on the wall-clock cron, over `payment_status = 'unknown'` (never `processing`, never during a test-clock Advance).

Industry (Stripe / Lago / Chargebee / Recurly, verified 2026-06-07) converges on: the provider's PaymentIntent is the authoritative state machine, the biller mirrors it, settlement happens only on a terminal signal, and **webhooks are primary truth but explicitly not infallible** — Stripe ships a reconciliation backstop (List Events `delivery_success=false`) precisely for dropped events. The problem isn't the *shape* (Velox already has the right states); it's that the **settlement action is not a single authoritative operation**.

## Decision

Introduce one idempotent **payment-settlement primitive** that owns the terminal transition **and the complete, correct side-effect set**. Every entry point becomes a thin *status discoverer* that hands the terminal outcome to the primitive:

- `SettleSucceeded(ctx, tenantID, inv, paymentIntentID, source)` — bind sim-time, `MarkPaid`, stamp the charged card, fire `payment.succeeded`, enqueue the receipt email. Idempotent (`MarkPaid` is a no-op on an already-paid invoice; the receipt/event are best-effort and log-only).
- `SettleFailed(ctx, tenantID, inv, paymentIntentID, failureMsg, suppressCustomerEmail, source)` — out-of-order guard (skip if already settled), `UpdatePayment(failed)`, fire `payment.failed`, auto-start dunning (sim-time anchored), enqueue the payment-failed email unless suppressed.

The four discoverers:

1. **Inbound webhook** — `payment_intent.succeeded` / `payment_intent.payment_failed` (primary truth).
2. **Charge synchronous response** — branch on `result.Status`: `succeeded` settles inline; `processing` / `requires_action` awaits the webhook + reconciler.
3. **Reconciler** — `GetPaymentIntent` for stale in-flight invoices (the dropped-webhook backstop), generalized from `unknown` to also cover stale `processing`.
4. **Operator "Check provider"** — on-demand reconcile from the attention banner.

Because all four route through the *same* primitive, a backstop-recovered settlement is **byte-identical** to a webhook one **by construction**, not by remembering to keep two code paths in sync.

**Scope boundary — "payment succeeded" ≠ "invoice settled without a charge."** The primitive consolidates *Stripe-PaymentIntent terminal outcomes only*. The engine/threshold `MarkPaid` calls for **$0 / fully-credit-covered** invoices move no money and must **not** fire a receipt email or `payment.succeeded` — they stay a separate "settle without payment" path. Out-of-band manual payments are their own operator-recorded path. The primitive does not absorb these; conflating them would email customers a "receipt" for a charge that never happened.

## Phased rollout

- **Phase 0** — this ADR.
- **Phase 1** — extract `SettleSucceeded` / `SettleFailed` and refactor the two webhook handlers onto them. **Behavior-preserving**; the existing webhook tests are the pin. **Shipped** (#188).
- **Phase 2** — wire the reconciler onto the primitive (fixes the silent under-collection for `unknown` *today*) and generalize its sweep to stale `processing` with its own cool-off (30m default) + a pre-write fresh-read race guard. The reconciler replicates the webhook's email-suppression from the PI `velox_purpose` (plumbed onto `GetPaymentIntent`). **Shipped** (#189).
- **Phase 3** — settle synchronously from the charge `result.Status` (fixes the test-clock symptom and all charge initiators at once, since they share `ChargeInvoice`). **Shipped** (#190).
- **Phase 4** — honest surfacing: age-aware `processing` attention (Info → Warning past an expected-settle window), wire the on-demand "Check provider" action, correct the banner copy so it never promises auto-resolution it can't deliver.

## Deferred (named triggers — the triggering surface does not exist yet)

- **Async payment methods** (ACH / SEPA / BACS direct debit). Trigger: first design partner who needs bank debit.
- **Dispute / late-failure handling** (`charge.dispute.*`, post-success reversal → counter-booking, à la Recurly). Trigger: an async method or dispute webhooks enabled.
- **Method-specific expected-settle windows** (cards: hours; ACH: ~5–8 business days). Trigger: first async method. Until then a single short card-appropriate window is correct.
- **A standalone daily List-Events drift cron.** Velox stores the PI id per invoice, so a targeted `GetPaymentIntent` reconcile already covers the payment path; a full event-stream scan only matters once a second webhook-driven side-effect has no per-row reconcile.

These are documented decisions, not silent gaps; the code would be dead until the trigger fires.

## Consequences

- One settlement implementation to test and reason about; the dropped-webhook backstop produces the same outcome as the webhook by construction.
- Fixes a live silent under-collection bug (Phase 2): a dropped `payment_intent.payment_failed` recovered by the reconciler will dun + notify + emit the event, instead of quietly marking `failed`.
- Reduces webhook dependence for the common synchronous-card path (Phase 3) and makes test-clock simulations resolve deterministically inside the Advance.
- The primitive is the single place future settlement behavior (disputes, async, partial payments) attaches to — no re-scattering.
