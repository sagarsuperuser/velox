package invoice

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/clock"
)

// InvoiceNumberer allocates the next invoice number for a tenant.
// Atomicity and uniqueness are the numberer's responsibility.
type InvoiceNumberer interface {
	NextInvoiceNumber(ctx context.Context, tenantID string) (string, error)
}

// TaxCommitter finalizes an upstream tax calculation into a tax_transaction
// (Stripe Tax) at invoice finalize time. Optional: when nil, finalize proceeds
// without a tax commit — fine for manual/none providers or when the invoice
// has no CalculationID. Failures here do NOT block finalize; they only get
// logged, because invoice finalize must remain idempotent and a transient
// Stripe error shouldn't leave the invoice stuck in draft.
type TaxCommitter interface {
	CommitTax(ctx context.Context, tenantID, invoiceID, calculationID string) error
}

// CouponApplier is the narrow view into coupon+tax+invoice orchestration
// the apply-coupon-to-draft-invoice endpoint depends on. Satisfied by
// billing.Engine in production. Lives here (not in the coupon package) so
// the invoice domain owns the surface it calls — coupon can't import
// invoice (peer-import rule), and invoice can't import billing, so the
// shared contract lives right where the handler consumes it.
type CouponApplier interface {
	ApplyCouponToInvoice(ctx context.Context, tenantID, invoiceID, code, idempotencyKey string) (domain.Invoice, error)
}

type Service struct {
	store         Store
	clock         clock.Clock
	numberer      InvoiceNumberer
	taxCommitter  TaxCommitter
	couponApplier CouponApplier
}

func NewService(store Store, clk clock.Clock, numberer InvoiceNumberer) *Service {
	if clk == nil {
		clk = clock.Real()
	}
	return &Service{store: store, clock: clk, numberer: numberer}
}

// SetTaxCommitter wires the upstream tax-transaction committer. Called from
// router.go with the billing engine so finalize can commit Stripe tax calcs.
func (s *Service) SetTaxCommitter(tc TaxCommitter) {
	s.taxCommitter = tc
}

// SetCouponApplier wires the orchestrator behind the apply-coupon endpoint.
// Production passes billing.Engine; tests can pass any implementation.
func (s *Service) SetCouponApplier(c CouponApplier) {
	s.couponApplier = c
}

type CreateInput struct {
	CustomerID         string    `json:"customer_id"`
	SubscriptionID     string    `json:"subscription_id"`
	Currency           string    `json:"currency"`
	BillingPeriodStart time.Time `json:"billing_period_start"`
	BillingPeriodEnd   time.Time `json:"billing_period_end"`
	NetPaymentTermDays int       `json:"net_payment_term_days"`
	Memo               string    `json:"memo,omitempty"`
}

func (s *Service) Create(ctx context.Context, tenantID string, input CreateInput) (domain.Invoice, error) {
	if input.CustomerID == "" {
		return domain.Invoice{}, errs.Required("customer_id")
	}
	if input.SubscriptionID == "" {
		return domain.Invoice{}, errs.Required("subscription_id")
	}

	currency := strings.ToUpper(strings.TrimSpace(input.Currency))
	if currency == "" {
		currency = "USD"
	}

	netDays := input.NetPaymentTermDays
	if netDays <= 0 {
		netDays = 30
	}

	now := s.clock.Now()
	invoiceNumber, err := s.numberer.NextInvoiceNumber(ctx, tenantID)
	if err != nil {
		return domain.Invoice{}, fmt.Errorf("allocate invoice number: %w", err)
	}
	issuedAt := now
	dueAt := now.AddDate(0, 0, netDays)

	return s.store.Create(ctx, tenantID, domain.Invoice{
		CustomerID:         input.CustomerID,
		SubscriptionID:     input.SubscriptionID,
		InvoiceNumber:      invoiceNumber,
		Status:             domain.InvoiceDraft,
		PaymentStatus:      domain.PaymentPending,
		Currency:           currency,
		BillingPeriodStart: input.BillingPeriodStart,
		BillingPeriodEnd:   input.BillingPeriodEnd,
		IssuedAt:           &issuedAt,
		DueAt:              &dueAt,
		NetPaymentTermDays: netDays,
		Memo:               strings.TrimSpace(input.Memo),
	})
}

