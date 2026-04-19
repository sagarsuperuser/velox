package webhook

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/crypto"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

type PostgresStore struct {
	db  *postgres.DB
	enc *crypto.Encryptor
}

func NewPostgresStore(db *postgres.DB) *PostgresStore {
	return &PostgresStore{db: db, enc: crypto.NewNoop()}
}

// SetEncryptor configures AES-256-GCM encryption for webhook signing secrets
// at rest. When set (non-noop), Create/UpdateEndpointSecret encrypt the raw
// whsec_ secret before INSERT; Get/ListEndpoints decrypt it after SELECT so
// the Dispatch path can sign with the plaintext key. Without this, the raw
// signing key is stored in plaintext — a DB dump yields webhook-forging
// capability against every tenant's receivers.
func (s *PostgresStore) SetEncryptor(enc *crypto.Encryptor) {
	if enc == nil {
		enc = crypto.NewNoop()
	}
	s.enc = enc
}

// lastFour returns the last 4 characters of the raw signing secret (e.g.
// "whsec_abc...xyz9" → "xyz9") for display in the UI. We compute it on
// write and persist alongside the ciphertext because the ciphertext is
// non-deterministic — we can't recompute last4 from storage without
// decrypting first.
func lastFour(secret string) string {
	if len(secret) <= 4 {
		return secret
	}
	return secret[len(secret)-4:]
}

func (s *PostgresStore) CreateEndpoint(ctx context.Context, tenantID string, ep domain.WebhookEndpoint) (domain.WebhookEndpoint, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.WebhookEndpoint{}, err
	}
	defer postgres.Rollback(tx)

	id := postgres.NewID("vlx_whe")
	now := time.Now().UTC()
	eventsJSON, _ := json.Marshal(ep.Events)

	plaintextSecret := ep.Secret
	secretEncrypted, err := s.enc.Encrypt(plaintextSecret)
	if err != nil {
		return domain.WebhookEndpoint{}, fmt.Errorf("encrypt webhook secret: %w", err)
	}
	secretLast4 := lastFour(plaintextSecret)

	err = tx.QueryRowContext(ctx, `
		INSERT INTO webhook_endpoints (id, tenant_id, url, description, secret_encrypted, secret_last4, events, active, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9)
		RETURNING id, tenant_id, url, COALESCE(description,''), events, active, created_at, updated_at
	`, id, tenantID, ep.URL, postgres.NullableString(ep.Description), secretEncrypted, secretLast4, eventsJSON, ep.Active, now,
	).Scan(&ep.ID, &ep.TenantID, &ep.URL, &ep.Description, &eventsJSON, &ep.Active, &ep.CreatedAt, &ep.UpdatedAt)
	if err != nil {
		return domain.WebhookEndpoint{}, err
	}
	_ = json.Unmarshal(eventsJSON, &ep.Events)
	ep.Secret = plaintextSecret // Callers need it once to show to the user.
	ep.SecretLast4 = secretLast4
	if err := tx.Commit(); err != nil {
		return domain.WebhookEndpoint{}, err
	}
	return ep, nil
}

func (s *PostgresStore) GetEndpoint(ctx context.Context, tenantID, id string) (domain.WebhookEndpoint, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.WebhookEndpoint{}, err
	}
	defer postgres.Rollback(tx)

	var ep domain.WebhookEndpoint
	var eventsJSON []byte
	var secretEncrypted string
	err = tx.QueryRowContext(ctx, `
		SELECT id, tenant_id, url, COALESCE(description,''), secret_encrypted, secret_last4, events, active, created_at, updated_at
		FROM webhook_endpoints WHERE id = $1
	`, id).Scan(&ep.ID, &ep.TenantID, &ep.URL, &ep.Description, &secretEncrypted, &ep.SecretLast4, &eventsJSON, &ep.Active, &ep.CreatedAt, &ep.UpdatedAt)
	if err == sql.ErrNoRows {
		return domain.WebhookEndpoint{}, errs.ErrNotFound
	}
	if err != nil {
		return domain.WebhookEndpoint{}, err
	}
	if ep.Secret, err = s.enc.Decrypt(secretEncrypted); err != nil {
		return domain.WebhookEndpoint{}, fmt.Errorf("decrypt webhook secret: %w", err)
	}
	_ = json.Unmarshal(eventsJSON, &ep.Events)
	return ep, nil
}

