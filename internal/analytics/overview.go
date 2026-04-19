package analytics

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// OverviewResponse aggregates the headline billing metrics for a tenant over
// a requested period, plus matching values for the prior period so the UI can
// render period-over-period deltas without a second round-trip.
type OverviewResponse struct {
	Period string `json:"period"`

	// Money (cents)
	MRR             int64 `json:"mrr"`
	MRRPrev         int64 `json:"mrr_prev"`
	ARR             int64 `json:"arr"`
	ARRPrev         int64 `json:"arr_prev"`
	Revenue         int64 `json:"revenue"`
	RevenuePrev     int64 `json:"revenue_prev"`
	OutstandingAR   int64 `json:"outstanding_ar"`
	AvgInvoiceValue int64 `json:"avg_invoice_value"`
	CreditBalance   int64 `json:"credit_balance_total"`

	// Counts
	ActiveCustomers int   `json:"active_customers"`
	NewCustomers    int   `json:"new_customers"`
	ActiveSubs      int   `json:"active_subscriptions"`
	TrialingSubs    int   `json:"trialing_subscriptions"`
	PaidInvoices    int   `json:"paid_invoices"`
	FailedPayments  int   `json:"failed_payments"`
	OpenInvoices    int   `json:"open_invoices"`
	DunningActive   int   `json:"dunning_active"`
	UsageEvents     int64 `json:"usage_events"`

	// Rates (0..1)
	LogoChurnRate       float64 `json:"logo_churn_rate"`
	RevenueChurnRate    float64 `json:"revenue_churn_rate"`
	NRR                 float64 `json:"nrr"`
	DunningRecoveryRate float64 `json:"dunning_recovery_rate"`

	// MRR movement within the period (cents; churned/contraction are positive
	// magnitudes — the UI subtracts them visually).
	MRRMovement MRRMovementTotals `json:"mrr_movement"`
}

// MRRMovementTotals breaks MRR change into its standard SaaS components.
// Net = New + Expansion - Contraction - Churned.
type MRRMovementTotals struct {
	New         int64 `json:"new"`
	Expansion   int64 `json:"expansion"`
	Contraction int64 `json:"contraction"`
	Churned     int64 `json:"churned"`
	Net         int64 `json:"net"`
}

