// Provider cost rates + per-customer margin (ADR-079). COST — what the
// OPERATOR pays their LLM providers — is a separate ledger from PRICE (what
// the customer is billed); nothing here touches invoices or rating. The
// per-event COGS stamp itself lives in store.Ingest (single funnel); this
// file is the operator surface: rate CRUD + the margin report.
package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// --- store ---

// ListProviderCostRates returns the tenant's current rates (mode-scoped via
// RLS), ordered for the dashboard table.
func (s *PostgresStore) ListProviderCostRates(ctx context.Context, tenantID string) ([]domain.ProviderCostRate, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	rows, err := tx.QueryContext(ctx, `
		SELECT id, tenant_id, provider, model, token_type, cost_per_token, currency, created_at, updated_at
		FROM provider_cost_rates
		WHERE tenant_id = $1
		ORDER BY provider, model, token_type
	`, tenantID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []domain.ProviderCostRate
	for rows.Next() {
		var r domain.ProviderCostRate
		if err := rows.Scan(&r.ID, &r.TenantID, &r.Provider, &r.Model, &r.TokenType,
			&r.CostPerToken, &r.Currency, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpsertProviderCostRate creates or edits-in-place the rate for a key
// (current-rate semantics, ADR-079 D1: the per-event stamp is the history;
// editing a rate only affects FUTURE events).
func (s *PostgresStore) UpsertProviderCostRate(ctx context.Context, tenantID string, r domain.ProviderCostRate) (domain.ProviderCostRate, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.ProviderCostRate{}, err
	}
	defer postgres.Rollback(tx)

	err = tx.QueryRowContext(ctx, `
		INSERT INTO provider_cost_rates (tenant_id, provider, model, token_type, cost_per_token, currency)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (tenant_id, livemode, provider, model, token_type)
		DO UPDATE SET cost_per_token = EXCLUDED.cost_per_token,
		              currency = EXCLUDED.currency,
		              updated_at = now()
		RETURNING id, tenant_id, provider, model, token_type, cost_per_token, currency, created_at, updated_at
	`, tenantID, r.Provider, r.Model, r.TokenType, r.CostPerToken, r.Currency,
	).Scan(&r.ID, &r.TenantID, &r.Provider, &r.Model, &r.TokenType,
		&r.CostPerToken, &r.Currency, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return domain.ProviderCostRate{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ProviderCostRate{}, err
	}
	return r, nil
}

// DeleteProviderCostRate removes a rate row. Stamped events keep their
// snapshot (documented — deletion never rewrites history).
func (s *PostgresStore) DeleteProviderCostRate(ctx context.Context, tenantID, id string) error {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return err
	}
	defer postgres.Rollback(tx)

	res, err := tx.ExecContext(ctx, `DELETE FROM provider_cost_rates WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errs.ErrNotFound
	}
	return tx.Commit()
}

// CustomerCostByModel aggregates the stamped COGS for one customer's window:
// per-model cost plus the honesty counters (ADR-079 D7 — 'unresolved' =
// token events that carried costable dims but matched no rate: the
// actionable signal; 'not_applicable' events are excluded so they can't
// drown it).
type CustomerCostByModel struct {
	Model      string
	CostMicros int64
	Events     int64
}

func (s *PostgresStore) CustomerProviderCost(ctx context.Context, tenantID, customerID string, from, to time.Time) (byModel []CustomerCostByModel, unresolvedEvents int64, err error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return nil, 0, err
	}
	defer postgres.Rollback(tx)

	rows, err := tx.QueryContext(ctx, `
		SELECT COALESCE(properties->>'model', properties->>'model_raw', '(unknown)') AS model,
			COALESCE(SUM(provider_cost_micros), 0)::BIGINT,
			COUNT(*)
		FROM usage_events
		WHERE tenant_id = $1 AND customer_id = $2
		  AND timestamp >= $3 AND timestamp < $4
		  AND provider_cost_micros IS NOT NULL
		GROUP BY 1
		ORDER BY 2 DESC
	`, tenantID, customerID, from, to)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var m CustomerCostByModel
		if err := rows.Scan(&m.Model, &m.CostMicros, &m.Events); err != nil {
			return nil, 0, err
		}
		byModel = append(byModel, m)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	// Unresolved = costable-but-unmatched: source is NULL AND no stamp.
	// ('not_applicable' rows are excluded by the source IS NULL predicate.)
	err = tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM usage_events
		WHERE tenant_id = $1 AND customer_id = $2
		  AND timestamp >= $3 AND timestamp < $4
		  AND provider_cost_micros IS NULL AND provider_cost_source IS NULL
	`, tenantID, customerID, from, to).Scan(&unresolvedEvents)
	if err != nil {
		return nil, 0, err
	}
	return byModel, unresolvedEvents, nil
}

// --- margin assembly ---

// MarginModelRow is one per-model line of the margin report. RevenueCents
// and MarginBps are set ONLY when the model's revenue is honestly
// attributable (a pricing rule pins `model` in dimension_match); otherwise
// Attributed=false and only cost renders — never a heuristic allocation
// (ADR-079 D6).
type MarginModelRow struct {
	Model        string `json:"model"`
	CostMicros   int64  `json:"cost_micros"`
	RevenueCents int64  `json:"revenue_cents,omitempty"`
	Attributed   bool   `json:"attributed"`
}

type MarginReport struct {
	CustomerID string    `json:"customer_id"`
	From       time.Time `json:"from"`
	To         time.Time `json:"to"`
	// Headline (always correct): total rated usage revenue vs total
	// stamped provider cost for the window. Rated USAGE revenue — base
	// fees, credits, and taxes are not in this number (it is a usage
	// unit-economics view, not GAAP margin; the UI copy says so).
	RevenueCents int64 `json:"revenue_cents"`
	CostMicros   int64 `json:"cost_micros"`
	// MarginBps = (revenue − cost) / revenue in basis points; omitted
	// when revenue is 0.
	MarginBps              *int64           `json:"margin_bps,omitempty"`
	ByModel                []MarginModelRow `json:"by_model"`
	UnattributedRevenueCents int64          `json:"unattributed_revenue_cents"`
	UnresolvedEvents       int64            `json:"unresolved_events"`
	CacheWriteExcluded     bool             `json:"cache_write_excluded"`
}

// MarginAssembler joins stamped COGS with rated usage revenue.
type MarginAssembler struct {
	store    *PostgresStore
	usageSvc *CustomerUsageService
}

func NewMarginAssembler(store *PostgresStore, usageSvc *CustomerUsageService) *MarginAssembler {
	return &MarginAssembler{store: store, usageSvc: usageSvc}
}

func (a *MarginAssembler) Get(ctx context.Context, tenantID, customerID string, from, to time.Time) (MarginReport, error) {
	rep := MarginReport{CustomerID: customerID, From: from, To: to, CacheWriteExcluded: true, ByModel: []MarginModelRow{}}

	byModel, unresolved, err := a.store.CustomerProviderCost(ctx, tenantID, customerID, from, to)
	if err != nil {
		return MarginReport{}, fmt.Errorf("aggregate provider cost: %w", err)
	}
	rep.UnresolvedEvents = unresolved

	// Revenue: rated usage for the same window, attributed per model ONLY
	// where the rule's dimension_match pins model.
	usage, err := a.usageSvc.Get(ctx, tenantID, customerID, CustomerUsagePeriod{From: from, To: to})
	if err != nil {
		return MarginReport{}, fmt.Errorf("rate usage window: %w", err)
	}
	revenueByModel := map[string]int64{}
	for _, m := range usage.Meters {
		for _, r := range m.Rules {
			model, ok := r.DimensionMatch["model"].(string)
			if ok && model != "" {
				revenueByModel[model] += r.AmountCents
			} else {
				rep.UnattributedRevenueCents += r.AmountCents
			}
			rep.RevenueCents += r.AmountCents
		}
	}

	seen := map[string]bool{}
	for _, c := range byModel {
		rep.CostMicros += c.CostMicros
		row := MarginModelRow{Model: c.Model, CostMicros: c.CostMicros}
		if rev, ok := revenueByModel[c.Model]; ok {
			row.RevenueCents, row.Attributed = rev, true
		}
		rep.ByModel = append(rep.ByModel, row)
		seen[c.Model] = true
	}
	// Models with attributed revenue but no stamped cost still render.
	for model, rev := range revenueByModel {
		if !seen[model] {
			rep.ByModel = append(rep.ByModel, MarginModelRow{Model: model, RevenueCents: rev, Attributed: true})
		}
	}

	if rep.RevenueCents > 0 {
		costCents := decimal.NewFromInt(rep.CostMicros).Div(decimal.NewFromInt(10000)) // micros → cents (1e4)
		revCents := decimal.NewFromInt(rep.RevenueCents)
		bps := revCents.Sub(costCents).Div(revCents).Mul(decimal.NewFromInt(10000)).Round(0).IntPart()
		rep.MarginBps = &bps
	}
	return rep, nil
}

// --- handler ---

// ProviderCostHandler is the operator surface: rate CRUD + margin report.
// Operator-auth only — COGS never renders on customer-facing pages.
type ProviderCostHandler struct {
	store    *PostgresStore
	margin   *MarginAssembler
}

func NewProviderCostHandler(store *PostgresStore, margin *MarginAssembler) *ProviderCostHandler {
	return &ProviderCostHandler{store: store, margin: margin}
}

func (h *ProviderCostHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", h.list)
	r.Put("/", h.upsert)
	r.Delete("/{id}", h.delete)
	return r
}

func (h *ProviderCostHandler) list(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	rates, err := h.store.ListProviderCostRates(r.Context(), tenantID)
	if err != nil {
		respond.FromError(w, r, err, "provider_cost_rate")
		return
	}
	if rates == nil {
		rates = []domain.ProviderCostRate{}
	}
	respond.JSON(w, r, http.StatusOK, map[string]any{"data": rates})
}

func (h *ProviderCostHandler) upsert(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	var in domain.ProviderCostRate
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		respond.BadRequest(w, r, "invalid JSON")
		return
	}
	in.Provider = strings.TrimSpace(strings.ToLower(in.Provider))
	in.Model = strings.TrimSpace(in.Model)
	in.TokenType = strings.TrimSpace(strings.ToLower(in.TokenType))
	if in.Provider == "" || in.Model == "" || in.TokenType == "" {
		respond.FromError(w, r, errs.Required("provider, model, token_type"), "provider_cost_rate")
		return
	}
	if in.CostPerToken.IsNegative() {
		respond.FromError(w, r, errs.Invalid("cost_per_token", "must be zero or positive"), "provider_cost_rate")
		return
	}
	if in.Currency == "" {
		in.Currency = "USD"
	}
	in.Currency = strings.ToUpper(in.Currency)
	out, err := h.store.UpsertProviderCostRate(r.Context(), tenantID, in)
	if err != nil {
		respond.FromError(w, r, err, "provider_cost_rate")
		return
	}
	respond.JSON(w, r, http.StatusOK, out)
}

func (h *ProviderCostHandler) delete(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	if err := h.store.DeleteProviderCostRate(r.Context(), tenantID, chi.URLParam(r, "id")); err != nil {
		respond.FromError(w, r, err, "provider_cost_rate")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Margin serves GET /v1/customers/{id}/margin?from&to (operator auth;
// mounted from the router next to the other customer subresources).
func (h *ProviderCostHandler) Margin(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	customerID := chi.URLParam(r, "id")
	from, to, err := parseWindow(r)
	if err != nil {
		respond.BadRequest(w, r, err.Error())
		return
	}
	rep, err := h.margin.Get(r.Context(), tenantID, customerID, from, to)
	if err != nil {
		respond.FromError(w, r, err, "margin")
		return
	}
	respond.JSON(w, r, http.StatusOK, rep)
}

// parseWindow reads from/to (RFC3339); defaults to the last 30 days.
func parseWindow(r *http.Request) (time.Time, time.Time, error) {
	now := time.Now().UTC()
	from, to := now.AddDate(0, 0, -30), now
	if v := r.URL.Query().Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("from: must be RFC3339")
		}
		from = t
	}
	if v := r.URL.Query().Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("to: must be RFC3339")
		}
		to = t
	}
	if !from.Before(to) {
		return time.Time{}, time.Time{}, fmt.Errorf("from must be before to")
	}
	return from, to, nil
}
