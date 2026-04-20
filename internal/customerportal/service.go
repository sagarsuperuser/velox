package customerportal

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sagarsuperuser/velox/internal/errs"
)

// Service is the programmatic surface for portal sessions. Handlers depend
// on this, not on Store directly.
type Service struct {
	store Store
}

func NewService(store Store) *Service {
	return &Service{store: store}
}

// CreateResult bundles the persisted session and the raw token that must
// be handed to the customer. The token is returned once, at create time,
// and never retrievable again — mirrors api_keys.
type CreateResult struct {
	Session  Session
	RawToken string
}

// Create mints a new portal session for (tenantID, customerID). ttl of 0
// means DefaultTTL. Caller is an authenticated tenant operator.
func (s *Service) Create(ctx context.Context, tenantID, customerID string, ttl time.Duration) (CreateResult, error) {
	if tenantID == "" {
		return CreateResult{}, errs.Required("tenant_id")
	}
	if customerID == "" {
		return CreateResult{}, errs.Required("customer_id")
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}

	raw, hash, err := newToken()
	if err != nil {
		return CreateResult{}, fmt.Errorf("generate token: %w", err)
	}

	sess, err := s.store.Create(ctx, tenantID, customerID, hash, time.Now().UTC().Add(ttl))
	if err != nil {
		return CreateResult{}, err
	}
	return CreateResult{Session: sess, RawToken: raw}, nil
}

// Validate resolves a raw bearer token into a session record. Returns
// errs.ErrNotFound (wrapping nil row / expired / revoked — we don't leak
// which) so Middleware can map the whole class to a single 401.
func (s *Service) Validate(ctx context.Context, rawToken string) (Session, error) {
	if rawToken == "" {
		return Session{}, errs.ErrNotFound
	}
	sess, err := s.store.GetByTokenHash(ctx, hashToken(rawToken))
	if err != nil {
		if errors.Is(err, errs.ErrNotFound) {
			return Session{}, errs.ErrNotFound
		}
		return Session{}, err
	}
	return sess, nil
}

// Revoke invalidates a session. Idempotent. Not currently exposed over
// HTTP, but tenant operators can call it through the service in-proc when
// a customer off-boards.
func (s *Service) Revoke(ctx context.Context, tenantID, sessionID string) error {
	return s.store.Revoke(ctx, tenantID, sessionID)
}
