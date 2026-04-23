package coupon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/middleware"
	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/audit"
	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

// couponETag formats a coupon's version as a strong ETag header value.
// RFC 7232: strong tags are quoted ASCII. We use the plain integer so
// clients can echo it back via If-Match without any parsing beyond
// strconv.
func couponETag(version int) string {
	return fmt.Sprintf("\"%d\"", version)
}

// setCouponETag writes the resource's ETag header. Called on every read
// and on successful writes so clients always see the current version.
func setCouponETag(w http.ResponseWriter, c domain.Coupon) {
	w.Header().Set("ETag", couponETag(c.Version))
}

// parseIfMatch extracts the integer version from a standards-shaped
// If-Match header value. Empty header → (nil, nil) meaning "no
// precondition". Any malformed value → non-nil error so the handler can
// respond with a clean 400 instead of the caller silently bypassing the
// concurrency check. Accepts weak form (W/"N") for leniency even though
// the server only emits strong tags; HTTP proxies occasionally downgrade.
func parseIfMatch(header string) (*int, error) {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil, nil
	}
	if header == "*" {
		// "*" means "any current version" — treat as no precondition.
		return nil, nil
	}
	header = strings.TrimPrefix(header, "W/")
	if len(header) < 2 || header[0] != '"' || header[len(header)-1] != '"' {
		return nil, fmt.Errorf("If-Match must be a quoted ETag")
	}
	v, err := strconv.Atoi(header[1 : len(header)-1])
	if err != nil {
		return nil, fmt.Errorf("If-Match value must be numeric")
	}
	return &v, nil
}

type Handler struct {
	svc         *Service
	auditLogger *audit.Logger
	events      domain.EventDispatcher
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// SetAuditLogger wires the audit writer. When nil (tests, or an assembly
// path that forgot to inject one) the middleware's catch-all still records
// basic create/update/archive rows — just with less metadata.
func (h *Handler) SetAuditLogger(l *audit.Logger) { h.auditLogger = l }

// SetEventDispatcher wires the outbound webhook dispatcher. When nil the
// handler still functions — events just aren't emitted, which is only the
// right behavior in narrow unit tests.
func (h *Handler) SetEventDispatcher(d domain.EventDispatcher) { h.events = d }

// fireCouponEvent dispatches a coupon lifecycle event with a canonical
// payload shape. Like the subscription equivalent, the call is synchronous:
// the outbox insert must persist-before-return so a crash between
// respond.JSON and event emission can't silently drop the event. A failed
// dispatch is logged rather than bubbled — audit + logs are enough for
// ops to notice, and failing the request after the mutation committed
// would be worse than a missed webhook.
func (h *Handler) fireCouponEvent(ctx context.Context, tenantID, eventType string, payload map[string]any) {
	if h.events == nil {
		return
	}
	if err := h.events.Dispatch(ctx, tenantID, eventType, payload); err != nil {
		slog.ErrorContext(ctx, "coupon event dispatch failed",
			"event_type", eventType, "tenant_id", tenantID, "error", err)
	}
}

// couponEventPayload is the canonical shape used for coupon.created and
// coupon.updated — all the fields a subscriber typically needs to sync their
// mirror without a follow-up GET.
func couponEventPayload(c domain.Coupon) map[string]any {
	return map[string]any{
		"coupon_id":        c.ID,
		"code":             c.Code,
		"name":             c.Name,
		"type":             c.Type,
		"amount_off":       c.AmountOff,
		"percent_off_bp":   c.PercentOffBP,
		"currency":         c.Currency,
		"duration":         c.Duration,
		"duration_periods": c.DurationPeriods,
		"max_redemptions":  c.MaxRedemptions,
		"times_redeemed":   c.TimesRedeemed,
		"expires_at":       c.ExpiresAt,
		"stackable":        c.Stackable,
		"customer_id":      c.CustomerID,
		"plan_ids":         c.PlanIDs,
		"archived_at":      c.ArchivedAt,
		"version":          c.Version,
	}
}

func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/", h.create)
	r.Get("/", h.list)
	r.Get("/{id}", h.get)
	r.Patch("/{id}", h.update)
	r.Post("/{id}/archive", h.archive)
	r.Post("/{id}/unarchive", h.unarchive)
	r.Post("/preview", h.preview)
	r.Post("/redeem", h.redeem)
	r.Get("/{id}/redemptions", h.listRedemptions)
	return r
}

