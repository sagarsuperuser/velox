package payment

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

type TokenService struct {
	db *postgres.DB
}

func NewTokenService(db *postgres.DB) *TokenService {
	return &TokenService{db: db}
}

type PaymentUpdateToken struct {
	ID         string
	TenantID   string
	CustomerID string
	InvoiceID  string
	ExpiresAt  time.Time
}

// Create generates a new payment update token. Returns the raw token (shown once in email).
func (s *TokenService) Create(ctx context.Context, tenantID, customerID, invoiceID string) (string, error) {
	// Generate 32 random bytes
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	rawToken := "vlx_pt_" + hex.EncodeToString(buf)

	// Hash for storage
	hash := sha256.Sum256([]byte(rawToken))
	tokenHash := hex.EncodeToString(hash[:])

	id := postgres.NewID("vlx_ptk")
	expiresAt := time.Now().UTC().Add(24 * time.Hour)

	_, err := s.db.Pool.ExecContext(ctx, `
		INSERT INTO payment_update_tokens (id, tenant_id, customer_id, invoice_id, token_hash, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, id, tenantID, customerID, invoiceID, tokenHash, expiresAt)
	if err != nil {
		return "", fmt.Errorf("store token: %w", err)
	}

	return rawToken, nil
}

// Validate checks a raw token and returns the associated data. Does NOT mark as used.
func (s *TokenService) Validate(ctx context.Context, rawToken string) (*PaymentUpdateToken, error) {
	hash := sha256.Sum256([]byte(rawToken))
	tokenHash := hex.EncodeToString(hash[:])

	var t PaymentUpdateToken
	err := s.db.Pool.QueryRowContext(ctx, `
		SELECT id, tenant_id, customer_id, invoice_id, expires_at
		FROM payment_update_tokens
		WHERE token_hash = $1 AND expires_at > NOW() AND used_at IS NULL
	`, tokenHash).Scan(&t.ID, &t.TenantID, &t.CustomerID, &t.InvoiceID, &t.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("invalid or expired token")
	}

	return &t, nil
}

// MarkUsed marks a token as consumed (prevents reuse).
func (s *TokenService) MarkUsed(ctx context.Context, rawToken string) error {
	hash := sha256.Sum256([]byte(rawToken))
	tokenHash := hex.EncodeToString(hash[:])

	_, err := s.db.Pool.ExecContext(ctx, `
		UPDATE payment_update_tokens SET used_at = NOW() WHERE token_hash = $1
	`, tokenHash)
	return err
}

// Cleanup deletes expired tokens older than 7 days.
func (s *TokenService) Cleanup(ctx context.Context) (int, error) {
	result, err := s.db.Pool.ExecContext(ctx, `
		DELETE FROM payment_update_tokens WHERE expires_at < NOW() - INTERVAL '7 days'
	`)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}
