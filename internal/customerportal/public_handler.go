package customerportal

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/errs"
)

// PublicHandler serves the unauthenticated half of the customer portal:
// request-a-magic-link (POST /magic-link) and consume-a-magic-link
// (POST /magic/consume). Mounted under /v1/public/customer-portal with
// no API-key auth — the email + the minted token are the only
// credentials involved.
type PublicHandler struct {
	request *MagicLinkRequestService
	magic   *MagicLinkService
}

func NewPublicHandler(request *MagicLinkRequestService, magic *MagicLinkService) *PublicHandler {
	return &PublicHandler{request: request, magic: magic}
}

func (h *PublicHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/magic-link", h.requestMagicLink)
	r.Post("/magic/consume", h.consumeMagicLink)
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
		slog.ErrorContext(r.Context(), "magic-link request failed", "error", err)
		respond.InternalError(w, r)
		return
	}

	// 202: "accepted, no result body". Match or miss, same response.
	w.WriteHeader(http.StatusAccepted)
}

type consumeMagicLinkInput struct {
	Token string `json:"token"`
}

type consumeMagicLinkResponse struct {
	Token      string    `json:"token"`
	CustomerID string    `json:"customer_id"`
	Livemode   bool      `json:"livemode"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// consumeMagicLink exchanges a single-use magic-link token for a
// reusable portal session. POST rather than GET so the token stays out
// of server access logs and Referer headers — the frontend's /magic
// page reads ?token=... from its own URL, then POSTs the body here.
//
// Every failure (unknown / used / expired / malformed) surfaces as
// 401 with the same generic message. The session token returned on
// success is the caller's /v1/me/* bearer for ~1h; it is NOT the
// magic token (that row is now used_at-locked and will never redeem
// again).
func (h *PublicHandler) consumeMagicLink(w http.ResponseWriter, r *http.Request) {
	var in consumeMagicLinkInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		respond.Unauthorized(w, r, "invalid or expired magic link")
		return
	}

	res, err := h.magic.Consume(r.Context(), in.Token)
	if err != nil {
		if errors.Is(err, errs.ErrNotFound) {
			respond.Unauthorized(w, r, "invalid or expired magic link")
			return
		}
		slog.ErrorContext(r.Context(), "magic-link consume failed", "error", err)
		respond.InternalError(w, r)
		return
	}

	respond.JSON(w, r, http.StatusOK, consumeMagicLinkResponse{
		Token:      res.RawToken,
		CustomerID: res.Session.CustomerID,
		Livemode:   res.Session.Livemode,
		ExpiresAt:  res.Session.ExpiresAt,
	})
}
