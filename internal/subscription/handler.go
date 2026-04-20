package subscription

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
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
	GetByProrationSource(ctx context.Context, tenantID, subscriptionID, subscriptionItemID string, planChangedAt time.Time) (domain.Invoice, error)
	NextInvoiceNumber(ctx context.Context, tenantID string) (string, error)
}

// ProrationCreditGranter grants credits for downgrade proration. Dedup key is
// (subscription, item, plan_changed_at) — see ProrationInvoiceCreator comment.
type ProrationCreditGranter interface {
	GrantProration(ctx context.Context, tenantID string, input ProrationGrantInput) error
	GetByProrationSource(ctx context.Context, tenantID, subscriptionID, subscriptionItemID string, planChangedAt time.Time) (domain.CreditLedgerEntry, error)
}

// ProrationCouponApplier computes a coupon discount against a proration
// invoice's subtotal. planIDs is the full set of item plan_ids on the
// subscription — the coupon's own plan gate is intersected against the full
// item set (any match ⇒ eligible), matching how Stripe treats coupons on
// multi-item subscriptions.
type ProrationCouponApplier interface {
	ApplyToInvoice(ctx context.Context, tenantID, subscriptionID string, planIDs []string, subtotalCents int64) (domain.CouponDiscountResult, error)
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

// ProrationGrantInput carries the downgrade credit payload plus the
// provenance fields required for dedup.
type ProrationGrantInput struct {
	CustomerID               string
	AmountCents              int64
	Description              string
	SourceSubscriptionID     string
	SourceSubscriptionItemID string
	SourcePlanChangedAt      time.Time
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
		slog.Error("dispatch subscription event",
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

	// Items — Stripe-style per-item mutation. Quantity and plan changes land
	// on the same PATCH (body discriminates), pending-change clear has its own
	// DELETE so client code can target it without a PATCH body shape.
	r.Post("/{id}/items", h.addItem)
	r.Patch("/{id}/items/{itemID}", h.updateItem)
	r.Delete("/{id}/items/{itemID}/pending-change", h.cancelPendingItemChange)
	r.Delete("/{id}/items/{itemID}", h.removeItem)
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
	if errors.Is(err, errs.ErrAlreadyExists) {
		respond.Conflict(w, r, err.Error())
		return
	}
	if err != nil {
		respond.Validation(w, r, err.Error())
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
		slog.Error("list subscriptions", "error", err)
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
		slog.Error("get subscription", "error", err)
		return
	}

	respond.JSON(w, r, http.StatusOK, sub)
}

func (h *Handler) activate(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	sub, err := h.svc.Activate(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "subscription")
		return
	}
	if err != nil {
		respond.Validation(w, r, err.Error())
		return
	}

	h.fireEvent(r.Context(), tenantID, domain.EventSubscriptionActivated, sub, nil)

	respond.JSON(w, r, http.StatusOK, sub)
}

func (h *Handler) pause(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	sub, err := h.svc.Pause(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "subscription")
		return
	}
	if err != nil {
		respond.Validation(w, r, err.Error())
		return
	}

	h.fireEvent(r.Context(), tenantID, domain.EventSubscriptionPaused, sub, nil)

	respond.JSON(w, r, http.StatusOK, sub)
}

func (h *Handler) resume(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	sub, err := h.svc.Resume(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "subscription")
		return
	}
	if err != nil {
		respond.Validation(w, r, err.Error())
		return
	}

	h.fireEvent(r.Context(), tenantID, domain.EventSubscriptionResumed, sub, nil)

	respond.JSON(w, r, http.StatusOK, sub)
}

