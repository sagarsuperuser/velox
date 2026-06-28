# ADR-063: Refund status is reconciled from Stripe webhooks (async truth)

**Date:** 2026-06-28
**Status:** Accepted

## Context

Stripe refunds are **asynchronous**. `Refund.create` is idempotent and often
returns `status=pending`; the final outcome (`succeeded` / **`failed`** /
`canceled`) lands later via a webhook. Even `succeeded` means "submitted to the
card network", **not** "on the cardholder statement" (5–10 business days, no
confirming event). A `pending` refund can flip to **`failed`** (bank reject /
insufficient platform balance → money returns to the *platform* balance, the
customer gets **nothing**).

Velox recorded refund status **optimistically from the create-call**:
`StripeRefunder.CreateRefund` dropped `ref.Status` and `creditnote.Issue()` /
`RetryRefund` hard-coded `refund_status = succeeded` on any non-error create.
The webhook switch handled **no** refund events. So a refund recorded
`succeeded` that Stripe later **failed** was a silent, money-wrong state — the
customer is owed money, and nothing surfaced it (it wasn't even `failed`).

#319 had just shipped a "refunds need attention" dashboard alert + Credit Notes
filter keyed on `refund_status IN ('failed','pending')` — written when `pending`
was *rare* (only "no refunder/no PI at issue").

## Decision

1. **Record the create-time status faithfully.** `CreateRefund` returns Stripe's
   actual status (mapped to a Velox `refund_status`); `Issue()` and `RetryRefund`
   record `pending` when Stripe says pending, not a blanket `succeeded`. (The
   common healthy card refund still returns `succeeded` synchronously.)

2. **The webhook is the source of truth for the async outcome.** Handle
   `charge.refund.updated`, `refund.updated`, and `refund.failed` (all carry a
   Refund object; one status-driven handler) — admitted via
   `endpointAuthoritativeEvent` (refunds carry no `velox_*` metadata; the
   per-tenant endpoint + HMAC is authoritative). Match webhook→credit-note by
   `stripe_refund_id`; apply **monotonically** (terminal wins; a stale
   out-of-order `pending` never clobbers a terminal). Reuse the existing
   event-id dedup. A refund with no matching credit note (Stripe-dashboard /
   direct-API refund) is **ack'd permanently, never auto-creates a credit note**;
   a *very recent* unmatched refund gets a bounded (15-min) redelivery to cover
   the rare create→webhook race.

3. **Status mapping (lossy, no migration).** Stripe's 5 states collapse into
   Velox's 4-value `refund_status` CHECK: `succeeded→succeeded`,
   `failed→failed`, **`canceled→failed`** (money returned to platform — the
   operator-actionable bucket), `pending→pending`, `requires_action→pending`.
   The `canceled→failed` collapse is the re-litigable call here.

4. **Co-refine the #319 alert** (same change) so faithful `pending` doesn't flood
   it: "needs attention" = `failed` **OR** (`pending` older than **72h** ≈ 3
   business days). Fresh `pending` is normal async settlement and is shown as a
   neutral per-CN badge, not an alert. The aged-pending arm is also the cheap
   backstop for a **never-delivered** terminal webhook (there is no refund poll).

## Consequences

- A refund that fails asynchronously now becomes `failed` and surfaces; the
  false-success state is closed.
- The refund leg stays **operator-retried** (`RetryRefund`), not auto-swept —
  money-out is conservative; the webhook + alert make stuck refunds *visible*.
- Honest UI: `succeeded` should read as "refund issued / on its way", since even
  Stripe's `succeeded` is "submitted", not "on the statement".

## Deferred (named triggers)

- **Refund-poll / `GetRefund` reconciler** for never-delivered webhooks — the
  aged-pending alert is its stand-in. Trigger: an observed missed webhook.
- **Dedicated `RefundCanceled` enum + CHECK-widening migration (0124)** — trigger:
  a partner actually hits canceled refunds.
- **Ingesting external (dashboard) refunds** into the credit ledger — larger
  separate decision; trigger: a tenant relies on dashboard refunds.

## Related
- #319 (the refund "needs attention" alert this co-refines).
- ADR-061 (atomic `creditnote.Issue()`); ADR-040 (webhook outbox / dedup spine).
