package dunning

import (
	"context"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// captureDispatcher records every dispatched event for assertion.
type captureDispatcher struct {
	events []capturedDunningEvent
}

type capturedDunningEvent struct {
	eventType string
	payload   map[string]any
}

func (c *captureDispatcher) Dispatch(_ context.Context, _, eventType string, payload map[string]any) error {
	c.events = append(c.events, capturedDunningEvent{eventType: eventType, payload: payload})
	return nil
}

func (c *captureDispatcher) firstOf(eventType string) (map[string]any, bool) {
	for _, e := range c.events {
		if e.eventType == eventType {
			return e.payload, true
		}
	}
	return nil, false
}

// TestDunningResolved_EventFires covers the medium-severity audit finding:
// dunning.resolved was advertised in the event catalog but never dispatched,
// silently dropping the payment-recovery signal that integrators subscribe to.
// Both operator-facing resolution paths (ResolveRun, ResolveByInvoice) must
// fire it with the run/invoice/customer/resolution payload.
func TestDunningResolved_EventFires(t *testing.T) {
	t.Run("ResolveRun", func(t *testing.T) {
		store := newMemStore()
		svc := NewService(store, &noopRetrier{}, nil)
		disp := &captureDispatcher{}
		svc.SetEventDispatcher(disp)
		ctx := context.Background()

		run, _ := svc.StartDunning(ctx, "t1", "inv_1", "cus_1", time.Now())
		if _, err := svc.ResolveRun(ctx, "t1", run.ID, domain.ResolutionPaymentRecovered); err != nil {
			t.Fatalf("ResolveRun: %v", err)
		}

		payload, ok := disp.firstOf(domain.EventDunningResolved)
		if !ok {
			t.Fatalf("dunning.resolved not dispatched; got %+v", disp.events)
		}
		assertResolvedPayload(t, payload, "inv_1", "cus_1", string(domain.ResolutionPaymentRecovered))
	})

	t.Run("ResolveByInvoice", func(t *testing.T) {
		store := newMemStore()
		svc := NewService(store, &noopRetrier{}, nil)
		disp := &captureDispatcher{}
		svc.SetEventDispatcher(disp)
		ctx := context.Background()

		if _, err := svc.StartDunning(ctx, "t1", "inv_2", "cus_2", time.Now()); err != nil {
			t.Fatalf("StartDunning: %v", err)
		}
		if err := svc.ResolveByInvoice(ctx, "t1", "inv_2", domain.ResolutionPaymentRecovered); err != nil {
			t.Fatalf("ResolveByInvoice: %v", err)
		}

		payload, ok := disp.firstOf(domain.EventDunningResolved)
		if !ok {
			t.Fatalf("dunning.resolved not dispatched; got %+v", disp.events)
		}
		assertResolvedPayload(t, payload, "inv_2", "cus_2", string(domain.ResolutionPaymentRecovered))
	})
}

func assertResolvedPayload(t *testing.T, payload map[string]any, invoiceID, customerID, resolution string) {
	t.Helper()
	if payload["invoice_id"] != invoiceID {
		t.Errorf("invoice_id: got %v, want %s", payload["invoice_id"], invoiceID)
	}
	if payload["customer_id"] != customerID {
		t.Errorf("customer_id: got %v, want %s", payload["customer_id"], customerID)
	}
	if payload["resolution"] != resolution {
		t.Errorf("resolution: got %v, want %s", payload["resolution"], resolution)
	}
	if payload["run_id"] == nil || payload["run_id"] == "" {
		t.Errorf("run_id missing from payload: %+v", payload)
	}
}
