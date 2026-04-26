// Package importstripe imports data from a source Stripe account into Velox.
//
// Phase 0 implements customer import only; subsequent phases extend the same
// Source/Importer/Report surface to subscriptions, products+prices, and
// finalized invoices. See docs/design-stripe-importer.md for the full plan.
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
}

// ErrStopIteration is a sentinel returned from a Source callback to halt
// iteration without surfacing as an error. Used by --dry-run and bounded
// test runs.
type stopIteration struct{}

func (stopIteration) Error() string { return "stop iteration" }

// ErrStopIteration is the canonical sentinel value.
var ErrStopIteration error = stopIteration{}
