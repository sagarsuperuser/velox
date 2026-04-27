package importstripe

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/stripe/stripe-go/v82"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/subscription"
)

// InvoiceStore is the narrow surface the invoice importer needs for
// persisting invoices + their line items. The importer uses
// CreateWithLineItems directly (not invoice.Service.Finalize) because the
// service applies its own state machine — finalize numbering, hosted-token
// generation, tax recompute — that would clobber the verbatim Stripe
// values we want to preserve. CreateWithLineItems accepts the row + lines
// as-is so the imported invoice matches what Stripe says it is.
//
// GetByStripeInvoiceID is the idempotency lookup; the partial unique index
// from migration 0063 ensures a rerun of `--resource=invoices` skips
// already-imported rows rather than failing with a unique violation.
type InvoiceStore interface {
	CreateWithLineItems(ctx context.Context, tenantID string, inv domain.Invoice, items []domain.InvoiceLineItem) (domain.Invoice, error)
	GetByStripeInvoiceID(ctx context.Context, tenantID, stripeInvoiceID string) (domain.Invoice, error)
	ListLineItems(ctx context.Context, tenantID, invoiceID string) ([]domain.InvoiceLineItem, error)
}

// InvoiceCustomerLookup finds a Velox customer by Stripe `cus_...` id.
// Reused from Phase 0/2's CustomerLookup pattern.
type InvoiceCustomerLookup interface {
	GetByExternalID(ctx context.Context, tenantID, externalID string) (domain.Customer, error)
}

// InvoiceSubscriptionLookup finds a Velox subscription by its `code` (which
// equals the Stripe sub id after Phase 2 import). The importer uses the
// subscription store's bounded List + filter rather than a dedicated
// GetByCode method — Phase 3 is the only caller and a bounded scan beats
// pushing a single-purpose method into the subscription store.
type InvoiceSubscriptionLookup interface {
	List(ctx context.Context, filter subscription.ListFilter) ([]domain.Subscription, int, error)
}

// InvoiceImporter drives the per-row outcome logic for Phase 3.
// Depends on customers AND subscriptions having been imported first —
// looks up the Velox customer (by Stripe `cus_...` id) and the Velox
// subscription (by Stripe `sub_...` id, when the invoice has one). Manual
// invoices with no subscription are inserted with empty SubscriptionID.
type InvoiceImporter struct {
	Source             Source
	Store              InvoiceStore
	CustomerLookup     InvoiceCustomerLookup
	SubscriptionLookup InvoiceSubscriptionLookup
	Report             *Report
	TenantID           string
	Livemode           bool
	DryRun             bool
}

// Run iterates every Stripe invoice the Source yields, applies the
// outcome-decision logic, and writes one report row per invoice.
func (ii *InvoiceImporter) Run(ctx context.Context) error {
	if ii.Source == nil {
		return errors.New("importstripe: nil Source")
	}
	if ii.Report == nil {
		return errors.New("importstripe: nil Report")
	}
	if ii.TenantID == "" {
		return errors.New("importstripe: empty TenantID")
	}
	return ii.Source.IterateInvoices(ctx, func(inv *stripe.Invoice) error {
		row := ii.processOne(ctx, inv)
		if err := ii.Report.Write(row); err != nil {
			return fmt.Errorf("write report row: %w", err)
		}
		return nil
	})
}

