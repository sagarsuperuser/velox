package customer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/audit"
	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

type Handler struct {
	svc         *Service
	gdpr        *GDPRHandler
	auditLogger *audit.Logger
	costSvc     CostDashboardService
	// apiBaseURL is the public-facing origin used to compose the
	// `public_url` field on the rotate-cost-dashboard-token response.
	// Empty → the operator gets back just the relative path; production
	// wires the real origin via SetAPIBaseURL.
	apiBaseURL string
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// SetGDPR attaches the GDPR handler so its routes are served under /customers.
func (h *Handler) SetGDPR(gh *GDPRHandler) {
	h.gdpr = gh
}

// SetAuditLogger wires the audit logger. Currently used by the
// cost-dashboard token rotate endpoint (record that a rotation happened
// without leaking the plaintext token into the audit trail). Nil-safe —
// callers that don't audit just skip the entry.
func (h *Handler) SetAuditLogger(l *audit.Logger) {
	h.auditLogger = l
}

// SetAPIBaseURL wires the public-facing API origin used to compose
// the `public_url` field on the rotate-cost-dashboard-token response.
// Empty is OK — the response then carries just the relative API path
// and the operator composes the full URL themselves.
func (h *Handler) SetAPIBaseURL(u string) {
	h.apiBaseURL = u
}

func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/", h.create)
	r.Get("/", h.list)
	r.Get("/{id}", h.get)
	r.Patch("/{id}", h.update)
	r.Route("/{id}/billing-profile", func(r chi.Router) {
		r.Put("/", h.upsertBillingProfile)
		r.Get("/", h.getBillingProfile)
	})
	r.Post("/{id}/rotate-cost-dashboard-token", h.rotateCostDashboardToken)
	// GDPR endpoints (data export + right to erasure)
	if h.gdpr != nil {
		r.Get("/{id}/export", h.gdpr.exportData)
		r.Post("/{id}/delete-data", h.gdpr.deleteData)
	}
	return r
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	var input CreateInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	customer, err := h.svc.Create(r.Context(), tenantID, input)
	if err != nil {
		respond.FromError(w, r, err, "customer")
		return
	}

	respond.JSON(w, r, http.StatusCreated, customer)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	filter := ListFilter{
		TenantID:   tenantID,
		Status:     r.URL.Query().Get("status"),
		ExternalID: r.URL.Query().Get("external_id"),
		Limit:      limit,
		Offset:     offset,
		Sort:       r.URL.Query().Get("sort"),
		SortDir:    r.URL.Query().Get("dir"),
	}

	customers, total, err := h.svc.List(r.Context(), filter)
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "list customers", "error", err)
		return
	}

	if customers == nil {
		customers = []domain.Customer{}
	}

	respond.List(w, r, customers, total)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	customer, err := h.svc.Get(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "customer")
		return
	}
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "get customer", "error", err)
		return
	}

	respond.JSON(w, r, http.StatusOK, customer)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	var input UpdateInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	customer, err := h.svc.Update(r.Context(), tenantID, id, input)
	if err != nil {
		respond.FromError(w, r, err, "customer")
		return
	}

	respond.JSON(w, r, http.StatusOK, customer)
}

func (h *Handler) upsertBillingProfile(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	customerID := chi.URLParam(r, "id")

	var bp domain.CustomerBillingProfile
	if err := json.NewDecoder(r.Body).Decode(&bp); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}
	bp.CustomerID = customerID

	profile, err := h.svc.UpsertBillingProfile(r.Context(), tenantID, bp)
	if err != nil {
		respond.FromError(w, r, err, "billing profile")
		return
	}

	respond.JSON(w, r, http.StatusOK, profile)
}

func (h *Handler) getBillingProfile(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	customerID := chi.URLParam(r, "id")

	profile, err := h.svc.GetBillingProfile(r.Context(), tenantID, customerID)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "billing profile")
		return
	}
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "get billing profile", "error", err)
		return
	}

	respond.JSON(w, r, http.StatusOK, profile)
}

// rotateCostDashboardToken mints a fresh public cost-dashboard token
// and writes it to the customer row. The old token is invalidated
// immediately (no grace window — read-only surface, rotation intent
// is "stop the previous URL right now").
//
// Response body: { "token": "vlx_pcd_<64 hex>", "public_url": "<url>" }.
// Audit log records the rotation with no plaintext token.
func (h *Handler) rotateCostDashboardToken(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == "" {
		respond.BadRequest(w, r, "missing customer id")
		return
	}

	token, err := h.svc.RotateCostDashboardToken(r.Context(), tenantID, id)
	if err != nil {
		respond.FromError(w, r, err, "customer")
		return
	}

	publicURL := "/v1/public/cost-dashboard/" + token
	if h.apiBaseURL != "" {
		publicURL = strings.TrimRight(h.apiBaseURL, "/") + publicURL
	}

	if h.auditLogger != nil {
		_ = h.auditLogger.Log(r.Context(), tenantID, domain.AuditActionRotate, "customer", id, map[string]any{
			"surface": "cost_dashboard_token",
		})
	}

	respond.JSON(w, r, http.StatusOK, map[string]string{
		"token":      token,
		"public_url": publicURL,
	})
}

// publicCostDashboard composes a sanitized cost-dashboard projection
// for the customer behind the token in the URL path. Unauthenticated;
// the 256-bit token IS the credential. Returns 401 when the token
// doesn't resolve (anti-enumeration — same 401 for invalid /
// never-existed).
//
// Sanitization: customer PII (email, display_name, external_id,
// metadata, billing_profile) is NEVER on this response. Only
// billing-relevant fields: customer_id, tenant_id, billing_period,
// subscriptions (id + plan name + period only), usage (meter + rules
// + totals), totals, projected_total_cents.
func (h *Handler) publicCostDashboard(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(chi.URLParam(r, "token"))
	if token == "" || !strings.HasPrefix(token, costDashboardTokenPrefix) {
		respond.Unauthorized(w, r, "invalid cost-dashboard token")
		return
	}
	if h.costSvc == nil {
		respond.FromError(w, r, fmt.Errorf("cost dashboard service not wired"), "cost_dashboard")
		return
	}
	proj, err := h.costSvc.GetByToken(r.Context(), token)
	if err != nil {
		if errors.Is(err, errs.ErrNotFound) {
			respond.Unauthorized(w, r, "invalid cost-dashboard token")
			return
		}
		respond.FromError(w, r, err, "cost_dashboard")
		return
	}
	respond.JSON(w, r, http.StatusOK, proj)
}

// SetCostDashboardService wires the public-projection assembler used
// by GET /v1/public/cost-dashboard/{token}. Optional in narrow tests.
func (h *Handler) SetCostDashboardService(s CostDashboardService) {
	h.costSvc = s
}

// PublicCostDashboardRoutes returns the chi router for the
// unauthenticated /v1/public/cost-dashboard/{token} surface. Mounted
// separately in router.go with its own rate-limit bucket.
func (h *Handler) PublicCostDashboardRoutes() chi.Router {
	r := chi.NewRouter()
	r.Get("/{token}", h.publicCostDashboard)
	return r
}

// CostDashboardService is the narrow shape used by the public route
// to compose the sanitized projection. Implemented by the cost-
// dashboard assembler wired in router.go from the existing
// customer-usage service.
type CostDashboardService interface {
	GetByToken(ctx context.Context, token string) (any, error)
}
