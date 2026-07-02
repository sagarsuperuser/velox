package subscription

import (
	"context"
	"errors"
	"testing"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

type fakeCustomerReader struct{ cust domain.Customer }

func (f *fakeCustomerReader) Get(_ context.Context, _, _ string) (domain.Customer, error) {
	return f.cust, nil
}

// TestCreate_RejectsArchivedCustomer locks ADR-067: subscription.Create for
// an archived customer is a 409 — archive means "no new business", and
// without this a direct API call starts billing a retired customer.
// Mutation seam: drop the status check in Create's customer fetch.
func TestCreate_RejectsArchivedCustomer(t *testing.T) {
	svc := NewService(newMemStore(), nil)
	svc.SetCustomerReader(&fakeCustomerReader{cust: domain.Customer{
		ID: "cus_1", Status: domain.CustomerStatusArchived,
	}})
	_, err := svc.Create(context.Background(), "t1", CreateInput{
		Code: "sub-arch", DisplayName: "S", CustomerID: "cus_1",
		Items: []CreateItemInput{{PlanID: "pln_1", Quantity: 1}},
	})
	if !errors.Is(err, errs.ErrInvalidState) {
		t.Fatalf("create for archived customer: err = %v, want InvalidState (409)", err)
	}
}

// TestCreate_RejectsArchivedPlan: an archived plan accepts no new
// subscriptions (it previously remained fully subscribable via the API).
func TestCreate_RejectsArchivedPlan(t *testing.T) {
	svc := NewService(newMemStore(), nil)
	svc.SetCustomerReader(&fakeCustomerReader{cust: domain.Customer{ID: "cus_1", Status: domain.CustomerStatusActive}})
	svc.SetPlanReader(&fakePlanReader{plans: map[string]domain.Plan{
		"pln_arch": {ID: "pln_arch", Currency: "USD", Status: domain.PlanArchived},
	}})
	_, err := svc.Create(context.Background(), "t1", CreateInput{
		Code: "sub-planarch", DisplayName: "S", CustomerID: "cus_1",
		Items: []CreateItemInput{{PlanID: "pln_arch", Quantity: 1}},
	})
	if !errors.Is(err, errs.ErrInvalidState) {
		t.Fatalf("create on archived plan: err = %v, want InvalidState (409)", err)
	}

	// Control: active plan passes the guard (may fail later for other
	// reasons, but NOT with the archived-plan 409).
	svc2 := NewService(newMemStore(), nil)
	svc2.SetCustomerReader(&fakeCustomerReader{cust: domain.Customer{ID: "cus_1", Status: domain.CustomerStatusActive}})
	svc2.SetPlanReader(&fakePlanReader{plans: map[string]domain.Plan{
		"pln_ok": {ID: "pln_ok", Currency: "USD", Status: domain.PlanActive, BillingInterval: domain.BillingMonthly},
	}})
	if _, err := svc2.Create(context.Background(), "t1", CreateInput{
		Code: "sub-ok", DisplayName: "S", CustomerID: "cus_1",
		Items: []CreateItemInput{{PlanID: "pln_ok", Quantity: 1}},
	}); errors.Is(err, errs.ErrInvalidState) {
		t.Fatalf("active plan tripped the archived guard: %v", err)
	}
}