func (ii *InvoiceImporter) processOne(ctx context.Context, inv *stripe.Invoice) Row {
	if inv == nil {
		return Row{Resource: ResourceInvoice, Action: ActionError, Detail: "nil stripe invoice"}
	}

	if inv.Livemode != ii.Livemode {
		return Row{
			StripeID: inv.ID,
			Resource: ResourceInvoice,
			Action:   ActionError,
			Detail: fmt.Sprintf(
				"livemode mismatch: stripe=%t importer=%t (use --livemode-default=%t to override)",
				inv.Livemode, ii.Livemode, inv.Livemode),
		}
	}

	mapped, err := mapInvoice(inv)
	if err != nil {
		// Translate the unsupported-status sentinel to a clearer operator
		// message — drafts and open invoices are the most common cause and
		// the message should explicitly point at the policy decision rather
		// than the raw "is not finalized" wording.
		if errors.Is(err, ErrInvoiceUnsupportedStatus) {
			return Row{
				StripeID: inv.ID,
				Resource: ResourceInvoice,
				Action:   ActionError,
				Detail: fmt.Sprintf(
					"stripe invoice status=%s skipped — Phase 3 only imports finalized invoices (paid / void / uncollectible). Settle or void the invoice in Stripe and re-run.",
					inv.Status),
			}
		}
		return Row{StripeID: inv.ID, Resource: ResourceInvoice, Action: ActionError, Detail: err.Error()}
	}

	// Customer lookup. The Stripe invoice references a `cus_...` id which
	// must already have been imported as a Velox customer.
	customer, err := ii.CustomerLookup.GetByExternalID(ctx, ii.TenantID, mapped.CustomerExternalID)
	if err != nil {
		if errors.Is(err, errs.ErrNotFound) {
			return Row{
				StripeID: inv.ID,
				Resource: ResourceInvoice,
				Action:   ActionError,
				Detail:   fmt.Sprintf("customer with external_id %q not found; run --resource=customers first", mapped.CustomerExternalID),
			}
		}
		return Row{
			StripeID: inv.ID,
			Resource: ResourceInvoice,
			Action:   ActionError,
			Detail:   fmt.Sprintf("customer lookup failed: %v", err),
		}
	}
	mapped.Invoice.CustomerID = customer.ID

	// Subscription lookup, when the invoice references one. Manual invoices
	// (billing_reason=manual + no parent.subscription_details) are valid
	// without a subscription — the column is nullable.
	if mapped.SubscriptionExternalID != "" {
		sub, err := ii.findSubByCode(ctx, mapped.SubscriptionExternalID)
		if err != nil {
			return Row{
				StripeID: inv.ID,
				Resource: ResourceInvoice,
				Action:   ActionError,
				Detail:   fmt.Sprintf("subscription lookup failed: %v", err),
			}
		}
		if sub.ID == "" {
			return Row{
				StripeID: inv.ID,
				Resource: ResourceInvoice,
				Action:   ActionError,
				Detail:   fmt.Sprintf("subscription with code %q not found; run --resource=subscriptions first", mapped.SubscriptionExternalID),
			}
		}
		mapped.Invoice.SubscriptionID = sub.ID
	}

	// Idempotency: check for an existing imported invoice carrying this
	// Stripe id. Migration 0063's partial unique index makes
	// stripe_invoice_id the dedup key for Phase 3.
	existing, err := ii.Store.GetByStripeInvoiceID(ctx, ii.TenantID, inv.ID)
	switch {
	case err == nil:
		return ii.handleExisting(ctx, mapped, existing)
	case errors.Is(err, errs.ErrNotFound):
		return ii.handleInsert(ctx, mapped)
	default:
		return Row{
			StripeID: inv.ID,
			Resource: ResourceInvoice,
			Action:   ActionError,
			Detail:   fmt.Sprintf("invoice lookup by stripe_invoice_id failed: %v", err),
		}
	}
}

func (ii *InvoiceImporter) handleInsert(ctx context.Context, mapped MappedInvoice) Row {
	row := Row{
		StripeID: mapped.Invoice.StripeInvoiceID,
		Resource: ResourceInvoice,
		Action:   ActionInsert,
		Detail:   strings.Join(mapped.Notes, "; "),
	}
	if ii.DryRun {
		return row
	}
	created, err := ii.Store.CreateWithLineItems(ctx, ii.TenantID, mapped.Invoice, mapped.LineItems)
	if err != nil {
		return Row{
			StripeID: mapped.Invoice.StripeInvoiceID,
			Resource: ResourceInvoice,
			Action:   ActionError,
			Detail:   fmt.Sprintf("create invoice: %v", err),
		}
	}
	row.VeloxID = created.ID
	return row
}

func (ii *InvoiceImporter) handleExisting(ctx context.Context, mapped MappedInvoice, existing domain.Invoice) Row {
	diff := diffInvoice(mapped, existing)
	if diff == "" {
		return Row{
			StripeID: mapped.Invoice.StripeInvoiceID,
			Resource: ResourceInvoice,
			Action:   ActionSkipEquivalent,
			VeloxID:  existing.ID,
			Detail:   strings.Join(mapped.Notes, "; "),
		}
	}
	notes := append([]string{diff}, mapped.Notes...)
	return Row{
		StripeID: mapped.Invoice.StripeInvoiceID,
		Resource: ResourceInvoice,
		Action:   ActionSkipDivergent,
		VeloxID:  existing.ID,
		Detail:   strings.Join(notes, "; "),
	}
}

