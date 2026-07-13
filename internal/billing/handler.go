package billing

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

	// ADR-090: the operator's TRIGGER gets its own row. The per-invoice
	// finalize rows this run writes cannot answer "who started this cycle" —
	// they are byte-identical whether the scheduler or an operator drove it.
	//
	// Residual OWN-TX emission, deliberately post-effect: the route owns no
	// transaction (each invoice already committed inside the engine, with its
	// own in-tx evidence), so there is nothing left to share fate with. The row
	// therefore records what the run DID — the invoice count it actually
	// produced — which is only knowable after it returns. A successful Log
	// self-marks the request as accounted-for.
	if aw := h.engine.auditWriter(); aw != nil {
		if err := aw.Log(r.Context(), tenantID, domain.AuditActionRun, "billing", "", "", map[string]any{
			"action":           "cycle_run_triggered",
			"invoices_created": generated,
			"failures":         len(failures),
		}); err != nil {
			// Never swallowed (`_ =` is the pattern ADR-090 kills). The run is
			// already committed, so the response keeps its invoice count rather
			// than lying with a 500 — but the caller is TOLD the trigger could
			// not be recorded, and the request stays UNMARKED so the
			// AuditCoverage detector reports the uncovered mutation.
			slog.ErrorContext(r.Context(), "billing run: audit trigger row failed",
				"tenant_id", tenantID, "error", err)
			errStrings = append(errStrings, "billing run completed but its audit record failed")
		}
	}

	w.Header().Set("Content-Type", "application/json")
	// Status keys off the surfaced error list, not the failure slice: an
	// unrecorded trigger is a partial outcome too, and a 200 alongside a
	// non-empty `errors` array would be a lie.
	if len(errStrings) > 0 {
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
