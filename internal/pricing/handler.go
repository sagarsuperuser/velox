package pricing

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

// MeterPricingRuleRoutes is the sub-router for
// /v1/meters/{meter_id}/pricing-rules. Reads use the read perm; upsert and
// delete need write. Pattern mirrors coupon.CustomerAssignmentRoutes so
// the router can compose perms in the same place — see router.go.
func (h *Handler) MeterPricingRuleRoutes(requireRead, requireWrite func(http.Handler) http.Handler) chi.Router {
	r := chi.NewRouter()
	r.With(requireRead).Get("/", h.listMeterPricingRules)
	r.With(requireWrite).Post("/", h.upsertMeterPricingRule)
	r.With(requireRead).Get("/{id}", h.getMeterPricingRule)
	r.With(requireWrite).Delete("/{id}", h.deleteMeterPricingRule)
	return r
}

func (h *Handler) createOverride(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	var input CreateOverrideInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	override, err := h.svc.CreateOverride(r.Context(), tenantID, input)
	if err != nil {
		respond.FromError(w, r, err, "price_override")
		return
	}

	respond.JSON(w, r, http.StatusCreated, override)
}

func (h *Handler) listOverrides(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	customerID := r.URL.Query().Get("customer_id")

	overrides, err := h.svc.ListOverrides(r.Context(), tenantID, customerID)
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "list overrides", "error", err)
		return
	}
	if overrides == nil {
		overrides = []domain.CustomerPriceOverride{}
	}

	respond.JSON(w, r, http.StatusOK, map[string]any{"data": overrides})
}

// ---------------------------------------------------------------------------
// Rating Rules
// ---------------------------------------------------------------------------

func (h *Handler) createRatingRule(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	var input CreateRatingRuleInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	rule, err := h.svc.CreateRatingRule(r.Context(), tenantID, input)
	if err != nil {
		respond.FromError(w, r, err, "rating_rule")
		return
	}

	respond.JSON(w, r, http.StatusCreated, rule)
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
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "list rating rules", "error", err)
		return
	}
	if rules == nil {
		rules = []domain.RatingRuleVersion{}
	}

	respond.JSON(w, r, http.StatusOK, map[string]any{"data": rules})
}

func (h *Handler) getRatingRule(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	rule, err := h.svc.GetRatingRule(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "rating rule")
		return
	}
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "get rating rule", "error", err)
		return
	}

	respond.JSON(w, r, http.StatusOK, rule)
}

// ---------------------------------------------------------------------------
// Meters
// ---------------------------------------------------------------------------

func (h *Handler) createMeter(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	var input CreateMeterInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	meter, err := h.svc.CreateMeter(r.Context(), tenantID, input)
	if err != nil {
		respond.FromError(w, r, err, "meter")
		return
	}

	respond.JSON(w, r, http.StatusCreated, meter)
}

func (h *Handler) listMeters(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	meters, err := h.svc.ListMeters(r.Context(), tenantID)
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "list meters", "error", err)
		return
	}
	if meters == nil {
		meters = []domain.Meter{}
	}

	respond.JSON(w, r, http.StatusOK, map[string]any{"data": meters})
}

func (h *Handler) getMeter(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	meter, err := h.svc.GetMeter(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "meter")
		return
	}
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "get meter", "error", err)
		return
	}

	respond.JSON(w, r, http.StatusOK, meter)
}

// ---------------------------------------------------------------------------
// Plans
// ---------------------------------------------------------------------------

func (h *Handler) createPlan(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	var input CreatePlanInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	plan, err := h.svc.CreatePlan(r.Context(), tenantID, input)
	if err != nil {
		respond.FromError(w, r, err, "plan")
		return
	}

	respond.JSON(w, r, http.StatusCreated, plan)
}

func (h *Handler) listPlans(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	plans, err := h.svc.ListPlans(r.Context(), tenantID)
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "list plans", "error", err)
		return
	}
	if plans == nil {
		plans = []domain.Plan{}
	}

	respond.JSON(w, r, http.StatusOK, map[string]any{"data": plans})
}

func (h *Handler) getPlan(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	plan, err := h.svc.GetPlan(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "plan")
		return
	}
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "get plan", "error", err)
		return
	}

	respond.JSON(w, r, http.StatusOK, plan)
}

func (h *Handler) updatePlan(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	var input CreatePlanInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	plan, err := h.svc.UpdatePlan(r.Context(), tenantID, id, input)
	if err != nil {
		respond.FromError(w, r, err, "plan")
		return
	}

	respond.JSON(w, r, http.StatusOK, plan)
}

// ---------------------------------------------------------------------------
// Meter Pricing Rules — N-rules-per-meter dispatch.
// ---------------------------------------------------------------------------

// listMeterPricingRules returns every pricing rule attached to a meter,
// sorted by priority-DESC / created-at-ASC (the store enforces the order).
func (h *Handler) listMeterPricingRules(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	meterID := chi.URLParam(r, "meter_id")

	rules, err := h.svc.ListMeterPricingRulesByMeter(r.Context(), tenantID, meterID)
	if err != nil {
		respond.FromError(w, r, err, "meter_pricing_rule")
		return
	}
	if rules == nil {
		rules = []domain.MeterPricingRule{}
	}
	respond.JSON(w, r, http.StatusOK, map[string]any{"data": rules})
}

// upsertMeterPricingRule creates or updates the rule identified by
// (meter_id, rating_rule_version_id). The URL meter_id wins over any
// meter_id in the body so the route surface is unambiguous.
func (h *Handler) upsertMeterPricingRule(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	meterID := chi.URLParam(r, "meter_id")

	var input UpsertMeterPricingRuleInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}
	input.MeterID = meterID

	rule, err := h.svc.UpsertMeterPricingRule(r.Context(), tenantID, input)
	if err != nil {
		respond.FromError(w, r, err, "meter_pricing_rule")
		return
	}
	respond.JSON(w, r, http.StatusCreated, rule)
}

func (h *Handler) getMeterPricingRule(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	rule, err := h.svc.GetMeterPricingRule(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "meter_pricing_rule")
		return
	}
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "get meter pricing rule", "error", err)
		return
	}
	respond.JSON(w, r, http.StatusOK, rule)
}

func (h *Handler) deleteMeterPricingRule(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	if err := h.svc.DeleteMeterPricingRule(r.Context(), tenantID, id); err != nil {
		if errors.Is(err, errs.ErrNotFound) {
			respond.NotFound(w, r, "meter_pricing_rule")
			return
		}
		respond.FromError(w, r, err, "meter_pricing_rule")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
