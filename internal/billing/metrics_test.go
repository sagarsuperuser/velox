package billing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
)

type mockMetricsSubs struct {
	subs []domain.Subscription
}

func (m *mockMetricsSubs) ListActive(_ context.Context, _ string) ([]domain.Subscription, error) {
	return m.subs, nil
}

type mockMetricsInvoices struct {
	invoices []domain.Invoice
}

func (m *mockMetricsInvoices) ListRecent(_ context.Context, _ string, _ time.Time, _ int) ([]domain.Invoice, error) {
	return m.invoices, nil
}

type mockMetricsTTFI struct {
	firstFinalizedAt *time.Time
	firstErr         error
	tenantCreatedAt  time.Time
	tenantErr        error
}

func (m *mockMetricsTTFI) FirstInvoiceFinalizedAt(_ context.Context, _ string) (*time.Time, error) {
	return m.firstFinalizedAt, m.firstErr
}

func (m *mockMetricsTTFI) TenantCreatedAt(_ context.Context, _ string) (time.Time, error) {
	return m.tenantCreatedAt, m.tenantErr
}

func TestComputeDashboard(t *testing.T) {
	subs := &mockMetricsSubs{
		subs: []domain.Subscription{
			{ID: "sub_1", CustomerID: "cus_1", Status: domain.SubscriptionActive,
				Items: []domain.SubscriptionItem{{PlanID: "pln_monthly", Quantity: 1}}},
			{ID: "sub_2", CustomerID: "cus_2", Status: domain.SubscriptionActive,
				Items: []domain.SubscriptionItem{{PlanID: "pln_monthly", Quantity: 1}}},
			{ID: "sub_3", CustomerID: "cus_3", Status: domain.SubscriptionActive,
				Items: []domain.SubscriptionItem{{PlanID: "pln_yearly", Quantity: 1}}},
		},
	}

	invoices := &mockMetricsInvoices{
		invoices: []domain.Invoice{
			{ID: "inv_1", Status: domain.InvoicePaid, TotalAmountCents: 4900},
			{ID: "inv_2", Status: domain.InvoicePaid, TotalAmountCents: 4900},
			{ID: "inv_3", Status: domain.InvoiceDraft, TotalAmountCents: 19900},
			{ID: "inv_4", Status: domain.InvoiceFinalized, TotalAmountCents: 9900},
		},
	}

	plans := map[string]domain.Plan{
		"pln_monthly": {ID: "pln_monthly", BaseAmountCents: 4900, BillingInterval: domain.BillingMonthly},
		"pln_yearly":  {ID: "pln_yearly", BaseAmountCents: 59900, BillingInterval: domain.BillingYearly},
	}

	metrics := NewMetrics(subs, invoices, &mockMetricsTTFI{})
	d, err := metrics.ComputeDashboard(context.Background(), "t1", plans)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// MRR: 2 monthly × $49 + 1 yearly $599/12 = $98 + $49.91 ≈ 14791 cents
	// (4900*2 + 59900/12 = 9800 + 4991 = 14791)
	expectedMRR := int64(4900*2 + 59900/12)
	if d.MRRCents != expectedMRR {
		t.Errorf("MRR: got %d, want %d", d.MRRCents, expectedMRR)
	}

	expectedARR := expectedMRR * 12
	if d.ARRCents != expectedARR {
		t.Errorf("ARR: got %d, want %d", d.ARRCents, expectedARR)
	}

	if d.ActiveSubscriptions != 3 {
		t.Errorf("active_subs: got %d, want 3", d.ActiveSubscriptions)
	}

	if d.TotalCustomers != 3 {
		t.Errorf("customers: got %d, want 3", d.TotalCustomers)
	}

	// Revenue this month: only paid invoices = 4900 + 4900 = 9800
	if d.RevenueThisMonth != 9800 {
		t.Errorf("revenue: got %d, want 9800", d.RevenueThisMonth)
	}

	// Invoice breakdown
	if d.InvoicesByStatus["paid"] != 2 {
		t.Errorf("paid invoices: got %d, want 2", d.InvoicesByStatus["paid"])
	}
	if d.InvoicesByStatus["draft"] != 1 {
		t.Errorf("draft invoices: got %d, want 1", d.InvoicesByStatus["draft"])
	}
}

