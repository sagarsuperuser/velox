package customer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"regexp"
	"strings"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/clock"
	"github.com/sagarsuperuser/velox/internal/tax"
)

var phonePattern = regexp.MustCompile(`^[\+\d\s\-\(\)]{7,20}$`)

// countryPattern matches an uppercase ISO-3166-1 alpha-2 code. Stripe (Tax and
// payments) expects alpha-2 for every address; validating the format here turns
// a bad code ("USA") into a clean 400 instead of an opaque Stripe-side
// rejection at invoice-compute time. Mirrors the tenant company_country check.
var countryPattern = regexp.MustCompile(`^[A-Z]{2}$`)

// currencyPattern: ISO-4217 alpha-3, canonical UPPERCASE (ADR-067 currency guard).
var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

// StripeSyncer syncs billing profile data to Stripe when a Stripe customer exists.
type StripeSyncer interface {
	SyncBillingProfile(ctx context.Context, stripeCustomerID, customerEmail string, bp domain.CustomerBillingProfile) error
}

// PaymentSetupReader — DEPRECATED. The 1:1 customer_payment_setups
// summary was retired in migration 0097. The Stripe Customer ID lives
// on customers.stripe_customer_id (read via store.Get) and PM details
// live on payment_methods rows. Kept as a no-op interface so existing
// callers compile during the transition; new code should depend on
// customer.Service.Get and read cust.StripeCustomerID directly.
type PaymentSetupReader interface{}

// TestClockChecker validates a test_clock_id at customer-create time
// (Stripe parity, ADR-027). Narrow shape — Service doesn't need
// the full TestClock; just "does it exist for this tenant?".
// Implemented by *testclock.PostgresStore.
type TestClockChecker interface {
	Exists(ctx context.Context, tenantID, clockID string) (bool, error)
}

// TaxFlusher fans out a tax-retry across every draft invoice for one
// customer that's stuck on `customer_data_invalid`. Fired from
// UpsertBillingProfile after a successful save — the only operator
// action that can resolve customer_data_invalid is editing the
// billing profile, so the moment the save lands we replay the tax
// calc against the (now possibly-correct) fields. Operator never has
// to click Retry per-invoice.
//
// Narrow shape (single method, no return values used by the caller)
// keeps the customer→invoice dep one-way: customer doesn't need to
// know what tax retry means, only that it exists. Implemented by
// *invoice.Service.
//
// Mirrors the ADR-019 Stripe-reconnect flush at per-customer scope.
type TaxFlusher interface {
	RetryCustomerDataErrors(ctx context.Context, tenantID, customerID string) (int, []error)
}

// NonTerminalSubscription is one still-billing subscription blocking a
// customer-level change — the archive guard's and the billing-profile
// currency guard's shared ground truth (ADR-067).
type NonTerminalSubscription struct {
	ID     string
	Status string
	// PlanCurrencies are the distinct currencies of the subscription's
	// item plans — the currency-mismatch guard compares the billing
	// profile's currency against them (a profile currency override
	// re-denominates plan prices at invoice time: $100 plan → €100).
	PlanCurrencies []string
}

// SubscriptionChecker reports a customer's non-terminal (still-billing)
// subscriptions: active or trialing, including active subs with a scheduled
// cancel (they bill until the boundary). Narrow interface wired via
// SetSubscriptionChecker (precedent: SetStripeSyncer) so the customer
// package keeps the zero-peer-import rule; implemented by an adapter over
// the subscription store in api/router.go.
type SubscriptionChecker interface {
	NonTerminalForCustomer(ctx context.Context, tenantID, customerID string) ([]NonTerminalSubscription, error)
}

type Service struct {
	store         Store
	stripeSyncer  StripeSyncer
	paymentSetups PaymentSetupReader
	clocks        TestClockChecker
	taxFlusher    TaxFlusher
	events        domain.EventDispatcher
	resolver      clock.Resolver
	subChecker    SubscriptionChecker
}

func NewService(store Store) *Service {
	return &Service{store: store}
}

// SetResolver wires the unified clock.Resolver. At Update /
// UpsertBillingProfile and other customer-scoped mutations, ctx is
// bound to the customer's effective-now so downstream postgres
// timestamp writes (updated_at on customer row, billing_profile
// updates, etc.) land in simulated time on clock-pinned customers.
//
// Customer.Create can't bind from the customer pin (the customer
// doesn't exist yet), but if the input carries a TestClockID at
// creation we bind from the clock directly. Otherwise wall-clock.
func (s *Service) SetResolver(r clock.Resolver) {
	s.resolver = r
}

