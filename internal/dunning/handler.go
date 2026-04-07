package dunning

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

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

	r.Route("/policy", func(r chi.Router) {
		r.Get("/", h.getPolicy)
		r.Put("/", h.upsertPolicy)
	})

	r.Route("/runs", func(r chi.Router) {
		r.Get("/", h.listRuns)
		r.Get("/{id}", h.getRun)
		r.Post("/{id}/resolve", h.resolveRun)
	})

	return r
}

func (h *Handler) getPolicy(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	policy, err := h.svc.GetPolicy(r.Context(), tenantID)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "dunning policy")
		return
	}
	if err != nil {
		respond.InternalError(w, r)
		slog.Error("get dunning policy", "error", err)
		return
	}

	respond.JSON(w, r, http.StatusOK, policy)
}

func (h *Handler) upsertPolicy(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	var policy domain.DunningPolicy
	if err := json.NewDecoder(r.Body).Decode(&policy); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	result, err := h.svc.UpsertPolicy(r.Context(), tenantID, policy)
	if err != nil {
		respond.Validation(w, r, err.Error())
		return
	}

	respond.JSON(w, r, http.StatusOK, result)
}

func (h *Handler) listRuns(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	runs, err := h.svc.ListRuns(r.Context(), RunListFilter{
		TenantID:  tenantID,
		InvoiceID: r.URL.Query().Get("invoice_id"),
		State:     r.URL.Query().Get("state"),
	})
	if err != nil {
		respond.InternalError(w, r)
		slog.Error("list dunning runs", "error", err)
		return
	}
	if runs == nil {
		runs = []domain.InvoiceDunningRun{}
	}

	respond.JSON(w, r, http.StatusOK, map[string]any{"data": runs})
}

func (h *Handler) getRun(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	run, err := h.svc.store.GetRun(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "dunning run")
		return
	}
	if err != nil {
		respond.InternalError(w, r)
		slog.Error("get dunning run", "error", err)
		return
	}

	events, _ := h.svc.store.ListEvents(r.Context(), tenantID, id)
	if events == nil {
		events = []domain.InvoiceDunningEvent{}
	}

	respond.JSON(w, r, http.StatusOK, map[string]any{
		"run":    run,
		"events": events,
	})
}

type resolveInput struct {
	Resolution string `json:"resolution"`
}

func (h *Handler) resolveRun(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	var input resolveInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	run, err := h.svc.ResolveRun(r.Context(), tenantID, id, domain.DunningResolution(input.Resolution))
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "dunning run")
		return
	}
	if err != nil {
		respond.Validation(w, r, err.Error())
		return
	}

	respond.JSON(w, r, http.StatusOK, run)
}
