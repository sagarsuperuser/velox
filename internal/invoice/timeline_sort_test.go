package invoice

import (
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// TestSortInvoiceTimeline_CausalTies locks the 2026-07-19 audit findings:
// a frozen-clock close cascade stamps finalize → dunning start → retries →
// escalation → write-off → resolve at ONE instant, and the old second-
// truncated string sort fell back to source-major insertion — rendering
// "Marked uncollectible" above the escalation that caused it, and
// "Invoice paid" above the same-second retry that collected it. Ties now
// order by causal rank; distinct sub-second instants order by full
// precision (the serialized string can't carry them).
func TestSortInvoiceTimeline_CausalTies(t *testing.T) {
	instant := time.Date(2027, 3, 8, 18, 30, 0, 0, time.UTC)

	t.Run("same-instant cascade orders causally regardless of insertion", func(t *testing.T) {
		// Deliberately inserted in the OLD source-major order that
		// rendered anti-causally: lifecycle first, dunning last.
		events := []timelineEvent{
			{EventType: "invoice.uncollectible", sortAt: instant, tieRank: rankLifecycleTerminal},
			{EventType: "invoice.finalized", sortAt: instant, tieRank: rankInvoiceFinalized},
			{EventType: "invoice.created", sortAt: instant, tieRank: rankInvoiceCreated},
			{EventType: "dunning.retry_attempted", sortAt: instant, tieRank: rankRetryAttempted},
			{EventType: "dunning.escalated", sortAt: instant, tieRank: rankEscalated},
			{EventType: "dunning.started", sortAt: instant, tieRank: rankDunningStarted},
		}
		sortInvoiceTimeline(events)
		want := []string{
			"invoice.created", "invoice.finalized", "dunning.started",
			"dunning.retry_attempted", "dunning.escalated", "invoice.uncollectible",
		}
		for i, w := range want {
			if events[i].EventType != w {
				t.Fatalf("position %d: got %s, want %s (full order: %v)", i, events[i].EventType, w, typesOf(events))
			}
		}
	})

	t.Run("failed retry precedes same-instant settle", func(t *testing.T) {
		events := []timelineEvent{
			{EventType: "invoice.paid", sortAt: instant, tieRank: rankLifecycleTerminal},
			{EventType: "dunning.retry_attempted", sortAt: instant, tieRank: rankRetryAttempted},
		}
		sortInvoiceTimeline(events)
		if events[0].EventType != "dunning.retry_attempted" {
			t.Errorf("the retry that collected the invoice must render before 'Invoice paid': got %v", typesOf(events))
		}
	})

	t.Run("sub-second precision beats rank", func(t *testing.T) {
		// Two CNs 36ms apart — the serialized string collides at second
		// granularity; full-precision sortAt must order them, with the
		// later-written CN below the earlier one whatever the rank says.
		events := []timelineEvent{
			{EventType: "cn.second", sortAt: instant.Add(36 * time.Millisecond), tieRank: rankCreditNote},
			{EventType: "cn.first", sortAt: instant.Add(4 * time.Millisecond), tieRank: rankCreditNote},
			{EventType: "late.lifecycle", sortAt: instant.Add(900 * time.Millisecond), tieRank: rankInvoiceCreated},
		}
		sortInvoiceTimeline(events)
		if events[0].EventType != "cn.first" || events[1].EventType != "cn.second" || events[2].EventType != "late.lifecycle" {
			t.Errorf("full-precision instants must dominate rank: got %v", typesOf(events))
		}
	})

	t.Run("dunningEventRank maps every kind causally", func(t *testing.T) {
		if !(dunningEventRank(domain.DunningEventStarted) < dunningEventRank(domain.DunningEventRetryAttempted) &&
			dunningEventRank(domain.DunningEventRetryAttempted) < dunningEventRank(domain.DunningEventEscalated) &&
			dunningEventRank(domain.DunningEventEscalated) < rankLifecycleTerminal &&
			rankLifecycleTerminal < dunningEventRank(domain.DunningEventResolved)) {
			t.Error("dunning rank ordering broken: started < retry < escalated < terminal-lifecycle < resolved must hold")
		}
	})
}

func typesOf(events []timelineEvent) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.EventType
	}
	return out
}
