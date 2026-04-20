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
	generated, errs := h.engine.RunCycle(r.Context(), 50)

	errStrings := make([]string, len(errs))
	for i, e := range errs {
		errStrings[i] = e.Error()
	}

	w.Header().Set("Content-Type", "application/json")
	if len(errs) > 0 {
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
