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
	"github.com/sagarsuperuser/velox/internal/usage"
)

// MaxExportedUsageEvents caps the number of usage events returned per export
// so a high-volume customer can't OOM the API process. The cap is generous
// (10k) because GDPR Art. 20 requires "the personal data concerning him or
// her" — so partial exports are non-conforming. If a customer exceeds this,
// the exporter returns the most recent 10k events and sets
// UsageEventsTruncated=true so the caller can fall back to the streaming
// /usage-events list endpoint paginated for the same customer.
const MaxExportedUsageEvents = 10000

// GDPRExport is the full data export for a customer (right to portability).
//
// Note on scope: tenant-level pricing metadata (meter_pricing_rules,
// billing_alerts, meters themselves) is intentionally NOT included.
// Those are the operator's pricing configuration, not the data subject's
// personal data, and including them would leak the operator's commercial
// pricing strategy without serving any GDPR right. The data subject can
// reconstruct what each event was billed for via the invoice line items
// (which ARE included).
type GDPRExport struct {
	ExportedAt     time.Time                      `json:"exported_at"`
	Customer       domain.Customer                `json:"customer"`
	BillingProfile *domain.CustomerBillingProfile `json:"billing_profile,omitempty"`
	PaymentSetup   *RedactedPaymentSetup          `json:"payment_setup,omitempty"`
	Invoices       []domain.Invoice               `json:"invoices"`
	CreditEntries  []domain.CreditLedgerEntry     `json:"credit_entries"`
	CreditBalance  *domain.CreditBalance          `json:"credit_balance,omitempty"`
	Subscriptions  []domain.Subscription          `json:"subscriptions"`
	// UsageEvents are the raw usage_events rows for this customer including
	// the per-event Dimensions (multi-dim meters carry their dimensions in
	// the same `properties` JSONB column the domain model exposes as
	// Dimensions). The full row is required because pricing-rule resolution
	// is dimension-aware: without dimensions a downstream replay could not
	// reconstruct the same per-rule aggregation.
	UsageEvents          []domain.UsageEvent `json:"usage_events"`
	UsageEventsTruncated bool                `json:"usage_events_truncated,omitempty"`
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
	usage         usage.Store
	auditLogger   *audit.Logger
}

// NewGDPRService creates a new GDPR service with the required stores.
func NewGDPRService(
	customers Store,
	invoices invoice.Store,
	credits credit.Store,
	subscriptions subscription.Store,
	usageStore usage.Store,
	auditLogger *audit.Logger,
) *GDPRService {
	return &GDPRService{
		customers:     customers,
		invoices:      invoices,
		credits:       credits,
		subscriptions: subscriptions,
		usage:         usageStore,
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

	// Usage events — full rows with per-event Dimensions. Multi-dim meters
	// stash their dimension values on each event (the JSONB `properties`
	// column the store exposes as Dimensions), so a portable export must
	// emit those alongside the customer/meter/quantity tuple. Without them,
	// a reissued export cannot be reconciled against original invoices.
	//
	// usage.PostgresStore.List clamps page size to 1000, so we paginate to
	// reach MaxExportedUsageEvents. We over-fetch by one to detect
	// truncation without a separate COUNT query and stop as soon as we
	// either fill the cap or hit a short page.
	if s.usage != nil {
		export.UsageEvents = []domain.UsageEvent{}
		const pageSize = 1000
		for offset := 0; offset <= MaxExportedUsageEvents; offset += pageSize {
			page, _, err := s.usage.List(ctx, usage.ListFilter{
				TenantID:   tenantID,
				CustomerID: customerID,
				Limit:      pageSize,
				Offset:     offset,
			})
			if err != nil {
				return GDPRExport{}, fmt.Errorf("list usage events (offset=%d): %w", offset, err)
			}
			if len(page) == 0 {
				break
			}
			// Detect truncation: the offset past the cap exists only to
			// peek for one extra row. If anything comes back at that
			// offset, the customer has more events than we exported.
			if offset >= MaxExportedUsageEvents {
				export.UsageEventsTruncated = true
				break
			}
			remaining := MaxExportedUsageEvents - len(export.UsageEvents)
			if len(page) > remaining {
				page = page[:remaining]
			}
			export.UsageEvents = append(export.UsageEvents, page...)
			if len(page) < pageSize {
				break
			}
		}
	} else {
		export.UsageEvents = []domain.UsageEvent{}
	}

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
