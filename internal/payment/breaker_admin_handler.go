package payment

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/payment/breaker"
)

// BreakerAdminHandler exposes operator endpoints for inspecting and resetting
// the per-tenant Stripe circuit breaker. Scoped to the caller's own tenant —
// there is no cross-tenant reset, so a compromised tenant key cannot clear
// another tenant's breaker and force a retry storm into a shared incident.
//
// The caller's intended flow: Stripe's status page goes green, the operator
// POSTs reset, Velox stops waiting out the cooldown and resumes charging.
type BreakerAdminHandler struct {
	breaker *breaker.Breaker
}

func NewBreakerAdminHandler(b *breaker.Breaker) *BreakerAdminHandler {
	return &BreakerAdminHandler{breaker: b}
}

func (h *BreakerAdminHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", h.getState)
	r.Post("/reset", h.reset)
	return r
}

func (h *BreakerAdminHandler) getState(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	if tenantID == "" {
		respond.Error(w, r, http.StatusUnauthorized, "authentication_error", "unauthorized", "tenant not resolved")
		return
	}
	respond.JSON(w, r, http.StatusOK, map[string]string{
		"tenant_id": tenantID,
		"state":     string(h.breaker.State(tenantID)),
	})
}

func (h *BreakerAdminHandler) reset(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	if tenantID == "" {
		respond.Error(w, r, http.StatusUnauthorized, "authentication_error", "unauthorized", "tenant not resolved")
		return
	}
	h.breaker.Reset(tenantID)
	slog.Info("stripe breaker manually reset", "tenant_id", tenantID)
	respond.JSON(w, r, http.StatusOK, map[string]string{
		"tenant_id": tenantID,
		"state":     string(h.breaker.State(tenantID)),
		"reset":     "ok",
	})
}
