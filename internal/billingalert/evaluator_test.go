package billingalert

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// TestDimensionsMatch covers the strict-superset semantics: an alert's
// dimension filter is a subset selector — every (k,v) in the filter must
// be present and equal in the rule's dimension_match. Empty filter
// matches every actual map.
func TestDimensionsMatch(t *testing.T) {
	cases := []struct {
		name   string
		filter map[string]any
		actual map[string]any
		want   bool
	}{
		{
			name:   "empty_filter_matches_anything",
			filter: nil,
			actual: map[string]any{"model": "gpt-4"},
			want:   true,
		},
		{
			name:   "empty_filter_matches_empty_actual",
			filter: map[string]any{},
			actual: map[string]any{},
			want:   true,
		},
		{
			name:   "exact_match",
			filter: map[string]any{"model": "gpt-4"},
			actual: map[string]any{"model": "gpt-4"},
			want:   true,
		},
		{
			name:   "subset_match_filter_in_actual_with_more_keys",
			filter: map[string]any{"model": "gpt-4"},
			actual: map[string]any{"model": "gpt-4", "operation": "input"},
			want:   true,
		},
		{
			name:   "filter_not_in_actual_missing_key",
			filter: map[string]any{"model": "gpt-4"},
			actual: map[string]any{"operation": "input"},
			want:   false,
		},
		{
			name:   "filter_value_differs",
			filter: map[string]any{"model": "gpt-4"},
			actual: map[string]any{"model": "gpt-3.5"},
			want:   false,
		},
		{
			name:   "multi_key_match",
			filter: map[string]any{"model": "gpt-4", "operation": "input"},
			actual: map[string]any{"model": "gpt-4", "operation": "input", "cached": false},
			want:   true,
		},
		{
			name:   "multi_key_one_differs",
			filter: map[string]any{"model": "gpt-4", "operation": "input"},
			actual: map[string]any{"model": "gpt-4", "operation": "output"},
			want:   false,
		},
		{
			name:   "int_vs_float64_normalised",
			filter: map[string]any{"count": 5},
			actual: map[string]any{"count": float64(5)},
			want:   true,
		},
		{
			name:   "int64_vs_float64_normalised",
			filter: map[string]any{"count": int64(42)},
			actual: map[string]any{"count": float64(42)},
			want:   true,
		},
		{
			name:   "bool_match",
			filter: map[string]any{"cached": true},
			actual: map[string]any{"cached": true},
			want:   true,
		},
		{
			name:   "bool_mismatch",
			filter: map[string]any{"cached": true},
			actual: map[string]any{"cached": false},
			want:   false,
		},
		{
			name:   "string_vs_number_no_coerce",
			filter: map[string]any{"id": "5"},
			actual: map[string]any{"id": 5},
			want:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := dimensionsMatch(tc.filter, tc.actual)
			if got != tc.want {
				t.Errorf("dimensionsMatch(%v, %v) = %v, want %v", tc.filter, tc.actual, got, tc.want)
			}
		})
	}
}

// TestShouldFire pins the threshold-comparison semantics: exactly one of
// AmountCentsGTE / QuantityGTE is set; the comparison is strict ≥ on the
// populated side; the unset side must not influence the verdict.
func TestShouldFire(t *testing.T) {
	amount := int64(50000)
	qty := decimal.RequireFromString("100.5")

	cases := []struct {
		name      string
		threshold domain.BillingAlertThreshold
		obsAmount int64
		obsQty    decimal.Decimal
		want      bool
	}{
		{
			name:      "amount_above",
			threshold: domain.BillingAlertThreshold{AmountCentsGTE: &amount},
			obsAmount: 60000,
			want:      true,
		},
		{
			name:      "amount_exactly_at",
			threshold: domain.BillingAlertThreshold{AmountCentsGTE: &amount},
			obsAmount: 50000,
			want:      true,
		},
		{
			name:      "amount_below",
			threshold: domain.BillingAlertThreshold{AmountCentsGTE: &amount},
			obsAmount: 49999,
			want:      false,
		},
		{
			name:      "quantity_above",
			threshold: domain.BillingAlertThreshold{QuantityGTE: &qty},
			obsQty:    decimal.RequireFromString("101"),
			want:      true,
		},
		{
			name:      "quantity_exactly_at",
			threshold: domain.BillingAlertThreshold{QuantityGTE: &qty},
			obsQty:    decimal.RequireFromString("100.5"),
			want:      true,
		},
		{
			name:      "quantity_below",
			threshold: domain.BillingAlertThreshold{QuantityGTE: &qty},
			obsQty:    decimal.RequireFromString("100.49999999"),
			want:      false,
		},
		{
			name:      "no_threshold_set_never_fires",
			threshold: domain.BillingAlertThreshold{},
			obsAmount: 1000000,
			obsQty:    decimal.RequireFromString("999999"),
			want:      false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldFire(tc.threshold, tc.obsAmount, tc.obsQty)
			if got != tc.want {
				t.Errorf("shouldFire(%v, %d, %s) = %v, want %v",
					tc.threshold, tc.obsAmount, tc.obsQty.String(), got, tc.want)
			}
		})
	}
}

