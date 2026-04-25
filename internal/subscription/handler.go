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
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/audit"
	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
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
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// SetAuditLogger configures audit logging for financial operations.
func (h *Handler) SetAuditLogger(l *audit.Logger) { h.auditLogger = l }

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
	r.Post("/{id}/pause", h.pause)
	r.Post("/{id}/resume", h.resume)
	r.Post("/{id}/cancel", h.cancel)
	r.Post("/{id}/schedule-cancel", h.scheduleCancel)
	r.Delete("/{id}/scheduled-cancel", h.clearScheduledCancel)
	r.Put("/{id}/pause-collection", h.pauseCollection)
	r.Delete("/{id}/pause-collection", h.resumeCollection)
	r.Post("/{id}/end-trial", h.endTrial)

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

	h.fireEvent(r.Context(), tenantID, domain.EventSubscriptionActivated, sub, nil)

	respond.JSON(w, r, http.StatusOK, sub)
}

func (h *Handler) pause(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	sub, err := h.svc.Pause(r.Context(), tenantID, id)
	if err != nil {
		respond.FromError(w, r, err, "subscription")
		return
	}

	h.fireEvent(r.Context(), tenantID, domain.EventSubscriptionPaused, sub, nil)

	respond.JSON(w, r, http.StatusOK, sub)
}

