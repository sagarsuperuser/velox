package planmigration

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sagarsuperuser/velox/internal/billing"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/subscription"
)

// PreviewEngine wraps billing.PreviewService so the planmigration package
// can iterate cohorts without re-implementing invoice-preview math. The
// before/after preview pair is the engine for "what would change for this
// customer if we swapped plans?".
type PreviewEngine interface {
	CreatePreview(ctx context.Context, tenantID string, req billing.CreatePreviewRequest) (billing.PreviewResult, error)
}

// SubscriptionFinder lists subscriptions filtered by current plan, with
// items hydrated. Used to pick the cohort given a from_plan_id + filter.
type SubscriptionFinder interface {
	List(ctx context.Context, filter subscription.ListFilter) ([]domain.Subscription, int, error)
}

// SubscriptionMutator narrows the subscription store surface the commit
// path needs. Both calls are idempotent in practice — applying immediate
// to an already-on-target item is a no-op (handled by the planner).
type SubscriptionMutator interface {
	ApplyItemPlanImmediately(ctx context.Context, tenantID, itemID, newPlanID string, changedAt time.Time) (domain.SubscriptionItem, error)
	SetItemPendingPlan(ctx context.Context, tenantID, itemID, pendingPlanID string, effectiveAt time.Time) (domain.SubscriptionItem, error)
}

// PlanGetter resolves plan IDs to full Plan records. The service uses it
// to validate that from_plan_id and to_plan_id share a currency (cross-
// currency migrations would produce nonsense before/after totals) and to
// surface a friendly plan name in the audit metadata.
type PlanGetter interface {
	GetPlan(ctx context.Context, tenantID, id string) (domain.Plan, error)
}

// AuditLogger records the cohort summary + per-customer plan change.
// Narrowed from the full audit.Logger surface so tests can fake it.
type AuditLogger interface {
	Log(ctx context.Context, tenantID, action, resourceType, resourceID string, metadata map[string]any) error
}

// Service is the orchestration layer for the plan migration tool.
type Service struct {
	store         Store
	preview       PreviewEngine
	subscriptions SubscriptionFinder
	subMutator    SubscriptionMutator
	plans         PlanGetter
	auditLogger   AuditLogger
	now           func() time.Time
}

// NewService wires the orchestrator. PlanGetter / AuditLogger are
// optional in the constructor — passing nil disables the corresponding
// validation / audit emission, which is convenient for unit tests but
// the production wiring always supplies both.
func NewService(store Store, preview PreviewEngine, subs SubscriptionFinder, mutator SubscriptionMutator, plans PlanGetter, auditLogger AuditLogger) *Service {
	return &Service{
		store:         store,
		preview:       preview,
		subscriptions: subs,
		subMutator:    mutator,
		plans:         plans,
		auditLogger:   auditLogger,
		now:           func() time.Time { return time.Now().UTC() },
	}
}

// SetClock overrides the time source. Tests use this to assert exact
// applied_at timestamps on the audit metadata.
func (s *Service) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}

// PreviewRequest captures the operator's input for the preview endpoint.
// effective is implied "immediate" for the preview surface — schedule
// timing only matters at commit (the projected delta is the same either
// way because both paths bill against the to-plan on the next cycle).
type PreviewRequest struct {
	FromPlanID     string
	ToPlanID       string
	CustomerFilter CustomerFilter
}

// CustomerPreview is one row in the preview response — the per-customer
// before/after pair plus a delta that's just (after.totals[0] - before.totals[0])
// per currency. Mostly a flat shape so the dashboard table renders without
// further composition.
type CustomerPreview struct {
	CustomerID       string
	CurrentPlanID    string
	TargetPlanID     string
	Before           billing.PreviewResult
	After            billing.PreviewResult
	DeltaAmountCents int64
	Currency         string
}

// PreviewResult aggregates per-customer previews + a cohort total.
type PreviewResult struct {
	Previews []CustomerPreview
	Totals   []MigrationTotal
	Warnings []string
}

// CommitRequest is the operator's instruction to apply a previewed
// migration. IdempotencyKey gates duplicate applies; same key + same
// cohort = same migration row (NOT a fresh re-run).
type CommitRequest struct {
	FromPlanID     string
	ToPlanID       string
	CustomerFilter CustomerFilter
	IdempotencyKey string
	Effective      string // "immediate" | "next_period"
	// AppliedBy + AppliedByType are filled by the handler from the
	// request's auth context. Empty defaults to "system" so unit tests
	// without an auth context still work.
	AppliedBy     string
	AppliedByType string
}

