package invoice

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/clock"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/tax"
)

// InvoiceNumberer allocates the next invoice number for a tenant.
// Atomicity and uniqueness are the numberer's responsibility.
type InvoiceNumberer interface {
	NextInvoiceNumber(ctx context.Context, tenantID string) (string, error)
	// NextInvoiceNumberTx is the in-transaction variant used by the
	// atomic AddItem-with-proration flow — allocate the number and
	// insert the invoice in one tx so a rollback frees the number.
	NextInvoiceNumberTx(ctx context.Context, tx *sql.Tx, tenantID string) (string, error)
}

// TaxCommitter finalizes an upstream tax calculation into a tax_transaction
// (Stripe Tax) at invoice finalize time. Optional: when nil, finalize proceeds
// without a tax commit — fine for manual/none providers or when the invoice
// has no CalculationID. Failures here do NOT block finalize; they only get
// logged, because invoice finalize must remain idempotent and a transient
// Stripe error shouldn't leave the invoice stuck in draft.
type TaxCommitter interface {
	CommitTax(ctx context.Context, tenantID, invoiceID, calculationID string) error
}

// TaxReverser issues a reversal of the invoice's committed tax transaction
// when the invoice is voided. Without this call, finalized-but-unpaid
// invoices that get voided would leave the tax transaction committed
// upstream — over-reporting tax collected to the authority. Optional —
// when unset, Void proceeds without the upstream reversal (suitable for
// none/manual providers and for narrow tests). Failures here log but
// do not block the void.
type TaxReverser interface {
	ReverseTax(ctx context.Context, tenantID string, req tax.ReversalRequest) (*tax.ReversalResult, error)
}

// TaxRetrier is the narrow view into tax recompute + persistence the
// retry-tax endpoint depends on. Satisfied by billing.Engine in
// production — the contract lives next to the handler that calls it.
type TaxRetrier interface {
	RetryTaxForInvoice(ctx context.Context, tenantID, invoiceID string) (domain.Invoice, error)
	// ComputeTaxForInvoice computes tax for a draft invoice regardless of
	// tax_status. Finalize calls this for manual / operator-composed
	// invoices, which accrue line items incrementally and have no tax until
	// finalize (Stripe-parity: tax is calculated when the invoice finalizes).
	ComputeTaxForInvoice(ctx context.Context, tenantID, invoiceID string) (domain.Invoice, error)
}

// PaymentMethodReader is the narrow lookup the attention classifier
// uses to decide between no_payment_method (operator-actionable) and
// awaiting_payment (engine race window). Satisfied by the existing
// payment.PaymentSetupStore — kept as a local interface so this
// package doesn't import internal/payment.
type PaymentMethodReader interface {
	GetPaymentSetup(ctx context.Context, tenantID, customerID string) (domain.CustomerPaymentSetup, error)
}

// StripeChecker reports whether a tenant has Stripe credentials
// connected for a given mode. Used by the attention classifier to
// distinguish two states that previously rendered identically on
// invoices stuck at tax_status='pending' with code
// 'provider_not_configured':
//   - Stripe NOT connected → operator action required.
//   - Stripe IS now connected (just connected, scheduler hasn't
//     ticked yet) → calculation will retry shortly; offer Retry now.
//
// Implemented by *payment.StripeClients via HasFor.
type StripeChecker interface {
	HasFor(ctx context.Context, tenantID string, livemode bool) bool
}

// CustomerClockReader reads a customer's test-clock pin so Create can stamp
// invoice.is_simulated authoritatively at write time — the same direct field
// check the engine uses for cycle invoices (sub.TestClockID != ""), applied to
// the customer for manual one-off invoices. Satisfied by
// *customer.PostgresStore.
//
// This is the WRITE-time capture, NOT the read-time snapshot heuristic that
// ADR-030 bans: we record whether the customer was pinned at the instant the
// invoice was born, then persist it. (We can't use clock.IsSimulated(ctx) here
// — bindForCreate binds ctx to the resolver's effective-now even for UNPINNED
// customers, so "ctx is bound" doesn't mean "on a test clock".)
type CustomerClockReader interface {
	Get(ctx context.Context, tenantID, customerID string) (domain.Customer, error)
}

// TenantSettingsReader reads tenant settings so a manual invoice with no
// explicit net term can fall back to the tenant's configured default —
// mirroring the cycle engine, which reads settings.NetPaymentTerms. Optional;
// nil falls straight through to the hardcoded 30-day default.
type TenantSettingsReader interface {
	Get(ctx context.Context, tenantID string) (domain.TenantSettings, error)
}

type Service struct {
	store          Store
	clock          clock.Clock
	resolver       clock.Resolver
	numberer       InvoiceNumberer
	taxCommitter   TaxCommitter
	taxReverser    TaxReverser
	taxRetrier     TaxRetrier
	paymentMethods PaymentMethodReader
	stripeChecker  StripeChecker
	customerClock  CustomerClockReader
	settings       TenantSettingsReader
	audit          AuditLogger
	events         domain.EventDispatcher
}

// AuditLogger is the narrow audit-write interface the service uses to
// record state-changing operations that are reachable from multiple
// entry points (HTTP handler + dunning adapter MarkUncollectible
// final-action + the ResolveRun(invoice_not_collectible) operator
// flow). Same pattern as subscription.Service — single canonical
// audit row regardless of which path triggered the transition.
type AuditLogger interface {
	Log(ctx context.Context, tenantID, action, resourceType, resourceID, resourceLabel string, metadata map[string]any) error
}

// SetTenantSettingsReader wires the tenant-settings reader used to default a
// manual invoice's net payment terms when the caller omits them. Optional.
func (s *Service) SetTenantSettingsReader(r TenantSettingsReader) { s.settings = r }

// SetAuditLogger wires the audit logger.
func (s *Service) SetAuditLogger(l AuditLogger) { s.audit = l }

// SetEventDispatcher wires outbound webhook dispatch for state-
// transitioning service methods (MarkUncollectible, RecordPayment).
// Optional — unset paths skip dispatch.
func (s *Service) SetEventDispatcher(d domain.EventDispatcher) { s.events = d }

func NewService(store Store, clk clock.Clock, numberer InvoiceNumberer) *Service {
	if clk == nil {
		clk = clock.Real()
	}
	return &Service{store: store, clock: clk, numberer: numberer}
}

// SetTaxCommitter wires the upstream tax-transaction committer. Called from
// router.go with the billing engine so finalize can commit Stripe tax calcs.
func (s *Service) SetTaxCommitter(tc TaxCommitter) {
	s.taxCommitter = tc
}

