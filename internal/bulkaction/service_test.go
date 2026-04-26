package bulkaction

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/customer"
	"github.com/sagarsuperuser/velox/internal/domain"
	verrs "github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/subscription"
)

// fakeStore is an in-memory Store used by the service-layer unit tests
// to avoid pulling in a Postgres dep. It models UNIQUE (tenant_id,
// idempotency_key) so the idempotency-key replay path is exercised
// faithfully without hitting the database.
type fakeStore struct {
	rows map[string]Action // keyed by id
}

func newFakeStore() *fakeStore {
	return &fakeStore{rows: map[string]Action{}}
}

func (f *fakeStore) Insert(ctx context.Context, tenantID string, row Action) (Action, error) {
	for _, existing := range f.rows {
		if existing.TenantID == tenantID && existing.IdempotencyKey == row.IdempotencyKey {
			return Action{}, verrs.ErrAlreadyExists
		}
	}
	if row.ID == "" {
		row.ID = "vlx_bact_" + tenantID + "_" + row.IdempotencyKey
	}
	row.TenantID = tenantID
	row.CreatedAt = time.Now().UTC()
	if row.Errors == nil {
		row.Errors = []TargetError{}
	}
	if row.Params == nil {
		row.Params = map[string]any{}
	}
	f.rows[row.ID] = row
	return row, nil
}

func (f *fakeStore) GetByIdempotencyKey(ctx context.Context, tenantID, key string) (Action, error) {
	for _, row := range f.rows {
		if row.TenantID == tenantID && row.IdempotencyKey == key {
			return row, nil
		}
	}
	return Action{}, verrs.ErrNotFound
}

func (f *fakeStore) Get(ctx context.Context, tenantID, id string) (Action, error) {
	row, ok := f.rows[id]
	if !ok || row.TenantID != tenantID {
		return Action{}, verrs.ErrNotFound
	}
	return row, nil
}

func (f *fakeStore) UpdateProgress(ctx context.Context, tenantID, id string, status string, target, succeeded, failed int, errs []TargetError, completedAt *time.Time) error {
	row, ok := f.rows[id]
	if !ok || row.TenantID != tenantID {
		return verrs.ErrNotFound
	}
	row.Status = status
	row.TargetCount = target
	row.SucceededCount = succeeded
	row.FailedCount = failed
	row.Errors = errs
	if row.Errors == nil {
		row.Errors = []TargetError{}
	}
	row.CompletedAt = completedAt
	f.rows[id] = row
	return nil
}

func (f *fakeStore) List(ctx context.Context, tenantID string, filter ListFilter) ([]Action, string, error) {
	out := make([]Action, 0)
	for _, row := range f.rows {
		if row.TenantID != tenantID {
			continue
		}
		if filter.Status != "" && row.Status != filter.Status {
			continue
		}
		if filter.ActionType != "" && row.ActionType != filter.ActionType {
			continue
		}
		out = append(out, row)
	}
	return out, "", nil
}

// fakeCustomers implements CustomerLister with an in-memory map keyed by
// (tenantID, customerID). Failing lookups surface verrs.ErrNotFound.
type fakeCustomers struct {
	rows map[string][]domain.Customer
}

func (f *fakeCustomers) List(ctx context.Context, filter customer.ListFilter) ([]domain.Customer, int, error) {
	out := f.rows[filter.TenantID]
	return out, len(out), nil
}

func (f *fakeCustomers) Get(ctx context.Context, tenantID, id string) (domain.Customer, error) {
	for _, c := range f.rows[tenantID] {
		if c.ID == id {
			return c, nil
		}
	}
	return domain.Customer{}, verrs.ErrNotFound
}

// fakeSubscriptions returns a pre-loaded set of subscriptions per
// (tenantID, customerID) pair.
type fakeSubscriptions struct {
	rows map[string][]domain.Subscription // key: tenantID + ":" + customerID
}

func (f *fakeSubscriptions) key(tenantID, customerID string) string {
	return tenantID + ":" + customerID
}

func (f *fakeSubscriptions) List(ctx context.Context, filter subscription.ListFilter) ([]domain.Subscription, int, error) {
	out := f.rows[f.key(filter.TenantID, filter.CustomerID)]
	return out, len(out), nil
}

// fakeCanceller records the per-sub cancel calls so tests assert which
// subscriptions were touched.
type fakeCanceller struct {
	calls []string
	fail  map[string]error
}

func (f *fakeCanceller) ScheduleCancel(ctx context.Context, tenantID, id string, _ subscription.ScheduleCancelInput) (domain.Subscription, error) {
	f.calls = append(f.calls, id)
	if err, ok := f.fail[id]; ok {
		return domain.Subscription{}, err
	}
	return domain.Subscription{ID: id}, nil
}

// fakeAssigner records each per-customer coupon attach.
type fakeAssigner struct {
	calls []string // customer ids
	fail  map[string]error
}

func (f *fakeAssigner) AssignToCustomer(ctx context.Context, tenantID string, input CouponAssignInput) error {
	f.calls = append(f.calls, input.CustomerID)
	if err, ok := f.fail[input.CustomerID]; ok {
		return err
	}
	return nil
}