// bindForCustomer binds effective-now from a customer pin for any
// post-create mutation (Update, UpsertBillingProfile, ArchiveCustomer).
// Returns ctx unchanged on resolver error or no resolver.
func (s *Service) bindForCustomer(ctx context.Context, tenantID, customerID string) context.Context {
	bound, _ := clock.BindEffectiveNow(ctx, s.resolver, clock.Pin{TenantID: tenantID, CustomerID: customerID})
	return bound
}

// SetStripeSyncer configures Stripe sync (optional, breaks circular dep).
func (s *Service) SetStripeSyncer(syncer StripeSyncer, setups PaymentSetupReader) {
	s.stripeSyncer = syncer
	s.paymentSetups = setups
}

// SetSubscriptionChecker wires the archive/currency guard's subscription
// lookup. Production always wires it (api/router.go); the guarded
// transitions FAIL LOUDLY when it's missing rather than silently
// permitting an archive that keeps billing (ADR-067).
func (s *Service) SetSubscriptionChecker(c SubscriptionChecker) {
	s.subChecker = c
}

// SetTestClockChecker wires the test-clock existence check used at
// customer-create time. Optional — if unwired, customer-create
// rejects any test_clock_id (defensive default; production wires
// the real store via api/router.go).
func (s *Service) SetTestClockChecker(checker TestClockChecker) {
	s.clocks = checker
}

// SetTaxFlusher wires the per-customer tax-retry flush fired after a
// successful billing-profile upsert. Optional — if unwired, the
// flush is a no-op and operators fall back to per-invoice Retry.
// Production wires *invoice.Service via api/router.go.
func (s *Service) SetTaxFlusher(flusher TaxFlusher) {
	s.taxFlusher = flusher
}

// SetEventDispatcher wires the outbound webhook dispatcher so the service
// can emit customer.email_bounced (and future customer.* events). Nil is
// acceptable — events become a no-op, which is the right behaviour in
// narrow unit tests.
func (s *Service) SetEventDispatcher(d domain.EventDispatcher) {
	s.events = d
}

// MarkEmailBounced records a permanent-delivery-failure signal for the
// given customer (resolved from email by the caller via the blind-index
// lookup). Fires customer.email_bounced after a successful write so
// partners can wire their own dead-address handling. Missing customer
// IDs are treated as soft failures — log and return — because the
// sender path must never panic an outbox worker.
func (s *Service) MarkEmailBounced(ctx context.Context, tenantID, customerID, reason string) error {
	if err := s.store.MarkEmailBounced(ctx, tenantID, customerID, reason); err != nil {
		return err
	}
	if s.events != nil {
		payload := map[string]any{
			"customer_id": customerID,
			"reason":      reason,
		}
		// Non-fatal on dispatch error — the state transition already
		// landed; losing a webhook event isn't worth reversing the
		// status.
		if err := s.events.Dispatch(ctx, tenantID, domain.EventCustomerEmailBounced, payload); err != nil {
			slog.WarnContext(ctx, "dispatch customer.email_bounced",
				"tenant_id", tenantID, "customer_id", customerID, "error", err)
		}
	}
	return nil
}

type CreateInput struct {
	ExternalID  string `json:"external_id"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email,omitempty"`
	// TestClockID pins this customer to a test clock — Stripe parity,
	// ADR-027. Test-mode only; rejected on live-mode customers via
	// the livemode check below. Once attached at create time, every
	// Subscription / Invoice for this customer uses the clock's
	// frozen_time as "now". Cannot be changed post-creation.
	TestClockID string `json:"test_clock_id,omitempty"`
}

