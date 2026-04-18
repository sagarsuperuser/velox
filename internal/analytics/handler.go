package analytics

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// OverviewResponse contains aggregated billing metrics for a tenant.
type OverviewResponse struct {
	MRR               int64 `json:"mrr"`
	ActiveCustomers   int   `json:"active_customers"`
	ActiveSubs        int   `json:"active_subscriptions"`
	TotalRevenue      int64 `json:"total_revenue"`
	OutstandingAR     int64 `json:"outstanding_ar"`
	AvgInvoiceValue   int64 `json:"avg_invoice_value"`
	PaidInvoices30d   int   `json:"paid_invoices_30d"`
	FailedPayments30d int   `json:"failed_payments_30d"`
	OpenInvoices      int   `json:"open_invoices"`
	DunningActive     int   `json:"dunning_active"`
	CreditBalance     int64 `json:"credit_balance_total"`
}

// RevenueDataPoint represents a single data point in the revenue chart.
type RevenueDataPoint struct {
	Date         string `json:"date"`
	RevenueCents int64  `json:"revenue_cents"`
	InvoiceCount int    `json:"invoice_count"`
}

// Handler serves analytics API endpoints.
type Handler struct {
	db *postgres.DB
}

// NewHandler creates a new analytics handler.
func NewHandler(db *postgres.DB) *Handler {
	return &Handler{db: db}
}

// Routes returns a chi router for analytics endpoints.
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/overview", h.overview)
	r.Get("/revenue-chart", h.revenueChart)
	return r
}

func (h *Handler) overview(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	tx, err := h.db.BeginTx(r.Context(), postgres.TxTenant, tenantID)
	if err != nil {
		slog.Error("analytics overview: begin tx", "error", err)
		respond.InternalError(w, r)
		return
	}
	defer func() { _ = tx.Rollback() }()

	var resp OverviewResponse

	// MRR: sum of active subscription plan amounts, normalized to monthly
	err = tx.QueryRowContext(r.Context(), `
		SELECT COALESCE(SUM(
			CASE WHEN p.billing_interval = 'yearly' THEN p.base_amount_cents / 12
			     ELSE p.base_amount_cents
			END
		), 0)
		FROM subscriptions s
		JOIN plans p ON p.id = s.plan_id
		WHERE s.status = 'active'
	`).Scan(&resp.MRR)
	if err != nil {
		slog.Error("analytics overview: mrr", "error", err)
		respond.InternalError(w, r)
		return
	}

	// Active customers
	err = tx.QueryRowContext(r.Context(), `
		SELECT COUNT(*) FROM customers WHERE status = 'active'
	`).Scan(&resp.ActiveCustomers)
	if err != nil {
		slog.Error("analytics overview: active customers", "error", err)
		respond.InternalError(w, r)
		return
	}

	// Active subscriptions
	err = tx.QueryRowContext(r.Context(), `
		SELECT COUNT(*) FROM subscriptions WHERE status = 'active'
	`).Scan(&resp.ActiveSubs)
	if err != nil {
		slog.Error("analytics overview: active subs", "error", err)
		respond.InternalError(w, r)
		return
	}

	// Total revenue (all-time paid invoices)
	err = tx.QueryRowContext(r.Context(), `
		SELECT COALESCE(SUM(total_amount_cents), 0) FROM invoices WHERE status = 'paid'
	`).Scan(&resp.TotalRevenue)
	if err != nil {
		slog.Error("analytics overview: total revenue", "error", err)
		respond.InternalError(w, r)
		return
	}

	// Outstanding AR (unpaid finalized invoices)
	err = tx.QueryRowContext(r.Context(), `
		SELECT COALESCE(SUM(amount_due_cents), 0) FROM invoices
		WHERE status = 'finalized' AND payment_status != 'succeeded'
	`).Scan(&resp.OutstandingAR)
	if err != nil {
		slog.Error("analytics overview: outstanding ar", "error", err)
		respond.InternalError(w, r)
		return
	}

	// Average invoice value (paid invoices)
	err = tx.QueryRowContext(r.Context(), `
		SELECT COALESCE(AVG(total_amount_cents), 0)::bigint FROM invoices WHERE status = 'paid'
	`).Scan(&resp.AvgInvoiceValue)
	if err != nil {
		slog.Error("analytics overview: avg invoice", "error", err)
		respond.InternalError(w, r)
		return
	}

	// Paid invoices in last 30 days
	thirtyDaysAgo := time.Now().AddDate(0, 0, -30)
	err = tx.QueryRowContext(r.Context(), `
		SELECT COUNT(*) FROM invoices WHERE status = 'paid' AND paid_at >= $1
	`, thirtyDaysAgo).Scan(&resp.PaidInvoices30d)
	if err != nil {
		slog.Error("analytics overview: paid 30d", "error", err)
		respond.InternalError(w, r)
		return
	}

	// Failed payments in last 30 days
	err = tx.QueryRowContext(r.Context(), `
		SELECT COUNT(*) FROM invoices WHERE payment_status = 'failed' AND created_at >= $1
	`, thirtyDaysAgo).Scan(&resp.FailedPayments30d)
	if err != nil {
		slog.Error("analytics overview: failed 30d", "error", err)
		respond.InternalError(w, r)
		return
	}

	// Open invoices (finalized, not paid)
	err = tx.QueryRowContext(r.Context(), `
		SELECT COUNT(*) FROM invoices WHERE status = 'finalized' AND payment_status != 'succeeded'
	`).Scan(&resp.OpenInvoices)
	if err != nil {
		slog.Error("analytics overview: open invoices", "error", err)
		respond.InternalError(w, r)
		return
	}

	// Active dunning runs
	err = tx.QueryRowContext(r.Context(), `
		SELECT COUNT(*) FROM invoice_dunning_runs WHERE state NOT IN ('resolved', 'exhausted')
	`).Scan(&resp.DunningActive)
	if err != nil {
		slog.Error("analytics overview: dunning active", "error", err)
		respond.InternalError(w, r)
		return
	}

	// Total outstanding credit balance
	err = tx.QueryRowContext(r.Context(), `
		SELECT COALESCE(SUM(balance_after), 0)
		FROM (
			SELECT DISTINCT ON (customer_id) balance_after
			FROM customer_credit_ledger
			ORDER BY customer_id, created_at DESC
		) latest
	`).Scan(&resp.CreditBalance)
	if err != nil {
		slog.Error("analytics overview: credit balance", "error", err)
		respond.InternalError(w, r)
		return
	}

	_ = tx.Commit()
	respond.JSON(w, r, http.StatusOK, resp)
}

