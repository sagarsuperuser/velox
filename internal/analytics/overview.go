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

// currentMRR sums normalized-to-monthly base fees across all items of active
// subscriptions. Each item contributes plan.base × item.quantity, normalized
// from the plan's billing_interval.
func currentMRR(ctx context.Context, tx *sql.Tx) (int64, error) {
	var v int64
	err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(
			CASE WHEN p.billing_interval = 'yearly' THEN (p.base_amount_cents / 12) * si.quantity
			     ELSE p.base_amount_cents * si.quantity
			END
		), 0)
		FROM subscriptions s
		JOIN subscription_items si ON si.subscription_id = s.id
		JOIN plans p ON p.id = si.plan_id
		WHERE s.status = 'active'
	`).Scan(&v)
	return v, err
}

// mrrAtPointInTime reconstructs MRR at instant t by replaying the audit log
// backwards from the current item state. For each item that existed at t, we
// sum its MRR using the plan/quantity it held at that moment — rewound
// through any subscription_item_changes that happened after t.
//
// Items added after t are excluded. Items removed after t are re-included
// (using their state before removal). An item's (plan, quantity) at t is
// derived from the most recent 'add' / 'plan' / 'quantity' event at or
// before t; if the only change event is after t, we use the event's
// `from_*` values as the pre-change state.
func mrrAtPointInTime(ctx context.Context, tx *sql.Tx, t any) (int64, error) {
	var v int64
	err := tx.QueryRowContext(ctx, `
		WITH items_at_t AS (
			-- Items that existed at time $1: either still live today (and were
			-- added on/before t) or removed after t. For each, compute the
			-- (plan_id, quantity) as-of t by walking the change log.
			SELECT
				c.subscription_id,
				c.subscription_item_id,
				-- Latest event at or before t on this item gives its state at t.
				-- If none exists (item added after t, then a later event), the
				-- earliest post-t event's from_* describes the pre-change state.
				COALESCE(
					(SELECT c2.to_plan_id FROM subscription_item_changes c2
					   WHERE c2.subscription_item_id = c.subscription_item_id
					     AND c2.changed_at <= $1
					     AND c2.change_type IN ('add', 'plan', 'quantity')
					   ORDER BY c2.changed_at DESC LIMIT 1),
					(SELECT c3.from_plan_id FROM subscription_item_changes c3
					   WHERE c3.subscription_item_id = c.subscription_item_id
					     AND c3.changed_at > $1
					     AND c3.change_type IN ('plan', 'quantity', 'remove')
					     AND c3.from_plan_id IS NOT NULL
					   ORDER BY c3.changed_at ASC LIMIT 1)
				) AS plan_id,
				COALESCE(
					(SELECT c2.to_quantity FROM subscription_item_changes c2
					   WHERE c2.subscription_item_id = c.subscription_item_id
					     AND c2.changed_at <= $1
					     AND c2.change_type IN ('add', 'plan', 'quantity')
					   ORDER BY c2.changed_at DESC LIMIT 1),
					(SELECT c3.from_quantity FROM subscription_item_changes c3
					   WHERE c3.subscription_item_id = c.subscription_item_id
					     AND c3.changed_at > $1
					     AND c3.change_type IN ('plan', 'quantity', 'remove')
					     AND c3.from_quantity IS NOT NULL
					   ORDER BY c3.changed_at ASC LIMIT 1)
				) AS quantity
			FROM subscription_item_changes c
			GROUP BY c.subscription_id, c.subscription_item_id
		)
		SELECT COALESCE(SUM(
			CASE WHEN p.billing_interval = 'yearly' THEN (p.base_amount_cents / 12) * i.quantity
			     ELSE p.base_amount_cents * i.quantity
			END
		), 0)
		FROM items_at_t i
		JOIN subscriptions s ON s.id = i.subscription_id
		JOIN plans p ON p.id = i.plan_id
		WHERE i.plan_id IS NOT NULL
		  AND i.quantity IS NOT NULL
		  AND s.activated_at IS NOT NULL
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
// within [start, end).
//
// New / Churned use current subscription_items (items are preserved on
// cancel — only the subscription's status/canceled_at change). Expansion /
// Contraction come from the subscription_item_changes audit log, restricted
// to subscriptions that were active throughout the period so their events
// don't double-count against New / Churned.
func computeMRRMovement(ctx context.Context, tx *sql.Tx, start, end any) (MRRMovementTotals, error) {
	var m MRRMovementTotals

	// New MRR: items of subscriptions activated in [start, end).
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(
			CASE WHEN p.billing_interval = 'yearly' THEN (p.base_amount_cents / 12) * si.quantity
			     ELSE p.base_amount_cents * si.quantity
			END
		), 0)
		FROM subscriptions s
		JOIN subscription_items si ON si.subscription_id = s.id
		JOIN plans p ON p.id = si.plan_id
		WHERE s.activated_at >= $1 AND s.activated_at < $2
	`, start, end).Scan(&m.New); err != nil {
		return m, err
	}

	// Churned MRR: items of subscriptions canceled in [start, end).
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(
			CASE WHEN p.billing_interval = 'yearly' THEN (p.base_amount_cents / 12) * si.quantity
			     ELSE p.base_amount_cents * si.quantity
			END
		), 0)
		FROM subscriptions s
		JOIN subscription_items si ON si.subscription_id = s.id
		JOIN plans p ON p.id = si.plan_id
		WHERE s.canceled_at >= $1 AND s.canceled_at < $2
	`, start, end).Scan(&m.Churned); err != nil {
		return m, err
	}

	// Expansion / Contraction: plan or quantity changes on subs that were
	// active throughout the period. Subs activated or canceled inside the
	// period are excluded — their MRR impact is already in New / Churned.
	err := tx.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN delta > 0 THEN delta ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN delta < 0 THEN -delta ELSE 0 END), 0)
		FROM (
			SELECT (
				(CASE WHEN pto.billing_interval = 'yearly' THEN pto.base_amount_cents / 12
				      ELSE pto.base_amount_cents END) * c.to_quantity
			  - (CASE WHEN pfrom.billing_interval = 'yearly' THEN pfrom.base_amount_cents / 12
				      ELSE pfrom.base_amount_cents END) * c.from_quantity
			) AS delta
			FROM subscription_item_changes c
			JOIN subscriptions s ON s.id = c.subscription_id
			JOIN plans pfrom ON pfrom.id = c.from_plan_id
			JOIN plans pto ON pto.id = c.to_plan_id
			WHERE c.change_type IN ('plan', 'quantity')
			  AND c.changed_at >= $1 AND c.changed_at < $2
			  AND s.activated_at IS NOT NULL
			  AND s.activated_at < $1
			  AND (s.canceled_at IS NULL OR s.canceled_at >= $2)
		) d
	`, start, end).Scan(&m.Expansion, &m.Contraction)
	if err != nil {
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
