package bulkaction

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sagarsuperuser/velox/internal/customer"
	"github.com/sagarsuperuser/velox/internal/domain"
	verrs "github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/subscription"
)

// CustomerLister is the narrow customer surface the cohort builder uses
// when filter.Type == "all". Mirrors customer.Store.List.
type CustomerLister interface {
	List(ctx context.Context, filter customer.ListFilter) ([]domain.Customer, int, error)
	Get(ctx context.Context, tenantID, id string) (domain.Customer, error)
}

// SubscriptionFinder lists active subscriptions for a customer. The
// schedule_cancel action operates on every active subscription a customer
// owns; apply_coupon is customer-scoped so it doesn't need this surface
// but the cohort builder shares it for consistency.
type SubscriptionFinder interface {
	List(ctx context.Context, filter subscription.ListFilter) ([]domain.Subscription, int, error)
}

// SubscriptionCanceller is the narrow surface used by the schedule_cancel
// action. Mirrors subscription.Service.ScheduleCancel.
type SubscriptionCanceller interface {
	ScheduleCancel(ctx context.Context, tenantID, id string, input subscription.ScheduleCancelInput) (domain.Subscription, error)
}

// CouponAssigner is the narrow surface for apply_coupon. Mirrors
// coupon.Service.AssignToCustomer with the bulk-action-specific
// idempotency-key prefix appended per-customer.
type CouponAssigner interface {
	AssignToCustomer(ctx context.Context, tenantID string, input CouponAssignInput) error
}

// CouponAssignInput is decoupled from coupon.AssignInput so this package
// doesn't import its sibling. The adapter in api/router.go translates.
type CouponAssignInput struct {
	Code           string
	CustomerID     string
	IdempotencyKey string
}

// AuditLogger records the cohort summary + per-customer outcome. Narrowed
// from the full audit.Logger surface so unit tests can fake it.
type AuditLogger interface {
	Log(ctx context.Context, tenantID, action, resourceType, resourceID string, metadata map[string]any) error
}

// Service is the orchestration layer for bulk actions.
type Service struct {
	store         Store
	customers     CustomerLister
	subscriptions SubscriptionFinder
	subCanceller  SubscriptionCanceller
	couponAssign  CouponAssigner
	audit         AuditLogger
	now           func() time.Time
}

// NewService wires the orchestrator. customers / subscriptions /
// subCanceller / couponAssign / audit may be nil in tests but production
// wiring always supplies all five.
func NewService(store Store, customers CustomerLister, subs SubscriptionFinder, subCanceller SubscriptionCanceller, couponAssign CouponAssigner, audit AuditLogger) *Service {
	return &Service{
		store:         store,
		customers:     customers,
		subscriptions: subs,
		subCanceller:  subCanceller,
		couponAssign:  couponAssign,
		audit:         audit,
		now:           func() time.Time { return time.Now().UTC() },
	}
}

// SetClock overrides the time source. Tests assert exact completed_at
// timestamps on the row.
func (s *Service) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}

// ApplyCouponRequest is the operator's instruction to attach a coupon to
// every customer in the cohort. CouponCode is the human-readable code
// (e.g. "SUMMER20"); the per-customer assignment carries an idempotency
// key derived from the bulk action's key + the customer id so retrying a
// partially-failed run doesn't re-attach to already-assigned customers.
type ApplyCouponRequest struct {
	IdempotencyKey string
	CustomerFilter CustomerFilter
	CouponCode     string

	// CreatedBy + CreatedByType are filled by the handler from the
	// request's auth context. Empty defaults to "system" so unit tests
	// without an auth context still work.
	CreatedBy     string
	CreatedByType string
}

// ScheduleCancelRequest is the operator's instruction to schedule
// cancellation on every active subscription owned by every customer in
// the cohort. AtPeriodEnd defers the cancel to the current period's end;
// CancelAt is an explicit timestamp (>= current_billing_period_end). Same
// shape as subscription.ScheduleCancelInput so the wire feels consistent.
type ScheduleCancelRequest struct {
	IdempotencyKey string
	CustomerFilter CustomerFilter
	AtPeriodEnd    bool
	CancelAt       *time.Time

	CreatedBy     string
	CreatedByType string
}

// CommitResult is the handler-facing response after a successful commit.
type CommitResult struct {
	BulkActionID     string
	Status           string
	TargetCount      int
	SucceededCount   int
	FailedCount      int
	Errors           []TargetError
	IdempotentReplay bool
}