// createWire is the on-the-wire shape. percent_off_bp carries percentages
// in basis points (5050 = 50.50%); the DB column has the same shape, so no
// float-to-int conversion happens anywhere in the stack.
type createWire struct {
	Code            string                    `json:"code"`
	Name            string                    `json:"name"`
	Type            domain.CouponType         `json:"type"`
	AmountOff       int64                     `json:"amount_off"`
	PercentOffBP    int                       `json:"percent_off_bp"`
	Currency        string                    `json:"currency"`
	MaxRedemptions  *int                      `json:"max_redemptions"`
	ExpiresAt       *time.Time                `json:"expires_at,omitempty"`
	PlanIDs         []string                  `json:"plan_ids,omitempty"`
	Duration        domain.CouponDuration     `json:"duration,omitempty"`
	DurationPeriods *int                      `json:"duration_periods,omitempty"`
	Stackable       bool                      `json:"stackable"`
	CustomerID      string                    `json:"customer_id,omitempty"`
	Restrictions    domain.CouponRestrictions `json:"restrictions"`
	Metadata        json.RawMessage           `json:"metadata,omitempty"`
}

func (w *createWire) toCreateInput() CreateInput {
	return CreateInput{
		Code:            w.Code,
		Name:            w.Name,
		Type:            w.Type,
		AmountOff:       w.AmountOff,
		PercentOffBP:    w.PercentOffBP,
		Currency:        w.Currency,
		MaxRedemptions:  w.MaxRedemptions,
		ExpiresAt:       w.ExpiresAt,
		PlanIDs:         w.PlanIDs,
		Duration:        w.Duration,
		DurationPeriods: w.DurationPeriods,
		Stackable:       w.Stackable,
		CustomerID:      w.CustomerID,
		Restrictions:    w.Restrictions,
		Metadata:        []byte(w.Metadata),
	}
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	var wire createWire
	if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	cpn, err := h.svc.Create(r.Context(), tenantID, wire.toCreateInput())
	if err != nil {
		respond.FromError(w, r, err, "coupon")
		return
	}

	if h.auditLogger != nil {
		_ = h.auditLogger.Log(r.Context(), tenantID, domain.AuditActionCreate, "coupon", cpn.ID, map[string]any{
			"resource_label":  cpn.Code,
			"type":            cpn.Type,
			"amount_off":      cpn.AmountOff,
			"percent_off_bp":  cpn.PercentOffBP,
			"currency":        cpn.Currency,
			"duration":        cpn.Duration,
			"max_redemptions": cpn.MaxRedemptions,
			"customer_id":     cpn.CustomerID,
		})
	}
	h.fireCouponEvent(r.Context(), tenantID, domain.EventCouponCreated, couponEventPayload(cpn))

	setCouponETag(w, cpn)
	respond.JSON(w, r, http.StatusCreated, cpn)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	// ?include_archived=true surfaces archived rows for the audit view.
	// Default is the live-set only — matches the common operator query.
	includeArchived := r.URL.Query().Get("include_archived") == "true"

	filter, err := buildListFilter(r, includeArchived)
	if err != nil {
		respond.BadRequest(w, r, err.Error())
		return
	}

	coupons, hasMore, err := h.svc.List(r.Context(), tenantID, filter)
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "list coupons", "error", err)
		return
	}
	if coupons == nil {
		coupons = []domain.Coupon{}
	}

	respond.JSON(w, r, http.StatusOK, newCouponPage(coupons, hasMore))
}

// buildListFilter parses pagination + filter query params into a
// ListFilter. Unknown/malformed values surface as 400 so the UI can
// reset cleanly rather than silently returning an unfiltered page.
// Supported filters:
//
//	?type=percentage|fixed_amount
//	?duration=once|repeating|forever
//	?expires_before=<RFC3339>
func buildListFilter(r *http.Request, includeArchived bool) (ListFilter, error) {
	p := middleware.ParsePageParams(r)
	filter := ListFilter{
		IncludeArchived: includeArchived,
		Limit:           p.Limit,
	}
	if p.Cursor != "" {
		cur, err := middleware.DecodeCursor(p.Cursor)
		if err != nil {
			return ListFilter{}, fmt.Errorf("invalid cursor")
		}
		filter.AfterID = cur.ID
		filter.AfterCreatedAt = cur.CreatedAt
	}

	q := r.URL.Query()
	if v := q.Get("type"); v != "" {
		t := domain.CouponType(v)
		if t != domain.CouponTypePercentage && t != domain.CouponTypeFixedAmount {
			return ListFilter{}, fmt.Errorf("invalid type: must be percentage or fixed_amount")
		}
		filter.Type = t
	}
	if v := q.Get("duration"); v != "" {
		d := domain.CouponDuration(v)
		if d != domain.CouponDurationOnce &&
			d != domain.CouponDurationRepeating &&
			d != domain.CouponDurationForever {
			return ListFilter{}, fmt.Errorf("invalid duration: must be once, repeating, or forever")
		}
		filter.Duration = d
	}
	if v := q.Get("expires_before"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return ListFilter{}, fmt.Errorf("invalid expires_before: must be RFC3339 timestamp")
		}
		filter.ExpiresBefore = t
	}
	return filter, nil
}