func (s *PostgresStore) ListEndpoints(ctx context.Context, tenantID string) ([]domain.WebhookEndpoint, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	rows, err := tx.QueryContext(ctx, `
		SELECT id, tenant_id, url, COALESCE(description,''), secret_encrypted, secret_last4, events, active, created_at, updated_at
		FROM webhook_endpoints WHERE active = true ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var endpoints []domain.WebhookEndpoint
	for rows.Next() {
		var ep domain.WebhookEndpoint
		var eventsJSON []byte
		var secretEncrypted string
		if err := rows.Scan(&ep.ID, &ep.TenantID, &ep.URL, &ep.Description, &secretEncrypted, &ep.SecretLast4, &eventsJSON, &ep.Active, &ep.CreatedAt, &ep.UpdatedAt); err != nil {
			return nil, err
		}
		if ep.Secret, err = s.enc.Decrypt(secretEncrypted); err != nil {
			return nil, fmt.Errorf("decrypt webhook secret: %w", err)
		}
		_ = json.Unmarshal(eventsJSON, &ep.Events)
		endpoints = append(endpoints, ep)
	}
	return endpoints, rows.Err()
}

func (s *PostgresStore) DeleteEndpoint(ctx context.Context, tenantID, id string) error {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return err
	}
	defer postgres.Rollback(tx)

	result, err := tx.ExecContext(ctx, `UPDATE webhook_endpoints SET active = false WHERE id = $1`, id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return errs.ErrNotFound
	}
	return tx.Commit()
}

func (s *PostgresStore) UpdateEndpointSecret(ctx context.Context, tenantID, id, newSecret string) (domain.WebhookEndpoint, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.WebhookEndpoint{}, err
	}
	defer postgres.Rollback(tx)

	secretEncrypted, err := s.enc.Encrypt(newSecret)
	if err != nil {
		return domain.WebhookEndpoint{}, fmt.Errorf("encrypt webhook secret: %w", err)
	}
	secretLast4 := lastFour(newSecret)

	var ep domain.WebhookEndpoint
	var eventsJSON []byte
	err = tx.QueryRowContext(ctx, `
		UPDATE webhook_endpoints SET secret_encrypted = $1, secret_last4 = $2, updated_at = NOW()
		WHERE id = $3
		RETURNING id, tenant_id, url, COALESCE(description,''), events, active, created_at, updated_at
	`, secretEncrypted, secretLast4, id).Scan(&ep.ID, &ep.TenantID, &ep.URL, &ep.Description, &eventsJSON, &ep.Active, &ep.CreatedAt, &ep.UpdatedAt)
	if err == sql.ErrNoRows {
		return domain.WebhookEndpoint{}, errs.ErrNotFound
	}
	if err != nil {
		return domain.WebhookEndpoint{}, err
	}
	_ = json.Unmarshal(eventsJSON, &ep.Events)
	ep.Secret = newSecret
	ep.SecretLast4 = secretLast4
	if err := tx.Commit(); err != nil {
		return domain.WebhookEndpoint{}, err
	}
	return ep, nil
}

func (s *PostgresStore) CreateEvent(ctx context.Context, tenantID string, event domain.WebhookEvent) (domain.WebhookEvent, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.WebhookEvent{}, err
	}
	defer postgres.Rollback(tx)

	id := postgres.NewID("vlx_whevt")
	now := time.Now().UTC()
	payloadJSON, _ := json.Marshal(event.Payload)

	event.ID = id
	event.TenantID = tenantID
	event.CreatedAt = now

	_, err = tx.ExecContext(ctx, `
		INSERT INTO webhook_events (id, tenant_id, event_type, payload, created_at)
		VALUES ($1,$2,$3,$4,$5)
	`, id, tenantID, event.EventType, payloadJSON, now)
	if err != nil {
		return domain.WebhookEvent{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.WebhookEvent{}, err
	}
	return event, nil
}

func (s *PostgresStore) ListEvents(ctx context.Context, tenantID string, limit int) ([]domain.WebhookEvent, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	if limit <= 0 || limit > 100 {
		limit = 50
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT id, tenant_id, event_type, payload, created_at
		FROM webhook_events ORDER BY created_at DESC LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var events []domain.WebhookEvent
	for rows.Next() {
		var e domain.WebhookEvent
		var payloadJSON []byte
		if err := rows.Scan(&e.ID, &e.TenantID, &e.EventType, &payloadJSON, &e.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(payloadJSON, &e.Payload)
		events = append(events, e)
	}
	return events, rows.Err()
}

func (s *PostgresStore) CreateDelivery(ctx context.Context, tenantID string, d domain.WebhookDelivery) (domain.WebhookDelivery, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.WebhookDelivery{}, err
	}
	defer postgres.Rollback(tx)

	id := postgres.NewID("vlx_whd")
	now := time.Now().UTC()

	d.ID = id
	d.TenantID = tenantID
	d.CreatedAt = now

	_, err = tx.ExecContext(ctx, `
		INSERT INTO webhook_deliveries (id, tenant_id, webhook_endpoint_id, webhook_event_id,
			status, created_at)
		VALUES ($1,$2,$3,$4,$5,$6)
	`, id, tenantID, d.WebhookEndpointID, d.WebhookEventID, d.Status, now)
	if err != nil {
		return domain.WebhookDelivery{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.WebhookDelivery{}, err
	}
	return d, nil
}

func (s *PostgresStore) UpdateDelivery(ctx context.Context, tenantID string, d domain.WebhookDelivery) (domain.WebhookDelivery, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.WebhookDelivery{}, err
	}
	defer postgres.Rollback(tx)

	_, err = tx.ExecContext(ctx, `
		UPDATE webhook_deliveries SET status=$1, http_status_code=$2,
			response_body=$3, error_message=$4, attempt_count=$5, completed_at=$6,
			next_retry_at=$7
		WHERE id=$8`,
		d.Status, d.HTTPStatusCode, postgres.NullableString(d.ResponseBody),
		postgres.NullableString(d.ErrorMessage), d.AttemptCount, postgres.NullableTime(d.CompletedAt),
		postgres.NullableTime(d.NextRetryAt), d.ID)
	if err != nil {
		return domain.WebhookDelivery{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.WebhookDelivery{}, err
	}
	return d, nil
}

func (s *PostgresStore) ListPendingDeliveries(ctx context.Context, limit int) ([]domain.WebhookDelivery, error) {
	if limit <= 0 {
		limit = 100
	}

	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	rows, err := tx.QueryContext(ctx, `
		SELECT id, tenant_id, webhook_endpoint_id, webhook_event_id, status,
			COALESCE(http_status_code, 0), COALESCE(response_body,''), COALESCE(error_message,''),
			attempt_count, next_retry_at, created_at, completed_at
		FROM webhook_deliveries
		WHERE status = 'pending' AND next_retry_at <= NOW()
		ORDER BY next_retry_at ASC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var deliveries []domain.WebhookDelivery
	for rows.Next() {
		var d domain.WebhookDelivery
		if err := rows.Scan(&d.ID, &d.TenantID, &d.WebhookEndpointID, &d.WebhookEventID,
			&d.Status, &d.HTTPStatusCode, &d.ResponseBody, &d.ErrorMessage,
			&d.AttemptCount, &d.NextRetryAt, &d.CreatedAt, &d.CompletedAt); err != nil {
			return nil, err
		}
		deliveries = append(deliveries, d)
	}
	return deliveries, rows.Err()
}

func (s *PostgresStore) GetEndpointStats(ctx context.Context, tenantID string) ([]EndpointStats, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	rows, err := tx.QueryContext(ctx, `
		SELECT webhook_endpoint_id,
			COUNT(*) AS total,
			COUNT(*) FILTER (WHERE status = 'succeeded') AS succeeded,
			COUNT(*) FILTER (WHERE status = 'failed') AS failed
		FROM webhook_deliveries
		GROUP BY webhook_endpoint_id
	`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var stats []EndpointStats
	for rows.Next() {
		var s EndpointStats
		if err := rows.Scan(&s.EndpointID, &s.TotalDeliveries, &s.Succeeded, &s.Failed); err != nil {
			return nil, err
		}
		if s.TotalDeliveries > 0 {
			s.SuccessRate = float64(s.Succeeded) / float64(s.TotalDeliveries) * 100
		}
		stats = append(stats, s)
	}
	return stats, rows.Err()
}

func (s *PostgresStore) ListDeliveries(ctx context.Context, tenantID, eventID string) ([]domain.WebhookDelivery, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	rows, err := tx.QueryContext(ctx, `
		SELECT id, tenant_id, webhook_endpoint_id, webhook_event_id, status,
			COALESCE(http_status_code, 0), COALESCE(response_body,''), COALESCE(error_message,''),
			attempt_count, next_retry_at, created_at, completed_at
		FROM webhook_deliveries WHERE webhook_event_id = $1
		ORDER BY created_at DESC
	`, eventID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var deliveries []domain.WebhookDelivery
	for rows.Next() {
		var d domain.WebhookDelivery
		if err := rows.Scan(&d.ID, &d.TenantID, &d.WebhookEndpointID, &d.WebhookEventID,
			&d.Status, &d.HTTPStatusCode, &d.ResponseBody, &d.ErrorMessage,
			&d.AttemptCount, &d.NextRetryAt, &d.CreatedAt, &d.CompletedAt); err != nil {
			return nil, err
		}
		deliveries = append(deliveries, d)
	}
	return deliveries, rows.Err()
}
