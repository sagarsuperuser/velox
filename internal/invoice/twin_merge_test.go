package invoice

import (
	"testing"
)

func stripeFail(pi, errMsg string) timelineEvent {
	return timelineEvent{Source: "stripe", EventType: "payment_intent.payment_failed",
		PaymentIntentID: pi, Error: errMsg, Timestamp: "2026-07-18T10:00:00Z"}
}

func dunningRow(eventType, pi string) timelineEvent {
	return timelineEvent{Source: "dunning", EventType: eventType,
		PaymentIntentID: pi, Timestamp: "2027-03-04T00:00:00Z"}
}

// TestMergeFailedPaymentTwins_PIAttribution locks the audit-finding-4 fix:
// index pairing attributed the k-th Stripe failure to the k-th dunning row,
// so a CUSTOMER's failed hosted-page attempt (same invoice, own PI) stamped
// its decline message onto an engine retry's row. Rows carrying PI ids now
// match exactly; only legacy id-less rows fall back to index pairing.
func TestMergeFailedPaymentTwins_PIAttribution(t *testing.T) {
	t.Run("customer checkout failure does not steal the retry row", func(t *testing.T) {
		events := []timelineEvent{
			dunningRow("dunning_started", ""), // legacy, no PI (initial failure)
			stripeFail("pi_initial", "card_declined initial"),
			stripeFail("pi_customer_checkout", "insufficient_funds (customer attempt)"),
			dunningRow("retry_attempted", "pi_retry_1"),
			stripeFail("pi_retry_1", "card_declined retry"),
		}
		out := mergeFailedPaymentTwins(events)

		var retryRow *timelineEvent
		checkoutSurvives := false
		for i := range out {
			if out[i].Source == "dunning" && out[i].EventType == "retry_attempted" {
				retryRow = &out[i]
			}
			if out[i].Source == "stripe" && out[i].PaymentIntentID == "pi_customer_checkout" {
				checkoutSurvives = true
			}
		}
		if retryRow == nil {
			t.Fatal("retry row missing")
		}
		if retryRow.Error != "card_declined retry" {
			t.Errorf("retry row lifted %q — the customer's checkout failure was mis-attributed onto the engine retry", retryRow.Error)
		}
		if !checkoutSurvives {
			t.Error("the customer's own failure row must survive standalone, not fold into a dunning row")
		}
		// The legacy started-row still index-pairs with the residual pool
		// (pi_initial is the chronologically-first residual failure).
		for _, e := range out {
			if e.Source == "dunning" && e.EventType == "dunning_started" && e.Error != "card_declined initial" {
				t.Errorf("legacy started row should pair with the initial failure, got error=%q", e.Error)
			}
		}
	})

	t.Run("identified-but-absent twin does not steal a positional partner", func(t *testing.T) {
		events := []timelineEvent{
			dunningRow("retry_attempted", "pi_no_webhook_yet"),
			stripeFail("pi_unrelated", "someone else's decline"),
		}
		out := mergeFailedPaymentTwins(events)
		for _, e := range out {
			if e.Source == "dunning" && e.Error != "" {
				t.Errorf("dunning row with an identified absent twin must stay untouched, lifted %q", e.Error)
			}
		}
		found := false
		for _, e := range out {
			if e.Source == "stripe" && e.PaymentIntentID == "pi_unrelated" {
				found = true
			}
		}
		if !found {
			t.Error("the unrelated stripe row must survive")
		}
	})

	t.Run("legacy id-less rows keep the index-pairing behavior", func(t *testing.T) {
		events := []timelineEvent{
			dunningRow("dunning_started", ""),
			dunningRow("retry_attempted", ""),
			stripeFail("pi_a", "first"),
			stripeFail("pi_b", "second"),
		}
		out := mergeFailedPaymentTwins(events)
		if len(out) != 2 {
			t.Fatalf("expected both stripe rows folded (legacy pairing), got %d rows", len(out))
		}
		if out[0].Error != "first" || out[1].Error != "second" {
			t.Errorf("legacy chronological pairing broken: %q / %q", out[0].Error, out[1].Error)
		}
	})
}