// SetTaxReverser wires the upstream tax-transaction reverser. Called from
// router.go with the billing engine so Void on a finalized-but-unpaid
// invoice can reverse the tax_transaction committed at finalize time.
// Without this, voided invoices leave their tax recorded as collected
// with the authority — over-reporting tax revenue.
func (s *Service) SetTaxReverser(tr TaxReverser) {
	s.taxReverser = tr
}

// SetResolver wires the unified clock.Resolver used to bind
// effective-now at invoice service entry points. Once bound on ctx
// via clock.BindEffectiveNow, every downstream s.clock.Now(ctx)
// (including in the postgres store's INSERT-time stamps) reads
// frozen_time on clock-pinned entities. Optional: nil leaves binding
// off and every callsite reads wall-clock — the test-friendly default.
//
// Replaces the per-service ClockResolver pattern shipped during the
// post-ADR-029 patches; matches Stripe's "no semantic change"
// guarantee for billing-engine resources.
func (s *Service) SetResolver(r clock.Resolver) {
	s.resolver = r
}

// bindForCreate binds effective-now at invoice-create time. Prefers
// the subscription pin when set (more specific), else falls back to
// the customer pin. Returns ctx unchanged on resolver error or
// missing ids — wall-clock fallback at every downstream callsite.
func (s *Service) bindForCreate(ctx context.Context, tenantID string, input CreateInput) context.Context {
	pin := clock.Pin{TenantID: tenantID}
	switch {
	case input.SubscriptionID != "":
		pin.SubscriptionID = input.SubscriptionID
	case input.CustomerID != "":
		pin.CustomerID = input.CustomerID
	default:
		return ctx
	}
	bound, _ := clock.BindEffectiveNow(ctx, s.resolver, pin)
	return bound
}

// bindForInvoice binds effective-now from an invoice id. Used by
// every per-invoice mutation entry point (Finalize, Void,
// RecordPayment, RetryTax, etc.) so downstream stamps
// inherit simulated time.
func (s *Service) bindForInvoice(ctx context.Context, tenantID, invoiceID string) context.Context {
	bound, _ := clock.BindEffectiveNow(ctx, s.resolver, clock.Pin{TenantID: tenantID, InvoiceID: invoiceID})
	return bound
}

// SetTaxRetrier wires the orchestrator behind the retry-tax endpoint.
// Production passes billing.Engine; tests can pass any implementation.
func (s *Service) SetTaxRetrier(r TaxRetrier) {
	s.taxRetrier = r
}

// SetPaymentMethodReader wires the customer payment-setup lookup used
// by the attention classifier to surface no_payment_method distinctly
// from generic awaiting_payment. Optional — when nil, the classifier
// falls back to the awaiting_payment branch (less specific but still
// correct for healthy/transient cases).
func (s *Service) SetPaymentMethodReader(r PaymentMethodReader) {
	s.paymentMethods = r
}

// SetCustomerClockReader wires the customer lookup Create uses to stamp
// is_simulated from the customer's test-clock pin. Optional — when nil (narrow
// unit tests), is_simulated defaults false, which is correct for any
// non-clock-pinned invoice; production always wires it.
func (s *Service) SetCustomerClockReader(r CustomerClockReader) {
	s.customerClock = r
}

// customerOnTestClock reports whether the customer is pinned to a test clock —
// the authoritative write-time signal for invoice.is_simulated on manual
// invoices (mirrors the engine's sub.TestClockID check for cycle invoices).
// Lookup failure / unwired reader → false (safe: an unbadged simulated invoice
// is better than a badged real one, and the reader is always wired in prod).
func (s *Service) customerOnTestClock(ctx context.Context, tenantID, customerID string) bool {
	if s.customerClock == nil || customerID == "" {
		return false
	}
	cust, err := s.customerClock.Get(ctx, tenantID, customerID)
	if err != nil {
		return false
	}
	return cust.TestClockID != ""
}

// SetStripeChecker wires the per-tenant Stripe-connected probe used
// by the attention classifier to distinguish "Stripe not connected"
// from "Stripe just connected, calculation will retry shortly" on
// tax_status='pending' invoices. Optional — when nil, the classifier
// falls through the existing "Stripe isn't connected" copy (the
// pre-fix behaviour) which is safe-but-less-helpful for the
// gap-window case.
func (s *Service) SetStripeChecker(c StripeChecker) {
	s.stripeChecker = c
}

type CreateInput struct {
	CustomerID         string    `json:"customer_id"`
	SubscriptionID     string    `json:"subscription_id"`
	Currency           string    `json:"currency"`
	BillingPeriodStart time.Time `json:"billing_period_start"`
	BillingPeriodEnd   time.Time `json:"billing_period_end"`
	// NetPaymentTermDays is a pointer so the service can distinguish
	// "omitted" (nil → fall back to the tenant's configured net terms, then
	// 30) from an explicit 0 ("Due on receipt" — a valid choice the composer
	// offers). A plain int conflated the two, silently turning Due-on-receipt
	// into Net 30.
	NetPaymentTermDays *int   `json:"net_payment_term_days,omitempty"`
	Memo               string `json:"memo,omitempty"`
	// LineItems, when present, are created atomically with the invoice
	// header in a single transaction. The operator composer sends them
	// this way so a network failure mid-compose can't leave a draft with
	// a partial set of lines. Omitted (nil) keeps the bare-header create
	// for callers that add lines incrementally afterwards.
	LineItems []AddLineItemInput `json:"line_items,omitempty"`
}