// newCouponPage renders a paginated coupon response in Stripe's shape.
// next_cursor is derived from the tail row only when hasMore is true so
// clients never chase a phantom page past the last row.
func newCouponPage(coupons []domain.Coupon, hasMore bool) middleware.PageResponse {
	resp := middleware.PageResponse{Data: coupons, HasMore: hasMore}
	if hasMore && len(coupons) > 0 {
		last := coupons[len(coupons)-1]
		resp.NextCursor = middleware.EncodeCursor(last.ID, last.CreatedAt)
	}
	return resp
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	cpn, err := h.svc.Get(r.Context(), tenantID, id)
	if err != nil {
		respond.FromError(w, r, err, "coupon")
		return
	}

	setCouponETag(w, cpn)
	respond.JSON(w, r, http.StatusOK, cpn)
}

// updateWire uses explicit-presence JSON decoding: a field set to null in
// the request body maps to "clear this field"; a field absent leaves it
// alone. json.RawMessage lets us distinguish the two at decode time.
type updateWire struct {
	Name           *string                    `json:"name,omitempty"`
	MaxRedemptions *int                       `json:"max_redemptions,omitempty"`
	ExpiresAt      json.RawMessage            `json:"expires_at,omitempty"`
	Restrictions   *domain.CouponRestrictions `json:"restrictions,omitempty"`
	Metadata       json.RawMessage            `json:"metadata,omitempty"`
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	ifMatch, err := parseIfMatch(r.Header.Get("If-Match"))
	if err != nil {
		respond.BadRequest(w, r, err.Error())
		return
	}

	var wire updateWire
	if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	in := UpdateInput{
		Name:           wire.Name,
		MaxRedemptions: wire.MaxRedemptions,
		Restrictions:   wire.Restrictions,
		Metadata:       []byte(wire.Metadata),
		IfMatch:        ifMatch,
	}
	if len(wire.ExpiresAt) > 0 {
		in.ExpiresAt = new(*time.Time)
		if string(wire.ExpiresAt) != "null" {
			var t time.Time
			if err := json.Unmarshal(wire.ExpiresAt, &t); err != nil {
				respond.BadRequest(w, r, "invalid expires_at")
				return
			}
			*in.ExpiresAt = &t
		}
	}

	cpn, err := h.svc.Update(r.Context(), tenantID, id, in)
	if err != nil {
		respond.FromError(w, r, err, "coupon")
		return
	}

	if h.auditLogger != nil {
		_ = h.auditLogger.Log(r.Context(), tenantID, domain.AuditActionUpdate, "coupon", cpn.ID, map[string]any{
			"resource_label":  cpn.Code,
			"changed_fields": changedFields(wire),
			"version":         cpn.Version,
		})
	}
	payload := couponEventPayload(cpn)
	payload["changed_fields"] = changedFields(wire)
	h.fireCouponEvent(r.Context(), tenantID, domain.EventCouponUpdated, payload)

	setCouponETag(w, cpn)
	respond.JSON(w, r, http.StatusOK, cpn)
}

func (h *Handler) archive(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	if err := h.svc.Archive(r.Context(), tenantID, id); err != nil {
		respond.FromError(w, r, err, "coupon")
		return
	}

	if h.auditLogger != nil {
		_ = h.auditLogger.Log(r.Context(), tenantID, domain.AuditActionArchive, "coupon", id, nil)
	}
	h.fireCouponEvent(r.Context(), tenantID, domain.EventCouponArchived, map[string]any{
		"coupon_id": id,
	})

	respond.JSON(w, r, http.StatusOK, map[string]string{"status": "archived"})
}

func (h *Handler) unarchive(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	if err := h.svc.Unarchive(r.Context(), tenantID, id); err != nil {
		respond.FromError(w, r, err, "coupon")
		return
	}

	if h.auditLogger != nil {
		_ = h.auditLogger.Log(r.Context(), tenantID, domain.AuditActionUnarchive, "coupon", id, nil)
	}
	h.fireCouponEvent(r.Context(), tenantID, domain.EventCouponUnarchived, map[string]any{
		"coupon_id": id,
	})

	respond.JSON(w, r, http.StatusOK, map[string]string{"status": "active"})
}