func TestService_ApplyCoupon_HappyPath(t *testing.T) {
	store := newFakeStore()
	custs := &fakeCustomers{rows: map[string][]domain.Customer{
		"tenant_a": {{ID: "vlx_cus_1"}, {ID: "vlx_cus_2"}},
	}}
	assigner := &fakeAssigner{}
	svc := NewService(store, custs, nil, nil, assigner, nil)

	res, err := svc.ApplyCoupon(context.Background(), "tenant_a", ApplyCouponRequest{
		IdempotencyKey: "key-1",
		CustomerFilter: CustomerFilter{Type: "all"},
		CouponCode:     "SUMMER20",
	})
	if err != nil {
		t.Fatalf("ApplyCoupon: %v", err)
	}
	if res.Status != StatusCompleted {
		t.Errorf("expected status %q, got %q", StatusCompleted, res.Status)
	}
	if res.SucceededCount != 2 || res.FailedCount != 0 || res.TargetCount != 2 {
		t.Errorf("unexpected counts: target=%d succeeded=%d failed=%d", res.TargetCount, res.SucceededCount, res.FailedCount)
	}
	if len(assigner.calls) != 2 {
		t.Errorf("expected 2 assigner calls, got %d", len(assigner.calls))
	}
}

func TestService_ApplyCoupon_PartialFailure(t *testing.T) {
	store := newFakeStore()
	custs := &fakeCustomers{rows: map[string][]domain.Customer{
		"tenant_a": {{ID: "vlx_cus_1"}, {ID: "vlx_cus_2"}, {ID: "vlx_cus_3"}},
	}}
	assigner := &fakeAssigner{fail: map[string]error{
		"vlx_cus_2": errors.New("coupon expired"),
	}}
	svc := NewService(store, custs, nil, nil, assigner, nil)

	res, err := svc.ApplyCoupon(context.Background(), "tenant_a", ApplyCouponRequest{
		IdempotencyKey: "key-2",
		CustomerFilter: CustomerFilter{Type: "all"},
		CouponCode:     "SUMMER20",
	})
	if err != nil {
		t.Fatalf("ApplyCoupon: %v", err)
	}
	if res.Status != StatusPartial {
		t.Errorf("expected status %q, got %q", StatusPartial, res.Status)
	}
	if res.SucceededCount != 2 || res.FailedCount != 1 {
		t.Errorf("expected 2 ok, 1 fail; got %d / %d", res.SucceededCount, res.FailedCount)
	}
	if len(res.Errors) != 1 || res.Errors[0].CustomerID != "vlx_cus_2" {
		t.Errorf("expected per-target error for vlx_cus_2, got %+v", res.Errors)
	}
	if !strings.Contains(res.Errors[0].Error, "coupon expired") {
		t.Errorf("expected error to contain 'coupon expired', got %q", res.Errors[0].Error)
	}
}

func TestService_ApplyCoupon_IdempotentReplay(t *testing.T) {
	store := newFakeStore()
	custs := &fakeCustomers{rows: map[string][]domain.Customer{
		"tenant_a": {{ID: "vlx_cus_1"}},
	}}
	assigner := &fakeAssigner{}
	svc := NewService(store, custs, nil, nil, assigner, nil)

	first, err := svc.ApplyCoupon(context.Background(), "tenant_a", ApplyCouponRequest{
		IdempotencyKey: "key-replay",
		CustomerFilter: CustomerFilter{Type: "all"},
		CouponCode:     "SUMMER20",
	})
	if err != nil {
		t.Fatalf("first ApplyCoupon: %v", err)
	}
	second, err := svc.ApplyCoupon(context.Background(), "tenant_a", ApplyCouponRequest{
		IdempotencyKey: "key-replay",
		CustomerFilter: CustomerFilter{Type: "all"},
		CouponCode:     "SUMMER20",
	})
	if err != nil {
		t.Fatalf("second ApplyCoupon: %v", err)
	}
	if !second.IdempotentReplay {
		t.Error("expected IdempotentReplay=true on second commit")
	}
	if second.BulkActionID != first.BulkActionID {
		t.Errorf("replay returned different id: %q vs %q", second.BulkActionID, first.BulkActionID)
	}
	if len(assigner.calls) != 1 {
		t.Errorf("expected only 1 assigner call across both commits, got %d", len(assigner.calls))
	}
}