func (h *Handler) cancel(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	sub, err := h.svc.Cancel(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "subscription")
		return
	}
	if err != nil {
		respond.Validation(w, r, err.Error())
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

// addItem appends a new priced line to a subscription.
func (h *Handler) addItem(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	var input AddItemInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	item, err := h.svc.AddItem(r.Context(), tenantID, id, input)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "subscription")
		return
	}
	if errors.Is(err, errs.ErrAlreadyExists) {
		respond.Conflict(w, r, "an item for this plan already exists on the subscription")
		return
	}
	if err != nil {
		respond.Validation(w, r, err.Error())
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

	// Re-fetch the subscription so the event payload reflects the full post-add
	// item_count and status. Silent fallback: if the read fails we still emit
	// with a minimal struct — the item.added event is still useful without
	// the enclosing sub snapshot.
	if h.events != nil {
		sub, getErr := h.svc.Get(r.Context(), tenantID, id)
		if getErr != nil {
			sub = domain.Subscription{ID: id}
		}
		h.fireEvent(r.Context(), tenantID, domain.EventSubscriptionItemAdded, sub, map[string]any{
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

	// Capture the pre-change plan only when we're about to drive proration —
	// the old plan id and the subscription's remaining period both come from
	// a snapshot taken before UpdateItem mutates the row.
	var oldPlanID string
	var prorationFactor float64
	var subBefore domain.Subscription
	isImmediatePlanChange := input.NewPlanID != "" && input.Immediate
	if isImmediatePlanChange && h.plans != nil {
		item, gerr := h.svc.store.GetItem(r.Context(), tenantID, itemID)
		if gerr == nil && item.SubscriptionID == subID {
			oldPlanID = item.PlanID
		}
		sub, serr := h.svc.Get(r.Context(), tenantID, subID)
		if serr == nil {
			subBefore = sub
			prorationFactor = remainingPeriodFactor(sub, time.Now().UTC())
		}
	}

	result, err := h.svc.UpdateItem(r.Context(), tenantID, subID, itemID, input)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "subscription item")
		return
	}
	if err != nil {
		respond.Validation(w, r, err.Error())
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

	if isImmediatePlanChange && prorationFactor > 0 && h.plans != nil && h.invoices != nil {
		// Re-hydrate the subscription post-change so the Items slice reflects
		// the swapped plan — handleProration walks it to resolve coupon plan
		// eligibility. We fall back to subBefore on error so the handler still
		// responds, but use the fresh Items when available.
		subAfter, getErr := h.svc.Get(r.Context(), tenantID, subID)
		if getErr != nil {
			subAfter = subBefore
		}
		prorationResult, prorationErr := h.handleItemProration(r.Context(), tenantID, subAfter, result.Item, oldPlanID, prorationFactor)
		if prorationErr != nil {
			slog.Error("item proration failed after plan change committed",
				"subscription_id", subID,
				"item_id", result.Item.ID,
				"tenant_id", tenantID,
				"old_plan_id", oldPlanID,
				"new_plan_id", input.NewPlanID,
				"proration_factor", prorationFactor,
				"error", prorationErr,
			)
			if h.auditLogger != nil {
				_ = h.auditLogger.Log(r.Context(), tenantID, "subscription.proration_failed", "subscription", subID, map[string]any{
					"item_id":          result.Item.ID,
					"old_plan_id":      oldPlanID,
					"new_plan_id":      input.NewPlanID,
					"proration_factor": prorationFactor,
					"error":            prorationErr.Error(),
				})
			}
			respond.Error(w, r, http.StatusInternalServerError, "api_error", "proration_failed",
				"plan change succeeded but proration generation failed — subscription item is on the new plan; retry or contact support to reconcile")
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
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "subscription item")
		return
	}
	if err != nil {
		respond.Validation(w, r, err.Error())
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

	if err := h.svc.RemoveItem(r.Context(), tenantID, subID, itemID); err != nil {
		if errors.Is(err, errs.ErrNotFound) {
			respond.NotFound(w, r, "subscription item")
			return
		}
		respond.Validation(w, r, err.Error())
		return
	}

	if h.auditLogger != nil {
		_ = h.auditLogger.Log(r.Context(), tenantID, domain.AuditActionUpdate, "subscription", subID, map[string]any{
			"action":  "item_removed",
			"item_id": itemID,
		})
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

// handleItemProration creates a proration invoice (upgrade) or grants credit
// (downgrade) for an immediate plan change on one item. Dedup key is
// (subscription, item, plan_changed_at) — retries of the same change converge
// on the existing invoice/credit via the proration dedup index.
func (h *Handler) handleItemProration(ctx context.Context, tenantID string, sub domain.Subscription, item domain.SubscriptionItem, oldPlanID string, prorationFactor float64) (*ProrationDetail, error) {
	oldPlan, err := h.plans.GetPlan(ctx, tenantID, oldPlanID)
	if err != nil {
		return nil, fmt.Errorf("get old plan: %w", err)
	}
	newPlan, err := h.plans.GetPlan(ctx, tenantID, item.PlanID)
	if err != nil {
		return nil, fmt.Errorf("get new plan: %w", err)
	}

	// Quantity multiplies the per-unit proration so a 10-seat upgrade charges
	// 10× the single-seat diff. Fixed-amount coupons still apply at the
	// invoice level (downstream).
	diff := float64(newPlan.BaseAmountCents-oldPlan.BaseAmountCents) * prorationFactor * float64(item.Quantity)
	proratedCents := int64(math.RoundToEven(diff))

	if proratedCents == 0 {
		return nil, nil
	}

	if item.PlanChangedAt == nil {
		return nil, fmt.Errorf("item missing plan_changed_at; cannot generate proration safely")
	}
	planChangedAt := *item.PlanChangedAt

	detail := &ProrationDetail{
		OldPlanID:       oldPlanID,
		NewPlanID:       newPlan.ID,
		ProrationFactor: prorationFactor,
		AmountCents:     proratedCents,
	}

	if proratedCents > 0 {
		now := time.Now().UTC()
		dueAt := now.AddDate(0, 0, 30)

		periodStart := planChangedAt
		var periodEnd time.Time
		if sub.CurrentBillingPeriodEnd != nil {
			periodEnd = *sub.CurrentBillingPeriodEnd
		} else {
			periodEnd = planChangedAt
		}

		var discountCents int64
		var appliedRedemptionIDs []string
		if h.coupons != nil {
			d, err := h.coupons.ApplyToInvoice(ctx, tenantID, sub.ID, planIDsFromItems(sub.Items), proratedCents)
			if err != nil {
				slog.Warn("coupon apply failed on proration, proceeding without discount",
					"error", err, "subscription_id", sub.ID)
			} else {
				discountCents = d.Cents
				appliedRedemptionIDs = d.RedemptionIDs
			}
		}
		memo := fmt.Sprintf("Plan upgrade proration: %s -> %s (qty %d)", oldPlan.Name, newPlan.Name, item.Quantity)
		lineItem := domain.InvoiceLineItem{
			LineType:         domain.LineTypeBaseFee,
			Description:      memo,
			Quantity:         item.Quantity,
			UnitAmountCents:  proratedCents / max64(item.Quantity, 1),
			AmountCents:      proratedCents,
			TotalAmountCents: proratedCents,
			Currency:         newPlan.Currency,
		}
		lineItems := []domain.InvoiceLineItem{lineItem}

		taxResult := ProrationTaxResult{
			SubtotalCents: proratedCents,
			DiscountCents: discountCents,
		}
		if h.tax != nil {
			r, err := h.tax.ApplyTaxToLineItems(ctx, tenantID, sub.CustomerID, newPlan.Currency, proratedCents, discountCents, lineItems)
			if err != nil {
				slog.Warn("tax apply failed on proration, proceeding with zero tax",
					"error", err, "subscription_id", sub.ID)
			} else {
				taxResult = r
			}
		}
		netProrated := taxResult.SubtotalCents - taxResult.DiscountCents + taxResult.TaxAmountCents

		invoice := domain.Invoice{
			CustomerID:               sub.CustomerID,
			SubscriptionID:           sub.ID,
			Status:                   domain.InvoiceFinalized,
			PaymentStatus:            domain.PaymentPending,
			Currency:                 newPlan.Currency,
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
			SourcePlanChangedAt:      &planChangedAt,
			SourceSubscriptionItemID: item.ID,
		}

		invoiceNumber, err := h.invoices.NextInvoiceNumber(ctx, tenantID)
		if err != nil {
			return nil, fmt.Errorf("allocate proration invoice number: %w", err)
		}
		invoice.InvoiceNumber = invoiceNumber

		inv, err := h.invoices.CreateInvoiceWithLineItems(ctx, tenantID, invoice, lineItems)
		if errors.Is(err, errs.ErrAlreadyExists) {
			existing, lookupErr := h.invoices.GetByProrationSource(ctx, tenantID, sub.ID, item.ID, planChangedAt)
			if lookupErr != nil {
				return nil, fmt.Errorf("proration dedup lookup: %w", lookupErr)
			}
			slog.Info("proration invoice already exists; retry dedup",
				"invoice_id", existing.ID,
				"subscription_id", sub.ID,
				"item_id", item.ID,
				"plan_changed_at", planChangedAt,
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
				slog.Warn("coupon mark-periods-applied failed on proration",
					"invoice_id", inv.ID,
					"subscription_id", sub.ID,
					"error", err)
			}
		}

		slog.Info("proration invoice created",
			"invoice_id", inv.ID,
			"subscription_id", sub.ID,
			"item_id", item.ID,
			"amount_cents", proratedCents,
			"old_plan", oldPlan.Name,
			"new_plan", newPlan.Name,
		)
	} else {
		creditAmount := -proratedCents
		if h.credits != nil {
			desc := fmt.Sprintf("Plan downgrade proration: %s -> %s (qty %d)", oldPlan.Name, newPlan.Name, item.Quantity)
			err := h.credits.GrantProration(ctx, tenantID, ProrationGrantInput{
				CustomerID:               sub.CustomerID,
				AmountCents:              creditAmount,
				Description:              desc,
				SourceSubscriptionID:     sub.ID,
				SourceSubscriptionItemID: item.ID,
				SourcePlanChangedAt:      planChangedAt,
			})
			if errors.Is(err, errs.ErrAlreadyExists) {
				existing, lookupErr := h.credits.GetByProrationSource(ctx, tenantID, sub.ID, item.ID, planChangedAt)
				if lookupErr != nil {
					return nil, fmt.Errorf("proration credit dedup lookup: %w", lookupErr)
				}
				slog.Info("proration credit already granted; retry dedup",
					"entry_id", existing.ID,
					"subscription_id", sub.ID,
					"item_id", item.ID,
					"plan_changed_at", planChangedAt,
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

			slog.Info("proration credit granted",
				"subscription_id", sub.ID,
				"item_id", item.ID,
				"credit_cents", creditAmount,
				"old_plan", oldPlan.Name,
				"new_plan", newPlan.Name,
			)
		}
	}

	return detail, nil
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
