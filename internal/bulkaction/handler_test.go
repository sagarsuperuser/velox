package bulkaction

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/customer"
	"github.com/sagarsuperuser/velox/internal/domain"
)

// TestHandler_ApplyCoupon_HTTPLevel exercises the full POST handler path
// through chi: JSON in, snake_case JSON out, status code on validation
// errors. Stubs the service deps with the same fakes as the service-level
// tests so we don't need a Postgres roundtrip.
func TestHandler_ApplyCoupon_HTTPLevel(t *testing.T) {
	store := newFakeStore()
	custs := &fakeCustomers{rows: map[string][]domain.Customer{
		"tenant_a": {{ID: "vlx_cus_1"}},
	}}
	assigner := &fakeAssigner{}
	svc := NewService(store, custs, nil, nil, assigner, nil)
	h := NewHandler(svc)

	body := `{
		"idempotency_key": "key-http",
		"customer_filter": {"type": "all"},
		"coupon_code": "WELCOME"
	}`
	req := httptest.NewRequest(http.MethodPost, "/apply_coupon", bytes.NewReader([]byte(body)))
	req = req.WithContext(context.WithValue(req.Context(), auth.TestTenantIDKey(), "tenant_a"))
	rr := httptest.NewRecorder()

	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, rr.Body.String())
	}
	for _, k := range []string{
		"bulk_action_id", "status", "target_count", "succeeded_count",
		"failed_count", "errors",
	} {
		if _, ok := resp[k]; !ok {
			t.Errorf("response missing %q (keys=%v)", k, mapKeys(resp))
		}
	}
	for _, k := range []string{
		"BulkActionID", "Status", "TargetCount", "SucceededCount", "FailedCount", "Errors",
	} {
		if _, ok := resp[k]; ok {
			t.Errorf("response leaked PascalCase key %q", k)
		}
	}
	if resp["status"] != "completed" {
		t.Errorf("expected status=completed, got %v", resp["status"])
	}
	if resp["succeeded_count"].(float64) != 1 {
		t.Errorf("expected succeeded_count=1, got %v", resp["succeeded_count"])
	}
}

func TestHandler_ApplyCoupon_ValidationError(t *testing.T) {
	svc := NewService(newFakeStore(), &fakeCustomers{}, nil, nil, &fakeAssigner{}, nil)
	h := NewHandler(svc)

	// Missing idempotency_key — service rejects with errs.Required.
	body := `{
		"customer_filter": {"type": "all"},
		"coupon_code": "X"
	}`
	req := httptest.NewRequest(http.MethodPost, "/apply_coupon", bytes.NewReader([]byte(body)))
	req = req.WithContext(context.WithValue(req.Context(), auth.TestTenantIDKey(), "tenant_a"))
	rr := httptest.NewRecorder()

	h.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for missing idempotency_key, got %d: %s", rr.Code, rr.Body.String())
	}
	// Stripe-style envelope must reference the offending field.
	if !strings.Contains(rr.Body.String(), "idempotency_key") {
		t.Errorf("expected error body to mention idempotency_key, got %s", rr.Body.String())
	}
}

func TestHandler_ScheduleCancel_HTTPLevel(t *testing.T) {
	store := newFakeStore()
	custs := &fakeCustomers{rows: map[string][]domain.Customer{
		"tenant_a": {{ID: "vlx_cus_1"}},
	}}
	subs := &fakeSubscriptions{rows: map[string][]domain.Subscription{
		"tenant_a:vlx_cus_1": {{ID: "vlx_sub_1"}},
	}}
	canceller := &fakeCanceller{}
	svc := NewService(store, custs, subs, canceller, nil, nil)
	h := NewHandler(svc)

	body := `{
		"idempotency_key": "key-cancel-http",
		"customer_filter": {"type": "ids", "ids": ["vlx_cus_1"]},
		"at_period_end": true
	}`
	req := httptest.NewRequest(http.MethodPost, "/schedule_cancel", bytes.NewReader([]byte(body)))
	req = req.WithContext(context.WithValue(req.Context(), auth.TestTenantIDKey(), "tenant_a"))
	rr := httptest.NewRecorder()

	h.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["status"] != "completed" {
		t.Errorf("expected status=completed, got %v (body=%s)", resp["status"], rr.Body.String())
	}
	if len(canceller.calls) != 1 || canceller.calls[0] != "vlx_sub_1" {
		t.Errorf("expected single ScheduleCancel call on vlx_sub_1, got %v", canceller.calls)
	}
}

func TestHandler_List_HTTPLevel(t *testing.T) {
	store := newFakeStore()
	store.Insert(context.Background(), "tenant_a", Action{
		IdempotencyKey: "k1",
		ActionType:     ActionApplyCoupon,
		Status:         StatusCompleted,
		Params:         map[string]any{"coupon_code": "X"},
	})
	store.Insert(context.Background(), "tenant_a", Action{
		IdempotencyKey: "k2",
		ActionType:     ActionScheduleCancel,
		Status:         StatusFailed,
	})
	svc := NewService(store, &fakeCustomers{}, nil, nil, &fakeAssigner{}, nil)
	h := NewHandler(svc)

	req := httptest.NewRequest(http.MethodGet, "/?status=completed", nil)
	req = req.WithContext(context.WithValue(req.Context(), auth.TestTenantIDKey(), "tenant_a"))
	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	rows, ok := resp["bulk_actions"].([]any)
	if !ok {
		t.Fatalf("bulk_actions must be JSON array, got %T", resp["bulk_actions"])
	}
	// Filter by status=completed → should return only k1.
	if len(rows) != 1 {
		t.Errorf("expected 1 row after status filter, got %d", len(rows))
	}
}

// asserts the package compiles against the production CustomerLister
// signature — ensures customer.ListFilter wiring stays correct as the
// upstream filter shape evolves.
var _ CustomerLister = (*fakeCustomers)(nil)
var _ = customer.ListFilter{}
