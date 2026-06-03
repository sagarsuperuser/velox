package invoice

import (
	"testing"
	"time"
)

// TestWithinWindow covers the timestamp-window helper used by the
// timeline cancel-vs-void dedup. ADR-020 issue #1 fix.
func TestWithinWindow(t *testing.T) {
	base := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		a, b   time.Time
		window time.Duration
		want   bool
	}{
		{"identical", base, base, 5 * time.Minute, true},
		{"a before b within window", base.Add(-2 * time.Minute), base, 5 * time.Minute, true},
		{"a after b within window", base.Add(2 * time.Minute), base, 5 * time.Minute, true},
		{"a before b outside window", base.Add(-10 * time.Minute), base, 5 * time.Minute, false},
		{"a after b outside window", base.Add(10 * time.Minute), base, 5 * time.Minute, false},
		{"exactly at boundary", base.Add(5 * time.Minute), base, 5 * time.Minute, true},
		{"hour-distant events not co-occurring", base.Add(1 * time.Hour), base, 5 * time.Minute, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := withinWindow(tc.a, tc.b, tc.window); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestDropCanceledForVoid covers the cancel-vs-void suppression decision,
// including the clock-pinned case where voidedAt (simulated) and the Stripe
// event time (wall-clock) are in different time domains.
func TestDropCanceledForVoid(t *testing.T) {
	wall := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	sim := time.Date(2053, 6, 8, 3, 16, 0, 0, time.UTC) // simulated test-clock void time
	ptr := func(tm time.Time) *time.Time { return &tm }

	cases := []struct {
		name        string
		voidedAt    *time.Time
		occurredAt  time.Time
		isSimulated bool
		want        bool
	}{
		{"no void (PI expiry) → keep", nil, wall, false, false},
		{"no void on simulated → keep", nil, wall, true, false},
		{"wall-clock void co-occurs (within 5m) → drop", ptr(wall.Add(30 * time.Second)), wall, false, true},
		{"wall-clock void long after (1h) → keep", ptr(wall.Add(1 * time.Hour)), wall, false, false},
		{"simulated void, cross-domain times years apart → drop", ptr(sim), wall, true, true},
		{"simulated but no void → keep", nil, wall, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dropCanceledForVoid(tc.voidedAt, tc.occurredAt, tc.isSimulated); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestFormatPaymentCardDetail covers the sub-line text shown
// beneath the "Invoice paid" timeline row (ADR-020). Brand is
// title-cased per Stripe convention; missing fields degrade
// cleanly.
func TestFormatPaymentCardDetail(t *testing.T) {
	cases := []struct {
		name  string
		brand string
		last4 string
		want  string
	}{
		{"both present, brand title-cased", "visa", "4242", "via Visa •••• 4242"},
		{"mastercard", "mastercard", "1234", "via Mastercard •••• 1234"},
		{"amex variant", "amex", "0005", "via American Express •••• 0005"},
		{"american express snake_case (Stripe DisplayBrand)", "american_express", "0005", "via American Express •••• 0005"},
		{"american express full", "american express", "0005", "via American Express •••• 0005"},
		{"cartes bancaires (dual-branded EU card)", "cartes_bancaires", "1111", "via Cartes Bancaires •••• 1111"},
		{"diners_club Stripe DisplayBrand form", "diners_club", "2222", "via Diners Club •••• 2222"},
		{"union_pay Stripe DisplayBrand form", "union_pay", "3333", "via UnionPay •••• 3333"},
		{"other -> Card", "other", "4444", "via Card •••• 4444"},
		{"unknown future brand title-cased per snake segment", "future_network", "9999", "via Future Network •••• 9999"},
		{"missing brand keeps last4 visible", "", "4242", "via card •••• 4242"},
		{"missing last4 keeps brand visible", "visa", "", "via Visa"},
		{"both empty: no sub-line", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatPaymentCardDetail(tc.brand, tc.last4)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestMergeFailedPaymentTwins covers the Stripe payment_failed ↔
// dunning [dunning_started, retry_attempted] consolidation that
// gives the invoice activity timeline one row per business fact
// (Stripe / Lago dashboard shape). Pairs by chronological index so
// it survives the simulated-time vs wall-clock split for test-clock-
// pinned invoices. Each subtest documents one rule.
func TestMergeFailedPaymentTwins(t *testing.T) {
	base := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	ts := func(d time.Duration) string { return base.Add(d).Format(time.RFC3339) }
	amt := func(c int64) *int64 { return &c }

	stripeFailed := func(at time.Duration, pi string) timelineEvent {
		return timelineEvent{
			Timestamp:       ts(at),
			Source:          "stripe",
			EventType:       "payment_intent.payment_failed",
			PaymentIntentID: pi,
			AmountCents:     amt(4900),
			Currency:        "USD",
			Error:           "card_declined",
		}
	}
	dunningStarted := func(at time.Duration) timelineEvent {
		return timelineEvent{
			Timestamp: ts(at),
			Source:    "dunning",
			EventType: "dunning_started",
		}
	}
	dunningRetry := func(at time.Duration, attempt int) timelineEvent {
		return timelineEvent{
			Timestamp:    ts(at),
			Source:       "dunning",
			EventType:    "retry_attempted",
			AttemptCount: attempt,
		}
	}

	t.Run("one-to-one: merge attrs onto dunning row, drop Stripe row", func(t *testing.T) {
		got := mergeFailedPaymentTwins([]timelineEvent{
			stripeFailed(30*time.Second, "pi_abc"),
			dunningRetry(0, 1),
		})
		if len(got) != 1 {
			t.Fatalf("want 1 row after merge, got %d", len(got))
		}
		if got[0].Source != "dunning" {
			t.Errorf("survivor should be the dunning row, got source=%q", got[0].Source)
		}
		if got[0].PaymentIntentID != "pi_abc" || got[0].AmountCents == nil || *got[0].AmountCents != 4900 || got[0].Currency != "USD" || got[0].Error != "card_declined" {
			t.Errorf("Stripe attrs should lift onto dunning row, got %+v", got[0])
		}
	})

	t.Run("simulated time + wall-clock pair across month gap (index pairing)", func(t *testing.T) {
		// Simulates a test-clock-pinned invoice: dunning event in
		// frozen time, Stripe webhook in wall-clock time. The two
		// timestamps differ by ~14 months but the rows describe the
		// same fact — and index pairing matches them.
		got := mergeFailedPaymentTwins([]timelineEvent{
			stripeFailed(14*30*24*time.Hour, "pi_real"),
			dunningRetry(0, 1),
		})
		if len(got) != 1 {
			t.Fatalf("index pairing should bridge time domains, got %d rows", len(got))
		}
		if got[0].Source != "dunning" || got[0].PaymentIntentID != "pi_real" {
			t.Errorf("dunning row should absorb Stripe attrs, got %+v", got[0])
		}
	})

	t.Run("dunning_started pairs with the initial Stripe failure", func(t *testing.T) {
		got := mergeFailedPaymentTwins([]timelineEvent{
			stripeFailed(30*time.Second, "pi_initial"),
			dunningStarted(0),
		})
		if len(got) != 1 || got[0].EventType != "dunning_started" || got[0].PaymentIntentID != "pi_initial" {
			t.Fatalf("dunning_started should absorb Stripe failure, got %+v", got)
		}
	})

	t.Run("k-th Stripe failure pairs with k-th dunning attempt event", func(t *testing.T) {
		// Two Stripe failures + two dunning attempts (started + retry).
		// Index pairing: stripe[0] ↔ dunning_started, stripe[1] ↔ retry #1.
		got := mergeFailedPaymentTwins([]timelineEvent{
			stripeFailed(time.Hour, "pi_initial"),
			stripeFailed(2*time.Hour, "pi_retry1"),
			dunningStarted(0),
			dunningRetry(30*time.Minute, 1),
		})
		if len(got) != 2 {
			t.Fatalf("want 2 dunning survivors after both merges, got %d", len(got))
		}
		var startedRow, retryRow *timelineEvent
		for i := range got {
			switch got[i].EventType {
			case "dunning_started":
				startedRow = &got[i]
			case "retry_attempted":
				retryRow = &got[i]
			}
		}
		if startedRow == nil || retryRow == nil {
			t.Fatalf("expected both dunning rows present, got %+v", got)
		}
		if startedRow.PaymentIntentID != "pi_initial" {
			t.Errorf("dunning_started should pair with initial Stripe failure, got PI=%q", startedRow.PaymentIntentID)
		}
		if retryRow.PaymentIntentID != "pi_retry1" {
			t.Errorf("retry #1 should pair with second Stripe failure, got PI=%q", retryRow.PaymentIntentID)
		}
	})

	t.Run("excess Stripe rows (no dunning twin) survive", func(t *testing.T) {
		got := mergeFailedPaymentTwins([]timelineEvent{
			stripeFailed(0, "pi_a"),
			stripeFailed(time.Hour, "pi_b"),
			dunningRetry(0, 1),
		})
		if len(got) != 2 {
			t.Fatalf("want 1 dunning + 1 surviving Stripe, got %d", len(got))
		}
		var dunningRow, stripeRow *timelineEvent
		for i := range got {
			switch got[i].Source {
			case "dunning":
				dunningRow = &got[i]
			case "stripe":
				stripeRow = &got[i]
			}
		}
		if dunningRow == nil || stripeRow == nil {
			t.Fatalf("expected one of each, got %+v", got)
		}
		if dunningRow.PaymentIntentID != "pi_a" {
			t.Errorf("dunning row should absorb earlier Stripe failure, got PI=%q", dunningRow.PaymentIntentID)
		}
		if stripeRow.PaymentIntentID != "pi_b" {
			t.Errorf("later Stripe row should survive, got PI=%q", stripeRow.PaymentIntentID)
		}
	})

	t.Run("excess dunning rows (no Stripe twin yet) survive", func(t *testing.T) {
		got := mergeFailedPaymentTwins([]timelineEvent{
			stripeFailed(0, "pi_initial"),
			dunningStarted(0),
			dunningRetry(time.Hour, 1),
		})
		if len(got) != 2 {
			t.Fatalf("want 2 dunning survivors (one merged, one orphan), got %d", len(got))
		}
	})

	t.Run("dunning row with pre-existing PI id is not overwritten", func(t *testing.T) {
		dunning := dunningRetry(0, 1)
		dunning.PaymentIntentID = "pi_preset"
		got := mergeFailedPaymentTwins([]timelineEvent{
			stripeFailed(15*time.Second, "pi_stripe"),
			dunning,
		})
		if len(got) != 1 || got[0].PaymentIntentID != "pi_preset" {
			t.Errorf("existing PI id should be preserved, got %+v", got)
		}
	})

	t.Run("payment_intent.succeeded is not affected (different event type)", func(t *testing.T) {
		got := mergeFailedPaymentTwins([]timelineEvent{
			{Timestamp: ts(15 * time.Second), Source: "stripe", EventType: "payment_intent.succeeded", PaymentIntentID: "pi_ok"},
			dunningRetry(0, 1),
		})
		if len(got) != 2 {
			t.Fatalf("succeeded row should not be deduped here, got %d rows", len(got))
		}
	})

	t.Run("no Stripe failures: pass-through", func(t *testing.T) {
		in := []timelineEvent{
			dunningStarted(0),
			{Timestamp: ts(time.Minute), Source: "lifecycle", EventType: "invoice.finalized"},
		}
		got := mergeFailedPaymentTwins(in)
		if len(got) != 2 {
			t.Fatalf("want passthrough, got %d", len(got))
		}
	})

	t.Run("Detail lifts from Stripe row onto dunning row when dunning has none", func(t *testing.T) {
		s := stripeFailed(0, "pi_x")
		s.Detail = "Customer notified by email"
		got := mergeFailedPaymentTwins([]timelineEvent{s, dunningRetry(0, 1)})
		if len(got) != 1 || got[0].Detail != "Customer notified by email" {
			t.Errorf("Detail should lift, got %+v", got)
		}
	})
}

// TestFoldEmailIntoStripeFailed covers the first pass of the
// timeline consolidation chain — successful payment-failed email
// rows fold as a Detail sub-line on the co-occurring Stripe failure
// row, then drop. Pending / failed sends survive as standalone rows
// so operators see delivery problems.
func TestFoldEmailIntoStripeFailed(t *testing.T) {
	base := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	ts := func(d time.Duration) string { return base.Add(d).Format(time.RFC3339) }
	amt := func(c int64) *int64 { return &c }

	stripeFailed := func(at time.Duration, pi string) timelineEvent {
		return timelineEvent{
			Timestamp:       ts(at),
			Source:          "stripe",
			EventType:       "payment_intent.payment_failed",
			PaymentIntentID: pi,
			AmountCents:     amt(4900),
			Currency:        "USD",
		}
	}
	emailFailedSent := func(at time.Duration, status string) timelineEvent {
		return timelineEvent{
			Timestamp: ts(at),
			Source:    "email",
			EventType: "email.payment_failed",
			Status:    status,
		}
	}

	t.Run("succeeded email folds into co-occurring Stripe failure as Detail", func(t *testing.T) {
		got := foldEmailIntoStripeFailed([]timelineEvent{
			stripeFailed(0, "pi_a"),
			emailFailedSent(30*time.Second, "succeeded"),
		}, 2*time.Minute)
		if len(got) != 1 {
			t.Fatalf("want email folded into Stripe row, got %d rows", len(got))
		}
		if got[0].Source != "stripe" || got[0].Detail != "Customer notified by email" {
			t.Errorf("Stripe row should carry email sub-line, got %+v", got[0])
		}
	})

	t.Run("failed-delivery email survives as standalone row", func(t *testing.T) {
		got := foldEmailIntoStripeFailed([]timelineEvent{
			stripeFailed(0, "pi_a"),
			emailFailedSent(30*time.Second, "failed"),
		}, 2*time.Minute)
		if len(got) != 2 {
			t.Fatalf("delivery problem must stay visible, got %d rows", len(got))
		}
	})

	t.Run("pending email survives as standalone row", func(t *testing.T) {
		got := foldEmailIntoStripeFailed([]timelineEvent{
			stripeFailed(0, "pi_a"),
			emailFailedSent(30*time.Second, "processing"),
		}, 2*time.Minute)
		if len(got) != 2 {
			t.Fatalf("processing email should be visible, got %d rows", len(got))
		}
	})

	t.Run("email outside window survives", func(t *testing.T) {
		got := foldEmailIntoStripeFailed([]timelineEvent{
			stripeFailed(0, "pi_a"),
			emailFailedSent(10*time.Minute, "succeeded"),
		}, 2*time.Minute)
		if len(got) != 2 {
			t.Fatalf("outside window must survive, got %d rows", len(got))
		}
	})

	t.Run("one-to-one: two emails against one Stripe row leaves the unclaimed email", func(t *testing.T) {
		got := foldEmailIntoStripeFailed([]timelineEvent{
			stripeFailed(0, "pi_a"),
			emailFailedSent(15*time.Second, "succeeded"),
			emailFailedSent(45*time.Second, "succeeded"),
		}, 2*time.Minute)
		if len(got) != 2 {
			t.Fatalf("want 1 stripe + 1 surviving email, got %d", len(got))
		}
	})

	t.Run("non-payment-failed email types untouched", func(t *testing.T) {
		got := foldEmailIntoStripeFailed([]timelineEvent{
			stripeFailed(0, "pi_a"),
			{Timestamp: ts(30 * time.Second), Source: "email", EventType: "email.invoice", Status: "succeeded"},
		}, 2*time.Minute)
		if len(got) != 2 {
			t.Fatalf("invoice email must not fold, got %d rows", len(got))
		}
	})

	t.Run("preserves a pre-set Detail on the Stripe row", func(t *testing.T) {
		s := stripeFailed(0, "pi_a")
		s.Detail = "Pre-existing detail"
		got := foldEmailIntoStripeFailed([]timelineEvent{
			s,
			emailFailedSent(30*time.Second, "succeeded"),
		}, 2*time.Minute)
		if len(got) != 1 || got[0].Detail != "Pre-existing detail" {
			t.Errorf("Detail must not be overwritten, got %+v", got)
		}
	})

	t.Run("no payment_failed email: pass-through", func(t *testing.T) {
		in := []timelineEvent{stripeFailed(0, "pi_a")}
		got := foldEmailIntoStripeFailed(in, 2*time.Minute)
		if len(got) != 1 {
			t.Fatalf("want unchanged, got %d", len(got))
		}
	})
}