func (s *Service) Create(ctx context.Context, tenantID string, input CreateInput) (domain.Invoice, error) {
	if input.CustomerID == "" {
		return domain.Invoice{}, errs.Required("customer_id")
	}
	// SubscriptionID is OPTIONAL: cycle invoices carry a subscription, one-off
	// invoices (operator composer, ad-hoc charges) do not. The DB column is
	// nullable as of migration 0060; the cycle-idempotency partial unique
	// index ignores NULLs so two one-off invoices can coexist for the same
	// (customer, period) without colliding.

	// Uppercase ISO-4217 — the canonical case across the system: the tenant
	// default currency is "USD" (tenant.DefaultSettings) and the analytics /
	// dunning revenue-by-currency queries filter with a case-SENSITIVE
	// `currency = $1` against it, so a stored "usd" would silently drop out
	// of revenue/at-risk sums. The store insert normalizes again as the
	// single chokepoint for all callers (incl. the cycle engine's "usd"
	// fallback); doing it here too keeps the in-memory struct consistent.
	currency := strings.ToUpper(strings.TrimSpace(input.Currency))
	if currency == "" {
		currency = "USD"
	}

	// Resolve net payment terms. An explicit value (including 0 = "Due on
	// receipt") is honored verbatim. When omitted, fall back to the tenant's
	// configured net terms, then to 30 — mirroring the cycle engine
	// (billOnePeriod reads settings.NetPaymentTerms, defaulting to 30). A
	// negative value is clamped to 0.
	netDays := 30
	switch {
	case input.NetPaymentTermDays != nil:
		netDays = *input.NetPaymentTermDays
		if netDays < 0 {
			netDays = 0
		}
	case s.settings != nil:
		if ts, err := s.settings.Get(ctx, tenantID); err == nil && ts.NetPaymentTerms > 0 {
			netDays = ts.NetPaymentTerms
		}
	}

	ctx = s.bindForCreate(ctx, tenantID, input)
	now := s.clock.Now(ctx)
	// One-off invoices that omit a billing window default to "now → now" so
	// the NOT NULL period columns get sane values. Cycle invoices always
	// pass an explicit window from the engine.
	periodStart := input.BillingPeriodStart
	periodEnd := input.BillingPeriodEnd
	if periodStart.IsZero() {
		periodStart = now
	}
	if periodEnd.IsZero() {
		periodEnd = now
	}

	invoiceNumber, err := s.numberer.NextInvoiceNumber(ctx, tenantID)
	if err != nil {
		return domain.Invoice{}, fmt.Errorf("allocate invoice number: %w", err)
	}
	issuedAt := now
	dueAt := now.AddDate(0, 0, netDays)

	inv := domain.Invoice{
		CustomerID:         input.CustomerID,
		SubscriptionID:     input.SubscriptionID,
		InvoiceNumber:      invoiceNumber,
		Status:             domain.InvoiceDraft,
		PaymentStatus:      domain.PaymentPending,
		Currency:           currency,
		BillingPeriodStart: periodStart,
		BillingPeriodEnd:   periodEnd,
		IssuedAt:           &issuedAt,
		DueAt:              &dueAt,
		NetPaymentTermDays: netDays,
		Memo:               strings.TrimSpace(input.Memo),
		// Operator-composed invoices are billing_reason='manual', mirroring
		// Stripe. The marker drives two behaviours: (1) Finalize computes tax
		// at finalize-time for these (cycle invoices already carry it), and
		// (2) auto-finalize-after-tax-retry skips them so the operator keeps
		// the explicit finalize step while a draft is still being composed.
		BillingReason: domain.BillingReasonManual,
		// Persist whether this draft is born on a frozen test clock, from the
		// customer's pin (manual invoices have no subscription to look through).
		// This is the authoritative write-time capture — the same direct field
		// check the engine uses for cycle invoices (sub.TestClockID) — NOT
		// clock.IsSimulated(ctx): bindForCreate binds ctx to the resolver's
		// effective-now even for UNPINNED customers (it returns wall-clock), so
		// "ctx is bound" would mis-flag every manual invoice as simulated.
		IsSimulated: s.customerOnTestClock(ctx, tenantID, input.CustomerID),
	}

	// Bare-header create — caller adds line items incrementally afterwards.
	if len(input.LineItems) == 0 {
		return s.store.Create(ctx, tenantID, inv)
	}

	// Atomic create-with-lines: validate + build every line, sum the
	// subtotal, and persist header + items in one transaction. This closes
	// the partial-failure window the old create-then-loop-AddLineItem flow
	// had (a network drop mid-loop left a draft with some-but-not-all lines).
	// Tax stays 0 here by design — it's computed at finalize for manual
	// invoices (see Finalize → ComputeTaxForInvoice). Totals therefore equal
	// the subtotal; finalize rewrites them once tax is known.
	items := make([]domain.InvoiceLineItem, 0, len(input.LineItems))
	var subtotal int64
	for i, liInput := range input.LineItems {
		li, err := buildLineItem(liInput, currency)
		if err != nil {
			return domain.Invoice{}, fmt.Errorf("line item %d: %w", i+1, err)
		}
		subtotal += li.AmountCents
		items = append(items, li)
	}
	inv.SubtotalCents = subtotal
	inv.TotalAmountCents = subtotal
	inv.AmountDueCents = subtotal
	return s.store.CreateWithLineItems(ctx, tenantID, inv, items)
}

func (s *Service) Get(ctx context.Context, tenantID, id string) (domain.Invoice, error) {
	inv, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return domain.Invoice{}, err
	}
	return s.attachAttention(ctx, inv), nil
}

// SetAutoChargePending flags a finalized invoice for the scheduler's
// auto-charge retry loop. Used by the finalize handler's no-payment-method
// branch so a manual invoice self-heals when the customer attaches a card —
// the same flag the billing engine sets for cycle invoices.
func (s *Service) SetAutoChargePending(ctx context.Context, tenantID, id string, pending bool) error {
	return s.store.SetAutoChargePending(ctx, tenantID, id, pending)
}

// attachAttention computes the unified Attention surface from durable
// invoice fields plus the customer's payment-method status. Internal
// callers that read straight from the store (engine, scheduler,
// finalize path) skip this; only user-facing reads see the Attention
// field. Keeps the derivation single-source so handlers, webhook
// serializers, and hosted-invoice rendering all see the same shape.
//
// PaymentMethodReader is read at attach-time, not on the hot path —
// dashboard reads dominate this code, and a PM lookup per invoice is
// cheap. When the reader isn't wired (older test fixtures, isolated
// unit tests), HasPaymentMethod defaults to false → no_payment_method
// shows up — but that's safe: tests that exercise specific reasons
// override the field anyway.
func (s *Service) attachAttention(ctx context.Context, inv domain.Invoice) domain.Invoice {
	// Wall-clock now ages the in-flight payment banner (Info → Warning past the
	// expected-settle window). Deliberately NOT s.clock.Now(ctx): processing
	// staleness is a real-world duration (the provider settles in wall-clock),
	// and the classifier guards on !IsSimulated so a clock-pinned invoice never
	// escalates on a wall-clock age it can't satisfy.
	atc := domain.AttentionContext{Now: time.Now().UTC()} // wall-clock: processing-staleness is a real-world duration (provider settles in wall-clock); classifier guards on !IsSimulated
	if s.paymentMethods != nil && inv.CustomerID != "" {
		ps, err := s.paymentMethods.GetPaymentSetup(ctx, inv.TenantID, inv.CustomerID)
		if err == nil && ps.SetupStatus == domain.PaymentSetupReady && ps.StripeCustomerID != "" {
			atc.HasPaymentMethod = true
		}
	}
	if s.stripeChecker != nil && inv.TenantID != "" {
		// Livemode comes off ctx (auth middleware set it) — invoice
		// rows don't carry the column on the domain struct since the
		// DB trigger derives it from app.livemode at insert time. The
		// request that's reading the invoice is already mode-scoped,
		// so the ctx value matches the row's stored mode under RLS.
		atc.StripeConnected = s.stripeChecker.HasFor(ctx, inv.TenantID, postgres.Livemode(ctx))
	}
	inv.Attention = domain.ClassifyInvoiceAttention(inv, atc)

	// Compute the inclusive display end ("Jun 1 – Jun 30") in the tenant TZ on
	// the read path (ADR-050 follow-up). Storage stays half-open; this is the
	// single backend-authored value every render surface (PDF, hosted,
	// dashboard, list) shows, so the inclusive end can't drift across runtimes.
	inv.BillingPeriodDisplay = domain.FormatInclusivePeriod(
		inv.BillingPeriodStart, inv.BillingPeriodEnd, s.invoiceLocation(ctx, inv.TenantID))
	return inv
}

