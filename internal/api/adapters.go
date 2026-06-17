package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/stripe/stripe-go/v82"

	"github.com/sagarsuperuser/velox/internal/audit"
	"github.com/sagarsuperuser/velox/internal/billing"
	"github.com/sagarsuperuser/velox/internal/credit"
	"github.com/sagarsuperuser/velox/internal/creditnote"
	"github.com/sagarsuperuser/velox/internal/customer"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/dunning"
	"github.com/sagarsuperuser/velox/internal/email"
	"github.com/sagarsuperuser/velox/internal/invoice"
	"github.com/sagarsuperuser/velox/internal/payment"
	"github.com/sagarsuperuser/velox/internal/paymentmethods"
	"github.com/sagarsuperuser/velox/internal/platform/clock"
	"github.com/sagarsuperuser/velox/internal/platform/crypto"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/subscription"
)

// compositePaymentSetupStore implements payment.PaymentSetupStore on
// top of the canonical sources — customers.stripe_customer_id +
// payment_methods. Lets payment/checkout.go and payment/stripe.go
// keep their existing PaymentSetupStore-shaped wiring while the
// deprecated customer_payment_setups table is read-only on its way
// out. Compose the CustomerPaymentSetup wire shape from canonical
// state on read; write only the parts the new architecture owns
// (stripe_customer_id) and drop the rest (card details flow through
// paymentmethods.Service via the webhook attach path).
type compositePaymentSetupStore struct {
	customers *customer.PostgresStore
	pms       *paymentmethods.Service
}

func (a *compositePaymentSetupStore) GetPaymentSetup(ctx context.Context, tenantID, customerID string) (domain.CustomerPaymentSetup, error) {
	cust, err := a.customers.Get(ctx, tenantID, customerID)
	if err != nil {
		return domain.CustomerPaymentSetup{}, err
	}
	pms, err := a.pms.List(ctx, tenantID, customerID)
	if err != nil {
		return domain.CustomerPaymentSetup{}, err
	}
	ps := domain.CustomerPaymentSetup{
		CustomerID:       customerID,
		TenantID:         tenantID,
		StripeCustomerID: cust.StripeCustomerID,
		UpdatedAt:        cust.UpdatedAt,
		CreatedAt:        cust.CreatedAt,
	}
	// Find the default active PM. List returns active rows only
	// (detached_at IS NULL) and orders is_default DESC.
	for _, pm := range pms {
		if pm.IsDefault {
			ps.SetupStatus = domain.PaymentSetupReady
			ps.DefaultPaymentMethodPresent = true
			ps.PaymentMethodType = pm.Type
			ps.StripePaymentMethodID = pm.StripePaymentMethodID
			ps.CardBrand = pm.CardBrand
			ps.CardLast4 = pm.CardLast4
			ps.CardExpMonth = pm.CardExpMonth
			ps.CardExpYear = pm.CardExpYear
			return ps, nil
		}
	}
	// Stripe Customer exists but no default PM yet → pending.
	// No Stripe Customer at all → missing.
	if cust.StripeCustomerID != "" {
		ps.SetupStatus = domain.PaymentSetupPending
	} else {
		ps.SetupStatus = domain.PaymentSetupMissing
	}
	return ps, nil
}

func (a *compositePaymentSetupStore) UpsertPaymentSetup(ctx context.Context, tenantID string, ps domain.CustomerPaymentSetup) (domain.CustomerPaymentSetup, error) {
	// Only the stripe_customer_id mapping survives on this code
	// path. Card details, default-PM flag, and setup_status now
	// live on payment_methods (written via paymentmethods.Service
	// from the webhook attach path). The other fields on ps are
	// ignored — they're either redundant with payment_methods or
	// state-machine vestiges from the dropped summary table.
	if ps.StripeCustomerID != "" && ps.CustomerID != "" {
		if err := a.customers.SetStripeCustomerID(ctx, tenantID, ps.CustomerID, ps.StripeCustomerID); err != nil {
			return domain.CustomerPaymentSetup{}, err
		}
	}
	// Return the post-write composed view so callers see their
	// write reflected.
	return a.GetPaymentSetup(ctx, tenantID, ps.CustomerID)
}

// stripeCustomerResolverAdapter implements payment.CustomerByStripeIDResolver
// over the customer store. Lets the setup_intent.succeeded webhook map a
// SetupIntent's `customer` (a Stripe Customer ID) back to the Velox customer
// without payment/ importing customer/ — same composition-root pattern as the
// other adapters here.
type stripeCustomerResolverAdapter struct {
	customers *customer.PostgresStore
}

func (a *stripeCustomerResolverAdapter) CustomerIDByStripeID(ctx context.Context, tenantID, stripeCustomerID string) (string, error) {
	c, err := a.customers.GetByStripeCustomerID(ctx, tenantID, stripeCustomerID)
	if err != nil {
		return "", err
	}
	return c.ID, nil
}

// paymentReadinessAdapter implements billing.PaymentReadiness by
// combining customer.PostgresStore (for the Stripe Customer ID
// mapping) and paymentmethods.Service (for the canonical "has
// default active PM?" query). Replaces the legacy
// customer_payment_setups.GetPaymentSetup read path which lumped
// both facts into a single denorm-cache table. Single source of
// truth per concern:
//   - customers.stripe_customer_id is the customer ↔ Stripe Customer
//     mapping (1:1, lazy creation).
//   - payment_methods is the canonical multi-PM store; the default
//     row (is_default=true, detached_at IS NULL) is the chargeable
//     PM. List filters out detached rows server-side.
type paymentReadinessAdapter struct {
	customers *customer.PostgresStore
	pms       *paymentmethods.Service
}

