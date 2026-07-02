package billing

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/errs"
)

type Handler struct {
	engine *Engine
	subs   SubscriptionReader
}

func NewHandler(engine *Engine, subs SubscriptionReader) *Handler {
	return &Handler{engine: engine, subs: subs}
}

func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/run", h.triggerCycle)
	r.Get("/preview/{subscription_id}", h.preview)
	return r
}

func (h *Handler) triggerCycle(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	if tenantID == "" {
		// Platform keys carry no tenant scope. Fail closed — NEVER fall through
		// to the unscoped, cross-tenant RunCycle (the pre-fix leak). RunCycle
		// stays scheduler-only.
		respond.Forbidden(w, r, "billing run requires a tenant-scoped secret key, not a platform key")
		return
	}

	generated, failures := h.engine.RunCycleForTenant(r.Context(), tenantID, 50)

	// Full detail (pq constraint names, Stripe internals) goes to the server log
	// only; the API caller gets its own subscription ids + a generic class. Even
	// a tenant's OWN raw errors leak DB/provider internals, so they are stripped.
	errStrings := make([]string, 0, len(failures))
	for _, f := range failures {
		slog.ErrorContext(r.Context(), "billing run: subscription failed",
			"tenant_id", tenantID,
			"subscription_id", f.SubscriptionID,
			"error", f.Err,
		)
		if f.SubscriptionID != "" {
			errStrings = append(errStrings, "subscription "+f.SubscriptionID+": billing failed")
		} else {
			errStrings = append(errStrings, "billing run failed")
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if len(failures) > 0 {
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.WriteHeader(http.StatusOK)
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"invoices_generated": generated,
		"errors":             errStrings,
	})
}

func (h *Handler) preview(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	subID := chi.URLParam(r, "subscription_id")

	sub, err := h.subs.Get(r.Context(), tenantID, subID)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "subscription")
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "billing preview: get subscription", "error", err, "subscription_id", subID)
		respond.InternalError(w, r)
		return
	}

	preview, err := h.engine.Preview(r.Context(), sub)
	if err != nil {
		respond.FromError(w, r, err, "subscription")
		return
	}

	respond.JSON(w, r, http.StatusOK, preview)
}
