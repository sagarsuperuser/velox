package coupon

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

// pastTime is a fixed timestamp in the past used to simulate archived /
// expired coupons in the seed helpers.
var pastTime = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

// ---------------------------------------------------------------------------
// In-memory mock store
// ---------------------------------------------------------------------------

type mockStore struct {
	coupons     map[string]domain.Coupon
	byCode      map[string]domain.Coupon
	redemptions []domain.CouponRedemption
	nextID      int
	createErr   error
}

func newMockStore() *mockStore {
	return &mockStore{
		coupons: make(map[string]domain.Coupon),
		byCode:  make(map[string]domain.Coupon),
	}
}

func (m *mockStore) Create(_ context.Context, _ string, c domain.Coupon) (domain.Coupon, error) {
	if m.createErr != nil {
		return domain.Coupon{}, m.createErr
	}
	m.nextID++
	c.ID = fmt.Sprintf("cpn_%d", m.nextID)
	c.CreatedAt = time.Now().UTC()
	m.coupons[c.ID] = c
	m.byCode[c.Code] = c
	return c, nil
}

func (m *mockStore) Get(_ context.Context, _, id string) (domain.Coupon, error) {
	c, ok := m.coupons[id]
	if !ok {
		return domain.Coupon{}, fmt.Errorf("not found")
	}
	return c, nil
}

func (m *mockStore) GetByCode(_ context.Context, _, code string) (domain.Coupon, error) {
	c, ok := m.byCode[code]
	if !ok {
		return domain.Coupon{}, fmt.Errorf("not found")
	}
	return c, nil
}

func (m *mockStore) GetByIDs(_ context.Context, _ string, ids []string) (map[string]domain.Coupon, error) {
	out := make(map[string]domain.Coupon, len(ids))
	for _, id := range ids {
		if c, ok := m.coupons[id]; ok {
			out[id] = c
		}
	}
	return out, nil
}

func (m *mockStore) List(_ context.Context, _ string, includeArchived bool) ([]domain.Coupon, error) {
	var result []domain.Coupon
	for _, c := range m.coupons {
		if !includeArchived && c.ArchivedAt != nil {
			continue
		}
		result = append(result, c)
	}
	return result, nil
}

func (m *mockStore) Update(_ context.Context, _ string, c domain.Coupon) (domain.Coupon, error) {
	m.coupons[c.ID] = c
	m.byCode[c.Code] = c
	return c, nil
}

func (m *mockStore) Archive(_ context.Context, _, id string, at time.Time) error {
	c, ok := m.coupons[id]
	if !ok {
		return fmt.Errorf("not found")
	}
	if c.ArchivedAt == nil {
		t := at
		c.ArchivedAt = &t
	}
	m.coupons[id] = c
	m.byCode[c.Code] = c
	return nil
}

func (m *mockStore) Unarchive(_ context.Context, _, id string) error {
	c, ok := m.coupons[id]
	if !ok {
		return fmt.Errorf("not found")
	}
	c.ArchivedAt = nil
	m.coupons[id] = c
	m.byCode[c.Code] = c
	return nil
}

// RedeemAtomic in the mock mirrors the Postgres path closely: a single
// "tx" loads the coupon, re-checks the gates under lock, increments the
// counter, and appends a redemption. Idempotency replay returns an
// existing redemption with Replay=true.
func (m *mockStore) RedeemAtomic(_ context.Context, _ string, in RedeemAtomicInput) (RedeemAtomicResult, error) {
	if in.IdempotencyKey != "" {
		for _, r := range m.redemptions {
			if r.IdempotencyKey == in.IdempotencyKey {
				c := m.coupons[r.CouponID]
				return RedeemAtomicResult{Coupon: c, Redemption: r, Replay: true}, nil
			}
		}
	}
	c, ok := m.byCode[in.Code]
	if !ok {
		return RedeemAtomicResult{}, ErrCouponGate{Reason: GateNotFound}
	}
	if c.ArchivedAt != nil {
		return RedeemAtomicResult{}, ErrCouponGate{Reason: GateArchived}
	}
	now := time.Now()
	if c.ExpiresAt != nil && !c.ExpiresAt.After(now) {
		return RedeemAtomicResult{}, ErrCouponGate{Reason: GateExpired}
	}
	if c.MaxRedemptions != nil && c.TimesRedeemed >= *c.MaxRedemptions {
		return RedeemAtomicResult{}, ErrCouponGate{Reason: GateMaxRedemptions}
	}
	c.TimesRedeemed++
	m.coupons[c.ID] = c
	m.byCode[c.Code] = c

	m.nextID++
	r := domain.CouponRedemption{
		ID:             fmt.Sprintf("red_%d", m.nextID),
		CouponID:       c.ID,
		CustomerID:     in.CustomerID,
		SubscriptionID: in.SubscriptionID,
		InvoiceID:      in.InvoiceID,
		DiscountCents:  in.DiscountCents,
		IdempotencyKey: in.IdempotencyKey,
		CreatedAt:      now.UTC(),
	}
	m.redemptions = append(m.redemptions, r)
	return RedeemAtomicResult{Coupon: c, Redemption: r, Replay: false}, nil
}

func (m *mockStore) GetRedemptionByIdempotencyKey(_ context.Context, _, key string) (domain.CouponRedemption, error) {
	for _, r := range m.redemptions {
		if r.IdempotencyKey == key {
			return r, nil
		}
	}
	return domain.CouponRedemption{}, fmt.Errorf("not found")
}

func (m *mockStore) ListRedemptions(_ context.Context, _, _ string) ([]domain.CouponRedemption, error) {
	return m.redemptions, nil
}

