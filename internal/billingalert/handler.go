package billingalert

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

// Handler exposes the four /v1/billing/alerts endpoints. Permissions
// are wired at the router mount site (PermInvoiceRead for read,
// PermInvoiceWrite for write) — same level as invoice-related surfaces
// since billing alerts are an invoice-adjacent operator capability.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// Routes returns the sub-router. Mounted at /v1/billing/alerts. The
// archive route is a sibling of the {id} read route — chi tries
// patterns in registration order, so the more-specific pattern
// (/{id}/archive) comes first to avoid {id} capturing "archive".
//
// Permission gating is applied inline via per-method `with` middleware
// passed by the caller. The read methods (GET /, GET /{id}) gate on
// `read`; the write methods (POST /, POST /{id}/archive) gate on
// `write`. Splitting the gates here (rather than mounting two
// sub-routers) keeps the chi pattern-precedence concentrated in one
// place — moving them to the api router would risk the
// /{id}/archive vs /{id} ordering drifting.
func (h *Handler) Routes(read, write func(http.Handler) http.Handler) chi.Router {
	r := chi.NewRouter()
	r.With(write).Post("/{id}/archive", h.archive)
	r.With(read).Get("/{id}", h.get)
	r.With(read).Get("/", h.list)
	r.With(write).Post("/", h.create)
	return r
}

// wireAlert is the JSON shape the wire emits / accepts. Decoupled
// from domain.BillingAlert so the wire contract is pinned by struct
// tags here and the domain type stays free to evolve.
type wireAlert struct {
	ID              string             `json:"id"`
	Title           string             `json:"title"`
	CustomerID      string             `json:"customer_id"`
	Filter          wireAlertFilter    `json:"filter"`
	Threshold       wireAlertThreshold `json:"threshold"`
	Recurrence      string             `json:"recurrence"`
	Status          string             `json:"status"`
	LastTriggeredAt *string            `json:"last_triggered_at"`
	LastPeriodStart *string            `json:"last_period_start"`
	CreatedAt       string             `json:"created_at"`
	UpdatedAt       string             `json:"updated_at"`
}

// wireAlertFilter mirrors domain.BillingAlertFilter but uses the
// always-object idiom: dimensions is a JSON object (`{}`) even when
// the alert has no filter, so dashboard rendering doesn't need a null
// guard. meter_id is omitempty because a missing meter and an empty
// meter mean the same thing on the wire.
type wireAlertFilter struct {
	MeterID    string         `json:"meter_id,omitempty"`
	Dimensions map[string]any `json:"dimensions"`
}

// wireAlertThreshold always emits both keys (one as null) — same
// rationale as filter.dimensions: clients can read both fields
// without conditional indexing.
type wireAlertThreshold struct {
	AmountGTE *int64  `json:"amount_gte"`
	UsageGTE  *string `json:"usage_gte"`
}

// wireCreateRequest is the input shape for POST /v1/billing/alerts.
// `threshold` is parsed loosely — both fields are optional in the
// wire shape and the service-layer validation enforces the
// "exactly one" rule. `filter` is optional; missing → no meter, no
// dimensions.
type wireCreateRequest struct {
	Title      string              `json:"title"`
	CustomerID string              `json:"customer_id"`
	Filter     *wireCreateFilter   `json:"filter"`
	Threshold  wireCreateThreshold `json:"threshold"`
	Recurrence string              `json:"recurrence"`
}

type wireCreateFilter struct {
	MeterID    string         `json:"meter_id"`
	Dimensions map[string]any `json:"dimensions"`
}