func (a *paymentReadinessAdapter) ResolveForCharge(ctx context.Context, tenantID, customerID string) (string, string, error) {
	cust, err := a.customers.Get(ctx, tenantID, customerID)
	if err != nil {
		return "", "", err
	}
	if cust.StripeCustomerID == "" {
		// No Stripe Customer object yet — can't auto-charge.
		return "", "", nil
	}
	pms, err := a.pms.List(ctx, tenantID, customerID)
	if err != nil {
		return "", "", err
	}
	for _, pm := range pms {
		if pm.IsDefault {
			// Return the default card's Stripe PaymentMethod id so the
			// engine charges this exact card (not whatever Stripe's own
			// default happens to be). List filters detached rows, so this
			// is always a chargeable, non-detached PM.
			return cust.StripeCustomerID, pm.StripePaymentMethodID, nil
		}
	}
	// Stripe Customer exists but no default PM attached (e.g. customer
	// created via Stripe but card removed). Empty PM id → engine treats
	// this as "no PM ready" → queues for retry-on-attach.
	return cust.StripeCustomerID, "", nil
}

type invoiceWriterAdapter struct {
	store *invoice.PostgresStore
}

func (a *invoiceWriterAdapter) CreateInvoice(ctx context.Context, tenantID string, inv domain.Invoice) (domain.Invoice, error) {
	return a.store.Create(ctx, tenantID, inv)
}

func (a *invoiceWriterAdapter) CreateLineItem(ctx context.Context, tenantID string, item domain.InvoiceLineItem) (domain.InvoiceLineItem, error) {
	return a.store.CreateLineItem(ctx, tenantID, item)
}

func (a *invoiceWriterAdapter) ApplyCreditAmount(ctx context.Context, tenantID, id string, amountCents int64) (domain.Invoice, error) {
	return a.store.ApplyCredits(ctx, tenantID, id, amountCents)
}

func (a *invoiceWriterAdapter) GetInvoice(ctx context.Context, tenantID, id string) (domain.Invoice, error) {
	return a.store.Get(ctx, tenantID, id)
}

func (a *invoiceWriterAdapter) MarkPaid(ctx context.Context, tenantID, id string, stripePaymentIntentID string, paidAt time.Time) (domain.Invoice, error) {
	return a.store.MarkPaid(ctx, tenantID, id, stripePaymentIntentID, paidAt)
}

func (a *invoiceWriterAdapter) CreateInvoiceWithLineItems(ctx context.Context, tenantID string, inv domain.Invoice, items []domain.InvoiceLineItem) (domain.Invoice, error) {
	return a.store.CreateWithLineItems(ctx, tenantID, inv, items)
}

func (a *invoiceWriterAdapter) SetAutoChargePending(ctx context.Context, tenantID, id string, pending bool) error {
	return a.store.SetAutoChargePending(ctx, tenantID, id, pending)
}

func (a *invoiceWriterAdapter) ListAutoChargePending(ctx context.Context, limit int) ([]domain.Invoice, error) {
	return a.store.ListAutoChargePending(ctx, limit)
}

func (a *invoiceWriterAdapter) ListAutoChargePendingForClock(ctx context.Context, tenantID, clockID string, limit int) ([]domain.Invoice, error) {
	return a.store.ListAutoChargePendingForClock(ctx, tenantID, clockID, limit)
}

func (a *invoiceWriterAdapter) SetTaxTransaction(ctx context.Context, tenantID, id string, taxTransactionID string) error {
	return a.store.SetTaxTransaction(ctx, tenantID, id, taxTransactionID)
}

func (a *invoiceWriterAdapter) ListLineItems(ctx context.Context, tenantID, invoiceID string) ([]domain.InvoiceLineItem, error) {
	return a.store.ListLineItems(ctx, tenantID, invoiceID)
}

func (a *invoiceWriterAdapter) UpdateTaxAtomic(ctx context.Context, tenantID, invoiceID string, update domain.InvoiceTaxRetryUpdate, lineItems []domain.InvoiceLineItem) (domain.Invoice, error) {
	return a.store.UpdateTaxAtomic(ctx, tenantID, invoiceID, update, lineItems)
}

func (a *invoiceWriterAdapter) FindBaseInvoiceForPeriod(ctx context.Context, tenantID, subscriptionID string, periodStart time.Time) (domain.Invoice, error) {
	return a.store.FindBaseInvoiceForPeriod(ctx, tenantID, subscriptionID, periodStart)
}

func (a *invoiceWriterAdapter) FindFundingInvoicesForPeriod(ctx context.Context, tenantID, subscriptionID string, periodStart, periodEnd time.Time) ([]domain.Invoice, error) {
	return a.store.FindFundingInvoicesForPeriod(ctx, tenantID, subscriptionID, periodStart, periodEnd)
}

func (a *invoiceWriterAdapter) LatestThresholdPeriodEnd(ctx context.Context, tenantID, subscriptionID string, periodStart, periodEnd time.Time) (time.Time, error) {
	return a.store.LatestThresholdPeriodEnd(ctx, tenantID, subscriptionID, periodStart, periodEnd)
}