func (s *Service) Create(ctx context.Context, tenantID string, input CreateInput) (domain.Customer, error) {
	input.ExternalID = strings.TrimSpace(input.ExternalID)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.Email = strings.TrimSpace(input.Email)
	input.TestClockID = strings.TrimSpace(input.TestClockID)

	if input.ExternalID == "" {
		return domain.Customer{}, errs.Required("external_id")
	}
	if err := domain.MaxLen("external_id", input.ExternalID, 255); err != nil {
		return domain.Customer{}, err
	}
	if input.DisplayName == "" {
		return domain.Customer{}, errs.Required("display_name")
	}
	if err := domain.MaxLen("display_name", input.DisplayName, 255); err != nil {
		return domain.Customer{}, err
	}
	if input.Email != "" {
		if err := validateEmail("email", input.Email); err != nil {
			return domain.Customer{}, err
		}
	}
	// Test-clock attach: validate clock exists (mode gating happens at
	// the route layer — test_clock_id only reaches us when the caller
	// is on a test-mode key). If the checker isn't wired (defensive
	// default) any test_clock_id is rejected.
	if input.TestClockID != "" {
		if s.clocks == nil {
			return domain.Customer{}, errs.Invalid("test_clock_id", "test clocks are not available in this environment")
		}
		exists, err := s.clocks.Exists(ctx, tenantID, input.TestClockID)
		if err != nil {
			return domain.Customer{}, err
		}
		if !exists {
			return domain.Customer{}, errs.Invalid("test_clock_id", "test clock not found")
		}
	}

	return s.store.Create(ctx, tenantID, domain.Customer{
		ExternalID:  input.ExternalID,
		DisplayName: input.DisplayName,
		Email:       input.Email,
		TestClockID: input.TestClockID,
	})
}

func (s *Service) Get(ctx context.Context, tenantID, id string) (domain.Customer, error) {
	return s.store.Get(ctx, tenantID, id)
}

// RotateCostDashboardToken mints a fresh public cost-dashboard token
// and writes it to the customer row, invalidating the old token
// (read-only public surface; no grace window needed). Returns the
// raw token — only chance to capture it; the database stores it
// plaintext so the public route can compare directly (256-bit
// entropy makes brute-force infeasible, same shape as the hosted-
// invoice public_token).
func (s *Service) RotateCostDashboardToken(ctx context.Context, tenantID, customerID string) (string, error) {
	ctx = s.bindForCustomer(ctx, tenantID, customerID)
	if _, err := s.store.Get(ctx, tenantID, customerID); err != nil {
		return "", err
	}
	token, err := NewCostDashboardToken()
	if err != nil {
		return "", err
	}
	if err := s.store.SetCostDashboardToken(ctx, tenantID, customerID, token); err != nil {
		return "", err
	}
	return token, nil
}

// GetByCostDashboardToken resolves the customer behind a public token.
// Used by the unauthenticated public route — the lookup is RLS-bypass
// (token IS the credential), and the returned tenant_id is the scope
// every downstream call uses.
func (s *Service) GetByCostDashboardToken(ctx context.Context, token string) (domain.Customer, error) {
	return s.store.GetByCostDashboardToken(ctx, token)
}

func (s *Service) List(ctx context.Context, filter ListFilter) ([]domain.Customer, int, error) {
	return s.store.List(ctx, filter)
}

// ListByTestClockID returns customers pinned to the given test clock.
// Exposed as a service method (not just a store passthrough) so the
// testclock domain can depend on the customer service's narrow
// CustomerReader interface — keeps the per-domain rule intact and
// the decrypt step on the customer read path.
func (s *Service) ListByTestClockID(ctx context.Context, tenantID, clockID string) ([]domain.Customer, error) {
	return s.store.ListByTestClockID(ctx, tenantID, clockID)
}

type UpdateInput struct {
	DisplayName string `json:"display_name"`
	Email       string `json:"email,omitempty"`
	Status      string `json:"status,omitempty"`
	// DunningPolicyID assigns this customer to a specific dunning
	// policy (ADR-036). Pointer so the handler can distinguish
	// "unset" (omitted) from "clear" (explicit empty string). nil =
	// leave as-is; *"" = clear assignment (fall back to default);
	// *"vlx_dpol_..." = assign to that policy.
	DunningPolicyID *string `json:"dunning_policy_id,omitempty"`
}