func (h *Handler) overview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID := auth.TenantID(ctx)
	period := parsePeriod(r.URL.Query().Get("period"))

	tx, err := h.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		slog.Error("analytics overview: begin tx", "error", err)
		respond.InternalError(w, r)
		return
	}
	defer func() { _ = tx.Rollback() }()

	resp := OverviewResponse{Period: period.Key}

	// MRR now and at the start of the period (approximated from current
	// subscription state — see mrrAtPointInTime).
	var err1, err2 error
	resp.MRR, err1 = currentMRR(ctx, tx)
	resp.MRRPrev, err2 = mrrAtPointInTime(ctx, tx, period.Start)
	if err := firstErr(err1, err2); err != nil {
		slog.Error("analytics overview: mrr", "error", err)
		respond.InternalError(w, r)
		return
	}
	resp.ARR = resp.MRR * 12
	resp.ARRPrev = resp.MRRPrev * 12

	// Revenue: paid invoices inside the period vs. the prior period.
	resp.Revenue, err1 = sumPaidRevenue(ctx, tx, period.Start, period.End)
	resp.RevenuePrev, err2 = sumPaidRevenue(ctx, tx, period.PrevStart, period.PrevEnd)
	if err := firstErr(err1, err2); err != nil {
		slog.Error("analytics overview: revenue", "error", err)
		respond.InternalError(w, r)
		return
	}

	// Snapshot counts
	queries := []struct {
		name string
		dst  any
		sql  string
		args []any
	}{
		{"active_customers", &resp.ActiveCustomers,
			`SELECT COUNT(*) FROM customers WHERE status = 'active'`, nil},
		{"new_customers", &resp.NewCustomers,
			`SELECT COUNT(*) FROM customers WHERE created_at >= $1 AND created_at < $2`,
			[]any{period.Start, period.End}},
		{"active_subs", &resp.ActiveSubs,
			`SELECT COUNT(*) FROM subscriptions WHERE status = 'active'`, nil},
		{"trialing_subs", &resp.TrialingSubs,
			`SELECT COUNT(*) FROM subscriptions WHERE status = 'active' AND trial_end_at IS NOT NULL AND trial_end_at > now()`, nil},
		{"outstanding_ar", &resp.OutstandingAR,
			`SELECT COALESCE(SUM(amount_due_cents), 0) FROM invoices WHERE status = 'finalized' AND payment_status != 'succeeded'`, nil},
		{"avg_invoice", &resp.AvgInvoiceValue,
			`SELECT COALESCE(AVG(total_amount_cents), 0)::bigint FROM invoices WHERE status = 'paid'`, nil},
		{"paid_invoices", &resp.PaidInvoices,
			`SELECT COUNT(*) FROM invoices WHERE status = 'paid' AND paid_at >= $1 AND paid_at < $2`,
			[]any{period.Start, period.End}},
		{"failed_payments", &resp.FailedPayments,
			`SELECT COUNT(*) FROM invoices WHERE payment_status = 'failed' AND created_at >= $1 AND created_at < $2`,
			[]any{period.Start, period.End}},
		{"open_invoices", &resp.OpenInvoices,
			`SELECT COUNT(*) FROM invoices WHERE status = 'finalized' AND payment_status != 'succeeded'`, nil},
		{"dunning_active", &resp.DunningActive,
			`SELECT COUNT(*) FROM invoice_dunning_runs WHERE state NOT IN ('resolved', 'exhausted')`, nil},
		{"usage_events", &resp.UsageEvents,
			`SELECT COUNT(*) FROM usage_events WHERE timestamp >= $1 AND timestamp < $2`,
			[]any{period.Start, period.End}},
	}
	for _, q := range queries {
		if err := tx.QueryRowContext(ctx, q.sql, q.args...).Scan(q.dst); err != nil {
			slog.Error("analytics overview", "query", q.name, "error", err)
			respond.InternalError(w, r)
			return
		}
	}

	// Credit balance: latest ledger row per customer, summed.
	err = tx.QueryRowContext(ctx, `
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

	// MRR movement within the period
	mvt, err := computeMRRMovement(ctx, tx, period.Start, period.End)
	if err != nil {
		slog.Error("analytics overview: mrr movement", "error", err)
		respond.InternalError(w, r)
		return
	}
	resp.MRRMovement = mvt

	// Derived rates
	resp.LogoChurnRate = churnRate(ctx, tx, period.Start, period.End, resp.MRRPrev != 0)
	if resp.MRRPrev > 0 {
		resp.RevenueChurnRate = float64(mvt.Churned) / float64(resp.MRRPrev)
		resp.NRR = float64(resp.MRRPrev+mvt.Expansion-mvt.Contraction-mvt.Churned) / float64(resp.MRRPrev)
	}
	resp.DunningRecoveryRate = dunningRecoveryRate(ctx, tx, period.Start, period.End)

	_ = tx.Commit()
	respond.JSON(w, r, http.StatusOK, resp)
}

// currentMRR sums normalized-to-monthly base fees for all active subscriptions.
func currentMRR(ctx context.Context, tx *sql.Tx) (int64, error) {
	var v int64
	err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(
			CASE WHEN p.billing_interval = 'yearly' THEN p.base_amount_cents / 12
			     ELSE p.base_amount_cents
			END
		), 0)
		FROM subscriptions s
		JOIN plans p ON p.id = s.plan_id
		WHERE s.status = 'active'
	`).Scan(&v)
	return v, err
}

// mrrAtPointInTime reconstructs MRR at instant t from the current schema. It
// accounts for (a) subscriptions already started by t and not yet canceled by
// t, and (b) plan changes — if a sub's plan changed after t, its previous_plan
// is used instead. With only one previous_plan_id column, multi-step plan
// histories earlier than the most recent change are approximated as the
// current plan.
func mrrAtPointInTime(ctx context.Context, tx *sql.Tx, t any) (int64, error) {
	var v int64
	err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(
			CASE WHEN eff.billing_interval = 'yearly' THEN eff.base_amount_cents / 12
			     ELSE eff.base_amount_cents
			END
		), 0)
		FROM subscriptions s
		JOIN LATERAL (
			SELECT p.billing_interval, p.base_amount_cents
			FROM plans p
			WHERE p.id = CASE
				WHEN s.plan_changed_at IS NOT NULL
				  AND s.plan_changed_at > $1
				  AND s.previous_plan_id IS NOT NULL
				THEN s.previous_plan_id
				ELSE s.plan_id
			END
		) eff ON true
		WHERE s.activated_at IS NOT NULL
		  AND s.activated_at <= $1
		  AND (s.canceled_at IS NULL OR s.canceled_at > $1)
	`, t).Scan(&v)
	return v, err
}

func sumPaidRevenue(ctx context.Context, tx *sql.Tx, start, end any) (int64, error) {
	var v int64
	err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(total_amount_cents), 0)
		FROM invoices
		WHERE status = 'paid' AND paid_at >= $1 AND paid_at < $2
	`, start, end).Scan(&v)
	return v, err
}