func (s *Service) Get(ctx context.Context, tenantID, id string) (domain.Invoice, error) {
	return s.store.Get(ctx, tenantID, id)
}

// HasSucceededInvoice is the implementation of coupon.CustomerHistoryLookup.
// Lives on the invoice service so the coupon package doesn't import invoice
// directly — the concrete dependency is injected at assembly time via
// coupon.Service.SetCustomerHistoryLookup.
func (s *Service) HasSucceededInvoice(ctx context.Context, tenantID, customerID string) (bool, error) {
	return s.store.HasSucceededInvoice(ctx, tenantID, customerID)
}

func (s *Service) List(ctx context.Context, filter ListFilter) ([]domain.Invoice, int, error) {
	return s.store.List(ctx, filter)
}

func (s *Service) Finalize(ctx context.Context, tenantID, id string) (domain.Invoice, error) {
	inv, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return domain.Invoice{}, err
	}
	if inv.Status != domain.InvoiceDraft {
		return domain.Invoice{}, errs.InvalidState(fmt.Sprintf("can only finalize draft invoices, current status: %s", inv.Status))
	}
	// Block finalize while tax is unresolved. Sending an invoice with
	// wrong or missing tax creates compliance exposure; we defer until the
	// retry worker lifts the block (TaxStatus=ok) or an operator resolves
	// a failed calculation manually.
	switch inv.TaxStatus {
	case domain.InvoiceTaxPending:
		return domain.Invoice{}, errs.InvalidState("tax calculation pending — retry in progress, finalize blocked")
	case domain.InvoiceTaxFailed:
		return domain.Invoice{}, errs.InvalidState("tax calculation failed after retries — operator intervention required")
	}
	finalized, err := s.store.UpdateStatus(ctx, tenantID, id, domain.InvoiceFinalized)
	if err != nil {
		return domain.Invoice{}, err
	}
	// Generate the hosted-invoice-URL token. Non-fatal if it fails — mirrors
	// the tax-commit convention below: the invoice is already authoritative
	// after UpdateStatus, so a transient DB hiccup on a side-effect doesn't
	// unwind the state transition. Operators can repair via the rotate
	// endpoint. Happens BEFORE tax commit because the rotate endpoint will
	// anyway need to talk to Stripe-less code paths, and token generation
	// is pure Go with no external dependency.
	if token, tokenErr := GeneratePublicToken(); tokenErr != nil {
		slog.Warn("invoice: public token generation failed at finalize",
			"error", tokenErr, "tenant_id", tenantID, "invoice_id", finalized.ID)
	} else if err := s.store.SetPublicToken(ctx, tenantID, id, token); err != nil {
		slog.Warn("invoice: public token persist failed at finalize",
			"error", err, "tenant_id", tenantID, "invoice_id", finalized.ID)
	} else {
		finalized.PublicToken = token
	}
	// Commit Stripe Tax calculation to a tax_transaction at finalize. Missing
	// calculation id = manual/none provider — skip silently. Commit failure
	// does not unwind finalize: the invoice is already authoritative; we log
	// and continue so the customer-facing state stays consistent.
	if s.taxCommitter != nil && finalized.TaxCalculationID != "" {
		if err := s.taxCommitter.CommitTax(ctx, tenantID, finalized.ID, finalized.TaxCalculationID); err != nil {
			slog.Warn("invoice: tax commit failed at finalize",
				"error", err, "tenant_id", tenantID, "invoice_id", finalized.ID,
				"tax_provider", finalized.TaxProvider, "calculation_id", finalized.TaxCalculationID)
		}
	}
	return finalized, nil
}

func (s *Service) Void(ctx context.Context, tenantID, id string) (domain.Invoice, error) {
	inv, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return domain.Invoice{}, err
	}
	if inv.Status == domain.InvoicePaid {
		return domain.Invoice{}, errs.InvalidState("cannot void a paid invoice — issue a credit note instead")
	}
	if inv.Status == domain.InvoiceVoided {
		return domain.Invoice{}, errs.InvalidState("invoice is already voided")
	}
	return s.store.UpdateStatus(ctx, tenantID, id, domain.InvoiceVoided)
}

