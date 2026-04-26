package billingalert

import (
	"context"
	"fmt"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

// Service is the read/write surface backing the four /v1/billing/alerts
// endpoints. Wire-handlers translate JSON to/from the request types
// here; the service validates inputs, verifies referential integrity,
// and delegates persistence to Store.
type Service struct {
	store     Store
	customers CustomerLookup
	meters    MeterLookup
}

// NewService wires the composition. customers and meters can be the
// same store implementation (customer.Store satisfies CustomerLookup,
// pricing.Service satisfies MeterLookup) — the narrow seam exists so
// the unit tests can use fakes.
func NewService(store Store, customers CustomerLookup, meters MeterLookup) *Service {
	return &Service{store: store, customers: customers, meters: meters}
}

// CreateRequest is the input shape for Service.Create. Decoupled from
// the HTTP wire shape so wire-shape changes don't force service-layer
// signatures to change in lockstep.
type CreateRequest struct {
	Title          string
	CustomerID     string
	MeterID        string
	Dimensions     map[string]any
	AmountCentsGTE *int64
	QuantityGTE    *decimal.Decimal
	Recurrence     domain.BillingAlertRecurrence
}

// MaxTitleLen caps the dashboard-visible title. 200 chars matches
// other display fields (plan.name, coupon.code) and keeps the column
// indexable without bloat.
const MaxTitleLen = 200

// MaxDimensionKeys mirrors the cap on usage_events.dimensions and
// meter_pricing_rules.dimension_match — keeps the JSONB column
// cheap to validate on every insert.
const MaxDimensionKeys = 16

// Create validates the request, verifies the customer / meter exist
// for this tenant, and persists the alert. Cross-tenant customer or
// meter IDs surface as 404 via RLS — the lookup returns ErrNotFound
// and we wrap it with the appropriate field annotation.
func (s *Service) Create(ctx context.Context, tenantID string, req CreateRequest) (domain.BillingAlert, error) {
	title := strings.TrimSpace(req.Title)
	customerID := strings.TrimSpace(req.CustomerID)

	if title == "" {
		return domain.BillingAlert{}, errs.Required("title")
	}
	if len(title) > MaxTitleLen {
		return domain.BillingAlert{}, errs.Invalid("title", fmt.Sprintf("must be ≤ %d characters", MaxTitleLen))
	}
	if customerID == "" {
		return domain.BillingAlert{}, errs.Required("customer_id")
	}
	if req.Recurrence != domain.BillingAlertRecurrenceOneTime && req.Recurrence != domain.BillingAlertRecurrencePerPeriod {
		return domain.BillingAlert{}, errs.Invalid("recurrence", "must be one of one_time, per_period")
	}

	// Threshold validation: exactly one of amount_gte / usage_gte must
	// be set. Both nil = no fire condition; both set = ambiguous.
	hasAmount := req.AmountCentsGTE != nil
	hasQty := req.QuantityGTE != nil
	if !hasAmount && !hasQty {
		return domain.BillingAlert{}, errs.Required("threshold")
	}
	if hasAmount && hasQty {
		return domain.BillingAlert{}, errs.Invalid("threshold", "exactly one of amount_gte or usage_gte must be set")
	}
	if hasAmount && *req.AmountCentsGTE <= 0 {
		return domain.BillingAlert{}, errs.Invalid("threshold.amount_gte", "must be > 0")
	}
	if hasQty && req.QuantityGTE.Sign() <= 0 {
		return domain.BillingAlert{}, errs.Invalid("threshold.usage_gte", "must be > 0")
	}

	meterID := strings.TrimSpace(req.MeterID)
	if hasQty && meterID == "" {
		return domain.BillingAlert{}, errs.Invalid(
			"threshold.usage_gte",
			"only valid when filter.meter_id is set — cross-meter quantity sums are not well-defined",
		)
	}

	// Dimensions must be a scalar-leaf map (same contract usage events
	// follow). Empty / nil → '{}' — always-object idiom downstream.
	if len(req.Dimensions) > 0 && meterID == "" {
		return domain.BillingAlert{}, errs.Invalid(
			"filter.dimensions",
			"only valid when filter.meter_id is set — cross-meter dimension filtering not supported in v1",
		)
	}
	if err := validateDimensions(req.Dimensions); err != nil {
		return domain.BillingAlert{}, err
	}

	// RLS-safe existence checks. Cross-tenant IDs return ErrNotFound
	// from the store; we surface as a 404 with the field tagged so the
	// frontend can route the message.
	if _, err := s.customers.Get(ctx, tenantID, customerID); err != nil {
		return domain.BillingAlert{}, err
	}
	if meterID != "" {
		if _, err := s.meters.GetMeter(ctx, tenantID, meterID); err != nil {
			return domain.BillingAlert{}, err
		}
	}

	dims := req.Dimensions
	if dims == nil {
		dims = map[string]any{}
	}

	alert := domain.BillingAlert{
		TenantID:   tenantID,
		CustomerID: customerID,
		Title:      title,
		Filter: domain.BillingAlertFilter{
			MeterID:    meterID,
			Dimensions: dims,
		},
		Threshold: domain.BillingAlertThreshold{
			AmountCentsGTE: req.AmountCentsGTE,
			QuantityGTE:    req.QuantityGTE,
		},
		Recurrence: req.Recurrence,
		Status:     domain.BillingAlertStatusActive,
	}

	return s.store.Create(ctx, tenantID, alert)
}

// Get fetches a single alert by ID. Cross-tenant ID returns
// ErrNotFound (RLS hides the row).
func (s *Service) Get(ctx context.Context, tenantID, id string) (domain.BillingAlert, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return domain.BillingAlert{}, errs.Required("id")
	}
	return s.store.Get(ctx, tenantID, id)
}