// wireCreateThreshold accepts the decimal as a string (per ADR-005)
// or a JSON number (loose) — DecodeDecimal handles both.
type wireCreateThreshold struct {
	AmountGTE *int64           `json:"amount_gte"`
	UsageGTE  *json.RawMessage `json:"usage_gte"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	body, err := io.ReadAll(r.Body)
	if err != nil {
		slog.ErrorContext(r.Context(), "billing_alerts: read body", "error", err)
		respond.BadRequest(w, r, "could not read request body")
		return
	}

	req, err := decodeCreateRequest(body)
	if err != nil {
		respond.FromError(w, r, err, "billing_alert")
		return
	}

	alert, err := h.svc.Create(r.Context(), tenantID, req)
	if err != nil {
		// The customer / meter lookups inside Create return
		// errs.ErrNotFound for cross-tenant IDs. Surface as 404
		// with the message indicating which resource was missing.
		if errors.Is(err, errs.ErrNotFound) {
			respond.NotFound(w, r, "customer or meter")
			return
		}
		respond.FromError(w, r, err, "billing_alert")
		return
	}

	respond.JSON(w, r, http.StatusCreated, toWireAlert(alert))
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	alert, err := h.svc.Get(r.Context(), tenantID, id)
	if err != nil {
		respond.FromError(w, r, err, "billing_alert")
		return
	}
	respond.JSON(w, r, http.StatusOK, toWireAlert(alert))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	filter := ListFilter{TenantID: tenantID}
	q := r.URL.Query()
	filter.CustomerID = strings.TrimSpace(q.Get("customer_id"))
	filter.Status = domain.BillingAlertStatus(strings.TrimSpace(q.Get("status")))

	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			respond.ValidationField(w, r, "limit", "must be a non-negative integer")
			return
		}
		filter.Limit = n
	}
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			respond.ValidationField(w, r, "offset", "must be a non-negative integer")
			return
		}
		filter.Offset = n
	}

	alerts, total, err := h.svc.List(r.Context(), filter)
	if err != nil {
		respond.FromError(w, r, err, "billing_alert")
		return
	}

	wire := make([]wireAlert, 0, len(alerts))
	for _, a := range alerts {
		wire = append(wire, toWireAlert(a))
	}
	respond.List(w, r, wire, total)
}

func (h *Handler) archive(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	alert, err := h.svc.Archive(r.Context(), tenantID, id)
	if err != nil {
		respond.FromError(w, r, err, "billing_alert")
		return
	}
	respond.JSON(w, r, http.StatusOK, toWireAlert(alert))
}

// decodeCreateRequest parses the JSON body and translates wire-typed
// fields (decimal-as-string, etc.) to the service-layer types.
// Empty body is acceptable as a degenerate {} — the service-layer
// validation surfaces the missing required fields with field tags.
func decodeCreateRequest(body []byte) (CreateRequest, error) {
	wire := wireCreateRequest{}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &wire); err != nil {
			return CreateRequest{}, errs.Invalid("body", "request body is not valid JSON")
		}
	}

	out := CreateRequest{
		Title:          wire.Title,
		CustomerID:     wire.CustomerID,
		Recurrence:     domain.BillingAlertRecurrence(strings.TrimSpace(wire.Recurrence)),
		AmountCentsGTE: wire.Threshold.AmountGTE,
	}

	if wire.Filter != nil {
		out.MeterID = wire.Filter.MeterID
		out.Dimensions = wire.Filter.Dimensions
	}

	if wire.Threshold.UsageGTE != nil {
		// Accept both JSON number (12345.67) and JSON string ("12345.67").
		// shopspring/decimal's UnmarshalJSON handles both.
		var qty decimal.Decimal
		if err := json.Unmarshal(*wire.Threshold.UsageGTE, &qty); err != nil {
			return CreateRequest{}, errs.Invalid("threshold.usage_gte", "must be a decimal number or string")
		}
		out.QuantityGTE = &qty
	}

	return out, nil
}

// toWireAlert converts a domain alert to the JSON shape. Always-object
// idioms (`dimensions` as `{}`, both `threshold` keys present) are
// applied here so every read endpoint emits the same shape regardless
// of whether the underlying domain.BillingAlert was scanned from the
// DB or constructed in memory.
func toWireAlert(a domain.BillingAlert) wireAlert {
	dims := a.Filter.Dimensions
	if dims == nil {
		dims = map[string]any{}
	}

	thresh := wireAlertThreshold{
		AmountGTE: a.Threshold.AmountCentsGTE,
	}
	if a.Threshold.QuantityGTE != nil {
		s := a.Threshold.QuantityGTE.String()
		thresh.UsageGTE = &s
	}

	out := wireAlert{
		ID:         a.ID,
		Title:      a.Title,
		CustomerID: a.CustomerID,
		Filter: wireAlertFilter{
			MeterID:    a.Filter.MeterID,
			Dimensions: dims,
		},
		Threshold:  thresh,
		Recurrence: string(a.Recurrence),
		Status:     string(a.Status),
		CreatedAt:  a.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:  a.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
	if a.LastTriggeredAt != nil {
		s := a.LastTriggeredAt.UTC().Format("2006-01-02T15:04:05Z07:00")
		out.LastTriggeredAt = &s
	}
	if a.LastPeriodStart != nil {
		s := a.LastPeriodStart.UTC().Format("2006-01-02T15:04:05Z07:00")
		out.LastPeriodStart = &s
	}
	return out
}