// computeMRRMovement returns gross new/expansion/contraction/churned MRR
// within [start, end). Deltas are computed from the subscription's current
// plan_id / previous_plan_id — a simplification that holds when a subscription
// changes plans at most once in the period.
func computeMRRMovement(ctx context.Context, tx *sql.Tx, start, end any) (MRRMovementTotals, error) {
	var m MRRMovementTotals

	// New MRR: subs first activated inside the period, at their current plan.
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(
			CASE WHEN p.billing_interval = 'yearly' THEN p.base_amount_cents / 12
			     ELSE p.base_amount_cents
			END
		), 0)
		FROM subscriptions s
		JOIN plans p ON p.id = s.plan_id
		WHERE s.activated_at >= $1 AND s.activated_at < $2
	`, start, end).Scan(&m.New); err != nil {
		return m, err
	}

	// Churned MRR: subs canceled inside the period (use plan_id at cancel time).
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(
			CASE WHEN p.billing_interval = 'yearly' THEN p.base_amount_cents / 12
			     ELSE p.base_amount_cents
			END
		), 0)
		FROM subscriptions s
		JOIN plans p ON p.id = s.plan_id
		WHERE s.canceled_at >= $1 AND s.canceled_at < $2
	`, start, end).Scan(&m.Churned); err != nil {
		return m, err
	}

	// Expansion / contraction: plan changes inside the period.
	rows, err := tx.QueryContext(ctx, `
		SELECT
			CASE WHEN pnew.billing_interval = 'yearly' THEN pnew.base_amount_cents / 12 ELSE pnew.base_amount_cents END AS new_mrr,
			CASE WHEN pold.billing_interval = 'yearly' THEN pold.base_amount_cents / 12 ELSE pold.base_amount_cents END AS old_mrr
		FROM subscriptions s
		JOIN plans pnew ON pnew.id = s.plan_id
		JOIN plans pold ON pold.id = s.previous_plan_id
		WHERE s.plan_changed_at IS NOT NULL
		  AND s.plan_changed_at >= $1 AND s.plan_changed_at < $2
	`, start, end)
	if err != nil {
		return m, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var newMRR, oldMRR int64
		if err := rows.Scan(&newMRR, &oldMRR); err != nil {
			return m, err
		}
		delta := newMRR - oldMRR
		if delta > 0 {
			m.Expansion += delta
		} else {
			m.Contraction += -delta
		}
	}
	if err := rows.Err(); err != nil {
		return m, err
	}

	m.Net = m.New + m.Expansion - m.Contraction - m.Churned
	return m, nil
}

// churnRate = canceled-in-period / active-at-period-start.
// Silent-fails to 0 on query errors to avoid nuking the whole overview for a
// derived metric — the caller has already reported success on core fields.
func churnRate(ctx context.Context, tx *sql.Tx, start, end any, _ bool) float64 {
	var canceled, activeAtStart int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM subscriptions
		WHERE canceled_at >= $1 AND canceled_at < $2
	`, start, end).Scan(&canceled); err != nil {
		return 0
	}
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM subscriptions
		WHERE activated_at IS NOT NULL
		  AND activated_at <= $1
		  AND (canceled_at IS NULL OR canceled_at > $1)
	`, start).Scan(&activeAtStart); err != nil {
		return 0
	}
	if activeAtStart == 0 {
		return 0
	}
	return float64(canceled) / float64(activeAtStart)
}

// dunningRecoveryRate = resolved-in-period / opened-in-period. Only counts
// resolutions that landed on 'resolved' (successful recovery), ignoring
// 'exhausted' / 'written_off' outcomes.
func dunningRecoveryRate(ctx context.Context, tx *sql.Tx, start, end any) float64 {
	var opened, recovered int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM invoice_dunning_runs
		WHERE created_at >= $1 AND created_at < $2
	`, start, end).Scan(&opened); err != nil {
		return 0
	}
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM invoice_dunning_runs
		WHERE created_at >= $1 AND created_at < $2 AND state = 'resolved'
	`, start, end).Scan(&recovered); err != nil {
		return 0
	}
	if opened == 0 {
		return 0
	}
	return float64(recovered) / float64(opened)
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}
