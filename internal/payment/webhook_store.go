package payment

import (
	"context"
	"encoding/json"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// PostgresWebhookStore persists Stripe webhook events for audit trail.
type PostgresWebhookStore struct {
	db *postgres.DB
}

func NewPostgresWebhookStore(db *postgres.DB) *PostgresWebhookStore {
	return &PostgresWebhookStore{db: db}
}

func (s *PostgresWebhookStore) IngestEvent(ctx context.Context, tenantID string, event domain.StripeWebhookEvent) (domain.StripeWebhookEvent, bool, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.StripeWebhookEvent{}, false, err
	}
	defer postgres.Rollback(tx)

	id := postgres.NewID("vlx_swe")
	now := time.Now().UTC()
	payloadJSON, _ := json.Marshal(event.Payload)
	if event.Payload == nil {
		payloadJSON = []byte("{}")
	}

	var amountCents *int64
	if event.AmountCents != nil {
		amountCents = event.AmountCents
	}

	err = tx.QueryRowContext(ctx, `
		INSERT INTO stripe_webhook_events (id, tenant_id, stripe_event_id, event_type, object_type,
			invoice_id, customer_external_id, payment_intent_id, payment_status,
			amount_cents, currency, failure_message, payload, received_at, occurred_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		ON CONFLICT (tenant_id, stripe_event_id) DO NOTHING
		RETURNING id
	`, id, tenantID, event.StripeEventID, event.EventType, event.ObjectType,
		postgres.NullableString(event.InvoiceID), postgres.NullableString(event.CustomerExternalID),
		postgres.NullableString(event.PaymentIntentID), postgres.NullableString(event.PaymentStatus),
		amountCents, postgres.NullableString(event.Currency),
		postgres.NullableString(event.FailureMessage), payloadJSON, now, now,
	).Scan(&event.ID)

	if err != nil {
		// ON CONFLICT DO NOTHING → no row returned → duplicate
		event.ID = ""
		if err := tx.Commit(); err != nil {
			return event, false, err
		}
		return event, false, nil
	}

	event.TenantID = tenantID
	event.ReceivedAt = now
	if err := tx.Commit(); err != nil {
		return domain.StripeWebhookEvent{}, false, err
	}
	return event, true, nil
}
