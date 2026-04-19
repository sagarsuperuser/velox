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
// CreateInvoiceWithLineItems atomically creates the invoice + its line items
// in one transaction — prevents orphaned invoices on partial failure. If the
// (subscription_id, source_plan_changed_at) dedup index fires, it returns
// errs.ErrAlreadyExists so the caller can look up the existing row via
// GetByProrationSource.
//
// NextInvoiceNumber must atomically allocate a unique, collision-free number
// per tenant — a proration invoice shares the same sequence as regular
// invoices (Stripe's model: one monotonic sequence, memo distinguishes kind).
type ProrationInvoiceCreator interface {
	CreateInvoiceWithLineItems(ctx context.Context, tenantID string, inv domain.Invoice, items []domain.InvoiceLineItem) (domain.Invoice, error)
	GetByProrationSource(ctx context.Context, tenantID, subscriptionID string, planChangedAt time.Time) (domain.Invoice, error)
	NextInvoiceNumber(ctx context.Context, tenantID string) (string, error)
}

// ProrationCreditGranter grants credits for downgrade proration.
// The source fields let the store enforce per-event idempotency — retries of
// the same (subscription, plan_changed_at) return errs.ErrAlreadyExists rather
// than double-crediting the customer; the handler then re-fetches via
// GetByProrationSource.
type ProrationCreditGranter interface {
	GrantProration(ctx context.Context, tenantID string, input ProrationGrantInput) error
	GetByProrationSource(ctx context.Context, tenantID, subscriptionID string, planChangedAt time.Time) (domain.CreditLedgerEntry, error)
}

// ProrationCouponApplier computes a coupon discount against a proration
// invoice's subtotal. Optional — when nil, proration invoices carry a zero
// discount (previous behaviour).
type ProrationCouponApplier interface {
	ApplyToInvoice(ctx context.Context, tenantID, subscriptionID, planID string, subtotalCents int64) (int64, error)
}

// ProrationGrantInput carries the downgrade credit payload plus the
// provenance fields required for dedup.
type ProrationGrantInput struct {
	CustomerID           string
	AmountCents          int64
	Description          string
	SourceSubscriptionID string
	SourcePlanChangedAt  time.Time
}

type Handler struct {
	svc         *Service
	plans       PlanReader
	invoices    ProrationInvoiceCreator
	credits     ProrationCreditGranter
	coupons     ProrationCouponApplier
	auditLogger *audit.Logger
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// SetAuditLogger configures audit logging for financial operations.
func (h *Handler) SetAuditLogger(l *audit.Logger) { h.auditLogger = l }

// SetProrationDeps sets optional dependencies for proration invoice generation.
func (h *Handler) SetProrationDeps(plans PlanReader, invoices ProrationInvoiceCreator, credits ProrationCreditGranter) {
	h.plans = plans
	h.invoices = invoices
	h.credits = credits
}

// SetProrationCouponApplier configures coupon resolution on proration invoices.
// Optional — nil leaves proration invoices undiscounted.
func (h *Handler) SetProrationCouponApplier(c ProrationCouponApplier) {
	h.coupons = c
}

func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/", h.create)
	r.Get("/", h.list)
	r.Get("/{id}", h.get)
	r.Post("/{id}/activate", h.activate)
	r.Post("/{id}/pause", h.pause)
	r.Post("/{id}/resume", h.resume)
	r.Post("/{id}/change-plan", h.changePlan)
	r.Post("/{id}/cancel", h.cancel)
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
	respond.JSON(w, r, http.StatusOK, sub)
}