// TestPickPrimarySubscription mirrors the create_preview / customer-usage
// heuristic: filter to active|trialing with a current cycle, pick most
// recent CurrentBillingPeriodStart. Subs without a current cycle (paused,
// canceled, draft) are excluded — they have no cycle to evaluate against.
func TestPickPrimarySubscription(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	t1End := t1.AddDate(0, 1, 0)
	t2End := t2.AddDate(0, 1, 0)

	t.Run("no_subs_returns_false", func(t *testing.T) {
		_, ok := pickPrimarySubscription(nil)
		if ok {
			t.Error("expected ok=false for nil input")
		}
	})

	t.Run("only_canceled_returns_false", func(t *testing.T) {
		subs := []domain.Subscription{
			{ID: "vlx_sub_1", Status: domain.SubscriptionCanceled, CurrentBillingPeriodStart: &t1, CurrentBillingPeriodEnd: &t1End},
		}
		_, ok := pickPrimarySubscription(subs)
		if ok {
			t.Error("expected ok=false when only canceled subs")
		}
	})

	t.Run("active_without_cycle_excluded", func(t *testing.T) {
		subs := []domain.Subscription{
			{ID: "vlx_sub_1", Status: domain.SubscriptionActive}, // no current period
		}
		_, ok := pickPrimarySubscription(subs)
		if ok {
			t.Error("expected ok=false when active sub lacks a cycle")
		}
	})

	t.Run("picks_latest_active", func(t *testing.T) {
		subs := []domain.Subscription{
			{ID: "vlx_sub_old", Status: domain.SubscriptionActive, CurrentBillingPeriodStart: &t1, CurrentBillingPeriodEnd: &t1End},
			{ID: "vlx_sub_new", Status: domain.SubscriptionActive, CurrentBillingPeriodStart: &t2, CurrentBillingPeriodEnd: &t2End},
		}
		got, ok := pickPrimarySubscription(subs)
		if !ok || got.ID != "vlx_sub_new" {
			t.Errorf("expected vlx_sub_new, got %q (ok=%v)", got.ID, ok)
		}
	})

	t.Run("trialing_treated_as_active", func(t *testing.T) {
		subs := []domain.Subscription{
			{ID: "vlx_sub_canceled", Status: domain.SubscriptionCanceled, CurrentBillingPeriodStart: &t2, CurrentBillingPeriodEnd: &t2End},
			{ID: "vlx_sub_trial", Status: domain.SubscriptionTrialing, CurrentBillingPeriodStart: &t1, CurrentBillingPeriodEnd: &t1End},
		}
		got, ok := pickPrimarySubscription(subs)
		if !ok || got.ID != "vlx_sub_trial" {
			t.Errorf("expected vlx_sub_trial, got %q (ok=%v)", got.ID, ok)
		}
	})
}