func TestService_ApplyCoupon_RejectsTagFilter(t *testing.T) {
	svc := NewService(newFakeStore(), nil, nil, nil, nil, nil)
	_, err := svc.ApplyCoupon(context.Background(), "tenant_a", ApplyCouponRequest{
		IdempotencyKey: "key",
		CustomerFilter: CustomerFilter{Type: "tag", Value: "enterprise"},
		CouponCode:     "X",
	})
	if err == nil {
		t.Fatal("expected error for tag filter")
	}
	if !strings.Contains(err.Error(), "tag filters are not yet supported") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestService_ApplyCoupon_RequiresIdempotencyKey(t *testing.T) {
	svc := NewService(newFakeStore(), nil, nil, nil, nil, nil)
	_, err := svc.ApplyCoupon(context.Background(), "tenant_a", ApplyCouponRequest{
		CustomerFilter: CustomerFilter{Type: "all"},
		CouponCode:     "X",
	})
	if err == nil || !strings.Contains(err.Error(), "idempotency_key") {
		t.Errorf("expected required idempotency_key error, got %v", err)
	}
}

func TestService_ScheduleCancel_HappyPath(t *testing.T) {
	store := newFakeStore()
	custs := &fakeCustomers{rows: map[string][]domain.Customer{
		"tenant_a": {{ID: "vlx_cus_1"}, {ID: "vlx_cus_2"}},
	}}
	subs := &fakeSubscriptions{rows: map[string][]domain.Subscription{
		"tenant_a:vlx_cus_1": {{ID: "vlx_sub_1"}},
		"tenant_a:vlx_cus_2": {{ID: "vlx_sub_2"}, {ID: "vlx_sub_3"}},
	}}
	canceller := &fakeCanceller{}
	svc := NewService(store, custs, subs, canceller, nil, nil)

	res, err := svc.ScheduleCancel(context.Background(), "tenant_a", ScheduleCancelRequest{
		IdempotencyKey: "key-cancel",
		CustomerFilter: CustomerFilter{Type: "all"},
		AtPeriodEnd:    true,
	})
	if err != nil {
		t.Fatalf("ScheduleCancel: %v", err)
	}
	if res.Status != StatusCompleted {
		t.Errorf("expected status %q, got %q", StatusCompleted, res.Status)
	}
	if res.SucceededCount != 2 {
		t.Errorf("expected 2 succeeded, got %d", res.SucceededCount)
	}
	if len(canceller.calls) != 3 {
		t.Errorf("expected 3 cancel calls (1 + 2 subs), got %d", len(canceller.calls))
	}
}

func TestService_ScheduleCancel_NoActiveSub(t *testing.T) {
	store := newFakeStore()
	custs := &fakeCustomers{rows: map[string][]domain.Customer{
		"tenant_a": {{ID: "vlx_cus_1"}},
	}}
	// Deliberately empty subscriptions for cus_1.
	subs := &fakeSubscriptions{rows: map[string][]domain.Subscription{}}
	canceller := &fakeCanceller{}
	svc := NewService(store, custs, subs, canceller, nil, nil)

	res, err := svc.ScheduleCancel(context.Background(), "tenant_a", ScheduleCancelRequest{
		IdempotencyKey: "key-no-sub",
		CustomerFilter: CustomerFilter{Type: "all"},
		AtPeriodEnd:    true,
	})
	if err != nil {
		t.Fatalf("ScheduleCancel: %v", err)
	}
	if res.Status != StatusFailed {
		t.Errorf("expected status %q, got %q", StatusFailed, res.Status)
	}
	if res.FailedCount != 1 {
		t.Errorf("expected 1 failed, got %d", res.FailedCount)
	}
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Error, "no active subscriptions") {
		t.Errorf("expected 'no active subscriptions' error, got %+v", res.Errors)
	}
}

func TestService_ScheduleCancel_RejectsBothModesUnset(t *testing.T) {
	svc := NewService(newFakeStore(), nil, nil, nil, nil, nil)
	_, err := svc.ScheduleCancel(context.Background(), "tenant_a", ScheduleCancelRequest{
		IdempotencyKey: "key",
		CustomerFilter: CustomerFilter{Type: "all"},
	})
	if err == nil || !strings.Contains(err.Error(), "at_period_end or cancel_at") {
		t.Errorf("expected at_period_end-or-cancel_at error, got %v", err)
	}
}

func TestService_ScheduleCancel_RejectsBothModesSet(t *testing.T) {
	svc := NewService(newFakeStore(), nil, nil, nil, nil, nil)
	when := time.Now().Add(48 * time.Hour)
	_, err := svc.ScheduleCancel(context.Background(), "tenant_a", ScheduleCancelRequest{
		IdempotencyKey: "key",
		CustomerFilter: CustomerFilter{Type: "all"},
		AtPeriodEnd:    true,
		CancelAt:       &when,
	})
	if err == nil || !strings.Contains(err.Error(), "cannot be set together") {
		t.Errorf("expected mutual-exclusion error, got %v", err)
	}
}

func TestService_finalStatus(t *testing.T) {
	cases := []struct {
		target, ok, fail int
		want             string
	}{
		{0, 0, 0, StatusCompleted},
		{5, 5, 0, StatusCompleted},
		{5, 4, 1, StatusPartial},
		{5, 0, 5, StatusFailed},
	}
	for _, c := range cases {
		got := finalStatus(c.target, c.ok, c.fail)
		if got != c.want {
			t.Errorf("finalStatus(%d,%d,%d) = %q; want %q", c.target, c.ok, c.fail, got, c.want)
		}
	}
}