func (s *Service) Update(ctx context.Context, tenantID, id string, input UpdateInput) (domain.Customer, error) {
	ctx = s.bindForCustomer(ctx, tenantID, id)
	existing, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return domain.Customer{}, err
	}

	prevEmail := existing.Email

	if name := strings.TrimSpace(input.DisplayName); name != "" {
		existing.DisplayName = name
	}
	if email := strings.TrimSpace(input.Email); email != "" {
		if err := validateEmail("email", email); err != nil {
			return domain.Customer{}, err
		}
		existing.Email = email
	}
	if status := domain.CustomerStatus(input.Status); status != "" {
		// Archive = 409-BLOCK while any subscription still bills, never
		// auto-cancel (ADR-067). Auto-cancel would run the immediate-cancel
		// billing path — final prorated invoice + auto-charge of the saved
		// card — turning "archive to stop billing" into N surprise charges.
		// Archive only hides the customer + blocks NEW subscriptions;
		// existing unpaid invoices remain collectible and dunning continues
		// (both are invoice-driven). The operator cancels subs first, then
		// archives.
		if status == domain.CustomerStatusArchived && existing.Status != domain.CustomerStatusArchived {
			if s.subChecker == nil {
				// Fail closed: an unenforced archive silently keeps charging
				// the customer against explicit operator intent — the exact
				// HIGH this guard exists to prevent.
				return domain.Customer{}, fmt.Errorf("subscription checker not wired: cannot verify archive safety")
			}
			blocking, err := s.subChecker.NonTerminalForCustomer(ctx, tenantID, id)
			if err != nil {
				return domain.Customer{}, fmt.Errorf("check subscriptions before archive: %w", err)
			}
			if len(blocking) > 0 {
				ids := make([]string, 0, len(blocking))
				for _, b := range blocking {
					ids = append(ids, b.ID)
				}
				return domain.Customer{}, errs.InvalidState(fmt.Sprintf(
					"customer has %d subscription(s) that still bill (%s) — cancel them before archiving; archiving does not stop billing",
					len(blocking), strings.Join(ids, ", ")))
			}
		}
		existing.Status = status
	}
	if input.DunningPolicyID != nil {
		existing.DunningPolicyID = strings.TrimSpace(*input.DunningPolicyID)
	}

	updated, err := s.store.Update(ctx, tenantID, existing)
	if err != nil {
		return domain.Customer{}, err
	}

	// Bounce-state reset: if the email value actually changed, any
	// previously-recorded 'bounced'/'complained' state was tied to the
	// OLD address and must be cleared so the suppression gate doesn't
	// silently drop sends to the new untested address. Best-effort —
	// failure here doesn't fail the update (the bounce flag is
	// diagnostic data; eventual consistency is acceptable).
	if existing.Email != prevEmail {
		if err := s.store.ResetEmailStatus(ctx, tenantID, id); err != nil {
			slog.WarnContext(ctx, "reset email_status after email change failed",
				"tenant_id", tenantID, "customer_id", id, "error", err)
		}
	}

	// Stripe sync (2026-05-29): propagate email + display_name changes
	// to the linked Stripe Customer. Pre-fix only UpsertBillingProfile
	// triggered Stripe sync; customer.Update silently diverged. Best-
	// effort: local save succeeded, Stripe sync failure logs but doesn't
	// fail the operator action (matches Lago/Recurly's "local is source
	// of truth, sync is downstream" model).
	if s.stripeSyncer != nil && updated.StripeCustomerID != "" {
		// Pull the billing profile so the full set of Stripe-relevant
		// fields stay in lockstep. ErrNotFound = no profile yet —
		// pass an empty BP, SyncBillingProfile gracefully skips the
		// fields that aren't set.
		bp, bpErr := s.store.GetBillingProfile(ctx, tenantID, id)
		if bpErr != nil && !errors.Is(bpErr, errs.ErrNotFound) {
			slog.WarnContext(ctx, "fetch billing profile for Stripe sync failed",
				"tenant_id", tenantID, "customer_id", id, "error", bpErr)
		}
		if syncErr := s.stripeSyncer.SyncBillingProfile(ctx, updated.StripeCustomerID, updated.Email, bp); syncErr != nil {
			slog.WarnContext(ctx, "failed to sync customer update to Stripe",
				"tenant_id", tenantID, "customer_id", id,
				"stripe_customer_id", updated.StripeCustomerID, "error", syncErr)
		}
	}

	return updated, nil
}

// GetDunningPolicyID returns the customer's assigned dunning_policy_id
// (empty string = no explicit assignment, dunning service falls back
// to the tenant default). Satisfies dunning.CustomerPolicyReader so
// the dunning service can resolve effective policy without importing
// the customer package.
func (s *Service) GetDunningPolicyID(ctx context.Context, tenantID, customerID string) (string, error) {
	c, err := s.store.Get(ctx, tenantID, customerID)
	if err != nil {
		return "", err
	}
	return c.DunningPolicyID, nil
}