func (m *mockStore) ListRedemptionsBySubscription(_ context.Context, _, subscriptionID string) ([]domain.CouponRedemption, error) {
	var out []domain.CouponRedemption
	for _, r := range m.redemptions {
		if r.SubscriptionID == subscriptionID {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *mockStore) CountRedemptionsByCustomer(_ context.Context, _, couponID, customerID string) (int, error) {
	var n int
	for _, r := range m.redemptions {
		if r.CouponID == couponID && r.CustomerID == customerID {
			n++
		}
	}
	return n, nil
}

func (m *mockStore) IncrementPeriodsApplied(_ context.Context, _ string, ids []string) error {
	for _, id := range ids {
		found := false
		for i := range m.redemptions {
			if m.redemptions[i].ID == id {
				m.redemptions[i].PeriodsApplied++
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("redemption %s not found", id)
		}
	}
	return nil
}

// seedRedemption appends a redemption with an auto-assigned ID so tests
// can round-trip the ID through MarkPeriodsApplied. Pre-FEAT-6 tests
// appended to m.redemptions directly with no ID — kept working because
// ApplyToInvoice didn't care, but the new duration tests do.
func (m *mockStore) seedRedemption(r domain.CouponRedemption) string {
	if r.ID == "" {
		m.nextID++
		r.ID = fmt.Sprintf("red_%d", m.nextID)
	}
	m.redemptions = append(m.redemptions, r)
	return r.ID
}

// seedCoupon inserts a coupon directly into the mock store for Redeem tests.
func (m *mockStore) seedCoupon(c domain.Coupon) {
	if c.ID == "" {
		m.nextID++
		c.ID = fmt.Sprintf("cpn_%d", m.nextID)
	}
	m.coupons[c.ID] = c
	m.byCode[c.Code] = c
}

// ---------------------------------------------------------------------------
// CalculateDiscount — pure function tests (no store needed)
// ---------------------------------------------------------------------------

func TestCalculateDiscount_Percentage(t *testing.T) {
	tests := []struct {
		name     string
		pct      float64
		subtotal int64
		wantDisc int64
	}{
		{
			name:     "25% of 10000 cents",
			pct:      25,
			subtotal: 10000,
			wantDisc: 2500,
		},
		{
			name:     "100% returns full subtotal",
			pct:      100,
			subtotal: 7777,
			wantDisc: 7777,
		},
		{
			name:     "150% caps at subtotal",
			pct:      150,
			subtotal: 5000,
			wantDisc: 5000,
		},
		{
			name:     "zero subtotal returns 0",
			pct:      25,
			subtotal: 0,
			wantDisc: 0,
		},
		{
			name:     "negative subtotal returns 0",
			pct:      25,
			subtotal: -500,
			wantDisc: 0,
		},
		{
			name:     "0.5% of 100 cents — banker's tie to even",
			pct:      0.5,
			subtotal: 100,
			// 100 * 0.5 / 100 = 0.5 exactly. Banker's rounds to nearest even → 0.
			// Half-up would give 1. Divergence is intentional (see money.RoundHalfToEven
			// docstring for the zero-bias rationale).
			wantDisc: 0,
		},
		{
			name:     "0.5% of 99 cents — rounds to 0",
			pct:      0.5,
			subtotal: 99,
			// 99 * 0.5 / 100 = 0.495, math.Round(0.495) = 0
			wantDisc: 0,
		},
		{
			name:     "small percentage on large subtotal",
			pct:      0.01,
			subtotal: 999_999_99, // $9,999,999.99
			// 99999999 * 0.01 / 100 = 9999.9999, math.Round = 10000
			wantDisc: 10000,
		},
		{
			name:     "50% of 1 cent — banker's tie to even",
			pct:      50,
			subtotal: 1,
			// 1 * 50 / 100 = 0.5 exactly. Banker's rounds to nearest even → 0.
			wantDisc: 0,
		},
		{
			name:     "33.33% of 300 cents",
			pct:      33.33,
			subtotal: 300,
			// 300 * 33.33 / 100 = 99.99, math.Round = 100
			wantDisc: 100,
		},
		{
			name:     "10% of 1 cent",
			pct:      10,
			subtotal: 1,
			// 1 * 10 / 100 = 0.1, math.Round(0.1) = 0
			wantDisc: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := domain.Coupon{Type: domain.CouponTypePercentage, PercentOffBP: int(tt.pct * 100)}
			got := CalculateDiscount(c, tt.subtotal)
			if got != tt.wantDisc {
				raw := float64(tt.subtotal) * tt.pct / 100
				t.Errorf("CalculateDiscount(pct=%.2f, subtotal=%d) = %d, want %d (raw float: %f, RoundToEven: %d)",
					tt.pct, tt.subtotal, got, tt.wantDisc, raw, int64(math.RoundToEven(raw)))
			}
		})
	}
}

func TestCalculateDiscount_FixedAmount(t *testing.T) {
	tests := []struct {
		name      string
		amountOff int64
		subtotal  int64
		wantDisc  int64
	}{
		{
			name:      "fixed less than subtotal",
			amountOff: 2000,
			subtotal:  10000,
			wantDisc:  2000,
		},
		{
			name:      "fixed greater than subtotal caps",
			amountOff: 15000,
			subtotal:  10000,
			wantDisc:  10000,
		},
		{
			name:      "fixed exactly equals subtotal",
			amountOff: 5000,
			subtotal:  5000,
			wantDisc:  5000,
		},
		{
			name:      "zero subtotal returns 0",
			amountOff: 2000,
			subtotal:  0,
			wantDisc:  0,
		},
		{
			name:      "negative subtotal returns 0",
			amountOff: 2000,
			subtotal:  -100,
			wantDisc:  0,
		},
		{
			name:      "1 cent fixed off 1 cent subtotal",
			amountOff: 1,
			subtotal:  1,
			wantDisc:  1,
		},
		{
			name:      "large fixed amount on small subtotal",
			amountOff: 100_000_000,
			subtotal:  500,
			wantDisc:  500,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := domain.Coupon{Type: domain.CouponTypeFixedAmount, AmountOff: tt.amountOff}
			got := CalculateDiscount(c, tt.subtotal)
			if got != tt.wantDisc {
				t.Errorf("CalculateDiscount(amountOff=%d, subtotal=%d) = %d, want %d",
					tt.amountOff, tt.subtotal, got, tt.wantDisc)
			}
		})
	}
}

func TestCalculateDiscount_UnknownType(t *testing.T) {
	c := domain.Coupon{Type: "bogus", PercentOffBP: 5000, AmountOff: 1000}
	got := CalculateDiscount(c, 10000)
	if got != 0 {
		t.Errorf("unknown coupon type should return 0 discount, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// Create — validation tests
// ---------------------------------------------------------------------------

// Empty code now auto-generates (enterprise private-coupon flow) — the
// returned code must pass the same format regexp that user-supplied codes
// are validated against, so the two paths are interchangeable downstream.
func TestCreate_EmptyCodeAutoGenerates(t *testing.T) {
	svc := NewService(newMockStore())
	cpn, err := svc.Create(context.Background(), "t1", CreateInput{
		Code: "", Name: "Test", Type: domain.CouponTypePercentage, PercentOffBP: 1000,
	})
	if err != nil {
		t.Fatalf("empty code should auto-generate, got error: %v", err)
	}
	if cpn.Code == "" {
		t.Fatal("generated code is empty")
	}
	if !codeRegexp.MatchString(cpn.Code) {
		t.Errorf("generated code %q doesn't match coupon code format", cpn.Code)
	}
}

func TestCreate_WhitespaceOnlyCodeAutoGenerates(t *testing.T) {
	svc := NewService(newMockStore())
	cpn, err := svc.Create(context.Background(), "t1", CreateInput{
		Code: "   ", Name: "Test", Type: domain.CouponTypePercentage, PercentOffBP: 1000,
	})
	if err != nil {
		t.Fatalf("whitespace code should auto-generate, got error: %v", err)
	}
	if cpn.Code == "" || cpn.Code == "   " {
		t.Errorf("whitespace should auto-generate, got %q", cpn.Code)
	}
}

// generateCouponCode collision-risk sanity: producing many codes should
// never collide in a reasonable sample. 40 bits of entropy = 2^40 space,
// so 1000 draws is comfortably below the birthday-collision range.
func TestGenerateCouponCode_Uniqueness(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for i := range 1000 {
		code, err := generateCouponCode()
		if err != nil {
			t.Fatalf("generateCouponCode: %v", err)
		}
		if _, dup := seen[code]; dup {
			t.Fatalf("duplicate code generated after %d draws: %s", i, code)
		}
		seen[code] = struct{}{}
		if !codeRegexp.MatchString(code) {
			t.Errorf("generated code %q rejected by codeRegexp", code)
		}
	}
}

func TestCreate_InvalidCodeFormatRejected(t *testing.T) {
	tests := []struct {
		name string
		code string
	}{
		{"too short (2 chars)", "AB"},
		{"special chars", "SAVE@50"},
		{"starts with dash", "-SAVE50"},
		{"ends with dash", "SAVE50-"},
		{"spaces", "SA VE"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(newMockStore())
			_, err := svc.Create(context.Background(), "t1", CreateInput{
				Code: tt.code, Name: "Test", Type: domain.CouponTypePercentage, PercentOffBP: 1000,
			})
			assertErrContains(t, err, "code must be")
		})
	}
}

func TestCreate_ValidCodeFormats(t *testing.T) {
	tests := []struct {
		name string
		code string
	}{
		{"simple alphanumeric", "SAVE50"},
		{"with dashes", "SUMMER-2025-SALE"},
		{"lowercase is uppercased", "save50"},
		{"minimum 3 chars", "ABC"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(newMockStore())
			got, err := svc.Create(context.Background(), "t1", CreateInput{
				Code: tt.code, Name: "Test", Type: domain.CouponTypePercentage, PercentOffBP: 1000,
			})
			if err != nil {
				t.Fatalf("expected no error, got: %v", err)
			}
			// Verify code is uppercased and trimmed
			if got.Code == "" {
				t.Error("returned coupon has empty code")
			}
		})
	}
}

func TestCreate_NameRequired(t *testing.T) {
	svc := NewService(newMockStore())
	_, err := svc.Create(context.Background(), "t1", CreateInput{
		Code: "SAVE50", Name: "", Type: domain.CouponTypePercentage, PercentOffBP: 1000,
	})
	assertErrContains(t, err, "name is required")
}

func TestCreate_NameTooLong(t *testing.T) {
	svc := NewService(newMockStore())
	longName := make([]byte, 201)
	for i := range longName {
		longName[i] = 'A'
	}
	_, err := svc.Create(context.Background(), "t1", CreateInput{
		Code: "SAVE50", Name: string(longName), Type: domain.CouponTypePercentage, PercentOffBP: 1000,
	})
	assertErrContains(t, err, "name must be at most 200")
}

func TestCreate_PercentageValidation(t *testing.T) {
	tests := []struct {
		name    string
		pct     float64
		wantErr string
	}{
		{"zero percent", 0, "percent_off_bp must be between 1 and 10000"},
		{"negative percent", -5, "percent_off_bp must be between 1 and 10000"},
		{"over 100 percent", 101, "percent_off_bp must be between 1 and 10000"},
		{"valid 1%", 1, ""},
		{"valid 100%", 100, ""},
		{"valid 50.5%", 50.5, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(newMockStore())
			_, err := svc.Create(context.Background(), "t1", CreateInput{
				Code: "TEST10", Name: "Test", Type: domain.CouponTypePercentage, PercentOffBP: int(tt.pct * 100),
			})
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("expected success, got: %v", err)
				}
			} else {
				assertErrContains(t, err, tt.wantErr)
			}
		})
	}
}

