package paymentmethods

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/customerportal"
	"github.com/sagarsuperuser/velox/internal/errs"
)

// Handler exposes /v1/me/payment-methods, the customer-facing self-service
// endpoints. All routes run under customerportal.Middleware, so the caller
// identity is read from ctx (tenant_id + customer_id), not from auth
// middleware tenant ctx — the /me namespace deliberately doesn't use API
// keys.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", h.list)
	r.Post("/setup-intent", h.createSetupIntent)
	r.Post("/setup-session", h.createSetupSession)
	r.Post("/{id}/default", h.setDefault)
	r.Delete("/{id}", h.detach)
	return r
}

type pmResponse struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	CardBrand string    `json:"card_brand,omitempty"`
	CardLast4 string    `json:"card_last4,omitempty"`
	CardExpMo int       `json:"card_exp_month,omitempty"`
	CardExpYr int       `json:"card_exp_year,omitempty"`
	IsDefault bool      `json:"is_default"`
	CreatedAt time.Time `json:"created_at"`
}

func toResp(pm PaymentMethod) pmResponse {
	return pmResponse{
		ID: pm.ID, Type: pm.Type,
		CardBrand: pm.CardBrand, CardLast4: pm.CardLast4,
		CardExpMo: pm.CardExpMonth, CardExpYr: pm.CardExpYear,
		IsDefault: pm.IsDefault, CreatedAt: pm.CreatedAt,
	}
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	tenantID, customerID, ok := identity(r)
	if !ok {
		respond.Unauthorized(w, r, "missing portal session context")
		return
	}
	pms, err := h.svc.List(r.Context(), tenantID, customerID)
	if err != nil {
		respond.InternalError(w, r)
		return
	}
	out := make([]pmResponse, 0, len(pms))
	for _, pm := range pms {
		out = append(out, toResp(pm))
	}
	respond.List(w, r, out, len(out))
}

type setupIntentResponse struct {
	ClientSecret  string `json:"client_secret"`
	SetupIntentID string `json:"setup_intent_id"`
}

func (h *Handler) createSetupIntent(w http.ResponseWriter, r *http.Request) {
	tenantID, customerID, ok := identity(r)
	if !ok {
		respond.Unauthorized(w, r, "missing portal session context")
		return
	}
	secret, siID, err := h.svc.CreateSetupIntent(r.Context(), tenantID, customerID)
	if err != nil {
		respond.FromError(w, r, err, "payment_method")
		return
	}
	respond.JSON(w, r, http.StatusCreated, setupIntentResponse{
		ClientSecret: secret, SetupIntentID: siID,
	})
}

type setupSessionRequest struct {
	ReturnURL string `json:"return_url,omitempty"`
}

type setupSessionResponse struct {
	URL       string `json:"url"`
	SessionID string `json:"session_id"`
}

func (h *Handler) createSetupSession(w http.ResponseWriter, r *http.Request) {
	tenantID, customerID, ok := identity(r)
	if !ok {
		respond.Unauthorized(w, r, "missing portal session context")
		return
	}
	var req setupSessionRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	url, sessionID, err := h.svc.CreateSetupSession(r.Context(), tenantID, customerID, req.ReturnURL)
	if err != nil {
		respond.FromError(w, r, err, "payment_method")
		return
	}
	respond.JSON(w, r, http.StatusCreated, setupSessionResponse{URL: url, SessionID: sessionID})
}

func (h *Handler) setDefault(w http.ResponseWriter, r *http.Request) {
	tenantID, customerID, ok := identity(r)
	if !ok {
		respond.Unauthorized(w, r, "missing portal session context")
		return
	}
	pmID := chi.URLParam(r, "id")
	if pmID == "" {
		respond.BadRequest(w, r, "missing payment method id")
		return
	}
	pm, err := h.svc.SetDefault(r.Context(), tenantID, customerID, pmID)
	if err != nil {
		if errors.Is(err, errs.ErrNotFound) {
			respond.NotFound(w, r, "payment_method")
			return
		}
		respond.InternalError(w, r)
		return
	}
	respond.JSON(w, r, http.StatusOK, toResp(pm))
}

func (h *Handler) detach(w http.ResponseWriter, r *http.Request) {
	tenantID, customerID, ok := identity(r)
	if !ok {
		respond.Unauthorized(w, r, "missing portal session context")
		return
	}
	pmID := chi.URLParam(r, "id")
	if pmID == "" {
		respond.BadRequest(w, r, "missing payment method id")
		return
	}
	pm, err := h.svc.Detach(r.Context(), tenantID, customerID, pmID)
	if err != nil {
		if errors.Is(err, errs.ErrNotFound) {
			respond.NotFound(w, r, "payment_method")
			return
		}
		respond.InternalError(w, r)
		return
	}
	respond.JSON(w, r, http.StatusOK, toResp(pm))
}

// identity pulls the portal-session context the middleware injected. If
// either field is empty the request didn't pass through Middleware — we
// treat that as an auth failure rather than silently fall through.
func identity(r *http.Request) (tenantID, customerID string, ok bool) {
	tenantID = customerportal.TenantID(r.Context())
	customerID = customerportal.CustomerID(r.Context())
	return tenantID, customerID, tenantID != "" && customerID != ""
}