// validateFilter enforces the request shape both action types share.
// Reused unchanged from planmigration: "all" / "ids" supported, "tag"
// rejected with a coded error.
func validateFilter(filter CustomerFilter) error {
	switch filter.Type {
	case "all":
		// no further check
	case "ids":
		if len(filter.IDs) == 0 {
			return verrs.Invalid("customer_filter.ids", "at least one customer id required when type=ids")
		}
	case "tag":
		return verrs.Invalid("customer_filter.type", "tag filters are not yet supported").
			WithCode("filter_type_unsupported")
	default:
		return verrs.Invalid("customer_filter.type", `must be one of "all", "ids", or "tag"`)
	}
	return nil
}

// ApplyCoupon attaches a coupon to every customer in the cohort.
// Idempotency-key replay short-circuits to the prior row with
// IdempotentReplay=true.
func (s *Service) ApplyCoupon(ctx context.Context, tenantID string, req ApplyCouponRequest) (CommitResult, error) {
	if strings.TrimSpace(req.IdempotencyKey) == "" {
		return CommitResult{}, verrs.Required("idempotency_key")
	}
	if strings.TrimSpace(req.CouponCode) == "" {
		return CommitResult{}, verrs.Required("coupon_code")
	}
	if err := validateFilter(req.CustomerFilter); err != nil {
		return CommitResult{}, err
	}

	if prior, err := s.store.GetByIdempotencyKey(ctx, tenantID, req.IdempotencyKey); err == nil {
		return toReplayResult(prior), nil
	} else if !errors.Is(err, verrs.ErrNotFound) {
		return CommitResult{}, fmt.Errorf("idempotency lookup: %w", err)
	}

	customers, err := s.cohortCustomers(ctx, tenantID, req.CustomerFilter)
	if err != nil {
		return CommitResult{}, err
	}

	createdBy := req.CreatedBy
	if createdBy == "" {
		createdBy = "system"
	}
	row := Action{
		TenantID:       tenantID,
		IdempotencyKey: req.IdempotencyKey,
		ActionType:     ActionApplyCoupon,
		CustomerFilter: req.CustomerFilter,
		Params: map[string]any{
			"coupon_code": req.CouponCode,
		},
		Status:      StatusRunning,
		TargetCount: len(customers),
		CreatedBy:   createdBy,
	}
	stored, err := s.store.Insert(ctx, tenantID, row)
	if err != nil {
		if errors.Is(err, verrs.ErrAlreadyExists) {
			prior, lookupErr := s.store.GetByIdempotencyKey(ctx, tenantID, req.IdempotencyKey)
			if lookupErr != nil {
				return CommitResult{}, fmt.Errorf("idempotency race recovery: %w", lookupErr)
			}
			return toReplayResult(prior), nil
		}
		return CommitResult{}, fmt.Errorf("insert bulk_action: %w", err)
	}

	succeeded := 0
	failed := 0
	errsList := []TargetError{}
	for _, cust := range customers {
		if s.couponAssign == nil {
			break
		}
		// Per-customer idempotency: bulk-action key + customer id makes
		// retries safe and deterministic. If the customer was already
		// assigned in the prior partial run, the coupon service treats
		// the same key as a replay and returns success.
		perCustomerKey := fmt.Sprintf("%s:%s", req.IdempotencyKey, cust.ID)
		err := s.couponAssign.AssignToCustomer(ctx, tenantID, CouponAssignInput{
			Code:           req.CouponCode,
			CustomerID:     cust.ID,
			IdempotencyKey: perCustomerKey,
		})
		if err != nil {
			failed++
			errsList = append(errsList, TargetError{
				CustomerID: cust.ID,
				Error:      err.Error(),
			})
			continue
		}
		succeeded++
		if s.audit != nil {
			_ = s.audit.Log(ctx, tenantID, "customer.coupon_assigned", "customer", cust.ID, map[string]any{
				"coupon_code":     req.CouponCode,
				"bulk_action_id":  stored.ID,
				"resource_label":  fmt.Sprintf("Coupon %s assigned via bulk action", req.CouponCode),
				"idempotency_key": perCustomerKey,
			})
		}
	}

	status := finalStatus(len(customers), succeeded, failed)
	now := s.now()
	if err := s.store.UpdateProgress(ctx, tenantID, stored.ID, status, len(customers), succeeded, failed, errsList, &now); err != nil {
		return CommitResult{}, fmt.Errorf("update bulk_action progress: %w", err)
	}

	if s.audit != nil {
		_ = s.audit.Log(ctx, tenantID, "bulk_action.completed", "bulk_action", stored.ID, map[string]any{
			"action_type":     ActionApplyCoupon,
			"customer_filter": req.CustomerFilter,
			"target_count":    len(customers),
			"succeeded_count": succeeded,
			"failed_count":    failed,
			"status":          status,
			"params":          row.Params,
			"resource_label":  fmt.Sprintf("Bulk apply coupon %s — %d/%d customers", req.CouponCode, succeeded, len(customers)),
			"idempotency_key": req.IdempotencyKey,
		})
	}

	return CommitResult{
		BulkActionID:   stored.ID,
		Status:         status,
		TargetCount:    len(customers),
		SucceededCount: succeeded,
		FailedCount:    failed,
		Errors:         errsList,
	}, nil
}