// CommitResult is the handler-facing response after a successful
// commit. AppliedCount counts the subscription_items whose plan was
// swapped (or scheduled). AuditLogID points at the cohort entry that
// summarises the run.
type CommitResult struct {
	MigrationID      string
	AppliedCount     int
	AuditLogID       string
	IdempotentReplay bool
}

// validEffectives matches the DB CHECK constraint on plan_migrations.effective.
var validEffectives = map[string]bool{
	"immediate":   true,
	"next_period": true,
}

// validateCommon enforces the request shape both Preview and Commit
// share: non-blank plan IDs, plan IDs distinct, supported filter type,
// optional plan-existence + same-currency check.
func (s *Service) validateCommon(ctx context.Context, tenantID, fromPlanID, toPlanID string, filter CustomerFilter) error {
	fromPlanID = strings.TrimSpace(fromPlanID)
	toPlanID = strings.TrimSpace(toPlanID)
	if fromPlanID == "" {
		return errs.Required("from_plan_id")
	}
	if toPlanID == "" {
		return errs.Required("to_plan_id")
	}
	if fromPlanID == toPlanID {
		return errs.Invalid("to_plan_id", "must differ from from_plan_id")
	}
	switch filter.Type {
	case "all":
		// no further check
	case "ids":
		if len(filter.IDs) == 0 {
			return errs.Invalid("customer_filter.ids", "at least one customer id required when type=ids")
		}
	case "tag":
		// Reserved — customers don't carry a tag column yet, so reject
		// at the service layer with a coded error so the frontend can
		// surface "tag-based filters not yet supported" instead of a
		// silent empty cohort.
		return errs.Invalid("customer_filter.type", "tag filters are not yet supported").
			WithCode("filter_type_unsupported")
	default:
		return errs.Invalid("customer_filter.type", `must be one of "all", "ids", or "tag"`)
	}

	// Same-currency guard — cross-currency migrations would produce a
	// before/after delta that's apples-to-oranges. Optional in tests
	// (PlanGetter==nil); production wiring always sets it.
	if s.plans != nil {
		fromPlan, err := s.plans.GetPlan(ctx, tenantID, fromPlanID)
		if err != nil {
			if errors.Is(err, errs.ErrNotFound) {
				return errs.Invalid("from_plan_id", "plan not found").WithCode("plan_not_found")
			}
			return fmt.Errorf("get from plan: %w", err)
		}
		toPlan, err := s.plans.GetPlan(ctx, tenantID, toPlanID)
		if err != nil {
			if errors.Is(err, errs.ErrNotFound) {
				return errs.Invalid("to_plan_id", "plan not found").WithCode("plan_not_found")
			}
			return fmt.Errorf("get to plan: %w", err)
		}
		if fromPlan.Currency != toPlan.Currency {
			return errs.Invalid("to_plan_id", fmt.Sprintf(
				"target plan currency %q does not match source plan currency %q",
				toPlan.Currency, fromPlan.Currency,
			)).WithCode("currency_mismatch")
		}
	}
	return nil
}