// invoiceLocation resolves the tenant billing timezone for display math, UTC
// when no settings reader is wired or the tenant has no timezone configured —
// matching ADR-050 / engine.tenantLocation.
func (s *Service) invoiceLocation(ctx context.Context, tenantID string) *time.Location {
	if s.settings == nil {
		return time.UTC
	}
	ts, err := s.settings.Get(ctx, tenantID)
	if err != nil {
		return time.UTC
	}
	return domain.LoadLocationOrUTC(ts.Timezone)
}

func (s *Service) List(ctx context.Context, filter ListFilter) ([]domain.Invoice, int, error) {
	invs, total, err := s.store.List(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	for i := range invs {
		invs[i] = s.attachAttention(ctx, invs[i])
	}
	return invs, total, nil
}

func (s *Service) Finalize(ctx context.Context, tenantID, id string) (domain.Invoice, error) {
	ctx = s.bindForInvoice(ctx, tenantID, id)
	inv, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return domain.Invoice{}, err
	}
	if inv.Status != domain.InvoiceDraft {
		return domain.Invoice{}, errs.InvalidState(fmt.Sprintf("can only finalize draft invoices, current status: %s", inv.Status))
	}
	// Compute tax for operator-composed (manual) invoices at finalize.
	// Cycle invoices already carry engine-computed tax from build time;
	// manual invoices accrue line items in the composer and have none
	// until now. This mirrors Stripe, which calculates tax when an invoice
	// is finalized rather than at draft-create. The engine resolves the
	// tenant's provider: 'none' → tax 0; 'manual' → flat-rate tax computed
	// synchronously; 'stripe' → a calculation that the tax_status block
	// below may park as pending until the retry worker commits it. Skipped
	// when no retrier is wired (isolated unit-test fixtures).
	if s.taxRetrier != nil && isOperatorComposed(inv) {
		taxed, terr := s.taxRetrier.ComputeTaxForInvoice(ctx, tenantID, id)
		if terr != nil {
			return domain.Invoice{}, fmt.Errorf("compute tax at finalize: %w", terr)
		}
		inv = taxed
	}
	// Block finalize while tax is unresolved. Sending an invoice with
	// wrong or missing tax creates compliance exposure; we defer until the
	// retry worker lifts the block (TaxStatus=ok) or an operator resolves
	// a failed calculation manually.
	switch inv.TaxStatus {
	case domain.InvoiceTaxPending:
		return domain.Invoice{}, errs.InvalidState("tax calculation pending — retry in progress, finalize blocked")
	case domain.InvoiceTaxFailed:
		return domain.Invoice{}, errs.InvalidState("tax calculation failed after retries — operator intervention required")
	}
	// Anchor issue + due dates to the finalize moment for operator-composed
	// (manual) invoices, mirroring Stripe's finalized_at: a manual draft is
	// "issued" when the operator finalizes it — possibly on a later test-clock
	// instant than draft-create — and Net terms run from issuance. ctx is
	// already bound to the invoice's clock (bindForInvoice above), so
	// clock.Now is simulated time for clock-pinned customers. Net term is the
	// value the draft was created with. Cycle invoices are born finalized at
	// build time and keep those dates (the UpdateStatus branch).
	var finalized domain.Invoice
	if isOperatorComposed(inv) {
		issuedAt := s.clock.Now(ctx)
		dueAt := issuedAt.AddDate(0, 0, inv.NetPaymentTermDays)
		finalized, err = s.store.FinalizeWithDates(ctx, tenantID, id, issuedAt, dueAt)
	} else {
		finalized, err = s.store.UpdateStatus(ctx, tenantID, id, domain.InvoiceFinalized)
	}
	if err != nil {
		return domain.Invoice{}, err
	}
	// Generate the hosted-invoice-URL token. Non-fatal if it fails — mirrors
	// the tax-commit convention below: the invoice is already authoritative
	// after UpdateStatus, so a transient DB hiccup on a side-effect doesn't
	// unwind the state transition. Operators can repair via the rotate
	// endpoint. Happens BEFORE tax commit because the rotate endpoint will
	// anyway need to talk to Stripe-less code paths, and token generation
	// is pure Go with no external dependency.
	if token, tokenErr := GeneratePublicToken(); tokenErr != nil {
		slog.Warn("invoice: public token generation failed at finalize",
			"error", tokenErr, "tenant_id", tenantID, "invoice_id", finalized.ID)
	} else if err := s.store.SetPublicToken(ctx, tenantID, id, token); err != nil {
		slog.Warn("invoice: public token persist failed at finalize",
			"error", err, "tenant_id", tenantID, "invoice_id", finalized.ID)
	} else {
		finalized.PublicToken = token
	}
	// Commit Stripe Tax calculation to a tax_transaction at finalize. Missing
	// calculation id = manual/none provider — skip silently. Commit failure
	// does not unwind finalize: the invoice is already authoritative; we log
	// and continue so the customer-facing state stays consistent.
	if s.taxCommitter != nil && finalized.TaxCalculationID != "" {
		if err := s.taxCommitter.CommitTax(ctx, tenantID, finalized.ID, finalized.TaxCalculationID); err != nil {
			slog.Warn("invoice: tax commit failed at finalize",
				"error", err, "tenant_id", tenantID, "invoice_id", finalized.ID,
				"tax_provider", finalized.TaxProvider, "calculation_id", finalized.TaxCalculationID)
		}
	}
	return finalized, nil
}

