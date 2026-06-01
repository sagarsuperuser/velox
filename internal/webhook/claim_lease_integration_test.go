package webhook_test

import (
	"context"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/testutil"
	"github.com/sagarsuperuser/velox/internal/webhook"
)

// TestListPendingDeliveries_LeasesClaimedRows covers the medium-severity audit
// finding: the retry worker had no row-claim, so two replicas would both pick
// the same due delivery and double-deliver the webhook. ListPendingDeliveries
// now atomically claims and leases each row (FOR UPDATE SKIP LOCKED + push
// next_retry_at forward), so a second call in the same lease window returns
// nothing — the claimed row is invisible to a concurrent worker.
func TestListPendingDeliveries_LeasesClaimedRows(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx, cancel := context.WithTimeout(postgres.WithLivemode(context.Background(), false), 15*time.Second)
	defer cancel()

	tenantID := testutil.CreateTestTenant(t, db, "Webhook Claim Lease")
	store := webhook.NewPostgresStore(db)

	ep, err := store.CreateEndpoint(ctx, tenantID, domain.WebhookEndpoint{
		URL: "https://example.test/hook", Events: []string{"invoice.created"}, Active: true, Secret: "whsec_test",
	})
	if err != nil {
		t.Fatalf("create endpoint: %v", err)
	}
	evt, err := store.CreateEvent(ctx, tenantID, domain.WebhookEvent{
		EventType: "invoice.created", Payload: map[string]any{"id": "inv_1"},
	})
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	del, err := store.CreateDelivery(ctx, tenantID, domain.WebhookDelivery{
		WebhookEndpointID: ep.ID, WebhookEventID: evt.ID, Status: domain.DeliveryPending,
	})
	if err != nil {
		t.Fatalf("create delivery: %v", err)
	}

	// Make the delivery due: status=pending, next_retry_at in the past.
	setDuePast(t, db, del.ID)

	first, err := store.ListPendingDeliveries(ctx, 100)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if !containsDelivery(first, del.ID) {
		t.Fatalf("first claim should return the due delivery; got %d rows", len(first))
	}

	// Second worker (same lease window) must NOT see the leased row.
	second, err := store.ListPendingDeliveries(ctx, 100)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if containsDelivery(second, del.ID) {
		t.Errorf("second claim re-returned the leased delivery — double-delivery window still open")
	}
}

func containsDelivery(ds []domain.WebhookDelivery, id string) bool {
	for _, d := range ds {
		if d.ID == id {
			return true
		}
	}
	return false
}

func setDuePast(t *testing.T, db *postgres.DB, deliveryID string) {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), postgres.TxBypass, "")
	if err != nil {
		t.Fatalf("begin bypass tx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`
		UPDATE webhook_deliveries SET status='pending', next_retry_at = NOW() - INTERVAL '1 minute'
		WHERE id = $1
	`, deliveryID); err != nil {
		t.Fatalf("set due past: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}
