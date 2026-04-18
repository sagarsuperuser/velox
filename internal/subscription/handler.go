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

// ProrationInvoiceCreator creates finalized proration invoices.
type ProrationInvoiceCreator interface {
	CreateInvoice(ctx context.Context, tenantID string, inv domain.Invoice) (domain.Invoice, error)
	CreateLineItem(ctx context.Context, tenantID string, item domain.InvoiceLineItem) (domain.InvoiceLineItem, error)
}

// ProrationCreditGranter grants credits for downgrade proration.
type ProrationCreditGranter interface {
	Grant(ctx context.Context, tenantID, customerID string, amountCents int64, description string) error
}

type Handler struct {
	svc         *Service
	plans       PlanReader
	invoices    ProrationInvoiceCreator
	credits     ProrationCreditGranter
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

	// Generate proration invoice or credit for immediate plan changes
	if input.Immediate && result.ProrationFactor > 0 && h.plans != nil && h.invoices != nil {
		if prorationResult, err := h.handleProration(r.Context(), tenantID, result, oldPlanID); err != nil {
			slog.Error("proration failed (plan change succeeded)",
				"subscription_id", id,
				"error", err,
			)
		} else if prorationResult != nil {
			result.Proration = prorationResult
		}
	}

	if h.auditLogger != nil {
		_ = h.auditLogger.Log(r.Context(), tenantID, "subscription.plan_changed", "subscription", result.Subscription.ID, map[string]any{
			"customer_id": result.Subscription.CustomerID,
			"old_plan_id": oldPlanID,
			"new_plan_id": input.NewPlanID,
			"immediate":   input.Immediate,
		})
	}

	respond.JSON(w, r, http.StatusOK, result)
}

// handleProration creates a proration invoice (upgrade) or grants credit (downgrade).
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
	proratedCents := int64(math.Round(diff))

	if proratedCents == 0 {
		return nil, nil
	}

	detail := &ProrationDetail{
		OldPlanID:       oldPlanID,
		NewPlanID:       newPlan.ID,
		ProrationFactor: result.ProrationFactor,
		AmountCents:     proratedCents,
	}

	if proratedCents > 0 {
		// Upgrade: create a finalized proration invoice
		now := time.Now().UTC()
		dueAt := now.AddDate(0, 0, 30)
		invoiceNumber := fmt.Sprintf("VLX-PRO-%s-%04d", now.Format("200601"), now.UnixMilli()%10000)

		inv, err := h.invoices.CreateInvoice(ctx, tenantID, domain.Invoice{
			CustomerID:         result.Subscription.CustomerID,
			SubscriptionID:     result.Subscription.ID,
			InvoiceNumber:      invoiceNumber,
			Status:             domain.InvoiceFinalized,
			PaymentStatus:      domain.PaymentPending,
			Currency:           newPlan.Currency,
			SubtotalCents:      proratedCents,
			TotalAmountCents:   proratedCents,
			AmountDueCents:     proratedCents,
			IssuedAt:           &now,
			DueAt:              &dueAt,
			NetPaymentTermDays: 30,
			Memo:               fmt.Sprintf("Plan upgrade proration: %s -> %s", oldPlan.Name, newPlan.Name),
		})
		if err != nil {
			return nil, fmt.Errorf("create proration invoice: %w", err)
		}

		_, err = h.invoices.CreateLineItem(ctx, tenantID, domain.InvoiceLineItem{
			InvoiceID:        inv.ID,
			LineType:         domain.LineTypeBaseFee,
			Description:      fmt.Sprintf("Plan upgrade proration: %s -> %s", oldPlan.Name, newPlan.Name),
			Quantity:         1,
			UnitAmountCents:  proratedCents,
			AmountCents:      proratedCents,
			TotalAmountCents: proratedCents,
			Currency:         newPlan.Currency,
		})
		if err != nil {
			return nil, fmt.Errorf("create proration line item: %w", err)
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
			if err := h.credits.Grant(ctx, tenantID, result.Subscription.CustomerID, creditAmount, desc); err != nil {
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