func (s *Service) Void(ctx context.Context, tenantID, id string) (domain.Invoice, error) {
	ctx = s.bindForInvoice(ctx, tenantID, id)
	inv, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return domain.Invoice{}, err
	}
	if inv.Status == domain.InvoicePaid {
		return domain.Invoice{}, errs.InvalidState("cannot void a paid invoice — issue a credit note instead")
	}
	if inv.Status == domain.InvoiceVoided {
		return domain.Invoice{}, errs.InvalidState("invoice is already voided")
	}

	// Reverse the upstream tax transaction so the authority's records
	// stop reporting the tax as collected. Industry standard: EU VAT
	// Directive Art. 90, Stripe Tax docs ("When an invoice is voided or
	// marked uncollectible, you must reverse the corresponding tax
	// transaction"). Best-effort: a transient failure logs but does NOT
	// block the void — the invoice is the operator's primary record and
	// must stay flippable to voided even if upstream is unreachable.
	// Operator reconciles stuck reversals manually in the provider
	// dashboard. Preconditions:
	//   - taxReverser wired (skipped for narrow tests + tenants with no
	//     provider)
	//   - invoice has a committed upstream transaction (TaxTransactionID
	//     non-empty — none/manual providers and legacy invoices skip)
	if s.taxReverser != nil && inv.TaxTransactionID != "" {
		if _, err := s.taxReverser.ReverseTax(ctx, tenantID, tax.ReversalRequest{
			OriginalTransactionID: inv.TaxTransactionID,
			Reference:             "inv_void_" + inv.ID,
			InvoiceID:             inv.ID,
			Mode:                  tax.ReversalModeFull,
		}); err != nil {
			slog.WarnContext(ctx, "tax reversal failed on invoice void — invoice still voided locally",
				"invoice_id", inv.ID,
				"tax_transaction_id", inv.TaxTransactionID,
				"error", err)
		}
	}

	return s.store.UpdateStatus(ctx, tenantID, id, domain.InvoiceVoided)
}

// MarkUncollectible flips a finalized-but-unpaid invoice to
// status='uncollectible'. Stripe-standard semantics (ADR-036
// amendment): operator (or dunning's mark_uncollectible final action)
// acknowledges the receivable won't be collected; the invoice stays
// in financial reporting but no further collection is attempted.
// Distinct from Void, which annuls the invoice.
//
// Refuses paid (collected — credit note instead) and already
// uncollectible (idempotent) states. Voided is allowed to transition
// only one direction (voided is terminal), so this errors on it too.
func (s *Service) MarkUncollectible(ctx context.Context, tenantID, id string) (domain.Invoice, error) {
	ctx = s.bindForInvoice(ctx, tenantID, id)
	inv, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return domain.Invoice{}, err
	}
	switch inv.Status {
	case domain.InvoicePaid:
		return domain.Invoice{}, errs.InvalidState("cannot mark paid invoice uncollectible — issue a credit note instead")
	case domain.InvoiceVoided:
		return domain.Invoice{}, errs.InvalidState("cannot mark voided invoice uncollectible — void is terminal")
	case domain.InvoiceUncollectible:
		return domain.Invoice{}, errs.InvalidState("invoice is already uncollectible")
	}

	// Reverse the upstream tax transaction. Stripe Tax docs: "When an
	// invoice is voided or marked uncollectible, you must reverse the
	// corresponding tax transaction." Jurisdictional caveat — bad-debt
	// VAT relief rules vary (EU permits reclaim under specific
	// conditions; US sales tax varies by state). We follow Stripe's
	// default behaviour and let tenants whose jurisdiction requires
	// the tax to stay re-commit manually. Best-effort — failure logs
	// but does not block the status transition. Reference is
	// inv_uncoll_<id> so retries converge; distinct from the void
	// Reference so an invoice that was marked uncollectible and then
	// (somehow) voided wouldn't collide.
	if s.taxReverser != nil && inv.TaxTransactionID != "" {
		if _, err := s.taxReverser.ReverseTax(ctx, tenantID, tax.ReversalRequest{
			OriginalTransactionID: inv.TaxTransactionID,
			Reference:             "inv_uncoll_" + inv.ID,
			InvoiceID:             inv.ID,
			Mode:                  tax.ReversalModeFull,
		}); err != nil {
			slog.WarnContext(ctx, "tax reversal failed on mark-uncollectible — invoice still marked locally",
				"invoice_id", inv.ID,
				"tax_transaction_id", inv.TaxTransactionID,
				"error", err)
		}
	}

	updated, err := s.store.UpdateStatus(ctx, tenantID, id, domain.InvoiceUncollectible)
	if err != nil {
		return domain.Invoice{}, err
	}
	if s.audit != nil {
		_ = s.audit.Log(ctx, tenantID, domain.AuditActionUpdate, "invoice", updated.ID, updated.InvoiceNumber, map[string]any{
			"action":           "marked_uncollectible",
			"customer_id":      updated.CustomerID,
			"amount_due_cents": updated.AmountDueCents,
			"currency":         updated.Currency,
		})
	}
	if s.events != nil {
		_ = s.events.Dispatch(ctx, tenantID, domain.EventInvoiceMarkedUncollectible, map[string]any{
			"invoice_id":       updated.ID,
			"invoice_number":   updated.InvoiceNumber,
			"customer_id":      updated.CustomerID,
			"amount_due_cents": updated.AmountDueCents,
			"currency":         updated.Currency,
		})
	}
	return updated, nil
}

// RecordOfflinePayment is the operator-driven Stripe-parity offline-
// recovery path. Lets the operator mark an unpaid invoice as paid
// without going through a PaymentIntent — for cheque, wire, cash,
// or any out-of-band collection. Mirrors Stripe's "Pay outside of
// Stripe" / paid_out_of_band=true affordance.
//
// Allowed source states: finalized (any payment_status that isn't
// already succeeded or processing) AND uncollectible (the Stripe-
// parity recovery transition — "we wrote it off but the customer
// paid after all"). Rejects paid (idempotent — nothing to do) and
// voided (terminal).
//
// note is a short operator memo persisted in the audit metadata
// (e.g. "Cheque #1234", "Wire 2026-05-20"). Not surfaced in
// customer-facing payloads. Empty string is permitted.
func (s *Service) RecordOfflinePayment(ctx context.Context, tenantID, id, note string) (domain.Invoice, error) {
	ctx = s.bindForInvoice(ctx, tenantID, id)
	inv, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return domain.Invoice{}, err
	}
	switch inv.Status {
	case domain.InvoicePaid:
		return domain.Invoice{}, errs.InvalidState("invoice is already paid")
	case domain.InvoiceVoided:
		return domain.Invoice{}, errs.InvalidState("cannot record payment on a voided invoice")
	case domain.InvoiceDraft:
		return domain.Invoice{}, errs.InvalidState("finalize the invoice before recording a payment")
	}
	if inv.PaymentStatus == domain.PaymentProcessing {
		return domain.Invoice{}, errs.InvalidState("a charge is already in flight on this invoice — wait for it to settle or cancel it before recording an offline payment")
	}
	now := s.clock.Now(ctx)
	// Use a synthetic out-of-band marker in the PaymentIntent field so
	// reports and the PaymentIntent column can distinguish engine-
	// collected charges from operator-recorded ones. Stripe encodes the
	// same distinction via paid_out_of_band; we use a string prefix
	// rather than a dedicated column to keep the schema small.
	// MarkPaid atomically flips status='paid', payment_status='succeeded',
	// amount_paid=amount_due, amount_due=0 — same end-state as a
	// successful engine charge, so downstream reports / customer
	// balance / dunning scans treat the recovery identically.
	updated, err := s.store.MarkPaid(ctx, tenantID, id, "out_of_band:"+now.UTC().Format(time.RFC3339), now)
	if err != nil {
		return domain.Invoice{}, err
	}
	if s.audit != nil {
		meta := map[string]any{
			"action":                "payment_recorded",
			"customer_id":           updated.CustomerID,
			"amount_cents":          updated.AmountDueCents,
			"currency":              updated.Currency,
			"recovered_from_status": string(inv.Status),
		}
		if note != "" {
			meta["note"] = note
		}
		_ = s.audit.Log(ctx, tenantID, domain.AuditActionUpdate, "invoice", updated.ID, updated.InvoiceNumber, meta)
	}
	if s.events != nil {
		_ = s.events.Dispatch(ctx, tenantID, domain.EventInvoicePaymentRecorded, map[string]any{
			"invoice_id":     updated.ID,
			"invoice_number": updated.InvoiceNumber,
			"customer_id":    updated.CustomerID,
			"amount_cents":   updated.AmountDueCents,
			"currency":       updated.Currency,
			"recovered_from": string(inv.Status),
			"recorded_at":    now.UTC(),
		})
	}
	return updated, nil
}