func TestCreate_FixedAmountValidation(t *testing.T) {
	tests := []struct {
		name      string
		amountOff int64
		currency  string
		wantErr   string
	}{
		{"zero amount", 0, "USD", "amount_off must be greater than 0"},
		{"negative amount", -100, "USD", "amount_off must be greater than 0"},
		{"exceeds cap", 100_000_001, "USD", "amount_off cannot exceed"},
		{"missing currency", 1000, "", "currency is required"},
		{"valid", 5000, "usd", ""},
		{"at cap", 100_000_000, "EUR", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(newMockStore())
			_, err := svc.Create(context.Background(), "t1", CreateInput{
				Code: "FIXED10", Name: "Test", Type: domain.CouponTypeFixedAmount,
				AmountOff: tt.amountOff, Currency: tt.currency,
			})
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("expected success, got: %v", err)
				}
			} else {
				assertErrContains(t, err, tt.wantErr)
			}
		})
	}
}

func TestCreate_InvalidType(t *testing.T) {
	svc := NewService(newMockStore())
	_, err := svc.Create(context.Background(), "t1", CreateInput{
		Code: "TEST10", Name: "Test", Type: "bogus",
	})
	assertErrContains(t, err, "type must be")
}

func TestCreate_MaxRedemptionsMustBePositive(t *testing.T) {
	svc := NewService(newMockStore())
	zero := 0
	_, err := svc.Create(context.Background(), "t1", CreateInput{
		Code: "TEST10", Name: "Test", Type: domain.CouponTypePercentage,
		PercentOffBP: 1000, MaxRedemptions: &zero,
	})
	assertErrContains(t, err, "max_redemptions must be at least 1")

	negative := -1
	_, err = svc.Create(context.Background(), "t1", CreateInput{
		Code: "TEST11", Name: "Test", Type: domain.CouponTypePercentage,
		PercentOffBP: 1000, MaxRedemptions: &negative,
	})
	assertErrContains(t, err, "max_redemptions must be at least 1")
}

func TestCreate_MaxRedemptionsValidWhenOne(t *testing.T) {
	svc := NewService(newMockStore())
	one := 1
	_, err := svc.Create(context.Background(), "t1", CreateInput{
		Code: "ONETIME", Name: "One-time", Type: domain.CouponTypePercentage,
		PercentOffBP: 1000, MaxRedemptions: &one,
	})
	if err != nil {
		t.Fatalf("max_redemptions=1 should be valid, got: %v", err)
	}
}

func TestCreate_ExpiresAtMustBeInFuture(t *testing.T) {
	svc := NewService(newMockStore())
	past := time.Now().Add(-1 * time.Hour)
	_, err := svc.Create(context.Background(), "t1", CreateInput{
		Code: "TEST10", Name: "Test", Type: domain.CouponTypePercentage,
		PercentOffBP: 1000, ExpiresAt: &past,
	})
	assertErrContains(t, err, "expires_at must be in the future")
}