func TestComputeDashboard_Empty(t *testing.T) {
	metrics := NewMetrics(
		&mockMetricsSubs{},
		&mockMetricsInvoices{},
		&mockMetricsTTFI{},
	)

	d, err := metrics.ComputeDashboard(context.Background(), "t1", map[string]domain.Plan{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if d.MRRCents != 0 {
		t.Errorf("empty MRR: got %d, want 0", d.MRRCents)
	}
	if d.ActiveSubscriptions != 0 {
		t.Errorf("empty subs: got %d, want 0", d.ActiveSubscriptions)
	}
	if d.TimeToFirstInvoiceSeconds != nil {
		t.Errorf("empty ttfi: got %v, want nil", *d.TimeToFirstInvoiceSeconds)
	}
}

func TestComputeDashboard_TimeToFirstInvoice(t *testing.T) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	finalized := created.Add(72 * time.Hour) // 3 days

	t.Run("populated when audit log has a finalize entry", func(t *testing.T) {
		ttfi := &mockMetricsTTFI{
			firstFinalizedAt: &finalized,
			tenantCreatedAt:  created,
		}
		metrics := NewMetrics(&mockMetricsSubs{}, &mockMetricsInvoices{}, ttfi)
		d, err := metrics.ComputeDashboard(context.Background(), "t1", map[string]domain.Plan{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if d.TimeToFirstInvoiceSeconds == nil {
			t.Fatalf("TimeToFirstInvoiceSeconds: got nil, want non-nil")
		}
		want := (72 * time.Hour).Seconds()
		if *d.TimeToFirstInvoiceSeconds != want {
			t.Errorf("TimeToFirstInvoiceSeconds: got %v, want %v", *d.TimeToFirstInvoiceSeconds, want)
		}
	})

	t.Run("nil when tenant has never finalized an invoice", func(t *testing.T) {
		ttfi := &mockMetricsTTFI{
			firstFinalizedAt: nil,
			tenantCreatedAt:  created,
		}
		metrics := NewMetrics(&mockMetricsSubs{}, &mockMetricsInvoices{}, ttfi)
		d, err := metrics.ComputeDashboard(context.Background(), "t1", map[string]domain.Plan{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if d.TimeToFirstInvoiceSeconds != nil {
			t.Errorf("TimeToFirstInvoiceSeconds: got %v, want nil", *d.TimeToFirstInvoiceSeconds)
		}
	})

	t.Run("nil and dashboard still computes when audit reader fails", func(t *testing.T) {
		ttfi := &mockMetricsTTFI{
			firstErr: errors.New("boom: audit log read failed"),
		}
		subs := &mockMetricsSubs{
			subs: []domain.Subscription{
				{ID: "sub_1", CustomerID: "cus_1", Status: domain.SubscriptionActive,
					Items: []domain.SubscriptionItem{{PlanID: "pln", Quantity: 1}}},
			},
		}
		plans := map[string]domain.Plan{
			"pln": {ID: "pln", BaseAmountCents: 1000, BillingInterval: domain.BillingMonthly},
		}
		metrics := NewMetrics(subs, &mockMetricsInvoices{}, ttfi)
		d, err := metrics.ComputeDashboard(context.Background(), "t1", plans)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if d.TimeToFirstInvoiceSeconds != nil {
			t.Errorf("TimeToFirstInvoiceSeconds: got %v, want nil on reader error", *d.TimeToFirstInvoiceSeconds)
		}
		if d.MRRCents != 1000 {
			t.Errorf("MRR should still compute when ttfi fails: got %d, want 1000", d.MRRCents)
		}
		if d.ActiveSubscriptions != 1 {
			t.Errorf("ActiveSubscriptions should still compute when ttfi fails: got %d, want 1", d.ActiveSubscriptions)
		}
	})

	t.Run("nil when tenant lookup fails after audit log returned a timestamp", func(t *testing.T) {
		ttfi := &mockMetricsTTFI{
			firstFinalizedAt: &finalized,
			tenantErr:        errors.New("boom: tenant row not found"),
		}
		metrics := NewMetrics(&mockMetricsSubs{}, &mockMetricsInvoices{}, ttfi)
		d, err := metrics.ComputeDashboard(context.Background(), "t1", map[string]domain.Plan{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if d.TimeToFirstInvoiceSeconds != nil {
			t.Errorf("TimeToFirstInvoiceSeconds: got %v, want nil on tenant lookup error", *d.TimeToFirstInvoiceSeconds)
		}
	})
}