func (s *Service) RecordPayment(ctx context.Context, tenantID, id string, stripePaymentIntentID string) (domain.Invoice, error) {
	ctx = s.bindForInvoice(ctx, tenantID, id)
	now := s.clock.Now(ctx)
	return s.store.UpdatePayment(ctx, tenantID, id, domain.PaymentSucceeded, stripePaymentIntentID, "", &now)
}

func (s *Service) RecordPaymentFailure(ctx context.Context, tenantID, id, stripePaymentIntentID, errorMessage string) (domain.Invoice, error) {
	ctx = s.bindForInvoice(ctx, tenantID, id)
	return s.store.UpdatePayment(ctx, tenantID, id, domain.PaymentFailed, stripePaymentIntentID, errorMessage, nil)
}

func (s *Service) GetWithLineItems(ctx context.Context, tenantID, id string) (domain.Invoice, []domain.InvoiceLineItem, error) {
	inv, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return domain.Invoice{}, nil, err
	}
	items, err := s.store.ListLineItems(ctx, tenantID, id)
	if err != nil {
		return domain.Invoice{}, nil, err
	}
	return s.attachAttention(ctx, inv), items, nil
}

// GetByPublicToken resolves a hosted-invoice-URL token to the invoice and
// its livemode. Exposed on the service so the public hosted-invoice
// handler can look up the invoice (and hence the owning tenant + mode)
// before any tenant context is available. Thin forward to the store —
// the store method is the one that uses TxBypass. The livemode return
// is what the handler uses to pin postgres.WithLivemode on the request
// context for every downstream RLS-scoped read.
func (s *Service) GetByPublicToken(ctx context.Context, token string) (domain.Invoice, bool, error) {
	inv, livemode, err := s.store.GetByPublicToken(ctx, token)
	if err != nil {
		return domain.Invoice{}, false, err
	}
	// Pin livemode on the ctx before attachAttention. attachAttention
	// reads payment_setups under TxTenant; without the pin it would
	// default to live and either return a stale-mode row or trip the
	// "livemode propagation missing" WARN. The hosted-invoice handler
	// pins again on the request ctx for its own downstream reads, but
	// service-level pinning here keeps callers (and the attention
	// classification specifically) honest regardless of who's calling.
	ctx = postgres.WithLivemode(ctx, livemode)
	return s.attachAttention(ctx, inv), livemode, nil
}

// SetPublicToken persists a rotated public_token on a non-draft invoice.
// Exposed on the service so the operator rotate-public-token endpoint
// (T0-17.4) can delegate without reaching past the service boundary.
func (s *Service) SetPublicToken(ctx context.Context, tenantID, invoiceID, token string) error {
	return s.store.SetPublicToken(ctx, tenantID, invoiceID, token)
}

type AddLineItemInput struct {
	Description     string `json:"description"`
	LineType        string `json:"line_type"`
	Quantity        int64  `json:"quantity"`
	UnitAmountCents int64  `json:"unit_amount_cents"`
}

func (s *Service) AddLineItem(ctx context.Context, tenantID, invoiceID string, input AddLineItemInput) (domain.InvoiceLineItem, error) {
	ctx = s.bindForInvoice(ctx, tenantID, invoiceID)
	// Currency is left empty here — AddLineItemAtomic stamps it from the
	// invoice row it locks. The atomic-create path passes the resolved
	// currency instead, since the header isn't persisted yet.
	li, err := buildLineItem(input, "")
	if err != nil {
		return domain.InvoiceLineItem{}, err
	}
	item, _, err := s.store.AddLineItemAtomic(ctx, tenantID, invoiceID, li)
	return item, err
}

// buildLineItem validates an AddLineItemInput and returns the domain line
// item with its amount computed. Shared by AddLineItem (incremental add) and
// Create (atomic create-with-lines) so both paths apply identical rules:
// description required, quantity > 0, unit_amount > 0, line_type defaulting
// to add_on. currency stamps the per-line currency column the store
// persists; pass "" when a downstream store call derives it from the
// invoice.
func buildLineItem(input AddLineItemInput, currency string) (domain.InvoiceLineItem, error) {
	desc := strings.TrimSpace(input.Description)
	if desc == "" {
		return domain.InvoiceLineItem{}, errs.Required("description")
	}
	if input.Quantity <= 0 {
		return domain.InvoiceLineItem{}, errs.Invalid("quantity", "must be greater than 0")
	}
	if input.UnitAmountCents <= 0 {
		return domain.InvoiceLineItem{}, errs.Invalid("unit_amount_cents", "must be greater than 0")
	}

	lineType := strings.TrimSpace(input.LineType)
	if lineType == "" {
		// add_on is the default for operator-added line items (one-off invoice
		// composer, ad-hoc charges). Engine-driven cycle invoices always pass
		// an explicit type (base_fee / usage). The DB CHECK accepts only:
		// base_fee, usage, add_on, discount, tax — so the previous "manual"
		// fallback would have been rejected by Postgres on insert.
		lineType = string(domain.LineTypeAddOn)
	}

	amountCents := input.Quantity * input.UnitAmountCents

	return domain.InvoiceLineItem{
		LineType:         domain.InvoiceLineItemType(lineType),
		Description:      desc,
		Quantity:         input.Quantity,
		UnitAmountCents:  input.UnitAmountCents,
		AmountCents:      amountCents,
		TotalAmountCents: amountCents,
		Currency:         currency,
	}, nil
}