// List paginates alerts for the tenant. Filter shape matches the wire
// query params; empty fields are ignored.
func (s *Service) List(ctx context.Context, filter ListFilter) ([]domain.BillingAlert, int, error) {
	filter.TenantID = strings.TrimSpace(filter.TenantID)
	if filter.TenantID == "" {
		return nil, 0, errs.Required("tenant_id")
	}
	// Trim status to the known enum or treat as no filter. Unknown
	// values surface as 422 so the caller knows their filter was
	// dropped — silently accepting "unknown" would mask client bugs.
	if filter.Status != "" {
		switch filter.Status {
		case domain.BillingAlertStatusActive,
			domain.BillingAlertStatusTriggered,
			domain.BillingAlertStatusTriggeredForPeriod,
			domain.BillingAlertStatusArchived:
			// ok
		default:
			return nil, 0, errs.Invalid("status", "must be one of active, triggered, triggered_for_period, archived")
		}
	}
	return s.store.List(ctx, filter)
}

// Archive flips the alert's status to archived. Idempotent — archiving
// an already-archived alert returns the same shape with no error.
func (s *Service) Archive(ctx context.Context, tenantID, id string) (domain.BillingAlert, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return domain.BillingAlert{}, errs.Required("id")
	}
	return s.store.Archive(ctx, tenantID, id)
}

// validateDimensions enforces the same scalar-leaf, max-keys contract
// usage events follow. Dimensions feed pricing-rule subset matches at
// fire time; bounding the per-row JSONB protects the GIN index from
// pathological tenants.
func validateDimensions(dims map[string]any) error {
	if len(dims) > MaxDimensionKeys {
		return errs.Invalid("filter.dimensions", fmt.Sprintf("at most %d keys (got %d)", MaxDimensionKeys, len(dims)))
	}
	for k, v := range dims {
		switch v.(type) {
		case nil, string, bool, float64, float32, int, int32, int64:
			// scalar
		default:
			return errs.Invalid("filter.dimensions", fmt.Sprintf("key %q value must be a scalar (string/number/bool), got %T", k, v))
		}
	}
	return nil
}
