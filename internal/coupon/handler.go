package coupon

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/domain"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/", h.create)
	r.Get("/", h.list)
	r.Get("/{id}", h.get)
	r.Post("/{id}/deactivate", h.deactivate)
	r.Post("/redeem", h.redeem)
	r.Get("/{id}/redemptions", h.listRedemptions)
	return r
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	var input CreateInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	cpn, err := h.svc.Create(r.Context(), tenantID, input)
	if err != nil {
		respond.FromError(w, r, err, "coupon")
		return
	}

	respond.JSON(w, r, http.StatusCreated, cpn)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	coupons, err := h.svc.List(r.Context(), tenantID)
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "list coupons", "error", err)
		return
	}
	if coupons == nil {
		coupons = []domain.Coupon{}
	}

	respond.JSON(w, r, http.StatusOK, map[string]any{"data": coupons})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	cpn, err := h.svc.Get(r.Context(), tenantID, id)
	if err != nil {
		respond.FromError(w, r, err, "coupon")
		return
	}

	respond.JSON(w, r, http.StatusOK, cpn)
}

func (h *Handler) deactivate(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	if err := h.svc.Deactivate(r.Context(), tenantID, id); err != nil {
		respond.FromError(w, r, err, "coupon")
		return
	}

	respond.JSON(w, r, http.StatusOK, map[string]string{"status": "deactivated"})
}

func (h *Handler) redeem(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	var input RedeemInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	redemption, err := h.svc.Redeem(r.Context(), tenantID, input)
	if err != nil {
		respond.FromError(w, r, err, "coupon")
		return
	}

	respond.JSON(w, r, http.StatusCreated, redemption)
}

func (h *Handler) listRedemptions(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	couponID := chi.URLParam(r, "id")

	redemptions, err := h.svc.ListRedemptions(r.Context(), tenantID, couponID)
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "list coupon redemptions", "error", err)
		return
	}
	if redemptions == nil {
		redemptions = []domain.CouponRedemption{}
	}

	respond.JSON(w, r, http.StatusOK, map[string]any{"data": redemptions})
}
