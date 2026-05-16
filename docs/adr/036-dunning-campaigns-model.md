# ADR-036: Dunning campaigns model (multi-policy-per-tenant)

**Date:** 2026-05-16
**Status:** Accepted

## Context

The pre-2026-05-16 dunning data model was a tenant-wide singleton:

- `dunning_policies` enforced one row per `(tenant_id, livemode)` via a
  UNIQUE constraint. The "policy" was a tenant-global retry config:
  `(max_retry_attempts, grace_period_days, retry_schedule[],
  final_action)`.
- `customer_dunning_overrides` allowed a per-customer **partial-field
  override** — `max_retry_attempts`, `grace_period_days`, and
  `final_action` could be overridden but `retry_schedule` could not.
  At runtime the engine merged the override fields into the tenant
  policy at `StartDunning` time.

Two structural problems surfaced during the 2026-05 dogfood pass:

1. **Silent retry-schedule fallback** — when an override set
   `max_retry_attempts=5` against a tenant policy with a 2-entry
   `retry_schedule`, the engine's `processRun` used `idx >=
   len(retryIntervals) { idx = len(retryIntervals) - 1 }` to **reuse
   the last interval** for the extra attempts. Operators saw three
   back-to-back 120h retries they hadn't configured. This is a
   `feedback_no_silent_fallbacks` violation — the engine paper-overed
   a misconfiguration instead of surfacing it at save time.

2. **Partial-field override has no industry precedent.** Multi-platform
   verification on 2026-05-16:

   | Platform | Data model | Per-customer mechanism |
   |---|---|---|
   | **Stripe** | Account-level Smart Retries policy (count + window) | Account-level only; "Automations" for customer-segment policies. No per-individual-customer field override. |
   | **Lago** | Named **Campaigns** (`name, threshold, delay, attempts`). | Assign a different campaign to the customer — whole template swap. |
   | **Orb** | **Dunning Schedules** (templates with step offsets) + **Dunning Rules** (eval order, customer/plan matching) | Customer differentiation via rule matching / exclusion. |
   | **Recurly** | Named **Dunning Campaigns**. | Assign a campaign per Plan or Account. |

   All four converge on **named templates** + **assign-to-customer** (or
   rule-match). Velox's partial-field override is a one-off shape with
   no precedent.

Sources:
- [Stripe — Smart Retries](https://docs.stripe.com/billing/revenue-recovery/smart-retries)
- [Lago — Automatic dunning](https://doc.getlago.com/guide/dunning/automatic-dunning)
- [Orb — Advanced dunning](https://docs.withorb.com/invoicing/advanced-dunning)
- [Recurly — Dunning campaigns](https://docs.recurly.com/recurly-subscriptions/docs/dunning-management)

## Decision

Adopt the converging industry shape: **multi-policy-per-tenant + per-
customer assignment**. Per `feedback_stripe_parity_framing`, where
Stripe is structurally limited (account-level only), adopt the
superset architecture used by Lago / Orb / Recurly (named templates +
assignment).

### Data model

1. **`dunning_policies` becomes multi-per-tenant.** Drop the
   `tenant_id, livemode` UNIQUE constraint. Add `is_default boolean
   NOT NULL DEFAULT false` with a partial UNIQUE index enforcing
   exactly one default per `(tenant_id, livemode)`.

2. **`customers.dunning_policy_id`** — nullable FK to
   `dunning_policies.id`. NULL = use the tenant's default policy.
   Updatable via `PATCH /v1/customers/{id}` (`dunning_policy_id`
   field; empty string = clear).

3. **Drop `customer_dunning_overrides`.** Per-customer differentiation
   now flows through the FK assignment.

### Service-layer resolution

`dunning.Service.GetEffectivePolicyForCustomer(ctx, tenantID,
customerID)` resolves:
1. If `customer.dunning_policy_id` is set → load that policy.
2. Else → tenant's `is_default=true` policy.

Called at `StartDunning` time to bind the policy to a new run.

Once a run exists, `processRun` resolves the policy via `run.policy_id`
(persisted at start) so **mid-flight runs stay on their original
policy** even if the customer's assignment changes. The new
assignment takes effect on the NEXT run.

### Save-time validation (closes the silent-fallback gap)

`UpsertPolicy` now rejects when `max_retry_attempts > len(retry_schedule) + 1`.
The runtime `idx >= len(retryIntervals)` reuse-last-interval branch is
removed; if the engine ever encounters an out-of-bounds index it
fails loudly (a schema-invariant violation, not a runtime fallback).

### Handler surface

- `GET /v1/dunning/policies` — list policies with `assigned_customers` counts.
- `POST /v1/dunning/policies` — create.
- `GET /v1/dunning/policies/{id}` — read one.
- `PATCH /v1/dunning/policies/{id}` — update (cannot flip `is_default`).
- `DELETE /v1/dunning/policies/{id}` — delete; refused for the default policy or any policy with assigned customers.
- `POST /v1/dunning/policies/{id}/set-default` — atomic flip.
- `PATCH /v1/customers/{id}` — `dunning_policy_id` field (empty = clear).

Dropped: `GET/PUT /v1/dunning/policy` (singleton),
`GET/PUT/DELETE /v1/dunning/customers/{id}/override`.

### UI

- New `/dunning-policies` admin page — list, create, edit, set-default,
  delete. Schedule shortage warning rendered inline when the form's
  `max_retry_attempts > retry_schedule.length + 1`.
- Customer detail page — "Dunning policy" card shows the effective
  policy (assigned or default) + "Change" button → `AssignDunningPolicyDialog`
  picking from the policy list.
- `/dunning` page no longer hosts the policy editor (Tabs → single
  Runs view; "Manage policies" link to the admin page).

## Why this design

- **Industry convergence.** All four reference platforms verified
  during 2026-05-16 research use the same named-templates pattern.
  Velox follows that shape rather than inventing a Velox-specific
  one.

- **No silent fallback.** The runtime "reuse last interval" branch is
  gone. Misconfiguration is caught at save time with a clear error
  message naming the missing entries.

- **Migration friendliness.** Existing tenants' singleton policies
  flip to `is_default=true` automatically (migration 0086 backfill).
  No customer data was lost because the pre-launch state has zero
  production customers (`project_state_2026_04`); even if it had,
  the old override semantics are subsumable: any
  `(max_retry_attempts, grace_period_days, final_action)` override
  can be expressed as a new named policy with those values plus the
  tenant's `retry_schedule`, then assigned to the customer.

- **Mid-flight stability.** Runs persist their `policy_id` at start
  time and re-resolve via that id (not via the customer's current
  assignment) for the run's lifetime. An operator reassigning a
  customer doesn't yank the rules out from under in-progress retries.

## Alternatives considered

1. **Extend `CustomerDunningOverride` with `retry_schedule []string`.**
   Pure Velox extension; closes the partial-override gap but ships a
   shape no industry platform uses. Rejected — `feedback_stripe_parity_framing`
   says match the converging industry pattern.

2. **Stripe-style account-level-only.** Drop per-customer
   differentiation entirely. Simplest but doesn't fit operators who
   want differentiated dunning for enterprise vs SMB. Rejected — the
   "we want different retry budgets for high-value customers" need is
   a real B2B SaaS workflow.

3. **Orb-style rules engine.** Customer matching by `plan_ids`,
   `payment_method_types`, `exclusion_lists` with rule priority
   ordering. More flexible than direct assignment but adds a rules
   evaluation layer Velox doesn't need today. Deferred — when a
   design partner asks for "assign by plan tier" we can layer
   rule-based assignment on top of the campaigns model without
   schema churn.

## Consequences

### Positive
- Operator workflow matches Stripe / Lago / Recurly mental model.
- Save-time validation prevents the back-to-back-retry surprise.
- Per-customer flexibility without partial-override confusion.
- Path to Orb-style rule-based assignment open if needed.

### Risks / open items
- **Schema migration is destructive** for `customer_dunning_overrides`
  rows. Velox is pre-launch so no real data lost; down-migration
  re-creates the table empty.
- **In-flight run persistence** assumes `dunning_policies` rows are
  never hard-deleted while a run references them. Application-layer
  `DeletePolicy` enforces this (refuses delete if `assigned_customers >
  0`); the schema-level FK `customers.dunning_policy_id ON DELETE
  SET NULL` is belt-and-suspenders for the customer side. There's no
  FK on `invoice_dunning_runs.policy_id` — if a policy is force-
  deleted via raw SQL while runs reference it, `processRun` errors
  cleanly at lookup time. Acceptable for an operator-controlled
  surface.

## References

- ADR-029: fully disjoint test-clock flows
- ADR-030: simulated time everywhere on clock-pinned entities
- ADR-035: per-fact simulated-time anchoring
- Migration 0086: `dunning_policies_campaigns_model`
- Memory: `feedback_stripe_parity_framing`, `feedback_verify_stripe_parity_claims`,
  `feedback_no_silent_fallbacks`, `feedback_reference_platforms`
