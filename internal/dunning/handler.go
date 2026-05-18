package dunning

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/clock"
)

// InvoiceUpdater updates invoice status when dunning is resolved.
type InvoiceUpdater interface {
	UpdateStatus(ctx context.Context, tenantID, id string, status domain.InvoiceStatus) (domain.Invoice, error)
	UpdatePayment(ctx context.Context, tenantID, id string, paymentStatus domain.InvoicePaymentStatus, stripePaymentIntentID, lastPaymentError string, paidAt *time.Time) (domain.Invoice, error)
	MarkPaid(ctx context.Context, tenantID, id string, stripePaymentIntentID string, paidAt time.Time) (domain.Invoice, error)
	Get(ctx context.Context, tenantID, id string) (domain.Invoice, error)
}

// CreditReverser reverses credits when an invoice is voided via dunning.
type CreditReverser interface {
	ReverseForInvoice(ctx context.Context, tenantID, customerID, invoiceID, invoiceNumber string) (int64, error)
}

// PaymentCanceler cancels Stripe PaymentIntent when invoice is voided via dunning.
type PaymentCanceler interface {
	CancelPaymentIntent(ctx context.Context, paymentIntentID string) error
}

type Handler struct {
	svc            *Service
	invoices       InvoiceUpdater
	creditReverser CreditReverser
	paymentCancel  PaymentCanceler
}

type HandlerDeps struct {
	Invoices       InvoiceUpdater
	CreditReverser CreditReverser
	PaymentCancel  PaymentCanceler
}

func NewHandler(svc *Service, deps ...HandlerDeps) *Handler {
	h := &Handler{svc: svc}
	if len(deps) > 0 {
		h.invoices = deps[0].Invoices
		h.creditReverser = deps[0].CreditReverser
		h.paymentCancel = deps[0].PaymentCancel
	}
	return h
}

func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()

	// Dunning policies (ADR-036 campaigns model — multi-policy-per-
	// tenant). Replaces the prior singleton /policy + per-customer
	// /customers/{id}/override surface. Customers are reassigned via
	// PATCH /v1/customers/{id} { "dunning_policy_id": ... } on the
	// customer handler.
	r.Route("/policies", func(r chi.Router) {
		r.Get("/", h.listPolicies)
		r.Post("/", h.createPolicy)
		r.Get("/{id}", h.getPolicy)
		r.Patch("/{id}", h.updatePolicy)
		r.Delete("/{id}", h.deletePolicy)
		r.Post("/{id}/set-default", h.setDefaultPolicy)
	})

	r.Route("/runs", func(r chi.Router) {
		r.Get("/", h.listRuns)
		r.Get("/{id}", h.getRun)
		r.Post("/{id}/resolve", h.resolveRun)
	})

	return r
}

func (h *Handler) listPolicies(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	policies, err := h.svc.ListPolicies(r.Context(), tenantID)
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "list dunning policies", "error", err)
		return
	}
	if policies == nil {
		policies = []domain.DunningPolicy{}
	}
	// Attach customer-assignment counts so the admin page can render
	// the "N customers assigned" badge without a round-trip per row.
	type policyWithCount struct {
		domain.DunningPolicy
		AssignedCustomers int `json:"assigned_customers"`
	}
	out := make([]policyWithCount, 0, len(policies))
	for _, p := range policies {
		count, _ := h.svc.CountCustomersOnPolicy(r.Context(), tenantID, p.ID)
		out = append(out, policyWithCount{DunningPolicy: p, AssignedCustomers: count})
	}
	respond.JSON(w, r, http.StatusOK, map[string]any{"data": out})
}

func (h *Handler) getPolicy(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")
	policy, err := h.svc.GetPolicyByID(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "dunning_policy")
		return
	}
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "get dunning policy", "id", id, "error", err)
		return
	}
	respond.JSON(w, r, http.StatusOK, policy)
}

func (h *Handler) createPolicy(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	var policy domain.DunningPolicy
	if err := json.NewDecoder(r.Body).Decode(&policy); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}
	policy.ID = "" // server-assigned
	result, err := h.svc.UpsertPolicy(r.Context(), tenantID, policy)
	if err != nil {
		respond.FromError(w, r, err, "dunning_policy")
		return
	}
	respond.JSON(w, r, http.StatusCreated, result)
}

func (h *Handler) updatePolicy(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")
	var policy domain.DunningPolicy
	if err := json.NewDecoder(r.Body).Decode(&policy); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}
	policy.ID = id
	result, err := h.svc.UpsertPolicy(r.Context(), tenantID, policy)
	if err != nil {
		respond.FromError(w, r, err, "dunning_policy")
		return
	}
	respond.JSON(w, r, http.StatusOK, result)
}

