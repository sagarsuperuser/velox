# Changelog

All notable changes to Velox are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

**Pre-1.0 versioning:** we're on `0.MINOR.PATCH`. MINOR bumps for new
features, PATCH for fixes. The public API is stabilising but not yet
frozen; breaking changes land on MINOR until `1.0.0`.

## [Unreleased]

### Added

- **Dunning final-action enum aligned with industry (ADR-036 amendment)** — verified 2026-05-16 across [Stripe](https://docs.stripe.com/billing/revenue-recovery/smart-retries) (Cancel default + Mark Uncollectible + Pause), [Lago](https://getlago.canny.io/feature-requests/p/trigger-automatic-subscription-termination-based-on-dunning-campaigns) (terminate), [Orb](https://docs.withorb.com/invoicing/advanced-dunning) (retry + email only), [Recurly](https://docs.recurly.com/recurly-subscriptions/docs/dunning-management) (expire + leave overdue + mark failed). Velox now exposes the converging four-action set: `manual_review`, `pause`, `mark_uncollectible`, `cancel_subscription`. Two semantic fixes: (1) `pause` previously called hard `PauseAtomic` (silently skipped invoice generation per ADR-035 analysis); now calls `subscription.Service.PauseCollection(behavior=keep_as_draft)` — Stripe-aligned, cycle keeps drafting. (2) Renamed pre-amendment `write_off_later` → `mark_uncollectible` (industry-standard spelling); added net-new `cancel_subscription` (Stripe-default, was missing from Velox). Migration 0088 expands the `dunning_policies.final_action` CHECK; migration 0089 adds `uncollectible` to the `invoices.status` CHECK (new `InvoiceStatusUncollectible` enables true Stripe-parity for the mark-uncollectible terminal — distinct from voided, preserves invoice in financial reporting). New `invoice.Service.MarkUncollectible` method; new `dunning.SubscriptionCanceler` + `InvoiceUncollectibleMarker` interfaces with router-side adapters. SPA `DunningPolicies` dropdown shows the four Stripe-aligned labels ("Pause collection (keep drafting invoices)" makes the keep_as_draft semantics explicit). Old `write_off_later` rows backfilled to `mark_uncollectible` in the up-migration.
- **Dunning campaigns model (ADR-036)** — multi-policy-per-tenant + per-customer assignment, replacing the singleton policy + partial-field-override shape. Verified industry shape against [Stripe](https://docs.stripe.com/billing/revenue-recovery/smart-retries) (account-level only, segments via Automations), [Lago](https://doc.getlago.com/guide/dunning/automatic-dunning) (named campaigns + per-customer assignment), [Orb](https://docs.withorb.com/invoicing/advanced-dunning) (rules + schedules), [Recurly](https://docs.recurly.com/recurly-subscriptions/docs/dunning-management) (campaigns + per-account assignment) — all four converge on **named templates + assign-to-customer**. Velox adopts the superset shape used by Lago/Recurly: multiple `dunning_policies` per tenant (one `is_default=true`, enforced by a partial UNIQUE index), customers carry an optional `dunning_policy_id` FK (NULL = use tenant default). Per-run `policy_id` persists at `StartDunning` time so mid-flight retries stay on their original policy even if the customer's assignment changes — the new assignment takes effect on the next run. Save-time validation now rejects `max_retry_attempts > len(retry_schedule) + 1`; the runtime "reuse last interval when index out of bounds" silent-fallback branch is removed (`feedback_no_silent_fallbacks`). New endpoints: `GET / POST / PATCH / DELETE /v1/dunning/policies/{id}`, `POST /v1/dunning/policies/{id}/set-default`. New `dunning_policy_id` field on `PATCH /v1/customers/{id}`. Dropped: singleton `/v1/dunning/policy`, all `/v1/dunning/customers/{id}/override` endpoints, the `customer_dunning_overrides` table (migration 0086 drops it — pre-launch zero data to migrate). Frontend: new `/dunning-policies` admin page (list, create, edit, set-default, delete with assigned-customer guard); `CustomerDetail` page replaces "Dunning Override" card with "Dunning policy" card + `AssignDunningPolicyDialog` picking from the policy list; `/dunning` page drops the Policy tab in favor of a single Runs view with a "Manage policies" link to the admin page. ADR-036 documents the design + rejection of alternatives (`CustomerDunningOverride.retry_schedule` extension, Stripe-style account-level-only, Orb-style rules engine).
- **"Sent emails" section on customer detail page.** Operators can now see every customer-facing email Velox dispatched for a given customer (last 30 days, newest first) without leaving the dashboard. **Anchored on Stripe specifically** ([docs.stripe.com/invoicing/send-email](https://docs.stripe.com/invoicing/send-email) — explicit "Sent emails" section on the customer page, 30-day window), not on a converging industry pattern: 2026-05-16 multi-platform verification found that Recurly explicitly lacks this surface ("does not have native, user-facing reports or data exports specifically for email deliveries"), Lago / Orb docs don't describe an operator-facing email log either way. Kept the feature because the operator audit gap it fills (dunning testing produces multiple emails per invoice; without this view the only trail is Mailpit or raw SQL) earns its keep regardless of broader convergence. New backend endpoint `GET /v1/customers/{id}/sent-emails` returns `[{id, email_type, recipient, status, invoice_number?, last_error?, created_at, dispatched_at?}]`. Backed by `email.OutboxStore.ListByCustomer` which joins `email_outbox → invoices` on `invoice_number` and filters by `customer_id` with a 30-day window. Frontend: new "Sent emails" card on `CustomerDetail.tsx` rendering type, recipient, send time, status pill (dispatched / failed / pending); failed rows surface `last_error` inline. Invoice number is a navigable link. As part of this change, `dunning_warning` and `dunning_escalation` email rows are now suppressed from the invoice activity timeline composer — those rows surface exclusively in the customer-page section, eliminating the wall-clock-vs-simulated-time visual mismatch on the invoice timeline (email actually sent at real-world May 16; the engine retry it represented was at simulated Mar 4).

### Removed

- **Hard-pause API surface fully removed (PR-8, Tier 1 + Tier 2).** PR-6 took the misleading "Pause subscription" option out of the dashboard but left the backend in place; PR-8 finishes the deletion. **Tier 1 (API + frontend):** routes `POST /v1/subscriptions/:id/pause` + `/resume` deleted, `Handler.pause` + `Handler.resume` deleted, `Service.Pause` + `Service.Resume` deleted, `Store.PauseAtomic` + `Store.ResumeAtomic` deleted (interface + postgres + memstore), SDK `api.pauseSubscription` + `api.resumeSubscription` deleted, dead `resumeMutation` + `status === 'paused'` Resume button block deleted from `SubscriptionDetail.tsx`, `EventSubscriptionPaused` + `EventSubscriptionResumed` enum constants deleted. **Tier 2 (schema):** `domain.SubscriptionPaused` enum constant deleted; migration **0090** drops `'paused'` from `subscriptions.status` CHECK with a safety guard that errors loudly if any row is in paused state (pre-launch: zero data, moot in practice); OpenAPI spec updated and types regenerated. **Industry framing corrected in ADR-037**: the earlier "no industry analog" claim was wrong — Chargebee and Recurly both ship hard pause with cycle skip (quoted source lines in the ADR). Velox deferred for scope and lack of named demand from the AI-infra Series A-B wedge market; `pause_collection` (Stripe-style soft pause) covers the legitimate "operator wants to freeze charging" use case. The ADR captures the Chargebee/Recurly e2e shape (paused_at + resume_at + remaining_pause_cycles + unbilled-charges decision tree) as the re-implementation starting point if a future DP names the need.
- **Hard "Pause subscription" option removed from the dashboard (Bug #12).** The Pause button now goes straight to **Pause collection** (Stripe-style soft pause — cycle keeps drafting invoices, no charge attempt until resumed). The previous radio-card choice exposed a second "Pause subscription" mode described as *"Freezes the cycle entirely. No usage metering, no invoices generated."* — a description that lied about the implementation: paused subs were excluded from the cycle scan, but on resume the engine immediately caught up and billed every "missed" cycle as if no pause had happened. Industry research found no analog: Stripe's only pause is `pause_collection` (keep_as_draft), Lago's subscription model has cancel-and-recreate, no platform ships true cycle-skip pause. Pre-launch with zero customers and no named use case for cycle-skip semantics (`feedback_pre_launch_scoping`), shipping a description that doesn't match behavior violates `feedback_design_for_production`. Backend API (`POST /v1/subscriptions/{id}/pause`, `Service.Pause`, `PauseAtomic`, `domain.SubscriptionPaused` status) is preserved as-is — no operator can reach it from the dashboard anymore, but external API callers are unbroken. The dashboard's `pauseSubscription` SDK method in `api.ts` is kept for the same reason. If a future design partner demands true cycle-skip semantics, re-expose with a `paused_at` column migration + proper period-anchor reset on resume; right now the cleanest move is to stop exposing the lie.

### Changed

- **Subscription detail page now reads simulation time on clock-pinned subs and renders trial-specific dots while `status=trialing`.** Two UX bugs fixed in one pass.
  - **(#5) Timeline `isPast` baseline used `new Date()` (wall-clock).** For a clock-pinned sub at sim-Nov-29 viewed in May-2026, every dot was painted as "past" (filled checkmark) because wall-now > every dot's date. Operator saw "completed" markers for events that hadn't happened yet in simulation time. Fix: `const now = sub.test_clock_id && testClock?.frozen_time ? new Date(testClock.frozen_time) : new Date()` — same pattern the `ExpiryBadge` already uses for the trial countdown.
  - **(#4) Timeline labels and the "Billing Period" card mislabeled trialing subs.** Pre-fix labels read `Created → Last Period → Period End → Next Billing` with the period dates pointing at the first POST-trial cycle (Dec 13 → Jan 1 after PR-2). Confusing two ways: (a) "Last Period" implied a billing period had already happened — none had; (b) the trial window itself (Nov 29 → Dec 13) was invisible. Fix: when `status === 'trialing'`, the timeline renders `Created → Trial ends → First charge`, and the "Billing Period" stat card switches its heading to `Trial period` showing `trial_start_at → trial_end_at`, with a small `First billing: …` sub-line for the post-trial cycle. The details panel below mirrors the same labelling, surfacing both the trial window and the first-chargeable-period in distinct rows. Active / paused / canceled subs are unchanged — they still use `Period Start / Period End / Next Billing`. Industry parity: Stripe's dashboard surfaces `Trial ends` as the dominant trialing-state field; Lago's customer detail does the same.

### Changed

- **"Billing Period" stat card renamed to "Current period" on the subscription detail + plan detail pages.** Same data (`current_period_start → current_period_end`); the old label implied "what's about to be billed," which lies for `in_advance` plans where the period was billed at its start (a customer reading the label after Phase 0.5 fired BillOnCreate could legitimately think the dashboard was showing an unbilled period). "Current period" matches Stripe's terminology and means what it says: the consumption window the customer is in, agnostic of when each line on the corresponding invoice fires. The trialing-state copy ("Trial period" + sub-line "First billing: …") is unchanged. The details panel below the stat card uses "First billing period" while trialing and "Current period" once active.
- **ADR-037 amended (2026-05-18) with cross-platform verification of the `in_advance` + metered usage invoice shape.** Stripe, Lago, and Chargebee each researched with quoted source lines. Result: Stripe + Lago both ship the hybrid invoice (next-period base + just-elapsed-period usage on one invoice at cycle close); Chargebee diverges (disallows the combination — metered subs cannot bill the base in advance). Velox follows the Stripe + Lago pattern; the Chargebee-strict validation guard is intentionally not adopted. Operator-facing implication captured in the ADR: a single invoice combines period-(N+1) base lines with period-N usage lines for an in_advance metered customer; the dashboard's per-line `billing_period_start/end` surfaces the distinction.
- **Cross-bill-timing plan-swap now rejected (in_arrears ↔ in_advance).** Pre-fix, the UI's Change Plan dialog accepted any active plan as the swap target; the backend's `rejectMixedItemIntervals` validation only checked `BillingInterval`, not `BaseBillTiming`. An operator could swap a single-item sub from in_arrears $29/mo to in_advance $49/mo, and the engine's hybrid invoice shape — which assumes a uniform bill_timing across items — was never exercised end-to-end for cross-bill-timing transitions (no E2E test for the boundary swap or the immediate-prorated path). Two guards added: (1) `rejectMixedItemIntervals` extended to also reject mixed `BaseBillTiming` on the post-change item set — covers Create / AddItem / multi-item UpdateItem; (2) explicit single-item guard inline in `UpdateItem` that rejects when the current item's plan timing differs from the new plan's timing — covers the single-item swap case that the post-change-set check can't catch (len < 2). Empty `BaseBillTiming` defaults to `in_arrears` to match the engine's lenient defaults so pre-ADR-031 plans validate cleanly. Error message steers the operator to cancel + recreate the sub. Four new tests: `Create rejects mixed in_arrears + in_advance items`, `UpdateItem rejects plan-swap that changes bill_timing on single-item sub (immediate=false)`, `(immediate=true)`, and a `UpdateItem allows plan-swap when bill_timing matches` no-regress guard.
- **UpdateItem plan-change now also rejects mixed intervals.** Closes the gap left by the original mixed-interval validation, which only fired at `Create` + `AddItem`. Pre-fix an operator could create a multi-item monthly sub and then swap one item's plan to a yearly plan — sub silently became mixed-interval and the per-sub period anchor drifted. Both immediate and scheduled plan-changes are gated at request time (Stripe parity — you can't queue a state that would be invalid when it lands). Single-item plan-change to a different interval is NOT covered — that's a clean interval swap, not a mix, and other platforms permit it. New test `UpdateItem rejects plan-change that would mix intervals on a multi-item sub`.
- **Mixed billing-interval items now rejected at Create + AddItem (Stripe / Lago / Chargebee parity).** Pre-fix, a sub with one monthly plan and one yearly plan would have an incoherent cycle (the period anchor is per-sub and uses the first item's interval). Validation runs after Create's item-dedupe + billing_time check and after AddItem's status check; `s.rejectMixedItemIntervals(items)` fetches each item's plan via the wired `PlanReader` and asserts every interval matches the first. Empty `BillingInterval` is treated as monthly (matches the engine's `advanceBillingPeriod` default — old plans validate cleanly). Plan-fetch errors surface as `errs.Invalid("items", ...)` so the operator sees a 400, not a 500. Skipped when PlanReader isn't wired (narrow unit tests). Two new test cases: `Create rejects mixed monthly + yearly items` and `AddItem rejects mismatched interval against existing sub`. UpdateItem plan-change doesn't yet re-validate — separate follow-up.
- **Trial-expiry scan paths now fire `subscription.trial_ended` webhook to match the engine auto-flip path.** Pre-fix, only the engine's `billOnePeriod` auto-flip at cycle close dispatched the event. The catchup orchestrator's Phase 0.5 (clock-pinned, PR-4) and the scheduler's phase 0.9 (wall-clock, PR-5) flipped status without firing the webhook — consumers downstream missed the trial-end signal for subs activated by the new orchestrator phases. Fix: `Subscription.Service` gains `SetEventDispatcher(eventDispatcher)`; both `ProcessExpiredTrialsForClock` and `ProcessExpiredTrials` now dispatch `EventSubscriptionTrialEnded` with `triggered_by="schedule"` after each successful activation (mirrors the engine path's payload shape). New regression test asserts the webhook fires with the correct event type, `triggered_by`, and `subscription_id`. Wiring in `router.go`: `subSvc.SetEventDispatcher(eventDispatcher)` next to the existing `subH.SetEventDispatcher` line.
- **ADR-037 — Trial-end and activation period anchoring.** New ADR documents the post-Bug-1/2/6/8/10/11 contract — the centralized period helpers, the `(billing_time × billing_interval)` matrix, the catchup Phase 0.5 / cron phase 0.9 transitions, and the design tradeoffs (e.g., yearly forces anniversary regardless of billing_time; PlanReader fails open to monthly). Industry shape verified across Stripe and Lago with quoted source lines. Indexed in `docs/adr/README.md`.

### Fixed

- **Sim-time on credit notes, dunning resolution paid_at, customer price overrides (PR-12 Round 2+3 — ADR-030 sweep continued).**
  - `internal/creditnote/postgres.go` — 5 timestamp writes (`Create`, `UpdateStatus`, `SetTaxTransaction`, `UpdateRefundStatus`, `CreateLineItem`) all now use `clock.Now(ctx)`. Credit notes against invoices on clock-pinned subs inherit sim-time stamps.
  - `internal/creditnote/service.go:224` — the `now` used as `issued_at` / `voided_at` / `created_at` in the credit-note creation path also now `clock.Now(ctx)`. `time` import removed.
  - `internal/dunning/handler.go:271` — `invoices.MarkPaid(ctx, ..., now)` was wall-clock. Now `clock.Now(ctx)` so dunning resolutions on clock-pinned invoices stamp `invoice.paid_at` in sim-time.
  - `internal/pricing/override.go:28` — `customer_price_overrides.created_at` / `updated_at` now `clock.Now(ctx)`. Overrides on clock-pinned customers stamp sim-time. `time` import removed.
  - All fallback to wall-clock when ctx is unbound (production paths unchanged).
- **Sim-time audit + proration timestamps across the subscription handler (PR-12, ADR-030 Round 1).** Continuing the PR-11 audit sweep, three more wall-clock leaks fixed on clock-pinned subs:
  - **Audit middleware catch-all** (`internal/api/middleware/audit.go:389`) — was unconditionally `time.Now().UTC()`. Now reads `clock.Now(parentCtx)` so audit rows from handlers that bound effective-now (e.g. customer mutations on clock-pinned customers, future invoice manual ops) get the sim-time stamp. Fallback path is unchanged for unbound ctx — no regression.
  - **Subscription handler proration math** — `remainingPeriodFactor(sub, time.Now())` ran in wall-clock at 3 sites (AddItem / UpdateItem / RemoveItem), producing wrong proration factors for clock-pinned subs whose `current_period_end` is in the simulated future. New `Handler.bindForSub(ctx, tenantID, subID)` helper binds effective-now from the sub pin via a wired `clock.Resolver` (`subH.SetResolver(engine)` in `router.go`); every proration path now does `ctx := h.bindForSub(r.Context(), tenantID, id); r = r.WithContext(ctx)` at entry, and downstream `time.Now()` calls become `clock.Now(ctx)`.
  - **changeAt timestamps on subscription_items.plan_changed_at** — 4 sites (AddItem/UpdateItem/RemoveItem/handleItemProration `dueAt`) — same fix shape via `clock.Now(ctx)`.
- **Activity timeline timestamps on clock-pinned subs now stamp simulated time (PR-11, ADR-030 follow-through).** The embedded activity timeline on the subscription detail page was showing wall-clock timestamps for operator actions (e.g. "Subscription created — May 18, 2026, 5:27 PM") even when the sub itself was created in simulated time (e.g. "Created Nov 29, 2025"). Mixed-domain timestamps on the same page made the timeline look broken. Two write paths to fix: (1) `audit.go:88` was unconditionally `time.Now().UTC()` — now reads `clock.Now(ctx)`, honoring ctx-bound effective-now; (2) the subscription handler's 9 explicit `auditLogger.Log` call sites now wrap `r.Context()` via a new `auditCtxForSub(ctx, sub)` helper that binds effective-now from `sub.UpdatedAt` (sim-time on clock-pinned subs after PR-1). The `create` and `activate` handlers gain explicit `auditLogger.Log` calls so they no longer fall through to the wall-clock audit-middleware catch-all. Item-level audit paths (AddItem/UpdateItem/RemoveItem audit entries that use `subID` rather than a loaded `sub`) still wall-clock — tracked as separate follow-up (item.UpdatedAt would be the right binding source). Industry framing: Stripe's separate Events page sidesteps this entirely by not embedding the timeline; Velox's embedded design (correct for AI-infra DP demos) requires the underlying audit to follow ADR-030's "simulated time everywhere on clock-pinned entities" principle.
- **Cancel-flow revenue gaps closed (PR-9 + PR-10 — two real bugs surfaced during the 2026-05-18 cancel-flow walkthrough).**
  - **PR-9 — `in_advance` + `cancel_at_period_end` overcharge.** Pre-fix, the cycle-close invoice fired BEFORE `advanceCycleOrCancel` applied the scheduled cancel. For `in_advance` plans, the cycle-close invoice covers the UPCOMING period's base — so a sub scheduled to cancel at period_end got billed $100 (example) for the next-period base immediately before being canceled. Customer paid for a period they wouldn't use. Fix: `billOnePeriod` now checks `shouldFireScheduledCancel(sub, periodEnd, now)` and skips the in_advance base line for the upcoming period when the cancel is about to fire at this boundary. Usage line for the just-elapsed period still bills normally (in-arrears for usage is always correct, captures final consumption). `in_arrears` plans were unaffected because their base covers the just-elapsed period, which they ARE consuming. Same bug class would re-appear if hard pause were ever re-added with cycle-skip semantics — captured in ADR-037's amendment for the future re-implementation. New regression test `TestRunCycle_InAdvance_ScheduledCancelAtPeriodEnd_NoOvercharge` locks the contract.
  - **PR-10 — Mid-period immediate cancel didn't bill partial-period usage** (both timings). Pre-fix, `Service.Cancel` only triggered `BillOnCancel` (which issues an in_advance proration credit for unused base) but generated NO final invoice. Partial-period usage was never captured — customer could rack up usage during a cycle and cancel for free. Also for `in_arrears`, the prorated base for elapsed days went unbilled. Fix: new `engine.BillFinalOnImmediateCancel` emits a final partial-period invoice covering `[current_period_start, canceled_at]` with `in_arrears` prorated base + usage line items. `in_advance` base is skipped (refund still flows via the existing `BillOnCancel` credit grant — two independent operations on cancel). `billing_reason = subscription_cancel` (new enum value via migration **0091** + new `domain.BillingReasonSubscriptionCancel` constant). Wired into `Service.Cancel` before `BillOnCancel` so the invoice generates with full base+usage before any credit application. Auto-charge attempt mirrors the post-finalize block in `BillOnCreate`; no-PM falls through to the queued-retry + notifier path. Best-effort: failures log but don't roll back the cancel — operator can manually invoice from the dashboard. Idempotent via the existing `(sub_id, period_start, period_end)` UNIQUE invoice constraint (period_end = canceled_at is durably stamped). Two new regression tests cover the wiring and error tolerance.
- **Yearly + trial first cycle is a full year, not 1 month (Bug #10).** Pre-fix, the period helpers `firstPeriodAfterTrial` and `firstPeriodForActivate` hardcoded monthly math (`AddDate(0, 1, 0)` for anniversary, next-month-start for calendar). A yearly plan with a trial got `period = trial_end → trial_end + 1mo`, so the customer paid 1/12 of yearly fee for the first cycle (an off-cycle prorated invoice), then full year invoices thereafter — 13 months of service for 13/12 of yearly fee. Fix threads `domain.BillingInterval` through the helpers: yearly plans get `period_end = ps + 1 year` regardless of `billing_time` (Stripe ships no "calendar yearly" either — a stub-to-next-Jan-1-then-full-year model has no industry analog). New `subscription.PlanReader` interface on `Service` (defaults to monthly when unwired, preserving narrow-test behavior) + `SetPlanReader(pricingStore)` wired in `router.go`. New helper `firstPlanInterval(items)` reads the first item's plan via the reader; fails open to `BillingMonthly` on any error (RLS gap / deleted plan / unwired reader). Multi-item subs with mixed intervals use the first item's interval — consistent with `engine.go`'s `firstPlanCurrency` pattern; explicit reject-on-mixed validation is a separate follow-up. Two new regression cases in `TestPeriodAnchoring` lock the contract: trial+yearly produces a full year, and start_now+yearly forces anniversary even when `billing_time=calendar` is set.
- **Wall-clock trial-expiry on the scheduler tick (Bug #8 — completes PR-4).** PR-4 fixed the catchup orchestrator's Phase 0.5 for clock-pinned subs; this PR mirrors it for **non-clock-pinned (production) subs** on the wall-clock cron tick. New phase **0.9 trial expiry** in `Scheduler.runBillingCycleForMode` — runs after threshold scan and BEFORE the natural cycle in step 1, so a trialing sub past its `trial_end_at` flips to active BEFORE `RunCycle` reads the sub list. New `subscription.Service.ProcessExpiredTrials(ctx, batch)` (mirrors `ProcessExpiredTrialsForClock`), backed by `Store.ListExpiredTrials(before, livemode, limit)` which queries `status='trialing' AND test_clock_id IS NULL AND trial_end_at <= now` per livemode partition with `FOR UPDATE SKIP LOCKED` to defend against concurrent scheduler ticks across replicas. ADR-028 disjoint flows: clock-pinned rows are EXPLICITLY EXCLUDED so they don't race the catchup orchestrator. New scheduler wiring: `Scheduler.TrialExpirer` interface, `Scheduler.SetTrialExpirer(subSvc)` in `cmd/velox/main.go`, `SubscriptionSvc` exposed on `Server` for main.go to wire. New regression `TestProcessExpiredTrials` asserts wall-clock activation + ADR-028 exclusion.
- **Status now flips `trialing` → `active` at `trial_end_at`, not at the next cycle close (Bug #8).** Pre-fix, status hung at `trialing` for the gap between the actual trial-end instant and the first chargeable cycle close — up to ~30 days for calendar billing where the cycle anchors on the next month boundary after `trial_end`. Operators viewing a clock-pinned sub at sim-Dec-20 still saw a `trialing` badge even though the trial ended Dec 13 in simulation time. Stripe / Lago both flip status at `trial_end_at`.
  - **Fix:** new **Phase 0.5 (trial expiry)** in the catchup orchestrator (`testclock.Service.RunCatchup`) — runs BEFORE Phase 1 cycle billing. Scans for clock-pinned subs with `status='trialing' AND trial_end_at <= frozen_time`, then for each calls `ActivateAfterTrial(at=trial_end_at)` (atomic status flip + activated_at stamp at the actual trial-end instant) and `BillOnCreate` so the in_advance first-paid-period coverage carries through (Bug #6 carries through this path).
  - **Race tolerance:** an operator EndTrial between the SELECT and per-sub UPDATE moves the row to active before the catchup gets there — the `ActivateAfterTrial` returns `ErrInvalidState`, the phase silently skips it, and the desired state is already reached. Postgres path uses `FOR UPDATE OF s SKIP LOCKED` so concurrent catchup passes don't double-process. `errors` package added to subscription/service.go imports.
  - **Wire-up:** new `TrialExpirer` interface on `testclock`, `subscription.Service.ProcessExpiredTrialsForClock` implements it, `SetTrialExpirer(subSvc)` in `router.go`. Store gains `ListExpiredTrialsForClock(tenantID, clockID, frozen, limit)` with hydrated items for the BillOnCreate downstream.
  - **Fallback path retained:** `engine.billOnePeriod`'s trial-elapsed branch (the old auto-flip-at-cycle-close path) stays as a defensive fallback for any code path that doesn't go through the orchestrator. With Phase 0.5 wired, the engine branch's `status='trialing'` guard naturally skips already-flipped subs.
  - **New regression test** `TestProcessExpiredTrialsForClock` covers 4 scenarios: trial elapsed → activated at trial_end_at + BillOnCreate fires; trial not yet elapsed → skipped; sub pinned to different clock → skipped; operator-EndTrial race → silently no-op.
- **in_advance + trial: first paid period now billed at trial-end (Bug #6 — revenue leak).** For an `in_advance` plan with a trial, the first paid period (trial_end → first cycle close) was never billed: `BillOnCreate` skipped trialing subs at Create time, and the engine's auto-flip path at the post-trial cycle close ran the normal cycle billing which charges `in_advance` items for the NEXT period (`periodEnd → nextPeriodEnd`), leaving the trial-end-to-first-cycle window unbilled. `in_arrears` plans were unaffected (cycle billing charges them for the just-closed period). Fix fires `BillOnCreate` from two trial-end transition points so the first paid period IS billed: (a) `Service.EndTrial` (operator path) — after `EndTrialEarly` activates the sub, BillOnCreate covers `[new_period_start, new_period_end]`; (b) `engine.billOnePeriod` auto-flip path — after `ActivateAfterTrial` succeeds, BillOnCreate covers the trial-end period before the normal cycle billing pre-pays the next one. Idempotent via the existing `(sub_id, period_start, period_end)` UNIQUE constraint, so retries don't double-bill. Best-effort: billing failures log but don't roll back the activation — operator can manually issue the invoice from the dashboard, same shape as the existing Cancel + BillOnCancel error path. No-op when no item is `in_advance`. Two new regression tests lock the contract: `TestCreate/EndTrial_BillOnCreate_failure_does_NOT_roll_back_activation` exercises the service-layer wiring; `TestRunCycle_Trial_Ended_InAdvance_CoversTrialEndPeriod` asserts 2 invoices at the engine auto-flip cycle close (trial-end coverage + next-period pre-pay).
- **Trial-end + activation period anchoring (calendar billing stub + early-end reset + extend-trial re-anchor + draft-Activate de-backdate).** Four related billing-correctness bugs fixed in one cluster; verified across Stripe / Lago for the converging cycle-anchor pattern.
  - **(#1) Calendar billing + trial dropped the stub period (revenue leak).** Sub created Nov 29 + 14d trial → trial_end Dec 13, but Velox set `current_period_start=Jan 1, current_period_end=Feb 1` — the 19-day window Dec 13 → Jan 1 was silently free. Worst case ~60 days at 30-day trials. Fix: new `firstPeriodAfterTrial` helper produces `ps=trial_end (day-snapped), pe=next calendar month start` — Dec 13 → Jan 1 stub, prorated correctly by `billOnePeriod`. Subsequent cycles roll forward via `advanceBillingPeriod` to calendar-aligned full months. Edge case handled: when `trial_end` lands exactly on a month boundary the stub collapses to a clean full cycle (`ps == pe` check promotes `pe` to the following month).
  - **(#2) EndTrial-early didn't reset period boundaries (gap-day revenue leak).** Operator ends trial at Dec 5 (8 days before scheduled Dec 13). Pre-fix: status flipped to active but `current_period_start` stayed at the original Dec 13 anchor → customer not billed until Jan 1, eight unbilled days from Dec 5 → Dec 13. Fix: new store atomic `EndTrialEarly` truncates `trial_end_at=at` AND resets `current_period_start/end/next_billing_at` to a fresh anchor computed via `firstPeriodForActivate(at, billing_time)` — Dec 5 → Jan 1 stub (calendar) or Dec 5 → Jan 5 (anniversary), all in one UPDATE. The engine auto-flip path (`billOnePeriod` calling `ActivateAfterTrial` at cycle close) is unchanged — periods were already correct at the cycle boundary.
  - **(#11) Activate (draft → active) backdated period_start to month-start, ignored `billing_time`.** Pre-fix: `Activate` hardcoded `beginningOfMonthIn(now)` for `period_start`, so a sub activated Nov 29 got `period_start=Nov 1`, billing the customer for the full Nov cycle including days the sub was a draft. Also ignored `sub.BillingTime` entirely — anniversary drafts activated mid-month got calendar-anchored periods. Fix: route through the same `firstPeriodForActivate(now, sub.BillingTime, loc)` helper so the activation instant is the actual period anchor and billing_time is honored.
  - **ExtendTrial period re-anchor.** Same bug class as #1: pushing trial past the original `current_period_end` silently dropped a stub between the new `trial_end` and the next cycle close. Fix: `Service.ExtendTrial` now also recomputes the period via `firstPeriodAfterTrial(newTrialEnd, billing_time)` and passes it to the new expanded `store.ExtendTrial(newTrialEnd, ps, pe, next)` signature.
  - **Industry verification:** Stripe ([docs.stripe.com/billing/subscriptions/billing-cycle](https://docs.stripe.com/billing/subscriptions/billing-cycle)) emits a prorated stub period between trial-end and the next billing-cycle anchor; Lago ([doc.getlago.com/guide/subscriptions/billing-time](https://doc.getlago.com/guide/subscriptions/billing-time)) does the same for "calendar" billing-time. Both reset period anchors on early-cancel-trial; both re-anchor on trial-extension. Velox now matches.
  - Store interface: `ExtendTrial` signature expanded to accept `(newTrialEnd, periodStart, periodEnd, nextBilling)`; new `EndTrialEarly(at, periodStart, periodEnd, nextBilling)` method added next to the existing `ActivateAfterTrial` (which the engine auto-flip path still uses unchanged). Memstore mirrors the new contract.
  - New test `TestPeriodAnchoring` exercises 8 scenarios — Create+trial × calendar/anniversary, trial_end-on-boundary edge, Activate-draft × calendar/anniversary, EndTrial-early, ExtendTrial × calendar/anniversary — locking the contract at the unit-test layer.
- **Cancel now works from trialing + draft (industry parity).** Velox's `CancelAtomic.allowedFrom` was `[active, paused]` — clicking Cancel on a trialing sub from the dashboard returned `cannot cancel trialing subscription`, trapping the dominant cancel scenario (customer abandons during trial). Industry verification: [Stripe](https://docs.stripe.com/api/subscriptions/cancel) (`subscription.cancel` works on any non-terminal status), Lago / Recurly / Chargebee all allow it. Velox now accepts cancel from every non-terminal state — draft, trialing, active, paused. `BillOnCancel` proration math is safe in the trialing case: `canceled_at` lies before `current_period_start` (since calendar-anchored periods don't start until trial-end), so the existing `!cancelAt.After(periodStart)` early-return fires and grants $0 (correct — there's no pre-billed amount to refund). `trial_end_at` is preserved across cancel for the historical "did this customer abandon during trial?" reporting question. Wrong-state error message updated to `cannot cancel %s subscription (already terminated)` — only `canceled` / `archived` are rejected. New regression test `TestCancel_NonTerminalStatuses` enumerates the four allowed start statuses + the double-cancel-rejection case.
- **Sub-mutator ctx-binding gap (canceled_at / paused_at / item created_at stamped wall-clock on clock-pinned subs).** `Subscription.Service.Cancel`, `Pause`, `Resume`, `AddItem`, `RemoveItem`, `CancelPendingItemChange` all delegated to the postgres store without first binding effective-now via `bindForSub`. The store's `transitionAtomic` and item-write paths read `clock.Now(ctx)` directly — without ctx-binding it falls back to wall-clock. Result: a clock-pinned sub frozen at sim-Nov-2025 that the operator canceled today stamped `canceled_at = 2026-05-18` (wall-clock), and the downstream `engine.BillOnCancel` cancel-proration math (which compares `canceled_at` against `current_period_end` to decide if any refund is owed) silently fired the "clean cancel, no proration" branch and granted $0 — customer owed an in_advance refund got nothing. Same root cause class as the 2026-05-04 tax-calc tenant_id pin regression (`feedback_ctx_attr_audit`). Fix is one line per method: `ctx = s.bindForSub(ctx, tenantID, id)` before the store call — matches the existing pattern in `EndTrial` / `ExtendTrial` / `ScheduleCancel` / `UpdateItem` / `Activate`. New regression test `TestSubMutators_StampSimTimeOnClockPinnedSub` enumerates every public sub-mutator on the Service and asserts each one stamps frozen time — a future mutator added without the bind trips it. The mem store used by unit tests was simultaneously threaded to honor `clock.Now(ctx)` (replacing direct `time.Now()` calls) so this contract is now exercised at the unit-test layer rather than only in integration.

### Changed

- **Create Subscription dialog unified across entry points.** The customer-detail page's `CreateSubscriptionDialog` previously rendered a reduced 4-field form (display_name, code, single plan, start_now), while the `/subscriptions` page rendered the full 9-field form (multi-item with qty, billing cycle, trial days, usage cap, overage action, start_now). Industry verification (2026-05-16): [Stripe](https://docs.stripe.com/no-code/subscriptions) and [Chargebee](https://www.chargebee.com/tutorials/subscription-enrollment/) both ship a single create-subscription surface invoked from either entry point, with the customer pre-filled when launched from a customer page; field set is identical between entry points. Velox's reduced variant blocked operators on the customer page from setting a trial, multi-item, billing cycle, or usage cap without leaving the page. Refactor extracts a shared `CreateSubscriptionDialog` component (new `web-v2/src/components/CreateSubscriptionDialog.tsx`) with an optional `lockedCustomer` prop — when set, the customer picker is hidden and `customer_id` is locked; when unset, the picker renders from a passed-in `customers` array. Both pages now render the same form; `CustomerDetail.tsx` passes `lockedCustomer={customer}`, `Subscriptions.tsx` passes `customers={customers}`. The reduced 4-field schema, the inline `CreateSubscriptionDialog` function in `CustomerDetail.tsx`, and the duplicate mutation+form-state on `Subscriptions.tsx` are all deleted (net −80 LOC). The ADR-027 inheritance hint follows the same code path in both entry points — derived from the form's watched `customer_id` (locked or picker-selected) against a passed-in `clockNameMap`.
- **ADR hygiene pass — Nygard-standard headers + index + supersession links.** All 36 existing ADRs now carry the canonical `**Date:** YYYY-MM-DD` + `**Status:** ...` headers (per [Michael Nygard's 2011 "Documenting Architecture Decisions"](https://cognitect.com/blog/2011/11/15/documenting-architecture-decisions)); historical dates resolved from `git log --diff-filter=A`. ADRs 007 + 008 (the API-key auth detours) marked `Superseded by ADR-011` in their status lines. ADR-036's status flags the 2026-05-16 in-place amendment. Mixed-format keys (`**Status**:` vs `**Status:**`) normalized to the latter consistently. New `docs/adr/README.md` index lists every ADR with date / status / title — entry point for new contributors and for grep'ing supersession state. New `docs/adr/TEMPLATE.md` Nygard-skeleton (Context / Decision / Why / Alternatives / Consequences / References) for future ADRs, with inline guidance that multi-platform claims must quote verified source lines (per `feedback_verify_stripe_parity_claims`).
- **Dunning escalation email actually enqueues at end of catchup.** The dunning service's warning + escalation email sends ran in goroutines bound to the parent catchup ctx — which `testclock/catchup.go:139`'s `defer cancel()` cancels the instant `RunCatchup` returns. Goroutines spawned LATE in the pass (the escalation, which always lands during the final retry's `exhaustRun`) lost the race and silently never enqueued. Symptom: DB state correctly read `state=escalated, attempt_count=N`, dunning event rows present, but Mailpit was missing the final escalation email. Warnings (spawned during earlier loop iterations) had enough head start to enqueue before cancellation. Fix: synchronous enqueue in the dunning service — the underlying `SendDunning*` calls are fast DB INSERTs into `email_outbox`, and the actual SMTP dispatch happens later on the outbox dispatcher worker's own long-lived ctx. Doubly-async goroutine wrapping was both unnecessary and the source of the race. New regression test `TestProcessDueRunsForClock_EnqueuesEscalationEmailOnExhaust` asserts the escalation enqueue on a 3-retry walk; a future refactor that reintroduces the goroutine wrapping trips it loudly.
- **One email per dunning retry attempt, not two.** The customer was receiving a `payment_failed` email AND a `dunning_warning` email for every failed retry — the webhook handler fires generic payment-failed on every `payment_intent.payment_failed`, and dunning's retrier fires its own warning per attempt. Both describe the same fact ("Payment failed for invoice X" vs "Action required — payment retry for invoice X (Attempt N of M)") within seconds. For a 2-retry policy that exhausted: 5 emails per invoice (3× payment_failed + 1× warning + 1× escalation). Industry shape (Stripe Smart Retries, Lago): one email per failed attempt carrying the retry-of-N context inline. Fix: tag dunning-retry PIs with `velox_purpose=dunning_retry` (new `payment.WithPIPurpose` context helper threaded from the retrier adapter, stamped into PI metadata by `ChargeInvoice`); the webhook handler now skips the generic payment-failed email when the tag is present and lets dunning's warning/escalation be the sole notification. Initial charge failures (no tag) still get the payment-failed email. End state per exhausted run: initial-failed + per-retry warning + final escalation = N+1 emails for an N-retry policy.
- **Inline StartDunning on known-failed charges closes the Phase 3 → Phase 5 webhook race.** `ChargeInvoice` previously left the dunning-run creation to the `payment_intent.payment_failed` webhook handler. Fine on wall-clock — the cron's Phase 5 ticks every 5 minutes and catches up. Broken on test-clock catchup: Phase 3 (auto-charge) fires the PI and returns, Phase 5 (dunning advance) runs IMMEDIATELY after, then the Stripe webhook arrives async and only THEN creates the dunning run — too late. The new run sits at `attempt_count=0` with no retries fired until the next Advance. Reproduction: clock advanced past the Aug 1 cycle close to Aug 21, Aug 1 cycle billed, charge fired against a decline card → dunning run created at `next_action_at=Aug 4` but `attempt_count=0` instead of the expected 2 + escalated. Fix: when `ChargeInvoice` gets a *definitively* failed PI error (not the ambiguous unknown/timeout case — those still defer to the reconciler), inline-call `StartDunning` so the run exists by the time Phase 5 queries. Safe alongside the webhook path because `StartDunning` is idempotent by invoice (migration 0085 UNIQUE). Extracts the failureAt cycle-close derivation into a `simulatedFailureAt(inv)` helper shared between the inline path and the webhook handler.
- **Cycle billing now anchors `now` on the cycle close instant, not advance-end frozen_time.** Closes the broader engine variant of the same bug class fixed in dunning. `engine.billOnePeriod` previously resolved `now := e.effectiveNow(ctx, sub)` — which during catchup returns the orchestrator's frozen_time (= advance-end), constant across every period billed in the loop. So an Apr→May 20 advance billing one or more cycles stamped every invoice's `IssuedAt` / `CreatedAt` / `DueAt` at May 20, regardless of which simulated period the invoice represented. Now `now := *sub.NextBillingAt` (with effectiveNow fallback for the unreachable nil case) — each period's invoice gets its own period-boundary timestamp, tax calculation runs against the rate applicable at the cycle close, the pause-resume gate evaluates correctly (a pause resuming May 5 honors that cutoff at simulated May 1 even when frozen_time is May 20), trial-end activation stamps at the cycle boundary, and `advanceCycleOrCancel` stamps cancel/pause timestamps at the simulated instant. In the cron path NextBillingAt ≈ wall-clock now within one scheduler tick — change is neutral. Zero test fallout; the existing fixtures were already using NextBillingAt-aligned reference instants.
- **Credit ledger entries now stamp `created_at` at the simulated instant.** Same anti-pattern as the dunning event store: `credit.PostgresStore.AppendEntry` overrode caller-supplied `entry.CreatedAt` with `clock.Now(ctx)`, and `ApplyToInvoiceAtomic` used `clock.Now(ctx)` directly for both the usage entry's `created_at` and the invoice's `updated_at` column. During catchup that resolves to advance-end frozen_time, so a customer's Credits tab showed every cancel-proration grant, plan-change credit, expiry, and applied-to-invoice usage at one timestamp instead of the per-fact chronology. Fixes: (1) `AppendEntry` honors caller `entry.CreatedAt` when non-zero, falls back to `clock.Now()` only when unset; (2) `credit.GrantInput` gains an `At time.Time` field that flows through to the entry — `engine.BillOnCancel` now passes `cancelAt`, so cancel-proration credits stamp at the cancel instant; (3) `processExpiry` stamps the expiry entry at the grant's own `expires_at` instead of advance-end; (4) `ApplyToInvoiceAtomic` takes an `at time.Time` parameter, threaded through a new `credit.Service.ApplyToInvoiceAt` and a new `engine.CreditApplier.ApplyToInvoiceAt` interface — engine + threshold-scan callers pass their `now` (cycle close instant), so a multi-period catchup writes one usage entry per cycle at the correct per-period boundary. Operator-path callers can still use `time.Time{}` to fall back to `clock.Now()`.
- **Each dunning event row carries its own simulated timestamp.** Closes the follow-up gap where all four event rows on the invoice timeline (started, retry #1, retry #2, escalated) shared one timestamp because `PostgresStore.CreateEvent` always stamped `clock.Now(ctx)` and ignored the caller-supplied `event.CreatedAt`. During catchup `clock.Now` resolves to the orchestrator's frozen_time, which is constant for the whole pass — so an Apr→Jul-21 advance produced four rows all at `Jul 21, 8:00 AM`. Industry shape (Stripe / Lago dashboards): each event row carries the moment that fact occurred in simulated time. `PostgresStore.CreateEvent` now honors `event.CreatedAt` when non-zero, falling back to `clock.Now()` only when unset (wall-clock callers stay correct). Service-side stamps: `dunning_started.CreatedAt = failureAt` (cycle-close instant), `retry_attempted.CreatedAt = run.NextActionAt at fire time`, `resolved.CreatedAt = effective-now`, `escalated.CreatedAt = the triggering retry's instant` (new `firedAt` param on `exhaustRun`). Loop test extended to assert exact per-event timestamps for a 3-retry walk (started May 1, retries May 4 / May 7 / May 12, escalated May 12).
- **One Advance click now walks dunning to completion (Stripe Test Clocks parity).** Closes two compounding gaps where an operator advancing the test clock past several scheduled retries had to click Advance N times to see N retries fire — the catchup orchestrator's Phase 5 was firing AT MOST one retry per click even when several were due in the simulated window, and the first retry's `next_action_at` was anchored to advance-end frozen_time rather than the actual simulated cycle-close instant. **Gap A**: `StartDunning` now takes a `failureAt time.Time` argument and the Stripe-webhook caller derives it from invoice period boundaries (`BillingPeriodEnd` for in_arrears, `BillingPeriodStart` for in_advance — the latest boundary ≤ `IssuedAt`, which is the cycle close moment in both directions). So a May 1 cycle invoice that fails during a May 20 Advance gets `next_action_at = May 1 + grace_period`, not `May 20 + grace_period`. **Gap B**: `ProcessDueRunsForClock` now loops `ListDueRunsForClock` → `processRunsBatch` until the query returns zero rows, with a 50-iteration safety cap and a non-progressing-run guard so a transient-skip case can't spin. `processRun` also now anchors each retry's effective-now on `run.NextActionAt` (the moment the retry was scheduled for) rather than `s.clock.Now()` — without this, a retry fired inside the catchup loop would set the *next* retry's `next_action_at` to `frozen_time + interval` (always past advance-end → never fires). Effect: a single Advance from Apr → May 20 with `grace=3d, retry_schedule=[3d, 5d], max=3` walks the full state machine: retry #1 at May 4, retry #2 at May 7, retry #3 at May 12 → `state=escalated`, `resolution=retries_exhausted`, final_action runs. Pre-fix: zero retries fired (next_action_at landed at May 23, past frozen_time). New tests: `TestProcessDueRunsForClock_LoopsUntilExhausted` (full 3-retry walk in one call), `TestProcessDueRunsForClock_StopsWhenAllAdvancePastFrozen` (terminates naturally when remaining runs are scheduled past frozen_time), updated `TestClockResolver_StampsFrozenDomain/processRun_chains_in_simulated_time` (asserts the new NextActionAt anchoring). MANUAL_TEST FLOW TC5 will be revised to drop the obsolete "Continue advancing through full retry schedule" step that documented the bug as a workaround.
- **Invoice timeline collapses payment-failure rows from all three sources onto one row.** Stripe / Lago dashboards render one row per business fact; Velox was rendering three for a single failed charge — the Stripe webhook (`payment_intent.payment_failed`), the engine-emitted dunning attempt (`dunning_started` / `retry_attempted`), and the "Payment-failed email sent to customer" delivery row. The composer now folds them inside-out: pass 1 collapses a successfully-dispatched payment-failed email into its co-occurring Stripe row as a `Customer notified by email` sub-line (both wall-clock-stamped, 2-min window); pass 2 pairs Stripe failures with their dunning twins **by chronological index** (the k-th Stripe failure on an invoice ↔ the k-th dunning attempt event), lifting PI id + amount + currency + error + email-sent sub-line onto the surviving dunning row. Index pairing replaces the earlier time-window approach because test-clock-pinned invoices emit dunning events in frozen time while Stripe webhooks land in wall-clock time — the two can differ by months. Email rows with `failed` / `processing` status stay as standalone rows so operators see delivery problems. New helpers `foldEmailIntoStripeFailed` + rewritten `mergeFailedPaymentTwins` in `internal/invoice/handler.go` with 18 unit-test cases between them covering simulated/wall-clock pairing, index-pairing across both event types, excess-rows-survive both directions, failed/pending email survival, pre-existing-Detail preservation, and passthrough cases.
- **MANUAL_TEST second-pass trim.** Cut U0 (Quickstart wizard / TTFI) — pre-launch speculative onboarding polish with zero DP demand. Trimmed B18 (Meter Detail page) from 9 UI checkpoints to 4 essentials. Trimmed R5 (Recipes dashboard UI) — collapsed two sub-sections into 2 lines, fixed stale "5 cards" to "3 cards" post-Phase 2 trim. Added a **priority signal** at the top of Tier 2: explicit Demo-blocking / Compliance / Operator-UX-polish buckets so operators know which flows to run before a DP demo vs which are routine UX-rework checks. Net delta: −18 lines.
- **MANUAL_TEST audited + trimmed for wedge alignment.** Cut U5 (dark mode), U6 (responsive), U11 (report-an-issue) — pure polish, not demo-blocking, easy regressions to spot manually. Cut X12 (velox-cli flow) — niche utility with no DP demand. Merged U9 (typed destructive confirms) into U7 (Edge cases) as a single row. Trimmed K3 (API keys page UX) from 8 polish checkpoints to 3 core observables (create dialog, raw-key-shown-once, revoke). Added FLOW S2 (AI-native end-to-end smoke) — strings the wedge pieces together: recipe instantiate → in_advance plan → LiteLLM ingest (5 calls) → hybrid cycle invoice → public cost dashboard. Run before every DP demo (~15 min). Net delta: +7 lines, but focus shifted from operator-polish surface area to wedge demo-path verification.

### Added

- **Plan billing-field immutability once a live sub attaches (ADR-034).** Closes a silent-billing-gap bug: previously, an operator flipping a plan's `base_bill_timing` from `in_arrears` to `in_advance` mid-cycle could lose one period's revenue per active sub (the elapsed period would never bill because the cycle path read the NOW-in_advance plan and billed upcoming-period base instead). Stripe-parity (their Price object) for the guard shape: billing-affecting fields freeze once any non-canceled, non-archived sub references the plan. Specifically, `PATCH /v1/plans/{id}` now rejects with 422 when `base_amount_cents`, `base_bill_timing`, or `meter_ids` differ from the current value AND any live sub exists; error message names the blocked field(s), live-sub count, and the "create a new plan instead" workaround. Display-only fields (`name`, `description`, `tax_code`, `status`) stay mutable. Plans with zero live subs are fully mutable (covers typo correction at plan creation). Implementation: new `pricing.SubscriptionPlanUsageReader` interface, `*subscription.PostgresStore.CountLiveSubsByPlan` query (NOT IN canceled/archived), wired via `pricingSvc.SetSubscriptionPlanUsageReader(subStore)` in `router.go`. Tests: 8 sub-cases (no-subs/live-subs × base_amount/base_bill_timing/meter_ids × allow/block + display-only-still-mutable + no-op-passes + unwired-reader-fail-open). MANUAL_TEST B7 covers the guard. ADR-034 documents the design + industry reference (Stripe, Orb, Lago, Chargebee, Recurly, Zuora all surveyed).
- **LiteLLM spend adapter (ADR-033).** Wedge integration. `POST /v1/integrations/litellm/spend` accepts LiteLLM's `StandardLoggingPayload` directly — operator configures LiteLLM's `generic_api` callback at this URL with a Velox API key as the Bearer token, and every LLM call lands in Velox as usage events automatically. No glue code. Mapping: each call → up to two events on `tokens_input` + `tokens_output` meters with `{model, provider, team_id?, request_tags?}` dimensions. Customer resolution via LiteLLM's `user=<external_customer_id>` convention. Idempotent via `<call_id>:input` / `<call_id>:output` keys. Partial-failure semantics: 200 with `{accepted, skipped, errors[]}` even when some rows fail (LiteLLM retries 5xx; per-row 422 avoids retry storms). Cost figures from LiteLLM stored as event metadata (audit only); Velox pricing rules drive the billable amount. New: `internal/integrations/litellm/{payload,mapper,handler}.go`, mounted under `/v1/integrations/litellm` with `PermUsageWrite`. ADR-033 documents the design + industry reference (Helicone/Langfuse pattern). `docs/integrations/litellm.md` is the operator-facing setup guide. MANUAL_TEST FLOW X15 covers happy path + replay idempotency + missing user + unknown customer + non-token-bearing + embedding-only + batch shape + dimension promotion + cost surfacing + auth.
- **Public cost-dashboard projection (ADR-032).** Closes the half-built cost-dashboard feature. Two new endpoints:
  - `POST /v1/customers/{id}/rotate-cost-dashboard-token` (operator, auth required): mints `vlx_pcd_<64 hex>`, returns `{token, public_url}`, audit-logs the rotation. Old token invalidated immediately.
  - `GET /v1/public/cost-dashboard/{token}` (unauthenticated, 60/min/IP rate limit): returns a sanitized projection — customer_id, tenant_id, billing_period, subscriptions (id + plan_name + currency + period), usage[] (meter + rules + totals), totals, projected_total_cents. Allowlist sanitization; PII (email, display_name, external_id, metadata, billing_profile) and internal IDs (plan_id, rating_rule_version_id) NEVER on the response. Empty state: no active sub → 200 with `billing_period.source="no_subscription"`. Wrong prefix / unknown / rotated token → 401 (anti-enumeration). New: `internal/customer/cost_dashboard_token.go` (token mint), `internal/usage/cost_dashboard.go` (assembler + projection types), `internal/customer/{handler,service,postgres,store}.go` (rotate + RLS-bypass lookup), `internal/api/router.go` (public route wiring + hostedInvoiceRL rate limit reuse), `web-v2/src/pages/CustomerDetail.tsx` ("Public cost-dashboard URL" card with Generate/Rotate button + copy). OpenAPI spec updated; MANUAL_TEST CU8 rewritten to assert the actual shape. Embeddable React widget deferred until first DP asks — the JSON is consumer-ready as-is.

### Added (in-flight: bill_timing bundle)

- **Per-plan `base_bill_timing` (ADR-031) — slice 1: schema + model + API.** Plans gain a `base_bill_timing` column (`in_advance` | `in_arrears`, default `in_arrears`). Default preserves every existing tenant's behaviour — no migration of in-flight subs. Setting `in_advance` is a forward-only opt-in that the engine-path slice (next) will honour with first-invoice-on-create + cancel proration. Usage lines remain structurally arrears-only (future-period quantities are unknown). Migration 0084 is additive; `POST /v1/plans` and `PATCH /v1/plans/:id` accept `base_bill_timing`; `domain.BillTiming` type with `BillInAdvance` / `BillInArrears` constants + `IsValid()`; web-v2 `Plan` interface carries the field. Tests cover default + explicit + invalid-value rejection.
- **Per-plan `base_bill_timing` — slice 2: first-invoice-on-create engine path.** Creating an active subscription on an in_advance plan now emits a day-1 invoice covering the upcoming period's base fee. `billing.Engine.BillOnCreate` handles plan resolve → in_advance filter → currency precedence (billing profile > tenant settings > plan) → mid-period proration → tax application → invoice with `billing_reason=subscription_create` → auto-charge (PM ready → 30s synchronous; no PM → queue `auto_charge_pending=true` + fire no-PM notifier). Idempotent via the standard `(subscription_id, period_start, period_end)` constraint. `subscription.Service.SetBiller` wires the engine; `subscription.Service.Create` calls `Biller.BillOnCreate` for active subs only (trialing subs land their first invoice when the trial ends, via the cycle path). Best-effort: a biller error logs but doesn't roll back the sub. Wiring lives in `router.go`. Tests cover active-path called, trial-path skipped, error-path tolerated.
- **Per-plan `base_bill_timing` — slice 5: dashboard UI.** Pricing → Create Plan dialog gets a "Base fee billed" dropdown (at end of period / at start of period) with inline help describing the difference. Plan Detail surfaces the timing in the Properties card; Edit Plan dialog adds the same dropdown so an operator can flip an existing plan. Invoice Detail line rows show a sub-line "Covers May 1 – May 31 (in advance)" when a line's period differs from the invoice's — surfaces the in_advance base for hybrid invoices that bill upcoming-period base + elapsed-period usage. OpenAPI spec gains `base_bill_timing` on the Plan schema + the POST /v1/plans body. The generated InvoiceLineItem schema already carried `billing_period_start` / `billing_period_end` from earlier work — no regen required for that surface; the Plan schema regen lands when orval is next run.
- **Per-plan `base_bill_timing` — slice 3: cancel proration credit.** Canceling an active subscription mid-period on an in_advance plan now issues a credit grant for the unused portion of the already-billed base. `billing.Engine.BillOnCancel` walks the sub's items, filters to in_advance plans, computes `unused = base * (period_end - canceled_at) / (period_end - period_start)` per item, sums, issues a single `credit.GrantInput` with a clear description referencing the period and cancel instant. No-op for clean cancels (canceled_at >= period_end), all-arrears subs, or zero unused amount. Refund mode is balance credit (Stripe's "out_of_band" equivalent) — operator can separately issue a credit note against the original invoice for a PM refund. `subscription.Biller` interface gains `BillOnCancel`; `subscription.Service.Cancel` calls it best-effort after CancelAtomic. Wiring: `engine.SetCreditGranter(creditSvc)`. Tests cover biller-called-on-cancel + biller-error-tolerated.
- **Per-plan `base_bill_timing` — slice 7: close create_preview gap.** `previewWithWindow` now mirrors the engine's invoice-header period shift for in_advance: when any item is in_advance, `BillingPeriodStart/End` on the PreviewResult is the upcoming period (matching what the cycle invoice will actually stamp). Base lines on in_advance items carry "in advance for upcoming period" in the description. Totals unchanged — preview math = invoice math both before and after. MANUAL_TEST I11 updated; the "operator preview shows arrears-style numbers" gap closed. Plan-change-across-timing remains framed as a rare operator-only operation that requires `effective=next_period` rather than `immediate`; tracked as design-deferred since cross-timing mid-cycle isn't a real DP-driven use case.
- **Per-plan `base_bill_timing` — slice 6: MANUAL_TEST + integration test.** Closes the bundle. B1 rewritten with explicit "default in_arrears" framing; B6 adds the in_advance cancel proration line; B7 / I11 carry caveat notes for mixed-timing plan-change + create_preview gaps. New flows: **B15** (in_advance happy path: day-1 invoice + cycle invoice for upcoming-period base), **B16** (hybrid in_advance base + in_arrears usage on one invoice), **B17** (cancel proration credit). Existing B17 (Meter Detail page) renumbered B18. New integration test `TestBillTiming_InAdvance_E2E` exercises slices 2 + 3 + 4 end-to-end against real Postgres: BillOnCreate emits day-1 subscription_create invoice with base only and the upcoming-period stamp; cycle-close emits subscription_cycle invoice on the NEXT period; cancel mid-period issues a credit grant of ~16/31 of the base. Also fixed: a slice-4 design gap discovered while writing the test — the cycle invoice's HEADER period now shifts to the upcoming period when any item is in_advance, so it coexists with the day-1 invoice under the (sub_id, period_start, period_end) UNIQUE constraint. Side cleanup: `rls_isolation_test.go` mode-aware-table list de-references the dropped `billing_alerts` / `billing_alert_triggers` tables from the Phase 2 trim.
- **Per-plan `base_bill_timing` — slice 4: cycle path bills next period's base.** Closes the double-bill bug that would have hit any in_advance plan: without this slice, the day-1 invoice (slice 2) AND the cycle-close invoice would both bill the SAME period's base. The base-fee loop in `billOnePeriod` now branches on `plan.BaseBillTiming`: `in_arrears` (default) bills the just-elapsed period as before; `in_advance` bills the *upcoming* period (`[periodEnd, nextPeriodEnd]`). The upcoming period is always full — proration only applies to `in_arrears` partial periods because in_advance partial-period creation was already settled by `BillOnCreate`'s day-1 invoice. Every base line now also stamps `billing_period_start` / `billing_period_end` explicitly so a future invoice mixing arrears and advance lines shows the correct period per row. Existing integration tests (TestFullBillingCycle_E2E etc.) confirm in_arrears default path unchanged.

### Removed

- **`bulk_actions` (operator-cohort apply-coupon / schedule-cancel) trimmed.** Pre-demo wedge-alignment cut (2026-05-14). Off-wedge: generic SaaS operator-cohort feature with no UI, no DP demand, no MANUAL_TEST coverage. ~2,150 LOC removed (package `internal/bulkaction/`, 2 router adapters, TS api client + types, migration to drop the `bulk_actions` table). Re-add cost when triggered by real DP need: ~4-6 days.
- **`plan_migrations` (operator-cohort bulk plan swap) trimmed.** Same trim, same reasoning. ~1,650 LOC removed (package `internal/planmigration/`, router wiring, TS api client + types, migration to drop the `plan_migrations` table). Re-add cost: ~5-7 days. Wedge-relevant proration code (`remainingPeriodFactor` for plan-change-on-individual-sub) is unchanged in `subscription/handler.go`.
- **`billing_alerts` (operator-configured spend/usage threshold + webhook fire) trimmed.** Phase 2 wedge-alignment cut (2026-05-14). 1,700+ LOC backend, zero UI page, zero DP pull — the speculative-AI-anomaly-alerts pivot is real but waiting for a real DP to describe the shape (token-spike detection is the likely AI-native form, not fixed-threshold). ~2,000 LOC removed (package `internal/billingalert/`, 2 router adapters, evaluator goroutine + advisory lock key, TS api client + types, MANUAL_TEST FLOW B14 sub-section, migration 0083 to drop `billing_alerts` + `billing_alert_triggers` tables, `docs/design-billing-alerts.md` design doc). Re-add cost: ~2-3 days with the actual AI-anomaly shape.
- **Generic recipes `b2b_saas_pro` + `marketplace_gmv` trimmed.** Phase 2 wedge-alignment cut (2026-05-14). The Recipes surface now shows three AI-native one-click pricing models (anthropic_style, openai_style, replicate_style) — sharper demo positioning to the Series-A AI infra buyer audience than mixing in generic SaaS / marketplace shapes. ~200 LOC removed (2 YAML files, test fixture pointer flipped to anthropic_style, MANUAL_TEST R1 entry count fixed). Re-add cost: ~30 min per recipe.

### Added

- **Billing-profile save now flushes stuck tax retries.** When an
  operator updates a customer's `country` / `postal_code` / `state` /
  `tax_id`, every draft invoice for that customer stuck on
  `customer_data_invalid` auto-retries — no per-invoice Retry click.
  Surgical filter: only `customer_data_invalid` rows replay (other
  codes like `provider_outage` or `jurisdiction_unsupported` aren't
  resolved by a billing-profile change, so retrying them here would
  burn the per-invoice retry budget). Same architectural shape as the
  ADR-019 Stripe-reconnect flush, scoped per-customer.
  `customer.Service` gains `SetTaxFlusher` (narrow `TaxFlusher` iface,
  one method); `invoice.Service` implements via new
  `RetryCustomerDataErrors`. Best-effort: per-invoice failures log a
  warn but don't roll back the profile save.

### Added

- **`make lint-clock` guard against ADR-030 regressions.** A
  shell-based check that flags bare `time.Now()` in
  `internal/{subscription,invoice,credit,dunning,customer,billing}`
  service / store / engine code — exactly the packages where
  clock-pinned entities are written. Bypassable per-line with a
  `// wall-clock: <reason>` justification comment for the legitimate
  carve-outs (cron tick, signature replay tolerance, audit-log
  recorded_at). Wired into `.github/workflows/ci.yml` as part of the
  lint job. Caught two real defects on first run that the prior
  ADR-030 audit had missed (`engine.nextTaxRetry` stamping wall-clock
  for `tax_next_retry_at`, and the `tax_deferred_at` write inside
  `ApplyTaxToLineItems`); both fixed in the same commit.

### Changed

- **Simulated time everywhere on clock-pinned entities (ADR-030).**
  Adopts Stripe's test-clock model end-to-end: every timestamp written
  for a clock-pinned customer / sub / invoice / dunning_run /
  credit_grant — including postgres `created_at` / `updated_at`
  audit columns, PDF "Generated on" footers, payment-success
  `paid_at`, and every state-machine stamp — lands in the test
  clock's `frozen_time` domain instead of wall-clock. Foundation:
  `clock.Clock.Now()` becomes ctx-aware (`Now(ctx)`); operator entry
  points bind effective-now once via `clock.BindEffectiveNow`;
  postgres stores and render code read directly from ctx via a
  package-level `clock.Now(ctx)` helper. Replaces the per-service
  `ClockResolver` interfaces shipped during ADR-029 patches with a
  unified `clock.Resolver` (implemented by `*billing.Engine`).
  PaymentIntent / Charge / Stripe webhook delivery timestamps stay
  wall-clock — matches Stripe's deliberate carve-out. The
  idle-clock-then-advance bug class (operator creates clock, walks
  away, advances later → wall-clock has drifted past `frozen_time` →
  any wall-clock-stamped row strands outside the catchup window) is
  closed at the architectural level: one ctx binding at the boundary,
  every nested service / store / render call inherits.

### Fixed

- **Operator writes on clock-pinned entities now stamp simulated
  time, not wall-clock.** Closes the cross-flow audit triggered by
  the dunning fix: any operator action (subscription create / activate
  / change-item / schedule-cancel / pause-collection / end-trial /
  extend-trial; one-off invoice composer; dunning state-machine
  steps) that writes a timestamp on a clock-pinned customer or
  subscription now resolves through `billing.Engine`'s
  `EffectiveNowForCustomer` / `EffectiveNowForSubscription` /
  `EffectiveNowForInvoice` and lands in the clock's `frozen_time`
  domain. Before, an idle test clock (created weeks ago, then
  advanced today) plus any operator-driven create-then-advance
  sequence stranded the affected row outside the catchup window —
  catchup queries compare `<= frozen_time` while writes were
  stamping `s.clock.Now()` (wall-clock 2026 vs. frozen 2024-04).
  The forward-simulation pattern (clock created and used minutes
  apart) hid the bug; the realistic idle-then-advance pattern
  exhibits it on every clock-pinned create.

  Stranding sites fixed:

  - **Subscription create** (`subscription.Service.Create`):
    `next_billing_at`, `current_billing_period_*`, `trial_end_at`,
    `started_at` were wall-clock; now resolved via the customer pin.
    The dominant impact, since `next_billing_at <= frozen_time` is
    the Phase 0 catchup gate.
  - **Subscription activate** (`Activate`): `activated_at`,
    `started_at`, period bounds, `next_billing_at`.
  - **Subscription item change** (`ChangeItem`):
    `pending_plan_effective_at` (when no period_end fallback),
    immediate `EffectiveAt` return.
  - **Subscription schedule-cancel / pause-collection /
    end-trial / extend-trial**: future-checks against simulated time
    (so an operator-supplied cancel_at "1 month in frozen-future"
    isn't rejected as wall-clock-past).
  - **Invoice create** (`invoice.Service.Create`): `due_at` was
    wall-clock; one-off invoices for clock-pinned customers and
    manual sub-attached addenda for clock-pinned subs now stamp
    simulated time, so `ListApproachingDueForClock`'s
    `due_at BETWEEN frozen AND frozen + N` window picks them up.
  - **Dunning** (`dunning.Service.StartDunning` / `processRun` /
    `exhaustRun` / `ResolveRun` / `ResolveByInvoice`):
    `next_action_at`, `last_attempt_at`, `resolved_at`,
    `created_at`. Phase 5 catchup gate.

  New `billing.Engine.EffectiveNowForCustomer` /
  `EffectiveNowForSubscription` (alongside existing
  `EffectiveNowForInvoice`) provide the resolution. Each affected
  service gains a narrow `ClockResolver` interface and `SetClockResolver`
  setter. Wired in `api/router.go` after engine construction.
  Wall-clock fallback preserved on resolver error / unwired
  resolver — never block an operator action on a dangling pin.

  Stranding bug surfaces on idle-clock + advance pattern, not just
  retro-simulation; common enough that any first design partner
  hitting test clocks would have observed it. Audit driven by the
  cross-flow review request after dunning fix landed.

### Changed

- **Test clock flows fully disjoint from wall-clock cron (ADR-029).**
  Closes the deferred-decoupling section ADR-028 left open. Every
  time-aware engine path that touches clock-pinned entities now has
  a per-clock catchup variant driven by the operator's Advance click,
  with the wall-clock cron's query filtering out clock-pinned rows
  via `NOT EXISTS`. Six phases shipped together so simulation purity
  is end-to-end, not partial:
  - **Phase 1 — auto-charge retry.** Clock-pinned `auto_charge_pending`
    invoices charge on Advance, never on the wall-clock 5-min tick.
    `Engine.RetryPendingChargesForClock` + `ListAutoChargePendingForClock`.
  - **Phase 2 — tax retry.** Pending tax calcs on clock-pinned invoices
    retry during catchup; runs before charge so unblocked tax flows
    into finalize → charge in one Advance. `Service.RetryPendingTaxForClock`
    + `ListPendingTaxRetryForClock`.
  - **Phase 3 — threshold scan.** Hard-cap (Stripe-parity billing-
    thresholds) invoices fire during catchup against simulated subtotal.
    `Engine.ScanThresholdsForClock` + `ListWithThresholdsForClock`.
  - **Phase 4 — credit expiry.** Clock-pinned customer grants expire
    against simulated `frozen_time`, not wall-clock. Frozen-time anchor
    read once per Advance for stable comparison across the catchup.
    `Service.ExpireCreditsForClock` + `ListExpiredGrantsForClock`.
  - **Phase 5 — dunning advance.** Past-due dunning state machines step
    forward in simulated time during catchup, after the charge phase
    so a successful charge clears dunning instead of advancing it.
    `Service.ProcessDueRunsForClock` + `ListDueRunsForClock`.
  - **Phase 6 — invoice reminders.** Reminder candidate lists for
    invoices approaching simulated `due_at` are gathered during
    catchup; matches today's cron behavior (list-and-log) and
    inherits future dispatch wiring symmetrically.
    `Service.ListApproachingDueForClock`.

  The `testclock.Service.RunCatchup` orchestrator now drives all six
  phases per Advance click in documented order with failure isolation
  (a failure in one phase appends to `runErrs` but doesn't stop
  subsequent phases). New narrow interfaces on `testclock.Service`:
  `TaxRetrier`, `CreditExpirer`, `DunningProcessor`, `ReminderLister`
  — all wired at the producer (`internal/api/router.go`).
  `Stripe Test Clocks` parity exact across all time-aware paths.
  Lesson memorialized in `feedback_long_term_means_cross_flow_audit`:
  when a contract changes (test clock = "operator drives time"),
  audit every consumer in the SAME design pass — don't punt them
  to deferred follow-up.

### Fixed

- **State conflicts now return `409 Conflict` with `code=invalid_state`
  (was `422 Unprocessable Entity` with `code=validation_error`).** The
  error router in `internal/api/respond/errors.go` collapsed
  `errs.ErrInvalidState` and `errs.ErrValidation` onto the same
  validation-error response, masking ~70 state-conflict guards
  across the codebase as input-validation failures. Wrong HTTP
  semantics: `422` means "fix your input"; `409` means "the resource
  is in a state that doesn't permit this operation" — exactly what
  every `errs.InvalidState(...)` site is. Caught while live-testing
  MANUAL_TEST FLOW TC4 ("Second advance while advancing → 409"),
  which got 422 in practice. Stripe / GitHub / AWS all use 409 for
  the same shape. Affected guards include test-clock advance-while-
  advancing, refund-while-not-paid, "cannot rotate a revoked key",
  recipe instance state transitions, and dozens more — all now
  return the right status with `code=invalid_state` (or the
  call-site's explicit `DomainError.Code` when set, e.g. a domain
  code like `clock_advancing`). One pinning test in
  `internal/invoice/refund_handler_test.go` was updated to assert
  the corrected wire shape.

### Removed

- **`POST /v1/subscriptions` no longer accepts `test_clock_id` (Stripe
  parity, ADR-027 follow-through).** ADR-027 (April 2026) moved the
  test-clock attach point from subscription to customer; subs have
  inherited from the owning customer ever since. The sub-create
  endpoint kept accepting `test_clock_id` as a vestigial input, with
  three branches of validation against the customer's value
  (livemode block, mismatch reject, customer-has-no-clock-but-input-does
  reject) — validation for an input no client should send.
  Stripe's `subscription.create` doesn't accept a `test_clock`
  parameter at all; the customer determines the answer. Velox now
  matches: `subscription.CreateInput.TestClockID` removed; the
  service unconditionally stamps `sub.test_clock_id = customer.test_clock_id`
  at create time. The OpenAPI spec already didn't surface the field;
  the SPA already didn't send it (per ADR-027 inheritance comments
  in `CustomerDetail.tsx`); only the dead server-side code goes
  away. MANUAL_TEST FLOW TC3 updated: the two lines exercising
  validation branches replaced with a positive assertion that the
  response carries the inherited clock and that a stray `test_clock_id`
  in the body is silently ignored. `subscriptions.test_clock_id`
  DB column stays — it's a denormalized cache used for routing
  (cron skips clock-pinned subs, ADR-028).

- **`test_clocks.deletes_after` column + TTL sweeper removed
  (consumer without a producer).** The column was added in migration
  0020 (Aug 2025) as a Stripe-parity 30-day idle-cleanup hook;
  ADR-016 (May 4 2026) wired the consumer side — a scheduler tick
  that soft-deleted any clock past its TTL and cascade-cancelled
  its pinned subs. The producer side never landed: no API field,
  no service input, no SPA control, no background job ever wrote
  `deletes_after` on a clock. The column was NULL on every row in
  production for ~9 months, the sweeper matched zero rows on every
  tick, and the only writer was a manual psql step in MANUAL_TEST
  FLOW TC2 that existed solely to exercise the sweeper machinery.
  Removed: `test_clocks.deletes_after` column + partial index
  (migration 0080), `domain.TestClock.DeletesAfter`,
  `Store.SweepDueDeletes`, `Service.SweepDueDeletes`,
  `billing.TestClockSweeper` interface + scheduler field +
  `SetTestClockSweeper` setter + the scheduler tick step,
  `cmd/velox/main.go` wiring, the postgres TTL test and the
  MANUAL_TEST psql step. ADR-016 amended to record the reversal
  and the trigger that revisits it (operator ask for auto-cleanup
  of idle clocks). Operator-driven `Delete` + cascade-cancel of
  pinned subs survives unchanged.

- **`usage_events.subscription_id` column dropped (Stripe / Lago / Orb
  parity).** The column has shipped since the original `usage_events`
  table was created but was never populated and never read. DB audit
  before removal: 49,751 rows, zero with `subscription_id` set. The
  HTTP ingest API never accepted it, the OpenAPI `UsageEvent` schema
  never advertised it, and no internal caller (handler, bench tool,
  test fixtures) wrote to it. Industry parallel: Stripe Meter Events
  do not store a sub reference at all — neither do Lago, Orb,
  Metronome or OpenMeter. Every comparable engine resolves the sub
  at invoice time from the customer's period overlap, which is what
  Velox's billing engine has always done in practice. Removed:
  `usage_events.subscription_id` column + FK, `domain.UsageEvent.SubscriptionID`,
  `usage.IngestInput.SubscriptionID`, the column from CSV exports
  and the field from related INSERT/SELECT SQL. Migration 0079
  drops the column with the FK first; downward migration restores
  both. The seed tool in `cmd/velox-migrate-safety/seed.go` no
  longer joins subscriptions just to populate a column nobody
  read.

- **Lean-cut: scope reduction for pre-design-partner stage (2026-04-29).**
  Velox is local-only with one user account; features built ahead of
  named customer demand were producing maintenance load and doc drift
  for an audience that doesn't exist yet. Cuts ride a single branch
  (`lean-cut`); each line below is "deferred until a real customer
  names the spec," not "killed forever." Git history is the audit
  trail — each cut is one revert away.
  - **Stripe Billing migration tool** — `cmd/velox-import/` and the
    backing `internal/importstripe/` package (28+ files: customer /
    product / price / subscription / invoice importers, mapper layer,
    integration tests, design RFC, operator playbook). Built before any
    tenant has migrated. Rebuild when a real customer announces a
    cutover; the implementation will fit their actual Stripe Billing
    shape rather than our guess.
  - **Self-host packaging beyond Compose** — `deploy/helm/` (Helm
    chart with templates), `deploy/terraform/aws/` (Terraform module),
    `deploy/k8s/` (raw Kubernetes manifests), `deploy/grafana/`,
    `deploy/prometheus/`, `ops/alerts/`. Compose-on-single-VM is the
    documented production-with-downtime-tolerance path; multi-replica
    HA + leader-elected scheduling lands when a design partner names
    which Kubernetes flavour they actually run.
  - **Compliance and ops runbooks** — `docs/compliance/soc2-mapping.md`
    (full SOC 2 TSC control mapping), `docs/ops/runbook.md` (803-line
    ops runbook), `docs/ops/audit-log-retention.md`,
    `docs/ops/encryption-at-rest.md`, `docs/ops/sla-slo.md`,
    `docs/ops/api-key-rotation.md`, `docs/ops/backup-recovery.md`,
    `docs/ops/capacity-planning.md`, `docs/ops/secrets-management.md`,
    `docs/self-host/postgres-backup.md`, `docs/blog/`. These were
    written for an enterprise buyer who hasn't shown up yet — and
    framework choices (TSC vs ISO vs HIPAA) belong to that buyer's
    auditor, not to us guessing.
  - **Operator polish surface (frontend pages)** — Analytics,
    BillingAlerts, BulkActions, Changelog (in-app changelog page),
    CustomerPortal, CustomerPortalLogin, PublicCostDashboard, Status,
    PlanMigration / PlanMigrationDetail / PlanMigrationsHistory (the
    cohort UI; one-customer plan-change endpoint and audit log remain),
    DPA, Privacy, Security, Terms, plus the entire embedded `/docs`
    site (12 sub-pages: DocsAccountSetup, DocsApi,
    DocsEmbedsCostDashboard, DocsErrors, DocsGlossary, DocsIdempotency,
    DocsQuickstart, DocsRateLimits, DocsRecipes, DocsTroubleshooting,
    DocsWebhooks, plus the index). Sidebar nav, route table, and the
    Help dropdown trimmed to match. The `<VeloxCostDashboard>` React
    component and the public cost-dashboard token API stay — they're
    the embeddable widget for tenants' own apps; only the in-dashboard
    demo page was cut.
  - **`docs/design-billing-alerts.md`** — design RFC for the alerts UI
    that's been removed. Alert engine logic stays in the codebase but
    the configuration UI is paused; tenants who need overrun alerts pre
    design-partner can wire a Slack webhook from the cycle scan.

### Added

- **OpenAPI as API contract source of truth — codegen + CI gate
  (Phase 1).** `api/openapi.yaml` is now the single source for the
  HTTP API. `make gen` regenerates the Go server interface + DTO
  types (`oapi-codegen` v2 → `internal/api/generated/`), the
  TypeScript raw type tree (`openapi-typescript` →
  `web-v2/src/lib/api.gen.ts`), and the typed `@tanstack/react-query`
  hooks (`orval` → `web-v2/src/lib/gen/queries.gen.ts` +
  `web-v2/src/lib/gen/schemas/`). A new CI job runs `make gen &&
  git diff --exit-code`, so any drift between the spec and the
  committed generated artifacts now fails the build — the same
  pattern Stripe uses internally with its openapi repo. Phase 1
  proves the pipeline end-to-end on `GET /v1/invoices/{id}`: the
  invoice handler exposes `(h *Handler).GetInvoice(w, r, id)` with
  a compile-time assertion that it satisfies the generated server
  interface, and `web-v2/src/pages/InvoiceDetail.tsx` now consumes
  `useGetInvoice` instead of a hand-rolled `useQuery`. Hand-written
  endpoints elsewhere keep working unchanged; subsequent phases will
  migrate the rest of the surface incrementally. The orval mutator
  at `web-v2/src/lib/orvalClient.ts` delegates to the existing
  `apiRequest` fetch wrapper, so generated hooks ride the same
  session-cookie auth, error-envelope parsing, and
  `Velox-Request-Id` capture as every hand-written caller.
  Workflow doc lives at `docs/dev/openapi-workflow.md`.

### Changed

- **Single OpenAPI spec at `api/openapi.yaml`.** The duplicate spec at
  `docs/openapi.yaml` is removed; README, the `/docs/api` Scalar viewer
  description, and the four design RFCs that linked to it now point at
  `api/openapi.yaml`. Eliminates the drift risk of two specs that
  needed to stay in sync by hand.

### Changed

- **Test-clock advance capped at +1 year per call (Stripe parity,
  ADR-028 amendment).** A single Advance now moves `frozen_time`
  forward by at most 1 year; longer simulations are chunked into
  successive operator clicks. Stops a single mistyped target from
  triggering a multi-decade catchup that would overrun the 10-min
  worker timeout and leave the clock in `internal_failure`. Each
  chunk's invoices, payments and dunning state stay reviewable
  before the next advance, and a bug surfacing in year 14 no longer
  poisons the earlier years' simulated billing in the same run.
  Server: `Service.Advance` rejects `newTime > current.FrozenTime + 1y`
  with a typed `errs.Invalid("frozen_time", …)` (leap-year-correct
  via `time.AddDate(1, 0, 0)`). SPA: the Advance dialog gates the
  submit button on the same window and surfaces the maximum allowed
  target inline so the operator never round-trips a 422.

- **Billing engine: per-sub period loop + disjoint cron / advance
  flows (ADR-028).** Operator click Advance to a far-future test
  clock (e.g., 25 years out) used to fail or stop short — the catchup
  outer loop hit `MaxAdvanceCatchupLoops = 120` before all periods
  were billed, then the wall-clock cron drip-billed clock-pinned
  subs at 1 invoice per tick (5 min in dev, 1 hour in prod). The
  cron and advance flows raced on `FOR UPDATE SKIP LOCKED`, so
  catchup sometimes returned `n=0` and exited cleanly while the
  sub was still due. Two architectural fixes shipped together:
  - **`Engine.billSubscription` loops internally per sub.** Extracted
    the existing body to `billOnePeriod`; the outer wrapper iterates
    until the sub catches up to its `effectiveNow`. One operator
    Advance click now generates ALL due invoices in one call, not 1
    per outer-pass. `MaxAdvanceCatchupLoops` removed; `maxPeriodsPerSubPerCall = 10000`
    is the inner safety counter (covers 833 years monthly).
  - **Disjoint flows: cron skips clock-pinned subs, advance is the
    sole path.** `GetDueBilling` now filters `WHERE test_clock_id IS NULL`;
    new `GetDueBillingForClock(clockID)` is used by `RunCatchup` via
    `Engine.RunCycleForClock`. SKIP-LOCKED race eliminated, drip-bill
    artifact gone, operator's mental model of "simulation time
    progresses only on Advance" upheld. Stripe Test Clocks
    operate the same way.

- **Test clocks now attach at the customer level (Stripe parity).**
  Velox previously attached test clocks at the subscription level —
  flexible but allowed a single customer to have subs on different
  clocks, breaking simulation-time consistency for customer-level
  state (credit balance, dunning override, payment-method setup).
  Stripe / Recurly attach at the customer; Velox now matches.
  Migration 0078 adds `customers.test_clock_id` (FK to test_clocks,
  ON DELETE SET NULL), backfills from existing per-sub pinning, and
  reconciles any sub whose clock disagrees with the customer's.
  `subscriptions.test_clock_id` stays as a denormalized cache;
  service layer enforces sub.clock == customer.clock at create time.
  `POST /v1/customers` accepts `test_clock_id` (test mode only,
  clock-existence-validated, immutable post-create — to switch
  clocks, recreate the customer, matching Stripe). `POST /v1/subscriptions`
  inherits the clock from the owning customer; mismatches rejected.
  New `GET /v1/test-clocks/:id/customers` endpoint powers the test-
  clock detail page's "Attached customers" surface (Tier 3 parity).
  SPA: dropdown moved from sub-create dialogs to the customer-
  create dialog; sub-create now shows a read-only inheritance hint;
  Customer Detail header carries a test-clock badge; Test Clock
  Detail shows attached customers above the existing subs list;
  Subscriptions-page create dialog no longer needs the dropdown
  (asymmetry resolved structurally). ADR-027.

- **Invoice timeline coalesces redundant rows + surfaces charged
  card (ADR-020).** Pre-fix the activity timeline rendered
  implementation-detail state-transition tuples instead of
  operator-meaningful milestones — a paid invoice showed both
  "Invoice paid" (lifecycle column flip) AND "Payment succeeded"
  (Stripe webhook), same minute, same amount. Dunning recovery
  added a third + fourth duplicate row. Server-side dedup now:
  drops Stripe `payment_intent.succeeded` when invoice.PaidAt is
  set; drops `payment_intent.canceled` when VoidedAt is set
  (PI-expired-without-void case still renders standalone); drops
  dunning `resolved` (`payment_recovered`) when PaidAt is set,
  with `attempt_count` rolled up onto the lifecycle row's
  sub-line ("after 3 retry attempts"). Migration 0077 adds
  `invoices.payment_card_brand` and `payment_card_last4`;
  `payment_intent.succeeded` webhook handler now retrieves the
  PI from Stripe with `expand=payment_method` and stamps the
  card details — works for one-off Checkout cards the customer
  didn't save (operator-reported case). Timeline `Detail` field
  surfaces "via Visa •••• 4242" beneath the "Invoice paid" row.
  `MarkPaid` signature unchanged (many upstream callers without
  card info); card-stamp is a separate `SetPaymentCard` update,
  best-effort with graceful no-sub-line fallback.

- **Stripe re-connect flushes stuck tax invoices (ADR-019).**
  When `POST /v1/settings/stripe` succeeds (verify returns 200),
  the service now fans out a background goroutine that retries
  every invoice in that (tenant, livemode) stuck on
  `provider_not_configured` / `provider_auth` —
  the two tax-error codes fresh Stripe credentials directly
  resolve. Reuses the existing `Service.RetryTax` per row, so
  engine-generated invoices that recompute clean also
  auto-finalize via the ADR-017 chain in the same pass.
  The Connect HTTP response includes `retries_queued: N`; the
  Settings → Payments toast shows "Retrying N stuck invoices in
  the background." Eliminates the per-invoice manual Retry-tax
  clicking after a re-connect — operator's mental model matches:
  "fixed the underlying issue → system catches up". Industry
  parallel: Stripe Connect replays queued events on account
  reconnect; Lago / Recurly do the equivalent. The fan-out runs
  with a 5-min wall-clock cap and pins tenant + livemode on a
  fresh ctx.Background (per the ctx-attribute audit pattern).

- **Test-clock retry advance + persisted failure reason (ADR-018).**
  Migration 0075 adds `test_clocks.last_failure_reason TEXT`.
  When the async catchup worker errors, it now captures the
  underlying error string on the clock row instead of just
  flipping status. Dashboard's `TestClockDetail` surfaces it
  inline: "Catchup failed during last advance. Reason: <...>"
  Eliminates the prior "grep server logs" recovery path.
  New `POST /v1/test-clocks/{id}/retry-advance` transitions
  internal_failure → advancing without changing frozen_time —
  the catchup loop is idempotent on subs (only processes rows
  with `next_billing_at <= frozen_time`), so resuming from where
  the previous attempt stopped is safe by construction. Operator
  no longer has to delete + rebuild the simulation to recover
  from a transient failure (Stripe Tax 503, intentionally-
  removed-creds debug runs, the now-fixed tenant-id-on-ctx bug).
  Matches Stripe Test Clocks' "Retry advance" UX. Replaces the
  "delete this clock to start a new simulation" copy with the
  actual recovery path.

- **Background tax-retry reconciler + auto-finalize on success
  (ADR-017).** Migration 0074 adds
  `invoices.tax_next_retry_at TIMESTAMPTZ`. The billing scheduler
  grows a tax-retry tick that picks up draft invoices stuck at
  `tax_status` pending / failed with retryable error codes
  (`provider_outage`, `unknown`) past their backoff timestamp,
  recomputes tax, and on success **auto-finalizes** when the
  invoice is engine-generated (`billing_reason != 'manual'`).
  Operator-driven "Retry tax" click follows the same auto-
  finalize rule. Backoff curve: 8 attempts over ~10 days
  (5min → 15min → 1h → 4h → 12h → 1d → 2d → 4d) with ±10% jitter
  to avoid thundering herd on outage recovery. Non-retryable
  codes (`provider_auth`, `provider_not_configured`,
  `customer_data_invalid`, `jurisdiction_unsupported`) are
  intentionally NOT retried — they need operator action and
  auto-retry would burn provider quota for nothing. Closes the
  half-built design from migration 0039 ("a background worker
  retries") that was never wired. Eliminates the two-click
  "Retry tax → Finalize" recovery for engine invoices; manual
  drafts still require explicit Finalize so operators can keep
  building.

- **Test clocks are soft-deleted with cascade-cancel of pinned
  subs (ADR-016).** Migration 0073 adds
  `test_clocks.deleted_at TIMESTAMPTZ` plus a partial index for
  the live (non-deleted) set. `DELETE /v1/test-clocks/{id}` no
  longer hard-deletes the row + leaves orphaned subs (pre-fix
  behavior under `ON DELETE SET NULL`). Instead it stamps
  `deleted_at = now()` and cascade-cancels every pinned
  subscription whose status isn't already terminal —
  atomically, in one tx. Generated invoices stay in place
  (Velox's invoice immutability rule). Aligns `test_clocks`
  with Velox's everywhere-else soft-delete convention
  (`status` columns, `archived_at`, `revoked_at`); fixes the
  silent-orphan-sub footgun where detached subs sat dormant
  with simulation-time `next_billing_at` values the wall-clock
  scheduler couldn't reconcile. Dialog copy truthed up:
  "This removes the clock and cancels its N pinned subscriptions."
  TTL sweeper for `deletes_after` (column existed since 0020
  with no sweeper) now wired into the scheduler tick — Stripe-
  parity 30-day idle cleanup, soft-deletes via the same path.
  Considered Stripe's hard-delete-with-cascade pattern; rejected
  because Velox runs test + live mode through a single audit_log
  + RLS partition and a special case for test-mode audit refs
  would break the uniform model.

- **Test-clock advance runs catchup asynchronously (ADR-015).** The
  HTTP advance handler now returns in milliseconds with the clock
  in `status: "advancing"` and a `CatchupJob` enqueued; a
  dedicated `CatchupWorker` goroutine drains the queue and runs
  the billing catchup off the request path. Matches the Stripe /
  Lago / Orb / Recurly / Chargebee shape — none of them tie
  catchup to an HTTP request lifetime. Eliminates load-balancer
  timeouts on long jumps, frees HTTP workers, and lets the
  operator navigate away while catchup runs. Boot recovery scans
  for clocks left in `advancing` from a prior process and
  re-enqueues them automatically — `kubectl rollout` or `velox`
  restart mid-catchup no longer leaves stuck clocks. 10-minute
  wall-clock timeout per catchup (was: unbounded; bounded only by
  `MaxAdvanceCatchupLoops=120`). The dashboard already polls
  `/v1/test-clocks/{id}` every 1.5s on `status==='advancing'`,
  so the status transition surfaces with no frontend change.
  Considered relying on the existing 5-minute billing scheduler
  to drive catchup — rejected: latency too high (operator wants
  seconds), couples test-clock UX to production-billing cadence,
  unusable when self-hosters run the scheduler at 1h+ intervals.

### Changed

- **Tax engine: removed silent zero-tax fallbacks. Fail loudly
  instead.** Pre-fix the engine had three zero-tax fallback
  branches (boot-time wiring nil, settings-missing,
  resolver-error) that masked production misconfigurations as
  "$0 tax invoices." That trade-off favoured availability over
  correctness — wrong for billing. Removed:
  1. **Resolver-error path: dead code.** The Resolver is
     exhaustive (none / manual / stripe_tax → falls back to
     manual when Stripe wiring is absent); never returns nil
     or error. The post-Resolve check never fired in production.
     Deleted.
  2. **Settings-missing path: fixed at the data layer.**
     `tenant.SettingsStore.Get` now synthesizes Velox defaults
     on `ErrNotFound` instead of bubbling the error — single
     source of truth via new `tenant.DefaultSettings()`. The
     HTTP handler (which already did this inline) now calls
     the helper. The engine never sees `ErrNotFound` again;
     orphan tenants from earlier failed bootstraps stop
     producing reconciler error spam.
  3. **Boot-time nil wiring: now fails fast.**
     `ApplyTaxToLineItems` returns an error when the resolver
     or settings store isn't wired. Previously this branch
     produced silent zero-tax for tests; tests now wire
     `tax.NewResolver(nil)` (NoneProvider for tax_provider="")
     explicitly via a `wireBaseTax` helper. Production has
     always wired both deps; this just makes the requirement
     enforceable instead of optional.
  Bootstrap now creates `tenant_settings` unconditionally
  alongside `tenants` so future tenants always have a settings
  row from creation. Belt-and-suspenders default
  (`TaxProvider:"none"` struct-init) from earlier today's
  hotfix removed — data-layer fix makes it unreachable.
  No new typed `tax_error_code` value added (you flagged the
  taxonomy fit was wrong — settings-missing is data integrity,
  not a tax-calculation failure).

### Added

- **`VELOX_TEST_CLOCK_CATCHUP_DELAY_MS` env knob for manual
  restart-resilience verification.** A 1-year monthly-billing
  catchup completes in 1-3 seconds on a single-sub fixture — too
  fast to reliably time `kill -TERM` mid-flight. Setting the env
  to a positive integer (milliseconds) makes `runCatchupLoop`
  sleep that long between billing passes, giving operators a
  deterministic window. With `=2000`, a 1-year advance takes ~24s
  — comfortable kill window. Off-by-default. Pacing sleep
  honours ctx cancellation so an actual `kill -TERM` mid-sleep
  wakes promptly. Mirrors `VELOX_TEST_CLOCK_INJECT_FAILURE`
  shape. ADR-015 amendment.

- **`VELOX_TEST_CLOCK_INJECT_FAILURE` env knob for manual UI
  verification of the catchup-failure path.** The MANUAL_TEST FLOW
  TC2 bullet ("Catchup failure → status internal_failure with
  destructive banner") suggested triggers (disconnect Stripe, hit
  the 10-min wall-clock cap) that don't reliably reproduce: tax
  failures defer via ADR-017's block-and-retry pattern rather than
  hard-fail catchup, and the wall-clock cap is machine-speed
  dependent. Now `runCatchupLoop` reads `VELOX_TEST_CLOCK_INJECT_FAILURE`
  at the top of the function and returns a failure with that value
  as the reason. One-shot — the helper clears the env after firing —
  so the operator can chain failure → Retry advance → success in
  one session. Off-by-default; production processes don't set it.
  Mirrors Stripe Test Clock's "force-fail" primitive. ADR-018
  amendment.

- **Smart polling on detail pages so webhook-driven state changes
  show up without manual refresh.** Detail pages were fetch-on-mount
  only — when the operator clicked Pay/Retry and waited for the
  Stripe webhook to land (typically 1-3s), the activity timeline +
  payment status sat stale until the page was reloaded. Now the
  invoice + timeline + customer payment-setup queries refetch on a
  state-aware cadence:
  - 2s while a charge is in flight (`payment_status=processing|unknown`)
    or a customer-portal update is mid-flight
    (`setup_status=pending`).
  - 5s on tax-retry, dunning-active, or post-decline states.
  - 10s on awaiting-first-charge / setup states.
  - 5s for ~30s after `paid_at` to catch trailing events (receipt
    email landing async via outbox, dunning resolution row, ADR-020
    card-detail stamping). Without this trailing window the operator
    would see "Paid" but the activity log would appear to be missing
    the "Payment receipt emailed" row that lands a few seconds later.
    Same pattern Stripe Dashboard / Recurly use.
  - No polling on truly terminal states (voided / draft / setup ready
    or missing, paid+>30s) — `refetchOnWindowFocus` covers the rare
    "tab open all day" case without burning cycles.
  Mirrors Stripe Dashboard's pattern. Picks polling over SSE
  (`/v1/webhook_events/stream`) at this stage — operator load is
  trivial, no latency complaint, and SSE adds connection-lifecycle
  complexity that pre-launch can defer.

### Changed

- **All paginated list pages now sort deterministically end-to-end.**
  Audit of every operator-visible list surface found the same three
  bugs that motivated the invoice fix repeated across 4 more pages:
  Subscriptions, Customers, Credit Notes, Credits ledger. Same shape:
  no id tie-break in `ORDER BY`, frontend sort UI not wired to the
  API, client-side re-sort only sorting the current page. All four
  fixed: store-local `<entity>OrderBy` helper with closed-allow-list
  + matching id tie-break direction; `ListFilter` gains validated
  Sort/SortDir; handler reads `?sort=` / `?dir=`; SPA passes them
  through queryParams + queryKey; client-side `useSortable.sorted`
  dropped (server is now the source of truth). Coupons already had
  the right shape (`id DESC` tie-break) and was untouched.

- **Invoice list now sorts deterministically end-to-end — bug + UX
  gap.** Operators reported the `/invoices` table appeared in
  random order. Three layered bugs:
  1. Backend `ORDER BY created_at DESC` had no tie-break.
    Test-clock catchup generates many invoices with microsecond-
    close `created_at` values; Postgres returned ties in arbitrary
    order.
  2. Frontend had clickable column headers + URL state but never
    sent `sort` / `dir` to the API — the UI affordance was decorative.
  3. Client-side re-sort hook (`useSortable`) was sorting only the
    current page, breaking pagination semantics ("top 50 by created_at,
    re-sorted by amount" instead of "top 50 by amount").
  Fixes: `ListFilter` gains validated `Sort` / `SortDir` fields with
  closed allow-list (invoice_number, amount_due_cents,
  billing_period_start, due_at, issued_at, status, payment_status —
  unknown keys default to created_at, prevents SQL injection),
  deterministic id tie-break matching primary sort direction.
  Frontend now passes sort + dir, includes them in queryKey for
  refetch, drops client-side re-sort. Period and Payment columns
  gain sortable headers. Tests cover allow-list + injection guard
  + tie-break direction.

- **Card-decline operator messages use Stripe's curated phrasings.**
  Previously the snake_case `decline_code` was mechanically
  title-cased, which produced awkward English for some codes ("Lost
  card.", "Do not honor."). New `internal/payment/decline_codes.go`
  carries a curated map sourced from
  [Stripe's official decline-code documentation](https://docs.stripe.com/declines/codes)
  with operator-readable phrasings ("The card has been reported lost.
  The customer should contact their bank.", "The card was declined by
  the issuer."). Codes not in the map fall back to title-case so new
  Stripe codes don't break the path. Tests assert the high-traffic
  codes resolve to their curated forms and unknown codes fall back
  cleanly. Built atop ADR-026's `SafeMessageError` boundary.

- **Backend errors no longer leak raw SDK / DB / internal strings to
  the operator UI.** Audit found 12 paths returning
  `respond.Validation(..., err.Error())` or equivalent — leaking raw
  Stripe SDK messages (the "Keys for idempotent requests can only be
  used with the same parameters…" toast was one), Postgres errors,
  and Velox-internal identifiers. Fixed with single-boundary
  sanitization in `respond.FromError` plus a new `SafeMessageError`
  marker interface that lets typed errors opt into curated rendering
  (`*payment.PaymentError` now returns "Card was declined:
  Insufficient funds." for declines instead of the raw SDK string).
  Untyped errors fall through to a generic 500 with the full error
  logged against the request-ID. Customer-facing public endpoints
  (hosted-invoice payment update) get Tier 2 sanitization — neutral
  copy, no operator framing. Auth-middleware errors no longer reveal
  whether a key exists / is expired / is revoked / hit a DB error
  (closes a key-enumeration leak too). Tests assert raw strings
  don't leak through the wrapper chain. ADR-026.

- **Attention banner separates Velox's classification from upstream
  provider responses.** Pre-fix, `Attention.Detail` was a single
  field labeled "Provider response" in the UI but populated from
  `inv.tax_pending_reason` — which conflated two semantics.
  For pre-flight failures (`tax.provider_not_configured` — we never
  called Stripe), the disclosure surfaced Velox's own internal string
  ("no client configured for livemode=false") under "Provider
  response", making operators believe a 4xx came back from Stripe
  when no API call was ever made. Now `Attention` has two distinct
  fields: `Detail` (Velox's own framing) and `ProviderResponse`
  (literal bytes from upstream). The frontend renders two
  conditional disclosures with honest labels; pre-flight codes leave
  both empty so no disclosure shows. Same pattern applies to
  `payment_failed` — Stripe's `last_payment_error` body goes in
  ProviderResponse. No data migration: API-formation routes per
  typed code (`tax_error_code`). ADR-025.

- **Cancel + Pause subscription dialogs redesigned as radio-card
  pattern.** The previous shape — two stacked full-width buttons with
  inconsistent click semantics (one fired the mutation immediately,
  the other opened a typed-confirm dialog) and only a "Back" button
  in the footer — was confusing: the destructive option's red border
  read as "pre-selected" rather than "destructive style hint", and
  there was no clear primary verb to commit a choice. Now each
  dialog has visible radio cards with selection state, defaults to
  the less-destructive option (period-end cancel / collection-only
  pause), and a single primary action in the footer that swaps to
  destructive variant when the irreversible option is selected.
  Stripe / Linear / GitHub all use this shape for destructive choice
  flows. Typed-confirm step preserved for the irreversible options
  ("cancel immediately", "pause subscription").

- **Activity timeline rows tightened to Stripe-class density (invoice
  + subscription detail).** Row bottom-padding `pb-4` → `pb-2`. ~30%
  shorter overall, no logic change. Bundled with ADR-024 documenting
  the canonical timeline
  primitives (cursor pagination, ETag, source field, raw-payload
  disclosure, server-derived `simulated`, generalized coalescer,
  dedicated events table) as **deferred-with-triggers** — Velox stays
  on the multi-source-assembly pattern that Lago / Recurly /
  QuickBooks all use; promotes individual primitives only when a
  named trigger fires. Prevents reinventing the design later under
  pressure.

### Removed

- **Paid + Voided banners on the operator invoice detail page.** Both
  rendered information already carried by the header status pill
  (Paid/Voided badge + date) and the activity timeline (which under
  ADR-020 coalesces "Invoice paid · via Visa •••• 4242" into a single
  row with the PI ID). Stripe / Lago / Recurly all follow the same
  pattern: banners are reserved for attention-required states
  (declined, dunning, tax pending); terminal states trust the header.
  The hosted invoice page (customer-facing) still shows its
  "Paid on {date}" confirmation — that audience needs the strong
  done-state signal; operators don't.

### Fixed

- **Pending plan-change applies on the cycle boundary, not on
  wall-clock drift.** The billing engine's pending-change gate
  compared `PendingPlanEffectiveAt` against `now` (wall-clock or
  test-clock `frozen_time`). When the engine ran after the
  effective date but processed an earlier unbilled cycle (scheduler
  outage + catch-up, OR test-clock advancing across multiple
  periods), the change was applied retroactively to that earlier
  cycle's invoice. Most visible during test-clock simulations:
  advancing a clock across a year would apply a mid-year scheduled
  change to ALL prior period invoices, not just post-effective
  ones. Stripe Billing applies scheduled phases on phase
  boundaries, not on calendar drift; Velox now matches. Gate
  switched to `CurrentBillingPeriodEnd` (in-memory check + the SQL
  filter inside `ApplyDuePendingItemPlansAtomic`); falls back to
  `now` only when period_end is unset (rare). Closes the
  long-standing `TestRunCycle_SkipsPendingChangeNotYetDue` failure.

- **Retry Payment no longer hits a Stripe idempotency-key 409 on the
  second attempt.** Pre-fix `ChargeInvoice` used
  `velox_inv_{INV}` as the idempotency key but stamped
  `velox_request_id` (per-HTTP-request chi ID) into PI metadata —
  Stripe sees same key + different params and rejects with
  "Keys for idempotent requests can only be used with the same
  parameters they were first used with." Now the key is
  `velox_inv_{INV}_{UPDATED_AT_NS}`: each genuine retry advances
  `inv.UpdatedAt` (UpdatePayment fires after every prior failure),
  yielding a fresh key. Two concurrent callers (scheduler tick +
  operator Retry click) reading the same UpdatedAt still dedupe
  correctly — Stripe returns the same PI, no double-charge. Dropped
  `velox_request_id` from metadata; log correlation goes through the
  recorded `stripe_payment_intent_id` instead.

- **Post-decline rendering: one banner per state, distinct emails per
  lifecycle moment, interactive flow suppression.** Pre-fix every
  card-decline produced four overlapping surfaces: a unified attention
  banner with only `[retry_payment]`, a hardcoded "Payment failed"
  `<Card>` block (pre-ADR-009 leftover) repeating the same headline
  with `[Update Payment Method, Retry Payment]`, two timeline rows
  labeled "Customer notified — payment method required" (one from
  finalize-no-PM, one from post-decline), and an email body identical
  for both lifecycle moments. Rolled into one cleanup:
  - **Attention banner**: new `update_payment_method` action; emitted
    primary on `payment.declined` (the card on file is broken,
    retrying with the same card declines again). `retry_payment`
    secondary, wired to the existing Collect endpoint.
  - **Hardcoded duplicate banner deleted** (`InvoiceDetail.tsx:893-937`);
    the unified banner is now the single surface.
  - **Email types split**: `payment_update_request` →
    `payment_setup_request` (finalize-no-PM only); post-decline now
    routes to the existing `payment_failed` template (same as dunning
    retry warnings, points at the long-lived hosted invoice URL).
    Two distinct timeline labels: "Customer notified — set up payment
    method" vs "Payment-failed email sent to customer".
  - **Interactive Pay decline suppresses the email**: PI metadata
    `velox_purpose=hosted_invoice_pay` → no email sent (customer just
    saw the failure inline on the hosted page). Auto-charge declines
    still email. ADR-023.

- **Customer detail Payment Method panel always shows the saved card
  during an Update flow (atomic swap, no between-cards empty state).**
  Pre-fix the panel gated card-rendering on `setup_status==='ready'`,
  so kicking off an Update flow (which flips the row to `pending`
  while preserving `card_last4` server-side) replaced the existing
  card UI with "Awaiting payment method setup" — making it look like
  the customer had no PM at all. Now the panel renders the card
  whenever `card_last4` is populated, with an amber "Update in
  progress" sub-line when `setup_status='pending'`. Matches Stripe /
  Recurly / Lago dashboards: the new card swaps in atomically when
  `checkout.session.completed` fires; the operator never sees a gap.

- **Stripe Checkout cancel no longer lands on a blank page; return
  URLs are now contextual instead of env-driven globals.** Pre-fix
  `STRIPE_CHECKOUT_CANCEL_URL=http://localhost:5173/checkout/cancel`
  in `.env.example` was stamped on every operator-side
  `/checkout/setup` session, and the SPA had no matching route — blank
  page on cancel. The deeper issue: every other Checkout flow
  (hosted-invoice Pay, customer-portal update PM, public update via
  token) already used contextual return URLs (`return_url` body field,
  defaulted to the source page); only `/checkout/setup` was the
  outlier reading from env. Now `setupRequest` accepts `return_url`,
  the SPA passes its current page, the handler appends
  `?payment=success` / `?payment=cancel`, and CustomerDetail.tsx reads
  the query param to toast + refetch `payment_setup`. Removed the env
  vars from `.env.example`, `deploy/compose/docker-compose.yml`,
  `deploy/compose/.env.example`, the `NewCheckoutHandler` constructor
  signature, and the now-unused `/checkout/success` SPA route +
  `CheckoutSuccess.tsx` page. ADR-022.

- **Hosted-invoice Pay flow saves the card to the customer's Stripe
  customer.** Pre-fix the Checkout session opened from the Pay button
  was created in `mode=payment` with no `Customer` and no
  `setup_future_usage` — guest checkout, PM consumed by the one-shot
  PaymentIntent and detached. Operator-reported: end customer ticked
  "save card," paid the invoice, but Velox kept showing "no payment
  method" and later invoices fell into dunning instead of
  auto-charging. `hostedInvoiceStripeAdapter` now resolves (or lazily
  mints) the Stripe customer via `paymentmethods.StripeAdapter.EnsureStripeCustomer`
  — the same helper the customer portal uses, so portal-created and
  Pay-flow customers share a Stripe customer record — and passes
  `Customer: stripeCustomerID` + `PaymentIntentData.SetupFutureUsage='off_session'`
  on the Checkout session. Canonical Stripe pattern from
  [Save card during payment](https://docs.stripe.com/payments/save-during-payment).
  `handleCheckoutCompleted` (already conditional from ADR-020) now
  finds the attached PM and upserts `customer_payment_setups` with
  `setup_status='ready'` + card details. ADR-021.

- **Payment-update email link now tokenized everywhere.** Pre-fix
  the no-PM-at-finalize email path
  (`noPaymentMethodNotifierAdapter`, ADR-013) built a tokenless
  URL of the form `?invoice_id=…&customer_id=…`. The SPA enforces
  `?token=…` and rejected it with "Link expired or invalid · No
  payment update token provided" — operator-reported. The adapter
  now holds a `payment.TokenService` reference, mints a single-use
  token, and builds the query-style
  `${PAYMENT_UPDATE_URL}?token=${rawToken}` URL the SPA expects.
  The charge-failure email path (`stripe.handlePaymentFailed`)
  had a tokenized branch but its fallback (when `tokenSvc==nil`)
  also produced a broken URL — removed. Both paths now refuse to
  send if TokenService isn't wired, rather than email a
  permanently-broken link. Also: `stripe.handlePaymentFailed` URL
  shape was path-style (`${URL}/${token}`) — fixed to query-style
  to match the SPA route.

- **Tax-retry reconciler stops error-spamming the wrong-mode
  partition.** `Store.ListPendingTaxRetry` now filters by livemode
  read off ctx. Pre-fix the cross-tenant scan returned both
  modes' rows and per-row `RetryTax` failed with "invoice X: not
  found" on whichever mode didn't match the scheduler's per-mode
  loop ctx. Six ERROR lines per scheduler tick, observed in prod
  logs.

- **Tax engine zero-tax fallback satisfies NOT-NULL constraint.**
  When `tenant_settings` is missing for a tenant (orphan tenants
  from earlier failed bootstrap runs), the engine logged
  "proceeding with zero tax" and returned `TaxApplication{}` with
  empty `TaxProvider`. `NullableString` converted that to NULL,
  hitting SQLSTATE 23502 on `invoices.tax_provider`. The
  reconciler retried these orphan invoices every tick. Now
  `TaxApplication{}` defaults `TaxProvider="none"` — any fallback
  path produces a valid row. After one tick the orphan invoices
  flip from `tax_status='pending'` to `'ok'` and exit the
  reconciler scan permanently.

- **Rate limiting is now per-mode for cookie-session callers.**
  `rateLimitKey` for cookie-session traffic was `tenant:<id>` — a
  single bucket shared by test and live operations from the same
  tenant. One operator's heavy test exploration could throttle
  another operator's legitimate live action on the same dashboard.
  Now keyed `tenant:<id>:test` / `tenant:<id>:live` so the two modes
  have independent quotas. API-key callers were already implicitly
  per-mode (each `vlx_secret_test_*` / `vlx_secret_live_*` is a
  separate key with a separate `key_id` bucket). Industry consensus
  pattern — Stripe, Twilio, Lago all keep mode buckets independent.

- **Mode toggle: cross-tab sync, URL-cursor strip, per-mode invoice
  numbering.** Three issues found during a brutal audit of test↔live
  toggle behaviour:
  1. **Cross-tab desync (production safety bug).** Tab A toggling
     test→live left Tab B's React state stuck on test while the
     shared session cookie flipped to live → Tab B refetched live
     data under an amber TEST pill, and an action click could mutate
     live data. AuthContext now broadcasts mode flips via
     `BroadcastChannel('velox-mode')` (with a `localStorage`
     `storage` event fallback for browsers without
     BroadcastChannel). Other tabs receive the message and update
     their `livemode` state without re-calling
     `/v1/dashboard/mode`.
  2. **URL query params survived mode toggle.** Pagination cursors
     (`?cursor=cus_test_xxx`) and filters reference the prior mode's
     dataset; carrying them across produced empty pages or
     mode-mismatched filters. Toggle now `navigate(pathname)` to
     drop the search string while keeping the path so detail-page
     IDs surface the existing "Not found" branch.
  3. **Shared invoice + credit-note sequence counter.**
     `tenant_settings.invoice_next_seq` and `credit_note_next_seq`
     were per-tenant, not per-mode — test exploration burned
     numbers that should have belonged to live invoices, leaving
     gaps like INV-000001…000005, then live's first real invoice
     was INV-000006 with no record-keeping reason. Migration 0072
     splits both counters into `_test` + `_live` columns; backfill
     copies the prior shared value to both so no per-mode sequence
     ever regresses. `NextInvoiceNumber` /
     `NextCreditNoteNumber` now read `postgres.Livemode(ctx)` to
     pick the active column. The
     `UNIQUE (tenant_id, livemode, invoice_number)` constraint
     from migration 0020 already permits both modes to start at
     the same value without clash.

  Mode-in-URL (Stripe-style `/test/` path prefix) was scoped during
  this work and explicitly deferred — Stripe does it but Lago, Orb
  and the rest of the OSS-billing field don't, so it's
  Stripe-tier polish rather than industry-standard. Cross-tab
  safety is solved without it.

- **Usage page stat cards + meter breakdown now reflect server-side
  filtered totals, not the visible page.** Before, the four cards on
  `/usage` (Total Events, Page Units, Active Meters, Active Customers)
  and the "Usage by Meter" horizontal-bar breakdown were computed by
  reducing only the 25-event page already fetched. Paging from page 1
  to page 2 silently shifted every number, and applying a customer
  filter only narrowed the bars to whichever meters happened to be
  represented in that page — both behaviours misled operators about
  the true scope. New endpoint `GET /v1/usage-events/aggregate`
  returns `{ total_events, total_units, active_meters,
  active_customers, by_meter[] }` honouring the same `customer_id` /
  `meter_id` / `from` / `to` / `dimensions` filter qs as the list
  endpoint but without `limit` / `offset`. `total_units` and
  `by_meter[].total` are decimal-string-encoded (NUMERIC(38,12) per
  ADR-005) so `0.5 + 0.5 + 0.0001 = "1.0001"` round-trips without
  precision loss. Frontend stat-card label "Page Units" renamed to
  "Total Units" to match. Closes #7.

### Documentation

- **README + design RFCs truthed up to reflect shipped state.**
  README's "Status" blockquote claiming multi-dim meters land 2026-05-08
  and the `POST /v1/usage-events` endpoint accepts integer quantity only
  was removed — multi-dim meters shipped 2026-04-25 and the endpoint has
  taken decimal-string quantities (`NUMERIC(38, 12)`) since migration
  `0054_usage_events_numeric_quantity`. The 13-week roadmap table that
  mixed shipped and not-yet-shipped rows under the same heading was
  replaced with a forward-looking `## Roadmap` section split into
  `### Recently shipped` (multi-dim meters, pricing recipes, quickstart
  wizard, `create_preview`, billing thresholds, billing alerts, cost
  dashboard, plan-migration UI, bulk ops, live event stream,
  `velox-import` CLI) and `### In flight` (Helm + Terraform self-host
  packaging, compliance posture, first design-partner production
  cutover) so the README states what's shipped today and what's next,
  not what was planned three months ago. Self-host one-liner softened
  from "five-minute self-host coming soon" to a concrete pointer at
  `docs/self-host/postgres-backup.md` for the backup playbook that
  already exists, with the Helm chart + Terraform module called out as
  "under construction." Five design RFCs (`docs/design-multi-dim-meters.md`,
  `docs/design-recipes.md`, `docs/design-billing-thresholds.md`,
  `docs/design-billing-alerts.md`, `docs/design-customer-usage.md`) got
  a `Status: Shipped <date> — see CHANGELOG.md` header plus a preserved
  "kept as historical RFC" italic note so the design rationale stays
  searchable, but no future reader is misled into thinking the work is
  still pending. Implementation checklists in those RFCs flipped from
  `[ ]` to `[x]` and dropped now-stale "(Week N)" qualifiers, broken
  pointers to the moved-out 90-day plan, and Track A / Track B
  internal-plan vocabulary that referenced the now-private velox-ops
  repo. The blog post `docs/blog/2026-04-stripe-meter-api-ai-workloads.md`
  got a 2026-04-28 update note at the top calling out that the
  multi-dim implementation it describes is now live in `main` and the
  endpoints in the post are the production contract — past-tensed the
  "Velox is implementing this" line further down so the post reads as
  shipped retrospective, not aspirational design.

- **Public/private repo split for internal-only docs.** Twelve internal
  planning + marketing docs moved out of the public repo to a private
  `sagarsuperuser/velox-ops` repo so the public surface only carries
  material consumers and operators actually need. Removed:
  `docs/90-day-plan.md`, `docs/positioning.md`, `docs/parallel-work.md`,
  `docs/parallel-handoff.md`, `docs/design-partner-readiness-plan.md`,
  `docs/design-partner-onboarding.md`, `docs/phase2-hardening-plan.md`,
  `docs/stripe-tier2-gap-analysis.md`, `docs/migration-safety-findings.md`,
  and the three `docs/marketing/` files (cold-email-templates,
  demo-script, outreach-list). Public repo retains: the design RFCs
  (`docs/design-*.md`), operator runbooks (`docs/ops/`), compliance
  mapping (`docs/compliance/`), the customer migration guide, the public
  blog, and self-host docs. Cross-references in retained docs rewritten
  to either drop the broken link (where the context is self-contained)
  or note that the supporting material lives in the internal repo (where
  an auditor or design partner needs the pointer). README.md trimmed in
  the same shape — no more "see the 90-day plan" or "see positioning"
  callouts. Pattern matches Stripe / Vercel / Linear / Supabase /
  PostHog: strategy and partner playbooks live behind the contributor
  boundary, not in the public-product surface.

- **Five new public docs pages — Account setup, Errors, Rate limits,
  Glossary, Troubleshooting.** Closes the integration-time gaps the
  existing Quickstart / Webhooks / Idempotency / Recipes / Embed pages
  left open: the post-deploy seven-step setup walkthrough (bootstrap →
  sign-in & company profile → connect Stripe test → first plan + meter
  → webhook endpoint → first customer + subscription → trigger billing
  & verify), the canonical error envelope reference (response shape,
  HTTP status table, common codes including the full coupon error code
  list, error-handling code with `switch (error.code)`), the rate-limit
  contract (per-key 100/min, per-tenant-session 100/min, per-public-IP
  60/min, response headers, exempt endpoints, retry code, batch-endpoint
  hint, fail-open-in-dev / fail-closed-in-prod note), the glossary (28
  terms grouped Core concepts / Billing & lifecycle / Platform &
  security with stable definitions for tenant / livemode / meter /
  pricing rule / dimension / RLS / idempotency-key / webhook secret
  rotation / hosted invoice page / cost dashboard embed / test clock /
  tax provider), and the troubleshooting runbook (nine integration-time
  symptoms with Symptom / Why / Check or Fix subsections — webhook
  events not firing, decimal usage quantity rejected, subscription
  stuck in trialing, usage event accepted but not on invoice, 422
  idempotency_error on retry, billing_setup_incomplete on charge,
  coupon won't apply, bootstrap returns 403, webhook signature fails).
  Every fact is grounded in the current source: error codes from
  `internal/api/respond/respond.go` and `internal/errs/domain.go`,
  coupon codes from `internal/coupon/errors.go`, rate-limit logic from
  `internal/api/middleware/ratelimit.go`, idempotency contract from
  `internal/api/middleware/idempotency.go`, bootstrap response shape
  from `internal/tenant/bootstrap.go`. `DocsShell` nav now groups
  Guides (Quickstart, Account setup, Pricing recipes, Webhooks,
  Idempotency & retries, Troubleshooting), Embeds (Cost dashboard),
  and Reference (API reference, Errors, Rate limits, Glossary). New
  routes wired into `web-v2/src/main.tsx` as lazy-loaded chunks.

### Added

- **Persist Stripe `taxability_reason` per invoice line and surface on
  the dashboard + PDF.** Stripe Tax returns a structured
  `taxability_reason` on every per-line `tax_breakdown` entry (`standard_rated`,
  `reverse_charge`, `not_collecting`, `product_exempt`, `customer_exempt`,
  `excluded_territory`, `jurisdiction_unsupported`, `not_subject_to_tax`,
  `reduced_rated`, `zero_rated`). Velox previously dropped the per-line
  value at the mapper layer, leaving two zero-tax invoices indistinguishable
  on disclosure even though they need different legends — `reverse_charge`
  needs the EU Art. 196 disclosure, `not_collecting` (merchant has no
  registration) needs none, and `customer_exempt` needs the
  exemption-certificate disclosure. Migration `0065` adds an opaque
  `invoice_line_items.tax_reason TEXT NOT NULL DEFAULT ''` column; the
  Stripe mapper, postgres store (5 SQL touch-points), and billing-engine
  per-line apply loop all round-trip the value end-to-end. The dashboard
  invoice-detail page renders a small outline badge under each non-trivial
  line item (`standard_rated` and empty are deliberately not badged — the
  Tax column already conveys the default-path case). The PDF renderer
  appends a new exemption legend below the existing reverse-charge legend
  when at least one line is `customer_exempt` or `product_exempt`; the
  reverse-charge legend itself still derives from the calc-level
  `inv.TaxReverseCharge` so issue #9's India/EU split is preserved
  untouched. Closes #4.

- **Recipe uninstall action on the Pricing recipes page.** Recipe cards
  flagged "Installed" now expose an Uninstall button in the configure
  dialog footer (left side, destructive-coloured, separate from the
  install/preview actions on the right). Click → AlertDialog confirms
  with explicit copy that uninstall is **no-cascade**: the plans, meters,
  rating rules, dunning policy, and webhook endpoint that the recipe
  originally created stay in place — only the `recipe_instances` row
  drops, which flips the card back to "not installed" so the operator
  can re-install with different overrides. Cascade-delete is
  deliberately not supported because plans may have live subscriptions,
  and silent cascade would lose billing data; the dialog spells this
  out so operators don't expect different behaviour from the
  Stripe-products / Lago-plans uninstall they may be coming from. On
  success the recipes query invalidates so the badge flips immediately
  without a manual refresh. Closes the Track B gap on
  `api.deleteRecipeInstance` — the method existed in
  `web-v2/src/lib/api.ts` and the backend has shipped the
  `DELETE /v1/recipes/instances/{id}` endpoint since the recipes API
  landed (Week 3), but the dashboard had no caller, leaving operators
  who tried out a recipe and then changed their mind with no clean way
  to remove the recipe link short of editing the database directly.

- **Edit-coupon dialog on the coupon detail page.** Operators can now extend
  a coupon's expiry, raise the redemption cap, tighten or loosen restrictions,
  or rename the coupon without archiving and recreating. New Edit button in
  the coupon detail header (between Duplicate and Archive, hidden on archived
  coupons since restoring is the intended path back) opens a dialog covering
  the Stripe-parity mutable subset: `name`, `max_redemptions`, `expires_at`,
  and the three `restrictions` fields (`min_amount_cents`,
  `max_redemptions_per_customer`, `first_time_customer_only`). Discount type,
  discount value, currency, duration, stackability, and plan/customer scope
  are deliberately excluded from the dialog — changing them after the fact
  would silently re-price open subscriptions or invalidate redemptions
  already on the ledger, so the duplicate-and-archive path is the right
  shape. Form pre-populates from the loaded coupon (max_redemptions empty
  = unlimited; expires_at empty = no expiration; restrictions block empty =
  no restrictions). Submit calls the existing `PATCH /v1/coupons/{id}` and
  always sends the full `restrictions` object — the backend's
  full-overwrite semantics on that field mean clearing all three sub-fields
  in the form clears the entire restrictions block in one shot, exactly the
  behaviour an operator expects when they uncheck everything. Validation
  mirrors the backend: name required, max_redemptions and
  max_redemptions_per_customer must be positive integers when set,
  min_amount must be a positive number with up to two decimal places.
  Server-side errors surface inline on the offending field via
  `applyApiError` (with a fallback toast for non-field errors). Closes the
  Track B gap on `api.updateCoupon` — the method existed in
  `web-v2/src/lib/api.ts` but had zero callers, blocking operators from
  any coupon mutation other than archive/unarchive.

- **Billing alerts dashboard page (Track B for the
  `POST/GET/POST archive /v1/billing/alerts` backend that shipped earlier).**
  New `/billing-alerts` page in the Config nav (between Dunning and the rest)
  lists every customer-scoped alert with status filter (active / triggered /
  triggered_for_period / archived / all), threshold rendered as `≥ $X` for
  amount alerts or `≥ N units` for usage alerts (trailing-zero-stripped from
  the NUMERIC(38,12) wire string per ADR-005), recurrence column ("one-time"
  vs "per period"), and last-triggered timestamp. Customer + meter cells link
  through to detail pages when present, fall back to the raw ID otherwise so
  alerts never look broken if a customer or meter is renamed mid-flight.
  New-alert dialog takes title + customer (CustomerCombobox) + optional meter
  (defaults to "all meters / cycle subtotal") + threshold-kind toggle
  (amount in major units → ×100 to cents on submit, OR usage as a
  decimal-string) + recurrence with both modes documented inline. Per-row
  Archive action with an explicit confirm dialog ("recreate the alert if
  you need to track this threshold again"). Validation mirrors the backend:
  exactly-one-of amount_gte / usage_gte (UI enforces via the kind toggle so
  the operator can never send both), positive numbers, customer required.
  Closes the Track B half of the billing-alerts feature — backend has been
  live since FEAT-7 but the dashboard had zero callers of `listBillingAlerts`
  / `createBillingAlert` / `archiveBillingAlert` until now.

- **Spend thresholds dashboard surface (Track B for the
  `PUT/DELETE /v1/subscriptions/{id}/billing-thresholds` backend that
  shipped earlier).** New "Spend thresholds" card on the subscription
  detail page surfaces the configured cap (subtotal cap in the sub's
  currency, plus the per-item usage caps mapped to plan names) with a
  hint that explains the reset-billing-cycle flag in plain English.
  When unset, the empty-state copy explains the cycle scan is the only
  invoice-emitting path, so operators know the default behavior.
  `Set thresholds` / `Edit` opens a dialog that takes amount in major
  units (× 100 to cents on submit) and per-item `usage_gte` as a
  decimal-string (round-trips fractional meter quantities without float
  drift, per ADR-005). `Clear thresholds` button on the destructive
  side wires the DELETE. Validation mirrors backend: at-least-one-of,
  subtotal must be positive, per-item must be a non-negative number.
  Edit is hidden on canceled / archived subs (matches the backend
  reject). Closes the Track B half of the billing-thresholds feature.

- **Typed `<VeloxCostDashboard />` React wrapper at
  `web-v2/src/components/embeds/VeloxCostDashboard.tsx`.** Wraps the
  public iframe with a typed prop interface (`token`, `baseUrl`, `theme`,
  `accent`, `width`, `height`, `className`, `title`) and constructs the
  embed URL for the consumer instead of leaving it to hand-rolled string
  concatenation. Accent is regex-validated client-side too (defence in
  depth — the server already drops anything that isn't 6-digit hex).
  Component lives in the repo today; a standalone `@velox/react` npm
  package and an inline (non-iframe) render mode that hits the public
  JSON endpoint from inside the consumer's React tree are tracked for
  v1.1. The originally-planned `tenantKey customerId` prop signature is
  deferred — that would require a publishable-key auth path on the
  public endpoint which would weaken the per-customer-token isolation
  the iframe currently provides. Closes the Week 5 React-component
  readiness item in `docs/90-day-plan.md` (with the v1.1 carveouts
  spelled out in the same line).

- **Public cost dashboard embed: theme + accent URL params.**
  The `/public/cost-dashboard/:token` route now reads `?theme=light|dark`
  (default dark — most product surfaces look better dark and the iframe
  default should be the polished one) and `?accent=#RRGGBB` (override
  `--primary` + `--ring` so the cycle progress bar and focus rings carry
  the operator's brand). The choice is deterministic from the URL — the
  iframe deliberately ignores localStorage and `prefers-color-scheme`
  so the host page's choice always wins. Accent values that don't match
  the strict 6-digit hex regex are silently ignored. Logic lives in
  `web-v2/src/lib/embedTheme.ts`; applied via `useLayoutEffect` so the
  first frame doesn't flash the wrong theme. Customisation surface
  documented at `/docs/embeds/cost-dashboard`. Closes the Week 5
  "Theming via CSS variables; dark mode by default" item.

- **Design-partner onboarding playbook at `docs/design-partner-onboarding.md`.**
  Partner-facing lifecycle document covering the 12-month free term, weekly
  check-in cadence, sandbox-cutover-week-steady-state phases, communication
  SLAs (< 1 business hour for production incidents, < 4 business hours for
  bugs), incident-response expectations, mutual exits, and the Day-90
  co-branded case-study draft. Distinct from the internal
  `docs/design-partner-readiness-plan.md` (what we ship before invites)
  vs. this (what happens once a partner signs the LOI). Closes the
  Week 8 readiness item.

### Documentation

- **`MANUAL_TEST.md` refresh — 18-migration staleness corrected, 11
  feature-area gaps closed.** The runbook had drifted to migration 46
  while `main` is at 64; entire feature areas had no manual test
  coverage at all. Added: Pricing Recipes section (R1-R5 — list +
  preview, end-to-end instantiation against `anthropic_style`,
  idempotency on `(tenant_id, recipe_key)`, atomic rollback on
  partial failure, dashboard UI flow); Quickstart wizard FLOW U0
  (5-step `/onboarding` — template → Stripe → tax → branding → first
  test invoice with TTFI telemetry); Bulk operations FLOW CU9 (apply
  coupon + schedule cancel with preview-then-commit, idempotency,
  partial-failure handling, history drawer); FLOWs B13 multi-dim
  meters / B14 billing thresholds / B15 billing alerts / B16 plan
  migration tool / I11 invoice preview / I12 one-off invoice
  composer / W4 live webhook stream / CU8 cost-dashboard embed —
  each previously had no row in this runbook. Tier 3 picks up FLOW
  X12 `velox-cli` (sub list / invoice send + JSON wire shape parity
  with server respond.List), FLOW X13 `velox-import` (4-phase Stripe
  pull: customers → products+prices → subscriptions → invoices, with
  idempotency + dry-run + CSV report + SIGINT discipline), and FLOW
  X14 self-host artifacts (Helm chart, Docker Compose, Terraform AWS
  single-EC2 + RDS — picker matrix in `deploy/README.md`, migration
  step explicit per artifact, `docs/self-host/postgres-backup.md`
  cross-referenced for the recovery path that the Week 12 incident
  runbook truth-up depends on). FLOW U10 picks up `/docs/recipes`
  and `/docs/embeds/cost-dashboard` as new public routes
  (`/docs/self-host` deliberately omitted — that lives under
  `docs/` as markdown and is not a public web route today). Migration
  version pointers in S1.1 and the bootstrap section bumped from 35
  / 46 to 64. Stale flows audited and kept — no removals, all old
  flows still describe behaviour that ships in `main`.

- **`docs/90-day-plan.md` truth-up — Week 12 incident-runbook line
  flipped to ✅.** The plan called for "Incident runbook tested
  (failover, rollback to Stripe Billing, billing reconciliation)";
  re-reading the three scenarios against what's already shipped:
  failover is standard process supervision (Helm liveness probe +
  Compose restart policy) plus DB recovery via the pg_basebackup +
  WAL archive guide in `docs/self-host/` — there's no Velox-specific
  failover gymnastics worth a separate runbook; rollback is already
  documented as Phase F of `docs/migration-from-stripe.md`,
  including the honest gap disclosure that there's no
  scheduler-disable env var (recommended pause is
  `kubectl scale --replicas=0`); reconciliation is already
  documented as the Reconciliation Toolkit section of the same doc
  (four copy-paste SQL recipes against the Stripe report API:
  customer count, active subs, paid invoices by month, revenue
  ±0.5%). The "tested in a real production incident" gate stays
  open until a design partner is in production (Week 12 line above)
  — runbook *documentation* was the deliverable this line tracked,
  not running a real incident, and the documentation is in place.

- **Demo recording script at `docs/marketing/demo-script.md`.**
  Shot-by-shot 5-minute screen-recording script for the cold-emailable
  product walkthrough — five-beat shape (hook → multi-dim meters via
  the `anthropic_style` recipe → live `usage_events` ingest with the
  public cost-dashboard reflecting the tick → PaymentIntent direct,
  no Stripe Billing fee → Helm install in your VPC → ask + outro).
  Pre-flight checklist (clean tenant, 1440×900, no-edits one-take
  rule), 4:30 ± 0:20 length target, captions discipline,
  re-record cadence (quarterly + on shape changes), and an explicit
  "what's deliberately not in the script" section pruning the
  anti-patterns (architecture deep-dive, feature inventory,
  competitive table, founder bio, pricing). Pairs with
  `docs/marketing/cold-email-templates.md` — the script is what the
  recipient sees after replying to one of those templates. Records
  against the runbook in `docs/ops/stripe-end-to-end-test.md` once
  the demo tenant is bootstrapped. The plan item that calls for the
  actual recording (Week 4) stays unchecked because a script is not
  a video — this PR is leverage for the maintainer to record from,
  not the recording itself.

- **Stripe end-to-end test runbook at `docs/ops/stripe-end-to-end-test.md`.**
  Eight-step manual smoke for the full Stripe integration surface — local
  Velox boot → connect a test-mode account (`POST /v1/settings/stripe`
  with verify roundtrip via `internal/tenantstripe/service.go`) →
  customer + saved card via Stripe Elements → flat subscription →
  immediate invoice + PaymentIntent (the no-Stripe-Billing-fee code
  path that's the load-bearing claim in `docs/positioning.md`) →
  webhook delivery via `stripe listen` → declined-card dunning loop
  with `4000 0000 0000 0002` → credit-note refund → disconnect.
  Designed to be re-run before each design-partner sandbox cutover
  and after any change touching `internal/payment/`,
  `internal/tenantstripe/`, or `internal/dunning/`. Step 1 (connect +
  Stripe API verify) was validated on 2026-04-27 against the
  maintainer's test account at `f1b2301`; the runbook's Last-Verified
  table records every subsequent run. Steps 2–8 are documented for
  human re-run because they require Stripe Elements (browser) or
  `stripe-cli` (interactive). Closes the Week 11 "test against a
  real Stripe test account end-to-end" readiness item.

- **Cold-email templates at `docs/marketing/cold-email-templates.md`.**
  Four founder-to-founder / CEO-to-VP-Eng templates for the three
  outreach segments in `docs/marketing/outreach-list.md`
  (AI inference, vector DB / regulated infra, dev infra), with
  per-segment subject A/B variants under 60 chars, a one-touch
  reply playbook (Day 5 single bump, Day 14 dead row), and an
  explicit "what's deliberately not here" section pruning the
  obvious anti-patterns (mass-merge sequencers, founder-bio
  paragraphs, urgency framing). Each template references the
  recipient's public posture by sentence two — the cold-email
  baseline that separates a 90-second read from a spam click.
  Pairs with the existing `docs/marketing/outreach-list.md` for
  Week 8's outreach push.

- **`docs/90-day-plan.md` truth-up:** flipped the time-to-first-invoice
  telemetry checkbox to ✅ — evidence is the `TimeToFirstInvoiceSeconds`
  field on `GET /v1/billing/dashboard` (PR #57), computed via the
  audit-log scan of `invoice.finalize` minus `tenants.created_at`. The
  plan item said "audit-log driven" and that is exactly what
  `internal/billing/ttfi_postgres.go` reads from.

- **Public cost dashboard embed page at `/public/cost-dashboard/:token`;
  embed snippet documented at `/docs/embeds/cost-dashboard`.**
  Operators mint a per-customer embed URL from the customer detail page
  (a new "Embed dashboard" button next to the cost dashboard) — the
  click calls `POST /v1/customers/{id}/rotate-cost-dashboard-token`
  (PR #59), shows the resulting public URL in a dialog with a Copy
  button, and offers a Regenerate action that re-mints and invalidates
  the prior URL. The public route is unauth (token is the auth) and
  rides the `hostedInvoiceRL` rate-limit bucket so a runaway embed in
  one tenant cannot starve another. Page renders cycle-to-date
  charges, the in-progress billing window, threshold alerts the
  customer is tracking, and the projected total for the cycle when
  the server populates it. Closes two of the four Week 5 cost-dashboard
  readiness items in `docs/90-day-plan.md`; the typed React component
  (`<VeloxCostDashboard tenantKey customerId />`) and CSS-variable
  theming + dark-mode-by-default remain explicitly deferred to v1.1.
- **Public cost-dashboard token + sanitised read endpoint (backend
  only).** Operators can now mint a long-lived embed token per customer
  via `POST /v1/customers/{id}/rotate-cost-dashboard-token` (returns
  `{token, public_url, customer_id}`); the unauthenticated
  `GET /v1/public/cost-dashboard/{token}` resolves it and returns a
  sanitised projection — current billing-period window, per-meter usage,
  per-currency totals, active subscriptions, and warnings. The
  projection deliberately omits `email`, `display_name`, `external_id`,
  `metadata`, and `billing_profile` so the iframe surface can never leak
  operator-controlled identity even if the URL is shared. Migration
  0064 adds `customers.cost_dashboard_token` with a partial UNIQUE
  index (`WHERE cost_dashboard_token IS NOT NULL`); the column carries
  256 bits of entropy as `vlx_pcd_` + 64 hex chars, so cross-tenant
  probing is infeasible. Token resolution runs cross-tenant under
  `TxBypass` because the handler can't know the tenant before resolving
  the token — once resolved, every downstream read is scoped to that
  customer's tenant. Rotation invalidates the previous URL atomically
  (the column is overwritten in-place) and the audit log records the
  rotation event with a `previous_token_was_unset` flag but never the
  plaintext token. Customers with no active subscription get an
  empty-state response (empty arrays, `billing_period.source =
  no_subscription`) rather than a 5xx, mirroring how Stripe's customer
  portal handles the "no plan yet" case. The public route is mounted
  under the same 60/min/IP rate-limit bucket as `/v1/public/invoices/*`
  with a 30s timeout. Frontend embed lands in a follow-up PR.
- **DocsRecipes page at `/docs/recipes` — copy-pasteable curl for
  instantiating the five built-in pricing recipes.** New in-app docs
  page rendered by the shared `DocsShell` covers what recipes are, the
  five built-in templates (anthropic_style, openai_style,
  replicate_style, b2b_saas_pro, marketplace_gmv) with per-recipe
  summaries (number of meters / rating rules / pricing rules / dunning
  steps / webhook events) plus GitHub permalinks to the YAML, working
  curl examples for `POST /v1/recipes/{key}/instantiate` against
  anthropic_style and b2b_saas_pro, the `POST /v1/recipes/{key}/preview`
  endpoint with response shape, atomicity guarantees (one transaction,
  full rollback on partial failure, RLS-isolated), idempotency on
  `(tenant_id, recipe_key)`, and a customisation pointer to
  `docs/design-recipes.md`. Closes the last unchecked Week 3 item in
  `docs/90-day-plan.md` ("Recipes documented at /docs/recipes with
  copy-pasteable curl"). New nav entry in `DocsShell` Guides group plus
  a discovery card on the docs overview page.
- **Time-to-first-invoice metric on the per-tenant billing dashboard,
  computed from the audit log.** Operators can see how long each
  tenant took from sign-up to their first finalized invoice via the
  new `time_to_first_invoice_seconds` field on the billing dashboard
  payload — derived from the earliest `invoice.finalize` audit-log
  entry minus `tenants.created_at`. Reading from the audit log keeps
  the invoice service's Finalize hot path untouched (no per-finalize
  query overhead) and lets backfills, deletions, or operator-driven
  re-finalizes be answered correctly. The field is omitted from the
  response payload (and from any downstream consumer) until the
  tenant has finalized at least one invoice — that's the "no invoice
  yet" signal. A read failure on either lookup logs a warning and
  leaves the field nil rather than failing the dashboard, since
  MRR/ARR are operator-critical and shouldn't break for a side
  telemetry signal.
- **SOC 2 cheap-gap closeout** (2026-04-27) — four of the SOC 2 gap
  items called out in `docs/compliance/soc2-mapping.md` close in a
  single PR. New `SECURITY.md` at the repo root names
  `security@velox.dev` as the responsible-disclosure inbox with a
  5-business-day triage SLA, 30-day patch-landing target for
  high/critical, explicit in-scope (the binary, schema, dashboard,
  encryption-at-rest + audit-log + RLS guarantees) / out-of-scope
  (operator deploy, third-party services, DoS via traffic flooding)
  list, and a safe-harbor clause for good-faith research — closes the
  largest CC2.3 gap. New `CODE_OF_CONDUCT.md` references Contributor
  Covenant 2.1 verbatim with `conduct@velox.dev` as the reporting
  inbox and a separate `conduct-escalation@velox.dev` for concerns
  about the maintainer — closes CC1.1. New `CODEOWNERS` assigns the
  maintainer as default reviewer for every path with comments outlining
  per-domain ownership splits as contributors join — closes CC5.2.
  `.github/workflows/ci.yml` drops `continue-on-error: true` from the
  `govulncheck` step so a stdlib or module vulnerability with a known
  fix now blocks PR merges (CI uses Go 1.25.9 and reports clean at
  time of merge) — closes CC6.8. `docs/compliance/soc2-mapping.md`
  updated to flip the four closed items in the "top gaps" list and
  rewrite the inline narratives in CC1.1 / CC2.3 / CC5.2 / CC6.8 so
  the doc stays the source of truth on what's done versus what's
  outstanding.

- **Stripe migration guide — Week 11 cutover playbook** (2026-04-27) —
  `docs/migration-from-stripe.md` ships the operator-facing companion
  to the four importer phases (PRs #47 / #51 / #52 / #53). Where
  `docs/design-stripe-importer.md` answers "how does the importer map
  Stripe objects onto Velox?", this guide answers "I run a SaaS today
  on Stripe Billing, my customers are charged tomorrow, and I want to
  be on Velox by month-end without missing an invoice — what's the
  playbook?". Structured as a 14-day calendar with explicit T-14 /
  T-7 / T-0 / T+1 / T+14 milestones plus a Phase F rollback playbook
  for the case the cutover goes wrong. Eight sections: (1)
  pre-migration checklist (Velox tenant provisioned, Stripe
  restricted key with read-only scope, `VELOX_ENCRYPTION_KEY` +
  `VELOX_EMAIL_BIDX_KEY` verified, downstream webhook consumers
  inventoried, dunning + tax + email + payment-method handover
  decisions made), (2) the five importer phases recap with the
  actual `velox-import --resource=…` invocations and the dependency
  order (customers → products → prices → subscriptions → finalized
  invoices, enforced regardless of CLI input order), (3) rehearsal
  run in test mode against `sk_test_…` exercising the same code
  paths the production run will hit, (4) production parallel-run
  cutover playbook with Phase A Prepare / B Initial backfill / C
  Parallel run with webhook shadow / D Cutover / E Stabilize / F
  Rollback — Phase F documents the honest gap that Velox does not
  currently ship a scheduler-disable env var, recommends
  `kubectl scale --replicas=0` on the API deployment as the
  operational pause, and tracks `VELOX_SCHEDULER_DISABLED` as future
  work, (5) reconciliation toolkit with copy-pasteable SQL recipes
  matching Velox totals against the Stripe report API on customer
  count / active subscriptions / paid invoices / revenue for each
  reconciliation gate, (6) webhook redirection strategy (parallel
  webhook posture during T-7 to T-0 with Stripe → ops sink during
  shadow window, swap-over at T-0, rollback procedure for flipping
  primary back to Stripe), (7) known limitations table — Schedules,
  Quotes, Promotion Codes, multi-item subscriptions, graduated /
  tiered prices, metered `usage_type`, Connect, draft / open
  invoices, multi-tax-ID customers, default payment methods + sources
  — each with a documented manual recreation path, (8) FAQ covering
  parallel-run length, mid-cycle subscription handling, refund
  reissue, and the "should I import-then-cutover or
  cutover-then-import?" decision. Cross-refs added from
  `README.md` (new "Migrating from Stripe Billing" subsection
  carrying a copy-pasteable `velox-import` invocation),
  `docs/self-host.md` (new Migrating from Stripe Billing section
  next to Compliance posture), `docs/ops/runbook.md` (new Migration
  section in the table of contents and body, alongside Compliance),
  and `docs/90-day-plan.md` (Week 11 "Migration guide with cutover
  playbook" checkbox flips to closed). Closes the migration-guide
  Week 11 readiness item; the importer + CLI rows are de-facto closed
  by Phase 0/1/2/3 PRs already shipped, leaving only "Test against
  a real Stripe test account end-to-end" as the remaining Week 11
  bullet not yet retired.
- **Stripe importer Phase 3 — finalized invoices** (2026-04-27) —
  `velox-import` now accepts `--resource=invoices` on top of the Phase
  0/1/2 slices. Stripe `Invoice` rows in terminal status (`paid`,
  `void`, `uncollectible`) map onto Velox `invoices` + 1..N
  `invoice_line_items` rows, inserted atomically via
  `CreateWithLineItems` so the imported invoice carries Stripe's
  verbatim totals and `status_transitions` timestamps (`finalized_at` →
  `issued_at`, `paid_at`, `voided_at`) without re-running Velox's
  finalize state machine. Drafts and open invoices are out of scope and
  surface as explanatory `error` rows in the CSV report — operators
  settle / void / write-off in Stripe and re-import after the status
  is terminal. Idempotency anchors on a brand-new column
  `invoices.stripe_invoice_id` (migration `0063_invoices_stripe_invoice_id`)
  with a partial unique index `WHERE stripe_invoice_id IS NOT NULL` so
  the dedup applies only to imported rows; Velox-native invoices keep
  using `invoice_number` (the per-tenant sequential allocator) and
  never collide. Status mapping: `paid` → Velox `paid` +
  `payment_status=succeeded`; `void` → Velox `voided` +
  `payment_status=failed`; `uncollectible` → Velox `voided` +
  `payment_status=failed` with a CSV note (Velox lacks an
  `uncollectible` invoice status; voided is the closest terminal
  state). Billing-reason mapping: `subscription_create`/
  `subscription_cycle`/`subscription_threshold`/`manual` map directly;
  `subscription_update` (proration / mid-cycle change-driven) remaps to
  `manual` with a note; legacy `subscription`, `quote_accept`,
  `automatic_pending_invoice_item_invoice`, `upcoming`, and
  empty/unknown values all remap to `manual` with notes preserving the
  original Stripe distinction. Customer + subscription resolution chain
  matches earlier phases (`customers.external_id = stripe.customer.id`
  for the cus lookup; `subscriptions.code = stripe.subscription.id` for
  the sub lookup); manual invoices with no parent subscription are
  inserted with empty `subscription_id` (the column is nullable). The
  Stripe v82 subscription reference is read from
  `parent.subscription_details.subscription` first, then falls back to
  `lines[0].subscription` / `lines[0].parent.*.subscription` for older
  payloads. Out-of-scope Stripe features (discounts,
  `amount_shipping`, `default_tax_rates`, multi-rate tax splits,
  hosted-invoice URL) are detected and emitted as CSV notes — never
  silently dropped or fabricated. Resources still run in strict
  dependency order regardless of CLI input order: customers → products
  → prices → subscriptions → invoices. Same per-row outcome model
  (`insert` / `skip-equivalent` / `skip-divergent` / `error`) and same
  conservative divergence policy: if Stripe and Velox disagree on any
  of {status, payment_status, customer, subscription, currency,
  billing_reason, totals, billing period, paid/voided/issued
  timestamps}, the importer writes a `skip-divergent` row and never
  overwrites — operators reconcile manually. New code:
  `internal/importstripe/mapper_invoice.go`, `invoice_importer.go`
  plus matching unit tests, driver tests, and an integration test
  against real Postgres. With Phase 3 shipping, `velox-import` covers
  the full Stripe Billing migration path end-to-end (customers,
  catalog, subscriptions, finalized invoices) — a tenant can rehome
  their state in one CLI run.
- **Stripe importer Phase 2 — subscriptions** (2026-04-27) —
  `velox-import` now accepts `--resource=subscriptions` on top of the
  Phase 0 (customers) and Phase 1 (products + prices) slices. Stripe
  `Subscription` rows map onto Velox `subscriptions` rows with
  `subscription.code = stripe_subscription_id` as the import-stable key
  (so reruns are idempotent without a new migration). Phase 2 handles
  single-item subscriptions only — multi-item subs surface as `error`
  rows in the CSV report so operators can recreate them by hand rather
  than getting silent partial data. The mapper preserves Stripe's
  `current_period_start/end`, `billing_cycle_anchor`, `start_date`,
  `trial_start/end`, `cancel_at`, `cancel_at_period_end`, and
  `canceled_at` verbatim, and translates the seven Stripe statuses into
  Velox's narrower set: `active`/`trialing`/`canceled`/`paused` map
  directly, while `past_due` remaps to `active` (Velox tracks dunning on
  invoices, not subscriptions), and `unpaid`/`incomplete`/
  `incomplete_expired`/unknown future states all remap to `canceled`
  with a `Notes` entry surfacing the divergence in the CSV. Customer
  resolution goes through Velox's stripe-id lookup
  (`customers.stripe_customer_id = sub.customer.id`); price → plan
  resolution pulls `item.price.product.id` and looks up the plan whose
  `code` matches that Stripe product id. Out-of-scope Stripe features
  (discounts, schedules, `pause_collection`, `default_tax_rates`,
  `billing_thresholds`) are detected and emitted as CSV notes — the
  importer never silently drops or fabricates them. Resources run in
  strict dependency order regardless of CLI input order: customers →
  products → prices → subscriptions. Per-row outcomes mirror the
  earlier phases: `insert` / `skip-equivalent` / `skip-divergent` /
  `error`. Conservative divergence policy: if Stripe and Velox disagree
  on any of {status, customer, display_name, trial window, cancel
  window, current period}, the importer writes a `skip-divergent` row
  and never overwrites — operators reconcile manually. New code:
  `internal/importstripe/mapper_subscription.go`,
  `subscription_importer.go` plus matching unit tests, driver tests,
  and an integration test against real Postgres. With Phase 2 shipping,
  Velox can now absorb a Stripe Billing tenant's full pricing +
  subscription state in one CLI run; only invoices remain (Phase 3).
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