// creditGrantAdapter bridges credit.Service → creditnote.CreditGranter.
type creditGrantAdapter struct {
	svc *credit.Service
}

func (a *creditGrantAdapter) Grant(ctx context.Context, tenantID string, input creditnote.CreditGrantInput) error {
	_, err := a.svc.Grant(ctx, tenantID, credit.GrantInput{
		CustomerID:  input.CustomerID,
		AmountCents: input.AmountCents,
		Description: input.Description,
		InvoiceID:   input.InvoiceID,
	})
	return err
}

// GrantForCreditNote bridges to credit.Service.GrantForCreditNote so
// CN Issue() retries dedup via migration 0093's partial unique index.
func (a *creditGrantAdapter) GrantForCreditNote(ctx context.Context, tenantID, creditNoteID string, input creditnote.CreditGrantInput) error {
	_, err := a.svc.GrantForCreditNote(ctx, tenantID, creditNoteID, credit.GrantInput{
		CustomerID:  input.CustomerID,
		AmountCents: input.AmountCents,
		Description: input.Description,
		InvoiceID:   input.InvoiceID,
	})
	return err
}

// creditNoteListerAdapter bridges creditnote.Service → invoice.CreditNoteLister.
type creditNoteListerAdapter struct {
	svc *creditnote.Service
}

func (a *creditNoteListerAdapter) List(ctx context.Context, tenantID, invoiceID string) ([]domain.CreditNote, error) {
	return a.svc.List(ctx, creditnote.ListFilter{
		TenantID:  tenantID,
		InvoiceID: invoiceID,
	})
}

// refundIssuerAdapter bridges creditnote.Service → invoice.RefundIssuer.
// Translates the handler-facing invoice.RefundInput to the creditnote form;
// both types are near-identical by design so the handler doesn't have to
// import creditnote just to issue a refund.
type refundIssuerAdapter struct {
	svc *creditnote.Service
}

func (a *refundIssuerAdapter) IssueRefund(ctx context.Context, tenantID string, input invoice.RefundInput) (domain.CreditNote, error) {
	return a.svc.CreateRefund(ctx, tenantID, creditnote.RefundInput{
		InvoiceID:   input.InvoiceID,
		AmountCents: input.AmountCents,
		Reason:      input.Reason,
		Description: input.Description,
	})
}

// paymentRetrierAdapter bridges Stripe + invoice/customer stores → dunning.PaymentRetrier.
type paymentRetrierAdapter struct {
	charger       *payment.Stripe
	invoiceStore  *invoice.PostgresStore
	paymentSetups payment.PaymentSetupStore
	credits       *credit.Service
}

func (a *paymentRetrierAdapter) RetryPayment(ctx context.Context, tenantID, invoiceID, customerID string) error {
	inv, err := a.invoiceStore.Get(ctx, tenantID, invoiceID)
	if err != nil {
		return fmt.Errorf("get invoice: %w", err)
	}
	if inv.AmountDueCents <= 0 {
		return nil // Nothing to charge
	}

	// Re-apply customer credits before retrying the card — same contract as
	// the auto-charge sweep (processAutoCharge): credits granted since the
	// original failure (or whose application failed at cycle close) must
	// reduce the charge, not sit unconsumed while the card is hit for the
	// full amount. ApplyToInvoiceAt is idempotent (drains min(due, balance)).
	// An apply FAILURE maps to ErrTransientSkip: the retry never reaches
	// Stripe, so it must not burn a dunning attempt.
	if a.credits != nil {
		at := clock.Now(ctx) // sim-aware: catchup binds effective-now on ctx
		if _, err := a.credits.ApplyToInvoiceAt(ctx, tenantID, customerID, invoiceID, inv.AmountDueCents, at, inv.InvoiceNumber); err != nil {
			return dunning.ErrTransientSkip
		}
		inv, err = a.invoiceStore.Get(ctx, tenantID, invoiceID)
		if err != nil {
			return fmt.Errorf("refresh invoice after credit apply: %w", err)
		}
		if inv.AmountDueCents <= 0 {
			// Fully covered by credits — settle without a card charge. nil
			// return = recovered, so the dunning run resolves; MarkPaid fires
			// the transactional invoice.paid event.
			if _, err := a.invoiceStore.MarkPaid(ctx, tenantID, invoiceID, "", at); err != nil {
				return fmt.Errorf("mark credit-covered invoice paid: %w", err)
			}
			return nil
		}
	}

	ps, err := a.paymentSetups.GetPaymentSetup(ctx, tenantID, customerID)
	if err != nil || ps.StripeCustomerID == "" || ps.StripePaymentMethodID == "" {
		return fmt.Errorf("no payment method for customer")
	}

	// 15s bound on the Stripe leg. Scheduler ticks run tens of retries
	// back-to-back; without this, one tenant with a network-partitioned
	// Stripe call could hold the goroutine for the full request deadline
	// (minutes), starving every other tenant's retry this tick.
	chargeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	// Dedicated retry-charge method tags the PI as velox_purpose=
	// dunning_retry so the payment_intent.payment_failed webhook
	// handler suppresses its generic payment-failed email — dunning
	// fires its own warning / escalation email inline. Without the
	// tag the customer receives two emails per failed retry: the
	// webhook's "Payment failed for invoice X" plus dunning's
	// "Action required — payment retry for invoice X (Attempt N of M)".
	_, err = a.charger.ChargeInvoiceForDunningRetry(chargeCtx, tenantID, inv, ps.StripeCustomerID, ps.StripePaymentMethodID)
	// Map payment's internal "call never happened" sentinel to dunning's
	// equivalent. Keeps peer domains from importing each other and gives
	// dunning a stable signal to skip attempt_count bookkeeping.
	if errors.Is(err, payment.ErrPaymentTransient) {
		return dunning.ErrTransientSkip
	}
	return err
}

