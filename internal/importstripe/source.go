// Package importstripe imports data from a source Stripe account into Velox.
//
// Phase 0 implements customer import; Phase 1 adds products + prices;
// Phase 2 adds subscriptions; Phase 3 adds finalized invoices (paid /
// void / uncollectible only). See docs/design-stripe-importer.md for the
// full plan.
package importstripe

import (
	"context"

	"github.com/stripe/stripe-go/v82"
)

// Source iterates over Stripe resources. Real implementation wraps the
// stripe-go SDK; tests substitute an in-memory fake. The interface is
// intentionally narrow — one method per resource type — so tests don't need
// to fake Stripe's full API surface.
type Source interface {
	// IterateCustomers yields every non-deleted Stripe customer in creation
	// order, oldest first. The callback may return ErrStopIteration to halt
	// early; any other error halts and is returned to the caller.
	IterateCustomers(ctx context.Context, fn func(*stripe.Customer) error) error

	// IterateProducts yields every non-deleted Stripe product in creation
	// order, oldest first. Same semantics as IterateCustomers — early-stop
	// via ErrStopIteration, all other errors halt and propagate.
	IterateProducts(ctx context.Context, fn func(*stripe.Product) error) error

	// IteratePrices yields every non-deleted Stripe price in creation order,
	// oldest first. Same semantics as IterateCustomers.
	IteratePrices(ctx context.Context, fn func(*stripe.Price) error) error

	// IterateSubscriptions yields every non-deleted Stripe subscription in
	// creation order, oldest first. Same semantics as IterateCustomers —
	// early-stop via ErrStopIteration, all other errors halt and propagate.
	// The default Stripe list omits canceled subscriptions; the importer's
	// Source impl passes status=all so historical canceled rows surface for
	// import too.
	IterateSubscriptions(ctx context.Context, fn func(*stripe.Subscription) error) error

	// IterateInvoices yields every Stripe invoice in creation order,
	// oldest first. Same semantics as IterateCustomers — early-stop via
	// ErrStopIteration, all other errors halt and propagate. Phase 3
	// imports finalized invoices only (status in {paid, void,
	// uncollectible}); the implementation in client.go iterates each
	// terminal status separately because Stripe's list API only accepts
	// a single status filter at a time and the all-statuses list would
	// require post-filtering and burn rate limit on draft / open rows
	// the importer would skip anyway.
	IterateInvoices(ctx context.Context, fn func(*stripe.Invoice) error) error
}

// ErrStopIteration is a sentinel returned from a Source callback to halt
// iteration without surfacing as an error. Used by --dry-run and bounded
// test runs.
type stopIteration struct{}

func (stopIteration) Error() string { return "stop iteration" }

// ErrStopIteration is the canonical sentinel value.
var ErrStopIteration error = stopIteration{}
