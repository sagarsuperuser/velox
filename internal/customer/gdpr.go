package customer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sagarsuperuser/velox/internal/audit"
	"github.com/sagarsuperuser/velox/internal/credit"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/invoice"
	"github.com/sagarsuperuser/velox/internal/subscription"
)

// GDPRExport is the full data export for a customer (right to portability).
type GDPRExport struct {
	ExportedAt     time.Time                      `json:"exported_at"`
	Customer       domain.Customer                `json:"customer"`
	BillingProfile *domain.CustomerBillingProfile `json:"billing_profile,omitempty"`
	PaymentSetup   *RedactedPaymentSetup          `json:"payment_setup,omitempty"`
	Invoices       []domain.Invoice               `json:"invoices"`
	CreditEntries  []domain.CreditLedgerEntry     `json:"credit_entries"`
	CreditBalance  *domain.CreditBalance          `json:"credit_balance,omitempty"`
	Subscriptions  []domain.Subscription          `json:"subscriptions"`
	UsageSummary   map[string]int64               `json:"usage_summary,omitempty"`
}

// RedactedPaymentSetup contains payment setup data with Stripe IDs redacted
// to show only the last 4 characters.
type RedactedPaymentSetup struct {
	CustomerID                  string                    `json:"customer_id"`
	SetupStatus                 domain.PaymentSetupStatus `json:"setup_status"`
	DefaultPaymentMethodPresent bool                      `json:"default_payment_method_present"`
	PaymentMethodType           string                    `json:"payment_method_type,omitempty"`
	StripeCustomerID            string                    `json:"stripe_customer_id,omitempty"`
	StripePaymentMethodID       string                    `json:"stripe_payment_method_id,omitempty"`
	CardBrand                   string                    `json:"card_brand,omitempty"`
	CardLast4                   string                    `json:"card_last4,omitempty"`
	CardExpMonth                int                       `json:"card_exp_month,omitempty"`
	CardExpYear                 int                       `json:"card_exp_year,omitempty"`
	LastVerifiedAt              *time.Time                `json:"last_verified_at,omitempty"`
	CreatedAt                   time.Time                 `json:"created_at"`
	UpdatedAt                   time.Time                 `json:"updated_at"`
}

// GDPRService handles GDPR data export and right-to-deletion operations.
type GDPRService struct {
	customers     Store
	invoices      invoice.Store
	credits       credit.Store
	subscriptions subscription.Store
	auditLogger   *audit.Logger
}

// NewGDPRService creates a new GDPR service with the required stores.
func NewGDPRService(
	customers Store,
	invoices invoice.Store,
	credits credit.Store,
	subscriptions subscription.Store,
	auditLogger *audit.Logger,
) *GDPRService {
	return &GDPRService{
		customers:     customers,
		invoices:      invoices,
		credits:       credits,
		subscriptions: subscriptions,
		auditLogger:   auditLogger,
	}
}

// ExportCustomerData returns all data associated with a customer for GDPR
// right-to-portability compliance. Stripe IDs are redacted to show only
// the last 4 characters.
func (s *GDPRService) ExportCustomerData(ctx context.Context, tenantID, customerID string) (GDPRExport, error) {
	cust, err := s.customers.Get(ctx, tenantID, customerID)
	if err != nil {
		return GDPRExport{}, fmt.Errorf("get customer: %w", err)
	}

	export := GDPRExport{
		ExportedAt: time.Now().UTC(),
		Customer:   cust,
	}

	// Billing profile (optional, may not exist)
	if bp, err := s.customers.GetBillingProfile(ctx, tenantID, customerID); err == nil {
		export.BillingProfile = &bp
	} else if !errors.Is(err, errs.ErrNotFound) {
		return GDPRExport{}, fmt.Errorf("get billing profile: %w", err)
	}

	// Payment setup (optional, redact Stripe IDs)
	if ps, err := s.customers.GetPaymentSetup(ctx, tenantID, customerID); err == nil {
		export.PaymentSetup = redactPaymentSetup(ps)
	} else if !errors.Is(err, errs.ErrNotFound) {
		return GDPRExport{}, fmt.Errorf("get payment setup: %w", err)
	}

	// All invoices for this customer
	invoices, _, err := s.invoices.List(ctx, invoice.ListFilter{
		TenantID:   tenantID,
		CustomerID: customerID,
		Limit:      1000,
	})
	if err != nil {
		return GDPRExport{}, fmt.Errorf("list invoices: %w", err)
	}
	if invoices == nil {
		invoices = []domain.Invoice{}
	}
	export.Invoices = invoices

	// Credit ledger entries
	entries, err := s.credits.ListEntries(ctx, credit.ListFilter{
		TenantID:   tenantID,
		CustomerID: customerID,
		Limit:      1000,
	})
	if err != nil {
		return GDPRExport{}, fmt.Errorf("list credit entries: %w", err)
	}
	if entries == nil {
		entries = []domain.CreditLedgerEntry{}
	}
	export.CreditEntries = entries

	// Credit balance
	if bal, err := s.credits.GetBalance(ctx, tenantID, customerID); err == nil {
		export.CreditBalance = &bal
	}

	// All subscriptions
	subs, _, err := s.subscriptions.List(ctx, subscription.ListFilter{
		TenantID:   tenantID,
		CustomerID: customerID,
		Limit:      1000,
	})
	if err != nil {
		return GDPRExport{}, fmt.Errorf("list subscriptions: %w", err)
	}
	if subs == nil {
		subs = []domain.Subscription{}
	}
	export.Subscriptions = subs

	return export, nil
}

