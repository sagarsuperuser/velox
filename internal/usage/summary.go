package usage

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/sagarsuperuser/velox/internal/auth"
)

// UsageSummary represents aggregated usage for a customer in a period.
type UsageSummary struct {
	CustomerID  string                     `json:"customer_id"`
	PeriodFrom  time.Time                  `json:"period_from"`
	PeriodTo    time.Time                  `json:"period_to"`
	Meters      map[string]decimal.Decimal `json:"meters"` // meter_id -> total quantity
	TotalEvents int                        `json:"total_events"`
}

// GetSummary aggregates usage for a customer in the current billing period.
func (s *Service) GetSummary(ctx context.Context, tenantID, customerID string, from, to time.Time) (UsageSummary, error) {
	events, _, err := s.store.List(ctx, ListFilter{
		TenantID:   tenantID,
		CustomerID: customerID,
		From:       &from,
		To:         &to,
		Limit:      10000,
	})
	if err != nil {
		return UsageSummary{}, err
	}

	meters := make(map[string]decimal.Decimal)
	for _, e := range events {
		meters[e.MeterID] = meters[e.MeterID].Add(e.Quantity)
	}

	return UsageSummary{
		CustomerID:  customerID,
		PeriodFrom:  from,
		PeriodTo:    to,
		Meters:      meters,
		TotalEvents: len(events),
	}, nil
}

// SummaryHandler handles the usage summary HTTP endpoint.
func (h *Handler) SummaryRoutes() chi.Router {
	r := chi.NewRouter()
	r.Get("/{customer_id}", h.getSummary)
	return r
}

func (h *Handler) getSummary(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	customerID := chi.URLParam(r, "customer_id")

	// Default to current month
	now := time.Now().UTC()
	from := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 1, 0)

	// Allow custom period via query params
	if f := r.URL.Query().Get("from"); f != "" {
		if t, err := time.Parse(time.RFC3339, f); err == nil {
			from = t
		}
	}
	if t := r.URL.Query().Get("to"); t != "" {
		if parsed, err := time.Parse(time.RFC3339, t); err == nil {
			to = parsed
		}
	}

	summary, err := h.svc.GetSummary(r.Context(), tenantID, customerID, from, to)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "internal_error"})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(summary)
}
