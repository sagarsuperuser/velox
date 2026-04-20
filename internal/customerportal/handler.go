package customerportal

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/auth"
)

// OperatorHandler exposes POST /v1/customer-portal-sessions — the operator
// endpoint where a tenant mints a portal session for one of their
// customers. Mounted under tenant auth + PermCustomerWrite. Not to be
// confused with the customer-side Middleware above.
type OperatorHandler struct {
	svc     *Service
	portalURL string
}

func NewOperatorHandler(svc *Service, portalURL string) *OperatorHandler {
	return &OperatorHandler{svc: svc, portalURL: portalURL}
}

func (h *OperatorHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/", h.create)
	return r
}

type createInput struct {
	CustomerID string `json:"customer_id"`
	TTLSeconds int    `json:"ttl_seconds,omitempty"`
}

type createResponse struct {
	ID         string    `json:"id"`
	CustomerID string    `json:"customer_id"`
	Token      string    `json:"token"`
	URL        string    `json:"url"`
	ExpiresAt  time.Time `json:"expires_at"`
	Livemode   bool      `json:"livemode"`
}

func (h *OperatorHandler) create(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	var in createInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	var ttl time.Duration
	if in.TTLSeconds > 0 {
		ttl = time.Duration(in.TTLSeconds) * time.Second
	}

	res, err := h.svc.Create(r.Context(), tenantID, in.CustomerID, ttl)
	if err != nil {
		respond.Validation(w, r, err.Error())
		return
	}

	url := h.portalURL
	if url != "" {
		if !strings.Contains(url, "?") {
			url += "?"
		} else {
			url += "&"
		}
		url += "token=" + res.RawToken
	}

	respond.JSON(w, r, http.StatusCreated, createResponse{
		ID:         res.Session.ID,
		CustomerID: res.Session.CustomerID,
		Token:      res.RawToken,
		URL:        url,
		ExpiresAt:  res.Session.ExpiresAt,
		Livemode:   res.Session.Livemode,
	})
}