// DeleteCustomerData anonymizes customer PII and archives the customer
// for GDPR right-to-erasure compliance. Financial records (invoices, credits)
// are preserved for legal compliance but the customer record itself is
// anonymized.
//
// Returns an error if the customer has active subscriptions (must cancel first).
func (s *GDPRService) DeleteCustomerData(ctx context.Context, tenantID, customerID string) error {
	// Verify customer exists
	cust, err := s.customers.Get(ctx, tenantID, customerID)
	if err != nil {
		return fmt.Errorf("get customer: %w", err)
	}

	// Block deletion if active subscriptions exist
	subs, _, err := s.subscriptions.List(ctx, subscription.ListFilter{
		TenantID:   tenantID,
		CustomerID: customerID,
		Status:     string(domain.SubscriptionActive),
		Limit:      1,
	})
	if err != nil {
		return fmt.Errorf("check subscriptions: %w", err)
	}
	if len(subs) > 0 {
		return fmt.Errorf("customer has active subscriptions; cancel them before deletion")
	}

	// Anonymize customer record
	cust.DisplayName = "Deleted Customer"
	cust.Email = ""
	cust.Status = domain.CustomerStatusArchived
	if _, err := s.customers.Update(ctx, tenantID, cust); err != nil {
		return fmt.Errorf("anonymize customer: %w", err)
	}

	// Anonymize billing profile (best-effort, may not exist)
	if bp, err := s.customers.GetBillingProfile(ctx, tenantID, customerID); err == nil {
		bp.LegalName = ""
		bp.Email = ""
		bp.Phone = ""
		bp.AddressLine1 = ""
		bp.AddressLine2 = ""
		bp.City = ""
		bp.State = ""
		bp.PostalCode = ""
		bp.TaxID = ""
		bp.ProfileStatus = domain.BillingProfileIncomplete
		if _, err := s.customers.UpsertBillingProfile(ctx, tenantID, bp); err != nil {
			return fmt.Errorf("anonymize billing profile: %w", err)
		}
	} else if !errors.Is(err, errs.ErrNotFound) {
		return fmt.Errorf("get billing profile for anonymization: %w", err)
	}

	// Record audit entry for compliance
	if s.auditLogger != nil {
		_ = s.auditLogger.Log(ctx, tenantID, "gdpr.delete", "customer", customerID, map[string]any{
			"original_display_name": cust.DisplayName,
			"reason":                "GDPR right-to-erasure request",
		})
	}

	return nil
}

// redactPaymentSetup creates a redacted copy of the payment setup where
// Stripe IDs show only the last 4 characters (e.g., "...a1b2").
func redactPaymentSetup(ps domain.CustomerPaymentSetup) *RedactedPaymentSetup {
	return &RedactedPaymentSetup{
		CustomerID:                  ps.CustomerID,
		SetupStatus:                 ps.SetupStatus,
		DefaultPaymentMethodPresent: ps.DefaultPaymentMethodPresent,
		PaymentMethodType:           ps.PaymentMethodType,
		StripeCustomerID:            redactID(ps.StripeCustomerID),
		StripePaymentMethodID:       redactID(ps.StripePaymentMethodID),
		CardBrand:                   ps.CardBrand,
		CardLast4:                   ps.CardLast4,
		CardExpMonth:                ps.CardExpMonth,
		CardExpYear:                 ps.CardExpYear,
		LastVerifiedAt:              ps.LastVerifiedAt,
		CreatedAt:                   ps.CreatedAt,
		UpdatedAt:                   ps.UpdatedAt,
	}
}

// redactID redacts an ID to show only the last 4 characters, prefixed with "...".
// Returns empty string if the input is empty.
func redactID(id string) string {
	if id == "" {
		return ""
	}
	if len(id) <= 4 {
		return "..." + id
	}
	return "..." + id[len(id)-4:]
}