func (h *Handler) changePlan(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	var input ChangePlanInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	// Read old plan ID before ChangePlan mutates the subscription
	oldPlanID := ""
	if input.Immediate && h.plans != nil {
		sub, err := h.svc.Get(r.Context(), tenantID, id)
		if err == nil {
			oldPlanID = sub.PlanID
		}
	}

	result, err := h.svc.ChangePlan(r.Context(), tenantID, id, input)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "subscription")
		return
	}
	if err != nil {
		respond.Validation(w, r, err.Error())
		return
	}

	// Audit the plan change first — it is committed regardless of what happens
	// to the proration step below, so the audit trail must reflect that truth.
	if h.auditLogger != nil {
		_ = h.auditLogger.Log(r.Context(), tenantID, "subscription.plan_changed", "subscription", result.Subscription.ID, map[string]any{
			"customer_id": result.Subscription.CustomerID,
			"old_plan_id": oldPlanID,
			"new_plan_id": input.NewPlanID,
			"immediate":   input.Immediate,
		})
	}

	// Generate proration invoice or credit for immediate plan changes.
	//
	// NOTE on non-atomicity: the plan change has already committed above. If
	// the proration step below fails, the subscription is on the new plan but
	// no invoice/credit exists yet. Previously this error was swallowed and
	// the client got 200 OK — customers ended up with free upgrades (no
	// proration invoice) or missing credits (no downgrade credit).
	//
	// We now return 500 with a distinct error code so clients and operators
	// can distinguish this partial-success case from a total failure. Proper
	// atomicity (cross-domain tx or outbox) is tracked as a follow-up; the
	// plan change is durable, and operators can reconcile via logs until then.
	if input.Immediate && result.ProrationFactor > 0 && h.plans != nil && h.invoices != nil {
		prorationResult, prorationErr := h.handleProration(r.Context(), tenantID, result, oldPlanID)
		if prorationErr != nil {
			slog.Error("proration failed after plan change committed",
				"subscription_id", id,
				"tenant_id", tenantID,
				"customer_id", result.Subscription.CustomerID,
				"old_plan_id", oldPlanID,
				"new_plan_id", input.NewPlanID,
				"proration_factor", result.ProrationFactor,
				"plan_changed_at", result.Subscription.PlanChangedAt,
				"error", prorationErr,
			)
			if h.auditLogger != nil {
				_ = h.auditLogger.Log(r.Context(), tenantID, "subscription.proration_failed", "subscription", result.Subscription.ID, map[string]any{
					"customer_id":      result.Subscription.CustomerID,
					"old_plan_id":      oldPlanID,
					"new_plan_id":      input.NewPlanID,
					"proration_factor": result.ProrationFactor,
					"error":            prorationErr.Error(),
				})
			}
			respond.Error(w, r, http.StatusInternalServerError, "api_error", "proration_failed",
				"plan change succeeded but proration generation failed — subscription is on the new plan; retry or contact support to reconcile")
			return
		}
		if prorationResult != nil {
			result.Proration = prorationResult
		}
	}

	respond.JSON(w, r, http.StatusOK, result)
}

