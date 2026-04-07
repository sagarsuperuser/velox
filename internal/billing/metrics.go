package billing

import (
	"context"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// Metrics computes billing KPIs from subscription and invoice data.
type Metrics struct {
	subs     MetricsSubReader
	invoices MetricsInvoiceReader
}

type MetricsSubReader interface {
	ListActive(ctx context.Context, tenantID string) ([]domain.Subscription, error)
}

type MetricsInvoiceReader interface {
	ListRecent(ctx context.Context, tenantID string, since time.Time, limit int) ([]domain.Invoice, error)
}

type Dashboard struct {
	MRRCents           int64            `json:"mrr_cents"`
	ARRCents           int64            `json:"arr_cents"`
	ActiveSubscriptions int             `json:"active_subscriptions"`
	TotalCustomers     int              `json:"total_customers"`
	RevenueThisMonth   int64            `json:"revenue_this_month_cents"`
	InvoicesByStatus   map[string]int   `json:"invoices_by_status"`
	GeneratedAt        time.Time        `json:"generated_at"`
}

func NewMetrics(subs MetricsSubReader, invoices MetricsInvoiceReader) *Metrics {
	return &Metrics{subs: subs, invoices: invoices}
}

// ComputeDashboard calculates MRR, ARR, and billing KPIs for a tenant.
func (m *Metrics) ComputeDashboard(ctx context.Context, tenantID string, plans map[string]domain.Plan) (Dashboard, error) {
	d := Dashboard{
		InvoicesByStatus: make(map[string]int),
		GeneratedAt:      time.Now().UTC(),
	}

	// Active subscriptions → MRR
	activeSubs, err := m.subs.ListActive(ctx, tenantID)
	if err != nil {
		return d, err
	}
	d.ActiveSubscriptions = len(activeSubs)

	customerSet := make(map[string]bool)
	for _, sub := range activeSubs {
		customerSet[sub.CustomerID] = true

		plan, ok := plans[sub.PlanID]
		if !ok {
			continue
		}

		switch plan.BillingInterval {
		case domain.BillingMonthly:
			d.MRRCents += plan.BaseAmountCents
		case domain.BillingYearly:
			d.MRRCents += plan.BaseAmountCents / 12
		}
	}
	d.TotalCustomers = len(customerSet)
	d.ARRCents = d.MRRCents * 12

	// Revenue this month
	monthStart := time.Date(time.Now().Year(), time.Now().Month(), 1, 0, 0, 0, 0, time.UTC)
	recentInvoices, err := m.invoices.ListRecent(ctx, tenantID, monthStart, 1000)
	if err != nil {
		return d, err
	}

	for _, inv := range recentInvoices {
		d.InvoicesByStatus[string(inv.Status)]++
		if inv.Status == domain.InvoicePaid {
			d.RevenueThisMonth += inv.TotalAmountCents
		}
	}

	return d, nil
}