func (h *Handler) revenueChart(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	period := r.URL.Query().Get("period")
	if period == "" {
		period = "30d"
	}

	var trunc string
	var since time.Time
	now := time.Now()

	switch period {
	case "90d":
		trunc = "day"
		since = now.AddDate(0, 0, -90)
	case "12m":
		trunc = "month"
		since = now.AddDate(-1, 0, 0)
	default: // "30d"
		trunc = "day"
		since = now.AddDate(0, 0, -30)
	}

	tx, err := h.db.BeginTx(r.Context(), postgres.TxTenant, tenantID)
	if err != nil {
		slog.Error("analytics revenue-chart: begin tx", "error", err)
		respond.InternalError(w, r)
		return
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(r.Context(), `
		SELECT to_char(date_trunc($1, paid_at), CASE WHEN $1 = 'month' THEN 'YYYY-MM' ELSE 'YYYY-MM-DD' END) AS date,
		       COALESCE(SUM(total_amount_cents), 0) AS revenue_cents,
		       COUNT(*) AS invoice_count
		FROM invoices
		WHERE status = 'paid' AND paid_at >= $2
		GROUP BY 1
		ORDER BY 1
	`, trunc, since)
	if err != nil {
		slog.Error("analytics revenue-chart: query", "error", err)
		respond.InternalError(w, r)
		return
	}
	defer func() { _ = rows.Close() }()

	var data []RevenueDataPoint
	for rows.Next() {
		var dp RevenueDataPoint
		if err := rows.Scan(&dp.Date, &dp.RevenueCents, &dp.InvoiceCount); err != nil {
			slog.Error("analytics revenue-chart: scan", "error", err)
			respond.InternalError(w, r)
			return
		}
		data = append(data, dp)
	}
	if err := rows.Err(); err != nil {
		slog.Error("analytics revenue-chart: rows err", "error", err)
		respond.InternalError(w, r)
		return
	}

	if data == nil {
		data = []RevenueDataPoint{}
	}

	_ = tx.Commit()
	respond.JSON(w, r, http.StatusOK, map[string]any{"data": data})
}
