package pricing

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

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

func (h *Handler) MeterRoutes() chi.Router {
	r := chi.NewRouter()
	r.Post("/", h.createMeter)
	r.Get("/", h.listMeters)
	r.Get("/{id}", h.getMeter)
	return r
}

func (h *Handler) PlanRoutes() chi.Router {
	r := chi.NewRouter()
	r.Post("/", h.createPlan)
	r.Get("/", h.listPlans)
	r.Get("/{id}", h.getPlan)
	r.Patch("/{id}", h.updatePlan)
	return r
}

func (h *Handler) RatingRuleRoutes() chi.Router {
	r := chi.NewRouter()
	r.Post("/", h.createRatingRule)
	r.Get("/", h.listRatingRules)
	r.Get("/{id}", h.getRatingRule)
	return r
}

func (h *Handler) OverrideRoutes() chi.Router {
	r := chi.NewRouter()
	r.Post("/", h.createOverride)
	r.Get("/", h.listOverrides)
	return r
}

func (h *Handler) createOverride(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	var input CreateOverrideInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	override, err := h.svc.CreateOverride(r.Context(), tenantID, input)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, override)
}

func (h *Handler) listOverrides(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	customerID := r.URL.Query().Get("customer_id")

	overrides, err := h.svc.ListOverrides(r.Context(), tenantID, customerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to list overrides")
		slog.Error("list overrides", "error", err)
		return
	}
	if overrides == nil {
		overrides = []domain.CustomerPriceOverride{}
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": overrides})
}

// ---------------------------------------------------------------------------
// Rating Rules
// ---------------------------------------------------------------------------

func (h *Handler) createRatingRule(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	var input CreateRatingRuleInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	rule, err := h.svc.CreateRatingRule(r.Context(), tenantID, input)
	if errors.Is(err, errs.ErrAlreadyExists) {
		writeError(w, http.StatusConflict, "already_exists", err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, rule)
}

func (h *Handler) listRatingRules(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	filter := RatingRuleFilter{
		TenantID:       tenantID,
		RuleKey:        r.URL.Query().Get("rule_key"),
		LifecycleState: r.URL.Query().Get("lifecycle_state"),
		LatestOnly:     r.URL.Query().Get("latest") == "true",
	}

	rules, err := h.svc.ListRatingRules(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to list rating rules")
		slog.Error("list rating rules", "error", err)
		return
	}
	if rules == nil {
		rules = []domain.RatingRuleVersion{}
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": rules})
}

func (h *Handler) getRatingRule(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	rule, err := h.svc.GetRatingRule(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "rating rule not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to get rating rule")
		slog.Error("get rating rule", "error", err)
		return
	}

	writeJSON(w, http.StatusOK, rule)
}

// ---------------------------------------------------------------------------
// Meters
// ---------------------------------------------------------------------------

func (h *Handler) createMeter(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	var input CreateMeterInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	meter, err := h.svc.CreateMeter(r.Context(), tenantID, input)
	if errors.Is(err, errs.ErrAlreadyExists) {
		writeError(w, http.StatusConflict, "already_exists", err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, meter)
}

func (h *Handler) listMeters(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	meters, err := h.svc.ListMeters(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to list meters")
		slog.Error("list meters", "error", err)
		return
	}
	if meters == nil {
		meters = []domain.Meter{}
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": meters})
}

func (h *Handler) getMeter(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	meter, err := h.svc.GetMeter(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "meter not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to get meter")
		slog.Error("get meter", "error", err)
		return
	}

	writeJSON(w, http.StatusOK, meter)
}

// ---------------------------------------------------------------------------
// Plans
// ---------------------------------------------------------------------------

func (h *Handler) createPlan(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	var input CreatePlanInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	plan, err := h.svc.CreatePlan(r.Context(), tenantID, input)
	if errors.Is(err, errs.ErrAlreadyExists) {
		writeError(w, http.StatusConflict, "already_exists", err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, plan)
}

func (h *Handler) listPlans(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	plans, err := h.svc.ListPlans(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to list plans")
		slog.Error("list plans", "error", err)
		return
	}
	if plans == nil {
		plans = []domain.Plan{}
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": plans})
}

func (h *Handler) getPlan(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	plan, err := h.svc.GetPlan(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "plan not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to get plan")
		slog.Error("get plan", "error", err)
		return
	}

	writeJSON(w, http.StatusOK, plan)
}

func (h *Handler) updatePlan(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	var input CreatePlanInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	plan, err := h.svc.UpdatePlan(r.Context(), tenantID, id, input)
	if errors.Is(err, errs.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "plan not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, plan)
}

// JSON helpers
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"error": code, "message": message})
}
