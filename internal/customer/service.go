package customer

import (
	"context"
	"log/slog"
	"regexp"
	"strings"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
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

type Service struct {
	store         Store
	stripeSyncer  StripeSyncer
	paymentSetups PaymentSetupReader
	events        domain.EventDispatcher
}

func NewService(store Store) *Service {
	return &Service{store: store}
}

// SetStripeSyncer configures Stripe sync (optional, breaks circular dep).
func (s *Service) SetStripeSyncer(syncer StripeSyncer, setups PaymentSetupReader) {
	s.stripeSyncer = syncer
	s.paymentSetups = setups
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
}

func (s *Service) Create(ctx context.Context, tenantID string, input CreateInput) (domain.Customer, error) {
	input.ExternalID = strings.TrimSpace(input.ExternalID)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.Email = strings.TrimSpace(input.Email)

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

	return s.store.Create(ctx, tenantID, domain.Customer{
		ExternalID:  input.ExternalID,
		DisplayName: input.DisplayName,
		Email:       input.Email,
	})
}

func (s *Service) Get(ctx context.Context, tenantID, id string) (domain.Customer, error) {
	return s.store.Get(ctx, tenantID, id)
}

func (s *Service) List(ctx context.Context, filter ListFilter) ([]domain.Customer, int, error) {
	return s.store.List(ctx, filter)
}

type UpdateInput struct {
	DisplayName string `json:"display_name"`
	Email       string `json:"email,omitempty"`
	Status      string `json:"status,omitempty"`
}

func (s *Service) Update(ctx context.Context, tenantID, id string, input UpdateInput) (domain.Customer, error) {
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