// subscriptionPauserAdapter bridges subscription.Service →
// dunning.SubscriptionPauser. ADR-036 amendment: this now calls
// PauseCollection(keep_as_draft), not the pre-amendment hard
// PauseAtomic — matching Stripe's pause_collection.behavior=
// keep_as_draft so the cycle keeps drafting (no silent skip of
// invoice generation).
type subscriptionPauserAdapter struct {
	svc *subscription.Service
}

func (a *subscriptionPauserAdapter) PauseCollection(ctx context.Context, tenantID, id string) error {
	_, err := a.svc.PauseCollection(ctx, tenantID, id, subscription.PauseCollectionInput{
		Behavior:    domain.PauseCollectionKeepAsDraft,
		TriggeredBy: "dunning",
	})
	return err
}

// subscriptionCancelerAdapter bridges subscription.Service →
// dunning.SubscriptionCanceler. Stripe-default dunning terminal
// action (ADR-036 amendment).
type subscriptionCancelerAdapter struct {
	svc *subscription.Service
}

func (a *subscriptionCancelerAdapter) Cancel(ctx context.Context, tenantID, id string) error {
	_, _, err := a.svc.Cancel(ctx, tenantID, id)
	return err
}

// invoiceUncollectibleAdapter bridges invoice.Service →
// dunning.InvoiceUncollectibleMarker. Stripe-standard dunning
// terminal action (ADR-036 amendment).
type invoiceUncollectibleAdapter struct {
	svc *invoice.Service
}

func (a *invoiceUncollectibleAdapter) MarkUncollectible(ctx context.Context, tenantID, id string) error {
	_, err := a.svc.MarkUncollectible(ctx, tenantID, id)
	return err
}

// dunningTimelineAdapter bridges dunning.Store → invoice.DunningTimelineFetcher.
type dunningTimelineAdapter struct {
	store *dunning.PostgresStore
}

func (a *dunningTimelineAdapter) ListRunsByInvoice(ctx context.Context, tenantID, invoiceID string) ([]domain.InvoiceDunningRun, error) {
	runs, _, err := a.store.ListRuns(ctx, dunning.RunListFilter{TenantID: tenantID, InvoiceID: invoiceID})
	return runs, err
}

func (a *dunningTimelineAdapter) ListEvents(ctx context.Context, tenantID, runID string) ([]domain.InvoiceDunningEvent, error) {
	return a.store.ListEvents(ctx, tenantID, runID)
}

// customerEmailFetcherAdapter bridges customer.PostgresStore → dunning.CustomerEmailFetcher + payment.CustomerEmailResolver.
type customerEmailFetcherAdapter struct {
	store *customer.PostgresStore
}

func (a *customerEmailFetcherAdapter) GetCustomerEmail(ctx context.Context, tenantID, customerID string) (string, string, error) {
	cust, err := a.store.Get(ctx, tenantID, customerID)
	if err != nil {
		return "", "", err
	}
	email := cust.Email
	name := cust.DisplayName
	// customers.email is the single canonical recipient (Phase 1 of the
	// dual-email collapse, migration 0100). Billing-profile legal_name
	// still wins for display because the operator types the document
	// display name there, but the send target is always cust.Email.
	if bp, err := a.store.GetBillingProfile(ctx, tenantID, customerID); err == nil {
		if bp.LegalName != "" {
			name = bp.LegalName
		}
	}
	return email, name, nil
}

// passwordResetEmailAdapter bridges email.OutboxSender (or the
// direct *email.Sender) to user.EmailSender's narrower 2-arg
// signature. The user package emits password-reset emails before
// the operator authenticates, so it has no tenant or display-name
// context — empty values flow through to email.Sender.brandingFor,
// which falls back to platform defaults. Production wires the
// outbox-backed sender so reset emails are durable.
type passwordResetEmailAdapter struct {
	sender interface {
		SendPasswordReset(ctx context.Context, tenantID, to, displayName, resetURL string) error
	}
}

func (a *passwordResetEmailAdapter) SendPasswordReset(ctx context.Context, tenantID, email, resetLink string) error {
	// Email-as-display-name: body shows "Hi alice@example.com"
	// rather than "Hi Alice". Acceptable trade for not threading
	// the user's display_name through the password-reset request
	// flow. tenantID comes from user.Service.IssueResetToken so the
	// outbox row stamps the right tenant for operator visibility.
	return a.sender.SendPasswordReset(ctx, tenantID, email, email, resetLink)
}

// invoiceEmailEventsAdapter bridges email.OutboxStore.ListByInvoice
// → invoice.EmailEventLister so the invoice timeline can surface
// customer-notification email rows alongside Stripe webhooks and
// dunning events. Pure shape conversion; the underlying query
// already filters to invoice-relevant email types.
type invoiceEmailEventsAdapter struct {
	store *email.OutboxStore
}

