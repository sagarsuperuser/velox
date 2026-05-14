package customer

import (
	"context"
	"log/slog"
	"regexp"
	"strings"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/clock"
	"github.com/sagarsuperuser/velox/internal/tax"
)

var phonePattern = regexp.MustCompile(`^[\+\d\s\-\(\)]{7,20}$`)

// StripeSyncer syncs billing profile data to Stripe when a Stripe customer exists.
type StripeSyncer interface {
	SyncBillingProfile(ctx context.Context, stripeCustomerID string, bp domain.CustomerBillingProfile) error
}

// PaymentSetupReader reads customer payment setup to find Stripe customer ID.
type PaymentSetupReader interface {
	GetPaymentSetup(ctx context.Context, tenantID, customerID string) (domain.CustomerPaymentSetup, error)
}

// TestClockChecker validates a test_clock_id at customer-create time
// (Stripe parity, ADR-027). Narrow shape — Service doesn't need
// the full TestClock; just "does it exist for this tenant?".
// Implemented by *testclock.PostgresStore.
type TestClockChecker interface {
	Exists(ctx context.Context, tenantID, clockID string) (bool, error)
}

// TaxFlusher fans out a tax-retry across every draft invoice for one
// customer that's stuck on `customer_data_invalid`. Fired from
// UpsertBillingProfile after a successful save — the only operator
// action that can resolve customer_data_invalid is editing the
// billing profile, so the moment the save lands we replay the tax
// calc against the (now possibly-correct) fields. Operator never has
// to click Retry per-invoice.
//
// Narrow shape (single method, no return values used by the caller)
// keeps the customer→invoice dep one-way: customer doesn't need to
// know what tax retry means, only that it exists. Implemented by
// *invoice.Service.
//
// Mirrors the ADR-019 Stripe-reconnect flush at per-customer scope.
type TaxFlusher interface {
	RetryCustomerDataErrors(ctx context.Context, tenantID, customerID string) (int, []error)
}

type Service struct {
	store         Store
	stripeSyncer  StripeSyncer
	paymentSetups PaymentSetupReader
	clocks        TestClockChecker
	taxFlusher    TaxFlusher
	events        domain.EventDispatcher
	resolver      clock.Resolver
}

func NewService(store Store) *Service {
	return &Service{store: store}
}

// SetResolver wires the unified clock.Resolver. At Update /
// UpsertBillingProfile and other customer-scoped mutations, ctx is
// bound to the customer's effective-now so downstream postgres
// timestamp writes (updated_at on customer row, billing_profile
// updates, etc.) land in simulated time on clock-pinned customers.
//
// Customer.Create can't bind from the customer pin (the customer
// doesn't exist yet), but if the input carries a TestClockID at
// creation we bind from the clock directly. Otherwise wall-clock.
func (s *Service) SetResolver(r clock.Resolver) {
	s.resolver = r
}

// bindForCustomer binds effective-now from a customer pin for any
// post-create mutation (Update, UpsertBillingProfile, ArchiveCustomer).
// Returns ctx unchanged on resolver error or no resolver.
func (s *Service) bindForCustomer(ctx context.Context, tenantID, customerID string) context.Context {
	bound, _ := clock.BindEffectiveNow(ctx, s.resolver, clock.Pin{TenantID: tenantID, CustomerID: customerID})
	return bound
}

// SetStripeSyncer configures Stripe sync (optional, breaks circular dep).
func (s *Service) SetStripeSyncer(syncer StripeSyncer, setups PaymentSetupReader) {
	s.stripeSyncer = syncer
	s.paymentSetups = setups
}

// SetTestClockChecker wires the test-clock existence check used at
// customer-create time. Optional — if unwired, customer-create
// rejects any test_clock_id (defensive default; production wires
// the real store via api/router.go).
func (s *Service) SetTestClockChecker(checker TestClockChecker) {
	s.clocks = checker
}

// SetTaxFlusher wires the per-customer tax-retry flush fired after a
// successful billing-profile upsert. Optional — if unwired, the
// flush is a no-op and operators fall back to per-invoice Retry.
// Production wires *invoice.Service via api/router.go.
func (s *Service) SetTaxFlusher(flusher TaxFlusher) {
	s.taxFlusher = flusher
}

// SetEventDispatcher wires the outbound webhook dispatcher so the service
// can emit customer.email_bounced (and future customer.* events). Nil is
// acceptable — events become a no-op, which is the right behaviour in
// narrow unit tests.
func (s *Service) SetEventDispatcher(d domain.EventDispatcher) {
	s.events = d
}