func (s *Service) UpsertBillingProfile(ctx context.Context, tenantID string, bp domain.CustomerBillingProfile) (domain.CustomerBillingProfile, error) {
	if bp.CustomerID == "" {
		return domain.CustomerBillingProfile{}, errs.Required("customer_id")
	}
	ctx = s.bindForCustomer(ctx, tenantID, bp.CustomerID)
	if phone := strings.TrimSpace(bp.Phone); phone != "" {
		if !phonePattern.MatchString(phone) {
			return domain.CustomerBillingProfile{}, errs.Invalid("phone", "must be 7-20 characters and contain only digits, spaces, +, -, (, )")
		}
	}
	// Normalize + format-validate the billing country to ISO-3166 alpha-2 so it
	// stores canonically ("US", not "us"/" US ") and a bad code fails as a clean
	// 400 here rather than as an opaque Stripe Tax rejection at invoice time. The
	// dashboard already sends a code from a fixed list; this closes the
	// direct-API bypass, like the tax-status enforcement below.
	bp.Country = strings.ToUpper(strings.TrimSpace(bp.Country))
	if bp.Country != "" && !countryPattern.MatchString(bp.Country) {
		return domain.CustomerBillingProfile{}, errs.Invalid("country", "must be an ISO-3166 alpha-2 country code (e.g. 'IN', 'US')")
	}
	// Currency: normalize to canonical UPPERCASE (lowercase broke
	// analytics/dunning filters once), format-validate, and — the money
	// guard — reject a currency that mismatches a still-billing
	// subscription's plan currency. The profile currency OVERRIDES the plan
	// currency at every invoice writer, so a mismatch silently
	// re-denominates plan prices ($100 plan invoiced as €100 or ¥100) with
	// no conversion. ADR-067.
	bp.Currency = strings.ToUpper(strings.TrimSpace(bp.Currency))
	if bp.Currency != "" {
		if !currencyPattern.MatchString(bp.Currency) {
			return domain.CustomerBillingProfile{}, errs.Invalid("currency", "must be an ISO-4217 alpha-3 currency code (e.g. 'USD', 'EUR')")
		}
		if s.subChecker != nil {
			subs, err := s.subChecker.NonTerminalForCustomer(ctx, tenantID, bp.CustomerID)
			if err != nil {
				return domain.CustomerBillingProfile{}, fmt.Errorf("check subscriptions for currency guard: %w", err)
			}
			for _, sub := range subs {
				for _, cur := range sub.PlanCurrencies {
					if cur != "" && !strings.EqualFold(cur, bp.Currency) {
						return domain.CustomerBillingProfile{}, errs.InvalidState(fmt.Sprintf(
							"billing profile currency %s conflicts with subscription %s's plan currency %s — invoices would relabel plan prices in %s without conversion; cancel the subscription or keep the currency",
							bp.Currency, sub.ID, strings.ToUpper(cur), bp.Currency))
					}
				}
			}
		} else {
			// Test-only path — production wires the checker in router.go. The
			// format validation above still applies; only the cross-sub
			// mismatch check is skipped.
			slog.Warn("billing profile currency guard skipped: subscription checker not wired",
				"customer_id", bp.CustomerID)
		}
	}
	// Normalize + format-validate tax IDs. Unknown kinds pass through untouched
	// so we don't reject jurisdictions we haven't added explicit support for.
	// Type is normalized to its canonical Stripe code so storage stays
	// consistent regardless of which alias the caller supplied.
	bp.TaxIDType = tax.NormalizeTaxIDType(bp.TaxIDType)
	bp.TaxID = tax.NormalizeTaxID(bp.TaxID)
	if err := tax.ValidateTaxID(bp.TaxIDType, bp.TaxID); err != nil {
		return domain.CustomerBillingProfile{}, errs.Invalid("tax_id", err.Error())
	}
	// Default to standard tax status so zero-valued API requests behave
	// predictably. The DB CHECK constraint mirrors this enum; validating
	// here produces a clean 400 instead of a constraint error.
	if bp.TaxStatus == "" {
		bp.TaxStatus = tax.StatusStandard
	}
	switch bp.TaxStatus {
	case tax.StatusStandard, tax.StatusExempt, tax.StatusReverseCharge:
	default:
		return domain.CustomerBillingProfile{}, errs.Invalid("tax_status", "must be 'standard', 'exempt', or 'reverse_charge'")
	}
	bp.TaxExemptReason = strings.TrimSpace(bp.TaxExemptReason)
	// Enforce the data each non-standard status legally requires so the
	// invoice can render a defensible legend:
	//   - exempt needs a reason (reseller certificate, non-profit, government)
	//     for the invoice + audit trail.
	//   - reverse_charge needs the buyer's tax ID — "tax payable by the
	//     recipient" is only valid when the buyer is a registered business, so
	//     a $0 reverse-charge invoice with no buyer VAT number is legally
	//     unsupportable.
	// The dashboard already guards both; enforcing here closes the direct-API
	// bypass the tax-flow audit surfaced.
	switch bp.TaxStatus {
	case tax.StatusExempt:
		if bp.TaxExemptReason == "" {
			return domain.CustomerBillingProfile{}, errs.Invalid("tax_exempt_reason", "a reason is required when tax_status is 'exempt'")
		}
	case tax.StatusReverseCharge:
		if bp.TaxID == "" {
			return domain.CustomerBillingProfile{}, errs.Invalid("tax_id", "a buyer tax ID is required when tax_status is 'reverse_charge'")
		}
	}
	if bp.ProfileStatus == "" {
		bp.ProfileStatus = domain.BillingProfileIncomplete
	}
	result, err := s.store.UpsertBillingProfile(ctx, tenantID, bp)
	if err != nil {
		return result, err
	}

	// Note: billing-profile.email was removed in migration 0100.
	// customers.email is now the single canonical recipient (Phase 1 of
	// the dual-email collapse). The prior BP→customer sync hack is
	// obsolete. Phase 2 (multi-recipient via customer_email_contacts
	// table) will be additive when a DP requests it.

	// Sync to Stripe if a Stripe customer exists. Reads from the
	// canonical mapping on customers.stripe_customer_id (migration
	// 0096); the legacy customer_payment_setups summary was retired
	// in 0097.
	if s.stripeSyncer != nil {
		if cust, getErr := s.store.Get(ctx, tenantID, bp.CustomerID); getErr == nil && cust.StripeCustomerID != "" {
			if syncErr := s.stripeSyncer.SyncBillingProfile(ctx, cust.StripeCustomerID, cust.Email, result); syncErr != nil {
				slog.Warn("failed to sync billing profile to Stripe",
					"customer_id", bp.CustomerID, "stripe_customer_id", cust.StripeCustomerID, "error", syncErr)
				// Non-fatal: local save succeeded, Stripe sync is best-effort
			}
		}
	}

	// Fan out a tax retry across every draft invoice for this customer
	// stuck on `customer_data_invalid`. The operator's edit IS the
	// retry trigger — same shape as ADR-019's Stripe-reconnect flush
	// scoped to one customer. Best-effort: per-invoice failures are
	// logged but don't roll back the profile save.
	if s.taxFlusher != nil {
		processed, retryErrs := s.taxFlusher.RetryCustomerDataErrors(ctx, tenantID, bp.CustomerID)
		if processed > 0 || len(retryErrs) > 0 {
			slog.InfoContext(ctx, "billing profile flush retried tax errors",
				"tenant_id", tenantID,
				"customer_id", bp.CustomerID,
				"processed", processed,
				"errors", len(retryErrs),
			)
		}
		for _, retryErr := range retryErrs {
			slog.WarnContext(ctx, "billing profile flush tax retry failed",
				"tenant_id", tenantID,
				"customer_id", bp.CustomerID,
				"error", retryErr,
			)
		}
	}

	return result, nil
}

func (s *Service) GetBillingProfile(ctx context.Context, tenantID, customerID string) (domain.CustomerBillingProfile, error) {
	return s.store.GetBillingProfile(ctx, tenantID, customerID)
}

// validateEmail accepts only well-formed RFC 5322 addresses via the
// Go stdlib parser, plus a hard requirement that the domain contains
// at least one dot. ParseAddress alone allows local-only addresses
// like `foo@bar` (no TLD) — fine in RFC terms, useless for actual
// SMTP delivery, so we still gate on the dot. Tier-1 syntax check;
// tier-6 (bounce-driven suppression) catches what survives.
func validateEmail(field, email string) error {
	addr, err := mail.ParseAddress(email)
	if err != nil {
		return errs.Invalid(field, "invalid email")
	}
	at := strings.LastIndex(addr.Address, "@")
	if at < 0 {
		return errs.Invalid(field, "invalid email")
	}
	domainPart := addr.Address[at+1:]
	if !strings.Contains(domainPart, ".") || strings.HasSuffix(domainPart, ".") {
		return errs.Invalid(field, "invalid email: domain must contain a dot")
	}
	return nil
}