func (a *invoiceEmailEventsAdapter) ListByInvoice(ctx context.Context, tenantID, invoiceNumber string) ([]invoice.EmailEventRow, error) {
	rows, err := a.store.ListByInvoice(ctx, tenantID, invoiceNumber)
	if err != nil {
		return nil, err
	}
	out := make([]invoice.EmailEventRow, 0, len(rows))
	for _, r := range rows {
		to, _ := r.Payload["to"].(string)
		out = append(out, invoice.EmailEventRow{
			EmailType:    r.EmailType,
			Status:       r.Status,
			CreatedAt:    r.CreatedAt,
			DispatchedAt: r.DispatchedAt,
			LastError:    r.LastError,
			To:           to,
		})
	}
	return out, nil
}

// paymentSetupEmailSender is the narrow interface noPaymentMethodNotifierAdapter
// needs from email/. Defined here (consumer-side) so the email package
// doesn't bleed an interface name into the api/payment dep graph.
// Satisfied by both *email.Sender and *email.OutboxSender.
type paymentSetupEmailSender interface {
	SendPaymentSetupRequest(ctx context.Context, tenantID, to, customerName, invoiceNumber string, amountDueCents int64, currency, updateURL string) error
}

// customerSentEmailsAdapter bridges email.OutboxStore.ListByCustomer →
// customer.SentEmailsLister. Pure shape conversion plus payload field
// extraction (recipient, invoice_number) so the customer package
// doesn't import the email package.
type customerSentEmailsAdapter struct {
	store *email.OutboxStore
}

func (a *customerSentEmailsAdapter) ListByCustomer(ctx context.Context, tenantID, customerID string) ([]customer.SentEmailOutboxRow, error) {
	rows, err := a.store.ListByCustomer(ctx, tenantID, customerID)
	if err != nil {
		return nil, err
	}
	out := make([]customer.SentEmailOutboxRow, 0, len(rows))
	for _, r := range rows {
		to, _ := r.Payload["to"].(string)
		invNum, _ := r.Payload["invoice_number"].(string)
		out = append(out, customer.SentEmailOutboxRow{
			ID:            r.ID,
			EmailType:     r.EmailType,
			Recipient:     to,
			Status:        r.Status,
			LastError:     r.LastError,
			CreatedAt:     r.CreatedAt,
			DispatchedAt:  r.DispatchedAt,
			InvoiceNumber: invNum,
		})
	}
	return out, nil
}

// noPaymentMethodNotifierAdapter bridges email.SendPaymentSetupRequest
// into billing.NoPaymentMethodNotifier so the engine can dispatch a
// "set up your payment method" email at finalize when the customer
// has no PM ready. Distinct from the post-decline path
// (Stripe.handlePaymentFailed) which points the customer at the
// hosted invoice URL — this email mints a single-use token for the
// /update-payment SPA route. ADR-013, ADR-023.
//
// The token-then-SPA indirection is retained (vs. embedding the Stripe
// Checkout URL directly) because the SPA validates the customer-scoped
// token first, surfaces invoice context to the customer, and only THEN
// mints a Checkout session. That two-step is the right shape for an
// email link the customer clicks days after issuance: the operator can
// invalidate tokens at any time, and the SPA shows "for invoice X, $Y"
// before sending the customer to Stripe.
type noPaymentMethodNotifierAdapter struct {
	email            paymentSetupEmailSender
	customerEmail    *customerEmailFetcherAdapter
	paymentUpdateURL string
	tokenSvc         *payment.TokenService
	auditLogger      *audit.Logger // optional — engine fires this background task; audit row records the send for operator forensics.
}

func (a *noPaymentMethodNotifierAdapter) NotifyNoPaymentMethod(ctx context.Context, tenantID string, inv domain.Invoice) error {
	if a.paymentUpdateURL == "" {
		return errors.New("PAYMENT_UPDATE_URL not configured")
	}
	if a.tokenSvc == nil {
		// The SPA enforces ?token= and rejects URLs without it
		// ("Link expired or invalid · No payment update token
		// provided"). Refusing to send is strictly better than
		// emailing the customer a permanently-broken link.
		return errors.New("payment update TokenService not wired")
	}
	to, name, err := a.customerEmail.GetCustomerEmail(ctx, tenantID, inv.CustomerID)
	if err != nil || to == "" {
		// Missing email is a delivery gap, not a billing failure.
		// Engine logs the warning and continues.
		return nil
	}
	rawToken, err := a.tokenSvc.Create(ctx, tenantID, inv.CustomerID, inv.ID)
	if err != nil {
		return fmt.Errorf("create payment update token: %w", err)
	}
	updateURL := fmt.Sprintf("%s?token=%s", a.paymentUpdateURL, rawToken)
	if err := a.email.SendPaymentSetupRequest(ctx, tenantID, to, name, inv.InvoiceNumber, inv.AmountDueCents, inv.Currency, updateURL); err != nil {
		return err
	}
	// Audit the engine-fired send so the operator can answer
	// "did we email the customer at finalize?" from the AuditLog page
	// without grepping outbox tables. Mirrors the operator-driven
	// setup_link_sent audit row written by paymentmethods.Handler.
	if a.auditLogger != nil {
		// No recipient address in the append-only row (GDPR erasure) — the
		// email outbox holds the delivery record; the row links the customer.
		_ = a.auditLogger.Log(ctx, tenantID, "update", "customer", inv.CustomerID, name, map[string]any{
			"action":         "setup_link_sent",
			"invoice_id":     inv.ID,
			"invoice_number": inv.InvoiceNumber,
			"trigger":        "finalize_no_pm",
		})
	}
	return nil
}