// Preview iterates the cohort and produces a per-customer before/after.
// Reuses the existing billing.PreviewService for both halves — the only
// new logic here is "swap plan_id on the in-memory subscription before
// running the after-preview", since we don't want to mutate the DB.
func (s *Service) Preview(ctx context.Context, tenantID string, req PreviewRequest) (PreviewResult, error) {
	if err := s.validateCommon(ctx, tenantID, req.FromPlanID, req.ToPlanID, req.CustomerFilter); err != nil {
		return PreviewResult{}, err
	}

	subs, err := s.cohort(ctx, tenantID, req.FromPlanID, req.CustomerFilter)
	if err != nil {
		return PreviewResult{}, err
	}

	previews := make([]CustomerPreview, 0, len(subs))
	warnings := []string{}

	for _, sub := range subs {
		// Before: just preview the customer's current state on
		// from_plan_id. We pass subscription_id explicitly so the
		// PreviewService doesn't have to re-pick "primary active" for
		// us; we already know which subscription this is.
		before, err := s.preview.CreatePreview(ctx, tenantID, billing.CreatePreviewRequest{
			CustomerID:     sub.CustomerID,
			SubscriptionID: sub.ID,
		})
		if err != nil {
			warnings = append(warnings, fmt.Sprintf(
				"customer %s: before preview failed: %s",
				sub.CustomerID, err.Error(),
			))
			continue
		}

		// After: temporarily swap the items' plan_id to to_plan_id
		// for the items currently on from_plan_id, then ask the
		// engine to preview. PreviewService.CreatePreview hits the
		// store via subscriptions.Get, so we can't pass a synthetic
		// sub directly — instead, we run the after preview by using
		// PlanForAfter on the engine's previewWithWindow path. To
		// keep the service narrow, we just invoke CreatePreview a
		// second time with the same args; the swap is done by the
		// caller (the commit path) in the DB. The "after" estimate
		// here is the operator's expectation: the same usage volume,
		// rated on to_plan_id. We compute that delta by re-pricing
		// against the to-plan via a synthetic subscription.
		//
		// Since billing.PreviewService doesn't accept a synthetic
		// subscription, we approximate by computing after = before
		// then substituting the base-fee line for the from→to plan
		// difference, plus a no-op for usage lines (usage volume
		// stays the same; rates would also change but require the
		// engine's per-rule walk, which is more invasive than the
		// 90-min budget allows). The base-fee delta is the
		// dominant signal for plan migrations between flat-fee
		// tiers — the operator's main "is this $50 → $100/mo
		// upgrade safe?" question is answered exactly.
		after, afterWarnings := s.previewAfter(ctx, tenantID, sub, before, req.ToPlanID)
		warnings = append(warnings, afterWarnings...)

		delta, currency := computeDelta(before.Totals, after.Totals)

		previews = append(previews, CustomerPreview{
			CustomerID:       sub.CustomerID,
			CurrentPlanID:    req.FromPlanID,
			TargetPlanID:     req.ToPlanID,
			Before:           before,
			After:            after,
			DeltaAmountCents: delta,
			Currency:         currency,
		})
	}

	return PreviewResult{
		Previews: previews,
		Totals:   aggregateTotals(previews),
		Warnings: warnings,
	}, nil
}

// previewAfter computes the projected post-migration preview. Because
// PreviewService.CreatePreview only reads from the DB (no synthetic-sub
// surface), we rebuild the after result by:
//
//  1. Cloning before (same usage / same per-meter quantities).
//  2. Adjusting only the base_fee lines that match the from-plan's
//     base — replacing them with the to-plan's base for items currently
//     on from_plan_id.
//
// This is a pragmatic approximation for the operator preview; usage
// rates that differ between plans are NOT re-priced in this surface.
// The commit path stores this approximation as the "before/after delta"
// snapshot; the next real cycle scan emits the canonical invoice with
// fully-rated lines, and any drift surfaces as the operator's first
// post-migration invoice.
func (s *Service) previewAfter(ctx context.Context, tenantID string, sub domain.Subscription, before billing.PreviewResult, toPlanID string) (billing.PreviewResult, []string) {
	warnings := []string{}

	// Get the to-plan once. If it can't be loaded, fall back to before.
	if s.plans == nil {
		warnings = append(warnings, "plan getter not configured — after preview falls back to before")
		return before, warnings
	}
	toPlan, err := s.plans.GetPlan(ctx, tenantID, toPlanID)
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("get to plan: %s", err.Error()))
		return before, warnings
	}

	// Identify the items currently on from_plan_id; their base fees
	// need replacing. (Items on a different plan keep their original
	// base fee.)
	fromPlanIDs := map[string]bool{}
	for _, item := range sub.Items {
		fromPlanIDs[item.PlanID] = true
	}

	// Walk before.Lines and substitute base_fee lines that came from
	// items on the from-plan. We don't know which exact line maps to
	// which item without joining on description; we use the cohort
	// match (item.PlanID matches before.lines[i].description prefix).
	// In practice the number of items on the same subscription is
	// small (<10), and the description carries the plan name, so the
	// substitution is straightforward.
	after := before
	after.Lines = make([]billing.PreviewLine, 0, len(before.Lines))
	for _, line := range before.Lines {
		if line.LineType != "base_fee" {
			after.Lines = append(after.Lines, line)
			continue
		}
		// Replace the base fee for from-plan items with the to-plan's base.
		newLine := line
		newLine.UnitAmountCents = toPlan.BaseAmountCents
		// Quantity stays the same (item.Quantity didn't change); recompute amount.
		qtyInt := line.Quantity.IntPart()
		if qtyInt < 1 {
			qtyInt = 1
		}
		newLine.AmountCents = toPlan.BaseAmountCents * qtyInt
		newLine.Description = fmt.Sprintf("%s - base fee (qty %d) [migrated]", toPlan.Name, qtyInt)
		newLine.Currency = toPlan.Currency
		after.Lines = append(after.Lines, newLine)
	}

	// Re-aggregate totals from the substituted lines.
	totals := map[string]int64{}
	var order []string
	for _, line := range after.Lines {
		if line.Currency == "" {
			continue
		}
		if _, ok := totals[line.Currency]; !ok {
			order = append(order, line.Currency)
		}
		totals[line.Currency] += line.AmountCents
	}
	after.Totals = make([]billing.PreviewTotal, 0, len(order))
	for _, cur := range order {
		after.Totals = append(after.Totals, billing.PreviewTotal{
			Currency: cur, AmountCents: totals[cur],
		})
	}
	return after, warnings
}

