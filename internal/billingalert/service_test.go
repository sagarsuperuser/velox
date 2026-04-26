package billingalert

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

// TestService_Create_Validation table-drives every input check the
// service performs before persistence. Each row is a minimal request +
// the expected validation field — all rows MUST surface a DomainError
// tagged with the right field so the dashboard can route the message.
func TestService_Create_Validation(t *testing.T) {
	amount := int64(10000)
	zeroAmount := int64(0)
	negAmount := int64(-1)
	qty := decimal.RequireFromString("100")
	zeroQty := decimal.Zero

	cases := []struct {
		name      string
		req       CreateRequest
		wantField string
	}{
		{
			name:      "title_required",
			req:       CreateRequest{Title: "  ", CustomerID: "vlx_cus", Recurrence: domain.BillingAlertRecurrenceOneTime, AmountCentsGTE: &amount},
			wantField: "title",
		},
		{
			name:      "title_too_long",
			req:       CreateRequest{Title: strings.Repeat("x", MaxTitleLen+1), CustomerID: "vlx_cus", Recurrence: domain.BillingAlertRecurrenceOneTime, AmountCentsGTE: &amount},
			wantField: "title",
		},
		{
			name:      "customer_required",
			req:       CreateRequest{Title: "ok", CustomerID: "  ", Recurrence: domain.BillingAlertRecurrenceOneTime, AmountCentsGTE: &amount},
			wantField: "customer_id",
		},
		{
			name:      "recurrence_invalid",
			req:       CreateRequest{Title: "ok", CustomerID: "vlx_cus", Recurrence: "weekly", AmountCentsGTE: &amount},
			wantField: "recurrence",
		},
		{
			name:      "threshold_required",
			req:       CreateRequest{Title: "ok", CustomerID: "vlx_cus", Recurrence: domain.BillingAlertRecurrenceOneTime},
			wantField: "threshold",
		},
		{
			name: "threshold_both_set",
			req: CreateRequest{
				Title: "ok", CustomerID: "vlx_cus", Recurrence: domain.BillingAlertRecurrenceOneTime,
				AmountCentsGTE: &amount, QuantityGTE: &qty, MeterID: "vlx_mtr",
			},
			wantField: "threshold",
		},
		{
			name:      "amount_zero",
			req:       CreateRequest{Title: "ok", CustomerID: "vlx_cus", Recurrence: domain.BillingAlertRecurrenceOneTime, AmountCentsGTE: &zeroAmount},
			wantField: "threshold.amount_gte",
		},
		{
			name:      "amount_negative",
			req:       CreateRequest{Title: "ok", CustomerID: "vlx_cus", Recurrence: domain.BillingAlertRecurrenceOneTime, AmountCentsGTE: &negAmount},
			wantField: "threshold.amount_gte",
		},
		{
			name: "quantity_zero",
			req: CreateRequest{
				Title: "ok", CustomerID: "vlx_cus", Recurrence: domain.BillingAlertRecurrenceOneTime,
				QuantityGTE: &zeroQty, MeterID: "vlx_mtr",
			},
			wantField: "threshold.usage_gte",
		},
		{
			name: "usage_without_meter",
			req: CreateRequest{
				Title: "ok", CustomerID: "vlx_cus", Recurrence: domain.BillingAlertRecurrenceOneTime,
				QuantityGTE: &qty, // no MeterID
			},
			wantField: "threshold.usage_gte",
		},
		{
			name: "dimensions_without_meter",
			req: CreateRequest{
				Title: "ok", CustomerID: "vlx_cus", Recurrence: domain.BillingAlertRecurrenceOneTime,
				AmountCentsGTE: &amount,
				Dimensions:     map[string]any{"model": "gpt-4"},
			},
			wantField: "filter.dimensions",
		},
		{
			name: "dimensions_too_many_keys",
			req: CreateRequest{
				Title: "ok", CustomerID: "vlx_cus", Recurrence: domain.BillingAlertRecurrenceOneTime,
				AmountCentsGTE: &amount, MeterID: "vlx_mtr",
				Dimensions: makeDimensionMap(MaxDimensionKeys + 1),
			},
			wantField: "filter.dimensions",
		},
		{
			name: "dimensions_non_scalar_value",
			req: CreateRequest{
				Title: "ok", CustomerID: "vlx_cus", Recurrence: domain.BillingAlertRecurrenceOneTime,
				AmountCentsGTE: &amount, MeterID: "vlx_mtr",
				Dimensions: map[string]any{"nested": map[string]any{"x": 1}},
			},
			wantField: "filter.dimensions",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewService(&fakeStore{}, &fakeCustomers{ok: true}, &fakeMeters{ok: true})
			_, err := svc.Create(context.Background(), "vlx_tenant", tc.req)
			if err == nil {
				t.Fatalf("expected validation error, got nil")
			}
			if got := errs.Field(err); got != tc.wantField {
				t.Errorf("field = %q, want %q (err=%v)", got, tc.wantField, err)
			}
			if !errors.Is(err, errs.ErrValidation) {
				t.Errorf("error must be a validation kind, got %v", err)
			}
		})
	}
}