// prorationCreditGranterAdapter bridges credit.Service → subscription.ProrationCreditGranter.
type prorationCreditGranterAdapter struct {
	svc *credit.Service
}

func (a *prorationCreditGranterAdapter) GrantProration(ctx context.Context, tenantID string, input subscription.ProrationGrantInput) error {
	planChangedAt := input.SourcePlanChangedAt
	_, err := a.svc.Grant(ctx, tenantID, credit.GrantInput{
		CustomerID:               input.CustomerID,
		AmountCents:              input.AmountCents,
		Description:              input.Description,
		SourceSubscriptionID:     input.SourceSubscriptionID,
		SourceSubscriptionItemID: input.SourceSubscriptionItemID,
		SourcePlanChangedAt:      &planChangedAt,
		SourceChangeType:         input.SourceChangeType,
	})
	return err
}

func (a *prorationCreditGranterAdapter) GetByProrationSource(ctx context.Context, tenantID, subscriptionID, subscriptionItemID string, changeType domain.ItemChangeType, changeAt time.Time) (domain.CreditLedgerEntry, error) {
	return a.svc.GetByProrationSource(ctx, tenantID, subscriptionID, subscriptionItemID, changeType, changeAt)
}

// GrantProrationTx is the in-transaction variant. The caller owns the
// tx; this adapter forwards to the credit service's tx-aware grant
// path. ADR-030 atomic-proration follow-through (2026-05-29).
func (a *prorationCreditGranterAdapter) GrantProrationTx(ctx context.Context, tx *sql.Tx, tenantID string, input subscription.ProrationGrantInput) error {
	planChangedAt := input.SourcePlanChangedAt
	_, err := a.svc.GrantTx(ctx, tx, tenantID, credit.GrantInput{
		CustomerID:               input.CustomerID,
		AmountCents:              input.AmountCents,
		Description:              input.Description,
		SourceSubscriptionID:     input.SourceSubscriptionID,
		SourceSubscriptionItemID: input.SourceSubscriptionItemID,
		SourcePlanChangedAt:      &planChangedAt,
		SourceChangeType:         input.SourceChangeType,
	})
	return err
}

// prorationInvoiceCreatorAdapter bridges invoice.PostgresStore + tenant.SettingsStore → subscription.ProrationInvoiceCreator.
type prorationInvoiceCreatorAdapter struct {
	store    *invoice.PostgresStore
	numberer invoice.InvoiceNumberer
}

func (a *prorationInvoiceCreatorAdapter) CreateInvoiceWithLineItems(ctx context.Context, tenantID string, inv domain.Invoice, items []domain.InvoiceLineItem) (domain.Invoice, error) {
	return a.store.CreateWithLineItems(ctx, tenantID, inv, items)
}

func (a *prorationInvoiceCreatorAdapter) GetByProrationSource(ctx context.Context, tenantID, subscriptionID, subscriptionItemID string, changeType domain.ItemChangeType, changeAt time.Time) (domain.Invoice, error) {
	return a.store.GetByProrationSource(ctx, tenantID, subscriptionID, subscriptionItemID, changeType, changeAt)
}

func (a *prorationInvoiceCreatorAdapter) NextInvoiceNumber(ctx context.Context, tenantID string) (string, error) {
	return a.numberer.NextInvoiceNumber(ctx, tenantID)
}

func (a *prorationInvoiceCreatorAdapter) FindBaseInvoiceForPeriod(ctx context.Context, tenantID, subscriptionID string, periodStart time.Time) (domain.Invoice, error) {
	return a.store.FindBaseInvoiceForPeriod(ctx, tenantID, subscriptionID, periodStart)
}

func (a *prorationInvoiceCreatorAdapter) FindFundingInvoicesForPeriod(ctx context.Context, tenantID, subscriptionID string, periodStart, periodEnd time.Time) ([]domain.Invoice, error) {
	return a.store.FindFundingInvoicesForPeriod(ctx, tenantID, subscriptionID, periodStart, periodEnd)
}

// CreateInvoiceWithLineItemsTx + NextInvoiceNumberTx are the
// in-transaction variants used by the atomic AddItem-with-proration
// flow. The caller (subscription.Handler) owns the tx; this adapter
// just forwards to the underlying store + numberer's Tx methods.
// ADR-030 atomic-proration follow-through (2026-05-29).
func (a *prorationInvoiceCreatorAdapter) CreateInvoiceWithLineItemsTx(ctx context.Context, tx *sql.Tx, tenantID string, inv domain.Invoice, items []domain.InvoiceLineItem) (domain.Invoice, error) {
	return a.store.CreateWithLineItemsTx(ctx, tx, tenantID, inv, items)
}

func (a *prorationInvoiceCreatorAdapter) NextInvoiceNumberTx(ctx context.Context, tx *sql.Tx, tenantID string) (string, error) {
	return a.numberer.NextInvoiceNumberTx(ctx, tx, tenantID)
}

// SetAutoChargePending enrolls a finalized proration charge invoice into the
// auto-charge sweep (forwards to the store's existing method — the same one the
// engine's finalize path uses).
func (a *prorationInvoiceCreatorAdapter) SetAutoChargePending(ctx context.Context, tenantID, id string, pending bool) error {
	return a.store.SetAutoChargePending(ctx, tenantID, id, pending)
}