func (h *Handler) deletePolicy(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")
	err := h.svc.DeletePolicy(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "dunning_policy")
		return
	}
	if err != nil {
		respond.FromError(w, r, err, "dunning_policy")
		return
	}
	respond.JSON(w, r, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *Handler) setDefaultPolicy(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")
	if err := h.svc.SetDefaultPolicy(r.Context(), tenantID, id); err != nil {
		if errors.Is(err, errs.ErrNotFound) {
			respond.NotFound(w, r, "dunning_policy")
			return
		}
		respond.FromError(w, r, err, "dunning_policy")
		return
	}
	respond.JSON(w, r, http.StatusOK, map[string]string{"status": "default_updated"})
}

func (h *Handler) listRuns(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	runs, total, err := h.svc.ListRuns(r.Context(), RunListFilter{
		TenantID:  tenantID,
		InvoiceID: r.URL.Query().Get("invoice_id"),
		State:     r.URL.Query().Get("state"),
		Limit:     limit,
		Offset:    offset,
	})
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "list dunning runs", "error", err)
		return
	}
	if runs == nil {
		runs = []domain.InvoiceDunningRun{}
	}

	respond.List(w, r, runs, total)
}

func (h *Handler) getRun(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	run, err := h.svc.store.GetRun(r.Context(), tenantID, id)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "dunning run")
		return
	}
	if err != nil {
		respond.InternalError(w, r)
		slog.ErrorContext(r.Context(), "get dunning run", "error", err)
		return
	}

	events, _ := h.svc.store.ListEvents(r.Context(), tenantID, id)
	if events == nil {
		events = []domain.InvoiceDunningEvent{}
	}

	respond.JSON(w, r, http.StatusOK, map[string]any{
		"run":    run,
		"events": events,
	})
}

// Customer override handlers (GetCustomerOverride / UpsertCustomerOverride
// / DeleteCustomerOverride) were removed in ADR-036. Per-customer
// differentiation is now expressed as `customers.dunning_policy_id`
// assignment; mutation goes through PATCH /v1/customers/{id} on the
// customer handler.

type resolveInput struct {
	Resolution string `json:"resolution"`
}

func (h *Handler) resolveRun(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	var input resolveInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	run, err := h.svc.ResolveRun(r.Context(), tenantID, id, domain.DunningResolution(input.Resolution))
	if err != nil {
		respond.FromError(w, r, err, "dunning_run")
		return
	}

	// Propagate resolution to invoice
	if h.invoices != nil && run.InvoiceID != "" {
		switch domain.DunningResolution(input.Resolution) {
		case domain.ResolutionPaymentRecovered:
			// MarkPaid stamps invoice.paid_at — for invoices on
			// clock-pinned subs that ought to land in sim-time
			// (ADR-030). clock.Now(ctx) falls back to wall-clock
			// for unbound ctx (production dunning resolutions).
			now := clock.Now(r.Context())
			if _, err := h.invoices.MarkPaid(r.Context(), tenantID, run.InvoiceID, "", now); err != nil {
				slog.WarnContext(r.Context(), "failed to mark invoice as paid after dunning resolution", "invoice_id", run.InvoiceID, "error", err)
			}
		case domain.ResolutionManuallyResolved:
			// Full void: status change + credit reversal + PI cancellation
			inv, _ := h.invoices.Get(r.Context(), tenantID, run.InvoiceID)
			if _, err := h.invoices.UpdateStatus(r.Context(), tenantID, run.InvoiceID, domain.InvoiceVoided); err != nil {
				slog.WarnContext(r.Context(), "failed to void invoice after dunning resolution", "invoice_id", run.InvoiceID, "error", err)
			}
			// Reverse credits
			if h.creditReverser != nil && inv.CustomerID != "" {
				if reversed, err := h.creditReverser.ReverseForInvoice(r.Context(), tenantID, inv.CustomerID, run.InvoiceID, inv.InvoiceNumber); err != nil {
					slog.WarnContext(r.Context(), "failed to reverse credits on dunning void", "invoice_id", run.InvoiceID, "error", err)
				} else if reversed > 0 {
					slog.InfoContext(r.Context(), "credits reversed on dunning void", "invoice_id", run.InvoiceID, "reversed_cents", reversed)
				}
			}
			// Cancel Stripe PI
			if h.paymentCancel != nil && inv.StripePaymentIntentID != "" {
				if err := h.paymentCancel.CancelPaymentIntent(r.Context(), inv.StripePaymentIntentID); err != nil {
					slog.WarnContext(r.Context(), "failed to cancel PI on dunning void", "invoice_id", run.InvoiceID, "error", err)
				}
			}
		}
	}

	respond.JSON(w, r, http.StatusOK, run)
}
