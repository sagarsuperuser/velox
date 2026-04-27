// Package costdashboard serves the public /v1/public/cost-dashboard/* surface
// — an embeddable customer-facing usage view, similar in spirit to Stripe's
// hosted_invoice_url but bound to a customer rather than to one invoice.
// The token in the URL is the sole credential: no API key, no session
// cookie. That matches the industry standard (Stripe, Lago, Orb, Paddle)
// so operators can drop the iframe into their own product surface
// without forcing their customer to authenticate twice.
//
// Token resolution runs cross-tenant under TxBypass because the handler
// cannot set a tenant context before it knows which tenant the token
// belongs to. Cross-tenant probing isn't feasible: the token carries 256
// bits of entropy and the underlying column is UNIQUE indexed (see
// migration 0064). Once the customer is resolved, every subsequent read
// uses that customer's tenant — consistent with how hostedinvoice scopes
// its /v1/public/invoices/* surface.
//
// Industry-standard semantics:
//   - Persistent URL: view remains accessible until the operator rotates
//     the token. Rotation invalidates the previous URL atomically.
//   - Sanitised projection of customer + usage state. NO email, NO
//     billing-profile, NO metadata, NO internal status fields. The
//     embed iframe is rendered inside the operator's product, which
//     already shows everything the operator wants to show; the public
//     surface stays minimal so it can never leak operator state.
//   - Per-meter breakdown + per-currency totals so a customer can see
//     "what am I spending across my plan(s) this cycle". When the
//     customer has no active subscription, the response degrades to an
//     empty-state with empty arrays rather than a 500.
//
// Dependencies are declared as narrow interfaces so the handler can be
// tested with in-memory fakes and coupling flows one way: costdashboard
// consumes customer + usage, never the reverse.
package costdashboard

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/usage"
)

// CustomerResolver resolves a token to its owning customer with no
// tenant context — the public iframe handler hits this BEFORE it knows
// which tenant to scope to.
type CustomerResolver interface {
	GetByCostDashboardToken(ctx context.Context, token string) (domain.Customer, error)
}

// UsageGetter is the customer-usage read surface. Same shape the
// authenticated /v1/customers/{id}/usage endpoint uses, so dashboard
// math == invoice math.
type UsageGetter interface {
	Get(ctx context.Context, tenantID, customerID string, period usage.CustomerUsagePeriod) (usage.CustomerUsageResult, error)
}

// Handler serves the public cost-dashboard view.
type Handler struct {
	customers CustomerResolver
	usage     UsageGetter
}

// New wires a Handler with the two collaborators it needs.
func New(customers CustomerResolver, usageSvc UsageGetter) *Handler {
	return &Handler{customers: customers, usage: usageSvc}
}

// Routes returns the chi sub-router. Mount under
// /v1/public/cost-dashboard inside the rate-limited public bucket.
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/{token}", h.view)
	return r
}

// noSubscriptionCode is the WithCode value usage.resolvePeriod attaches
// when a customer has no active subscription with a billing cycle. We
// match on it to render an empty-state response rather than a 4xx —
// the customer's iframe should still show "no plan yet" cleanly.
const noSubscriptionCode = "customer_has_no_subscription"

func (h *Handler) view(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	if token == "" {
		respond.NotFound(w, r, "cost_dashboard")
		return
	}

	cust, err := h.customers.GetByCostDashboardToken(r.Context(), token)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "cost_dashboard")
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "costdashboard: token resolve", "error", err)
		respond.InternalError(w, r)
		return
	}

	// Default to the customer's current billing cycle. Explicit windows
	// are out of scope for the iframe — the embed shows "this cycle"
	// only, matching how Stripe's customer-portal usage section works.
	result, err := h.usage.Get(r.Context(), cust.TenantID, cust.ID, usage.CustomerUsagePeriod{})
	if errs.Code(err) == noSubscriptionCode {
		// Customer has no plan yet. Don't fail — render an empty-state
		// payload so the iframe shows "no usage to show" cleanly.
		respond.JSON(w, r, http.StatusOK, emptyResponse(cust))
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "costdashboard: usage get",
			"customer_id", cust.ID, "tenant_id", cust.TenantID, "error", err)
		respond.InternalError(w, r)
		return
	}

	respond.JSON(w, r, http.StatusOK, projectResponse(cust, result))
}