// MarkEmailBounced records a permanent-delivery-failure signal for the
// given customer (resolved from email by the caller via the blind-index
// lookup). Fires customer.email_bounced after a successful write so
// partners can wire their own dead-address handling. Missing customer
// IDs are treated as soft failures — log and return — because the
// sender path must never panic an outbox worker.
func (s *Service) MarkEmailBounced(ctx context.Context, tenantID, customerID, reason string) error {
	if err := s.store.MarkEmailBounced(ctx, tenantID, customerID, reason); err != nil {
		return err
	}
	if s.events != nil {
		payload := map[string]any{
			"customer_id": customerID,
			"reason":      reason,
		}
		// Non-fatal on dispatch error — the state transition already
		// landed; losing a webhook event isn't worth reversing the
		// status.
		if err := s.events.Dispatch(ctx, tenantID, domain.EventCustomerEmailBounced, payload); err != nil {
			slog.WarnContext(ctx, "dispatch customer.email_bounced",
				"tenant_id", tenantID, "customer_id", customerID, "error", err)
		}
	}
	return nil
}

type CreateInput struct {
	ExternalID  string `json:"external_id"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email,omitempty"`
	// TestClockID pins this customer to a test clock — Stripe parity,
	// ADR-027. Test-mode only; rejected on live-mode customers via
	// the livemode check below. Once attached at create time, every
	// Subscription / Invoice for this customer uses the clock's
	// frozen_time as "now". Cannot be changed post-creation.
	TestClockID string `json:"test_clock_id,omitempty"`
}

func (s *Service) Create(ctx context.Context, tenantID string, input CreateInput) (domain.Customer, error) {
	input.ExternalID = strings.TrimSpace(input.ExternalID)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.Email = strings.TrimSpace(input.Email)
	input.TestClockID = strings.TrimSpace(input.TestClockID)

	if input.ExternalID == "" {
		return domain.Customer{}, errs.Required("external_id")
	}
	if err := domain.MaxLen("external_id", input.ExternalID, 255); err != nil {
		return domain.Customer{}, err
	}
	if input.DisplayName == "" {
		return domain.Customer{}, errs.Required("display_name")
	}
	if err := domain.MaxLen("display_name", input.DisplayName, 255); err != nil {
		return domain.Customer{}, err
	}
	if input.Email != "" {
		if err := validateEmail("email", input.Email); err != nil {
			return domain.Customer{}, err
		}
	}
	// Test-clock attach: validate clock exists (mode gating happens at
	// the route layer — test_clock_id only reaches us when the caller
	// is on a test-mode key). If the checker isn't wired (defensive
	// default) any test_clock_id is rejected.
	if input.TestClockID != "" {
		if s.clocks == nil {
			return domain.Customer{}, errs.Invalid("test_clock_id", "test clocks are not available in this environment")
		}
		exists, err := s.clocks.Exists(ctx, tenantID, input.TestClockID)
		if err != nil {
			return domain.Customer{}, err
		}
		if !exists {
			return domain.Customer{}, errs.Invalid("test_clock_id", "test clock not found")
		}
	}

	return s.store.Create(ctx, tenantID, domain.Customer{
		ExternalID:  input.ExternalID,
		DisplayName: input.DisplayName,
		Email:       input.Email,
		TestClockID: input.TestClockID,
	})
}

func (s *Service) Get(ctx context.Context, tenantID, id string) (domain.Customer, error) {
	return s.store.Get(ctx, tenantID, id)
}

func (s *Service) List(ctx context.Context, filter ListFilter) ([]domain.Customer, int, error) {
	return s.store.List(ctx, filter)
}

// ListByTestClockID returns customers pinned to the given test clock.
// Exposed as a service method (not just a store passthrough) so the
// testclock domain can depend on the customer service's narrow
// CustomerReader interface — keeps the per-domain rule intact and
// the decrypt step on the customer read path.
func (s *Service) ListByTestClockID(ctx context.Context, tenantID, clockID string) ([]domain.Customer, error) {
	return s.store.ListByTestClockID(ctx, tenantID, clockID)
}

type UpdateInput struct {
	DisplayName string `json:"display_name"`
	Email       string `json:"email,omitempty"`
	Status      string `json:"status,omitempty"`
}

func (s *Service) Update(ctx context.Context, tenantID, id string, input UpdateInput) (domain.Customer, error) {
	ctx = s.bindForCustomer(ctx, tenantID, id)
	existing, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return domain.Customer{}, err
	}

	if name := strings.TrimSpace(input.DisplayName); name != "" {
		existing.DisplayName = name
	}
	if email := strings.TrimSpace(input.Email); email != "" {
		if err := validateEmail("email", email); err != nil {
			return domain.Customer{}, err
		}
		existing.Email = email
	}
	if status := domain.CustomerStatus(input.Status); status != "" {
		existing.Status = status
	}

	return s.store.Update(ctx, tenantID, existing)
}

