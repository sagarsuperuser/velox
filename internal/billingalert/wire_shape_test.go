package billingalert

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// TestWireShape_SnakeCase pins the JSON contract Track B's billing-alerts
// dashboard depends on — both the four /v1/billing/alerts endpoints and the
// outbound billing.alert.triggered webhook payload. Drift here (e.g.
// dropping a json:"…" tag and falling back to PascalCase, or `dimensions`
// marshaling as null when empty) breaks the frontend at runtime, so we
// marshal a fully-populated alert + webhook payload and assert:
//
//   - Every documented snake_case key is present at the right level.
//   - No PascalCase keys leak through (caught by missing struct tags).
//   - `filter.dimensions` is `{}` not null when empty (always-object idiom).
//   - `threshold` always emits both `amount_gte` and `usage_gte`, with the
//     unset side as JSON null — the dashboard reads both without
//     conditional indexing.
//   - Decimal usage_gte / observed.quantity round-trip as strings (per
//     ADR-005), not JSON numbers that would lose precision.
//
// This is the merge gate for /v1/billing/alerts. See
// docs/design-billing-alerts.md "Wire shape" + "Webhook payload".
func TestWireShape_SnakeCase(t *testing.T) {
	t.Run("AlertResponse_AmountThreshold_FullDimensions", func(t *testing.T) {
		amount := int64(50000) // $500
		triggered := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
		periodStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
		alert := domain.BillingAlert{
			ID:         "vlx_alrt_abc",
			TenantID:   "vlx_tenant_xyz",
			CustomerID: "vlx_cus_abc",
			Title:      "Acme spend > $500",
			Filter: domain.BillingAlertFilter{
				MeterID:    "vlx_mtr_tokens",
				Dimensions: map[string]any{"model": "gpt-4", "operation": "input"},
			},
			Threshold: domain.BillingAlertThreshold{
				AmountCentsGTE: &amount,
			},
			Recurrence:      domain.BillingAlertRecurrencePerPeriod,
			Status:          domain.BillingAlertStatusTriggeredForPeriod,
			LastTriggeredAt: &triggered,
			LastPeriodStart: &periodStart,
			CreatedAt:       time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
			UpdatedAt:       triggered,
		}

		raw := marshalToMap(t, toWireAlert(alert))

		// Top-level snake_case keys.
		topLevel := []string{
			"id",
			"title",
			"customer_id",
			"filter",
			"threshold",
			"recurrence",
			"status",
			"last_triggered_at",
			"last_period_start",
			"created_at",
			"updated_at",
		}
		for _, k := range topLevel {
			if _, ok := raw[k]; !ok {
				t.Errorf("response missing %q (keys=%v)", k, mapKeys(raw))
			}
		}

		// Forbidden PascalCase keys.
		for _, k := range []string{
			"ID", "Title", "CustomerID", "Filter", "Threshold",
			"Recurrence", "Status", "LastTriggeredAt", "LastPeriodStart",
			"CreatedAt", "UpdatedAt",
		} {
			if _, ok := raw[k]; ok {
				t.Errorf("response leaked PascalCase key %q", k)
			}
		}

		// filter.{meter_id, dimensions} both present; dimensions is an object.
		filter, ok := raw["filter"].(map[string]any)
		if !ok {
			t.Fatalf("filter must marshal as JSON object, got %T", raw["filter"])
		}
		for _, k := range []string{"meter_id", "dimensions"} {
			if _, ok := filter[k]; !ok {
				t.Errorf("filter missing %q (keys=%v)", k, mapKeys(filter))
			}
		}
		dims, ok := filter["dimensions"].(map[string]any)
		if !ok {
			t.Fatalf("filter.dimensions must marshal as JSON object, got %T", filter["dimensions"])
		}
		if dims["model"] != "gpt-4" || dims["operation"] != "input" {
			t.Errorf("dimensions content lost: %v", dims)
		}

		// threshold always emits both keys; the unset side is JSON null.
		thresh, ok := raw["threshold"].(map[string]any)
		if !ok {
			t.Fatalf("threshold must marshal as JSON object, got %T", raw["threshold"])
		}
		if _, ok := thresh["amount_gte"]; !ok {
			t.Errorf("threshold missing amount_gte (keys=%v)", mapKeys(thresh))
		}
		if _, ok := thresh["usage_gte"]; !ok {
			t.Errorf("threshold missing usage_gte (keys=%v) — must always emit both keys", mapKeys(thresh))
		}
		// usage_gte should be JSON null when only amount_gte is set.
		if thresh["usage_gte"] != nil {
			t.Errorf("threshold.usage_gte should be null when only amount_gte set, got %v (%T)", thresh["usage_gte"], thresh["usage_gte"])
		}
		// amount_gte unmarshals as float64 (no precision loss for int64 ≤ 2^53).
		if amt, ok := thresh["amount_gte"].(float64); !ok || amt != 50000 {
			t.Errorf("threshold.amount_gte should be 50000, got %v (%T)", thresh["amount_gte"], thresh["amount_gte"])
		}
	})

	t.Run("AlertResponse_UsageThreshold_StringDecimal", func(t *testing.T) {
		// Usage threshold side: usage_gte must round-trip as a JSON
		// string (not number) to preserve fractional precision for
		// AI-usage primitives like cached-token ratios.
		qty := decimal.RequireFromString("1234567.891234")
		alert := domain.BillingAlert{
			ID:         "vlx_alrt_xyz",
			CustomerID: "vlx_cus_abc",
			Title:      "Tokens > 1.2M this cycle",
			Filter: domain.BillingAlertFilter{
				MeterID:    "vlx_mtr_tokens",
				Dimensions: map[string]any{},
			},
			Threshold: domain.BillingAlertThreshold{
				QuantityGTE: &qty,
			},
			Recurrence: domain.BillingAlertRecurrenceOneTime,
			Status:     domain.BillingAlertStatusActive,
			CreatedAt:  time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
			UpdatedAt:  time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		}

		raw := marshalToMap(t, toWireAlert(alert))
		thresh, ok := raw["threshold"].(map[string]any)
		if !ok {
			t.Fatalf("threshold must marshal as JSON object, got %T", raw["threshold"])
		}
		usage, isString := thresh["usage_gte"].(string)
		if !isString {
			t.Fatalf("threshold.usage_gte must marshal as JSON string (decimal precision), got %T", thresh["usage_gte"])
		}
		if usage != "1234567.891234" {
			t.Errorf("usage_gte precision lost: got %q want %q", usage, "1234567.891234")
		}
		// amount_gte should be JSON null when only usage_gte is set.
		if thresh["amount_gte"] != nil {
			t.Errorf("threshold.amount_gte should be null when only usage_gte set, got %v", thresh["amount_gte"])
		}

		// last_triggered_at / last_period_start are nil (alert never fired) —
		// must serialize as JSON null, not omitted.
		if v, ok := raw["last_triggered_at"]; !ok {
			t.Errorf("last_triggered_at should be present (as null) when never fired")
		} else if v != nil {
			t.Errorf("last_triggered_at should be null when never fired, got %v", v)
		}
	})

	t.Run("AlertResponse_EmptyDimensions_MarshalAsObject", func(t *testing.T) {
		// The always-object idiom: dimensions is `{}` not null even when
		// the alert has no dimension filter. Dashboard rendering iterates
		// without a null guard.
		amount := int64(10000)
		alert := domain.BillingAlert{
			ID:         "vlx_alrt_no_dims",
			CustomerID: "vlx_cus_abc",
			Title:      "No-filter spend alert",
			Filter: domain.BillingAlertFilter{
				MeterID:    "",
				Dimensions: nil, // toWireAlert must normalize → {}
			},
			Threshold:  domain.BillingAlertThreshold{AmountCentsGTE: &amount},
			Recurrence: domain.BillingAlertRecurrenceOneTime,
			Status:     domain.BillingAlertStatusActive,
		}

		raw := marshalToMap(t, toWireAlert(alert))
		filter, ok := raw["filter"].(map[string]any)
		if !ok {
			t.Fatalf("filter must marshal as JSON object")
		}
		dims, ok := filter["dimensions"].(map[string]any)
		if !ok {
			t.Fatalf("filter.dimensions must marshal as JSON object even when empty, got %T", filter["dimensions"])
		}
		if len(dims) != 0 {
			t.Errorf("filter.dimensions should be empty in this fixture, got %d keys", len(dims))
		}
		// meter_id is omitempty; missing key is OK.
		if _, ok := filter["meter_id"]; ok {
			t.Errorf("filter.meter_id should be omitted (omitempty) when empty, got %v", filter["meter_id"])
		}
	})

	t.Run("WebhookPayload_SnakeCaseAndDecimalString", func(t *testing.T) {
		// The outbound webhook payload is the persistent contract for
		// every alert subscriber. Track B's CLI sample output and the
		// docs example pin this exact shape.
		amount := int64(50000)
		triggered := time.Date(2026, 4, 26, 12, 5, 30, 123456789, time.UTC)
		alert := domain.BillingAlert{
			ID:         "vlx_alrt_abc",
			TenantID:   "vlx_tenant_xyz",
			CustomerID: "vlx_cus_abc",
			Title:      "Acme spend > $500",
			Filter: domain.BillingAlertFilter{
				MeterID:    "vlx_mtr_tokens",
				Dimensions: map[string]any{"model": "gpt-4"},
			},
			Threshold:  domain.BillingAlertThreshold{AmountCentsGTE: &amount},
			Recurrence: domain.BillingAlertRecurrencePerPeriod,
			Status:     domain.BillingAlertStatusTriggeredForPeriod,
		}
		trigger := domain.BillingAlertTrigger{
			ID:                  "vlx_atrg_001",
			TenantID:            "vlx_tenant_xyz",
			AlertID:             alert.ID,
			PeriodFrom:          time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
			PeriodTo:            time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
			ObservedAmountCents: 60000,
			ObservedQuantity:    decimal.RequireFromString("9876543.210987"),
			Currency:            "USD",
			TriggeredAt:         triggered,
		}

		payload := buildEventPayload(alert, trigger)
		raw := marshalToMap(t, payload)

		// Top-level snake_case keys for the data envelope. The webhook
		// dispatcher wraps this under {id, type, data: <payload>}; the
		// payload itself is what we inspect here.
		topLevel := []string{
			"alert_id", "customer_id", "title", "threshold",
			"observed", "currency", "triggered_at", "period",
			"filter", "recurrence",
		}
		for _, k := range topLevel {
			if _, ok := raw[k]; !ok {
				t.Errorf("payload missing %q (keys=%v)", k, mapKeys(raw))
			}
		}
		// Sanity: the payload uses snake_case throughout — no PascalCase
		// or camelCase leak from struct fields when we move to a typed
		// payload struct in the future.
		for _, k := range []string{"alertId", "customerId", "triggeredAt", "AlertID", "CustomerID"} {
			if _, ok := raw[k]; ok {
				t.Errorf("payload leaked non-snake_case key %q", k)
			}
		}

		// observed.{amount_cents, quantity}; quantity must be a string.
		observed, ok := raw["observed"].(map[string]any)
		if !ok {
			t.Fatalf("observed must marshal as JSON object, got %T", raw["observed"])
		}
		for _, k := range []string{"amount_cents", "quantity"} {
			if _, ok := observed[k]; !ok {
				t.Errorf("observed missing %q (keys=%v)", k, mapKeys(observed))
			}
		}
		qty, isString := observed["quantity"].(string)
		if !isString {
			t.Fatalf("observed.quantity must marshal as JSON string (decimal precision), got %T", observed["quantity"])
		}
		if qty != "9876543.210987" {
			t.Errorf("observed.quantity precision lost: got %q want %q", qty, "9876543.210987")
		}

		// period.{from, to, source}.
		period, ok := raw["period"].(map[string]any)
		if !ok {
			t.Fatalf("period must marshal as JSON object, got %T", raw["period"])
		}
		for _, k := range []string{"from", "to", "source"} {
			if _, ok := period[k]; !ok {
				t.Errorf("period missing %q (keys=%v)", k, mapKeys(period))
			}
		}
		if period["source"] != "current_billing_cycle" {
			t.Errorf("period.source should be 'current_billing_cycle', got %v", period["source"])
		}

		// filter.{meter_id, dimensions} both present; dimensions is an object.
		filter, ok := raw["filter"].(map[string]any)
		if !ok {
			t.Fatalf("filter must marshal as JSON object")
		}
		for _, k := range []string{"meter_id", "dimensions"} {
			if _, ok := filter[k]; !ok {
				t.Errorf("filter missing %q (keys=%v)", k, mapKeys(filter))
			}
		}
		dims, ok := filter["dimensions"].(map[string]any)
		if !ok {
			t.Fatalf("filter.dimensions must marshal as JSON object, got %T", filter["dimensions"])
		}
		if dims["model"] != "gpt-4" {
			t.Errorf("dimensions content lost: %v", dims)
		}

		// threshold always emits both keys; usage_gte must be null here.
		thresh, ok := raw["threshold"].(map[string]any)
		if !ok {
			t.Fatalf("threshold must marshal as JSON object")
		}
		for _, k := range []string{"amount_gte", "usage_gte"} {
			if _, ok := thresh[k]; !ok {
				t.Errorf("threshold missing %q (must always emit both keys)", k)
			}
		}
		if thresh["usage_gte"] != nil {
			t.Errorf("threshold.usage_gte should be null when only amount_gte set, got %v", thresh["usage_gte"])
		}

		// recurrence is a JSON string (not the typed Go enum).
		if _, isString := raw["recurrence"].(string); !isString {
			t.Errorf("recurrence must marshal as JSON string, got %T", raw["recurrence"])
		}
	})

	t.Run("WebhookPayload_EmptyDimensions_MarshalAsObject", func(t *testing.T) {
		// Same always-object guarantee for the webhook side: subscriber
		// code reads payload.filter.dimensions without a null guard.
		amount := int64(10000)
		alert := domain.BillingAlert{
			ID:         "vlx_alrt_no_dims",
			TenantID:   "vlx_tenant_xyz",
			CustomerID: "vlx_cus_abc",
			Title:      "No-dim alert",
			Filter:     domain.BillingAlertFilter{Dimensions: nil},
			Threshold:  domain.BillingAlertThreshold{AmountCentsGTE: &amount},
			Recurrence: domain.BillingAlertRecurrenceOneTime,
		}
		trigger := domain.BillingAlertTrigger{
			AlertID:             alert.ID,
			ObservedAmountCents: 12000,
			ObservedQuantity:    decimal.Zero,
			Currency:            "USD",
			TriggeredAt:         time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC),
		}

		raw := marshalToMap(t, buildEventPayload(alert, trigger))
		filter, ok := raw["filter"].(map[string]any)
		if !ok {
			t.Fatalf("filter must marshal as JSON object")
		}
		dims, ok := filter["dimensions"].(map[string]any)
		if !ok {
			t.Fatalf("filter.dimensions must marshal as JSON object even when empty, got %T", filter["dimensions"])
		}
		if len(dims) != 0 {
			t.Errorf("filter.dimensions should be empty in this fixture, got %d keys", len(dims))
		}
	})
}

// marshalToMap is a small helper that round-trips any value through
// encoding/json into a generic map for key-by-key inspection. Mirrors
// the helper in preview_wire_shape_test.go.
func marshalToMap(t *testing.T, v any) map[string]any {
	t.Helper()
	blob, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(blob, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return raw
}

func mapKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
