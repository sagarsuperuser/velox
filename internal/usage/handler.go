package usage

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/", h.ingest)
	r.Post("/batch", h.batchIngest)
	r.Get("/", h.list)
	return r
}

func (h *Handler) ingest(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	var input IngestInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	event, err := h.svc.Ingest(r.Context(), tenantID, input)
	if errors.Is(err, errs.ErrDuplicateKey) {
		respond.Conflict(w, r, err.Error())
		return
	}
	if err != nil {
		respond.Validation(w, r, err.Error())
		return
	}

	respond.JSON(w, r, http.StatusCreated, event)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	events, err := h.svc.List(r.Context(), ListFilter{
		TenantID:   tenantID,
		CustomerID: r.URL.Query().Get("customer_id"),
		MeterID:    r.URL.Query().Get("meter_id"),
		Limit:      limit,
		Offset:     offset,
	})
	if err != nil {
		respond.InternalError(w, r)
		slog.Error("list usage events", "error", err)
		return
	}
	if events == nil {
		events = []domain.UsageEvent{}
	}

	respond.JSON(w, r, http.StatusOK, map[string]any{"data": events})
}

func (h *Handler) batchIngest(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	var events []IngestInput
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

	ingested, errs := h.svc.BatchIngest(r.Context(), tenantID, events)

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