func (s *Service) UpsertBillingProfile(ctx context.Context, tenantID string, bp domain.CustomerBillingProfile) (domain.CustomerBillingProfile, error) {
	if bp.CustomerID == "" {
		return domain.CustomerBillingProfile{}, errs.Required("customer_id")
	}
	ctx = s.bindForCustomer(ctx, tenantID, bp.CustomerID)
	if email := strings.TrimSpace(bp.Email); email != "" {
		if err := validateEmail("email", email); err != nil {
			return domain.CustomerBillingProfile{}, err
		}
	}
	if phone := strings.TrimSpace(bp.Phone); phone != "" {
		if !phonePattern.MatchString(phone) {
			return domain.CustomerBillingProfile{}, errs.Invalid("phone", "must be 7-20 characters and contain only digits, spaces, +, -, (, )")
		}
	}
	// Normalize + format-validate tax IDs. Unknown kinds pass through untouched
	// so we don't reject jurisdictions we haven't added explicit support for.
	// Type is normalized to its canonical Stripe code so storage stays
	// consistent regardless of which alias the caller supplied.
	bp.TaxIDType = tax.NormalizeTaxIDType(bp.TaxIDType)
	bp.TaxID = tax.NormalizeTaxID(bp.TaxID)
	if err := tax.ValidateTaxID(bp.TaxIDType, bp.TaxID); err != nil {
		return domain.CustomerBillingProfile{}, errs.Invalid("tax_id", err.Error())
	}
	// Default to standard tax status so zero-valued API requests behave
	// predictably. The DB CHECK constraint mirrors this enum; validating
	// here produces a clean 400 instead of a constraint error.
	if bp.TaxStatus == "" {
		bp.TaxStatus = tax.StatusStandard
	}
	switch bp.TaxStatus {
	case tax.StatusStandard, tax.StatusExempt, tax.StatusReverseCharge:
	default:
		return domain.CustomerBillingProfile{}, errs.Invalid("tax_status", "must be 'standard', 'exempt', or 'reverse_charge'")
	}
	bp.TaxExemptReason = strings.TrimSpace(bp.TaxExemptReason)
	if bp.ProfileStatus == "" {
		bp.ProfileStatus = domain.BillingProfileIncomplete
	}
	result, err := s.store.UpsertBillingProfile(ctx, tenantID, bp)
	if err != nil {
		return result, err
	}

	// Sync to Stripe if a Stripe customer exists
	if s.stripeSyncer != nil && s.paymentSetups != nil {
		ps, psErr := s.paymentSetups.GetPaymentSetup(ctx, tenantID, bp.CustomerID)
		if psErr == nil && ps.StripeCustomerID != "" {
			if syncErr := s.stripeSyncer.SyncBillingProfile(ctx, ps.StripeCustomerID, result); syncErr != nil {
				slog.Warn("failed to sync billing profile to Stripe",
					"customer_id", bp.CustomerID, "stripe_customer_id", ps.StripeCustomerID, "error", syncErr)
				// Non-fatal: local save succeeded, Stripe sync is best-effort
			}
		}
	}

	// Fan out a tax retry across every draft invoice for this customer
	// stuck on `customer_data_invalid`. The operator's edit IS the
	// retry trigger — same shape as ADR-019's Stripe-reconnect flush
	// scoped to one customer. Best-effort: per-invoice failures are
	// logged but don't roll back the profile save.
	if s.taxFlusher != nil {
		processed, retryErrs := s.taxFlusher.RetryCustomerDataErrors(ctx, tenantID, bp.CustomerID)
		if processed > 0 || len(retryErrs) > 0 {
			slog.InfoContext(ctx, "billing profile flush retried tax errors",
				"tenant_id", tenantID,
				"customer_id", bp.CustomerID,
				"processed", processed,
				"errors", len(retryErrs),
			)
		}
		for _, retryErr := range retryErrs {
			slog.WarnContext(ctx, "billing profile flush tax retry failed",
				"tenant_id", tenantID,
				"customer_id", bp.CustomerID,
				"error", retryErr,
			)
		}
	}

	return result, nil
}

func (s *Service) GetBillingProfile(ctx context.Context, tenantID, customerID string) (domain.CustomerBillingProfile, error) {
	return s.store.GetBillingProfile(ctx, tenantID, customerID)
}

func validateEmail(field, email string) error {
	at := strings.Index(email, "@")
	if at < 1 {
		return errs.Invalid(field, "invalid email: must contain @")
	}
	domainPart := email[at+1:]
	if !strings.Contains(domainPart, ".") || strings.HasSuffix(domainPart, ".") {
		return errs.Invalid(field, "invalid email: domain must contain a dot")
	}
	return nil
}
