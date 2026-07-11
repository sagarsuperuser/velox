# ADR-087: One post-finalize collection pipeline; collection gates stay per-site

**Status:** Accepted (2026-07-11)
**Context:** design review 2026-07-10, redesign #4 ("CollectAfterFinalize"); executed after an 8-agent site census verified 18 divergence dimensions across the seven finalize paths.

## Decision

Extract the post-finalize collection **pipeline** — resolve payment setup →
charge (30s, reload-first), or queue `auto_charge_pending` + send the
setup-link email — into one method, `Engine.collectAfterFinalize`, called by
the four engine finalize sites (cycle close, subscription_create day-1,
final-on-cancel, finalized threshold fire).

Do **not** unify the per-site collection **gates**. Credit-apply with
`creditApplyOK` failure-suppression, pause-collection, `amount_due > 0`, and
the threshold's draft/finalized status split remain at their call sites.

Do **not** fold in the three non-engine paths:

- **Manual finalize** (`invoice.Handler.collectAtFinalize`) — deliberately
  different retry-owner semantics (no flag on decline; the charged invoice is
  returned in the HTTP response). Its verified drift was a recorded follow-up,
  not silently absorbed — **executed 2026-07-11 as #448–#451**: PM-ID added to
  the readiness predicate (#448); charge failures classified — definite
  declines stay dunning-owned/no-flag, transient/unknown/unclassified queue
  for the sweep, safe by the sweep's payment_status='pending' predicate
  (#449); collection detached from the caller's cancellation in BOTH collect
  steps + engine-parity 30s charge deadline (#450 — the engine pipeline's
  day-1/final-on-cancel callers arrive on request ctxs too); zero-due settles
  PAID via Service.SettleZeroDue, the manual writer's ADR-066 T12 twin
  (#451).
- **Proration invoices** — sweep-mediated by design: inline charging would
  wall-clock-charge simulated invoices and duplicate the charge path
  (subscription handler comment). The missing no-PM setup email for proration
  invoices remains an open gap whose fix belongs in the sweep, gated on a
  dedup mechanism (the sweep re-ticks; an email per tick is spam).
- **Tax-retry auto-finalize** — collectless by design; the threshold path
  pre-plants the inert flag on tax-deferred drafts and the sweep collects the
  moment tax retry finalizes.

## Why the gates stay per-site (the traps a full unification hits)

1. **Credit-apply presence is a revenue-behavior decision.** Cycle close and
   threshold apply the customer's credit balance and suppress both the charge
   and the $0-MarkPaid arm when the apply fails (`creditApplyOK` — the
   2026-05-30 overcharge fix). Day-1 / final-on-cancel / manual do not apply
   credits. Adopting credit-apply there changes what customers are charged —
   a deliberate product decision to make explicitly, never a refactor
   side-effect.
2. **The $0/fully-credited MarkPaid arm is entangled with cycle control
   flow.** In billOnePeriod it advances the billing period and early-returns;
   extracting it either double-advances or skips the advance. Its
   `Status == Finalized` conjunct is the DEMO-000906 guard (a tax-pending
   draft must never transition to paid).
3. **Decline retry-ownership differs on purpose.** Engine sites flag
   `auto_charge_pending=true` on decline while the charger starts dunning
   inline; the sweep clears the flag for decline codes so dunning is the
   single owner. Manual finalize never flags. Unifying either direction
   creates double-charge windows (two retry owners, distinct idempotency
   keys) or silent dead-ends. A shared decline arm needs error-type
   classification, which no finalize site had at decision time; #449 has
   since given the MANUAL site exactly that classification (declines →
   dunning, everything else → flag) — the engine sites keep flag-always,
   whose decline flag the sweep clears on its next tick.

## Error-path fixes unified into the pipeline (behavior changes, tested)

- Resolver error ≠ no-PM: queue for the sweep, do **not** email — before this,
  all four sites emailed "payment method needed" to card-having customers on a
  transient read error.
- Pre-charge reload failure queues for the sweep — before this it skipped
  silently (no charge, no flag, no log): the invoice never entered any retry
  path.
- Pre-notify reload failure is logged (was silent email loss).

The charger/paymentSetups/noPMNotifier nil-guards at the four sites are
deleted (boot fails closed on nil collaborators since #442 — this completes
the redesign #3 arm-deletion program for the collect gates).

## Consequences

- The #434-style "edit the notify outcome switch at four sites" class is gone;
  pipeline changes are one edit.
- Site log lines are now prefixed by a site tag ("cycle close", …) instead of
  four hand-written variants.
- Unit fixtures reaching a finalize path must wire the collect collaborators;
  `wireBaseTax` defaults them (no-PM readiness, recording notifier, erroring
  sentinel charger).
