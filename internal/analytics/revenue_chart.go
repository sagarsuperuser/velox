package analytics

import (
	"log/slog"
	"net/http"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// RevenueDataPoint is one bucket of paid-invoice revenue.
type RevenueDataPoint struct {
	Date         string `json:"date"`
	RevenueCents int64  `json:"revenue_cents"`
	InvoiceCount int    `json:"invoice_count"`
}

func (h *Handler) revenueChart(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID := auth.TenantID(ctx)
	period := parsePeriod(r.URL.Query().Get("period"))

	tx, err := h.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		slog.Error("analytics revenue-chart: begin tx", "error", err)
		respond.InternalError(w, r)
		return
	}
	defer func() { _ = tx.Rollback() }()

	dateFmt := "YYYY-MM-DD"
	if period.Trunc == "month" {
		dateFmt = "YYYY-MM"
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT to_char(date_trunc($1, paid_at), $2) AS date,
		       COALESCE(SUM(total_amount_cents), 0) AS revenue_cents,
		       COUNT(*) AS invoice_count
		FROM invoices
		WHERE status = 'paid' AND paid_at >= $3 AND paid_at < $4
		GROUP BY 1
		ORDER BY 1
	`, period.Trunc, dateFmt, period.Start, period.End)
	if err != nil {
		slog.Error("analytics revenue-chart: query", "error", err)
		respond.InternalError(w, r)
		return
	}
	defer func() { _ = rows.Close() }()

	data := make([]RevenueDataPoint, 0)
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

	_ = tx.Commit()
	respond.JSON(w, r, http.StatusOK, map[string]any{"period": period.Key, "data": data})
}
