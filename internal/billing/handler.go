package billing

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
)

type Handler struct {
	engine *Engine
}

func NewHandler(engine *Engine) *Handler {
	return &Handler{engine: engine}
}

func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/run", h.triggerCycle)
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

	json.NewEncoder(w).Encode(map[string]any{
		"invoices_generated": generated,
		"errors":             errStrings,
	})
}
