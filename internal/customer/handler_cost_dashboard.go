package customer

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

// rotateCostDashboardTokenResponse is what the operator sees after a
// successful rotate. The token is included once (so the operator can copy
// it / hand it to a frontend that needs the raw value); public_url is the
// path-only convenience URL the frontend should use to embed the iframe.
//
// Path-only (no scheme/host): the operator's product is responsible for
// adding their own origin — single-tenant self-hosters and the cloud
// build run under different origins, and the API doesn't try to guess.
// Mirrors how Stripe returns hosted_invoice_url paths in some test
// fixtures and how Vercel renders preview URLs.
type rotateCostDashboardTokenResponse struct {
	Token     string `json:"token"`
	PublicURL string `json:"public_url"`
	CustomerID string `json:"customer_id"`
}

// rotateCostDashboardToken mints (or rotates) the public cost-dashboard
// URL token for one customer. Defensive rotation invalidates the
// previous URL — useful when the operator suspects the embed iframe has
// been re-shared somewhere it shouldn't have been (a customer ticket, a
// public bug report, a screenshot). Mirrors invoice.rotatePublicToken
// in shape, with two differences:
//
//   - No "must be in state X" guard: any customer (active, archived,
//     test-mode) can have a token. Drafts-don't-leak doesn't apply
//     here; the public surface itself sanitises every field that could
//     leak operator state.
//   - The response carries a public_url helper so frontends don't have
//     to reconstruct the path from the token + a hardcoded prefix.
func (h *Handler) rotateCostDashboardToken(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	cust, err := h.svc.Get(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "customer")
		return
	}
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "rotate cost dashboard token: get customer",
			"customer_id", id, "error", err)
		return
	}

	previousTokenWasUnset := cust.CostDashboardToken == ""
	token, err := h.svc.RotateCostDashboardToken(r.Context(), tenantID, id)
	if err != nil {
		respond.FromError(w, r, err, "customer")
		return
	}

	if h.auditLogger != nil {
		// NEVER log the token plaintext — the audit trail is itself a
		// credential surface (queryable by any operator with audit
		// read permissions). Record only that a rotation happened
		// and whether the customer had a token before.
		_ = h.auditLogger.Log(r.Context(), tenantID, domain.AuditActionRotate, "customer", cust.ID, map[string]any{
			"external_id":              cust.ExternalID,
			"field":                    "cost_dashboard_token",
			"previous_token_was_unset": previousTokenWasUnset,
		})
	}

	respond.JSON(w, r, http.StatusOK, rotateCostDashboardTokenResponse{
		Token:      token,
		PublicURL:  "/v1/public/cost-dashboard/" + token,
		CustomerID: cust.ID,
	})
}