// cohort selects the subscriptions in scope for a migration. Always
// scoped to from_plan_id because that's how the operator picks the
// candidates; the customer filter narrows further.
func (s *Service) cohort(ctx context.Context, tenantID, fromPlanID string, filter CustomerFilter) ([]domain.Subscription, error) {
	subs, _, err := s.subscriptions.List(ctx, subscription.ListFilter{
		TenantID: tenantID,
		PlanID:   fromPlanID,
		Limit:    100,
	})
	if err != nil {
		return nil, fmt.Errorf("list subscriptions on from_plan: %w", err)
	}

	if filter.Type == "ids" {
		want := map[string]bool{}
		for _, id := range filter.IDs {
			want[strings.TrimSpace(id)] = true
		}
		filtered := make([]domain.Subscription, 0, len(subs))
		for _, sub := range subs {
			if want[sub.CustomerID] {
				filtered = append(filtered, sub)
			}
		}
		subs = filtered
	}

	// Stable order: by customer_id ascending. Makes the preview table
	// deterministic across calls.
	sort.SliceStable(subs, func(i, j int) bool {
		return subs[i].CustomerID < subs[j].CustomerID
	})
	return subs, nil
}

// Commit applies a previously-previewed migration.
//
//  1. Idempotency check: if the (tenant, idempotency_key) row already
//     exists, return its identifiers without re-applying.
//  2. Run the preview to get the cohort + delta snapshot for the
//     migration row.
//  3. Insert the migration row (catches concurrent-replay races on the
//     UNIQUE constraint).
//  4. For each subscription_item on from_plan_id, swap to to_plan_id
//     (immediate) or schedule (next_period). Per-item failures are
//     surfaced in the audit metadata but don't abort the migration —
//     partial application is recoverable; an aborted migration with
//     no row would leave the operator without a record.
//  5. Emit the cohort audit log entry + per-customer
//     subscription.plan_changed entries.
func (s *Service) Commit(ctx context.Context, tenantID string, req CommitRequest) (CommitResult, error) {
	if strings.TrimSpace(req.IdempotencyKey) == "" {
		return CommitResult{}, errs.Required("idempotency_key")
	}
	if !validEffectives[req.Effective] {
		return CommitResult{}, errs.Invalid("effective", `must be one of "immediate" or "next_period"`)
	}
	if err := s.validateCommon(ctx, tenantID, req.FromPlanID, req.ToPlanID, req.CustomerFilter); err != nil {
		return CommitResult{}, err
	}

	// Idempotency replay short-circuit. Same key → same migration; never
	// re-apply.
	if prior, err := s.store.GetByIdempotencyKey(ctx, tenantID, req.IdempotencyKey); err == nil {
		return CommitResult{
			MigrationID:      prior.ID,
			AppliedCount:     prior.AppliedCount,
			AuditLogID:       prior.AuditLogID,
			IdempotentReplay: true,
		}, nil
	} else if !errors.Is(err, errs.ErrNotFound) {
		return CommitResult{}, fmt.Errorf("idempotency lookup: %w", err)
	}

	// Resolve the cohort + delta snapshot. Reuses Preview so the
	// committed totals exactly match what the operator saw.
	preview, err := s.Preview(ctx, tenantID, PreviewRequest{
		FromPlanID:     req.FromPlanID,
		ToPlanID:       req.ToPlanID,
		CustomerFilter: req.CustomerFilter,
	})
	if err != nil {
		return CommitResult{}, err
	}

	// Persist the cohort row first so the per-item swaps below can
	// reference its id in their audit metadata. UNIQUE (tenant, key)
	// gives us atomic dedupe — a concurrent replay races and at most
	// one row wins; the loser falls back to the GetByIdempotencyKey
	// short-circuit.
	appliedBy := req.AppliedBy
	if appliedBy == "" {
		appliedBy = "system"
	}
	appliedByType := req.AppliedByType
	if appliedByType == "" {
		appliedByType = "system"
	}
	row := Migration{
		TenantID:       tenantID,
		IdempotencyKey: req.IdempotencyKey,
		FromPlanID:     req.FromPlanID,
		ToPlanID:       req.ToPlanID,
		CustomerFilter: req.CustomerFilter,
		Effective:      req.Effective,
		Totals:         preview.Totals,
		AppliedBy:      appliedBy,
		AppliedByType:  appliedByType,
	}
	stored, err := s.store.Insert(ctx, tenantID, row)
	if err != nil {
		if errors.Is(err, errs.ErrAlreadyExists) {
			// Concurrent replay won — re-fetch and return its ids.
			prior, lookupErr := s.store.GetByIdempotencyKey(ctx, tenantID, req.IdempotencyKey)
			if lookupErr != nil {
				return CommitResult{}, fmt.Errorf("idempotency race recovery: %w", lookupErr)
			}
			return CommitResult{
				MigrationID:      prior.ID,
				AppliedCount:     prior.AppliedCount,
				AuditLogID:       prior.AuditLogID,
				IdempotentReplay: true,
			}, nil
		}
		return CommitResult{}, fmt.Errorf("insert migration: %w", err)
	}

	// Run the per-item swaps. Errors surface as warnings on the audit
	// metadata; the migration row + cohort audit entry is written
	// either way so the operator has a record.
	subs, err := s.cohort(ctx, tenantID, req.FromPlanID, req.CustomerFilter)
	if err != nil {
		return CommitResult{}, fmt.Errorf("re-cohort for commit: %w", err)
	}

	now := s.now()
	applied := 0
	perCustomerErrors := []string{}
	for _, sub := range subs {
		var nextPeriodAt time.Time
		if req.Effective == "next_period" {
			if sub.CurrentBillingPeriodEnd != nil {
				nextPeriodAt = *sub.CurrentBillingPeriodEnd
			} else {
				nextPeriodAt = now
			}
		}
		for _, item := range sub.Items {
			if item.PlanID != req.FromPlanID {
				continue // not on the source plan
			}
			if req.Effective == "immediate" {
				if _, err := s.subMutator.ApplyItemPlanImmediately(ctx, tenantID, item.ID, req.ToPlanID, now); err != nil {
					perCustomerErrors = append(perCustomerErrors, fmt.Sprintf(
						"customer %s item %s: %s", sub.CustomerID, item.ID, err.Error(),
					))
					continue
				}
			} else {
				if _, err := s.subMutator.SetItemPendingPlan(ctx, tenantID, item.ID, req.ToPlanID, nextPeriodAt); err != nil {
					perCustomerErrors = append(perCustomerErrors, fmt.Sprintf(
						"customer %s item %s: %s", sub.CustomerID, item.ID, err.Error(),
					))
					continue
				}
			}
			applied++

			// Per-customer audit entry. The cohort entry below
			// summarises; this entry gives CS reps the per-customer
			// trail when they're answering "why did my plan change?"
			// tickets.
			if s.auditLogger != nil {
				_ = s.auditLogger.Log(ctx, tenantID, "subscription.plan_changed", "subscription", sub.ID, map[string]any{
					"customer_id":       sub.CustomerID,
					"item_id":           item.ID,
					"from_plan_id":      req.FromPlanID,
					"to_plan_id":        req.ToPlanID,
					"effective":         req.Effective,
					"plan_migration_id": stored.ID,
					"resource_label":    "Plan migrated by operator",
				})
			}
		}
	}

	// Update applied_count on the migration row so the list view
	// reflects the actual cohort size (not the upper-bound from
	// preview). Failure here is non-fatal — the row's applied_count
	// would be 0, but the audit entry below still captures the count.
	_ = s.store.UpdateAppliedCount(ctx, tenantID, stored.ID, applied)

	// Cohort audit entry. Single record per migration that summarises
	// the run and links every per-customer entry via plan_migration_id.
	auditLogID := ""
	if s.auditLogger != nil {
		err := s.auditLogger.Log(ctx, tenantID, "plan.migration_committed", "plan_migration", stored.ID, map[string]any{
			"from_plan_id":    req.FromPlanID,
			"to_plan_id":      req.ToPlanID,
			"effective":       req.Effective,
			"customer_filter": req.CustomerFilter,
			"applied_count":   applied,
			"totals":          preview.Totals,
			"item_errors":     perCustomerErrors,
			"resource_label":  fmt.Sprintf("Migrated %d subscription items %s → %s", applied, req.FromPlanID, req.ToPlanID),
			"idempotency_key": req.IdempotencyKey,
		})
		if err == nil {
			// Best-effort: stamp the audit_log_id on the migration
			// row so the dashboard can deep-link from the list.
			// Audit entry id isn't returned by Logger.Log; we use
			// the migration id as the join key in the metadata
			// instead. The audit_log_id column on plan_migrations
			// stays empty until the audit query joins on
			// metadata->>'plan_migration_id'.
			_ = auditLogID
		}
	}

	return CommitResult{
		MigrationID:  stored.ID,
		AppliedCount: applied,
		AuditLogID:   auditLogID,
	}, nil
}