// prorationTaxApplierAdapter bridges billing.Engine → subscription.ProrationTaxApplier.
// Narrow translation: same signature, different named return type so the
// subscription package doesn't import billing.
type prorationTaxApplierAdapter struct {
	engine *billing.Engine
}

func (a *prorationTaxApplierAdapter) ApplyTaxToLineItems(ctx context.Context, tenantID, customerID, currency string, subtotal, discount int64, lineItems []domain.InvoiceLineItem) (subscription.ProrationTaxResult, error) {
	r, err := a.engine.ApplyTaxToLineItems(ctx, tenantID, customerID, currency, subtotal, discount, lineItems)
	if err != nil {
		return subscription.ProrationTaxResult{}, err
	}
	return subscription.ProrationTaxResult{
		TaxAmountCents:   r.TaxAmountCents,
		TaxRate:          r.TaxRate,
		TaxName:          r.TaxName,
		TaxCountry:       r.TaxCountry,
		TaxID:            r.TaxID,
		TaxProvider:      r.TaxProvider,
		TaxCalculationID: r.TaxCalculationID,
		TaxReverseCharge: r.TaxReverseCharge,
		TaxExemptReason:  r.TaxExemptReason,
		SubtotalCents:    r.SubtotalCents,
		DiscountCents:    r.DiscountCents,
		TaxStatus:        r.TaxStatus,
	}, nil
}

// CommitTax commits a provider tax calculation into a reportable tax
// transaction, delegating to the same engine method the cycle/create paths
// use. No-op for manual/none providers (no calculation id).
func (a *prorationTaxApplierAdapter) CommitTax(ctx context.Context, tenantID, invoiceID, calculationID string) error {
	return a.engine.CommitTax(ctx, tenantID, invoiceID, calculationID)
}

// pmCustomerLookupAdapter bridges customer.PostgresStore →
// paymentmethods.CustomerLookup. paymentmethods doesn't import
// customer (keeps the dep graph one-way); router.go composes the
// adapter so the operator send-setup-email endpoint can resolve the
// recipient address + display name from the canonical customer row.
type pmCustomerLookupAdapter struct {
	store *customer.PostgresStore
}

func (a *pmCustomerLookupAdapter) GetForSetupLink(ctx context.Context, tenantID, customerID string) (email, displayName string, err error) {
	c, err := a.store.Get(ctx, tenantID, customerID)
	if err != nil {
		return "", "", err
	}
	return c.Email, c.DisplayName, nil
}

// bounceReporterAdapter bridges email.Sender → customer.Service. When
// SMTP rejects a send with a permanent-failure (5xx), the Sender calls
// ReportBounce with (tenantID, email, reason). This adapter resolves
// the email via the blind-index lookup and flips email_status on every
// matching customer in that tenant. Multiple matches can happen when
// two customers share an email (rare; we mark all).
//
// Lives in internal/api so email doesn't import customer — keeps the
// one-way-coupled layering intact.
type bounceReporterAdapter struct {
	blinder *crypto.Blinder
	store   *customer.PostgresStore
	svc     *customer.Service
}

func (a *bounceReporterAdapter) ReportBounce(ctx context.Context, tenantID, email, reason string) {
	if a == nil || a.store == nil || a.svc == nil || a.blinder == nil || email == "" {
		return
	}
	blind := a.blinder.Blind(email)
	if blind == "" {
		return
	}
	matches, err := a.store.FindByEmailBlindIndex(ctx, blind, 10)
	if err != nil {
		return
	}
	for _, m := range matches {
		if m.TenantID != tenantID {
			continue
		}
		if err := a.svc.MarkEmailBounced(ctx, tenantID, m.CustomerID, reason); err != nil {
			// Swallow — the sender has already logged the upstream bounce.
			// Failure here just means the badge won't update, not a
			// correctness issue.
			_ = err
		}
	}
}

// suppressionCheckerAdapter is the inverse of bounceReporterAdapter:
// given (tenant, email), it answers "is this recipient suppressable
// because we've already seen them bounce or complain?". Both
// email.Sender and email.OutboxSender consult this before every send
// so bounced addresses don't get hammered with retry-DLQ noise.
//
// Returns the first matching customer's email_status; if multiple
// customers share the same email (rare) and any one is suppressed,
// the address is treated as suppressed for that tenant. Better to
// suppress one legitimate send than to keep mailing a dead address.
type suppressionCheckerAdapter struct {
	blinder *crypto.Blinder
	store   *customer.PostgresStore
}

func (a *suppressionCheckerAdapter) IsSuppressed(ctx context.Context, tenantID, emailAddr string) (bool, string, error) {
	if a == nil || a.store == nil || a.blinder == nil || emailAddr == "" || tenantID == "" {
		return false, "", nil
	}
	blind := a.blinder.Blind(emailAddr)
	if blind == "" {
		return false, "", nil
	}
	matches, err := a.store.FindByEmailBlindIndex(ctx, blind, 10)
	if err != nil {
		return false, "", err
	}
	for _, m := range matches {
		if m.TenantID != tenantID {
			continue
		}
		c, err := a.store.Get(ctx, tenantID, m.CustomerID)
		if err != nil {
			continue
		}
		switch c.EmailStatus {
		case domain.EmailStatusBounced:
			return true, "bounced: " + c.EmailBounceReason, nil
		case domain.EmailStatusComplained:
			return true, "complained", nil
		}
	}
	return false, "", nil
}

