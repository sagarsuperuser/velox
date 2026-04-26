package importstripe

import (
	"context"
	"errors"
	"fmt"

	"github.com/stripe/stripe-go/v82"
)

// StripeSource adapts the stripe-go v82 SDK to the Source interface. It pages
// through /v1/customers using the SDK's built-in auto-pagination (Seq2)
// — no manual cursor handling needed.
type StripeSource struct {
	client *stripe.Client
	// since filters customers by created >= since (Unix seconds). 0 disables.
	since int64
	// pageSize tunes the per-request page size. Stripe caps at 100; default 100.
	pageSize int64
}

// NewStripeSource constructs a Source backed by stripe-go. apiKey is the
// source Stripe account's secret key — the importer reads from it; it never
// writes to Stripe.
func NewStripeSource(apiKey string, sinceUnix int64) *StripeSource {
	return &StripeSource{
		client:   stripe.NewClient(apiKey),
		since:    sinceUnix,
		pageSize: 100,
	}
}

func (s *StripeSource) IterateCustomers(ctx context.Context, fn func(*stripe.Customer) error) error {
	if s == nil || s.client == nil {
		return errors.New("importstripe: nil StripeSource")
	}
	params := &stripe.CustomerListParams{}
	params.Limit = stripe.Int64(s.pageSize)
	if s.since > 0 {
		// Stripe's "created" filter accepts gte for ranges.
		params.Created = stripe.Int64(s.since)
		params.CreatedRange = &stripe.RangeQueryParams{GreaterThanOrEqual: s.since}
	}
	for cust, err := range s.client.V1Customers.List(ctx, params) {
		if err != nil {
			return fmt.Errorf("stripe list customers: %w", err)
		}
		// Filter out tombstoned customers — Stripe returns them in some
		// edge-case list paths even though the public docs say List skips
		// them. Defensive belt-and-braces.
		if cust == nil || cust.Deleted {
			continue
		}
		if err := fn(cust); err != nil {
			if errors.Is(err, ErrStopIteration) {
				return nil
			}
			return err
		}
	}
	return nil
}
