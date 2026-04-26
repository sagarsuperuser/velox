// Package billingalert implements operator-configured billing alerts —
// thresholds that fire a webhook + dashboard notification when a
// customer's cycle spend crosses a limit. See
// docs/design-billing-alerts.md for the full design.
//
// The package is composed of:
//   - Store / postgres.go : persistence with RLS + outbox-enqueue helper.
//   - Service              : Create / Get / List / Archive surface.
//   - Handler              : chi router for the four HTTP endpoints.
//   - Evaluator            : background trigger that scans active alerts
//     on a tick and emits billing.alert.triggered
//     via the webhook outbox atomically.
//
// Cross-domain dependencies are narrow interfaces (CustomerLookup,
// SubscriptionLister, PricingReader, OutboxEnqueuer) so this package
// doesn't import its sibling domains directly.
package billingalert

import (
	"context"
	"database/sql"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// Store is the persistence surface for billing alerts. Each method opens
// a tenant-scoped tx via postgres.TxTenant; cross-tenant IDs naturally
// 404 because RLS hides the row.
//
// EnqueueOutboxRow is the bridge into the webhook outbox: the evaluator's
// per-alert tx must insert the trigger row, update the alert's status,
// and enqueue the outbound webhook event together. Wrapping a single tx
// is the source of truth for atomicity.
type Store interface {
	Create(ctx context.Context, tenantID string, alert domain.BillingAlert) (domain.BillingAlert, error)
	Get(ctx context.Context, tenantID, id string) (domain.BillingAlert, error)
	List(ctx context.Context, filter ListFilter) ([]domain.BillingAlert, int, error)
	Archive(ctx context.Context, tenantID, id string) (domain.BillingAlert, error)

	// ListCandidates returns alerts the evaluator should evaluate this
	// tick — status IN ('active','triggered_for_period') across all
	// tenants. The evaluator runs under TxBypass (cross-tenant scan)
	// and then opens a per-alert TxTenant for the actual read+fire+commit.
	ListCandidates(ctx context.Context, limit int) ([]domain.BillingAlert, error)

	// FireInTx is called by the evaluator inside a tenant-scoped tx
	// after determining that an alert should fire. It inserts the
	// trigger row, updates the alert's status / last_triggered_at /
	// last_period_start, and returns the inserted trigger. The caller
	// is responsible for enqueueing the outbox row in the same tx and
	// committing.
	//
	// Returns ErrAlreadyFired (sentinel) when UNIQUE (alert_id,
	// period_from) collides — the caller should treat this as a no-op
	// (the period was already fired by another evaluator).
	FireInTx(ctx context.Context, tx *sql.Tx, alert domain.BillingAlert, trigger domain.BillingAlertTrigger, newStatus domain.BillingAlertStatus) (domain.BillingAlertTrigger, error)

	// Rearm is called by the evaluator when a per_period alert's
	// last_period_start is older than the customer's current cycle
	// start. Flips the alert back to 'active' so a fresh evaluation
	// can fire on the new cycle.
	Rearm(ctx context.Context, tenantID, alertID string) error

	// BeginTenantTx is exposed so the evaluator can open the same tx
	// it uses for FireInTx and pass it to OutboxEnqueuer.Enqueue.
	BeginTenantTx(ctx context.Context, tenantID string) (*sql.Tx, error)
}

// ListFilter is the query shape for the list endpoint.
type ListFilter struct {
	TenantID   string
	CustomerID string
	Status     domain.BillingAlertStatus
	Limit      int
	Offset     int
}

// OutboxEnqueuer is the narrow surface the evaluator needs from the
// webhook outbox. Mirrors webhook.OutboxStore.Enqueue — implemented by
// *webhook.OutboxStore in production, by a fake in unit tests.
//
// The Enqueue runs inside the caller's tx so the outbox row commits
// atomically with the alert state change.
type OutboxEnqueuer interface {
	Enqueue(ctx context.Context, tx *sql.Tx, tenantID, eventType string, payload map[string]any) (string, error)
}

// CustomerLookup is the narrow surface used to verify customer
// existence on Create. Mirrors customer.Store.Get.
type CustomerLookup interface {
	Get(ctx context.Context, tenantID, id string) (domain.Customer, error)
}

// MeterLookup is the narrow surface used to verify meter existence on
// Create when filter.meter_id is supplied.
type MeterLookup interface {
	GetMeter(ctx context.Context, tenantID, id string) (domain.Meter, error)
}

// SubscriptionLister returns the customer's subscriptions so the
// evaluator can resolve the current billing cycle. Same shape
// customer-usage and create_preview consume.
type SubscriptionLister interface {
	List(ctx context.Context, filter SubscriptionListFilter) ([]domain.Subscription, int, error)
}

// SubscriptionListFilter is decoupled from the subscription package's
// own ListFilter so this package doesn't import its sibling. The
// adapter in api/router.go translates between the two.
type SubscriptionListFilter struct {
	TenantID   string
	CustomerID string
}

// PricingReader is the narrow read surface for resolving plan / meter
// metadata during evaluation. Mirrors what create_preview consumes.
type PricingReader interface {
	GetPlan(ctx context.Context, tenantID, id string) (domain.Plan, error)
	GetMeter(ctx context.Context, tenantID, id string) (domain.Meter, error)
	GetRatingRule(ctx context.Context, tenantID, id string) (domain.RatingRuleVersion, error)
	ListMeterPricingRulesByMeter(ctx context.Context, tenantID, meterID string) ([]domain.MeterPricingRule, error)
}

// UsageAggregator is the narrow aggregation surface — single method
// from usage.Service. The evaluator's hot read.
type UsageAggregator interface {
	AggregateByPricingRules(ctx context.Context, tenantID, customerID, meterID string, defaultMode domain.AggregationMode, from, to time.Time) ([]domain.RuleAggregation, error)
}

// Locker mirrors postgres.DB.TryAdvisoryLock through a narrow seam so
// the evaluator can be unit-tested without a real Postgres.
type Locker interface {
	TryAdvisoryLock(ctx context.Context, key int64) (Lock, bool, error)
}

// Lock is a held cluster-wide lock. Release frees it. Same shape as
// billing.Lock.
type Lock interface {
	Release()
}
