package coupon

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/money"
)

var codeRegexp = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9\-]{1,48}[A-Za-z0-9]$`)

type Service struct {
	store           Store
	customerHistory CustomerHistoryLookup
}

func NewService(store Store) *Service {
	return &Service{store: store}
}

// CustomerHistoryLookup answers the "has this customer ever paid" question the
// first_time_customer_only restriction depends on. Defined as an interface here
// so the coupon package stays insulated from the invoice package per the
// zero-cross-domain rule — production wires up an invoice-backed impl, tests
// pass a stub.
type CustomerHistoryLookup interface {
	HasSucceededInvoice(ctx context.Context, tenantID, customerID string) (bool, error)
}

// SetCustomerHistoryLookup wires the lookup the service consults when
// evaluating first_time_customer_only. Unset on a newly-constructed service
// so tests that don't exercise the restriction don't need to stub anything;
// production must call this during assembly.
func (s *Service) SetCustomerHistoryLookup(h CustomerHistoryLookup) {
	s.customerHistory = h
}

// CreateInput is the validated wire format for POST /coupons.
// PercentOffBP is the source of truth for percentage coupons; the
// legacy percent_off (float) input is accepted on the handler side and
// converted to BP before reaching the service.
type CreateInput struct {
	Code            string                    `json:"code"`
	Name            string                    `json:"name"`
	Type            domain.CouponType         `json:"type"`
	AmountOff       int64                     `json:"amount_off"`
	PercentOffBP    int                       `json:"percent_off_bp"`
	Currency        string                    `json:"currency"`
	MaxRedemptions  *int                      `json:"max_redemptions"`
	ExpiresAt       *time.Time                `json:"expires_at,omitempty"`
	PlanIDs         []string                  `json:"plan_ids,omitempty"`
	Duration        domain.CouponDuration     `json:"duration,omitempty"`
	DurationPeriods *int                      `json:"duration_periods,omitempty"`
	Stackable       bool                      `json:"stackable"`
	CustomerID      string                    `json:"customer_id,omitempty"`
	Restrictions    domain.CouponRestrictions `json:"restrictions"`
	Metadata        []byte                    `json:"metadata,omitempty"`
}

func (s *Service) Create(ctx context.Context, tenantID string, input CreateInput) (domain.Coupon, error) {
	code := strings.TrimSpace(strings.ToUpper(input.Code))
	if code == "" {
		generated, err := generateCouponCode()
		if err != nil {
			return domain.Coupon{}, fmt.Errorf("generate coupon code: %w", err)
		}
		code = generated
	}
	if !codeRegexp.MatchString(code) {
		return domain.Coupon{}, errs.Invalid("code", "must be 3-50 alphanumeric characters or dashes")
	}

	name := strings.TrimSpace(input.Name)
	if name == "" {
		return domain.Coupon{}, errs.Required("name")
	}
	if len(name) > 200 {
		return domain.Coupon{}, errs.Invalid("name", "must be at most 200 characters")
	}

	switch input.Type {
	case domain.CouponTypePercentage:
		// BP range: 1 (0.01%) through 10000 (100%). Anything else is
		// either a bug (negative), a rejected "free" coupon (0), or a
		// nonsense value (>100%). Use fixed_amount if you want >= subtotal.
		if input.PercentOffBP <= 0 || input.PercentOffBP > 10000 {
			return domain.Coupon{}, errs.Invalid("percent_off_bp", "must be between 1 and 10000 (0.01% - 100%)")
		}
	case domain.CouponTypeFixedAmount:
		if input.AmountOff <= 0 {
			return domain.Coupon{}, errs.Invalid("amount_off", "must be greater than 0")
		}
		if input.AmountOff > 100_000_000 { // $1M cap
			return domain.Coupon{}, errs.Invalid("amount_off", "cannot exceed 1,000,000.00")
		}
		cur := strings.TrimSpace(strings.ToUpper(input.Currency))
		if cur == "" {
			return domain.Coupon{}, errs.Required("currency")
		}
		input.Currency = cur
	default:
		return domain.Coupon{}, errs.Invalid("type", "must be 'percentage' or 'fixed_amount'")
	}

	if input.MaxRedemptions != nil && *input.MaxRedemptions < 1 {
		return domain.Coupon{}, errs.Invalid("max_redemptions", "must be at least 1")
	}

	if input.ExpiresAt != nil && input.ExpiresAt.Before(time.Now()) {
		return domain.Coupon{}, errs.Invalid("expires_at", "must be in the future")
	}

	// Duration defaults to Forever so older API clients that don't send the
	// field land on the same behaviour they had before FEAT-6.
	duration := input.Duration
	if duration == "" {
		duration = domain.CouponDurationForever
	}
	switch duration {
	case domain.CouponDurationOnce, domain.CouponDurationForever:
		if input.DurationPeriods != nil {
			return domain.Coupon{}, errs.Invalid("duration_periods",
				"only valid when duration is 'repeating'")
		}
	case domain.CouponDurationRepeating:
		if input.DurationPeriods == nil || *input.DurationPeriods < 1 {
			return domain.Coupon{}, errs.Invalid("duration_periods",
				"required and must be at least 1 when duration is 'repeating'")
		}
	default:
		return domain.Coupon{}, errs.Invalid("duration",
			"must be 'once', 'repeating', or 'forever'")
	}

	// Restrictions validation — cheap sanity bounds, not exhaustive.
	if input.Restrictions.MinAmountCents < 0 {
		return domain.Coupon{}, errs.Invalid("restrictions.min_amount_cents", "cannot be negative")
	}
	if input.Restrictions.MaxRedemptionsPerCustomer < 0 {
		return domain.Coupon{}, errs.Invalid("restrictions.max_redemptions_per_customer", "cannot be negative")
	}

	return s.store.Create(ctx, tenantID, domain.Coupon{
		Code:            code,
		Name:            name,
		Type:            input.Type,
		AmountOff:       input.AmountOff,
		PercentOffBP:    input.PercentOffBP,
		Currency:        input.Currency,
		MaxRedemptions:  input.MaxRedemptions,
		ExpiresAt:       input.ExpiresAt,
		PlanIDs:         input.PlanIDs,
		Duration:        duration,
		DurationPeriods: input.DurationPeriods,
		Stackable:       input.Stackable,
		CustomerID:      strings.TrimSpace(input.CustomerID),
		Restrictions:    input.Restrictions,
		Metadata:        input.Metadata,
	})
}

// generateCouponCode returns a cryptographically-random code in the format
// CPN-XXXX-XXXX using Crockford-safe base32 (no I/L/O/U) so codes read and
// type cleanly on the phone. 40 bits of entropy — enough that guessing
// another tenant's private coupon is computationally infeasible within a
// rate-limited surface, and short enough to paste into a sales email.
func generateCouponCode() (string, error) {
	var b [5]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	enc := base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)
	s := enc.EncodeToString(b[:])
	return "CPN-" + s[:4] + "-" + s[4:], nil
}

func (s *Service) Get(ctx context.Context, tenantID, id string) (domain.Coupon, error) {
	return s.store.Get(ctx, tenantID, id)
}

// List returns coupons scoped to the tenant. Archived rows are excluded
// by default so the operator's day-to-day view is clean; pass
// includeArchived=true for the audit/history view.
func (s *Service) List(ctx context.Context, tenantID string, includeArchived bool) ([]domain.Coupon, error) {
	return s.store.List(ctx, tenantID, includeArchived)
}

// UpdateInput is the set of mutable fields the PATCH endpoint accepts.
// Immutable fields (type, code, amount_off, percent_off_bp, currency,
// plan_ids, customer_id, duration, stackable) are frozen post-create so
// redemption semantics under the same code stay consistent.
type UpdateInput struct {
	Name           *string                    `json:"name,omitempty"`
	MaxRedemptions *int                       `json:"max_redemptions,omitempty"`
	ExpiresAt      **time.Time                `json:"expires_at,omitempty"`
	Restrictions   *domain.CouponRestrictions `json:"restrictions,omitempty"`
	Metadata       []byte                     `json:"metadata,omitempty"`
	// IfMatch is the optimistic-concurrency token (current row version)
	// the caller expects to find. Non-nil enables the check; mismatch
	// yields errs.ErrPreconditionFailed. Nil — the field was absent on
	// the wire — skips the check, matching lost-update-tolerant clients
	// (CLI scripts, one-shot tooling) that don't round-trip ETags.
	IfMatch *int `json:"-"`
}

func (s *Service) Update(ctx context.Context, tenantID, id string, in UpdateInput) (domain.Coupon, error) {
	existing, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return domain.Coupon{}, err
	}

	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if name == "" {
			return domain.Coupon{}, errs.Required("name")
		}
		if len(name) > 200 {
			return domain.Coupon{}, errs.Invalid("name", "must be at most 200 characters")
		}
		existing.Name = name
	}
	if in.MaxRedemptions != nil {
		if *in.MaxRedemptions < 1 {
			return domain.Coupon{}, errs.Invalid("max_redemptions", "must be at least 1")
		}
		// Reducing below current times_redeemed would instantly render the
		// coupon invalid — allowed because it's the explicit operator
		// intent to cap further use.
		mr := *in.MaxRedemptions
		existing.MaxRedemptions = &mr
	}
	if in.ExpiresAt != nil {
		// Double-pointer: outer nil = field absent; inner nil = explicit null.
		existing.ExpiresAt = *in.ExpiresAt
	}
	if in.Restrictions != nil {
		if in.Restrictions.MinAmountCents < 0 {
			return domain.Coupon{}, errs.Invalid("restrictions.min_amount_cents", "cannot be negative")
		}
		if in.Restrictions.MaxRedemptionsPerCustomer < 0 {
			return domain.Coupon{}, errs.Invalid("restrictions.max_redemptions_per_customer", "cannot be negative")
		}
		existing.Restrictions = *in.Restrictions
	}
	if in.Metadata != nil {
		existing.Metadata = in.Metadata
	}

	return s.store.Update(ctx, tenantID, existing, in.IfMatch)
}

// Archive marks a coupon archived. The coupon stops accepting new
// redemptions immediately, but existing redemptions continue to apply
// to future invoices until they exhaust their own duration — ongoing
// contracts aren't broken retroactively.
func (s *Service) Archive(ctx context.Context, tenantID, id string) error {
	return s.store.Archive(ctx, tenantID, id, time.Now().UTC())
}

// Unarchive restores an archived coupon. Idempotent on an already-live row.
func (s *Service) Unarchive(ctx context.Context, tenantID, id string) error {
	return s.store.Unarchive(ctx, tenantID, id)
}

type RedeemInput struct {
	Code           string `json:"code"`
	CustomerID     string `json:"customer_id"`
	SubscriptionID string `json:"subscription_id,omitempty"`
	InvoiceID      string `json:"invoice_id,omitempty"`
	PlanID         string `json:"plan_id,omitempty"`
	SubtotalCents  int64  `json:"subtotal_cents"`
	// Currency is the ISO-4217 code of the target invoice/subscription.
	// Optional; when provided, a fixed_amount coupon with a different
	// currency is rejected here (front-loads the error to redemption time
	// rather than letting it surface silently at invoice time). Ignored for
	// percentage coupons since they are currency-agnostic.
	Currency string `json:"currency,omitempty"`
	// IdempotencyKey is the client-supplied retry token. When set, a
	// repeat call with the same key returns the original redemption
	// rather than creating a duplicate. Typically sourced from the
	// Idempotency-Key HTTP header.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// PreviewResult is the dry-run outcome of a Redeem call — validates the
// gates and returns the discount cents without mutating state. Drives
// the "show me the final price before I click Pay" UI.
type PreviewResult struct {
	DiscountCents int64         `json:"discount_cents"`
	Coupon        domain.Coupon `json:"coupon"`
}

// RedeemResult bundles the redemption row with a flag indicating whether
// this call returned a previously-completed request (Replay=true) rather
// than a fresh commit. The HTTP layer uses Replay to set the
// Idempotent-Replay response header and return 200 instead of 201 so
// integrating clients can distinguish a genuine retry-to-success from a
// true first-time create.
type RedeemResult struct {
	Redemption domain.CouponRedemption
	Replay     bool
}

// Preview exercises every redeem gate except the atomic-persist step,
// returning the computed discount. Useful for the cart-preview flow —
// the caller shows the discount alongside the unaffected subtotal, and
// commits via Redeem when the user confirms.
//
// Subtle: Preview uses the point-in-time snapshot of the coupon and can
// therefore race Archive/Expire. The real Redeem path is the source of
// truth; Preview is advisory.
func (s *Service) Preview(ctx context.Context, tenantID string, input RedeemInput) (PreviewResult, error) {
	cpn, err := s.validateRedeem(ctx, tenantID, &input)
	if err != nil {
		return PreviewResult{}, err
	}
	discount := CalculateDiscount(cpn, input.SubtotalCents)
	if discount <= 0 {
		return PreviewResult{}, errs.InvalidState("discount amount is zero")
	}
	return PreviewResult{DiscountCents: discount, Coupon: cpn}, nil
}

// Redeem is a thin wrapper over RedeemDetail that returns only the
// redemption. Prefer RedeemDetail when the caller needs to distinguish a
// fresh redemption from an idempotent replay — typically the HTTP handler,
// which sets the Idempotent-Replay header on the response.
func (s *Service) Redeem(ctx context.Context, tenantID string, input RedeemInput) (domain.CouponRedemption, error) {
	res, err := s.RedeemDetail(ctx, tenantID, input)
	if err != nil {
		return domain.CouponRedemption{}, err
	}
	return res.Redemption, nil
}

// RedeemDetail runs the full redeem pipeline and returns the redemption
// along with whether it was served from the idempotency replay path. Two
// replay entry points feed this flag:
//
//  1. Fast-path: when the caller's idempotency key already matches a
//     committed redemption, skip the validation + atomic insert entirely.
//  2. Store race: when two concurrent requests share a key, one INSERTs
//     successfully and the other sees the unique violation; the store
//     then reads back the winner and flags the loser's result Replay.
func (s *Service) RedeemDetail(ctx context.Context, tenantID string, input RedeemInput) (RedeemResult, error) {
	// Fast-path for idempotency replay: look up by key before even
	// loading the coupon. Saves the round trip on the common retry case.
	if key := strings.TrimSpace(input.IdempotencyKey); key != "" {
		if existing, err := s.store.GetRedemptionByIdempotencyKey(ctx, tenantID, key); err == nil {
			return RedeemResult{Redemption: existing, Replay: true}, nil
		}
	}

	cpn, err := s.validateRedeem(ctx, tenantID, &input)
	if err != nil {
		return RedeemResult{}, err
	}

	discount := CalculateDiscount(cpn, input.SubtotalCents)
	if discount <= 0 {
		return RedeemResult{}, errs.InvalidState("discount amount is zero")
	}

	result, err := s.store.RedeemAtomic(ctx, tenantID, RedeemAtomicInput{
		Code:           cpn.Code,
		CustomerID:     input.CustomerID,
		SubscriptionID: input.SubscriptionID,
		InvoiceID:      input.InvoiceID,
		DiscountCents:  discount,
		IdempotencyKey: input.IdempotencyKey,
	})
	if err != nil {
		return RedeemResult{}, translateGate(err)
	}
	return RedeemResult{Redemption: result.Redemption, Replay: result.Replay}, nil
}

// validateRedeem is the stateless-or-near-stateless gate pass used by
// both Preview and Redeem. It loads the coupon, normalises the code,
// and checks everything we can check without committing a write. The
// final max_redemptions / archived check happens again inside
// RedeemAtomic under row lock — this pass is mostly about surfacing
// friendly error messages before we take the lock.
func (s *Service) validateRedeem(ctx context.Context, tenantID string, input *RedeemInput) (domain.Coupon, error) {
	code := strings.TrimSpace(strings.ToUpper(input.Code))
	if code == "" {
		return domain.Coupon{}, errs.Required("code")
	}
	input.Code = code

	if input.CustomerID == "" {
		return domain.Coupon{}, errs.Required("customer_id")
	}
	if input.SubtotalCents <= 0 {
		return domain.Coupon{}, errs.Invalid("subtotal_cents", "must be greater than 0")
	}

	cpn, err := s.store.GetByCode(ctx, tenantID, code)
	if err != nil {
		return domain.Coupon{}, errs.Invalid("code", "coupon not found")
	}

	if cpn.ArchivedAt != nil {
		return domain.Coupon{}, errs.InvalidState("coupon is not active")
	}

	if cpn.ExpiresAt != nil && cpn.ExpiresAt.Before(time.Now()) {
		return domain.Coupon{}, errs.InvalidState("coupon has expired")
	}

	if cpn.MaxRedemptions != nil && cpn.TimesRedeemed >= *cpn.MaxRedemptions {
		return domain.Coupon{}, errs.InvalidState("coupon has reached maximum redemptions")
	}

	// Private coupon: the error shape mirrors "coupon not found" on
	// purpose so the endpoint doesn't leak that a code exists but isn't
	// yours — private codes are effectively secrets in the enterprise flow.
	if cpn.CustomerID != "" && cpn.CustomerID != input.CustomerID {
		return domain.Coupon{}, errs.Invalid("code", "coupon not found")
	}

	if len(cpn.PlanIDs) > 0 && input.PlanID != "" && !slices.Contains(cpn.PlanIDs, input.PlanID) {
		return domain.Coupon{}, errs.Invalid("plan_id", "coupon is not valid for this plan")
	}

	if cpn.Type == domain.CouponTypeFixedAmount && input.Currency != "" {
		if !strings.EqualFold(cpn.Currency, input.Currency) {
			return domain.Coupon{}, errs.Invalid("currency",
				fmt.Sprintf("coupon currency %s does not match target currency %s",
					strings.ToUpper(cpn.Currency), strings.ToUpper(input.Currency)))
		}
	}

	// Restrictions — checked here because they're cheap. The expensive
	// one (per-customer count) only runs when the restriction is set.
	if !cpn.Restrictions.IsZero() {
		if cpn.Restrictions.MinAmountCents > 0 && input.SubtotalCents < cpn.Restrictions.MinAmountCents {
			return domain.Coupon{}, errs.Invalid("subtotal_cents",
				fmt.Sprintf("coupon requires a minimum order of %d cents", cpn.Restrictions.MinAmountCents))
		}
		if cpn.Restrictions.MaxRedemptionsPerCustomer > 0 {
			n, err := s.store.CountRedemptionsByCustomer(ctx, tenantID, cpn.ID, input.CustomerID)
			if err != nil {
				return domain.Coupon{}, fmt.Errorf("count customer redemptions: %w", err)
			}
			if n >= cpn.Restrictions.MaxRedemptionsPerCustomer {
				return domain.Coupon{}, errs.InvalidState("coupon has reached per-customer redemption limit")
			}
		}
		if cpn.Restrictions.FirstTimeCustomerOnly {
			if s.customerHistory == nil {
				slog.WarnContext(ctx,
					"coupon: first_time_customer_only set but no customer history lookup wired — skipping",
					"coupon_id", cpn.ID)
			} else {
				hasPaid, err := s.customerHistory.HasSucceededInvoice(ctx, tenantID, input.CustomerID)
				if err != nil {
					return domain.Coupon{}, fmt.Errorf("check prior payments: %w", err)
				}
				if hasPaid {
					return domain.Coupon{}, errs.InvalidState("coupon limited to first-time customers")
				}
			}
		}
	}

	return cpn, nil
}

// translateGate converts store-layer gate errors into the
// service-layer DomainError shape. Keeps the "coupon not active / has
// expired / maximum redemptions" user-facing strings stable with the
// pre-refactor behaviour so existing API clients don't see new messages.
func translateGate(err error) error {
	var gate ErrCouponGate
	if !errors.As(err, &gate) {
		return err
	}
	switch gate.Reason {
	case GateArchived:
		return errs.InvalidState("coupon is not active")
	case GateExpired:
		return errs.InvalidState("coupon has expired")
	case GateMaxRedemptions:
		return errs.InvalidState("coupon has reached maximum redemptions")
	case GateNotFound:
		return errs.Invalid("code", "coupon not found")
	default:
		return err
	}
}

// VoidRedemptionsForInvoice is the hook the credit-note flow calls when an
// invoice has been fully credited or refunded. Reverses the coupon usage
// tied to that invoice: each redemption is marked voided, times_redeemed
// on the coupon is decremented, and any periods_applied the billing engine
// had already bumped is rolled back (floored at 0). Idempotent — a repeat
// call voids nothing further.
func (s *Service) VoidRedemptionsForInvoice(ctx context.Context, tenantID, invoiceID string) (int, error) {
	return s.store.VoidRedemptionsForInvoice(ctx, tenantID, invoiceID)
}

func (s *Service) ListRedemptions(ctx context.Context, tenantID, couponID string) ([]domain.CouponRedemption, error) {
	return s.store.ListRedemptions(ctx, tenantID, couponID)
}

// ApplyToInvoice computes the coupon discount for an invoice on the given
// subscription. It walks active redemptions, filters by eligibility
// (coupon not archived, not expired, plan match, and duration not yet
// exhausted), then either picks the best single coupon or combines
// stackable coupons — whichever policy is correct for the mix.
//
// Stacking rules:
//   - If any eligible coupon is non-stackable, only the single largest
//     discount wins.
//   - If every eligible coupon is stackable, percent_offs sum (capped at
//     100%) and fixed amount_offs sum, each applied to the gross subtotal;
//     the combined discount is clamped to the subtotal.
//
// Side-effect-free: no store writes. The caller (billing engine) owns the
// "mark applied" step so a failed invoice create doesn't burn a period of
// a repeating coupon.
//
// ApplyToInvoice takes the full set of plan_ids on the target subscription
// (one per item) rather than a single plan_id. A coupon whose PlanIDs gate
// references any plan the subscription currently carries is eligible.
//
// customerID scopes the check defensively: a private coupon (Coupon.CustomerID
// non-empty) must match the invoice's customer, and a redemption stamped for
// a different customer is skipped. Defence-in-depth for the case where the
// redemption path missed a gate — better to drop the discount than to honour
// a stranger's private coupon.
//
// invoiceCurrency is the currency the invoice will settle in. Fixed-amount
// coupons whose stored currency differs are skipped (with a warning). Percentage
// coupons are currency-agnostic and pass through regardless.
func (s *Service) ApplyToInvoice(ctx context.Context, tenantID, subscriptionID, customerID, invoiceCurrency string, planIDs []string, subtotalCents int64) (domain.CouponDiscountResult, error) {
	if subscriptionID == "" || subtotalCents <= 0 {
		return domain.CouponDiscountResult{}, nil
	}

	redemptions, err := s.store.ListRedemptionsBySubscription(ctx, tenantID, subscriptionID)
	if err != nil {
		return domain.CouponDiscountResult{}, fmt.Errorf("list redemptions: %w", err)
	}
	if len(redemptions) == 0 {
		return domain.CouponDiscountResult{}, nil
	}

	couponIDs := make([]string, 0, len(redemptions))
	seen := make(map[string]struct{}, len(redemptions))
	for _, r := range redemptions {
		if _, ok := seen[r.CouponID]; ok {
			continue
		}
		seen[r.CouponID] = struct{}{}
		couponIDs = append(couponIDs, r.CouponID)
	}
	coupons, err := s.store.GetByIDs(ctx, tenantID, couponIDs)
	if err != nil {
		return domain.CouponDiscountResult{}, fmt.Errorf("load coupons: %w", err)
	}

	now := time.Now()

	type eligible struct {
		coupon     domain.Coupon
		redemption domain.CouponRedemption
	}
	var pool []eligible

	for _, r := range redemptions {
		cpn, ok := coupons[r.CouponID]
		if !ok {
			continue
		}
		if customerID != "" && cpn.CustomerID != "" && cpn.CustomerID != customerID {
			slog.Warn("coupon: private coupon customer mismatch — skipping",
				"coupon_id", cpn.ID,
				"coupon_customer_id", cpn.CustomerID,
				"invoice_customer_id", customerID,
				"subscription_id", subscriptionID)
			continue
		}
		if customerID != "" && r.CustomerID != "" && r.CustomerID != customerID {
			slog.Warn("coupon: redemption customer mismatch — skipping",
				"redemption_id", r.ID,
				"redemption_customer_id", r.CustomerID,
				"invoice_customer_id", customerID,
				"subscription_id", subscriptionID)
			continue
		}
		if cpn.ArchivedAt != nil {
			continue
		}
		if cpn.ExpiresAt != nil && cpn.ExpiresAt.Before(now) {
			continue
		}
		if len(cpn.PlanIDs) > 0 && len(planIDs) > 0 && !anyPlanMatches(cpn.PlanIDs, planIDs) {
			continue
		}
		if !durationHasPeriodLeft(cpn, r) {
			continue
		}
		if cpn.Type == domain.CouponTypeFixedAmount && invoiceCurrency != "" &&
			!strings.EqualFold(cpn.Currency, invoiceCurrency) {
			slog.Warn("coupon: currency mismatch — skipping fixed-amount coupon",
				"coupon_id", cpn.ID,
				"coupon_currency", cpn.Currency,
				"invoice_currency", invoiceCurrency,
				"subscription_id", subscriptionID)
			continue
		}
		pool = append(pool, eligible{coupon: cpn, redemption: r})
	}

	if len(pool) == 0 {
		return domain.CouponDiscountResult{}, nil
	}

	anyNonStackable := slices.ContainsFunc(pool, func(e eligible) bool { return !e.coupon.Stackable })
	if anyNonStackable {
		var bestIdx int
		var bestCents int64
		for i, e := range pool {
			d := CalculateDiscount(e.coupon, subtotalCents)
			if d > bestCents {
				bestCents = d
				bestIdx = i
			}
		}
		if bestCents == 0 {
			return domain.CouponDiscountResult{}, nil
		}
		return domain.CouponDiscountResult{
			Cents:         bestCents,
			RedemptionIDs: []string{pool[bestIdx].redemption.ID},
		}, nil
	}

	// All stackable — combine percent_offs (capped 100%) and amount_offs.
	var percentBPSum int
	var fixedSum int64
	for _, e := range pool {
		switch e.coupon.Type {
		case domain.CouponTypePercentage:
			percentBPSum += e.coupon.PercentOffBP
		case domain.CouponTypeFixedAmount:
			fixedSum += e.coupon.AmountOff
		}
	}
	if percentBPSum > 10000 {
		percentBPSum = 10000
	}
	percentCents := money.RoundHalfToEven(subtotalCents*int64(percentBPSum), 10000)
	total := min(percentCents+fixedSum, subtotalCents)
	if total <= 0 {
		return domain.CouponDiscountResult{}, nil
	}

	ids := make([]string, 0, len(pool))
	for _, e := range pool {
		ids = append(ids, e.redemption.ID)
	}
	return domain.CouponDiscountResult{Cents: total, RedemptionIDs: ids}, nil
}

// anyPlanMatches returns true if any plan_id in itemPlans appears in the
// coupon's allowed PlanIDs gate. Used to evaluate coupon eligibility against
// the full item set of a multi-item subscription — a coupon for "Plan A"
// activates so long as Plan A is one of the items.
func anyPlanMatches(couponPlans, itemPlans []string) bool {
	for _, p := range itemPlans {
		if slices.Contains(couponPlans, p) {
			return true
		}
	}
	return false
}

// durationHasPeriodLeft reports whether the redemption still has at least
// one billing period to apply against under the coupon's duration rule.
// Forever always returns true; once exhausts after the first application;
// repeating exhausts once periods_applied reaches duration_periods.
func durationHasPeriodLeft(c domain.Coupon, r domain.CouponRedemption) bool {
	switch c.Duration {
	case domain.CouponDurationOnce:
		return r.PeriodsApplied < 1
	case domain.CouponDurationRepeating:
		if c.DurationPeriods == nil {
			return false
		}
		return r.PeriodsApplied < *c.DurationPeriods
	case domain.CouponDurationForever, "":
		return true
	default:
		return false
	}
}

// MarkPeriodsApplied advances the periods_applied counter on each
// redemption by one. Callers invoke this after the invoice that consumed
// the discount commits — doing it beforehand would burn a period of a
// repeating coupon even if the invoice create rolled back. The store does
// the bump in a single tx so partial application can't leave some
// redemptions bumped and others not — the pre-batch loop here previously
// swallowed all-but-the-first error, which hid exactly that case.
func (s *Service) MarkPeriodsApplied(ctx context.Context, tenantID string, redemptionIDs []string) error {
	ids := redemptionIDs[:0:0]
	for _, id := range redemptionIDs {
		if id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return s.store.IncrementPeriodsApplied(ctx, tenantID, ids)
}

// CalculateDiscount computes the discount amount in cents for a given
// coupon and subtotal. Pure integer math — no float conversion — so the
// output is byte-deterministic across platforms and immune to the
// well-known float-rounding drift that affects large totals (e.g.
// 99_999_999 cents × 1 bp on a float path would silently lose the
// trailing digit). Uses the shared money.RoundHalfToEven helper for
// banker's rounding so repeated small discounts don't systematically
// favour one side; the same rule is applied across pricing, tax, and
// subscription proration.
//
// Overflow bound: subtotalCents × 10000 must fit in int64. With a
// practical invoice ceiling of ~$10M (1e9 cents) the product is 1e13,
// five orders of magnitude below int64 max.
func CalculateDiscount(c domain.Coupon, subtotalCents int64) int64 {
	if subtotalCents <= 0 {
		return 0
	}

	switch c.Type {
	case domain.CouponTypePercentage:
		// percent_off_bp is basis points: 5050 = 50.50%.
		discount := money.RoundHalfToEven(subtotalCents*int64(c.PercentOffBP), 10000)
		if discount > subtotalCents {
			return subtotalCents
		}
		return discount
	case domain.CouponTypeFixedAmount:
		if c.AmountOff > subtotalCents {
			return subtotalCents
		}
		return c.AmountOff
	default:
		return 0
	}
}