func (s *Service) ListApproachingDue(ctx context.Context, daysBeforeDue int) ([]domain.Invoice, error) {
	return s.store.ListApproachingDue(ctx, daysBeforeDue)
}

// ListApproachingDueForClock returns clock-pinned invoices approaching
// their simulated due_at. ADR-029 Phase 6 — the catchup orchestrator
// uses this to drive reminder dispatch on operator Advance instead of
// the wall-clock cron tick.
func (s *Service) ListApproachingDueForClock(ctx context.Context, tenantID, clockID string, frozenTime time.Time, daysBeforeDue int) ([]domain.Invoice, error) {
	return s.store.ListApproachingDueForClock(ctx, tenantID, clockID, frozenTime, daysBeforeDue)
}

// RetryTax routes a "Retry tax" action through the billing engine.
// Called from both the operator-triggered HTTP handler and the
// background reconciler (RetryPendingTax). The engine owns the
// recompute and atomic persist; this service method layers
// auto-finalize on top: when the retry succeeds AND the invoice
// is engine-generated (billing_reason != "manual"), finalize in
// the same call. Manual invoices stay draft so the operator
// retains the explicit finalize step.
//
// Returns the updated invoice with its Attention re-derived so
// the caller renders the new state immediately.
func (s *Service) RetryTax(ctx context.Context, tenantID, invoiceID string) (domain.Invoice, error) {
	ctx = s.bindForInvoice(ctx, tenantID, invoiceID)
	if s.taxRetrier == nil {
		return domain.Invoice{}, errs.InvalidState("tax retry is not configured")
	}
	inv, err := s.taxRetrier.RetryTaxForInvoice(ctx, tenantID, invoiceID)
	if err != nil {
		return domain.Invoice{}, err
	}
	if shouldAutoFinalizeAfterRetry(inv) {
		final, ferr := s.Finalize(ctx, tenantID, invoiceID)
		if ferr != nil {
			// Tax already updated and persisted; finalize-side
			// failure shouldn't unwind the recompute. Return the
			// post-retry invoice so the operator at least sees the
			// tax decision; finalize remains available as a
			// follow-up click. Logged so post-mortems can see why
			// auto-finalize didn't fire.
			slog.Warn("invoice: auto-finalize after tax retry failed; tax recomputed but invoice stays draft",
				"error", ferr, "tenant_id", tenantID, "invoice_id", invoiceID,
				"billing_reason", inv.BillingReason)
			return s.attachAttention(ctx, inv), nil
		}
		// Auto-pay when amount_due_cents <= 0 after finalize. This
		// closes the loop on the "credits applied at draft time,
		// tax pending" path: billOnePeriod applied credits to a
		// draft invoice and left it draft because tax was pending.
		// Tax retry now resolves — finalize lands the authoritative
		// total. If credits covered the new total too (amount_due=0),
		// transition straight to paid. If new tax made the total
		// larger than the credits could cover, leave finalized for
		// the normal charge / dunning flow.
		if final.AmountDueCents <= 0 {
			now := s.clock.Now(ctx)
			paid, perr := s.store.MarkPaid(ctx, tenantID, invoiceID, "", now)
			if perr != nil {
				slog.Warn("invoice: auto-pay after tax-retry finalize failed; invoice stays finalized with $0 due",
					"error", perr, "tenant_id", tenantID, "invoice_id", invoiceID,
					"amount_due_cents", final.AmountDueCents)
				return s.attachAttention(ctx, final), nil
			}
			return s.attachAttention(ctx, paid), nil
		}
		return s.attachAttention(ctx, final), nil
	}
	return s.attachAttention(ctx, inv), nil
}

// shouldAutoFinalizeAfterRetry encodes the gate for chaining a
// successful retry into Finalize. Both must be true:
//
//   - Tax is now resolved (status=ok). Pending/failed retries leave
//     the invoice draft regardless.
//   - Invoice came from the engine, not a manual draft. Manual
//     drafts can still be works-in-progress (operator may add line
//     items, edit memo, etc.); auto-finalize would surprise them.
//     billing_reason='manual' marks operator-created drafts;
//     subscription_cycle / threshold / proration / etc. all qualify
//     for auto-finalize.
//
// Subscription PaymentMethod readiness is intentionally not gated
// here — Finalize doesn't require a PM (PM matters for collection,
// which fires post-finalize via the auto-charge path).
func shouldAutoFinalizeAfterRetry(inv domain.Invoice) bool {
	if inv.Status != domain.InvoiceDraft {
		return false
	}
	if inv.TaxStatus != domain.InvoiceTaxOK {
		return false
	}
	if isOperatorComposed(inv) {
		return false
	}
	return true
}

// isOperatorComposed reports whether an invoice was created by an operator
// (the one-off composer / ad-hoc charge path) rather than the billing
// engine. billing_reason='manual' is the explicit marker Create stamps; the
// empty string covers legacy drafts written before that stamp existed.
// Engine invoices (subscription_cycle / create / cancel / threshold) return
// false. Drives finalize-time tax computation (manual invoices have no tax
// until finalize) and the auto-finalize-after-retry gate (manual drafts may
// still be works-in-progress, so we don't finalize them out from under the
// operator).
func isOperatorComposed(inv domain.Invoice) bool {
	return inv.BillingReason == "" || inv.BillingReason == domain.BillingReasonManual
}

// RetryProviderConfigErrors flushes every invoice in the (tenant,
// livemode) partition that's stuck on Stripe-configuration tax
// errors (provider_not_configured / provider_auth). Called by
// tenantstripe.Service.Connect on verify-success — when the
// operator just supplied fresh credentials, the system should
// catch up the work that was waiting on that signal. ADR-019.
//
// Reuses RetryTax per row, which already includes the auto-finalize
// chain (ADR-017): engine-generated invoices that recompute clean
// will move from draft → finalized in the same call. Manual drafts
// stay draft for explicit operator finalize.
//
// Errors are collected, not aborted-on. One bad row (e.g. concurrent
// operator click racing the same retry) shouldn't stall the rest of
// the flush.
func (s *Service) RetryProviderConfigErrors(ctx context.Context, tenantID string, livemode bool) (int, []error) {
	if s.taxRetrier == nil {
		return 0, nil
	}
	stuck, err := s.store.ListProviderConfigErrors(ctx, tenantID, livemode)
	if err != nil {
		return 0, []error{fmt.Errorf("list stuck provider-config invoices: %w", err)}
	}
	return s.retryStuck(ctx, tenantID, stuck)
}

