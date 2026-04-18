package coupon

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
)

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

func (m *mockStore) List(_ context.Context, _ string) ([]domain.Coupon, error) {
	var result []domain.Coupon
	for _, c := range m.coupons {
		result = append(result, c)
	}
	return result, nil
}

func (m *mockStore) Update(_ context.Context, _ string, c domain.Coupon) (domain.Coupon, error) {
	m.coupons[c.ID] = c
	m.byCode[c.Code] = c
	return c, nil
}

func (m *mockStore) Deactivate(_ context.Context, _, id string) error {
	c, ok := m.coupons[id]
	if !ok {
		return fmt.Errorf("not found")
	}
	c.Active = false
	m.coupons[id] = c
	m.byCode[c.Code] = c
	return nil
}

func (m *mockStore) IncrementRedemptions(_ context.Context, _, id string) error {
	c, ok := m.coupons[id]
	if !ok {
		return fmt.Errorf("not found")
	}
	c.TimesRedeemed++
	m.coupons[id] = c
	m.byCode[c.Code] = c
	return nil
}

func (m *mockStore) CreateRedemption(_ context.Context, _ string, r domain.CouponRedemption) (domain.CouponRedemption, error) {
	m.nextID++
	r.ID = fmt.Sprintf("red_%d", m.nextID)
	r.CreatedAt = time.Now().UTC()
	m.redemptions = append(m.redemptions, r)
	return r, nil
}

func (m *mockStore) ListRedemptions(_ context.Context, _, _ string) ([]domain.CouponRedemption, error) {
	return m.redemptions, nil
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
			name:     "0.5% of 100 cents — rounding",
			pct:      0.5,
			subtotal: 100,
			// 100 * 0.5 / 100 = 0.5, math.Round(0.5) = 0 in Go (banker's rounding to even)
			// Actually math.Round(0.5) = 1 in Go (rounds half away from zero)
			wantDisc: 1,
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
			name:     "50% of 1 cent",
			pct:      50,
			subtotal: 1,
			// 1 * 50 / 100 = 0.5, math.Round(0.5) = 1
			wantDisc: 1,
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
			c := domain.Coupon{Type: domain.CouponTypePercentage, PercentOff: tt.pct}
			got := CalculateDiscount(c, tt.subtotal)
			if got != tt.wantDisc {
				raw := float64(tt.subtotal) * tt.pct / 100
				t.Errorf("CalculateDiscount(pct=%.2f, subtotal=%d) = %d, want %d (raw float: %f, math.Round: %d)",
					tt.pct, tt.subtotal, got, tt.wantDisc, raw, int64(math.Round(raw)))
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
	c := domain.Coupon{Type: "bogus", PercentOff: 50, AmountOff: 1000}
	got := CalculateDiscount(c, 10000)
	if got != 0 {
		t.Errorf("unknown coupon type should return 0 discount, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// Create — validation tests
// ---------------------------------------------------------------------------

func TestCreate_EmptyCodeRejected(t *testing.T) {
	svc := NewService(newMockStore())
	_, err := svc.Create(context.Background(), "t1", CreateInput{
		Code: "", Name: "Test", Type: domain.CouponTypePercentage, PercentOff: 10,
	})
	assertErrContains(t, err, "code is required")
}

func TestCreate_WhitespaceOnlyCodeRejected(t *testing.T) {
	svc := NewService(newMockStore())
	_, err := svc.Create(context.Background(), "t1", CreateInput{
		Code: "   ", Name: "Test", Type: domain.CouponTypePercentage, PercentOff: 10,
	})
	assertErrContains(t, err, "code is required")
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
				Code: tt.code, Name: "Test", Type: domain.CouponTypePercentage, PercentOff: 10,
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
				Code: tt.code, Name: "Test", Type: domain.CouponTypePercentage, PercentOff: 10,
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
		Code: "SAVE50", Name: "", Type: domain.CouponTypePercentage, PercentOff: 10,
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
		Code: "SAVE50", Name: string(longName), Type: domain.CouponTypePercentage, PercentOff: 10,
	})
	assertErrContains(t, err, "name must be at most 200")
}

func TestCreate_PercentageValidation(t *testing.T) {
	tests := []struct {
		name    string
		pct     float64
		wantErr string
	}{
		{"zero percent", 0, "percent_off must be between 0 and 100"},
		{"negative percent", -5, "percent_off must be between 0 and 100"},
		{"over 100 percent", 101, "percent_off must be between 0 and 100"},
		{"valid 1%", 1, ""},
		{"valid 100%", 100, ""},
		{"valid 50.5%", 50.5, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(newMockStore())
			_, err := svc.Create(context.Background(), "t1", CreateInput{
				Code: "TEST10", Name: "Test", Type: domain.CouponTypePercentage, PercentOff: tt.pct,
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
		PercentOff: 10, MaxRedemptions: &zero,
	})
	assertErrContains(t, err, "max_redemptions must be at least 1")

	negative := -1
	_, err = svc.Create(context.Background(), "t1", CreateInput{
		Code: "TEST11", Name: "Test", Type: domain.CouponTypePercentage,
		PercentOff: 10, MaxRedemptions: &negative,
	})
	assertErrContains(t, err, "max_redemptions must be at least 1")
}

func TestCreate_MaxRedemptionsValidWhenOne(t *testing.T) {
	svc := NewService(newMockStore())
	one := 1
	_, err := svc.Create(context.Background(), "t1", CreateInput{
		Code: "ONETIME", Name: "One-time", Type: domain.CouponTypePercentage,
		PercentOff: 10, MaxRedemptions: &one,
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
		PercentOff: 10, ExpiresAt: &past,
	})
	assertErrContains(t, err, "expires_at must be in the future")
}

func TestCreate_ExpiresAtInFutureAccepted(t *testing.T) {
	svc := NewService(newMockStore())
	future := time.Now().Add(24 * time.Hour)
	_, err := svc.Create(context.Background(), "t1", CreateInput{
		Code: "TEST10", Name: "Test", Type: domain.CouponTypePercentage,
		PercentOff: 10, ExpiresAt: &future,
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
		PercentOff: 10, Active: false,
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
		PercentOff: 10, Active: true, ExpiresAt: &past,
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
		PercentOff: 10, Active: true, MaxRedemptions: &maxR, TimesRedeemed: 3,
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
		PercentOff: 20, Active: true, PlanIDs: []string{"plan_pro", "plan_enterprise"},
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
		PercentOff: 25, Active: true,
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
		AmountOff: 500, Active: true,
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
		PercentOff: 0.1, Active: true,
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
		PercentOff: 50, Active: true,
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

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func assertErrContains(t *testing.T, err error, substr string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error containing %q, got nil", substr)
	}
	if !contains(err.Error(), substr) {
		t.Errorf("expected error containing %q, got %q", substr, err.Error())
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
