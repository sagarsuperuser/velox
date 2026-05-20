package subscription

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/audit"
	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/clock"
)

// PlanReader reads plan data for proration calculations.
type PlanReader interface {
	GetPlan(ctx context.Context, tenantID, id string) (domain.Plan, error)
}

// ProrationInvoiceCreator creates finalized proration invoices and supports
// idempotent retry via a source-of-truth lookup.
//
// GetByProrationSource now takes the subscription_item_id alongside
// plan_changed_at — two items on the same subscription can be changed in the
// same wall-clock moment, and the pre-FEAT-5 key (subscription,
// plan_changed_at) would collide between them. The store's dedup index
// matches this tuple.
type ProrationInvoiceCreator interface {
	CreateInvoiceWithLineItems(ctx context.Context, tenantID string, inv domain.Invoice, items []domain.InvoiceLineItem) (domain.Invoice, error)
	GetByProrationSource(ctx context.Context, tenantID, subscriptionID, subscriptionItemID string, changeType domain.ItemChangeType, changeAt time.Time) (domain.Invoice, error)
	NextInvoiceNumber(ctx context.Context, tenantID string) (string, error)
	// FindBaseInvoiceForPeriod gates immediate in_advance proration on
	// whether the source invoice was actually paid. Mirrors the engine's
	// BillOnCancel paid-check; same industry rationale (Chargebee
	// Refundable vs Adjustment / Stripe proration_behavior=none).
	FindBaseInvoiceForPeriod(ctx context.Context, tenantID, subscriptionID string, periodStart time.Time) (domain.Invoice, error)
}

// ProrationCreditGranter grants credits for downgrade proration. Dedup key is
// (subscription, item, plan_changed_at) — see ProrationInvoiceCreator comment.
type ProrationCreditGranter interface {
	GrantProration(ctx context.Context, tenantID string, input ProrationGrantInput) error
	GetByProrationSource(ctx context.Context, tenantID, subscriptionID, subscriptionItemID string, changeType domain.ItemChangeType, changeAt time.Time) (domain.CreditLedgerEntry, error)
}

// ProrationCouponApplier computes a coupon discount against a proration
// invoice's subtotal. planIDs is the full set of item plan_ids on the
// subscription — the coupon's own plan gate is intersected against the full
// item set (any match ⇒ eligible), matching how Stripe treats coupons on
// multi-item subscriptions.
type ProrationCouponApplier interface {
	ApplyToInvoice(ctx context.Context, tenantID, subscriptionID, customerID, invoiceCurrency string, planIDs []string, subtotalCents int64) (domain.CouponDiscountResult, error)
	MarkPeriodsApplied(ctx context.Context, tenantID string, redemptionIDs []string) error
}

// ProrationTaxResult is what ApplyTaxToLineItems returns: invoice-level tax
// totals plus per-line mutations to the supplied line-item slice. Duplicates
// billing.TaxApplication so subscription package doesn't import billing.
type ProrationTaxResult struct {
	TaxAmountCents int64
	TaxRateBP      int64
	TaxName        string
	TaxCountry     string
	TaxID          string
	SubtotalCents  int64
	DiscountCents  int64
	// TaxStatus signals whether the provider's tax calculation
	// succeeded (ok) or was deferred (pending / failed). Drives the
	// proration invoice's finalized-vs-draft decision via
	// domain.InvoiceFinalizationStatus — consistent with the engine's
	// billOnePeriod + BillOnCreate gates. Pre-fix proration invoices
	// finalized regardless of tax status, lying about authoritative
	// amounts when calculation was deferred.
	TaxStatus domain.InvoiceTaxStatus
}

// ProrationTaxApplier resolves and applies tax against a proration invoice's
// single line item.
type ProrationTaxApplier interface {
	ApplyTaxToLineItems(ctx context.Context, tenantID, customerID, currency string, subtotal, discount int64, lineItems []domain.InvoiceLineItem) (ProrationTaxResult, error)
}

// ProrationGrantInput carries the downgrade/removal/reduction credit payload
// plus the provenance fields required for dedup. SourceChangeType
// distinguishes plan-downgrade from qty-reduction from item-remove when the
// same item is mutated multiple ways within the same billing period.
type ProrationGrantInput struct {
	CustomerID               string
	AmountCents              int64
	Description              string
	SourceSubscriptionID     string
	SourceSubscriptionItemID string
	SourcePlanChangedAt      time.Time
	SourceChangeType         domain.ItemChangeType
}