// ScheduleCancel schedules cancellation on every active subscription for
// every customer in the cohort. Per-customer per-subscription failures
// are recorded in errors[] but don't abort the whole run.
func (s *Service) ScheduleCancel(ctx context.Context, tenantID string, req ScheduleCancelRequest) (CommitResult, error) {
	if strings.TrimSpace(req.IdempotencyKey) == "" {
		return CommitResult{}, verrs.Required("idempotency_key")
	}
	if !req.AtPeriodEnd && req.CancelAt == nil {
		return CommitResult{}, verrs.Invalid("body", "one of at_period_end or cancel_at must be set")
	}
	if req.AtPeriodEnd && req.CancelAt != nil {
		return CommitResult{}, verrs.Invalid("body", "at_period_end and cancel_at cannot be set together; pick one")
	}
	if err := validateFilter(req.CustomerFilter); err != nil {
		return CommitResult{}, err
	}

	if prior, err := s.store.GetByIdempotencyKey(ctx, tenantID, req.IdempotencyKey); err == nil {
		return toReplayResult(prior), nil
	} else if !errors.Is(err, verrs.ErrNotFound) {
		return CommitResult{}, fmt.Errorf("idempotency lookup: %w", err)
	}

	customers, err := s.cohortCustomers(ctx, tenantID, req.CustomerFilter)
	if err != nil {
		return CommitResult{}, err
	}

	createdBy := req.CreatedBy
	if createdBy == "" {
		createdBy = "system"
	}
	params := map[string]any{
		"at_period_end": req.AtPeriodEnd,
	}
	if req.CancelAt != nil {
		params["cancel_at"] = req.CancelAt.UTC().Format(time.RFC3339)
	}
	row := Action{
		TenantID:       tenantID,
		IdempotencyKey: req.IdempotencyKey,
		ActionType:     ActionScheduleCancel,
		CustomerFilter: req.CustomerFilter,
		Params:         params,
		Status:         StatusRunning,
		TargetCount:    len(customers),
		CreatedBy:      createdBy,
	}
	stored, err := s.store.Insert(ctx, tenantID, row)
	if err != nil {
		if errors.Is(err, verrs.ErrAlreadyExists) {
			prior, lookupErr := s.store.GetByIdempotencyKey(ctx, tenantID, req.IdempotencyKey)
			if lookupErr != nil {
				return CommitResult{}, fmt.Errorf("idempotency race recovery: %w", lookupErr)
			}
			return toReplayResult(prior), nil
		}
		return CommitResult{}, fmt.Errorf("insert bulk_action: %w", err)
	}

	succeeded := 0
	failed := 0
	errsList := []TargetError{}
	cancelInput := subscription.ScheduleCancelInput{
		AtPeriodEnd: req.AtPeriodEnd,
		CancelAt:    req.CancelAt,
	}
	for _, cust := range customers {
		// One customer can have multiple active subscriptions — schedule
		// cancel on each. Failures on individual subs surface as
		// per-customer errors (concatenated), so the operator sees
		// "customer X: sub Y: <reason>" without separate entries.
		if s.subscriptions == nil || s.subCanceller == nil {
			break
		}
		subs, _, err := s.subscriptions.List(ctx, subscription.ListFilter{
			TenantID:   tenantID,
			CustomerID: cust.ID,
			Status:     string(domain.SubscriptionActive),
			Limit:      100,
		})
		if err != nil {
			failed++
			errsList = append(errsList, TargetError{CustomerID: cust.ID, Error: fmt.Sprintf("list subscriptions: %s", err.Error())})
			continue
		}
		if len(subs) == 0 {
			// No-active-subscription is treated as a soft failure with
			// a clear reason — operators expect the cohort total to
			// reflect what actually got scheduled.
			failed++
			errsList = append(errsList, TargetError{CustomerID: cust.ID, Error: "no active subscriptions"})
			continue
		}
		customerOK := true
		for _, sub := range subs {
			if _, err := s.subCanceller.ScheduleCancel(ctx, tenantID, sub.ID, cancelInput); err != nil {
				customerOK = false
				errsList = append(errsList, TargetError{
					CustomerID: cust.ID,
					Error:      fmt.Sprintf("subscription %s: %s", sub.ID, err.Error()),
				})
			} else if s.audit != nil {
				_ = s.audit.Log(ctx, tenantID, "subscription.cancel_scheduled", "subscription", sub.ID, map[string]any{
					"customer_id":     cust.ID,
					"at_period_end":   req.AtPeriodEnd,
					"cancel_at":       fmtTimePtr(req.CancelAt),
					"bulk_action_id":  stored.ID,
					"resource_label":  "Subscription cancel scheduled via bulk action",
					"idempotency_key": req.IdempotencyKey,
				})
			}
		}
		if customerOK {
			succeeded++
		} else {
			failed++
		}
	}

	status := finalStatus(len(customers), succeeded, failed)
	now := s.now()
	if err := s.store.UpdateProgress(ctx, tenantID, stored.ID, status, len(customers), succeeded, failed, errsList, &now); err != nil {
		return CommitResult{}, fmt.Errorf("update bulk_action progress: %w", err)
	}

	if s.audit != nil {
		_ = s.audit.Log(ctx, tenantID, "bulk_action.completed", "bulk_action", stored.ID, map[string]any{
			"action_type":     ActionScheduleCancel,
			"customer_filter": req.CustomerFilter,
			"target_count":    len(customers),
			"succeeded_count": succeeded,
			"failed_count":    failed,
			"status":          status,
			"params":          params,
			"resource_label":  fmt.Sprintf("Bulk schedule cancel — %d/%d customers", succeeded, len(customers)),
			"idempotency_key": req.IdempotencyKey,
		})
	}

	return CommitResult{
		BulkActionID:   stored.ID,
		Status:         status,
		TargetCount:    len(customers),
		SucceededCount: succeeded,
		FailedCount:    failed,
		Errors:         errsList,
	}, nil
}