// TestService_Create_HappyPath ensures the simplest valid request reaches
// the store.Create call cleanly with the trimmed, normalized inputs.
func TestService_Create_HappyPath(t *testing.T) {
	amount := int64(50000)
	store := &fakeStore{}
	svc := NewService(store, &fakeCustomers{ok: true}, &fakeMeters{ok: true})

	req := CreateRequest{
		Title:          "  Acme spend > $500  ",
		CustomerID:     "  vlx_cus_abc  ",
		Recurrence:     domain.BillingAlertRecurrencePerPeriod,
		AmountCentsGTE: &amount,
	}
	_, err := svc.Create(context.Background(), "vlx_tenant", req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !store.created {
		t.Fatal("expected store.Create to be called")
	}
	if store.lastAlert.Title != "Acme spend > $500" {
		t.Errorf("title not trimmed: %q", store.lastAlert.Title)
	}
	if store.lastAlert.CustomerID != "vlx_cus_abc" {
		t.Errorf("customer_id not trimmed: %q", store.lastAlert.CustomerID)
	}
	// Always-object idiom: nil dimensions become {}.
	if store.lastAlert.Filter.Dimensions == nil {
		t.Error("filter.dimensions should be {} not nil")
	}
}

// TestService_Create_CustomerNotFound checks the cross-tenant 404 path:
// when the customer lookup returns ErrNotFound (RLS hides cross-tenant
// rows), Create propagates the same sentinel so the handler can map to 404.
func TestService_Create_CustomerNotFound(t *testing.T) {
	amount := int64(10000)
	svc := NewService(&fakeStore{}, &fakeCustomers{notFound: true}, &fakeMeters{ok: true})
	_, err := svc.Create(context.Background(), "vlx_tenant", CreateRequest{
		Title:          "ok",
		CustomerID:     "vlx_cus_other_tenant",
		Recurrence:     domain.BillingAlertRecurrenceOneTime,
		AmountCentsGTE: &amount,
	})
	if !errors.Is(err, errs.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// TestService_Create_MeterNotFound checks the same 404 propagation for
// the meter lookup when filter.meter_id is set.
func TestService_Create_MeterNotFound(t *testing.T) {
	qty := decimal.RequireFromString("100")
	svc := NewService(&fakeStore{}, &fakeCustomers{ok: true}, &fakeMeters{notFound: true})
	_, err := svc.Create(context.Background(), "vlx_tenant", CreateRequest{
		Title:       "ok",
		CustomerID:  "vlx_cus",
		Recurrence:  domain.BillingAlertRecurrenceOneTime,
		QuantityGTE: &qty,
		MeterID:     "vlx_mtr_other_tenant",
	})
	if !errors.Is(err, errs.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// TestService_List_StatusValidation drops unknown filter values rather
// than silently serving them — frontend bugs must surface as 422.
func TestService_List_StatusValidation(t *testing.T) {
	svc := NewService(&fakeStore{}, &fakeCustomers{ok: true}, &fakeMeters{ok: true})

	_, _, err := svc.List(context.Background(), ListFilter{
		TenantID: "vlx_tenant",
		Status:   "weird_status",
	})
	if err == nil {
		t.Fatal("expected validation error for unknown status")
	}
	if errs.Field(err) != "status" {
		t.Errorf("expected field=status, got %q", errs.Field(err))
	}

	// Empty status is fine — no filter.
	_, _, err = svc.List(context.Background(), ListFilter{TenantID: "vlx_tenant"})
	if err != nil {
		t.Errorf("empty status should be OK, got %v", err)
	}

	// Each known status passes.
	for _, s := range []domain.BillingAlertStatus{
		domain.BillingAlertStatusActive,
		domain.BillingAlertStatusTriggered,
		domain.BillingAlertStatusTriggeredForPeriod,
		domain.BillingAlertStatusArchived,
	} {
		_, _, err := svc.List(context.Background(), ListFilter{TenantID: "vlx_tenant", Status: s})
		if err != nil {
			t.Errorf("status %q should pass, got %v", s, err)
		}
	}
}

// TestService_Get_RequiresID — defence in depth: the handler already
// pulls id from the URL pattern, but the service still validates so a
// direct call from a future internal caller can't bypass.
func TestService_Get_RequiresID(t *testing.T) {
	svc := NewService(&fakeStore{}, &fakeCustomers{ok: true}, &fakeMeters{ok: true})
	_, err := svc.Get(context.Background(), "vlx_tenant", "  ")
	if errs.Field(err) != "id" {
		t.Errorf("expected field=id, got %q (err=%v)", errs.Field(err), err)
	}
}

// TestService_Archive_Idempotent checks the documented contract: archive
// returns the alert (status=archived) on each call without erroring on
// already-archived rows. The store fake's Archive implementation mirrors
// that behaviour (RETURNING the post-update row works the same on
// repeat).
func TestService_Archive_Idempotent(t *testing.T) {
	svc := NewService(&fakeStore{
		archived: domain.BillingAlert{
			ID:     "vlx_alrt_1",
			Status: domain.BillingAlertStatusArchived,
		},
	}, &fakeCustomers{ok: true}, &fakeMeters{ok: true})

	first, err := svc.Archive(context.Background(), "vlx_tenant", "vlx_alrt_1")
	if err != nil {
		t.Fatalf("first archive: %v", err)
	}
	if first.Status != domain.BillingAlertStatusArchived {
		t.Errorf("expected status=archived, got %q", first.Status)
	}

	second, err := svc.Archive(context.Background(), "vlx_tenant", "vlx_alrt_1")
	if err != nil {
		t.Errorf("second archive errored: %v", err)
	}
	if second.Status != domain.BillingAlertStatusArchived {
		t.Errorf("second archive status not stable: %q", second.Status)
	}
}

// makeDimensionMap returns a fresh map with `n` simple string entries.
// Used to construct over-cap dimension fixtures without copy/paste.
func makeDimensionMap(n int) map[string]any {
	out := make(map[string]any, n)
	for i := 0; i < n; i++ {
		out["k"+itoa(i)] = "v"
	}
	return out
}

// fakeStore is the in-memory Store the service tests drive. We only
// implement enough surface to satisfy the Service paths exercised here;
// integration tests exercise the real PostgresStore.
type fakeStore struct {
	created   bool
	lastAlert domain.BillingAlert
	archived  domain.BillingAlert
}

func (f *fakeStore) Create(ctx context.Context, tenantID string, alert domain.BillingAlert) (domain.BillingAlert, error) {
	f.created = true
	f.lastAlert = alert
	return alert, nil
}
func (f *fakeStore) Get(ctx context.Context, tenantID, id string) (domain.BillingAlert, error) {
	return f.lastAlert, nil
}
func (f *fakeStore) List(ctx context.Context, filter ListFilter) ([]domain.BillingAlert, int, error) {
	return nil, 0, nil
}
func (f *fakeStore) Archive(ctx context.Context, tenantID, id string) (domain.BillingAlert, error) {
	if f.archived.ID == "" {
		f.archived = domain.BillingAlert{ID: id, Status: domain.BillingAlertStatusArchived}
	}
	return f.archived, nil
}
func (f *fakeStore) ListCandidates(ctx context.Context, limit int) ([]domain.BillingAlert, error) {
	return nil, nil
}
func (f *fakeStore) FireInTx(ctx context.Context, tx *sql.Tx, alert domain.BillingAlert, trigger domain.BillingAlertTrigger, newStatus domain.BillingAlertStatus) (domain.BillingAlertTrigger, error) {
	return trigger, nil
}
func (f *fakeStore) Rearm(ctx context.Context, tenantID, alertID string) error { return nil }
func (f *fakeStore) BeginTenantTx(ctx context.Context, tenantID string) (*sql.Tx, error) {
	return nil, nil
}

type fakeCustomers struct {
	ok       bool
	notFound bool
}

func (f *fakeCustomers) Get(ctx context.Context, tenantID, id string) (domain.Customer, error) {
	if f.notFound {
		return domain.Customer{}, errs.ErrNotFound
	}
	return domain.Customer{ID: id, TenantID: tenantID}, nil
}

type fakeMeters struct {
	ok       bool
	notFound bool
}

func (f *fakeMeters) GetMeter(ctx context.Context, tenantID, id string) (domain.Meter, error) {
	if f.notFound {
		return domain.Meter{}, errs.ErrNotFound
	}
	return domain.Meter{ID: id, TenantID: tenantID}, nil
}