// findSubByCode scans the tenant's subscriptions for one whose Code matches
// the Stripe sub id. Bounded ListAll-style scan — same pattern Phase 2's
// importer uses for plan/sub lookups when no dedicated GetByCode method
// exists. Returns a zero Subscription (with ID="") on miss; only returns
// an error on transport failure.
func (ii *InvoiceImporter) findSubByCode(ctx context.Context, code string) (domain.Subscription, error) {
	const pageLimit = 100
	offset := 0
	for {
		subs, total, err := ii.SubscriptionLookup.List(ctx, subscription.ListFilter{
			TenantID: ii.TenantID,
			Limit:    pageLimit,
			Offset:   offset,
		})
		if err != nil {
			return domain.Subscription{}, err
		}
		for _, s := range subs {
			if s.Code == code {
				return s, nil
			}
		}
		offset += len(subs)
		if offset >= total || len(subs) == 0 {
			return domain.Subscription{}, nil
		}
	}
}

// diffInvoice compares the invoice-level fields the import owns (status,
// payment_status, customer_id, subscription_id, currency, totals, period
// window, paid/voided/issued timestamps, billing reason) against the
// persisted Velox row. Stable order for deterministic CSV output.
//
// Line items aren't diffed at the unit-test level: Phase 3 inserts every
// line atomically with the parent and the importer never mutates lines
// on existing invoices. If a Stripe invoice's lines drift after import,
// that's a divergence the operator must resolve manually.
func diffInvoice(mapped MappedInvoice, existing domain.Invoice) string {
	var diffs []string
	add := func(field, want, got string) {
		if want != got {
			diffs = append(diffs, fmt.Sprintf("%s stripe=%q velox=%q", field, want, got))
		}
	}
	add("status", string(mapped.Invoice.Status), string(existing.Status))
	add("payment_status", string(mapped.Invoice.PaymentStatus), string(existing.PaymentStatus))
	add("customer_id", mapped.Invoice.CustomerID, existing.CustomerID)
	add("subscription_id", mapped.Invoice.SubscriptionID, existing.SubscriptionID)
	add("currency", mapped.Invoice.Currency, existing.Currency)
	add("billing_reason", string(mapped.Invoice.BillingReason), string(existing.BillingReason))

	addInt := func(field string, want, got int64) {
		if want != got {
			diffs = append(diffs, fmt.Sprintf("%s stripe=%d velox=%d", field, want, got))
		}
	}
	addInt("subtotal_cents", mapped.Invoice.SubtotalCents, existing.SubtotalCents)
	addInt("tax_amount_cents", mapped.Invoice.TaxAmountCents, existing.TaxAmountCents)
	addInt("total_amount_cents", mapped.Invoice.TotalAmountCents, existing.TotalAmountCents)
	addInt("amount_due_cents", mapped.Invoice.AmountDueCents, existing.AmountDueCents)
	addInt("amount_paid_cents", mapped.Invoice.AmountPaidCents, existing.AmountPaidCents)

	addTime := func(field string, want, got *time.Time) {
		l, r := timeStr(want), timeStr(got)
		if l != r {
			diffs = append(diffs, fmt.Sprintf("%s stripe=%q velox=%q", field, l, r))
		}
	}
	addTime("paid_at", mapped.Invoice.PaidAt, existing.PaidAt)
	addTime("voided_at", mapped.Invoice.VoidedAt, existing.VoidedAt)
	addTime("issued_at", mapped.Invoice.IssuedAt, existing.IssuedAt)

	if !mapped.Invoice.BillingPeriodStart.Equal(existing.BillingPeriodStart) {
		diffs = append(diffs, fmt.Sprintf("billing_period_start stripe=%q velox=%q",
			mapped.Invoice.BillingPeriodStart.UTC().Format(time.RFC3339),
			existing.BillingPeriodStart.UTC().Format(time.RFC3339)))
	}
	if !mapped.Invoice.BillingPeriodEnd.Equal(existing.BillingPeriodEnd) {
		diffs = append(diffs, fmt.Sprintf("billing_period_end stripe=%q velox=%q",
			mapped.Invoice.BillingPeriodEnd.UTC().Format(time.RFC3339),
			existing.BillingPeriodEnd.UTC().Format(time.RFC3339)))
	}

	sort.Strings(diffs)
	return strings.Join(diffs, ", ")
}
