package customerportal

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/respond"
)

// PublicHandler serves the unauthenticated half of the customer portal:
// request-a-magic-link (POST) and consume-a-magic-link (GET, added in P4).
// Mounted under /v1/public/customer-portal with no API-key auth — the
// email + the minted token are the only credentials involved.
type PublicHandler struct {
	request *MagicLinkRequestService
}

func NewPublicHandler(request *MagicLinkRequestService) *PublicHandler {
	return &PublicHandler{request: request}
}

func (h *PublicHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/magic-link", h.requestMagicLink)
	return r
}

type magicLinkRequestInput struct {
	Email string `json:"email"`
}

// requestMagicLink accepts {email} and always responds 202 Accepted on
// well-formed input, regardless of whether the email matches a customer.
// Any other response would give an attacker an enumeration oracle —
// repeatedly probing emails and timing/reading the body to infer which
// ones exist in our system.
//
// We return 400 only for structural failures (non-JSON body, missing
// email field). That's intentional: malformed requests are noise, not
// enumeration attempts, and a real client needs to know its payload was
// malformed. The critical property is that valid JSON with any email,
// matched or not, returns the same 202 with no body.
func (h *PublicHandler) requestMagicLink(w http.ResponseWriter, r *http.Request) {
	var in magicLinkRequestInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}
	if in.Email == "" {
		respond.BadRequest(w, r, "email is required")
		return
	}

	if err := h.request.RequestByEmail(r.Context(), in.Email); err != nil {
		// Real infra failure — surface as 500. The error text is
		// generic so the client still can't infer "we found your email
		// but failed to mint" vs. "we failed before lookup". All the
		// identifying detail lives in slog.
		slog.Error("magic-link request failed", "error", err)
		respond.InternalError(w, r)
		return
	}

	// 202: "accepted, no result body". Match or miss, same response.
	w.WriteHeader(http.StatusAccepted)
}
