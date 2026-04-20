package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

// CustomerResolver looks up a customer by external ID.
type CustomerResolver interface {
	GetByExternalID(ctx context.Context, tenantID, externalID string) (domain.Customer, error)
}

// MeterResolver looks up a meter by key.
type MeterResolver interface {
	GetMeterByKey(ctx context.Context, tenantID, key string) (domain.Meter, error)
}

type Handler struct {
	svc       *Service
	customers CustomerResolver
	meters    MeterResolver
}

func NewHandler(svc *Service, customers CustomerResolver, meters MeterResolver) *Handler {
	return &Handler{svc: svc, customers: customers, meters: meters}
}

func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/", h.ingest)
	r.Post("/batch", h.batchIngest)
	r.Get("/", h.list)
	return r
}

// Backfill is exported so the router can mount it behind PermUsageWrite —
// the /usage-events subtree uses PermUsageRead for historical reasons, but
// backfill is a sensitive ledger operation and should not be reachable by
// read-only keys.
func (h *Handler) Backfill(w http.ResponseWriter, r *http.Request) {
	h.backfill(w, r)
}

// apiEvent is the public API input — developers send external identifiers.
type apiEvent struct {
	ExternalCustomerID string           `json:"external_customer_id"`
	EventName          string           `json:"event_name"`
	Quantity           int64            `json:"quantity,omitempty"`
	Properties         map[string]any   `json:"properties,omitempty"`
	IdempotencyKey     string           `json:"idempotency_key,omitempty"`
	Timestamp          *json.RawMessage `json:"timestamp,omitempty"`
}

// resolve converts public API identifiers to internal IDs.
func (h *Handler) resolve(ctx context.Context, tenantID string, evt apiEvent) (IngestInput, error) {
	extCust := strings.TrimSpace(evt.ExternalCustomerID)
	eventName := strings.TrimSpace(evt.EventName)

	if extCust == "" {
		return IngestInput{}, errs.Required("external_customer_id")
	}
	if eventName == "" {
		return IngestInput{}, errs.Required("event_name")
	}

	cust, err := h.customers.GetByExternalID(ctx, tenantID, extCust)
	if err != nil {
		return IngestInput{}, errs.Invalid("external_customer_id", fmt.Sprintf("customer %q not found", extCust))
	}

	meter, err := h.meters.GetMeterByKey(ctx, tenantID, eventName)
	if err != nil {
		return IngestInput{}, errs.Invalid("event_name", fmt.Sprintf("meter %q not found", eventName))
	}

	input := IngestInput{
		CustomerID:     cust.ID,
		MeterID:        meter.ID,
		Quantity:       evt.Quantity,
		Properties:     evt.Properties,
		IdempotencyKey: evt.IdempotencyKey,
	}

	if evt.Timestamp != nil {
		var ts interface{}
		if err := json.Unmarshal(*evt.Timestamp, &ts); err == nil {
			if tsStr, ok := ts.(string); ok {
				if parsed, err := parseTimestamp(tsStr); err == nil {
					input.Timestamp = &parsed
				}
			}
		}
	}

	return input, nil
}

func (h *Handler) ingest(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	var evt apiEvent
	if err := json.NewDecoder(r.Body).Decode(&evt); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	input, err := h.resolve(r.Context(), tenantID, evt)
	if err != nil {
		respond.FromError(w, r, err, "usage_event")
		return
	}

	event, err := h.svc.Ingest(r.Context(), tenantID, input)
	if err != nil {
		respond.FromError(w, r, err, "usage_event")
		return
	}

	respond.JSON(w, r, http.StatusCreated, event)
}

// backfill ingests a historical usage event — same payload shape as POST /
// but the service enforces that timestamp is non-nil and strictly in the
// past, and the row is tagged origin='backfill' in the audit trail.
//
// Use cases: CSV imports from a legacy system, reconciliation after a
// dropped-event incident, migrating from another billing vendor. For
// real-time ingest, use POST /usage-events instead.
func (h *Handler) backfill(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	var evt apiEvent
	if err := json.NewDecoder(r.Body).Decode(&evt); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	input, err := h.resolve(r.Context(), tenantID, evt)
	if err != nil {
		respond.FromError(w, r, err, "usage_event")
		return
	}

	event, err := h.svc.Backfill(r.Context(), tenantID, input)
	if err != nil {
		respond.FromError(w, r, err, "usage_event")
		return
	}

	respond.JSON(w, r, http.StatusCreated, event)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	events, total, err := h.svc.List(r.Context(), ListFilter{
		TenantID:   tenantID,
		CustomerID: r.URL.Query().Get("customer_id"),
		MeterID:    r.URL.Query().Get("meter_id"),
		Limit:      limit,
		Offset:     offset,
	})
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "list usage events", "error", err)
		return
	}
	if events == nil {
		events = []domain.UsageEvent{}
	}

	respond.List(w, r, events, total)
}

func (h *Handler) batchIngest(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	var events []apiEvent
	if err := json.NewDecoder(r.Body).Decode(&events); err != nil {
		respond.BadRequest(w, r, "expected JSON array of events")
		return
	}

	if len(events) == 0 {
		respond.BadRequest(w, r, "at least one event is required")
		return
	}
	if len(events) > 1000 {
		respond.BadRequest(w, r, "maximum 1000 events per batch")
		return
	}

	var inputs []IngestInput
	for i, evt := range events {
		input, err := h.resolve(r.Context(), tenantID, evt)
		if err != nil {
			respond.Validation(w, r, fmt.Sprintf("event[%d]: %s", i, err.Error()))
			return
		}
		inputs = append(inputs, input)
	}

	ingested, errs := h.svc.BatchIngest(r.Context(), tenantID, inputs)

	errStrings := make([]string, len(errs))
	for i, e := range errs {
		errStrings[i] = e.Error()
	}

	status := http.StatusCreated
	if len(errs) > 0 {
		status = http.StatusPartialContent
	}

	respond.JSON(w, r, status, map[string]any{
		"ingested": ingested,
		"errors":   errStrings,
		"total":    len(events),
	})
}

func parseTimestamp(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("invalid timestamp format: %s", s)
}