func (h *Handler) resume(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	sub, err := h.svc.Resume(r.Context(), tenantID, id)
	if err != nil {
		respond.FromError(w, r, err, "subscription")
		return
	}

	h.fireEvent(r.Context(), tenantID, domain.EventSubscriptionResumed, sub, nil)

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
		_ = h.auditLogger.Log(r.Context(), tenantID, domain.AuditActionCancel, "subscription", sub.ID, map[string]any{
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
		_ = h.auditLogger.Log(r.Context(), tenantID, domain.AuditActionUpdate, "subscription", sub.ID, meta)
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
		_ = h.auditLogger.Log(r.Context(), tenantID, domain.AuditActionUpdate, "subscription", sub.ID, meta)
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
		_ = h.auditLogger.Log(r.Context(), tenantID, domain.AuditActionUpdate, "subscription", sub.ID, map[string]any{
			"action":      "trial_ended",
			"customer_id": sub.CustomerID,
		})
	}

	h.fireEvent(r.Context(), tenantID, domain.EventSubscriptionTrialEnded, sub, map[string]any{
		"triggered_by": "operator",
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
		_ = h.auditLogger.Log(r.Context(), tenantID, domain.AuditActionUpdate, "subscription", sub.ID, map[string]any{
			"action":      "collection_resumed",
			"customer_id": sub.CustomerID,
		})
	}

	h.fireEvent(r.Context(), tenantID, domain.EventSubscriptionCollectionResumed, sub, nil)

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
		_ = h.auditLogger.Log(r.Context(), tenantID, domain.AuditActionUpdate, "subscription", sub.ID, map[string]any{
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

	// Snapshot the pre-add subscription for proration. Factor is computed
	// from the period boundaries which don't change when an item is added
	// mid-cycle, so a pre-add read is equivalent to a post-add read here.
	var subBefore domain.Subscription
	var prorationFactor float64
	if h.plans != nil && h.invoices != nil {
		sub, serr := h.svc.Get(r.Context(), tenantID, id)
		if serr == nil {
			subBefore = sub
			if sub.Status == domain.SubscriptionActive {
				prorationFactor = remainingPeriodFactor(sub, time.Now().UTC())
			}
		}
	}

	item, err := h.svc.AddItem(r.Context(), tenantID, id, input)
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
			changeAt = time.Now().UTC()
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
		item, gerr := h.svc.store.GetItem(r.Context(), tenantID, itemID)
		if gerr == nil && item.SubscriptionID == subID {
			oldPlanID = item.PlanID
			oldQuantity = item.Quantity
		}
		sub, serr := h.svc.Get(r.Context(), tenantID, subID)
		if serr == nil {
			subBefore = sub
			prorationFactor = remainingPeriodFactor(sub, time.Now().UTC())
		}
	}

	result, err := h.svc.UpdateItem(r.Context(), tenantID, subID, itemID, input)
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
		_ = h.auditLogger.Log(r.Context(), tenantID, "subscription.item_updated", "subscription", subID, payload)
	}

	if prorationEligible && prorationFactor > 0 && h.invoices != nil {
		// Re-hydrate the subscription post-change so the Items slice reflects
		// the swapped plan/quantity — handleProration walks it to resolve
		// coupon plan eligibility. Fall back to subBefore on error so the
		// handler still responds, but use the fresh Items when available.
		subAfter, getErr := h.svc.Get(r.Context(), tenantID, subID)
		if getErr != nil {
			subAfter = subBefore
		}
		var spec itemProrationSpec
		if isImmediatePlanChange {
			var changeAt time.Time
			if result.Item.PlanChangedAt != nil {
				changeAt = *result.Item.PlanChangedAt
			} else {
				changeAt = time.Now().UTC()
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
				changeAt = time.Now().UTC()
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

	// Capture the pre-delete item + sub for proration. Removing mid-period
	// should credit back the unused portion of what the customer already paid
	// for this item. RemoveItem is a hard delete so the snapshot must be
	// taken before the call.
	var removedPlanID string
	var removedQuantity int64
	var subBefore domain.Subscription
	var prorationFactor float64
	if h.plans != nil && h.credits != nil {
		item, gerr := h.svc.store.GetItem(r.Context(), tenantID, itemID)
		if gerr == nil && item.SubscriptionID == subID {
			removedPlanID = item.PlanID
			removedQuantity = item.Quantity
		}
		sub, serr := h.svc.Get(r.Context(), tenantID, subID)
		if serr == nil {
			subBefore = sub
			if sub.Status == domain.SubscriptionActive {
				prorationFactor = remainingPeriodFactor(sub, time.Now().UTC())
			}
		}
	}

	if err := h.svc.RemoveItem(r.Context(), tenantID, subID, itemID); err != nil {
		respond.FromError(w, r, err, "subscription item")
		return
	}

	if h.auditLogger != nil {
		_ = h.auditLogger.Log(r.Context(), tenantID, domain.AuditActionUpdate, "subscription", subID, map[string]any{
			"action":  "item_removed",
			"item_id": itemID,
		})
	}

	if prorationFactor > 0 && removedPlanID != "" {
		// Re-fetch for coupon plan eligibility over the remaining items.
		subAfter, getErr := h.svc.Get(r.Context(), tenantID, subID)
		if getErr != nil {
			subAfter = subBefore
		}
		spec := itemProrationSpec{
			changeType:      domain.ItemChangeTypeRemove,
			changeAt:        time.Now().UTC(),
			prorationFactor: prorationFactor,
			itemID:          itemID,
			oldPlanID:       removedPlanID,
			oldQuantity:     removedQuantity,
			newPlanID:       "",
			newQuantity:     0,
		}
		prorationResult, prorationErr := h.handleItemProration(r.Context(), tenantID, subAfter, spec)
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
		now := time.Now().UTC()
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
			CustomerID:               sub.CustomerID,
			SubscriptionID:           sub.ID,
			Status:                   domain.InvoiceFinalized,
			PaymentStatus:            domain.PaymentPending,
			Currency:                 effectivePlan.Currency,
			SubtotalCents:            taxResult.SubtotalCents,
			DiscountCents:            taxResult.DiscountCents,
			TaxRateBP:                taxResult.TaxRateBP,
			TaxName:                  taxResult.TaxName,
			TaxCountry:               taxResult.TaxCountry,
			TaxID:                    taxResult.TaxID,
			TaxAmountCents:           taxResult.TaxAmountCents,
			TotalAmountCents:         netProrated,
			AmountDueCents:           netProrated,
			BillingPeriodStart:       periodStart,
			BillingPeriodEnd:         periodEnd,
			IssuedAt:                 &now,
			DueAt:                    &dueAt,
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
	ActorType   string `json:"actor_type,omitempty"`
	ActorName   string `json:"actor_name,omitempty"`
	ActorID     string `json:"actor_id,omitempty"`
}

// describeSubscriptionAction maps audit_log action + metadata to a
// human-readable sentence + a status tag the UI colors by. Unknown
// actions pass through with a neutral info tag rather than hiding —
// the feed should never silently drop an event.
func describeSubscriptionAction(action string, meta map[string]any) (string, string) {
	switch action {
	case domain.AuditActionCreate:
		return "Subscription created", "info"
	case domain.AuditActionActivate:
		return "Subscription activated", "succeeded"
	case domain.AuditActionPause:
		return "Subscription paused", "warning"
	case domain.AuditActionResume:
		return "Subscription resumed", "succeeded"
	case domain.AuditActionCancel:
		by := ""
		if v, ok := meta["canceled_by"].(string); ok && v != "" {
			by = " by " + v
		}
		return "Subscription canceled" + by, "canceled"
	case domain.AuditActionUpdate:
		// Item-level mutations (plan change, quantity change, add, remove)
		// and the cancel-schedule mutations all land on AuditActionUpdate
		// with a metadata discriminator. Read the most-useful field if
		// present; otherwise stay generic.
		if a, ok := meta["action"].(string); ok {
			switch a {
			case "cancel_scheduled":
				if v, ok := meta["cancel_at_period_end"].(bool); ok && v {
					return "Cancellation scheduled at period end", "warning"
				}
				return "Cancellation scheduled", "warning"
			case "cancel_cleared":
				return "Scheduled cancellation cleared", "info"
			}
		}
		if t, ok := meta["change_type"].(string); ok && t != "" {
			switch t {
			case "plan":
				return "Plan changed", "info"
			case "quantity":
				return "Quantity updated", "info"
			case "add":
				return "Item added", "info"
			case "remove":
				return "Item removed", "info"
			}
		}
		return "Subscription updated", "info"
	default:
		return action, "info"
	}
}

func (h *Handler) activityTimeline(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	// Verify the subscription exists + belongs to this tenant before
	// leaking a 200 with empty events — otherwise a bad id returns the
	// same shape as a real sub that just has no audit yet.
	if _, err := h.svc.Get(r.Context(), tenantID, id); err != nil {
		if errors.Is(err, errs.ErrNotFound) {
			respond.NotFound(w, r, "subscription")
			return
		}
		respond.InternalError(w, r)
		return
	}

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
			for _, e := range entries {
				desc, status := describeSubscriptionAction(e.Action, e.Metadata)
				events = append(events, timelineEvent{
					Timestamp:   e.CreatedAt.UTC().Format(time.RFC3339),
					Source:      "audit",
					EventType:   e.Action,
					Status:      status,
					Description: desc,
					ActorType:   e.ActorType,
					ActorName:   e.ActorName,
					ActorID:     e.ActorID,
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