type Handler struct {
	svc         *Service
	plans       PlanReader
	invoices    ProrationInvoiceCreator
	credits     ProrationCreditGranter
	coupons     ProrationCouponApplier
	tax         ProrationTaxApplier
	events      domain.EventDispatcher
	auditLogger *audit.Logger
	// Resolver binds effective-now from the sub pin at handler entry
	// for proration math + changeAt stamping (PR-12, ADR-030 follow-
	// through). Without it, mid-cycle plan changes on clock-pinned
	// subs computed proration against wall-clock now — wrong factor
	// for the simulated state. Optional: nil-safe; without it the
	// proration paths fall back to wall-clock (pre-PR-12 behavior).
	resolver clock.Resolver
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// SetAuditLogger configures audit logging for financial operations.
func (h *Handler) SetAuditLogger(l *audit.Logger) { h.auditLogger = l }

// SetResolver wires the clock.Resolver used to bind effective-now at
// proration entry points so wall-clock-time computations on
// clock-pinned subs use simulated time. Implemented by *billing.Engine
// (same resolver the Service uses internally).
func (h *Handler) SetResolver(r clock.Resolver) { h.resolver = r }

// bindForSub returns ctx with effective-now bound from the sub pin
// when the resolver is wired. Used at proration handler entries so
// remainingPeriodFactor / changeAt stamps land in simulated time on
// clock-pinned subs. Falls through unchanged when resolver isn't set
// or the sub isn't clock-pinned (resolver returns wall-clock).
func (h *Handler) bindForSub(ctx context.Context, tenantID, subID string) context.Context {
	if h.resolver == nil {
		return ctx
	}
	bound, _ := clock.BindEffectiveNow(ctx, h.resolver, clock.Pin{TenantID: tenantID, SubscriptionID: subID})
	return bound
}

// auditCtxForSub returns ctx with effective-now bound to the entity's
// UpdatedAt timestamp when the sub is clock-pinned, so audit.Logger.Log
// stamps `created_at` in simulated time (ADR-030 — simulated time
// everywhere on clock-pinned entities). Wall-clock subs fall through
// unchanged — the audit row stamps wall-clock-now via clock.Now's
// fallback path. Service.Cancel / Activate / EndTrial / etc. all
// bound ctx internally and stamped sub.UpdatedAt in sim-time per PR-1;
// this helper just propagates that stamp into the audit row.
func auditCtxForSub(ctx context.Context, sub domain.Subscription) context.Context {
	if sub.TestClockID == "" {
		return ctx
	}
	return clock.WithEffectiveNow(ctx, sub.UpdatedAt)
}

// planIDsForAudit projects a sub's items into the audit metadata
// shape — same as the cancel handler's existing payload, so audit
// rows from create/cancel/etc carry a consistent plan-ids array.
func planIDsForAudit(sub domain.Subscription) []string {
	ids := make([]string, 0, len(sub.Items))
	for _, it := range sub.Items {
		ids = append(ids, it.PlanID)
	}
	return ids
}

// SetEventDispatcher wires the outbound webhook dispatcher. When nil the
// handler still functions — events just aren't emitted, which is only the
// right behavior in narrow unit tests.
func (h *Handler) SetEventDispatcher(d domain.EventDispatcher) { h.events = d }

// SetProrationDeps sets optional dependencies for proration invoice generation.
func (h *Handler) SetProrationDeps(plans PlanReader, invoices ProrationInvoiceCreator, credits ProrationCreditGranter) {
	h.plans = plans
	h.invoices = invoices
	h.credits = credits
}

// SetProrationCouponApplier configures coupon resolution on proration invoices.
func (h *Handler) SetProrationCouponApplier(c ProrationCouponApplier) {
	h.coupons = c
}

// SetProrationTaxApplier configures tax resolution on proration invoices.
func (h *Handler) SetProrationTaxApplier(a ProrationTaxApplier) {
	h.tax = a
}

// fireEvent dispatches a subscription lifecycle event. Synchronous by design:
// with the webhook_outbox in place (RES-1), Dispatch is a short DB insert that
// must persist-before-return so a crash between the handler's respond.JSON and
// event emission can't silently lose the event. Logging an error beats
// dropping.
func (h *Handler) fireEvent(ctx context.Context, tenantID, eventType string, sub domain.Subscription, extra map[string]any) {
	if h.events == nil {
		return
	}
	payload := map[string]any{
		"subscription_id": sub.ID,
		"customer_id":     sub.CustomerID,
		"status":          string(sub.Status),
		"item_count":      len(sub.Items),
	}
	if sub.CurrentBillingPeriodStart != nil {
		payload["current_period_start"] = sub.CurrentBillingPeriodStart.UTC()
	}
	if sub.CurrentBillingPeriodEnd != nil {
		payload["current_period_end"] = sub.CurrentBillingPeriodEnd.UTC()
	}
	for k, v := range extra {
		payload[k] = v
	}
	if err := h.events.Dispatch(ctx, tenantID, eventType, payload); err != nil {
		slog.ErrorContext(ctx, "dispatch subscription event",
			"event_type", eventType,
			"subscription_id", sub.ID,
			"tenant_id", tenantID,
			"error", err,
		)
	}
}

// itemPayload projects a SubscriptionItem into an event payload. Stable keys
// — consumers depend on the shape, so we don't echo the full domain struct.
func itemPayload(item domain.SubscriptionItem) map[string]any {
	p := map[string]any{
		"item_id":  item.ID,
		"plan_id":  item.PlanID,
		"quantity": item.Quantity,
	}
	if item.PendingPlanID != "" {
		p["pending_plan_id"] = item.PendingPlanID
	}
	if item.PendingPlanEffectiveAt != nil {
		p["pending_plan_effective_at"] = item.PendingPlanEffectiveAt.UTC()
	}
	return p
}

func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/", h.create)
	r.Get("/", h.list)
	r.Get("/{id}", h.get)
	r.Post("/{id}/activate", h.activate)
	r.Post("/{id}/cancel", h.cancel)
	r.Post("/{id}/schedule-cancel", h.scheduleCancel)
	r.Delete("/{id}/scheduled-cancel", h.clearScheduledCancel)
	r.Put("/{id}/pause-collection", h.pauseCollection)
	r.Delete("/{id}/pause-collection", h.resumeCollection)
	r.Post("/{id}/end-trial", h.endTrial)
	r.Post("/{id}/extend-trial", h.extendTrial)

	// Billing thresholds — Stripe-parity hard-cap config. PUT writes the full
	// (amount, reset, items) triple; DELETE clears it. Idempotent.
	r.Put("/{id}/billing-thresholds", h.setBillingThresholds)
	r.Delete("/{id}/billing-thresholds", h.clearBillingThresholds)

	// Items — Stripe-style per-item mutation. Quantity and plan changes land
	// on the same PATCH (body discriminates), pending-change clear has its own
	// DELETE so client code can target it without a PATCH body shape.
	r.Post("/{id}/items", h.addItem)
	r.Patch("/{id}/items/{itemID}", h.updateItem)
	r.Delete("/{id}/items/{itemID}/pending-change", h.cancelPendingItemChange)
	r.Delete("/{id}/items/{itemID}", h.removeItem)
	r.Get("/{id}/timeline", h.activityTimeline)
	return r
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	var input CreateInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	sub, err := h.svc.Create(r.Context(), tenantID, input)
	if err != nil {
		respond.FromError(w, r, err, "subscription")
		return
	}

	// Explicit audit row so the timestamp comes through auditCtxForSub
	// (sim-time on clock-pinned subs, ADR-030). Without this the audit
	// middleware's catch-all path fires with wall-clock created_at —
	// the mixed-domain timestamp shows up on the embedded activity
	// timeline next to the sim-time "Created" header on the same page.
	if h.auditLogger != nil {
		_ = h.auditLogger.Log(auditCtxForSub(r.Context(), sub), tenantID, domain.AuditActionCreate, "subscription", sub.ID, map[string]any{
			"customer_id": sub.CustomerID,
			"plan_ids":    planIDsForAudit(sub),
		})
	}

	h.fireEvent(r.Context(), tenantID, domain.EventSubscriptionCreated, sub, nil)

	respond.JSON(w, r, http.StatusCreated, sub)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	subs, total, err := h.svc.List(r.Context(), ListFilter{
		TenantID:   tenantID,
		CustomerID: r.URL.Query().Get("customer_id"),
		PlanID:     r.URL.Query().Get("plan_id"),
		Status:     r.URL.Query().Get("status"),
		Limit:      limit,
		Offset:     offset,
		Sort:       r.URL.Query().Get("sort"),
		SortDir:    r.URL.Query().Get("dir"),
	})
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "list subscriptions", "error", err)
		return
	}
	if subs == nil {
		subs = []domain.Subscription{}
	}

	respond.List(w, r, subs, total)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	sub, err := h.svc.Get(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "subscription")
		return
	}
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "get subscription", "error", err)
		return
	}

	respond.JSON(w, r, http.StatusOK, sub)
}

func (h *Handler) activate(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	sub, err := h.svc.Activate(r.Context(), tenantID, id)
	if err != nil {
		respond.FromError(w, r, err, "subscription")
		return
	}

	// Explicit audit so the row's created_at is sim-time on clock-pinned
	// subs (auditCtxForSub binds from sub.UpdatedAt) — same rationale as
	// the create handler above.
	if h.auditLogger != nil {
		_ = h.auditLogger.Log(auditCtxForSub(r.Context(), sub), tenantID, domain.AuditActionActivate, "subscription", sub.ID, map[string]any{
			"customer_id": sub.CustomerID,
		})
	}

	h.fireEvent(r.Context(), tenantID, domain.EventSubscriptionActivated, sub, nil)

	respond.JSON(w, r, http.StatusOK, sub)
}

func (h *Handler) cancel(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	sub, err := h.svc.Cancel(r.Context(), tenantID, id)
	if err != nil {
		respond.FromError(w, r, err, "subscription")
		return
	}

	if h.auditLogger != nil {
		planIDs := planIDsFromItems(sub.Items)
		_ = h.auditLogger.Log(auditCtxForSub(r.Context(), sub), tenantID, domain.AuditActionCancel, "subscription", sub.ID, map[string]any{
			"customer_id": sub.CustomerID,
			"plan_ids":    planIDs,
		})
	}

	h.fireEvent(r.Context(), tenantID, domain.EventSubscriptionCanceled, sub, nil)

	respond.JSON(w, r, http.StatusOK, sub)
}

// scheduleCancel records a soft-cancel intent. Body must set exactly one of
// {at_period_end:true, cancel_at:<RFC3339>}. The current period is unaffected;
// the billing engine flips the sub to canceled when the boundary fires.
// Re-calling this endpoint replaces any prior schedule (so a caller can
// switch from at_period_end to a specific date by issuing a new request).
func (h *Handler) scheduleCancel(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	var input ScheduleCancelInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	sub, err := h.svc.ScheduleCancel(r.Context(), tenantID, id, input)
	if err != nil {
		respond.FromError(w, r, err, "subscription")
		return
	}

	if h.auditLogger != nil {
		meta := map[string]any{
			"action":               "cancel_scheduled",
			"customer_id":          sub.CustomerID,
			"cancel_at_period_end": sub.CancelAtPeriodEnd,
		}
		if sub.CancelAt != nil {
			meta["cancel_at"] = sub.CancelAt.UTC()
		}
		_ = h.auditLogger.Log(auditCtxForSub(r.Context(), sub), tenantID, domain.AuditActionUpdate, "subscription", sub.ID, meta)
	}

	extra := map[string]any{"cancel_at_period_end": sub.CancelAtPeriodEnd}
	if sub.CancelAt != nil {
		extra["cancel_at"] = sub.CancelAt.UTC()
	}
	h.fireEvent(r.Context(), tenantID, domain.EventSubscriptionCancelScheduled, sub, extra)

	respond.JSON(w, r, http.StatusOK, sub)
}

