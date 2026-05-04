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
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
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

// TaxRetrier is the narrow view into tax recompute + persistence the
// retry-tax endpoint depends on. Satisfied by billing.Engine in
// production. Same rationale as CouponApplier — the contract lives
// next to the handler that calls it.
type TaxRetrier interface {
	RetryTaxForInvoice(ctx context.Context, tenantID, invoiceID string) (domain.Invoice, error)
}

// PaymentMethodReader is the narrow lookup the attention classifier
// uses to decide between no_payment_method (operator-actionable) and
// awaiting_payment (engine race window). Satisfied by the existing
// payment.PaymentSetupStore — kept as a local interface so this
// package doesn't import internal/payment.
type PaymentMethodReader interface {
	GetPaymentSetup(ctx context.Context, tenantID, customerID string) (domain.CustomerPaymentSetup, error)
}

type Service struct {
	store          Store
	clock          clock.Clock
	numberer       InvoiceNumberer
	taxCommitter   TaxCommitter
	couponApplier  CouponApplier
	taxRetrier     TaxRetrier
	paymentMethods PaymentMethodReader
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

// SetTaxRetrier wires the orchestrator behind the retry-tax endpoint.
// Production passes billing.Engine; tests can pass any implementation.
func (s *Service) SetTaxRetrier(r TaxRetrier) {
	s.taxRetrier = r
}

// SetPaymentMethodReader wires the customer payment-setup lookup used
// by the attention classifier to surface no_payment_method distinctly
// from generic awaiting_payment. Optional — when nil, the classifier
// falls back to the awaiting_payment branch (less specific but still
// correct for healthy/transient cases).
func (s *Service) SetPaymentMethodReader(r PaymentMethodReader) {
	s.paymentMethods = r
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
	// SubscriptionID is OPTIONAL: cycle invoices carry a subscription, one-off
	// invoices (operator composer, ad-hoc charges) do not. The DB column is
	// nullable as of migration 0060; the cycle-idempotency partial unique
	// index ignores NULLs so two one-off invoices can coexist for the same
	// (customer, period) without colliding.

	currency := strings.ToUpper(strings.TrimSpace(input.Currency))
	if currency == "" {
		currency = "USD"
	}

	netDays := input.NetPaymentTermDays
	if netDays <= 0 {
		netDays = 30
	}

	now := s.clock.Now()
	// One-off invoices that omit a billing window default to "now → now" so
	// the NOT NULL period columns get sane values. Cycle invoices always
	// pass an explicit window from the engine.
	periodStart := input.BillingPeriodStart
	periodEnd := input.BillingPeriodEnd
	if periodStart.IsZero() {
		periodStart = now
	}
	if periodEnd.IsZero() {
		periodEnd = now
	}

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
		BillingPeriodStart: periodStart,
		BillingPeriodEnd:   periodEnd,
		IssuedAt:           &issuedAt,
		DueAt:              &dueAt,
		NetPaymentTermDays: netDays,
		Memo:               strings.TrimSpace(input.Memo),
	})
}

func (s *Service) Get(ctx context.Context, tenantID, id string) (domain.Invoice, error) {
	inv, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return domain.Invoice{}, err
	}
	return s.attachAttention(ctx, inv), nil
}

// attachAttention computes the unified Attention surface from durable
// invoice fields plus the customer's payment-method status. Internal
// callers that read straight from the store (engine, scheduler,
// finalize path) skip this; only user-facing reads see the Attention
// field. Keeps the derivation single-source so handlers, webhook
// serializers, and hosted-invoice rendering all see the same shape.
//
// PaymentMethodReader is read at attach-time, not on the hot path —
// dashboard reads dominate this code, and a PM lookup per invoice is
// cheap. When the reader isn't wired (older test fixtures, isolated
// unit tests), HasPaymentMethod defaults to false → no_payment_method
// shows up — but that's safe: tests that exercise specific reasons
// override the field anyway.
func (s *Service) attachAttention(ctx context.Context, inv domain.Invoice) domain.Invoice {
	atc := domain.AttentionContext{}
	if s.paymentMethods != nil && inv.CustomerID != "" {
		ps, err := s.paymentMethods.GetPaymentSetup(ctx, inv.TenantID, inv.CustomerID)
		if err == nil && ps.SetupStatus == domain.PaymentSetupReady && ps.StripeCustomerID != "" {
			atc.HasPaymentMethod = true
		}
	}
	inv.Attention = domain.ClassifyInvoiceAttention(inv, atc)
	return inv
}