// stripeCustomerEnsurer resolves-or-creates the Stripe customer for a
// Velox customer + persists the mapping. Satisfied by
// *paymentmethods.StripeAdapter — kept as a one-method interface here
// so api/adapters.go doesn't need to import paymentmethods directly.
type stripeCustomerEnsurer interface {
	EnsureStripeCustomer(ctx context.Context, tenantID, customerID string) (string, error)
}

// hostedInvoiceStripeAdapter bridges *payment.StripeClients →
// hostedinvoice.CheckoutSessionCreator. The caller is a public
// unauthenticated request: livemode is read from the request ctx
// (pinned by hostedinvoice.resolveInvoice from the GetByPublicToken
// row), then we pick the matching Stripe key and build a Checkout
// Session in payment mode with a single pre-totaled line item.
// Velox owns the tax computation, so Stripe's automatic_tax is
// intentionally off — the UnitAmount already includes tax from the
// invoice row.
//
// Metadata stamps velox_purpose=hosted_invoice_pay so the existing
// payment-intent webhook path can route successful charges to
// Invoice.RecordPayment via invoice_id lookup, instead of mis-identifying
// these as subscription-billing charges.
//
// `ensurer` resolves or mints the Stripe customer so the Checkout
// session can attach the saved card to it; without `customer` +
// `setup_future_usage=off_session` Stripe takes the card but never
// links it to the Velox customer's Stripe customer record, so the
// "save card" tick on the hosted page would be a no-op.
type hostedInvoiceStripeAdapter struct {
	clients *payment.StripeClients
	ensurer stripeCustomerEnsurer
}

func (a *hostedInvoiceStripeAdapter) CreateInvoicePaymentSession(
	ctx context.Context, tenantID string, inv domain.Invoice, successURL, cancelURL string,
) (string, error) {
	if a == nil || a.clients == nil {
		return "", fmt.Errorf("stripe not configured")
	}
	// Livemode comes from ctx, pinned by resolveInvoice off the
	// public_token lookup. Previous version did a raw db.Pool query
	// on `invoices` which has RLS — without app.tenant_id /
	// app.bypass_rls set on the session, the policy returned zero
	// rows ("resolve livemode: sql: no rows in result set").
	livemode := postgres.Livemode(ctx)
	sc := a.clients.For(ctx, tenantID, livemode)
	if sc == nil {
		return "", fmt.Errorf("stripe not configured for mode livemode=%v", livemode)
	}
	if a.ensurer == nil {
		return "", fmt.Errorf("hosted invoice: Stripe customer ensurer not wired")
	}
	stripeCustomerID, err := a.ensurer.EnsureStripeCustomer(ctx, tenantID, inv.CustomerID)
	if err != nil {
		return "", fmt.Errorf("ensure stripe customer: %w", err)
	}
	currency := strings.ToLower(inv.Currency)
	productName := "Invoice " + inv.InvoiceNumber
	// Duplicate the metadata on BOTH the session and the PaymentIntent.
	// Session-level metadata only lives on the checkout_session object;
	// Stripe does NOT automatically copy it to the underlying PaymentIntent.
	// Our webhook path routes payment_intent.succeeded → Invoice.MarkPaid
	// by reading velox_invoice_id off the PI's metadata (payment/handler.go
	// line 195), so PaymentIntentData.Metadata is the one that actually
	// matters. The session copy is kept for operator visibility when
	// inspecting a checkout session directly in Stripe dashboard.
	meta := map[string]string{
		"velox_invoice_id":  inv.ID,
		"velox_tenant_id":   tenantID,
		"velox_customer_id": inv.CustomerID,
		"velox_purpose":     "hosted_invoice_pay",
	}
	// `customer` + `setup_future_usage=off_session` is the canonical
	// Stripe pattern for "charge now, save the card for auto-charge of
	// future invoices" (https://docs.stripe.com/payments/save-during-payment).
	// Without `customer`, Stripe creates a guest checkout and the
	// PaymentMethod is never attached to anything. Without
	// `setup_future_usage`, the card is consumed by the one-shot PI and
	// detached. Both are required for dunning auto-retry to work.
	sess, err := sc.V1CheckoutSessions.Create(ctx, &stripe.CheckoutSessionCreateParams{
		Customer: stripe.String(stripeCustomerID),
		Mode:     stripe.String(string(stripe.CheckoutSessionModePayment)),
		LineItems: []*stripe.CheckoutSessionCreateLineItemParams{
			{
				Quantity: stripe.Int64(1),
				PriceData: &stripe.CheckoutSessionCreateLineItemPriceDataParams{
					Currency:   stripe.String(currency),
					UnitAmount: stripe.Int64(inv.AmountDueCents),
					ProductData: &stripe.CheckoutSessionCreateLineItemPriceDataProductDataParams{
						Name: stripe.String(productName),
					},
				},
			},
		},
		PaymentMethodTypes: stripe.StringSlice([]string{"card"}),
		SuccessURL:         stripe.String(successURL),
		CancelURL:          stripe.String(cancelURL),
		PaymentIntentData: &stripe.CheckoutSessionCreatePaymentIntentDataParams{
			Metadata:         meta,
			Description:      stripe.String(productName),
			SetupFutureUsage: stripe.String("off_session"),
		},
		Params: stripe.Params{
			Metadata: meta,
		},
	})
	if err != nil {
		return "", fmt.Errorf("checkout session: %w", err)
	}
	return sess.URL, nil
}
