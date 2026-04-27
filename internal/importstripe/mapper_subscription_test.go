package importstripe

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stripe/stripe-go/v82"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// loadSubscriptionFixture decodes a Stripe subscription fixture into
// *stripe.Subscription. Mirrors the customer/price helpers.
func loadSubscriptionFixture(t *testing.T, name string) *stripe.Subscription {
	t.Helper()
	path := filepath.Join("testdata", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	var sub stripe.Subscription
	if err := json.Unmarshal(data, &sub); err != nil {
		t.Fatalf("decode fixture %s: %v", path, err)
	}
	return &sub
}

func TestMapSubscription_FullActive(t *testing.T) {
	sub := loadSubscriptionFixture(t, "subscription_full.json")
	got, err := mapSubscription(sub)
	if err != nil {
		t.Fatalf("mapSubscription: %v", err)
	}
	if got.Subscription.Code != "sub_NfJG2N4m6X" {
		t.Errorf("Code = %q, want sub_NfJG2N4m6X", got.Subscription.Code)
	}
	if got.Subscription.Status != domain.SubscriptionActive {
		t.Errorf("Status = %q, want active", got.Subscription.Status)
	}
	if got.Subscription.BillingTime != domain.BillingTimeAnniversary {
		t.Errorf("BillingTime = %q, want anniversary", got.Subscription.BillingTime)
	}
	if got.CustomerExternalID != "cus_NfJG2N4m6X" {
		t.Errorf("CustomerExternalID = %q, want cus_NfJG2N4m6X", got.CustomerExternalID)
	}
	if got.PriceID != "price_flat001" {
		t.Errorf("PriceID = %q, want price_flat001", got.PriceID)
	}
	if got.Quantity != 1 {
		t.Errorf("Quantity = %d, want 1", got.Quantity)
	}
	// current_period_start/end were copied from the item.
	if got.Subscription.CurrentBillingPeriodStart == nil {
		t.Fatal("CurrentBillingPeriodStart is nil")
	}
	if got.Subscription.CurrentBillingPeriodStart.Unix() != 1701000000 {
		t.Errorf("CurrentBillingPeriodStart = %v, want unix 1701000000",
			got.Subscription.CurrentBillingPeriodStart)
	}
	if got.Subscription.CurrentBillingPeriodEnd == nil {
		t.Fatal("CurrentBillingPeriodEnd is nil")
	}
	if got.Subscription.CurrentBillingPeriodEnd.Unix() != 1703678400 {
		t.Errorf("CurrentBillingPeriodEnd = %v, want unix 1703678400",
			got.Subscription.CurrentBillingPeriodEnd)
	}
	// next_billing_at = period end.
	if got.Subscription.NextBillingAt == nil ||
		got.Subscription.NextBillingAt.Unix() != 1703678400 {
		t.Errorf("NextBillingAt = %v, want unix 1703678400 (= period end)",
			got.Subscription.NextBillingAt)
	}
	// Active sub has activated_at set.
	if got.Subscription.ActivatedAt == nil {
		t.Error("ActivatedAt = nil, want set for active sub")
	}
	if got.Subscription.CanceledAt != nil {
		t.Errorf("CanceledAt = %v, want nil for active sub", got.Subscription.CanceledAt)
	}
	if got.Subscription.CancelAtPeriodEnd {
		t.Error("CancelAtPeriodEnd = true, want false")
	}
}

func TestMapSubscription_Trialing(t *testing.T) {
	sub := loadSubscriptionFixture(t, "subscription_trial.json")
	got, err := mapSubscription(sub)
	if err != nil {
		t.Fatalf("mapSubscription: %v", err)
	}
	if got.Subscription.Status != domain.SubscriptionTrialing {
		t.Errorf("Status = %q, want trialing", got.Subscription.Status)
	}
	if got.Subscription.TrialStartAt == nil {
		t.Fatal("TrialStartAt = nil, want set")
	}
	if got.Subscription.TrialStartAt.Unix() != 1701000000 {
		t.Errorf("TrialStartAt = %v, want unix 1701000000", got.Subscription.TrialStartAt)
	}
	if got.Subscription.TrialEndAt == nil {
		t.Fatal("TrialEndAt = nil, want set")
	}
	if got.Subscription.TrialEndAt.Unix() != 1703592000 {
		t.Errorf("TrialEndAt = %v, want unix 1703592000", got.Subscription.TrialEndAt)
	}
	// Trialing subs don't get activated_at populated.
	if got.Subscription.ActivatedAt != nil {
		t.Errorf("ActivatedAt = %v, want nil for trialing", got.Subscription.ActivatedAt)
	}
}

func TestMapSubscription_CancelAtPeriodEnd(t *testing.T) {
	sub := loadSubscriptionFixture(t, "subscription_cancel_at_period_end.json")
	got, err := mapSubscription(sub)
	if err != nil {
		t.Fatalf("mapSubscription: %v", err)
	}
	if !got.Subscription.CancelAtPeriodEnd {
		t.Error("CancelAtPeriodEnd = false, want true")
	}
	// Active sub with future cancel — status remains active.
	if got.Subscription.Status != domain.SubscriptionActive {
		t.Errorf("Status = %q, want active (cancel at period end is a future schedule)",
			got.Subscription.Status)
	}
}

func TestMapSubscription_PastDueRemapsToActive(t *testing.T) {
	sub := loadSubscriptionFixture(t, "subscription_past_due.json")
	got, err := mapSubscription(sub)
	if err != nil {
		t.Fatalf("mapSubscription: %v", err)
	}
	if got.Subscription.Status != domain.SubscriptionActive {
		t.Errorf("Status = %q, want active (past_due remaps)", got.Subscription.Status)
	}
	// The remap must surface in Notes so the CSV makes the divergence visible.
	if !containsAny(got.Notes, "past_due", "active") {
		t.Errorf("Notes missing past_due remap entry; got %v", got.Notes)
	}
}

func TestMapSubscription_CanceledStatus(t *testing.T) {
	sub := loadSubscriptionFixture(t, "subscription_canceled.json")
	got, err := mapSubscription(sub)
	if err != nil {
		t.Fatalf("mapSubscription: %v", err)
	}
	if got.Subscription.Status != domain.SubscriptionCanceled {
		t.Errorf("Status = %q, want canceled", got.Subscription.Status)
	}
	if got.Subscription.CanceledAt == nil {
		t.Fatal("CanceledAt = nil, want set for canceled sub")
	}
	if got.Subscription.CanceledAt.Unix() != 1702000000 {
		t.Errorf("CanceledAt = %v, want unix 1702000000", got.Subscription.CanceledAt)
	}
}

func TestMapSubscription_MultiItemRejected(t *testing.T) {
	sub := loadSubscriptionFixture(t, "subscription_multi_item.json")
	_, err := mapSubscription(sub)
	if !errors.Is(err, ErrSubscriptionUnsupportedMultiItem) {
		t.Errorf("err = %v, want ErrSubscriptionUnsupportedMultiItem", err)
	}
}

func TestMapSubscription_LossyFieldsNoted(t *testing.T) {
	sub := loadSubscriptionFixture(t, "subscription_with_extras.json")
	got, err := mapSubscription(sub)
	if err != nil {
		t.Fatalf("mapSubscription: %v", err)
	}
	// Discounts, schedule, default_tax_rates, pause_collection should all
	// each surface a Note for operator visibility.
	wantSubstrings := []string{"discount", "schedule", "default_tax_rates", "pause_collection"}
	for _, want := range wantSubstrings {
		if !containsAny(got.Notes, want) {
			t.Errorf("Notes missing %q; got %v", want, got.Notes)
		}
	}
}

func TestMapSubscription_EmptyIDIsError(t *testing.T) {
	_, err := mapSubscription(&stripe.Subscription{ID: ""})
	if !errors.Is(err, ErrMapEmptySubscriptionID) {
		t.Errorf("err = %v, want ErrMapEmptySubscriptionID", err)
	}
}

func TestMapSubscription_NilIsError(t *testing.T) {
	_, err := mapSubscription(nil)
	if err == nil {
		t.Fatal("expected error for nil subscription")
	}
}

func TestMapSubscription_MissingCustomer(t *testing.T) {
	sub := &stripe.Subscription{
		ID: "sub_x", Status: stripe.SubscriptionStatusActive,
		Items: &stripe.SubscriptionItemList{
			Data: []*stripe.SubscriptionItem{
				{ID: "si_x", Price: &stripe.Price{ID: "price_x"}, Quantity: 1},
			},
		},
		Livemode: true,
	}
	_, err := mapSubscription(sub)
	if !errors.Is(err, ErrSubscriptionMissingCustomer) {
		t.Errorf("err = %v, want ErrSubscriptionMissingCustomer", err)
	}
}

func TestMapSubscription_UnknownStatusFallsBackToCanceled(t *testing.T) {
	sub := &stripe.Subscription{
		ID:       "sub_unknown_001",
		Status:   stripe.SubscriptionStatus("future_unknown_state"),
		Customer: &stripe.Customer{ID: "cus_x"},
		Items: &stripe.SubscriptionItemList{
			Data: []*stripe.SubscriptionItem{
				{
					ID: "si_x",
					Price: &stripe.Price{
						ID: "price_x", Product: &stripe.Product{ID: "prod_x"},
					},
					Quantity:           1,
					CurrentPeriodStart: 1701000000,
					CurrentPeriodEnd:   1703678400,
				},
			},
		},
		Livemode: true,
		Created:  1701000000,
	}
	got, err := mapSubscription(sub)
	if err != nil {
		t.Fatalf("mapSubscription: %v", err)
	}
	if got.Subscription.Status != domain.SubscriptionCanceled {
		t.Errorf("Status = %q, want canceled (unknown remap)", got.Subscription.Status)
	}
	if !containsAny(got.Notes, "unknown") {
		t.Errorf("Notes missing 'unknown' note; got %v", got.Notes)
	}
}

// containsAny returns true if any of the substrings appears in any of the
// given strings. Tests use it to assert against Notes without pinning the
// exact wording.
func containsAny(haystack []string, needles ...string) bool {
	for _, h := range haystack {
		for _, n := range needles {
			if len(h) >= len(n) && contains(h, n) {
				return true
			}
		}
	}
	return false
}

// contains is a tiny case-sensitive substring check — kept inline so the
// test file doesn't take a strings dep just for one call.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// silence "imported and not used" if time isn't referenced in the future.
var _ = time.Now
