package analytics

import (
	"log/slog"
	"net/http"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// MRRMovementPoint is one bucket (day or month) of MRR change breakdown.
type MRRMovementPoint struct {
	Date        string `json:"date"`
	New         int64  `json:"new"`
	Expansion   int64  `json:"expansion"`
	Contraction int64  `json:"contraction"`
	Churned     int64  `json:"churned"`
	Net         int64  `json:"net"`
}

// mrrMovement returns a time series of MRR movement buckets plus totals over
// the requested period.
func (h *Handler) mrrMovement(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID := auth.TenantID(ctx)
	period := parsePeriod(r.URL.Query().Get("period"))

	tx, err := h.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		slog.Error("analytics mrr-movement: begin tx", "error", err)
		respond.InternalError(w, r)
		return
	}
	defer func() { _ = tx.Rollback() }()

	dateFmt := "YYYY-MM-DD"
	if period.Trunc == "month" {
		dateFmt = "YYYY-MM"
	}

	// Pull three per-bucket aggregates (new / churned / plan-changes) then
	// merge in Go. Doing this in three focused queries is cheaper and clearer
	// than one giant UNION.
	byDate := map[string]*MRRMovementPoint{}
	touch := func(date string) *MRRMovementPoint {
		if p, ok := byDate[date]; ok {
			return p
		}
		p := &MRRMovementPoint{Date: date}
		byDate[date] = p
		return p
	}

	// New MRR per bucket
	rows, err := tx.QueryContext(ctx, `
		SELECT to_char(date_trunc($1, s.activated_at), $2) AS d,
		       COALESCE(SUM(
		           CASE WHEN p.billing_interval = 'yearly' THEN p.base_amount_cents / 12
		                ELSE p.base_amount_cents
		           END
		       ), 0)
		FROM subscriptions s
		JOIN plans p ON p.id = s.plan_id
		WHERE s.activated_at >= $3 AND s.activated_at < $4
		GROUP BY 1
	`, period.Trunc, dateFmt, period.Start, period.End)
	if err != nil {
		slog.Error("analytics mrr-movement: new query", "error", err)
		respond.InternalError(w, r)
		return
	}
	for rows.Next() {
		var d string
		var v int64
		if err := rows.Scan(&d, &v); err != nil {
			_ = rows.Close()
			slog.Error("analytics mrr-movement: new scan", "error", err)
			respond.InternalError(w, r)
			return
		}
		touch(d).New = v
	}
	_ = rows.Close()

	// Churned MRR per bucket
	rows, err = tx.QueryContext(ctx, `
		SELECT to_char(date_trunc($1, s.canceled_at), $2) AS d,
		       COALESCE(SUM(
		           CASE WHEN p.billing_interval = 'yearly' THEN p.base_amount_cents / 12
		                ELSE p.base_amount_cents
		           END
		       ), 0)
		FROM subscriptions s
		JOIN plans p ON p.id = s.plan_id
		WHERE s.canceled_at >= $3 AND s.canceled_at < $4
		GROUP BY 1
	`, period.Trunc, dateFmt, period.Start, period.End)
	if err != nil {
		slog.Error("analytics mrr-movement: churned query", "error", err)
		respond.InternalError(w, r)
		return
	}
	for rows.Next() {
		var d string
		var v int64
		if err := rows.Scan(&d, &v); err != nil {
			_ = rows.Close()
			slog.Error("analytics mrr-movement: churned scan", "error", err)
			respond.InternalError(w, r)
			return
		}
		touch(d).Churned = v
	}
	_ = rows.Close()

	// Expansion / contraction per bucket from plan changes
	rows, err = tx.QueryContext(ctx, `
		SELECT to_char(date_trunc($1, s.plan_changed_at), $2) AS d,
		       CASE WHEN pnew.billing_interval = 'yearly' THEN pnew.base_amount_cents / 12 ELSE pnew.base_amount_cents END AS new_mrr,
		       CASE WHEN pold.billing_interval = 'yearly' THEN pold.base_amount_cents / 12 ELSE pold.base_amount_cents END AS old_mrr
		FROM subscriptions s
		JOIN plans pnew ON pnew.id = s.plan_id
		JOIN plans pold ON pold.id = s.previous_plan_id
		WHERE s.plan_changed_at IS NOT NULL
		  AND s.plan_changed_at >= $3 AND s.plan_changed_at < $4
	`, period.Trunc, dateFmt, period.Start, period.End)
	if err != nil {
		slog.Error("analytics mrr-movement: plan-change query", "error", err)
		respond.InternalError(w, r)
		return
	}
	for rows.Next() {
		var d string
		var newMRR, oldMRR int64
		if err := rows.Scan(&d, &newMRR, &oldMRR); err != nil {
			_ = rows.Close()
			slog.Error("analytics mrr-movement: plan-change scan", "error", err)
			respond.InternalError(w, r)
			return
		}
		p := touch(d)
		delta := newMRR - oldMRR
		if delta > 0 {
			p.Expansion += delta
		} else {
			p.Contraction += -delta
		}
	}
	_ = rows.Close()

	// Materialize sorted, computed net, build totals.
	data := make([]MRRMovementPoint, 0, len(byDate))
	var totals MRRMovementTotals
	for _, p := range byDate {
		p.Net = p.New + p.Expansion - p.Contraction - p.Churned
		totals.New += p.New
		totals.Expansion += p.Expansion
		totals.Contraction += p.Contraction
		totals.Churned += p.Churned
		data = append(data, *p)
	}
	totals.Net = totals.New + totals.Expansion - totals.Contraction - totals.Churned
	sortByDate(data)

	_ = tx.Commit()
	respond.JSON(w, r, http.StatusOK, map[string]any{
		"period": period.Key,
		"data":   data,
		"totals": totals,
	})
}

func sortByDate(pts []MRRMovementPoint) {
	// Bucket dates are fixed-width lexicographic-sortable ("YYYY-MM-DD" /
	// "YYYY-MM"), so string comparison is correct.
	for i := 1; i < len(pts); i++ {
		for j := i; j > 0 && pts[j-1].Date > pts[j].Date; j-- {
			pts[j-1], pts[j] = pts[j], pts[j-1]
		}
	}
}