// TestBuildEventPayload pins the snake_case + always-object + decimal-as-
// string contract for the outbound webhook. The wire-shape test covers
// these invariants too; this test additionally pins the exact values the
// dashboard CLI snapshot relies on.
func TestBuildEventPayload(t *testing.T) {
	amount := int64(50000)
	alert := domain.BillingAlert{
		ID:         "vlx_alrt_abc",
		TenantID:   "vlx_tenant",
		CustomerID: "vlx_cus_abc",
		Title:      "Spend > $500",
		Filter: domain.BillingAlertFilter{
			MeterID:    "vlx_mtr_tokens",
			Dimensions: map[string]any{"model": "gpt-4"},
		},
		Threshold:  domain.BillingAlertThreshold{AmountCentsGTE: &amount},
		Recurrence: domain.BillingAlertRecurrencePerPeriod,
	}
	trigger := domain.BillingAlertTrigger{
		AlertID:             alert.ID,
		PeriodFrom:          time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		PeriodTo:            time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		ObservedAmountCents: 60000,
		ObservedQuantity:    decimal.RequireFromString("987.654"),
		Currency:            "USD",
		TriggeredAt:         time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC),
	}

	payload := buildEventPayload(alert, trigger)
	if payload["alert_id"] != "vlx_alrt_abc" {
		t.Errorf("alert_id mismatch: %v", payload["alert_id"])
	}
	if payload["recurrence"] != "per_period" {
		t.Errorf("recurrence mismatch: %v", payload["recurrence"])
	}
	if payload["currency"] != "USD" {
		t.Errorf("currency mismatch: %v", payload["currency"])
	}

	// observed.quantity is a JSON string (decimal precision per ADR-005).
	observed, ok := payload["observed"].(map[string]any)
	if !ok {
		t.Fatalf("observed must be a map, got %T", payload["observed"])
	}
	if qs, isString := observed["quantity"].(string); !isString {
		t.Errorf("observed.quantity must be string, got %T", observed["quantity"])
	} else if qs != "987.654" {
		t.Errorf("observed.quantity = %q, want %q", qs, "987.654")
	}
	if observed["amount_cents"].(int64) != 60000 {
		t.Errorf("observed.amount_cents = %v, want 60000", observed["amount_cents"])
	}

	// threshold always emits both keys.
	threshold, ok := payload["threshold"].(map[string]any)
	if !ok {
		t.Fatalf("threshold must be a map")
	}
	if threshold["usage_gte"] != nil {
		t.Errorf("threshold.usage_gte must be nil when only amount_gte is set")
	}
	if threshold["amount_gte"].(int64) != 50000 {
		t.Errorf("threshold.amount_gte mismatch: %v", threshold["amount_gte"])
	}

	// period.source is the audit string the dashboard renders to explain
	// "where did this window come from".
	period, ok := payload["period"].(map[string]any)
	if !ok {
		t.Fatalf("period must be a map")
	}
	if period["source"] != "current_billing_cycle" {
		t.Errorf("period.source = %v, want current_billing_cycle", period["source"])
	}
}

// TestBuildEventPayload_NilDimensions enforces the always-object idiom
// for filter.dimensions when the alert's filter is nil. Subscriber code
// indexes payload.filter.dimensions without a null guard; the helper
// must normalise to {}.
func TestBuildEventPayload_NilDimensions(t *testing.T) {
	amount := int64(10000)
	alert := domain.BillingAlert{
		ID:         "vlx_alrt_nil_dims",
		TenantID:   "vlx_tenant",
		CustomerID: "vlx_cus",
		Title:      "spend alert",
		Filter:     domain.BillingAlertFilter{Dimensions: nil},
		Threshold:  domain.BillingAlertThreshold{AmountCentsGTE: &amount},
		Recurrence: domain.BillingAlertRecurrenceOneTime,
	}
	trigger := domain.BillingAlertTrigger{
		AlertID:             alert.ID,
		ObservedAmountCents: 12000,
		ObservedQuantity:    decimal.Zero,
		Currency:            "USD",
		TriggeredAt:         time.Now().UTC(),
	}
	payload := buildEventPayload(alert, trigger)
	filter, ok := payload["filter"].(map[string]any)
	if !ok {
		t.Fatalf("filter must be a map")
	}
	dims, ok := filter["dimensions"].(map[string]any)
	if !ok {
		t.Fatalf("filter.dimensions must be a map even when nil, got %T", filter["dimensions"])
	}
	if len(dims) != 0 {
		t.Errorf("filter.dimensions should be empty, got %d keys", len(dims))
	}
}

// TestEqualAny exercises the JSON-int/float normalisation directly so a
// regression in the helper surfaces a focused failure.
func TestEqualAny(t *testing.T) {
	cases := []struct {
		name string
		a, b any
		want bool
	}{
		{"nil_nil", nil, nil, true},
		{"nil_string", nil, "x", false},
		{"int_float64", 5, float64(5), true},
		{"int64_float64", int64(5), float64(5), true},
		{"float64_int", float64(5), 5, true},
		{"float64_int_diff", float64(5), 6, false},
		{"string_eq", "abc", "abc", true},
		{"string_neq", "abc", "abd", false},
		{"bool_eq", true, true, true},
		{"bool_neq", true, false, false},
		{"map_unsupported", map[string]any{"x": 1}, map[string]any{"x": 1}, false}, // helper rejects non-scalars
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := equalAny(tc.a, tc.b); got != tc.want {
				t.Errorf("equalAny(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// TestMapMeterAggregation confirms the meter.Aggregation string → AggregationMode
// map. A drift here would silently route every meter through AggSum and
// quietly break alerts on count/max/last meters.
func TestMapMeterAggregation(t *testing.T) {
	cases := []struct {
		in   string
		want domain.AggregationMode
	}{
		{"sum", domain.AggSum},
		{"count", domain.AggCount},
		{"max", domain.AggMax},
		{"last", domain.AggLastDuringPeriod},
		{"unknown", domain.AggSum}, // sensible default
		{"", domain.AggSum},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := mapMeterAggregation(tc.in); got != tc.want {
				t.Errorf("mapMeterAggregation(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
