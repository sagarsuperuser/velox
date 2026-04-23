package coupon

import (
	"context"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// ListFilter is the input to every paginated coupon query. Zero values
// are meaningful: empty AfterID + zero AfterCreatedAt means "first
// page"; Limit ≤ 0 defaults to 25 inside the store. The seek position
// is the composite (created_at, id) of the last row the caller saw —
// both fields are required together since id alone isn't unique by
// time under concurrent inserts at the same instant.
type ListFilter struct {
	// IncludeArchived surfaces archived rows for the audit view. Default
	// false keeps the operator's day-to-day list clean. Ignored by
	// ListRedemptions (redemptions have no archived concept).
	IncludeArchived bool
	// AfterID and AfterCreatedAt together seek past the previous page.
	// Both must be set to enable the seek — providing only one is a
	// programming error and callers should pass either the zero values
	// or both from the previous page's last row.
	AfterID        string
	AfterCreatedAt time.Time
	// Limit caps the page size. Implementations clamp to [1, 100]; 0
	// falls back to the default (25).
	Limit int

	// Type narrows the result to one coupon kind (percentage or
	// fixed_amount). Empty matches all. Drives the "show me only
	// percentage coupons" filter chip in the dashboard.
	Type domain.CouponType
	// Duration narrows by coupon duration (once / repeating / forever).
	// Empty matches all.
	Duration domain.CouponDuration
	// ExpiresBefore, when non-zero, keeps only rows with an expires_at
	// strictly earlier than the cutoff. Rows with a NULL expires_at
	// (never-expiring coupons) are excluded — the filter is about
	// "coupons that will lapse", so a NULL row can't satisfy it.
	ExpiresBefore time.Time
}

// Store is the persistence surface the service depends on. Everything is
// per-tenant; implementations must scope their queries with RLS.
type Store interface {
	// Create inserts a new coupon row. Returns AlreadyExists on
	// (tenant_id, code) collision.
	Create(ctx context.Context, tenantID string, coupon domain.Coupon) (domain.Coupon, error)

	Get(ctx context.Context, tenantID, id string) (domain.Coupon, error)
	GetByCode(ctx context.Context, tenantID, code string) (domain.Coupon, error)
	// GetByIDs batch-loads coupons keyed by id. Missing ids are omitted
	// from the map rather than raising ErrNotFound — ApplyToInvoice uses
	// it to resolve the coupons for a redemption set and simply drops
	// redemptions whose coupon has been deleted.
	GetByIDs(ctx context.Context, tenantID string, ids []string) (map[string]domain.Coupon, error)
	// List returns a page of coupons ordered by created_at DESC, id DESC.
	// The second return is hasMore — true when the page was filled to
	// filter.Limit. The service layer (not the store) assembles the
	// next-cursor from the last item so callers don't need to peek
	// inside Coupon.
	List(ctx context.Context, tenantID string, filter ListFilter) ([]domain.Coupon, bool, error)

	// Update patches mutable fields (name, max_redemptions, expires_at,
	// metadata). Immutable fields (type, code, amount/percent, plan_ids,
	// customer_id, duration, stackable) are rejected upstream to keep the
	// redemption semantics stable under the same code. ifMatch is the
	// optimistic-concurrency token: when non-nil, the UPDATE only fires
	// if the stored version matches; a mismatch yields
	// errs.ErrPreconditionFailed so the caller can surface 412. When nil,
	// the write proceeds unconditionally.
	Update(ctx context.Context, tenantID string, coupon domain.Coupon, ifMatch *int) (domain.Coupon, error)

	// Archive stamps archived_at = now(). Idempotent: repeat calls on an
	// already-archived row are a no-op, not an error.
	Archive(ctx context.Context, tenantID, id string, at time.Time) error
	// Unarchive clears archived_at. Idempotent for the already-live row.
	Unarchive(ctx context.Context, tenantID, id string) error

	// RedeemAtomic is the heart of correctness for the redeem path. In a
	// single transaction it locks the coupon row, validates the live-state
	// gates (archived, expired, max_redemptions), increments
	// times_redeemed, and inserts the redemption. Returns ErrCouponGate
	// with a Reason when a gate fails. Collision on the idempotency-key
	// unique index returns the existing redemption with Replay=true.
	RedeemAtomic(ctx context.Context, tenantID string, in RedeemAtomicInput) (RedeemAtomicResult, error)

	// GetRedemptionByIdempotencyKey returns an existing redemption with
	// the given key. Used before the atomic path to short-circuit
	// repeated requests without even loading the coupon.
	GetRedemptionByIdempotencyKey(ctx context.Context, tenantID, key string) (domain.CouponRedemption, error)

	// ListRedemptions returns a page of redemptions for one coupon,
	// ordered by created_at DESC, id DESC. Shares the ListFilter shape
	// with List so the handler layer can reuse ParsePageParams.
	// IncludeArchived is ignored — redemptions don't carry the archived
	// concept (voided is the analogue, and voided rows are still shown
	// for audit).
	ListRedemptions(ctx context.Context, tenantID, couponID string, filter ListFilter) ([]domain.CouponRedemption, bool, error)
	ListRedemptionsBySubscription(ctx context.Context, tenantID, subscriptionID string) ([]domain.CouponRedemption, error)
	// CountRedemptionsByCustomer returns how many times a coupon has been
	// redeemed by a specific customer. Drives the
	// max_redemptions_per_customer restriction; bundled as a count query
	// rather than a full list because the only question is "how many".
	CountRedemptionsByCustomer(ctx context.Context, tenantID, couponID, customerID string) (int, error)

	// IncrementPeriodsApplied bumps periods_applied by 1 on each redemption in
	// one transaction. Called by the billing engine after an invoice that used
	// the redemptions commits, so duration-limited coupons (once / repeating)
	// exhaust on schedule. Atomic: either all succeed or none do — avoids the
	// half-bumped state the pre-batch per-id loop could produce on a mid-run
	// failure.
	IncrementPeriodsApplied(ctx context.Context, tenantID string, redemptionIDs []string) error

	// VoidRedemptionsForInvoice marks every (non-voided) redemption pinned
	// to invoiceID as voided and reverses its effects on the parent coupon:
	// times_redeemed is decremented by one per voided redemption, and
	// periods_applied is rolled back to max(0, periods_applied - 1) — all
	// in a single transaction. Returns the count of redemptions voided so
	// callers can tell "nothing to do" apart from a successful reversal.
	// Idempotent: repeat calls with the same invoice id void nothing
	// further and return 0.
	VoidRedemptionsForInvoice(ctx context.Context, tenantID, invoiceID string) (int, error)

	// ListActiveCustomerAssignments returns live customer-scoped
	// assignments: redemption rows with subscription_id IS NULL AND
	// invoice_id IS NULL AND voided_at IS NULL. At most one active row per
	// customer is expected under normal operation — the service layer
	// rejects a second attach while the first still has periods left.
	// ApplyToInvoiceForCustomer re-reads this on every invoice generation,
	// which is why 0046 adds the partial index on (tenant_id, customer_id).
	ListActiveCustomerAssignments(ctx context.Context, tenantID, customerID string) ([]domain.CouponRedemption, error)

	// VoidCustomerAssignment marks a single customer-scoped assignment
	// voided and rolls back its coupon.times_redeemed in the same tx.
	// Mirrors VoidRedemptionsForInvoice but scopes by redemption id since
	// revocation is operator-initiated, not invoice-driven. Returns
	// errs.ErrNotFound if the row doesn't exist or is already voided.
	VoidCustomerAssignment(ctx context.Context, tenantID, redemptionID string) error
}

// RedeemAtomicInput carries everything the atomic redeem path needs. The
// store resolves Code -> coupon inside the same tx as the counter
// increment so a concurrent archive/expire racing the caller's pre-fetch
// can't produce a false-positive redemption.
type RedeemAtomicInput struct {
	Code           string
	CustomerID     string
	SubscriptionID string
	InvoiceID      string
	DiscountCents  int64
	IdempotencyKey string
}

// RedeemAtomicResult returns the persisted redemption plus the coupon
// row as it looked post-increment. Replay=true means the idempotency key
// already had a row and the response is the original redemption.
type RedeemAtomicResult struct {
	Coupon     domain.Coupon
	Redemption domain.CouponRedemption
	Replay     bool
}

// GateReason is the specific reason a RedeemAtomic gate rejected the
// request. Wrapping the reason (rather than using the generic errs.*
// shape) lets the service layer translate into field-scoped DomainErrors
// with the right user-facing message.
type GateReason string

const (
	GateArchived       GateReason = "archived"
	GateExpired        GateReason = "expired"
	GateMaxRedemptions GateReason = "max_redemptions"
	GateNotFound       GateReason = "not_found"
)

// ErrCouponGate is returned by RedeemAtomic when the coupon exists but
// fails one of the live-state gates. The caller maps Reason to the
// user-facing message; the DB has already rolled back.
type ErrCouponGate struct {
	Reason GateReason
}

func (e ErrCouponGate) Error() string { return "coupon gate: " + string(e.Reason) }