// RetryCustomerDataErrors fans out a tax retry across every draft
// invoice for ONE customer stuck on `customer_data_invalid`. Fired
// from customer.Service.UpsertBillingProfile after a successful
// billing-profile save — when the operator fixes the customer's
// country / postal_code / state / tax_id, every invoice that was
// failing because of those fields auto-retries without per-invoice
// clicking. Same architectural shape as RetryProviderConfigErrors
// (which fires on Stripe-reconnect for provider-credential errors);
// scoped per-customer instead of per-tenant. ADR-019 sibling.
//
// Surgical-filter principle: only `customer_data_invalid` rows. Other
// codes (provider_outage, jurisdiction_unsupported, etc.) aren't
// resolved by a billing-profile change, so retrying them here would
// just burn `tax_retry_count` budget. The wall-clock scheduler's
// RetryPendingTax + the existing per-invoice operator Retry button
// cover those cases.
func (s *Service) RetryCustomerDataErrors(ctx context.Context, tenantID, customerID string) (int, []error) {
	if s.taxRetrier == nil {
		return 0, nil
	}
	stuck, err := s.store.ListCustomerDataInvalidErrors(ctx, tenantID, customerID)
	if err != nil {
		return 0, []error{fmt.Errorf("list customer-data-invalid invoices: %w", err)}
	}
	return s.retryStuck(ctx, tenantID, stuck)
}

// retryStuck is the shared per-row body of RetryProviderConfigErrors
// and RetryCustomerDataErrors. The candidate list shape differs by
// trigger; the per-invoice retry call is identical.
func (s *Service) retryStuck(ctx context.Context, tenantID string, stuck []domain.Invoice) (int, []error) {
	var (
		processed int
		errs      []error
	)
	for _, inv := range stuck {
		if _, retryErr := s.RetryTax(ctx, tenantID, inv.ID); retryErr != nil {
			errs = append(errs, fmt.Errorf("invoice %s: %w", inv.ID, retryErr))
			continue
		}
		processed++
	}
	return processed, errs
}

// CountProviderConfigErrors returns how many invoices would be
// retried by RetryProviderConfigErrors right now. Used by the
// connect handler to populate the response body so the dashboard
// can render "Retrying N stuck invoices" without waiting for the
// async fan-out to finish.
func (s *Service) CountProviderConfigErrors(ctx context.Context, tenantID string, livemode bool) (int, error) {
	stuck, err := s.store.ListProviderConfigErrors(ctx, tenantID, livemode)
	if err != nil {
		return 0, err
	}
	return len(stuck), nil
}

// RetryPendingTax is the background-reconciler entry. Scans
// invoices whose retryable-coded tax failure is due for another
// attempt, calls RetryTax for each (which may auto-finalize on
// success), returns counts for telemetry.
//
// Cross-tenant: each invoice carries its tenant_id, and RetryTax
// re-pins it on ctx through the engine's path. Bounded by `batch`
// per tick so a Stripe Tax outage that stuck thousands of
// invoices doesn't burn the entire scheduler tick on tax retries.
//
// Errors per invoice are collected, not aborted-on — one bad row
// (e.g. concurrent operator-driven Finalize racing the reconciler)
// shouldn't stall the rest of the batch.
func (s *Service) RetryPendingTax(ctx context.Context, batch int) (int, []error) {
	if s.taxRetrier == nil {
		return 0, nil
	}
	codes := []string{"provider_outage", "unknown"}
	const maxAttempts = 8
	// Reconciler runs once per mode in the scheduler tick — pull
	// the mode off ctx and filter the SQL scan so this tick only
	// processes its own partition's invoices. Without the filter,
	// the cross-mode RLS-bypassed scan returns rows for both modes
	// and per-row RetryTax fails with "not found" on the half whose
	// livemode doesn't match ctx.
	livemode := postgres.Livemode(ctx)
	stuck, err := s.store.ListPendingTaxRetry(ctx, batch, codes, maxAttempts, livemode)
	if err != nil {
		return 0, []error{fmt.Errorf("list pending tax retries: %w", err)}
	}
	return s.processTaxRetryBatch(ctx, stuck)
}

// RetryPendingTaxForClock is the catchup-path counterpart to
// RetryPendingTax. ADR-029 Phase 2: tax retries on clock-pinned
// invoices fire only when an operator advances the clock — the
// wall-clock cron's ListPendingTaxRetry filters them out via NOT
// EXISTS so the two paths process disjoint row sets.
//
// One retry per row per Advance (no backoff gate). Operator-friendly:
// each click does something visible. Faithful per-window retry-
// sequence simulation (Stripe-parity event-walking) is deferred —
// it's a niche use case operators don't typically run, and the
// trade-off "I clicked Advance and nothing happened" is a worse
// failure mode for the workflows operators actually exercise.
//
// Catchup-side ordering: this runs BEFORE the auto-charge phase so
// any invoices that get unstuck (tax_status: pending → ok) become
// charge-eligible in the same Advance click. Without this ordering,
// an operator who fixes the tax provider then clicks Advance would
// see the period-gen + auto-charge phases run, but the still-pending
// tax invoices wouldn't retry until the NEXT advance — confusing.
func (s *Service) RetryPendingTaxForClock(ctx context.Context, tenantID, clockID string, batch int) (int, []error) {
	if s.taxRetrier == nil {
		return 0, nil
	}
	codes := []string{"provider_outage", "unknown"}
	const maxAttempts = 8
	stuck, err := s.store.ListPendingTaxRetryForClock(ctx, tenantID, clockID, codes, maxAttempts, batch)
	if err != nil {
		return 0, []error{fmt.Errorf("list pending tax retries for clock %s: %w", clockID, err)}
	}
	return s.processTaxRetryBatch(ctx, stuck)
}

// processTaxRetryBatch is the per-row body of the cron-path
// RetryPendingTax. The catchup path (RetryPendingTaxForClock) has
// its own event-walking loop with per-row ctx-binding; the cron
// path doesn't need that since it's already wall-clock-driven.
func (s *Service) processTaxRetryBatch(ctx context.Context, stuck []domain.Invoice) (int, []error) {
	var (
		processed int
		errs      []error
	)
	for _, inv := range stuck {
		// RetryTax pins tenant via the engine entry point that
		// already does WithTenantID. We pass the per-row tenant
		// explicitly here rather than relying on ctx because this
		// loop is cross-tenant.
		if _, retryErr := s.RetryTax(ctx, inv.TenantID, inv.ID); retryErr != nil {
			errs = append(errs, fmt.Errorf("invoice %s: %w", inv.ID, retryErr))
			continue
		}
		processed++
	}
	return processed, errs
}
