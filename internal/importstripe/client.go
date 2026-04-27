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

func (s *StripeSource) IterateProducts(ctx context.Context, fn func(*stripe.Product) error) error {
	if s == nil || s.client == nil {
		return errors.New("importstripe: nil StripeSource")
	}
	params := &stripe.ProductListParams{}
	params.Limit = stripe.Int64(s.pageSize)
	if s.since > 0 {
		params.Created = stripe.Int64(s.since)
		params.CreatedRange = &stripe.RangeQueryParams{GreaterThanOrEqual: s.since}
	}
	for prod, err := range s.client.V1Products.List(ctx, params) {
		if err != nil {
			return fmt.Errorf("stripe list products: %w", err)
		}
		if prod == nil || prod.Deleted {
			continue
		}
		if err := fn(prod); err != nil {
			if errors.Is(err, ErrStopIteration) {
				return nil
			}
			return err
		}
	}
	return nil
}

func (s *StripeSource) IteratePrices(ctx context.Context, fn func(*stripe.Price) error) error {
	if s == nil || s.client == nil {
		return errors.New("importstripe: nil StripeSource")
	}
	params := &stripe.PriceListParams{}
	params.Limit = stripe.Int64(s.pageSize)
	if s.since > 0 {
		params.Created = stripe.Int64(s.since)
		params.CreatedRange = &stripe.RangeQueryParams{GreaterThanOrEqual: s.since}
	}
	for price, err := range s.client.V1Prices.List(ctx, params) {
		if err != nil {
			return fmt.Errorf("stripe list prices: %w", err)
		}
		if price == nil || price.Deleted {
			continue
		}
		if err := fn(price); err != nil {
			if errors.Is(err, ErrStopIteration) {
				return nil
			}
			return err
		}
	}
	return nil
}

func (s *StripeSource) IterateSubscriptions(ctx context.Context, fn func(*stripe.Subscription) error) error {
	if s == nil || s.client == nil {
		return errors.New("importstripe: nil StripeSource")
	}
	params := &stripe.SubscriptionListParams{}
	params.Limit = stripe.Int64(s.pageSize)
	// Stripe's default list filters out canceled subs; "all" includes every
	// status so historical canceled rows can be imported too. Operators who
	// only want active subs can post-filter the CSV.
	params.Status = stripe.String("all")
	if s.since > 0 {
		params.Created = stripe.Int64(s.since)
		params.CreatedRange = &stripe.RangeQueryParams{GreaterThanOrEqual: s.since}
	}
	for sub, err := range s.client.V1Subscriptions.List(ctx, params) {
		if err != nil {
			return fmt.Errorf("stripe list subscriptions: %w", err)
		}
		if sub == nil {
			continue
		}
		if err := fn(sub); err != nil {
			if errors.Is(err, ErrStopIteration) {
				return nil
			}
			return err
		}
	}
	return nil
}

// IterateInvoices yields every finalized Stripe invoice (paid / void /
// uncollectible) in creation order. Stripe's list API only accepts one
// status at a time, so we make three sequential sweeps and concatenate
// the streams. Drafts and open invoices are intentionally excluded —
// they're not terminal in Stripe and would have to be remapped or
// dropped on the Velox side; Phase 3 explicitly imports finalized rows
// only (the importer maps a draft / open invoice to an error CSV row
// at processOne time, but iterating them at all is wasted bandwidth).
func (s *StripeSource) IterateInvoices(ctx context.Context, fn func(*stripe.Invoice) error) error {
	if s == nil || s.client == nil {
		return errors.New("importstripe: nil StripeSource")
	}
	for _, status := range []string{"paid", "void", "uncollectible"} {
		params := &stripe.InvoiceListParams{}
		params.Limit = stripe.Int64(s.pageSize)
		params.Status = stripe.String(status)
		if s.since > 0 {
			params.Created = stripe.Int64(s.since)
			params.CreatedRange = &stripe.RangeQueryParams{GreaterThanOrEqual: s.since}
		}
		// Expand line items so the per-line subscription / period / pricing
		// sub-objects come back populated rather than as bare ids.
		params.AddExpand("data.lines.data")
		stopped := false
		for inv, err := range s.client.V1Invoices.List(ctx, params) {
			if err != nil {
				return fmt.Errorf("stripe list invoices status=%s: %w", status, err)
			}
			if inv == nil {
				continue
			}
			if err := fn(inv); err != nil {
				if errors.Is(err, ErrStopIteration) {
					stopped = true
					break
				}
				return err
			}
		}
		if stopped {
			return nil
		}
	}
	return nil
}