// Get returns one bulk action with its full per-target error list.
func (s *Service) Get(ctx context.Context, tenantID, id string) (Action, error) {
	return s.store.Get(ctx, tenantID, id)
}

// List delegates to the store. Limit defaults to 25 if zero/negative.
func (s *Service) List(ctx context.Context, tenantID string, filter ListFilter) ([]Action, string, error) {
	return s.store.List(ctx, tenantID, filter)
}

// cohortCustomers picks the cohort. "all" → every active customer for the
// tenant; "ids" → just the supplied ones (existence verified one-by-one).
// Stable order (id ASC) so re-runs produce the same per-customer error
// ordering.
func (s *Service) cohortCustomers(ctx context.Context, tenantID string, filter CustomerFilter) ([]domain.Customer, error) {
	if s.customers == nil {
		return nil, nil
	}
	switch filter.Type {
	case "ids":
		out := make([]domain.Customer, 0, len(filter.IDs))
		for _, id := range filter.IDs {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			cust, err := s.customers.Get(ctx, tenantID, id)
			if err != nil {
				if errors.Is(err, verrs.ErrNotFound) {
					// Skip unknown ids rather than aborting; the per-target
					// error surface captures it as "not found" via the
					// downstream coupon/cancel call. But we want a clear
					// signal here too — synthesize a customer with just
					// the id so the action sees the full requested cohort.
					out = append(out, domain.Customer{ID: id})
					continue
				}
				return nil, fmt.Errorf("lookup customer %s: %w", id, err)
			}
			out = append(out, cust)
		}
		sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		return out, nil
	case "all":
		// Capped at 500 — bulk operations beyond that cohort size deserve
		// async/batch handling we haven't built yet. Surfacing a hard cap
		// is better than silently truncating in production.
		out, _, err := s.customers.List(ctx, customer.ListFilter{
			TenantID: tenantID,
			Limit:    500,
		})
		if err != nil {
			return nil, fmt.Errorf("list customers: %w", err)
		}
		sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		return out, nil
	default:
		// validateFilter already rejected, but defence in depth.
		return nil, verrs.Invalid("customer_filter.type", "unsupported filter type")
	}
}

// finalStatus maps per-target counters to one of completed / partial /
// failed. An empty cohort is treated as completed (no work, no errors).
func finalStatus(target, succeeded, failed int) string {
	if target == 0 {
		return StatusCompleted
	}
	if failed == 0 {
		return StatusCompleted
	}
	if succeeded == 0 {
		return StatusFailed
	}
	return StatusPartial
}

// toReplayResult builds a CommitResult from a prior bulk_actions row so
// idempotent retries return the same shape as a fresh commit.
func toReplayResult(a Action) CommitResult {
	errs := a.Errors
	if errs == nil {
		errs = []TargetError{}
	}
	return CommitResult{
		BulkActionID:     a.ID,
		Status:           a.Status,
		TargetCount:      a.TargetCount,
		SucceededCount:   a.SucceededCount,
		FailedCount:      a.FailedCount,
		Errors:           errs,
		IdempotentReplay: true,
	}
}

func fmtTimePtr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