// pauseCollection sets the Stripe-parity collection-pause state. Distinct
// from POST /pause (which hard-pauses the subscription via status). Body
// must include behavior; v1 only accepts "keep_as_draft". resumes_at is
// optional — when set, the cycle scan auto-clears the pause at the start
// of that period.
func (h *Handler) pauseCollection(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	var input PauseCollectionInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	sub, err := h.svc.PauseCollection(r.Context(), tenantID, id, input)
	if err != nil {
		respond.FromError(w, r, err, "subscription")
		return
	}

	if h.auditLogger != nil {
		meta := map[string]any{
			"action":      "collection_paused",
			"customer_id": sub.CustomerID,
			"behavior":    string(input.Behavior),
		}
		if sub.PauseCollection != nil && sub.PauseCollection.ResumesAt != nil {
			meta["resumes_at"] = sub.PauseCollection.ResumesAt.UTC()
		}
		_ = h.auditLogger.Log(auditCtxForSub(r.Context(), sub), tenantID, domain.AuditActionUpdate, "subscription", sub.ID, meta)
	}

	extra := map[string]any{"behavior": string(input.Behavior)}
	if sub.PauseCollection != nil && sub.PauseCollection.ResumesAt != nil {
		extra["resumes_at"] = sub.PauseCollection.ResumesAt.UTC()
	}
	h.fireEvent(r.Context(), tenantID, domain.EventSubscriptionCollectionPaused, sub, extra)

	respond.JSON(w, r, http.StatusOK, sub)
}

// endTrial flips a 'trialing' subscription to 'active' immediately,
// regardless of trial_end_at. Operator-driven counterpart to the cycle
// scan's auto-flip. Fires subscription.trial_ended with
// triggered_by="operator" so analytics can distinguish from the
// scheduled transition. Returns 422 if the row is not in 'trialing'.
func (h *Handler) endTrial(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	sub, err := h.svc.EndTrial(r.Context(), tenantID, id)
	if err != nil {
		respond.FromError(w, r, err, "subscription")
		return
	}

	if h.auditLogger != nil {
		_ = h.auditLogger.Log(auditCtxForSub(r.Context(), sub), tenantID, domain.AuditActionUpdate, "subscription", sub.ID, map[string]any{
			"action":      "trial_ended",
			"customer_id": sub.CustomerID,
		})
	}

	h.fireEvent(r.Context(), tenantID, domain.EventSubscriptionTrialEnded, sub, map[string]any{
		"triggered_by": "operator",
	})

	respond.JSON(w, r, http.StatusOK, sub)
}