// publicCostDashboardResponse is the safe, presentation-shaped projection
// the iframe consumes. Deliberately excludes email, billing_profile,
// metadata, display_name, external_id, and any field that could leak
// operator-level identity. Customers see only what they need to
// understand their own usage and spend.
type publicCostDashboardResponse struct {
	CustomerID    string               `json:"customer_id"`
	TenantID      string               `json:"tenant_id"`
	BillingPeriod publicBillingPeriod  `json:"billing_period"`
	Subscriptions []publicSubscription `json:"subscriptions"`
	Usage         []publicUsageMeter   `json:"usage"`
	Totals        []publicTotal        `json:"totals"`
	// Thresholds is reserved for the future billing-alerts integration;
	// always emitted as [] so the SPA can iterate without null guards.
	Thresholds []publicThreshold `json:"thresholds"`
	// Warnings surfaces non-fatal pricing/config issues from the rating
	// path (e.g. mismatched-currency rules). The iframe can render them
	// as a banner; an empty slice means "no warnings".
	Warnings []string `json:"warnings"`
}

type publicBillingPeriod struct {
	From   time.Time `json:"from"`
	To     time.Time `json:"to"`
	Source string    `json:"source"`
}

type publicSubscription struct {
	ID                 string    `json:"id"`
	PlanID             string    `json:"plan_id"`
	PlanName           string    `json:"plan_name"`
	Currency           string    `json:"currency"`
	CurrentPeriodStart time.Time `json:"current_period_start"`
	CurrentPeriodEnd   time.Time `json:"current_period_end"`
}

type publicUsageMeter struct {
	MeterID          string             `json:"meter_id"`
	MeterKey         string             `json:"meter_key"`
	MeterName        string             `json:"meter_name"`
	Unit             string             `json:"unit"`
	Currency         string             `json:"currency"`
	TotalQuantity    decimal.Decimal    `json:"total_quantity"`
	TotalAmountCents int64              `json:"total_amount_cents"`
	Rules            []publicUsageRule  `json:"rules"`
}

type publicUsageRule struct {
	RuleKey        string          `json:"rule_key"`
	DimensionMatch map[string]any  `json:"dimension_match,omitempty"`
	Quantity       decimal.Decimal `json:"quantity"`
	AmountCents    int64           `json:"amount_cents"`
}

type publicTotal struct {
	Currency    string `json:"currency"`
	AmountCents int64  `json:"amount_cents"`
}

// publicThreshold is the future shape billing-alerts will project into.
// Kept here as an empty struct so the response shape is stable; the
// frontend can iterate `thresholds: []` today without refactor when
// alerts ship.
type publicThreshold struct{}

func emptyResponse(cust domain.Customer) publicCostDashboardResponse {
	return publicCostDashboardResponse{
		CustomerID: cust.ID,
		TenantID:   cust.TenantID,
		BillingPeriod: publicBillingPeriod{
			Source: "no_subscription",
		},
		Subscriptions: []publicSubscription{},
		Usage:         []publicUsageMeter{},
		Totals:        []publicTotal{},
		Thresholds:    []publicThreshold{},
		Warnings:      []string{},
	}
}

func projectResponse(cust domain.Customer, result usage.CustomerUsageResult) publicCostDashboardResponse {
	subs := make([]publicSubscription, 0, len(result.Subscriptions))
	for _, s := range result.Subscriptions {
		subs = append(subs, publicSubscription{
			ID:                 s.ID,
			PlanID:             s.PlanID,
			PlanName:           s.PlanName,
			Currency:           s.Currency,
			CurrentPeriodStart: s.CurrentPeriodStart,
			CurrentPeriodEnd:   s.CurrentPeriodEnd,
		})
	}
	meters := make([]publicUsageMeter, 0, len(result.Meters))
	for _, m := range result.Meters {
		rules := make([]publicUsageRule, 0, len(m.Rules))
		for _, ru := range m.Rules {
			rules = append(rules, publicUsageRule{
				RuleKey:        ru.RuleKey,
				DimensionMatch: ru.DimensionMatch,
				Quantity:       ru.Quantity,
				AmountCents:    ru.AmountCents,
			})
		}
		meters = append(meters, publicUsageMeter{
			MeterID:          m.MeterID,
			MeterKey:         m.MeterKey,
			MeterName:        m.MeterName,
			Unit:             m.Unit,
			Currency:         m.Currency,
			TotalQuantity:    m.TotalQuantity,
			TotalAmountCents: m.TotalAmountCents,
			Rules:            rules,
		})
	}
	totals := make([]publicTotal, 0, len(result.Totals))
	for _, t := range result.Totals {
		totals = append(totals, publicTotal{Currency: t.Currency, AmountCents: t.AmountCents})
	}
	warnings := result.Warnings
	if warnings == nil {
		warnings = []string{}
	}

	return publicCostDashboardResponse{
		CustomerID: cust.ID,
		TenantID:   cust.TenantID,
		BillingPeriod: publicBillingPeriod{
			From:   result.Period.From,
			To:     result.Period.To,
			Source: result.Period.Source,
		},
		Subscriptions: subs,
		Usage:         meters,
		Totals:        totals,
		Thresholds:    []publicThreshold{},
		Warnings:      warnings,
	}
}