func (s *Service) RecordPayment(ctx context.Context, tenantID, id string, stripePaymentIntentID string) (domain.Invoice, error) {
	now := s.clock.Now()
	return s.store.UpdatePayment(ctx, tenantID, id, domain.PaymentSucceeded, stripePaymentIntentID, "", &now)
}

func (s *Service) RecordPaymentFailure(ctx context.Context, tenantID, id, stripePaymentIntentID, errorMessage string) (domain.Invoice, error) {
	return s.store.UpdatePayment(ctx, tenantID, id, domain.PaymentFailed, stripePaymentIntentID, errorMessage, nil)
}

func (s *Service) GetWithLineItems(ctx context.Context, tenantID, id string) (domain.Invoice, []domain.InvoiceLineItem, error) {
	inv, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return domain.Invoice{}, nil, err
	}
	items, err := s.store.ListLineItems(ctx, tenantID, id)
	if err != nil {
		return domain.Invoice{}, nil, err
	}
	return inv, items, nil
}

// GetByPublicToken resolves a hosted-invoice-URL token to the invoice.
// Exposed on the service so the public hosted-invoice handler can look
// up the invoice (and hence the owning tenant) before any tenant context
// is available. Thin forward to the store — the store method is the one
// that uses TxBypass.
func (s *Service) GetByPublicToken(ctx context.Context, token string) (domain.Invoice, error) {
	return s.store.GetByPublicToken(ctx, token)
}

// SetPublicToken persists a rotated public_token on a non-draft invoice.
// Exposed on the service so the operator rotate-public-token endpoint
// (T0-17.4) can delegate without reaching past the service boundary.
func (s *Service) SetPublicToken(ctx context.Context, tenantID, invoiceID, token string) error {
	return s.store.SetPublicToken(ctx, tenantID, invoiceID, token)
}

type AddLineItemInput struct {
	Description     string `json:"description"`
	LineType        string `json:"line_type"`
	Quantity        int64  `json:"quantity"`
	UnitAmountCents int64  `json:"unit_amount_cents"`
}

func (s *Service) AddLineItem(ctx context.Context, tenantID, invoiceID string, input AddLineItemInput) (domain.InvoiceLineItem, error) {
	desc := strings.TrimSpace(input.Description)
	if desc == "" {
		return domain.InvoiceLineItem{}, errs.Required("description")
	}
	if input.Quantity <= 0 {
		return domain.InvoiceLineItem{}, errs.Invalid("quantity", "must be greater than 0")
	}
	if input.UnitAmountCents <= 0 {
		return domain.InvoiceLineItem{}, errs.Invalid("unit_amount_cents", "must be greater than 0")
	}

	lineType := strings.TrimSpace(input.LineType)
	if lineType == "" {
		lineType = "manual"
	}

	amountCents := input.Quantity * input.UnitAmountCents

	item, _, err := s.store.AddLineItemAtomic(ctx, tenantID, invoiceID, domain.InvoiceLineItem{
		LineType:         domain.InvoiceLineItemType(lineType),
		Description:      desc,
		Quantity:         input.Quantity,
		UnitAmountCents:  input.UnitAmountCents,
		AmountCents:      amountCents,
		TotalAmountCents: amountCents,
	})
	return item, err
}

func (s *Service) ListApproachingDue(ctx context.Context, daysBeforeDue int) ([]domain.Invoice, error) {
	return s.store.ListApproachingDue(ctx, daysBeforeDue)
}

// ApplyCoupon routes an operator-initiated coupon apply against an
// already-issued draft invoice through the billing engine, which owns the
// redeem → tax recompute → atomic persist → mark-periods orchestration.
// Handlers call this so the HTTP surface stays tied to the invoice
// resource even though the engine does the heavy lifting.
func (s *Service) ApplyCoupon(ctx context.Context, tenantID, invoiceID, code, idempotencyKey string) (domain.Invoice, error) {
	code = strings.TrimSpace(strings.ToUpper(code))
	if code == "" {
		return domain.Invoice{}, errs.Required("code")
	}
	if s.couponApplier == nil {
		return domain.Invoice{}, errs.InvalidState("coupon application is not configured")
	}
	return s.couponApplier.ApplyCouponToInvoice(ctx, tenantID, invoiceID, code, idempotencyKey)
}
