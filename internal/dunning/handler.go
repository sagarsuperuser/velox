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

	r.Route("/policy", func(r chi.Router) {
		r.Get("/", h.getPolicy)
		r.Put("/", h.upsertPolicy)
	})

	r.Route("/runs", func(r chi.Router) {
		r.Get("/", h.listRuns)
		r.Get("/{id}", h.getRun)
		r.Post("/{id}/resolve", h.resolveRun)
	})

	r.Route("/customers/{customer_id}/override", func(r chi.Router) {
		r.Get("/", h.getCustomerOverride)
		r.Put("/", h.upsertCustomerOverride)
		r.Delete("/", h.deleteCustomerOverride)
	})

	return r
}

func (h *Handler) getPolicy(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	policy, err := h.svc.GetPolicy(r.Context(), tenantID)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "dunning policy")
		return
	}
	if err != nil {
		respond.InternalError(w, r)
		slog.Error("get dunning policy", "error", err)
		return
	}

	respond.JSON(w, r, http.StatusOK, policy)
}

func (h *Handler) upsertPolicy(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	var policy domain.DunningPolicy
	if err := json.NewDecoder(r.Body).Decode(&policy); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}

	result, err := h.svc.UpsertPolicy(r.Context(), tenantID, policy)
	if err != nil {
		respond.FromError(w, r, err, "dunning_policy")
		return
	}

	respond.JSON(w, r, http.StatusOK, result)
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
		slog.Error("list dunning runs", "error", err)
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
		slog.Error("get dunning run", "error", err)
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

func (h *Handler) getCustomerOverride(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	customerID := chi.URLParam(r, "customer_id")

	override, err := h.svc.GetCustomerOverride(r.Context(), tenantID, customerID)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "customer dunning override")
		return
	}
	if err != nil {
		respond.InternalError(w, r)
		slog.Error("get customer dunning override", "error", err)
		return
	}

	respond.JSON(w, r, http.StatusOK, override)
}

func (h *Handler) upsertCustomerOverride(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	customerID := chi.URLParam(r, "customer_id")

	var override domain.CustomerDunningOverride
	if err := json.NewDecoder(r.Body).Decode(&override); err != nil {
		respond.BadRequest(w, r, "invalid JSON body")
		return
	}
	override.CustomerID = customerID

	result, err := h.svc.UpsertCustomerOverride(r.Context(), tenantID, override)
	if err != nil {
		respond.FromError(w, r, err, "customer_dunning_override")
		return
	}

	respond.JSON(w, r, http.StatusOK, result)
}

func (h *Handler) deleteCustomerOverride(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	customerID := chi.URLParam(r, "customer_id")

	err := h.svc.DeleteCustomerOverride(r.Context(), tenantID, customerID)
	if errors.Is(err, errs.ErrNotFound) {
		respond.NotFound(w, r, "customer dunning override")
		return
	}
	if err != nil {
		respond.InternalError(w, r)
		slog.Error("delete customer dunning override", "error", err)
		return
	}

	respond.JSON(w, r, http.StatusOK, map[string]string{"status": "deleted"})
}

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
			now := time.Now().UTC()
			if _, err := h.invoices.MarkPaid(r.Context(), tenantID, run.InvoiceID, "", now); err != nil {
				slog.Warn("failed to mark invoice as paid after dunning resolution", "invoice_id", run.InvoiceID, "error", err)
			}
		case domain.ResolutionManuallyResolved:
			// Full void: status change + credit reversal + PI cancellation
			inv, _ := h.invoices.Get(r.Context(), tenantID, run.InvoiceID)
			if _, err := h.invoices.UpdateStatus(r.Context(), tenantID, run.InvoiceID, domain.InvoiceVoided); err != nil {
				slog.Warn("failed to void invoice after dunning resolution", "invoice_id", run.InvoiceID, "error", err)
			}
			// Reverse credits
			if h.creditReverser != nil && inv.CustomerID != "" {
				if reversed, err := h.creditReverser.ReverseForInvoice(r.Context(), tenantID, inv.CustomerID, run.InvoiceID, inv.InvoiceNumber); err != nil {
					slog.Warn("failed to reverse credits on dunning void", "invoice_id", run.InvoiceID, "error", err)
				} else if reversed > 0 {
					slog.Info("credits reversed on dunning void", "invoice_id", run.InvoiceID, "reversed_cents", reversed)
				}
			}
			// Cancel Stripe PI
			if h.paymentCancel != nil && inv.StripePaymentIntentID != "" {
				if err := h.paymentCancel.CancelPaymentIntent(r.Context(), inv.StripePaymentIntentID); err != nil {
					slog.Warn("failed to cancel PI on dunning void", "invoice_id", run.InvoiceID, "error", err)
				}
			}
		}
	}

	respond.JSON(w, r, http.StatusOK, run)
}