func (h *Handler) preview(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	var input RedeemInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	res, err := h.svc.Preview(r.Context(), tenantID, input)
	if err != nil {
		respond.FromError(w, r, err, "coupon")
		return
	}

	respond.JSON(w, r, http.StatusOK, res)
}

func (h *Handler) redeem(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	var input RedeemInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}
	// Idempotency-Key header overrides the body field so standard
	// clients (curl / the SDK) work without reshaping the body.
	if h := strings.TrimSpace(r.Header.Get("Idempotency-Key")); h != "" {
		input.IdempotencyKey = h
	}

	res, err := h.svc.RedeemDetail(r.Context(), tenantID, input)
	if err != nil {
		// Label by the stable domain code so operators can alert on a
		// spike in a specific failure mode (e.g. coupon_expired climbing
		// suggests someone just published a campaign with a stale code).
		// Empty code → "error" so we don't lose the signal entirely.
		outcome := errs.Code(err)
		if outcome == "" {
			outcome = "error"
		}
		middleware.RecordCouponRedemption(outcome)
		respond.FromError(w, r, err, "coupon")
		return
	}
	if !res.Replay {
		middleware.RecordCouponRedemption("success")
	} else {
		middleware.RecordCouponRedemption("replay")
	}

	// Replays are the same business event fired twice; audit it once at the
	// original redemption and not again on idempotent retries.
	if !res.Replay {
		if h.auditLogger != nil {
			_ = h.auditLogger.Log(r.Context(), tenantID, domain.AuditActionRedeem, "coupon", res.Redemption.CouponID, map[string]any{
				"redemption_id":   res.Redemption.ID,
				"customer_id":     res.Redemption.CustomerID,
				"subscription_id": res.Redemption.SubscriptionID,
				"invoice_id":      res.Redemption.InvoiceID,
				"discount_cents":  res.Redemption.DiscountCents,
				"periods_applied": res.Redemption.PeriodsApplied,
			})
		}
		h.fireCouponEvent(r.Context(), tenantID, domain.EventCouponRedeemed, map[string]any{
			"coupon_id":       res.Redemption.CouponID,
			"redemption_id":   res.Redemption.ID,
			"customer_id":     res.Redemption.CustomerID,
			"subscription_id": res.Redemption.SubscriptionID,
			"invoice_id":      res.Redemption.InvoiceID,
			"discount_cents":  res.Redemption.DiscountCents,
			"periods_applied": res.Redemption.PeriodsApplied,
		})
	}

	// Stripe convention: an idempotent replay returns 200 + the
	// Idempotent-Replay: true response header. Callers can then tell a
	// genuine retry-to-success apart from a true first-time create.
	if res.Replay {
		w.Header().Set("Idempotent-Replay", "true")
		respond.JSON(w, r, http.StatusOK, res.Redemption)
		return
	}
	respond.JSON(w, r, http.StatusCreated, res.Redemption)
}

// changedFields returns the list of updateWire fields the client actually
// sent, so the audit row captures the shape of the change instead of the
// pre/post state (which would duplicate the stored coupon row).
func changedFields(w updateWire) []string {
	fields := []string{}
	if w.Name != nil {
		fields = append(fields, "name")
	}
	if w.MaxRedemptions != nil {
		fields = append(fields, "max_redemptions")
	}
	if len(w.ExpiresAt) > 0 {
		fields = append(fields, "expires_at")
	}
	if w.Restrictions != nil {
		fields = append(fields, "restrictions")
	}
	if len(w.Metadata) > 0 {
		fields = append(fields, "metadata")
	}
	return fields
}

func (h *Handler) listRedemptions(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	couponID := chi.URLParam(r, "id")

	filter, err := buildListFilter(r, false)
	if err != nil {
		respond.BadRequest(w, r, err.Error())
		return
	}

	redemptions, hasMore, err := h.svc.ListRedemptions(r.Context(), tenantID, couponID, filter)
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "list coupon redemptions", "error", err)
		return
	}
	if redemptions == nil {
		redemptions = []domain.CouponRedemption{}
	}

	resp := middleware.PageResponse{Data: redemptions, HasMore: hasMore}
	if hasMore && len(redemptions) > 0 {
		last := redemptions[len(redemptions)-1]
		resp.NextCursor = middleware.EncodeCursor(last.ID, last.CreatedAt)
	}
	respond.JSON(w, r, http.StatusOK, resp)
}