func TestCreate_ExpiresAtInFutureAccepted(t *testing.T) {
	svc := NewService(newMockStore())
	future := time.Now().Add(24 * time.Hour)
	_, err := svc.Create(context.Background(), "t1", CreateInput{
		Code: "TEST10", Name: "Test", Type: domain.CouponTypePercentage,
		PercentOffBP: 1000, ExpiresAt: &future,
	})
	if err != nil {
		t.Fatalf("future expires_at should be valid, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Redeem — validation and business logic tests
// ---------------------------------------------------------------------------

func TestRedeem_CodeRequired(t *testing.T) {
	svc := NewService(newMockStore())
	_, err := svc.Redeem(context.Background(), "t1", RedeemInput{
		Code: "", CustomerID: "cust_1", SubtotalCents: 10000,
	})
	assertErrContains(t, err, "code is required")
}

func TestRedeem_CustomerIDRequired(t *testing.T) {
	svc := NewService(newMockStore())
	_, err := svc.Redeem(context.Background(), "t1", RedeemInput{
		Code: "SAVE50", CustomerID: "", SubtotalCents: 10000,
	})
	assertErrContains(t, err, "customer_id is required")
}

func TestRedeem_SubtotalMustBePositive(t *testing.T) {
	svc := NewService(newMockStore())
	_, err := svc.Redeem(context.Background(), "t1", RedeemInput{
		Code: "SAVE50", CustomerID: "cust_1", SubtotalCents: 0,
	})
	assertErrContains(t, err, "subtotal_cents must be greater than 0")

	_, err = svc.Redeem(context.Background(), "t1", RedeemInput{
		Code: "SAVE50", CustomerID: "cust_1", SubtotalCents: -100,
	})
	assertErrContains(t, err, "subtotal_cents must be greater than 0")
}

func TestRedeem_CouponNotFound(t *testing.T) {
	svc := NewService(newMockStore())
	_, err := svc.Redeem(context.Background(), "t1", RedeemInput{
		Code: "NONEXISTENT", CustomerID: "cust_1", SubtotalCents: 10000,
	})
	assertErrContains(t, err, "coupon not found")
}

func TestRedeem_InactiveCouponRejected(t *testing.T) {
	store := newMockStore()
	store.seedCoupon(domain.Coupon{
		Code: "INACTIVE", Name: "Dead", Type: domain.CouponTypePercentage,
		PercentOffBP: 1000, ArchivedAt: &pastTime,
	})
	svc := NewService(store)
	_, err := svc.Redeem(context.Background(), "t1", RedeemInput{
		Code: "INACTIVE", CustomerID: "cust_1", SubtotalCents: 10000,
	})
	assertErrContains(t, err, "coupon is not active")
}

func TestRedeem_ExpiredCouponRejected(t *testing.T) {
	store := newMockStore()
	past := time.Now().Add(-1 * time.Hour)
	store.seedCoupon(domain.Coupon{
		Code: "EXPIRED", Name: "Old", Type: domain.CouponTypePercentage,
		PercentOffBP: 1000, ExpiresAt: &past,
	})
	svc := NewService(store)
	_, err := svc.Redeem(context.Background(), "t1", RedeemInput{
		Code: "EXPIRED", CustomerID: "cust_1", SubtotalCents: 10000,
	})
	assertErrContains(t, err, "coupon has expired")
}

func TestRedeem_MaxRedemptionsReached(t *testing.T) {
	store := newMockStore()
	maxR := 3
	store.seedCoupon(domain.Coupon{
		Code: "LIMITED", Name: "Limited", Type: domain.CouponTypePercentage,
		PercentOffBP: 1000, MaxRedemptions: &maxR, TimesRedeemed: 3,
	})
	svc := NewService(store)
	_, err := svc.Redeem(context.Background(), "t1", RedeemInput{
		Code: "LIMITED", CustomerID: "cust_1", SubtotalCents: 10000,
	})
	assertErrContains(t, err, "maximum redemptions")
}

func TestRedeem_PlanIDRestriction(t *testing.T) {
	store := newMockStore()
	store.seedCoupon(domain.Coupon{
		Code: "PLANONLY", Name: "Plan-restricted", Type: domain.CouponTypePercentage,
		PercentOffBP: 2000, PlanIDs: []string{"plan_pro", "plan_enterprise"},
	})
	svc := NewService(store)

	// Wrong plan
	_, err := svc.Redeem(context.Background(), "t1", RedeemInput{
		Code: "PLANONLY", CustomerID: "cust_1", SubtotalCents: 10000, PlanID: "plan_free",
	})
	assertErrContains(t, err, "not valid for this plan")

	// Correct plan — should succeed
	red, err := svc.Redeem(context.Background(), "t1", RedeemInput{
		Code: "PLANONLY", CustomerID: "cust_1", SubtotalCents: 10000, PlanID: "plan_pro",
	})
	if err != nil {
		t.Fatalf("expected success for allowed plan, got: %v", err)
	}
	if red.DiscountCents != 2000 {
		t.Errorf("expected discount 2000, got %d", red.DiscountCents)
	}
}

func TestRedeem_SuccessfulPercentage(t *testing.T) {
	store := newMockStore()
	store.seedCoupon(domain.Coupon{
		Code: "SAVE25", Name: "25% Off", Type: domain.CouponTypePercentage,
		PercentOffBP: 2500,
	})
	svc := NewService(store)

	red, err := svc.Redeem(context.Background(), "t1", RedeemInput{
		Code: "SAVE25", CustomerID: "cust_1", SubtotalCents: 10000,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if red.DiscountCents != 2500 {
		t.Errorf("expected discount 2500, got %d", red.DiscountCents)
	}
	if red.CustomerID != "cust_1" {
		t.Errorf("expected customer_id cust_1, got %s", red.CustomerID)
	}

	// Verify redemption count was incremented
	cpn, _ := store.GetByCode(context.Background(), "t1", "SAVE25")
	if cpn.TimesRedeemed != 1 {
		t.Errorf("expected times_redeemed=1, got %d", cpn.TimesRedeemed)
	}
}

func TestRedeem_SuccessfulFixedAmount(t *testing.T) {
	store := newMockStore()
	store.seedCoupon(domain.Coupon{
		Code: "FLAT500", Name: "$5 Off", Type: domain.CouponTypeFixedAmount,
		AmountOff: 500,
	})
	svc := NewService(store)

	red, err := svc.Redeem(context.Background(), "t1", RedeemInput{
		Code: "FLAT500", CustomerID: "cust_1", SubtotalCents: 10000,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if red.DiscountCents != 500 {
		t.Errorf("expected discount 500, got %d", red.DiscountCents)
	}
}

func TestRedeem_ZeroDiscountRejected(t *testing.T) {
	// A very small percentage on a tiny subtotal rounds to 0
	store := newMockStore()
	store.seedCoupon(domain.Coupon{
		Code: "TINY", Name: "Tiny", Type: domain.CouponTypePercentage,
		PercentOffBP: 10,
	})
	svc := NewService(store)

	_, err := svc.Redeem(context.Background(), "t1", RedeemInput{
		Code: "TINY", CustomerID: "cust_1", SubtotalCents: 1, // 1 cent * 0.1% = 0.001 -> rounds to 0
	})
	assertErrContains(t, err, "discount amount is zero")
}

func TestRedeem_CodeIsCaseInsensitive(t *testing.T) {
	store := newMockStore()
	store.seedCoupon(domain.Coupon{
		Code: "SAVE50", Name: "50% Off", Type: domain.CouponTypePercentage,
		PercentOffBP: 5000,
	})
	svc := NewService(store)

	// Lowercase input should be uppercased and match
	red, err := svc.Redeem(context.Background(), "t1", RedeemInput{
		Code: "save50", CustomerID: "cust_1", SubtotalCents: 10000,
	})
	if err != nil {
		t.Fatalf("expected case-insensitive match, got: %v", err)
	}
	if red.DiscountCents != 5000 {
		t.Errorf("expected discount 5000, got %d", red.DiscountCents)
	}
}

func TestRedeem_FixedAmountCurrencyMismatchRejected(t *testing.T) {
	store := newMockStore()
	store.seedCoupon(domain.Coupon{
		Code: "FLAT5USD", Name: "$5 Off", Type: domain.CouponTypeFixedAmount,
		AmountOff: 500, Currency: "USD",
	})
	svc := NewService(store)

	_, err := svc.Redeem(context.Background(), "t1", RedeemInput{
		Code: "FLAT5USD", CustomerID: "cust_1", SubtotalCents: 10000, Currency: "EUR",
	})
	assertErrContains(t, err, "does not match")
}

func TestRedeem_FixedAmountCurrencyMatchAccepted(t *testing.T) {
	store := newMockStore()
	store.seedCoupon(domain.Coupon{
		Code: "FLAT5USD", Name: "$5 Off", Type: domain.CouponTypeFixedAmount,
		AmountOff: 500, Currency: "USD",
	})
	svc := NewService(store)

	// Lowercase input vs stored uppercase — EqualFold matches.
	red, err := svc.Redeem(context.Background(), "t1", RedeemInput{
		Code: "FLAT5USD", CustomerID: "cust_1", SubtotalCents: 10000, Currency: "usd",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if red.DiscountCents != 500 {
		t.Errorf("expected discount 500, got %d", red.DiscountCents)
	}
}

func TestRedeem_PercentageIgnoresCurrency(t *testing.T) {
	store := newMockStore()
	store.seedCoupon(domain.Coupon{
		Code: "SAVE10", Name: "10% Off", Type: domain.CouponTypePercentage,
		PercentOffBP: 1000,
	})
	svc := NewService(store)

	// Percentage coupons have no currency; any target currency works.
	red, err := svc.Redeem(context.Background(), "t1", RedeemInput{
		Code: "SAVE10", CustomerID: "cust_1", SubtotalCents: 10000, Currency: "EUR",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if red.DiscountCents != 1000 {
		t.Errorf("expected discount 1000, got %d", red.DiscountCents)
	}
}

// ---------------------------------------------------------------------------
// Redeem — private (customer-scoped) coupon tests
// ---------------------------------------------------------------------------

// A private coupon (CustomerID set) redeems normally for the named customer.
func TestRedeem_PrivateCouponAcceptsTargetCustomer(t *testing.T) {
	store := newMockStore()
	store.seedCoupon(domain.Coupon{
		Code: "ACME-DEAL", Name: "Acme Enterprise", Type: domain.CouponTypePercentage,
		PercentOffBP: 3000, CustomerID: "cust_acme",
	})
	svc := NewService(store)

	red, err := svc.Redeem(context.Background(), "t1", RedeemInput{
		Code: "ACME-DEAL", CustomerID: "cust_acme", SubtotalCents: 10000,
	})
	if err != nil {
		t.Fatalf("target customer should redeem cleanly, got: %v", err)
	}
	if red.DiscountCents != 3000 {
		t.Errorf("expected discount 3000, got %d", red.DiscountCents)
	}
}

// The other-customer path is the core of this feature — a private coupon
// must be unusable by anyone it wasn't issued to. Error shape mirrors
// "coupon not found" on purpose so the endpoint doesn't leak that a code
// exists but isn't yours.
func TestRedeem_PrivateCouponRejectsOtherCustomer(t *testing.T) {
	store := newMockStore()
	store.seedCoupon(domain.Coupon{
		Code: "ACME-DEAL", Name: "Acme Enterprise", Type: domain.CouponTypePercentage,
		PercentOffBP: 3000, CustomerID: "cust_acme",
	})
	svc := NewService(store)

	_, err := svc.Redeem(context.Background(), "t1", RedeemInput{
		Code: "ACME-DEAL", CustomerID: "cust_other", SubtotalCents: 10000,
	})
	assertErrContains(t, err, "coupon not found")
}

// Regression: CustomerID == "" is the public-coupon case and must not
// restrict anyone. A stray "private-by-accident" blocker here would
// silently break every public coupon.
func TestRedeem_PublicCouponAcceptsAnyCustomer(t *testing.T) {
	store := newMockStore()
	store.seedCoupon(domain.Coupon{
		Code: "LAUNCH20", Name: "Launch", Type: domain.CouponTypePercentage,
		PercentOffBP: 2000, // CustomerID intentionally empty
	})
	svc := NewService(store)

	for _, cust := range []string{"cust_1", "cust_2", "cust_3"} {
		_, err := svc.Redeem(context.Background(), "t1", RedeemInput{
			Code: "LAUNCH20", CustomerID: cust, SubtotalCents: 10000,
		})
		if err != nil {
			t.Errorf("public coupon rejected %s: %v", cust, err)
		}
	}
}

// TestRedeemDetail_IdempotencyReplay exercises the contract the HTTP layer
// relies on for setting the Idempotent-Replay response header: a repeated
// RedeemDetail call with the same idempotency key returns Replay=true and
// the original redemption, without bumping the redemption count.
func TestRedeemDetail_IdempotencyReplay(t *testing.T) {
	store := newMockStore()
	store.seedCoupon(domain.Coupon{
		Code: "RETRY10", Name: "10% Off", Type: domain.CouponTypePercentage,
		PercentOffBP: 1000,
	})
	svc := NewService(store)

	in := RedeemInput{
		Code: "RETRY10", CustomerID: "cust_1", SubtotalCents: 10000,
		IdempotencyKey: "k_retry_1",
	}

	first, err := svc.RedeemDetail(context.Background(), "t1", in)
	if err != nil {
		t.Fatalf("first redeem: %v", err)
	}
	if first.Replay {
		t.Error("first call Replay=true, expected false")
	}

	second, err := svc.RedeemDetail(context.Background(), "t1", in)
	if err != nil {
		t.Fatalf("replay redeem: %v", err)
	}
	if !second.Replay {
		t.Error("replay call Replay=false, expected true")
	}
	if second.Redemption.ID != first.Redemption.ID {
		t.Errorf("replay returned redemption %s, want %s",
			second.Redemption.ID, first.Redemption.ID)
	}

	cpn, _ := store.GetByCode(context.Background(), "t1", "RETRY10")
	if cpn.TimesRedeemed != 1 {
		t.Errorf("replay bumped counter: times_redeemed=%d, want 1", cpn.TimesRedeemed)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// assertErrContains checks the error message for a substring. For DomainError
// the effective text is "<field> <message>" — the field now lives in metadata
// rather than being prefixed into the message, but legacy assertions still
// read naturally (e.g. "code must be").
func assertErrContains(t *testing.T, err error, substr string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error containing %q, got nil", substr)
	}
	msg := err.Error()
	if field := errs.Field(err); field != "" {
		msg = field + " " + msg
	}
	if !contains(msg, substr) {
		t.Errorf("expected error containing %q, got %q", substr, msg)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// ApplyToInvoice — consult active redemptions, recompute against current subtotal
// ---------------------------------------------------------------------------

func TestApplyToInvoice_PercentageCoupon(t *testing.T) {
	store := newMockStore()
	svc := NewService(store)

	store.seedCoupon(domain.Coupon{
		ID:         "cpn_pct",
		Code:       "SAVE10",
		Name:       "10% off",
		Type:       domain.CouponTypePercentage,
		PercentOffBP: 1000,
		
	})
	store.redemptions = append(store.redemptions, domain.CouponRedemption{
		CouponID:       "cpn_pct",
		CustomerID:     "cust_1",
		SubscriptionID: "sub_1",
		DiscountCents:  1000,
	})

	got, err := svc.ApplyToInvoice(context.Background(), "tenant_1", "sub_1", "", "", []string{"plan_1"}, 12345)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 10% of 12345 = 1234.5 → banker's rounding to 1234.
	if got.Cents != 1234 {
		t.Errorf("expected discount 1234, got %d", got.Cents)
	}
}

func TestApplyToInvoice_FixedAmountCoupon(t *testing.T) {
	store := newMockStore()
	svc := NewService(store)

	store.seedCoupon(domain.Coupon{
		ID:        "cpn_fix",
		Code:      "FLAT5",
		Name:      "$5 off",
		Type:      domain.CouponTypeFixedAmount,
		AmountOff: 500,
		Currency:  "USD",
		
	})
	store.redemptions = append(store.redemptions, domain.CouponRedemption{
		CouponID:       "cpn_fix",
		CustomerID:     "cust_1",
		SubscriptionID: "sub_1",
		DiscountCents:  500,
	})

	got, err := svc.ApplyToInvoice(context.Background(), "tenant_1", "sub_1", "", "", []string{"plan_1"}, 10000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Cents != 500 {
		t.Errorf("expected discount 500, got %d", got.Cents)
	}
}

func TestApplyToInvoice_SkipsFixedAmountCouponWithCurrencyMismatch(t *testing.T) {
	// Defensive filter: even if a USD coupon was redeemed against a
	// subscription whose currency later changed to EUR (or the redeem-time
	// currency check was bypassed), the invoice must not apply a mismatched
	// fixed-amount coupon. The discount silently drops to zero and we log.
	store := newMockStore()
	svc := NewService(store)

	store.seedCoupon(domain.Coupon{
		ID:        "cpn_usd",
		Code:      "FLAT5USD",
		Name:      "$5 off",
		Type:      domain.CouponTypeFixedAmount,
		AmountOff: 500,
		Currency:  "USD",
		
	})
	store.redemptions = append(store.redemptions, domain.CouponRedemption{
		CouponID:       "cpn_usd",
		CustomerID:     "cust_1",
		SubscriptionID: "sub_1",
		DiscountCents:  500,
	})

	got, err := svc.ApplyToInvoice(context.Background(), "tenant_1", "sub_1", "", "EUR", []string{"plan_1"}, 10000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Cents != 0 {
		t.Errorf("expected discount 0 (coupon skipped), got %d", got.Cents)
	}
	if len(got.RedemptionIDs) != 0 {
		t.Errorf("expected zero redemption IDs applied, got %d", len(got.RedemptionIDs))
	}
}

func TestApplyToInvoice_AppliesPercentageRegardlessOfInvoiceCurrency(t *testing.T) {
	// Percentage coupons carry no currency, so a non-matching invoice
	// currency must not skip them.
	store := newMockStore()
	svc := NewService(store)

	store.seedCoupon(domain.Coupon{
		ID:         "cpn_pct",
		Code:       "SAVE10",
		Name:       "10% off",
		Type:       domain.CouponTypePercentage,
		PercentOffBP: 1000,
		
	})
	store.redemptions = append(store.redemptions, domain.CouponRedemption{
		CouponID:       "cpn_pct",
		CustomerID:     "cust_1",
		SubscriptionID: "sub_1",
		DiscountCents:  1000,
	})

	got, err := svc.ApplyToInvoice(context.Background(), "tenant_1", "sub_1", "", "EUR", []string{"plan_1"}, 10000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Cents != 1000 {
		t.Errorf("expected discount 1000, got %d", got.Cents)
	}
}

func TestApplyToInvoice_ClampsToSubtotal(t *testing.T) {
	// A $50 fixed coupon applied to a $20 invoice must clamp to $20, not
	// produce a negative total. Critical: otherwise a coupon larger than the
	// invoice would net the customer a credit, which we don't issue this way.
	store := newMockStore()
	svc := NewService(store)

	store.seedCoupon(domain.Coupon{
		ID:        "cpn_big",
		Code:      "BIG50",
		Name:      "$50 off",
		Type:      domain.CouponTypeFixedAmount,
		AmountOff: 5000,
		Currency:  "USD",
		
	})
	store.redemptions = append(store.redemptions, domain.CouponRedemption{
		CouponID:       "cpn_big",
		SubscriptionID: "sub_1",
		DiscountCents:  5000,
	})

	got, err := svc.ApplyToInvoice(context.Background(), "tenant_1", "sub_1", "", "", []string{"plan_1"}, 2000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Cents != 2000 {
		t.Errorf("expected clamp to 2000, got %d", got.Cents)
	}
}

func TestApplyToInvoice_ExpiredCouponIgnored(t *testing.T) {
	store := newMockStore()
	svc := NewService(store)

	past := time.Now().Add(-1 * time.Hour)
	store.seedCoupon(domain.Coupon{
		ID:         "cpn_old",
		Code:       "OLDCODE",
		Name:       "10% off",
		Type:       domain.CouponTypePercentage,
		PercentOffBP: 1000,
		
		ExpiresAt:  &past,
	})
	store.redemptions = append(store.redemptions, domain.CouponRedemption{
		CouponID:       "cpn_old",
		SubscriptionID: "sub_1",
		DiscountCents:  1000,
	})

	got, err := svc.ApplyToInvoice(context.Background(), "tenant_1", "sub_1", "", "", []string{"plan_1"}, 10000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Cents != 0 {
		t.Errorf("expected 0 discount for expired coupon, got %d", got.Cents)
	}
}

func TestApplyToInvoice_InactiveCouponIgnored(t *testing.T) {
	store := newMockStore()
	svc := NewService(store)

	store.seedCoupon(domain.Coupon{
		ID:         "cpn_off",
		Code:       "OFFCODE",
		Name:       "10% off",
		Type:       domain.CouponTypePercentage,
		PercentOffBP: 1000,
		ArchivedAt: &pastTime,
	})
	store.redemptions = append(store.redemptions, domain.CouponRedemption{
		CouponID:       "cpn_off",
		SubscriptionID: "sub_1",
		DiscountCents:  1000,
	})

	got, err := svc.ApplyToInvoice(context.Background(), "tenant_1", "sub_1", "", "", []string{"plan_1"}, 10000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Cents != 0 {
		t.Errorf("expected 0 discount for inactive coupon, got %d", got.Cents)
	}
}

func TestApplyToInvoice_PlanRestriction(t *testing.T) {
	store := newMockStore()
	svc := NewService(store)

	store.seedCoupon(domain.Coupon{
		ID:         "cpn_planA",
		Code:       "PLANA10",
		Name:       "10% off plan A",
		Type:       domain.CouponTypePercentage,
		PercentOffBP: 1000,
		PlanIDs:    []string{"plan_A"},
		
	})
	store.redemptions = append(store.redemptions, domain.CouponRedemption{
		CouponID:       "cpn_planA",
		SubscriptionID: "sub_1",
		DiscountCents:  1000,
	})

	// Subscription is on plan_B — restriction blocks.
	got, err := svc.ApplyToInvoice(context.Background(), "tenant_1", "sub_1", "", "", []string{"plan_B"}, 10000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Cents != 0 {
		t.Errorf("plan_B should not match restricted coupon, got %d", got.Cents)
	}

	// Subscription is on plan_A — restriction passes.
	got, err = svc.ApplyToInvoice(context.Background(), "tenant_1", "sub_1", "", "", []string{"plan_A"}, 10000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Cents != 1000 {
		t.Errorf("plan_A should match restricted coupon, got %d", got.Cents)
	}
}

func TestApplyToInvoice_NoRedemptions(t *testing.T) {
	store := newMockStore()
	svc := NewService(store)

	got, err := svc.ApplyToInvoice(context.Background(), "tenant_1", "sub_1", "", "", []string{"plan_1"}, 10000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Cents != 0 {
		t.Errorf("expected 0 with no redemptions, got %d", got.Cents)
	}
}

func TestApplyToInvoice_EmptySubscriptionID(t *testing.T) {
	store := newMockStore()
	svc := NewService(store)

	got, err := svc.ApplyToInvoice(context.Background(), "tenant_1", "", "", "", []string{"plan_1"}, 10000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Cents != 0 {
		t.Errorf("expected 0 with empty subscription ID, got %d", got.Cents)
	}
}

// Defence-in-depth: even if a redemption row exists, a coupon privately scoped
// to a different customer must not apply to this invoice. Skips the discount
// silently — logged as a warning inside the service.
func TestApplyToInvoice_PrivateCouponCustomerMismatch_Skips(t *testing.T) {
	store := newMockStore()
	svc := NewService(store)

	store.seedCoupon(domain.Coupon{
		ID:           "cpn_priv",
		Code:         "FRIEND",
		Name:         "20% off",
		Type:         domain.CouponTypePercentage,
		PercentOffBP: 2000,
		CustomerID:   "cus_alice",
	})
	store.redemptions = append(store.redemptions, domain.CouponRedemption{
		CouponID:       "cpn_priv",
		SubscriptionID: "sub_1",
		CustomerID:     "cus_alice",
		DiscountCents:  2000,
	})

	// Invoice belongs to cus_bob — the private coupon must not apply.
	got, err := svc.ApplyToInvoice(context.Background(), "tenant_1", "sub_1", "cus_bob", "", []string{"plan_1"}, 10000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Cents != 0 {
		t.Errorf("expected 0 (private coupon not owned by invoice customer), got %d", got.Cents)
	}
}

// The matching customer path still works — regression guard against the
// private-coupon skip becoming too aggressive.
func TestApplyToInvoice_PrivateCouponMatchingCustomer_Applies(t *testing.T) {
	store := newMockStore()
	svc := NewService(store)

	store.seedCoupon(domain.Coupon{
		ID:           "cpn_priv",
		Code:         "FRIEND",
		Name:         "20% off",
		Type:         domain.CouponTypePercentage,
		PercentOffBP: 2000,
		CustomerID:   "cus_alice",
	})
	store.redemptions = append(store.redemptions, domain.CouponRedemption{
		CouponID:       "cpn_priv",
		SubscriptionID: "sub_1",
		CustomerID:     "cus_alice",
		DiscountCents:  2000,
	})

	got, err := svc.ApplyToInvoice(context.Background(), "tenant_1", "sub_1", "cus_alice", "", []string{"plan_1"}, 10000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Cents != 2000 {
		t.Errorf("expected 2000 (20%% of 10000), got %d", got.Cents)
	}
}

// A public coupon (empty CustomerID) with a redemption stamped for a
// different customer should still be skipped — the redemption-side check
// catches the case where a redemption leaked across customers on the same
// subscription.
func TestApplyToInvoice_RedemptionCustomerMismatch_Skips(t *testing.T) {
	store := newMockStore()
	svc := NewService(store)

	store.seedCoupon(domain.Coupon{
		ID:           "cpn_pub",
		Code:         "SAVE10",
		Name:         "10% off",
		Type:         domain.CouponTypePercentage,
		PercentOffBP: 1000,
	})
	store.redemptions = append(store.redemptions, domain.CouponRedemption{
		CouponID:       "cpn_pub",
		SubscriptionID: "sub_1",
		CustomerID:     "cus_old",
		DiscountCents:  1000,
	})

	got, err := svc.ApplyToInvoice(context.Background(), "tenant_1", "sub_1", "cus_new", "", []string{"plan_1"}, 10000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Cents != 0 {
		t.Errorf("expected 0 (redemption stamped for different customer), got %d", got.Cents)
	}
}

func TestApplyToInvoice_MultipleCouponsTakesLargest(t *testing.T) {
	// Only one coupon wins per invoice (Stripe's model). We pick the largest
	// discount so the customer gets the best available price — inverting this
	// would punish users who stacked multiple promos.
	store := newMockStore()
	svc := NewService(store)

	store.seedCoupon(domain.Coupon{
		ID:         "cpn_small",
		Code:       "SMALL5",
		Name:       "5% off",
		Type:       domain.CouponTypePercentage,
		PercentOffBP: 500,
		
	})
	store.seedCoupon(domain.Coupon{
		ID:         "cpn_big",
		Code:       "BIG20",
		Name:       "20% off",
		Type:       domain.CouponTypePercentage,
		PercentOffBP: 2000,
		
	})
	store.redemptions = append(store.redemptions,
		domain.CouponRedemption{CouponID: "cpn_small", SubscriptionID: "sub_1"},
		domain.CouponRedemption{CouponID: "cpn_big", SubscriptionID: "sub_1"},
	)

	got, err := svc.ApplyToInvoice(context.Background(), "tenant_1", "sub_1", "", "", []string{"plan_1"}, 10000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Cents != 2000 {
		t.Errorf("expected largest discount 2000, got %d", got.Cents)
	}
}

// ---------------------------------------------------------------------------
// FEAT-6: duration semantics — once / repeating / forever
// ---------------------------------------------------------------------------

func TestApplyToInvoice_DurationOnce_ExhaustsAfterFirst(t *testing.T) {
	// once = apply to exactly one invoice. Before MarkPeriodsApplied runs it
	// still appears as eligible; after one cycle it filters out.
	store := newMockStore()
	svc := NewService(store)

	store.seedCoupon(domain.Coupon{
		ID: "cpn_once", Code: "ONCE10", Name: "10% once",
		Type: domain.CouponTypePercentage, PercentOffBP: 1000,
		Duration: domain.CouponDurationOnce,
	})
	redID := store.seedRedemption(domain.CouponRedemption{
		CouponID: "cpn_once", SubscriptionID: "sub_1",
	})

	got, err := svc.ApplyToInvoice(context.Background(), "t1", "sub_1", "", "", []string{"plan_1"}, 10000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Cents != 1000 {
		t.Fatalf("cycle 1: expected discount 1000, got %d", got.Cents)
	}
	if len(got.RedemptionIDs) != 1 || got.RedemptionIDs[0] != redID {
		t.Fatalf("cycle 1: expected redemption id %q, got %v", redID, got.RedemptionIDs)
	}

	if err := svc.MarkPeriodsApplied(context.Background(), "t1", got.RedemptionIDs); err != nil {
		t.Fatalf("MarkPeriodsApplied: %v", err)
	}

	got, err = svc.ApplyToInvoice(context.Background(), "t1", "sub_1", "", "", []string{"plan_1"}, 10000)
	if err != nil {
		t.Fatalf("unexpected error on cycle 2: %v", err)
	}
	if got.Cents != 0 {
		t.Errorf("cycle 2: expected 0 after 'once' exhausted, got %d", got.Cents)
	}
}

func TestApplyToInvoice_DurationRepeating_ExhaustsAfterNPeriods(t *testing.T) {
	// repeating with duration_periods=3 applies for invoices 1..3; invoice 4
	// sees an empty DiscountResult because periods_applied has caught up.
	store := newMockStore()
	svc := NewService(store)

	three := 3
	store.seedCoupon(domain.Coupon{
		ID: "cpn_rep", Code: "REP10", Name: "10% for 3 months",
		Type: domain.CouponTypePercentage, PercentOffBP: 1000,
		Duration: domain.CouponDurationRepeating, DurationPeriods: &three,
		
	})
	store.seedRedemption(domain.CouponRedemption{
		CouponID: "cpn_rep", SubscriptionID: "sub_1",
	})

	for cycle := 1; cycle <= 3; cycle++ {
		got, err := svc.ApplyToInvoice(context.Background(), "t1", "sub_1", "", "", []string{"plan_1"}, 10000)
		if err != nil {
			t.Fatalf("cycle %d unexpected error: %v", cycle, err)
		}
		if got.Cents != 1000 {
			t.Errorf("cycle %d: expected 1000, got %d", cycle, got.Cents)
		}
		if err := svc.MarkPeriodsApplied(context.Background(), "t1", got.RedemptionIDs); err != nil {
			t.Fatalf("cycle %d MarkPeriodsApplied: %v", cycle, err)
		}
	}

	got, err := svc.ApplyToInvoice(context.Background(), "t1", "sub_1", "", "", []string{"plan_1"}, 10000)
	if err != nil {
		t.Fatalf("cycle 4 unexpected error: %v", err)
	}
	if got.Cents != 0 {
		t.Errorf("cycle 4: expected 0 after 3 periods exhausted, got %d", got.Cents)
	}
	if len(got.RedemptionIDs) != 0 {
		t.Errorf("cycle 4: expected no redemption IDs, got %v", got.RedemptionIDs)
	}
}

func TestApplyToInvoice_DurationForever_NeverExhausts(t *testing.T) {
	// forever keeps applying regardless of periods_applied count.
	store := newMockStore()
	svc := NewService(store)

	store.seedCoupon(domain.Coupon{
		ID: "cpn_forever", Code: "FOREVER10", Name: "10% forever",
		Type: domain.CouponTypePercentage, PercentOffBP: 1000,
		Duration: domain.CouponDurationForever,
	})
	redID := store.seedRedemption(domain.CouponRedemption{
		CouponID: "cpn_forever", SubscriptionID: "sub_1",
		PeriodsApplied: 99, // already applied many times
	})
	_ = redID

	got, err := svc.ApplyToInvoice(context.Background(), "t1", "sub_1", "", "", []string{"plan_1"}, 10000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Cents != 1000 {
		t.Errorf("forever should still apply at periods_applied=99, got %d", got.Cents)
	}
}

// ---------------------------------------------------------------------------
// FEAT-6: stacking
// ---------------------------------------------------------------------------

func TestApplyToInvoice_StackablePercentAndFixed(t *testing.T) {
	// Two stackable coupons: 10% + $5. On a $100 invoice that's $10 + $5 = $15.
	store := newMockStore()
	svc := NewService(store)

	store.seedCoupon(domain.Coupon{
		ID: "cpn_pct", Code: "PCT10", Type: domain.CouponTypePercentage,
		PercentOffBP: 1000, Duration: domain.CouponDurationForever,
		Stackable: true,
	})
	store.seedCoupon(domain.Coupon{
		ID: "cpn_fix", Code: "FIX500", Type: domain.CouponTypeFixedAmount,
		AmountOff: 500, Currency: "USD", Duration: domain.CouponDurationForever,
		Stackable: true,
	})
	id1 := store.seedRedemption(domain.CouponRedemption{CouponID: "cpn_pct", SubscriptionID: "sub_1"})
	id2 := store.seedRedemption(domain.CouponRedemption{CouponID: "cpn_fix", SubscriptionID: "sub_1"})

	got, err := svc.ApplyToInvoice(context.Background(), "t1", "sub_1", "", "", []string{"plan_1"}, 10000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Cents != 1500 {
		t.Errorf("expected stacked discount 1500 (1000 + 500), got %d", got.Cents)
	}
	if len(got.RedemptionIDs) != 2 {
		t.Fatalf("expected both redemptions attributed, got %v", got.RedemptionIDs)
	}
	// Order: stackable pool iterates in seed order → pct first, fixed second.
	if got.RedemptionIDs[0] != id1 || got.RedemptionIDs[1] != id2 {
		t.Errorf("expected [%s, %s], got %v", id1, id2, got.RedemptionIDs)
	}
}

func TestApplyToInvoice_StackablePercentCappedAt100(t *testing.T) {
	// Two 60% stackable coupons → 120% naive, capped at 100% = $100 on $100.
	store := newMockStore()
	svc := NewService(store)

	store.seedCoupon(domain.Coupon{
		ID: "cpn_a", Code: "HALF_A", Type: domain.CouponTypePercentage,
		PercentOffBP: 6000, Duration: domain.CouponDurationForever,
		Stackable: true,
	})
	store.seedCoupon(domain.Coupon{
		ID: "cpn_b", Code: "HALF_B", Type: domain.CouponTypePercentage,
		PercentOffBP: 6000, Duration: domain.CouponDurationForever,
		Stackable: true,
	})
	store.seedRedemption(domain.CouponRedemption{CouponID: "cpn_a", SubscriptionID: "sub_1"})
	store.seedRedemption(domain.CouponRedemption{CouponID: "cpn_b", SubscriptionID: "sub_1"})

	got, err := svc.ApplyToInvoice(context.Background(), "t1", "sub_1", "", "", []string{"plan_1"}, 10000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Cents != 10000 {
		t.Errorf("expected capped 10000, got %d", got.Cents)
	}
}

func TestApplyToInvoice_StackableClampedToSubtotal(t *testing.T) {
	// $50 fixed + $80 fixed = $130 combined, clamped to a $100 subtotal.
	store := newMockStore()
	svc := NewService(store)

	store.seedCoupon(domain.Coupon{
		ID: "cpn_50", Code: "FIX50", Type: domain.CouponTypeFixedAmount,
		AmountOff: 5000, Currency: "USD", Duration: domain.CouponDurationForever,
		Stackable: true,
	})
	store.seedCoupon(domain.Coupon{
		ID: "cpn_80", Code: "FIX80", Type: domain.CouponTypeFixedAmount,
		AmountOff: 8000, Currency: "USD", Duration: domain.CouponDurationForever,
		Stackable: true,
	})
	store.seedRedemption(domain.CouponRedemption{CouponID: "cpn_50", SubscriptionID: "sub_1"})
	store.seedRedemption(domain.CouponRedemption{CouponID: "cpn_80", SubscriptionID: "sub_1"})

	got, err := svc.ApplyToInvoice(context.Background(), "t1", "sub_1", "", "", []string{"plan_1"}, 10000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Cents != 10000 {
		t.Errorf("expected clamp to 10000, got %d", got.Cents)
	}
}

func TestApplyToInvoice_NonStackableOverridesStackable(t *testing.T) {
	// If any coupon is non-stackable, the combined-stack policy is skipped and
	// we fall back to "best single wins" — documented FEAT-6 rule.
	// Layout: 5% stackable + 20% non-stackable + $3 stackable. Non-stackable
	// forces single-coupon mode; the 20% wins ($2000 on $10000) despite the
	// stackable combination (5% + $3 = $800) being available if we ignored it.
	store := newMockStore()
	svc := NewService(store)

	store.seedCoupon(domain.Coupon{
		ID: "cpn_5p_s", Code: "SMALL_S", Type: domain.CouponTypePercentage,
		PercentOffBP: 500, Duration: domain.CouponDurationForever,
		Stackable: true,
	})
	store.seedCoupon(domain.Coupon{
		ID: "cpn_20p_ns", Code: "BIG_NS", Type: domain.CouponTypePercentage,
		PercentOffBP: 2000, Duration: domain.CouponDurationForever,
		Stackable: false,
	})
	store.seedCoupon(domain.Coupon{
		ID: "cpn_3f_s", Code: "FIX3_S", Type: domain.CouponTypeFixedAmount,
		AmountOff: 300, Currency: "USD", Duration: domain.CouponDurationForever,
		Stackable: true,
	})
	store.seedRedemption(domain.CouponRedemption{CouponID: "cpn_5p_s", SubscriptionID: "sub_1"})
	bigID := store.seedRedemption(domain.CouponRedemption{CouponID: "cpn_20p_ns", SubscriptionID: "sub_1"})
	store.seedRedemption(domain.CouponRedemption{CouponID: "cpn_3f_s", SubscriptionID: "sub_1"})

	got, err := svc.ApplyToInvoice(context.Background(), "t1", "sub_1", "", "", []string{"plan_1"}, 10000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Cents != 2000 {
		t.Errorf("expected single best (2000) when non-stackable present, got %d", got.Cents)
	}
	if len(got.RedemptionIDs) != 1 || got.RedemptionIDs[0] != bigID {
		t.Errorf("expected only bigID %q attributed, got %v", bigID, got.RedemptionIDs)
	}
}

// ---------------------------------------------------------------------------
// FEAT-6: Create-time validation for duration + stackable
// ---------------------------------------------------------------------------

func TestCreate_DurationDefaultsToForever(t *testing.T) {
	store := newMockStore()
	svc := NewService(store)

	got, err := svc.Create(context.Background(), "t1", CreateInput{
		Code: "SAVE10", Name: "10%", Type: domain.CouponTypePercentage, PercentOffBP: 1000,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Duration != domain.CouponDurationForever {
		t.Errorf("expected default duration=forever, got %q", got.Duration)
	}
}

func TestCreate_RepeatingRequiresPositivePeriods(t *testing.T) {
	svc := NewService(newMockStore())
	_, err := svc.Create(context.Background(), "t1", CreateInput{
		Code: "REP10", Name: "10%", Type: domain.CouponTypePercentage,
		PercentOffBP: 1000, Duration: domain.CouponDurationRepeating,
	})
	assertErrContains(t, err, "duration_periods")

	zero := 0
	_, err = svc.Create(context.Background(), "t1", CreateInput{
		Code: "REP10B", Name: "10%", Type: domain.CouponTypePercentage,
		PercentOffBP: 1000, Duration: domain.CouponDurationRepeating, DurationPeriods: &zero,
	})
	assertErrContains(t, err, "duration_periods")
}

func TestCreate_OnceAndForeverRejectDurationPeriods(t *testing.T) {
	// once/forever coupons have no meaningful period count — reject so the
	// on-disk row can't disagree with its own duration label.
	svc := NewService(newMockStore())
	n := 3

	_, err := svc.Create(context.Background(), "t1", CreateInput{
		Code: "ONCE10", Name: "10%", Type: domain.CouponTypePercentage,
		PercentOffBP: 1000, Duration: domain.CouponDurationOnce, DurationPeriods: &n,
	})
	assertErrContains(t, err, "duration_periods")

	_, err = svc.Create(context.Background(), "t1", CreateInput{
		Code: "FOR10", Name: "10%", Type: domain.CouponTypePercentage,
		PercentOffBP: 1000, Duration: domain.CouponDurationForever, DurationPeriods: &n,
	})
	assertErrContains(t, err, "duration_periods")
}

func TestCreate_InvalidDurationRejected(t *testing.T) {
	svc := NewService(newMockStore())
	_, err := svc.Create(context.Background(), "t1", CreateInput{
		Code: "TEST10", Name: "10%", Type: domain.CouponTypePercentage,
		PercentOffBP: 1000, Duration: "bogus",
	})
	assertErrContains(t, err, "duration")
}

func TestMarkPeriodsApplied_NoopOnEmpty(t *testing.T) {
	svc := NewService(newMockStore())
	if err := svc.MarkPeriodsApplied(context.Background(), "t1", nil); err != nil {
		t.Errorf("nil slice: %v", err)
	}
	if err := svc.MarkPeriodsApplied(context.Background(), "t1", []string{}); err != nil {
		t.Errorf("empty slice: %v", err)
	}
	// Empty-string IDs in the slice are skipped — they represent "no
	// redemption to increment" (defensive guard, see service.go).
	if err := svc.MarkPeriodsApplied(context.Background(), "t1", []string{""}); err != nil {
		t.Errorf("empty-string id: %v", err)
	}
}