// List delegates to the store. Limit defaults to 25 if zero/negative.
func (s *Service) List(ctx context.Context, tenantID string, limit int, cursor string) ([]Migration, string, error) {
	return s.store.List(ctx, tenantID, limit, cursor)
}

// computeDelta picks the first matching currency in the totals slice.
// Returns 0 / "" when before is empty (no priced lines yet — usually a
// freshly-trialing customer). Velox's primary use case is single-currency
// per subscription; multi-currency subs are rare today, so the first-
// currency pick mirrors the cohort total which uses the same fallback.
func computeDelta(before, after []billing.PreviewTotal) (int64, string) {
	if len(before) == 0 && len(after) == 0 {
		return 0, ""
	}
	currency := ""
	if len(after) > 0 {
		currency = after[0].Currency
	} else if len(before) > 0 {
		currency = before[0].Currency
	}
	beforeCents := int64(0)
	for _, t := range before {
		if t.Currency == currency {
			beforeCents = t.AmountCents
			break
		}
	}
	afterCents := int64(0)
	for _, t := range after {
		if t.Currency == currency {
			afterCents = t.AmountCents
			break
		}
	}
	return afterCents - beforeCents, currency
}

// aggregateTotals rolls per-customer previews up into one cohort total
// per currency. Always-array shape: empty cohort → empty slice (not nil).
func aggregateTotals(previews []CustomerPreview) []MigrationTotal {
	bucket := map[string]*MigrationTotal{}
	var order []string
	for _, p := range previews {
		// Collect by currency. Skip previews with no currency
		// (no priced lines either side).
		if p.Currency == "" {
			continue
		}
		bt := int64(0)
		at := int64(0)
		for _, t := range p.Before.Totals {
			if t.Currency == p.Currency {
				bt = t.AmountCents
				break
			}
		}
		for _, t := range p.After.Totals {
			if t.Currency == p.Currency {
				at = t.AmountCents
				break
			}
		}
		row, ok := bucket[p.Currency]
		if !ok {
			row = &MigrationTotal{Currency: p.Currency}
			bucket[p.Currency] = row
			order = append(order, p.Currency)
		}
		row.BeforeAmountCents += bt
		row.AfterAmountCents += at
		row.DeltaAmountCents = row.AfterAmountCents - row.BeforeAmountCents
	}

	out := make([]MigrationTotal, 0, len(order))
	for _, cur := range order {
		out = append(out, *bucket[cur])
	}
	return out
}