// extendTrial pushes a trialing subscription's trial_end_at later. Body:
// {trial_end:<RFC3339>}. Returns 422 if the new value is in the past or
// not strictly after the current trial_end_at, or the sub is not in
// 'trialing'. Fires subscription.trial_extended with
// triggered_by="operator".
func (h *Handler) extendTrial(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	var body struct {
		TrialEnd time.Time `json:"trial_end"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, r, http.StatusBadRequest, "invalid_body", "invalid request body", "")
		return
	}
	if body.TrialEnd.IsZero() {
		respond.Error(w, r, http.StatusUnprocessableEntity, "validation_error", "trial_end is required", "trial_end")
		return
	}

	sub, err := h.svc.ExtendTrial(r.Context(), tenantID, id, body.TrialEnd)
	if err != nil {
		respond.FromError(w, r, err, "subscription")
		return
	}

	if h.auditLogger != nil {
		_ = h.auditLogger.Log(auditCtxForSub(r.Context(), sub), tenantID, domain.AuditActionUpdate, "subscription", sub.ID, map[string]any{
			"action":      "trial_extended",
			"customer_id": sub.CustomerID,
			"trial_end":   body.TrialEnd.UTC(),
		})
	}

	h.fireEvent(r.Context(), tenantID, domain.EventSubscriptionTrialExtended, sub, map[string]any{
		"triggered_by": "operator",
		"trial_end":    body.TrialEnd.UTC(),
	})

	respond.JSON(w, r, http.StatusOK, sub)
}

// resumeCollection clears the collection-pause state. Idempotent —
// clearing a row that has no active pause returns 200 with the unchanged
// subscription. Returns 404 only when the subscription itself doesn't
// exist.
func (h *Handler) resumeCollection(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	sub, err := h.svc.ResumeCollection(r.Context(), tenantID, id)
	if err != nil {
		respond.FromError(w, r, err, "subscription")
		return
	}

	if h.auditLogger != nil {
		_ = h.auditLogger.Log(auditCtxForSub(r.Context(), sub), tenantID, domain.AuditActionUpdate, "subscription", sub.ID, map[string]any{
			"action":      "collection_resumed",
			"customer_id": sub.CustomerID,
		})
	}

	h.fireEvent(r.Context(), tenantID, domain.EventSubscriptionCollectionResumed, sub, nil)

	respond.JSON(w, r, http.StatusOK, sub)
}

// setBillingThresholds writes the Stripe-parity hard-cap config onto a
// subscription. Body shape:
//
//	{
//	  "amount_gte": 50000,                    // optional, integer cents
//	  "reset_billing_cycle": true,            // optional, defaults true
//	  "item_thresholds": [                    // optional, always-array
//	    {"subscription_item_id": "si_xxx", "usage_gte": "1000"}
//	  ]
//	}
//
// At least one of amount_gte or item_thresholds must be supplied. Returns
// 422 on validation failure (terminal sub, unknown item id, negative
// usage_gte, multi-currency item set, etc).
//
// Replaces the full set on every call: the per-item rows for any item not
// in the new slice are deleted by the store.
func (h *Handler) setBillingThresholds(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	var input BillingThresholdsInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	// Multi-currency check happens here because plan currency lookups need
	// h.plans, which the service doesn't have. We hydrate the sub's items,
	// fetch each item's plan, and reject when the set spans more than one
	// currency. A threshold expressed in cents only makes sense against a
	// single-currency line set.
	if h.plans != nil {
		sub, gerr := h.svc.Get(r.Context(), tenantID, id)
		if gerr == nil && len(sub.Items) > 0 {
			seen := make(map[string]struct{}, 2)
			for _, it := range sub.Items {
				p, perr := h.plans.GetPlan(r.Context(), tenantID, it.PlanID)
				if perr != nil {
					continue
				}
				if p.Currency != "" {
					seen[p.Currency] = struct{}{}
				}
			}
			if len(seen) > 1 {
				respond.Error(w, r, http.StatusUnprocessableEntity, "validation_error",
					"billing thresholds are not supported on multi-currency subscriptions",
					"billing_thresholds")
				return
			}
		}
	}

	sub, err := h.svc.SetBillingThresholds(r.Context(), tenantID, id, input)
	if err != nil {
		respond.FromError(w, r, err, "subscription")
		return
	}

	if h.auditLogger != nil {
		meta := map[string]any{
			"action":               "billing_thresholds_set",
			"customer_id":          sub.CustomerID,
			"amount_gte":           input.AmountGTE,
			"item_threshold_count": len(input.ItemThresholds),
		}
		_ = h.auditLogger.Log(auditCtxForSub(r.Context(), sub), tenantID, domain.AuditActionUpdate, "subscription", sub.ID, meta)
	}

	respond.JSON(w, r, http.StatusOK, sub)
}

// clearBillingThresholds removes any threshold configuration on a
// subscription. Idempotent — clearing on a sub that has no threshold returns
// 200 with the unchanged subscription. Returns 404 only when the
// subscription itself doesn't exist.
func (h *Handler) clearBillingThresholds(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	sub, err := h.svc.ClearBillingThresholds(r.Context(), tenantID, id)
	if err != nil {
		respond.FromError(w, r, err, "subscription")
		return
	}

	if h.auditLogger != nil {
		_ = h.auditLogger.Log(auditCtxForSub(r.Context(), sub), tenantID, domain.AuditActionUpdate, "subscription", sub.ID, map[string]any{
			"action":      "billing_thresholds_cleared",
			"customer_id": sub.CustomerID,
		})
	}

	respond.JSON(w, r, http.StatusOK, sub)
}

// clearScheduledCancel removes any prior schedule. Idempotent — clearing a
// row that has no pending cancel returns 200 with the unchanged subscription.
// Returns 404 only when the subscription itself doesn't exist.
func (h *Handler) clearScheduledCancel(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	sub, err := h.svc.ClearScheduledCancel(r.Context(), tenantID, id)
	if err != nil {
		respond.FromError(w, r, err, "subscription")
		return
	}

	if h.auditLogger != nil {
		_ = h.auditLogger.Log(auditCtxForSub(r.Context(), sub), tenantID, domain.AuditActionUpdate, "subscription", sub.ID, map[string]any{
			"action":      "cancel_cleared",
			"customer_id": sub.CustomerID,
		})
	}

	h.fireEvent(r.Context(), tenantID, domain.EventSubscriptionCancelCleared, sub, nil)

	respond.JSON(w, r, http.StatusOK, sub)
}

// addItem appends a new priced line to a subscription. When the parent
// subscription is mid-period, the new item drives a proration invoice so the
// customer is charged for the partial-period cost of the addition rather than
// getting it free until next cycle close.
func (h *Handler) addItem(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	var input AddItemInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	// Bind effective-now from the sub pin so proration math runs in
	// simulated time on clock-pinned subs (ADR-030). Without binding,
	// `remainingPeriodFactor(sub, time.Now())` would compute the
	// wrong factor for a clock-pinned sub whose current_period is in
	// the simulated future. PR-12.
	ctx := h.bindForSub(r.Context(), tenantID, id)
	r = r.WithContext(ctx)

	// Snapshot the pre-add subscription for proration. Factor is computed
	// from the period boundaries which don't change when an item is added
	// mid-cycle, so a pre-add read is equivalent to a post-add read here.
	var subBefore domain.Subscription
	var prorationFactor float64
	if h.plans != nil && h.invoices != nil {
		sub, serr := h.svc.Get(ctx, tenantID, id)
		if serr == nil {
			subBefore = sub
			if sub.Status == domain.SubscriptionActive {
				prorationFactor = remainingPeriodFactor(sub, clock.Now(ctx))
			}
		}
	}

	item, err := h.svc.AddItem(ctx, tenantID, id, input)
	if err != nil {
		respond.FromError(w, r, err, "subscription")
		return
	}

	if h.auditLogger != nil {
		_ = h.auditLogger.Log(r.Context(), tenantID, domain.AuditActionUpdate, "subscription", id, map[string]any{
			"action":   "item_added",
			"item_id":  item.ID,
			"plan_id":  item.PlanID,
			"quantity": item.Quantity,
		})
	}

	// Re-fetch the subscription so downstream payload/proration paths see the
	// full post-add Items slice. Silent fallback: if the read fails we use a
	// minimal struct for events — item.added is still useful without the
	// enclosing snapshot.
	var subAfter domain.Subscription
	if h.events != nil || (prorationFactor > 0) {
		s, getErr := h.svc.Get(r.Context(), tenantID, id)
		if getErr != nil {
			subAfter = subBefore
			if subAfter.ID == "" {
				subAfter = domain.Subscription{ID: id}
			}
		} else {
			subAfter = s
		}
	}

	if prorationFactor > 0 && h.invoices != nil {
		changeAt := item.CreatedAt
		if changeAt.IsZero() {
			changeAt = clock.Now(ctx)
		}
		spec := itemProrationSpec{
			changeType:      domain.ItemChangeTypeAdd,
			changeAt:        changeAt,
			prorationFactor: prorationFactor,
			itemID:          item.ID,
			oldPlanID:       "",
			oldQuantity:     0,
			newPlanID:       item.PlanID,
			newQuantity:     item.Quantity,
		}
		prorationResult, prorationErr := h.handleItemProration(r.Context(), tenantID, subAfter, spec)
		if prorationErr != nil {
			slog.ErrorContext(r.Context(), "item proration failed after item add committed",
				"subscription_id", id,
				"item_id", item.ID,
				"tenant_id", tenantID,
				"plan_id", item.PlanID,
				"quantity", item.Quantity,
				"proration_factor", prorationFactor,
				"error", prorationErr,
			)
			if h.auditLogger != nil {
				_ = h.auditLogger.Log(r.Context(), tenantID, "subscription.proration_failed", "subscription", id, map[string]any{
					"item_id":          item.ID,
					"change_type":      string(domain.ItemChangeTypeAdd),
					"plan_id":          item.PlanID,
					"quantity":         item.Quantity,
					"proration_factor": prorationFactor,
					"error":            prorationErr.Error(),
				})
			}
			respond.Error(w, r, http.StatusInternalServerError, "api_error", "proration_failed",
				"item add succeeded but proration generation failed — item is on the subscription; retry or contact support to reconcile")
			return
		}
		_ = prorationResult
	}

	if h.events != nil {
		h.fireEvent(r.Context(), tenantID, domain.EventSubscriptionItemAdded, subAfter, map[string]any{
			"item": itemPayload(item),
		})
	}

	respond.JSON(w, r, http.StatusCreated, item)
}

// updateItem applies a quantity change or plan change (immediate/scheduled)
// to a single item. Quantity-only edits return the updated item directly;
// immediate plan changes also drive proration (new invoice or credit) keyed
// on the item.
func (h *Handler) updateItem(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	subID := chi.URLParam(r, "id")
	itemID := chi.URLParam(r, "itemID")

	var input UpdateItemInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	// Bind effective-now from the sub pin so proration math + changeAt
	// stamps run in simulated time on clock-pinned subs (PR-12).
	ctx := h.bindForSub(r.Context(), tenantID, subID)
	r = r.WithContext(ctx)

	// Capture the pre-change item/plan only when we're about to drive proration
	// — the old plan id, old quantity, and the subscription's remaining period
	// all come from a snapshot taken before UpdateItem mutates the row.
	var oldPlanID string
	var oldQuantity int64
	var prorationFactor float64
	var subBefore domain.Subscription
	isImmediatePlanChange := input.NewPlanID != "" && input.Immediate
	isQuantityChange := input.Quantity != nil
	prorationEligible := (isImmediatePlanChange || isQuantityChange) && h.plans != nil
	if prorationEligible {
		item, gerr := h.svc.store.GetItem(ctx, tenantID, itemID)
		if gerr == nil && item.SubscriptionID == subID {
			oldPlanID = item.PlanID
			oldQuantity = item.Quantity
		}
		sub, serr := h.svc.Get(ctx, tenantID, subID)
		if serr == nil {
			subBefore = sub
			prorationFactor = remainingPeriodFactor(sub, clock.Now(ctx))
		}
	}

	result, err := h.svc.UpdateItem(ctx, tenantID, subID, itemID, input)
	if err != nil {
		respond.FromError(w, r, err, "subscription item")
		return
	}

	if h.auditLogger != nil {
		payload := map[string]any{
			"item_id":   result.Item.ID,
			"immediate": input.Immediate,
		}
		if input.Quantity != nil {
			payload["action"] = "item_quantity_changed"
			payload["quantity"] = *input.Quantity
		} else {
			payload["action"] = "item_plan_changed"
			payload["old_plan_id"] = oldPlanID
			payload["new_plan_id"] = input.NewPlanID
		}
		_ = h.auditLogger.Log(ctx, tenantID, "subscription.item_updated", "subscription", subID, payload)
	}

	if prorationEligible && prorationFactor > 0 && h.invoices != nil {
		// Re-hydrate the subscription post-change so the Items slice reflects
		// the swapped plan/quantity — handleProration walks it to resolve
		// coupon plan eligibility. Fall back to subBefore on error so the
		// handler still responds, but use the fresh Items when available.
		subAfter, getErr := h.svc.Get(ctx, tenantID, subID)
		if getErr != nil {
			subAfter = subBefore
		}
		var spec itemProrationSpec
		if isImmediatePlanChange {
			var changeAt time.Time
			if result.Item.PlanChangedAt != nil {
				changeAt = *result.Item.PlanChangedAt
			} else {
				changeAt = clock.Now(ctx)
			}
			spec = itemProrationSpec{
				changeType:      domain.ItemChangeTypePlan,
				changeAt:        changeAt,
				prorationFactor: prorationFactor,
				itemID:          result.Item.ID,
				oldPlanID:       oldPlanID,
				oldQuantity:     result.Item.Quantity,
				newPlanID:       result.Item.PlanID,
				newQuantity:     result.Item.Quantity,
			}
		} else {
			// Quantity-only change. Plan is unchanged; store doesn't stamp a
			// dedicated timestamp so we use UpdatedAt (the store bumps it on
			// every item write) — stable across retries of the same in-flight
			// UpdateItemQuantity call.
			changeAt := result.Item.UpdatedAt
			if changeAt.IsZero() {
				changeAt = clock.Now(ctx)
			}
			spec = itemProrationSpec{
				changeType:      domain.ItemChangeTypeQuantity,
				changeAt:        changeAt,
				prorationFactor: prorationFactor,
				itemID:          result.Item.ID,
				oldPlanID:       oldPlanID,
				oldQuantity:     oldQuantity,
				newPlanID:       result.Item.PlanID,
				newQuantity:     result.Item.Quantity,
			}
		}
		prorationResult, prorationErr := h.handleItemProration(r.Context(), tenantID, subAfter, spec)
		if prorationErr != nil {
			slog.ErrorContext(r.Context(), "item proration failed after item change committed",
				"subscription_id", subID,
				"item_id", result.Item.ID,
				"tenant_id", tenantID,
				"change_type", spec.changeType,
				"old_plan_id", oldPlanID,
				"new_plan_id", input.NewPlanID,
				"old_quantity", oldQuantity,
				"new_quantity", spec.newQuantity,
				"proration_factor", prorationFactor,
				"error", prorationErr,
			)
			if h.auditLogger != nil {
				_ = h.auditLogger.Log(r.Context(), tenantID, "subscription.proration_failed", "subscription", subID, map[string]any{
					"item_id":          result.Item.ID,
					"change_type":      string(spec.changeType),
					"old_plan_id":      oldPlanID,
					"new_plan_id":      input.NewPlanID,
					"old_quantity":     oldQuantity,
					"new_quantity":     spec.newQuantity,
					"proration_factor": prorationFactor,
					"error":            prorationErr.Error(),
				})
			}
			respond.Error(w, r, http.StatusInternalServerError, "api_error", "proration_failed",
				"item change succeeded but proration generation failed — item is on the new state; retry or contact support to reconcile")
			return
		}
		if prorationResult != nil {
			result.Proration = prorationResult
		}
	}

	// Event dispatch. Quantity changes and immediate plan changes are
	// observable-now → subscription.item.updated. A scheduled plan change
	// (Immediate=false, NewPlanID set) is an intent, not a mutation of the
	// current cycle → subscription.pending_change.scheduled; the applied
	// event fires at the cycle boundary from billing.Engine.
	if h.events != nil {
		sub, getErr := h.svc.Get(r.Context(), tenantID, subID)
		if getErr != nil {
			sub = domain.Subscription{ID: subID}
		}
		extra := map[string]any{"item": itemPayload(result.Item)}
		eventType := domain.EventSubscriptionItemUpdated
		if input.NewPlanID != "" && !input.Immediate {
			eventType = domain.EventSubscriptionPendingChangeScheduled
			extra["new_plan_id"] = input.NewPlanID
			if result.Item.PendingPlanEffectiveAt != nil {
				extra["effective_at"] = result.Item.PendingPlanEffectiveAt.UTC()
			}
		}
		h.fireEvent(r.Context(), tenantID, eventType, sub, extra)
	}

	respond.JSON(w, r, http.StatusOK, result)
}

func (h *Handler) cancelPendingItemChange(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	subID := chi.URLParam(r, "id")
	itemID := chi.URLParam(r, "itemID")

	item, err := h.svc.CancelPendingItemChange(r.Context(), tenantID, subID, itemID)
	if err != nil {
		respond.FromError(w, r, err, "subscription item")
		return
	}

	if h.auditLogger != nil {
		_ = h.auditLogger.Log(r.Context(), tenantID, domain.AuditActionUpdate, "subscription", subID, map[string]any{
			"action":  "cancel_pending_item_plan_change",
			"item_id": item.ID,
		})
	}

	if h.events != nil {
		sub, getErr := h.svc.Get(r.Context(), tenantID, subID)
		if getErr != nil {
			sub = domain.Subscription{ID: subID}
		}
		h.fireEvent(r.Context(), tenantID, domain.EventSubscriptionPendingChangeCanceled, sub, map[string]any{
			"item": itemPayload(item),
		})
	}

	respond.JSON(w, r, http.StatusOK, item)
}

func (h *Handler) removeItem(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	subID := chi.URLParam(r, "id")
	itemID := chi.URLParam(r, "itemID")

	// Bind effective-now from the sub pin (PR-12).
	ctx := h.bindForSub(r.Context(), tenantID, subID)
	r = r.WithContext(ctx)

	// Capture the pre-delete item + sub for proration. Removing mid-period
	// should credit back the unused portion of what the customer already paid
	// for this item. RemoveItem is a hard delete so the snapshot must be
	// taken before the call.
	var removedPlanID string
	var removedQuantity int64
	var subBefore domain.Subscription
	var prorationFactor float64
	if h.plans != nil && h.credits != nil {
		item, gerr := h.svc.store.GetItem(ctx, tenantID, itemID)
		if gerr == nil && item.SubscriptionID == subID {
			removedPlanID = item.PlanID
			removedQuantity = item.Quantity
		}
		sub, serr := h.svc.Get(ctx, tenantID, subID)
		if serr == nil {
			subBefore = sub
			if sub.Status == domain.SubscriptionActive {
				prorationFactor = remainingPeriodFactor(sub, clock.Now(ctx))
			}
		}
	}

	if err := h.svc.RemoveItem(ctx, tenantID, subID, itemID); err != nil {
		respond.FromError(w, r, err, "subscription item")
		return
	}

	if h.auditLogger != nil {
		_ = h.auditLogger.Log(ctx, tenantID, domain.AuditActionUpdate, "subscription", subID, map[string]any{
			"action":  "item_removed",
			"item_id": itemID,
		})
	}

	if prorationFactor > 0 && removedPlanID != "" {
		// Re-fetch for coupon plan eligibility over the remaining items.
		subAfter, getErr := h.svc.Get(ctx, tenantID, subID)
		if getErr != nil {
			subAfter = subBefore
		}
		spec := itemProrationSpec{
			changeType:      domain.ItemChangeTypeRemove,
			changeAt:        clock.Now(ctx),
			prorationFactor: prorationFactor,
			itemID:          itemID,
			oldPlanID:       removedPlanID,
			oldQuantity:     removedQuantity,
			newPlanID:       "",
			newQuantity:     0,
		}
		prorationResult, prorationErr := h.handleItemProration(ctx, tenantID, subAfter, spec)
		if prorationErr != nil {
			slog.ErrorContext(r.Context(), "item proration failed after item remove committed",
				"subscription_id", subID,
				"item_id", itemID,
				"tenant_id", tenantID,
				"plan_id", removedPlanID,
				"quantity", removedQuantity,
				"proration_factor", prorationFactor,
				"error", prorationErr,
			)
			if h.auditLogger != nil {
				_ = h.auditLogger.Log(r.Context(), tenantID, "subscription.proration_failed", "subscription", subID, map[string]any{
					"item_id":          itemID,
					"change_type":      string(domain.ItemChangeTypeRemove),
					"plan_id":          removedPlanID,
					"quantity":         removedQuantity,
					"proration_factor": prorationFactor,
					"error":            prorationErr.Error(),
				})
			}
			respond.Error(w, r, http.StatusInternalServerError, "api_error", "proration_failed",
				"item remove succeeded but proration credit failed — item is removed; retry or contact support to reconcile")
			return
		}
		_ = prorationResult
	}

	if h.events != nil {
		sub, getErr := h.svc.Get(r.Context(), tenantID, subID)
		if getErr != nil {
			sub = domain.Subscription{ID: subID}
		}
		h.fireEvent(r.Context(), tenantID, domain.EventSubscriptionItemRemoved, sub, map[string]any{
			"item_id": itemID,
		})
	}

	w.WriteHeader(http.StatusNoContent)
}

// itemProrationSpec describes a single item mutation that the proration
// generator should price. The generator computes a per-unit-period delta
// between the before-state amount (oldPlan × oldQty) and the after-state
// amount (newPlan × newQty), scales by prorationFactor, and emits either a
// proration invoice (positive delta) or a proration credit (negative).
//
// Each of the four mutation types fills the struct slightly differently:
//   - plan:     old/new plan differ, old/new qty equal (single item.Quantity)
//   - quantity: old/new plan equal, old/new qty differ
//   - add:      oldPlanID="", oldQuantity=0; new populated (delta = +new)
//   - remove:   newPlanID="", newQuantity=0; old populated (delta = -old)
//
// changeAt is the dedup-key timestamp (kept on invoice.source_plan_changed_at
// for historical reasons — see migration 0027 comment). Callers should pass
// item.PlanChangedAt for plan changes, or a freshly-stamped clock for the
// other three.
type itemProrationSpec struct {
	changeType      domain.ItemChangeType
	changeAt        time.Time
	prorationFactor float64
	itemID          string
	oldPlanID       string
	oldQuantity     int64
	newPlanID       string
	newQuantity     int64
}

// handleItemProration generates the invoice or credit for a single item
// mutation. Dedup key is (tenant, subscription, item, change_type, change_at)
// — retries of the same mutation converge on the existing artifact via the
// proration dedup index.
func (h *Handler) handleItemProration(ctx context.Context, tenantID string, sub domain.Subscription, spec itemProrationSpec) (*ProrationDetail, error) {
	// Resolve plans needed for pricing and naming. The "effective" plan drives
	// currency and coupon eligibility — for a remove it's the old plan; for
	// anything else it's the new plan.
	var oldPlan, newPlan domain.Plan
	if spec.oldPlanID != "" {
		p, err := h.plans.GetPlan(ctx, tenantID, spec.oldPlanID)
		if err != nil {
			return nil, fmt.Errorf("get old plan: %w", err)
		}
		oldPlan = p
	}
	if spec.newPlanID != "" {
		p, err := h.plans.GetPlan(ctx, tenantID, spec.newPlanID)
		if err != nil {
			return nil, fmt.Errorf("get new plan: %w", err)
		}
		newPlan = p
	}

	effectivePlan := newPlan
	if spec.newPlanID == "" {
		effectivePlan = oldPlan
	}
	if effectivePlan.ID == "" {
		return nil, fmt.Errorf("proration spec resolved no plan; cannot price item mutation")
	}

	// Industry-standard gates on immediate proration emission. Two
	// scenarios must NOT produce an immediate invoice or credit grant:
	//
	// (1) effective plan is in_arrears: the customer hasn't paid for
	//     the current period yet (in_arrears bills at cycle close).
	//     Emitting an immediate proration invoice + cycle-close full-
	//     period billing double-counts the new rate. Emitting an
	//     immediate credit gives "refund" against money never paid.
	//     Industry-aligned: Stripe `proration_behavior=none` for this
	//     case; Lago defers downgrades entirely. Velox defers the
	//     proration to cycle close (cycle bills at the NEW plan/qty
	//     full-period — slight imprecision vs. true segment math, but
	//     no double-counting and no phantom credit).
	//
	// (2) effective plan is in_advance BUT the source invoice for the
	//     current period was not paid: the would-be credit is against
	//     money the customer never put in (Chargebee "Adjustment" credit
	//     case; Stripe explicitly warns about this). Skip immediate
	//     emission; if the operator wants to settle, void the unpaid
	//     invoice or wait for dunning.
	//
	// Both gates are silent-defer (logged at info, no error). The
	// downstream item change itself still applies — just no proration
	// artifact.
	if effectivePlan.BaseBillTiming != domain.BillInAdvance {
		slog.InfoContext(ctx, "item proration deferred: in_arrears plan; cycle close bills under new plan/qty",
			"subscription_id", sub.ID,
			"item_id", spec.itemID,
			"change_type", spec.changeType,
			"effective_plan_id", effectivePlan.ID,
		)
		return nil, nil
	}
	if h.invoices != nil && sub.CurrentBillingPeriodStart != nil {
		src, lookupErr := h.invoices.FindBaseInvoiceForPeriod(ctx, tenantID, sub.ID, *sub.CurrentBillingPeriodStart)
		if lookupErr != nil {
			slog.InfoContext(ctx, "item proration deferred: in_advance source invoice not found for current period",
				"subscription_id", sub.ID,
				"item_id", spec.itemID,
				"change_type", spec.changeType,
				"period_start", *sub.CurrentBillingPeriodStart,
				"error", lookupErr,
			)
			return nil, nil
		}
		if src.PaymentStatus != domain.PaymentSucceeded {
			slog.InfoContext(ctx, "item proration deferred: in_advance source invoice not paid",
				"subscription_id", sub.ID,
				"item_id", spec.itemID,
				"change_type", spec.changeType,
				"source_invoice_id", src.ID,
				"source_payment_status", src.PaymentStatus,
			)
			return nil, nil
		}
	}

	oldAmount := oldPlan.BaseAmountCents * spec.oldQuantity
	newAmount := newPlan.BaseAmountCents * spec.newQuantity
	diff := float64(newAmount-oldAmount) * spec.prorationFactor
	proratedCents := int64(math.RoundToEven(diff))

	if proratedCents == 0 {
		return nil, nil
	}

	detail := &ProrationDetail{
		OldPlanID:       spec.oldPlanID,
		NewPlanID:       spec.newPlanID,
		ProrationFactor: spec.prorationFactor,
		AmountCents:     proratedCents,
	}

	memo := prorationMemo(spec, oldPlan, newPlan)

	if proratedCents > 0 {
		// Honors ctx-bound effective-now (PR-12) so proration invoice
		// IssuedAt/DueAt land in sim-time on clock-pinned subs.
		now := clock.Now(ctx)
		dueAt := now.AddDate(0, 0, 30)

		periodStart := spec.changeAt
		var periodEnd time.Time
		if sub.CurrentBillingPeriodEnd != nil {
			periodEnd = *sub.CurrentBillingPeriodEnd
		} else {
			periodEnd = spec.changeAt
		}

		// Line item quantity represents "what was billed" for this charge.
		// For plan changes it's the item quantity; for qty/add it's the
		// new quantity (effectively the delta billed).
		lineQty := spec.newQuantity
		if lineQty == 0 {
			lineQty = 1
		}

		var discountCents int64
		var appliedRedemptionIDs []string
		if h.coupons != nil {
			d, err := h.coupons.ApplyToInvoice(ctx, tenantID, sub.ID, sub.CustomerID, effectivePlan.Currency, planIDsFromItems(sub.Items), proratedCents)
			if err != nil {
				slog.WarnContext(ctx, "coupon apply failed on proration, proceeding without discount",
					"error", err, "subscription_id", sub.ID)
			} else {
				discountCents = d.Cents
				appliedRedemptionIDs = d.RedemptionIDs
			}
		}
		lineItem := domain.InvoiceLineItem{
			LineType:         domain.LineTypeBaseFee,
			Description:      memo,
			Quantity:         lineQty,
			UnitAmountCents:  proratedCents / max64(lineQty, 1),
			AmountCents:      proratedCents,
			TotalAmountCents: proratedCents,
			Currency:         effectivePlan.Currency,
		}
		lineItems := []domain.InvoiceLineItem{lineItem}

		taxResult := ProrationTaxResult{
			SubtotalCents: proratedCents,
			DiscountCents: discountCents,
		}
		if h.tax != nil {
			r, err := h.tax.ApplyTaxToLineItems(ctx, tenantID, sub.CustomerID, effectivePlan.Currency, proratedCents, discountCents, lineItems)
			if err != nil {
				slog.WarnContext(ctx, "tax apply failed on proration, proceeding with zero tax",
					"error", err, "subscription_id", sub.ID)
			} else {
				taxResult = r
			}
		}
		netProrated := taxResult.SubtotalCents - taxResult.DiscountCents + taxResult.TaxAmountCents

		changeAt := spec.changeAt
		invoice := domain.Invoice{
			CustomerID:     sub.CustomerID,
			SubscriptionID: sub.ID,
			// Tax-deferred + pause-collection gate (matches
			// engine.billOnePeriod + BillOnCreate). Pre-fix the
			// proration invoice hardcoded Finalized regardless of
			// tax; if Stripe Tax returned customer_data_invalid the
			// invoice finalized with TaxAmountCents=0, lying about
			// authoritative amounts.
			Status:             domain.InvoiceFinalizationStatus(taxResult.TaxStatus, sub.PauseCollection),
			PaymentStatus:      domain.PaymentPending,
			Currency:           effectivePlan.Currency,
			SubtotalCents:      taxResult.SubtotalCents,
			DiscountCents:      taxResult.DiscountCents,
			TaxRateBP:          taxResult.TaxRateBP,
			TaxName:            taxResult.TaxName,
			TaxCountry:         taxResult.TaxCountry,
			TaxID:              taxResult.TaxID,
			TaxAmountCents:     taxResult.TaxAmountCents,
			TaxStatus:          taxResult.TaxStatus,
			TotalAmountCents:   netProrated,
			AmountDueCents:     netProrated,
			BillingPeriodStart: periodStart,
			BillingPeriodEnd:   periodEnd,
			IssuedAt:           &now,
			DueAt:              &dueAt,
			// CreatedAt on the same `now` so test-clock-driven plan
			// changes have created_at == issued_at on simulation time.
			CreatedAt:                now,
			NetPaymentTermDays:       30,
			Memo:                     memo,
			SourcePlanChangedAt:      &changeAt,
			SourceSubscriptionItemID: spec.itemID,
			SourceChangeType:         spec.changeType,
		}

		invoiceNumber, err := h.invoices.NextInvoiceNumber(ctx, tenantID)
		if err != nil {
			return nil, fmt.Errorf("allocate proration invoice number: %w", err)
		}
		invoice.InvoiceNumber = invoiceNumber

		inv, err := h.invoices.CreateInvoiceWithLineItems(ctx, tenantID, invoice, lineItems)
		if errors.Is(err, errs.ErrAlreadyExists) {
			existing, lookupErr := h.invoices.GetByProrationSource(ctx, tenantID, sub.ID, spec.itemID, spec.changeType, spec.changeAt)
			if lookupErr != nil {
				return nil, fmt.Errorf("proration dedup lookup: %w", lookupErr)
			}
			slog.InfoContext(ctx, "proration invoice already exists; retry dedup",
				"invoice_id", existing.ID,
				"subscription_id", sub.ID,
				"item_id", spec.itemID,
				"change_type", spec.changeType,
				"change_at", spec.changeAt,
			)
			detail.InvoiceID = existing.ID
			detail.Type = "invoice"
			return detail, nil
		}
		if err != nil {
			return nil, fmt.Errorf("create proration invoice: %w", err)
		}

		detail.InvoiceID = inv.ID
		detail.Type = "invoice"

		if h.coupons != nil && len(appliedRedemptionIDs) > 0 {
			if err := h.coupons.MarkPeriodsApplied(ctx, tenantID, appliedRedemptionIDs); err != nil {
				slog.WarnContext(ctx, "coupon mark-periods-applied failed on proration",
					"invoice_id", inv.ID,
					"subscription_id", sub.ID,
					"error", err)
			}
		}

		slog.InfoContext(ctx, "proration invoice created",
			"invoice_id", inv.ID,
			"subscription_id", sub.ID,
			"item_id", spec.itemID,
			"change_type", spec.changeType,
			"amount_cents", proratedCents,
		)
	} else {
		creditAmount := -proratedCents
		if h.credits != nil {
			err := h.credits.GrantProration(ctx, tenantID, ProrationGrantInput{
				CustomerID:               sub.CustomerID,
				AmountCents:              creditAmount,
				Description:              memo,
				SourceSubscriptionID:     sub.ID,
				SourceSubscriptionItemID: spec.itemID,
				SourcePlanChangedAt:      spec.changeAt,
				SourceChangeType:         spec.changeType,
			})
			if errors.Is(err, errs.ErrAlreadyExists) {
				existing, lookupErr := h.credits.GetByProrationSource(ctx, tenantID, sub.ID, spec.itemID, spec.changeType, spec.changeAt)
				if lookupErr != nil {
					return nil, fmt.Errorf("proration credit dedup lookup: %w", lookupErr)
				}
				slog.InfoContext(ctx, "proration credit already granted; retry dedup",
					"entry_id", existing.ID,
					"subscription_id", sub.ID,
					"item_id", spec.itemID,
					"change_type", spec.changeType,
					"change_at", spec.changeAt,
				)
				detail.AmountCents = existing.AmountCents
				detail.Type = "credit"
				return detail, nil
			}
			if err != nil {
				return nil, fmt.Errorf("grant proration credit: %w", err)
			}

			detail.AmountCents = creditAmount
			detail.Type = "credit"

			slog.InfoContext(ctx, "proration credit granted",
				"subscription_id", sub.ID,
				"item_id", spec.itemID,
				"change_type", spec.changeType,
				"credit_cents", creditAmount,
			)
		}
	}

	return detail, nil
}

// prorationMemo picks a human-readable description per change type. Kept
// separate from handleItemProration so the math and the wording don't tangle.
func prorationMemo(spec itemProrationSpec, oldPlan, newPlan domain.Plan) string {
	switch spec.changeType {
	case domain.ItemChangeTypePlan:
		verb := "upgrade"
		if newPlan.BaseAmountCents < oldPlan.BaseAmountCents {
			verb = "downgrade"
		}
		return fmt.Sprintf("Plan %s proration: %s -> %s (qty %d)", verb, oldPlan.Name, newPlan.Name, spec.newQuantity)
	case domain.ItemChangeTypeQuantity:
		return fmt.Sprintf("Quantity change proration: %s (%d -> %d seats)", newPlan.Name, spec.oldQuantity, spec.newQuantity)
	case domain.ItemChangeTypeAdd:
		return fmt.Sprintf("Item add proration: %s (qty %d)", newPlan.Name, spec.newQuantity)
	case domain.ItemChangeTypeRemove:
		return fmt.Sprintf("Item remove proration: %s (qty %d)", oldPlan.Name, spec.oldQuantity)
	}
	return "Item change proration"
}

// remainingPeriodFactor returns the fraction of the current billing period
// that is still ahead of `now`, clamped to [0, 1]. Used to scale a proration
// charge/credit against the per-period price.
func remainingPeriodFactor(sub domain.Subscription, now time.Time) float64 {
	if sub.CurrentBillingPeriodStart == nil || sub.CurrentBillingPeriodEnd == nil {
		return 0
	}
	total := sub.CurrentBillingPeriodEnd.Sub(*sub.CurrentBillingPeriodStart).Hours() / 24
	remaining := sub.CurrentBillingPeriodEnd.Sub(now).Hours() / 24
	if total <= 0 || remaining <= 0 {
		return 0
	}
	if remaining > total {
		return 1
	}
	return remaining / total
}

func planIDsFromItems(items []domain.SubscriptionItem) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.PlanID)
	}
	return out
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// --- Activity timeline (T0-18) ---
//
// Industry-standard subscription detail view (Stripe, Lago, Orb) shows
// a chronological feed of everything that happened to a subscription so
// a CS rep responding to "why was my sub cancelled?" lands on the right
// page with the right story already written. We source it from the
// audit_log — every lifecycle mutation (create/activate/pause/resume/
// cancel, item add/remove/update) is already captured there with the
// actor, timestamp, and a metadata blob. No new instrumentation needed,
// and no RLS gap since audit.Logger.Query already runs under the
// tenant-scoped tx.

// timelineEvent is the wire shape the SPA's timeline component consumes.
// Kept structurally compatible with the invoice payment-timeline event
// so the React renderer can handle both without branching logic.
type timelineEvent struct {
	Timestamp   string `json:"timestamp"`
	Source      string `json:"source"`
	EventType   string `json:"event_type"`
	Status      string `json:"status"`
	Description string `json:"description"`
	// Detail renders as a sub-line beneath the description (mirrors
	// invoice timeline's same-named field). Used for the
	// human-meaningful context that doesn't belong in the main line:
	// "At end of current period", "New trial end: …", "Amount ≥ …",
	// "Plan: … → …", "After next cycle close", etc.
	Detail      string `json:"detail,omitempty"`
	ActorType   string `json:"actor_type,omitempty"`
	ActorName   string `json:"actor_name,omitempty"`
	ActorID     string `json:"actor_id,omitempty"`
	// IsSimulated marks events whose timestamp is in the simulated-
	// time domain. On a clock-pinned sub, operator audit actions
	// stamp audit_log.created_at via clock.Now(boundCtx) (PR-11/12
	// + b46bdee), so the row's timestamp IS in sim-time. Mirrors the
	// invoice timeline's same-named field — authoritative flag,
	// SPA renders the chip purely off this. Wall-clock subs ship
	// false and no chip renders.
	IsSimulated bool `json:"is_simulated,omitempty"`
}

// planLabel renders an operator-facing plan reference: prefer the
// human-readable plan name from planNames, fall back to "Plan {id}"
// only when the lookup is empty (deleted plan, lookup failure, or
// the description is generated outside the timeline handler where
// the map isn't populated). Industry standard — Stripe / Lago /
// Chargebee / Orb all show plan names in activity feeds, never raw
// "plan_xxx" tokens.
func planLabel(planID string, planNames map[string]string) string {
	if name, ok := planNames[planID]; ok && name != "" {
		return name
	}
	return planID
}

// describeSubscriptionAction maps audit_log action + metadata to a
// human-readable sentence + sub-line + status tag the UI colors by.
// Unknown actions pass through with a neutral info tag rather than
// hiding — the feed should never silently drop an event.
//
// Detail is the optional sub-line beneath the main description. Used
// for human-meaningful context (dates, counts, plan names) that
// bloats the title if inlined. Mirrors the invoice timeline shape.
//
// planNames is a lookup from plan_id → plan.Name used to resolve
// raw IDs in metadata to operator-friendly labels. The caller
// (activityTimeline) batch-fetches every plan_id referenced in the
// audit entries before invoking this function. Missing entries fall
// back to the raw ID (deleted plan / lookup miss).
func describeSubscriptionAction(action string, meta map[string]any, planNames map[string]string) (desc, detail, status string) {
	switch action {
	case domain.AuditActionCreate:
		return "Subscription created", "", "info"
	case domain.AuditActionActivate:
		return "Subscription activated", "", "succeeded"
	case domain.AuditActionCancel:
		by := ""
		if v, ok := meta["canceled_by"].(string); ok && v != "" {
			by = " by " + v
		}
		return "Subscription canceled" + by, "", "canceled"
	case domain.AuditActionUpdate:
		// AuditActionUpdate is a catch-all bucket; the meaningful
		// discriminator is meta["action"]. Every operator-driven
		// mutation that doesn't have its own audit action (cancel,
		// activate, create) routes through here with a sub-action tag.
		a, _ := meta["action"].(string)
		switch a {
		case "cancel_scheduled":
			if v, ok := meta["cancel_at_period_end"].(bool); ok && v {
				return "Cancellation scheduled", "At end of current period", "warning"
			}
			if t, ok := meta["cancel_at"].(string); ok && t != "" {
				return "Cancellation scheduled", "On " + formatAuditTimestamp(t), "warning"
			}
			return "Cancellation scheduled", "", "warning"
		case "cancel_cleared":
			return "Scheduled cancellation cleared", "", "info"
		case "collection_paused":
			d := ""
			if r, ok := meta["resumes_at"].(string); ok && r != "" {
				d = "Auto-resumes " + formatAuditTimestamp(r)
			} else {
				d = "Cycle keeps drafting; no charge until resumed"
			}
			return "Collection paused", d, "warning"
		case "collection_resumed":
			return "Collection resumed", "", "succeeded"
		case "trial_ended":
			return "Trial ended early", "", "info"
		case "trial_extended":
			d := ""
			if t, ok := meta["trial_end"].(string); ok && t != "" {
				d = "New trial end: " + formatAuditTimestamp(t)
			}
			return "Trial extended", d, "info"
		case "billing_thresholds_set":
			parts := []string{}
			if v, ok := meta["amount_gte"].(float64); ok && v > 0 {
				parts = append(parts, fmt.Sprintf("Amount ≥ %d¢", int64(v)))
			}
			if v, ok := meta["item_threshold_count"].(float64); ok && v > 0 {
				parts = append(parts, fmt.Sprintf("%d item threshold%s", int(v), plural(int(v))))
			}
			return "Billing thresholds set", strings.Join(parts, " · "), "info"
		case "billing_thresholds_cleared":
			return "Billing thresholds cleared", "", "info"
		case "item_added":
			parts := []string{}
			if v, ok := meta["plan_id"].(string); ok && v != "" {
				parts = append(parts, planLabel(v, planNames))
			}
			if q, ok := meta["quantity"].(float64); ok && q > 0 {
				parts = append(parts, fmt.Sprintf("qty %d", int(q)))
			}
			return "Item added", strings.Join(parts, " · "), "info"
		case "item_removed":
			// item_id was operator-illegible noise on the row; drop
			// it entirely. "Item removed" is enough — operators
			// reading the timeline don't think in vlx_si_ tokens.
			return "Item removed", "", "info"
		case "cancel_pending_item_plan_change":
			return "Pending plan change canceled", "", "info"
		}
		// Unknown sub-action — surface the bucket label rather than
		// hiding the row. Better than silently dropping audit context.
		return "Subscription updated", "", "info"
	case "subscription.item_updated":
		// Item-level plan + quantity changes go through this dedicated
		// action (not AuditActionUpdate) so the metadata discriminator
		// lives in meta["action"]: "item_plan_changed" or
		// "item_quantity_changed".
		a, _ := meta["action"].(string)
		immediate, _ := meta["immediate"].(bool)
		when := "At next period"
		if immediate {
			when = "Immediate"
		}
		switch a {
		case "item_plan_changed":
			parts := []string{}
			oldPlan, _ := meta["old_plan_id"].(string)
			newPlan, _ := meta["new_plan_id"].(string)
			if oldPlan != "" && newPlan != "" {
				parts = append(parts, planLabel(oldPlan, planNames)+" → "+planLabel(newPlan, planNames))
			}
			parts = append(parts, when)
			return "Plan changed", strings.Join(parts, " · "), "info"
		case "item_quantity_changed":
			parts := []string{}
			if q, ok := meta["quantity"].(float64); ok {
				parts = append(parts, fmt.Sprintf("To qty %d", int(q)))
			}
			parts = append(parts, when)
			return "Quantity changed", strings.Join(parts, " · "), "info"
		}
		return "Item updated", "", "info"
	case "subscription.proration_failed":
		d := ""
		if e, ok := meta["error"].(string); ok && e != "" {
			d = e
		}
		return "Proration failed", d, "warning"
	default:
		return action, "", "info"
	}
}

// plural returns "s" for n != 1, else "". Tiny helper to avoid the
// "1 thresholds" / "2 threshold" pluralization mistake.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// formatAuditTimestamp humanizes an RFC3339 timestamp from audit
// metadata for sub-line rendering. Returns the input unchanged on
// parse failure — better than dropping the value entirely.
func formatAuditTimestamp(raw string) string {
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return raw
	}
	return t.UTC().Format("Jan 2, 2006 3:04 PM") + " UTC"
}

func (h *Handler) activityTimeline(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	// Verify the subscription exists + belongs to this tenant before
	// leaking a 200 with empty events — otherwise a bad id returns the
	// same shape as a real sub that just has no audit yet. Sub is
	// also used to compute is_simulated below.
	sub, err := h.svc.Get(r.Context(), tenantID, id)
	if err != nil {
		if errors.Is(err, errs.ErrNotFound) {
			respond.NotFound(w, r, "subscription")
			return
		}
		respond.InternalError(w, r)
		return
	}
	// Audit rows on clock-pinned subs stamp audit_log.created_at via
	// clock.Now(boundCtx) (PR-11/12 + b46bdee), so each row's
	// timestamp IS sim-time. Marking every audit-sourced row
	// is_simulated=true on a clock-pinned sub mirrors the invoice
	// timeline's lifecycle-row convention.
	//
	// Caveat: pre-fix audit rows (written before PR-11/12 landed)
	// were stamped wall-clock and will be incorrectly flagged.
	// Acceptable — the timestamp itself reads as recent wall-clock,
	// which is obvious to the operator, and pre-launch DBs only.
	subOnClock := sub.TestClockID != ""

	events := []timelineEvent{}

	if h.auditLogger != nil {
		// Pull a generous slice of audit entries for this sub — the UI
		// shows the most recent first anyway, and subs rarely have more
		// than a few dozen mutations over their lifetime. 200 leaves
		// headroom for pathological cases without unbounded fetches.
		entries, _, err := h.auditLogger.Query(r.Context(), tenantID, audit.QueryFilter{
			ResourceType: "subscription",
			ResourceID:   id,
			Limit:        200,
		})
		if err == nil {
			// Plan-name lookup so the timeline shows "Pro Monthly" instead
			// of "vlx_pln_d83g2obmajdtlif0mk00". Collect every plan_id
			// referenced in metadata (plan_id, old_plan_id, new_plan_id)
			// across all entries, batch-fetch via the wired PlanReader,
			// hand the map to describeSubscriptionAction. Missing plans
			// (deleted, RLS gap, unwired reader) fall back to the raw
			// ID per planLabel's contract — operators still see
			// something rather than a blank.
			planNames := map[string]string{}
			if h.plans != nil {
				wanted := map[string]struct{}{}
				for _, e := range entries {
					for _, key := range []string{"plan_id", "old_plan_id", "new_plan_id"} {
						if v, ok := e.Metadata[key].(string); ok && v != "" {
							wanted[v] = struct{}{}
						}
					}
					if arr, ok := e.Metadata["plan_ids"].([]any); ok {
						for _, v := range arr {
							if s, ok := v.(string); ok && s != "" {
								wanted[s] = struct{}{}
							}
						}
					}
				}
				for pid := range wanted {
					if p, err := h.plans.GetPlan(r.Context(), tenantID, pid); err == nil {
						planNames[pid] = p.Name
					}
				}
			}
			for _, e := range entries {
				desc, detail, status := describeSubscriptionAction(e.Action, e.Metadata, planNames)
				events = append(events, timelineEvent{
					Timestamp:   e.CreatedAt.UTC().Format(time.RFC3339),
					Source:      "audit",
					EventType:   e.Action,
					Status:      status,
					Description: desc,
					Detail:      detail,
					ActorType:   e.ActorType,
					ActorName:   e.ActorName,
					ActorID:     e.ActorID,
					IsSimulated: subOnClock,
				})
			}
		} else {
			slog.ErrorContext(r.Context(), "subscription timeline: audit query",
				"subscription_id", id, "error", err)
		}
	}

	// Ascending order — CS reps read a timeline top-down, earliest first.
	// audit.Logger.Query returns DESC so we flip.
	sort.Slice(events, func(i, j int) bool {
		return events[i].Timestamp < events[j].Timestamp
	})

	respond.JSON(w, r, http.StatusOK, map[string]any{"events": events})
}
