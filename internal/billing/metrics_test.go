package billing

import (
	"context"
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

	metrics := NewMetrics(subs, invoices)
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
}