// handleProration creates a proration invoice (upgrade) or grants credit
// (downgrade). Idempotency is enforced at the store layer via the
// (subscription_id, plan_changed_at) natural key — if this function is
// retried after a partial failure, the existing artifact is returned instead
// of a duplicate being written.
func (h *Handler) handleProration(ctx context.Context, tenantID string, result ChangePlanResult, oldPlanID string) (*ProrationDetail, error) {
	oldPlan, err := h.plans.GetPlan(ctx, tenantID, oldPlanID)
	if err != nil {
		return nil, fmt.Errorf("get old plan: %w", err)
	}
	newPlan, err := h.plans.GetPlan(ctx, tenantID, result.Subscription.PlanID)
	if err != nil {
		return nil, fmt.Errorf("get new plan: %w", err)
	}

	diff := float64(newPlan.BaseAmountCents-oldPlan.BaseAmountCents) * result.ProrationFactor
	proratedCents := int64(math.RoundToEven(diff))

	if proratedCents == 0 {
		return nil, nil
	}

	// PlanChangedAt is the natural key for proration dedup. ChangePlan always
	// sets this, but guard defensively — without it, we cannot safely retry.
	if result.Subscription.PlanChangedAt == nil {
		return nil, fmt.Errorf("subscription missing plan_changed_at; cannot generate proration safely")
	}
	planChangedAt := *result.Subscription.PlanChangedAt

	detail := &ProrationDetail{
		OldPlanID:       oldPlanID,
		NewPlanID:       newPlan.ID,
		ProrationFactor: result.ProrationFactor,
		AmountCents:     proratedCents,
	}

	if proratedCents > 0 {
		// Upgrade: create a finalized proration invoice with its line item in
		// one transaction.
		now := time.Now().UTC()
		dueAt := now.AddDate(0, 0, 30)

		// BillingPeriodStart = plan change moment, BillingPeriodEnd = remaining
		// cycle end. Matches Stripe's proration semantics and gives the
		// existing billing-period idempotency index a meaningful tuple to work
		// with (vs the zero-value period used previously).
		periodStart := planChangedAt
		var periodEnd time.Time
		if result.Subscription.CurrentBillingPeriodEnd != nil {
			periodEnd = *result.Subscription.CurrentBillingPeriodEnd
		} else {
			periodEnd = planChangedAt
		}

		// Apply coupon discount before recording totals — same Stripe-style
		// order (subtotal → discount → total) the main billing path uses.
		// Proration is tax-free today; COR-2 will fold tax into this block.
		var discountCents int64
		if h.coupons != nil {
			d, err := h.coupons.ApplyToInvoice(ctx, tenantID, result.Subscription.ID, result.Subscription.PlanID, proratedCents)
			if err != nil {
				slog.Warn("coupon apply failed on proration, proceeding without discount",
					"error", err, "subscription_id", result.Subscription.ID)
			} else {
				discountCents = d
			}
		}
		netProrated := proratedCents - discountCents

		memo := fmt.Sprintf("Plan upgrade proration: %s -> %s", oldPlan.Name, newPlan.Name)
		invoice := domain.Invoice{
			CustomerID:          result.Subscription.CustomerID,
			SubscriptionID:      result.Subscription.ID,
			Status:              domain.InvoiceFinalized,
			PaymentStatus:       domain.PaymentPending,
			Currency:            newPlan.Currency,
			SubtotalCents:       proratedCents,
			DiscountCents:       discountCents,
			TotalAmountCents:    netProrated,
			AmountDueCents:      netProrated,
			BillingPeriodStart:  periodStart,
			BillingPeriodEnd:    periodEnd,
			IssuedAt:            &now,
			DueAt:               &dueAt,
			NetPaymentTermDays:  30,
			Memo:                memo,
			SourcePlanChangedAt: &planChangedAt,
		}
		lineItem := domain.InvoiceLineItem{
			LineType:         domain.LineTypeBaseFee,
			Description:      memo,
			Quantity:         1,
			UnitAmountCents:  proratedCents,
			AmountCents:      proratedCents,
			TotalAmountCents: proratedCents,
			Currency:         newPlan.Currency,
		}

		// Allocate the invoice number lazily — only if we're actually going to
		// insert. On dedup hit, the existing invoice already has its number
		// and we skip this allocation entirely.
		invoiceNumber, err := h.invoices.NextInvoiceNumber(ctx, tenantID)
		if err != nil {
			return nil, fmt.Errorf("allocate proration invoice number: %w", err)
		}
		invoice.InvoiceNumber = invoiceNumber

		inv, err := h.invoices.CreateInvoiceWithLineItems(ctx, tenantID, invoice, []domain.InvoiceLineItem{lineItem})
		if errors.Is(err, errs.ErrAlreadyExists) {
			existing, lookupErr := h.invoices.GetByProrationSource(ctx, tenantID, result.Subscription.ID, planChangedAt)
			if lookupErr != nil {
				return nil, fmt.Errorf("proration dedup lookup: %w", lookupErr)
			}
			slog.Info("proration invoice already exists; retry dedup",
				"invoice_id", existing.ID,
				"subscription_id", result.Subscription.ID,
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

		slog.Info("proration invoice created",
			"invoice_id", inv.ID,
			"subscription_id", result.Subscription.ID,
			"amount_cents", proratedCents,
			"old_plan", oldPlan.Name,
			"new_plan", newPlan.Name,
		)
	} else {
		// Downgrade: grant credit for the difference
		creditAmount := -proratedCents // Make positive
		if h.credits != nil {
			desc := fmt.Sprintf("Plan downgrade proration: %s -> %s", oldPlan.Name, newPlan.Name)
			err := h.credits.GrantProration(ctx, tenantID, ProrationGrantInput{
				CustomerID:           result.Subscription.CustomerID,
				AmountCents:          creditAmount,
				Description:          desc,
				SourceSubscriptionID: result.Subscription.ID,
				SourcePlanChangedAt:  planChangedAt,
			})
			if errors.Is(err, errs.ErrAlreadyExists) {
				existing, lookupErr := h.credits.GetByProrationSource(ctx, tenantID, result.Subscription.ID, planChangedAt)
				if lookupErr != nil {
					return nil, fmt.Errorf("proration credit dedup lookup: %w", lookupErr)
				}
				slog.Info("proration credit already granted; retry dedup",
					"entry_id", existing.ID,
					"subscription_id", result.Subscription.ID,
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
				"subscription_id", result.Subscription.ID,
				"credit_cents", creditAmount,
				"old_plan", oldPlan.Name,
				"new_plan", newPlan.Name,
			)
		}
	}

	return detail, nil
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
		_ = h.auditLogger.Log(r.Context(), tenantID, domain.AuditActionCancel, "subscription", sub.ID, map[string]any{
			"customer_id": sub.CustomerID,
			"plan_id":     sub.PlanID,
		})
	}

	respond.JSON(w, r, http.StatusOK, sub)
}
