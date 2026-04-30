package tax

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestClassify_TableDriven(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want ErrorCode
	}{
		{"nil", nil, ""},
		{"stripe-postal-code-422", errors.New(`stripe tax: {"message":"For addresses with country=US, you must provide postal_code","type":"invalid_request_error","code":"customer_tax_location_invalid","param":"customer_details[address][postal_code]"}`), ErrCodeCustomerDataInvalid},
		{"stripe-missing-country", errors.New(`stripe tax: customer_details.address.country must be set`), ErrCodeCustomerDataInvalid},
		{"jurisdiction-not-supported", errors.New("not supported in this jurisdiction"), ErrCodeJurisdictionUnsupported},
		{"not-registered", errors.New("seller not registered for this region"), ErrCodeJurisdictionUnsupported},
		{"503-service-unavailable", errors.New("stripe: 503 service unavailable"), ErrCodeProviderOutage},
		{"timed-out", errors.New("request timed out"), ErrCodeProviderOutage},
		{"context-deadline", errors.New("context deadline exceeded"), ErrCodeProviderOutage},
		{"invalid-api-key", errors.New("Invalid API Key provided: sk_test_***"), ErrCodeProviderAuth},
		{"unauthorized", errors.New("401 unauthorized"), ErrCodeProviderAuth},
		{"unknown-blob", errors.New("an unexpected error happened"), ErrCodeUnknown},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.err)
			if got != tc.want {
				t.Errorf("Classify(%q) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

func TestClassify_OrderingPostalBeats5xx(t *testing.T) {
	// A 503 response that mentions postal_code should still classify as
	// customer-data — the customer-data check sits above the outage check.
	err := errors.New("503 service unavailable: postal_code missing")
	if got := Classify(err); got != ErrCodeCustomerDataInvalid {
		t.Errorf("got %q, want %q (customer-data must take priority over outage)",
			got, ErrCodeCustomerDataInvalid)
	}
}

func TestClassify_ContextCanceled(t *testing.T) {
	// Real context.Canceled error must classify as outage.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := ctx.Err()
	if got := Classify(err); got != ErrCodeProviderOutage {
		t.Errorf("got %q, want %q for context.Canceled", got, ErrCodeProviderOutage)
	}
}

func TestCleanMessage(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"empty", "", ""},
		{"plain-string", "rate not set", "rate not set"},
		{"stripe-prefix-json", `stripe tax: {"message":"For addresses with country=US, you must provide postal_code"}`, "For addresses with country=US, you must provide postal_code"},
		{"stripe-snake-prefix-json", `stripe_tax: {"message":"jurisdiction unsupported"}`, "jurisdiction unsupported"},
		{"manual-prefix", `manual: rate not configured`, "rate not configured"},
		{"non-json-no-prefix", `connection refused`, "connection refused"},
		{"json-without-message", `{"code":"foo"}`, `{"code":"foo"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CleanMessage(tc.raw)
			if got != tc.want {
				t.Errorf("CleanMessage(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestCleanMessage_TruncatesLongRaw(t *testing.T) {
	raw := strings.Repeat("x", 250)
	got := CleanMessage(raw)
	if len(got) <= 200 {
		t.Errorf("expected truncation to >200 chars with ellipsis, got %d chars", len(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("expected trailing ellipsis, got %q", got[len(got)-10:])
	}
}