// HasSucceededInvoice is the implementation of coupon.CustomerHistoryLookup.
// Lives on the invoice service so the coupon package doesn't import invoice
// directly — the concrete dependency is injected at assembly time via
// coupon.Service.SetCustomerHistoryLookup.
func (s *Service) HasSucceededInvoice(ctx context.Context, tenantID, customerID string) (bool, error) {
	return s.store.HasSucceededInvoice(ctx, tenantID, customerID)
}

func (s *Service) List(ctx context.Context, filter ListFilter) ([]domain.Invoice, int, error) {
	invs, total, err := s.store.List(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	for i := range invs {
		invs[i] = s.attachAttention(ctx, invs[i])
	}
	return invs, total, nil
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
	return s.attachAttention(ctx, inv), items, nil
}

// GetByPublicToken resolves a hosted-invoice-URL token to the invoice and
// its livemode. Exposed on the service so the public hosted-invoice
// handler can look up the invoice (and hence the owning tenant + mode)
// before any tenant context is available. Thin forward to the store —
// the store method is the one that uses TxBypass. The livemode return
// is what the handler uses to pin postgres.WithLivemode on the request
// context for every downstream RLS-scoped read.
func (s *Service) GetByPublicToken(ctx context.Context, token string) (domain.Invoice, bool, error) {
	inv, livemode, err := s.store.GetByPublicToken(ctx, token)
	if err != nil {
		return domain.Invoice{}, false, err
	}
	// Pin livemode on the ctx before attachAttention. attachAttention
	// reads payment_setups under TxTenant; without the pin it would
	// default to live and either return a stale-mode row or trip the
	// "livemode propagation missing" WARN. The hosted-invoice handler
	// pins again on the request ctx for its own downstream reads, but
	// service-level pinning here keeps callers (and the attention
	// classification specifically) honest regardless of who's calling.
	ctx = postgres.WithLivemode(ctx, livemode)
	return s.attachAttention(ctx, inv), livemode, nil
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
		// add_on is the default for operator-added line items (one-off invoice
		// composer, ad-hoc charges). Engine-driven cycle invoices always pass
		// an explicit type (base_fee / usage). The DB CHECK accepts only:
		// base_fee, usage, add_on, discount, tax — so the previous "manual"
		// fallback would have been rejected by Postgres on insert.
		lineType = string(domain.LineTypeAddOn)
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
	inv, err := s.couponApplier.ApplyCouponToInvoice(ctx, tenantID, invoiceID, code, idempotencyKey)
	if err != nil {
		return domain.Invoice{}, err
	}
	return s.attachAttention(ctx, inv), nil
}

// RetryTax routes a "Retry tax" action through the billing engine.
// Called from both the operator-triggered HTTP handler and the
// background reconciler (RetryPendingTax). The engine owns the
// recompute and atomic persist; this service method layers
// auto-finalize on top: when the retry succeeds AND the invoice
// is engine-generated (billing_reason != "manual"), finalize in
// the same call. Manual invoices stay draft so the operator
// retains the explicit finalize step.
//
// Returns the updated invoice with its Attention re-derived so
// the caller renders the new state immediately.
func (s *Service) RetryTax(ctx context.Context, tenantID, invoiceID string) (domain.Invoice, error) {
	if s.taxRetrier == nil {
		return domain.Invoice{}, errs.InvalidState("tax retry is not configured")
	}
	inv, err := s.taxRetrier.RetryTaxForInvoice(ctx, tenantID, invoiceID)
	if err != nil {
		return domain.Invoice{}, err
	}
	if shouldAutoFinalizeAfterRetry(inv) {
		final, ferr := s.Finalize(ctx, tenantID, invoiceID)
		if ferr != nil {
			// Tax already updated and persisted; finalize-side
			// failure shouldn't unwind the recompute. Return the
			// post-retry invoice so the operator at least sees the
			// tax decision; finalize remains available as a
			// follow-up click. Logged so post-mortems can see why
			// auto-finalize didn't fire.
			slog.Warn("invoice: auto-finalize after tax retry failed; tax recomputed but invoice stays draft",
				"error", ferr, "tenant_id", tenantID, "invoice_id", invoiceID,
				"billing_reason", inv.BillingReason)
			return s.attachAttention(ctx, inv), nil
		}
		return s.attachAttention(ctx, final), nil
	}
	return s.attachAttention(ctx, inv), nil
}

// shouldAutoFinalizeAfterRetry encodes the gate for chaining a
// successful retry into Finalize. Both must be true:
//
//   - Tax is now resolved (status=ok). Pending/failed retries leave
//     the invoice draft regardless.
//   - Invoice came from the engine, not a manual draft. Manual
//     drafts can still be works-in-progress (operator may add line
//     items, edit memo, etc.); auto-finalize would surprise them.
//     billing_reason='manual' marks operator-created drafts;
//     subscription_cycle / threshold / proration / etc. all qualify
//     for auto-finalize.
//
// Subscription PaymentMethod readiness is intentionally not gated
// here — Finalize doesn't require a PM (PM matters for collection,
// which fires post-finalize via the auto-charge path).
func shouldAutoFinalizeAfterRetry(inv domain.Invoice) bool {
	if inv.Status != domain.InvoiceDraft {
		return false
	}
	if inv.TaxStatus != domain.InvoiceTaxOK {
		return false
	}
	if string(inv.BillingReason) == "" || string(inv.BillingReason) == "manual" {
		return false
	}
	return true
}

// RetryProviderConfigErrors flushes every invoice in the (tenant,
// livemode) partition that's stuck on Stripe-configuration tax
// errors (provider_not_configured / provider_auth). Called by
// tenantstripe.Service.Connect on verify-success — when the
// operator just supplied fresh credentials, the system should
// catch up the work that was waiting on that signal. ADR-019.
//
// Reuses RetryTax per row, which already includes the auto-finalize
// chain (ADR-017): engine-generated invoices that recompute clean
// will move from draft → finalized in the same call. Manual drafts
// stay draft for explicit operator finalize.
//
// Errors are collected, not aborted-on. One bad row (e.g. concurrent
// operator click racing the same retry) shouldn't stall the rest of
// the flush.
func (s *Service) RetryProviderConfigErrors(ctx context.Context, tenantID string, livemode bool) (int, []error) {
	if s.taxRetrier == nil {
		return 0, nil
	}
	stuck, err := s.store.ListProviderConfigErrors(ctx, tenantID, livemode)
	if err != nil {
		return 0, []error{fmt.Errorf("list stuck provider-config invoices: %w", err)}
	}
	var (
		processed int
		errs      []error
	)
	for _, inv := range stuck {
		if _, retryErr := s.RetryTax(ctx, tenantID, inv.ID); retryErr != nil {
			errs = append(errs, fmt.Errorf("invoice %s: %w", inv.ID, retryErr))
			continue
		}
		processed++
	}
	return processed, errs
}

// CountProviderConfigErrors returns how many invoices would be
// retried by RetryProviderConfigErrors right now. Used by the
// connect handler to populate the response body so the dashboard
// can render "Retrying N stuck invoices" without waiting for the
// async fan-out to finish.
func (s *Service) CountProviderConfigErrors(ctx context.Context, tenantID string, livemode bool) (int, error) {
	stuck, err := s.store.ListProviderConfigErrors(ctx, tenantID, livemode)
	if err != nil {
		return 0, err
	}
	return len(stuck), nil
}

// RetryPendingTax is the background-reconciler entry. Scans
// invoices whose retryable-coded tax failure is due for another
// attempt, calls RetryTax for each (which may auto-finalize on
// success), returns counts for telemetry.
//
// Cross-tenant: each invoice carries its tenant_id, and RetryTax
// re-pins it on ctx through the engine's path. Bounded by `batch`
// per tick so a Stripe Tax outage that stuck thousands of
// invoices doesn't burn the entire scheduler tick on tax retries.
//
// Errors per invoice are collected, not aborted-on — one bad row
// (e.g. concurrent operator-driven Finalize racing the reconciler)
// shouldn't stall the rest of the batch.
func (s *Service) RetryPendingTax(ctx context.Context, batch int) (int, []error) {
	if s.taxRetrier == nil {
		return 0, nil
	}
	codes := []string{"provider_outage", "unknown"}
	const maxAttempts = 8
	// Reconciler runs once per mode in the scheduler tick — pull
	// the mode off ctx and filter the SQL scan so this tick only
	// processes its own partition's invoices. Without the filter,
	// the cross-mode RLS-bypassed scan returns rows for both modes
	// and per-row RetryTax fails with "not found" on the half whose
	// livemode doesn't match ctx.
	livemode := postgres.Livemode(ctx)
	stuck, err := s.store.ListPendingTaxRetry(ctx, batch, codes, maxAttempts, livemode)
	if err != nil {
		return 0, []error{fmt.Errorf("list pending tax retries: %w", err)}
	}
	var (
		processed int
		errs      []error
	)
	for _, inv := range stuck {
		// RetryTax pins tenant via the engine entry point that
		// already does WithTenantID. We pass the per-row tenant
		// explicitly here rather than relying on ctx because this
		// loop is cross-tenant.
		if _, retryErr := s.RetryTax(ctx, inv.TenantID, inv.ID); retryErr != nil {
			errs = append(errs, fmt.Errorf("invoice %s: %w", inv.ID, retryErr))
			continue
		}
		processed++
	}
	return processed, errs
}
