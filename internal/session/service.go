package session

import (
	"context"
	"time"
)

// Service is the auth-flow facade over Store. Issue mints a session
// from a validated key context (caller has already resolved the key);
// Resolve looks up a raw cookie value and returns the Session if it's
// still active; Revoke handles logout. The package never accepts an
// API key directly — key validation is auth.Service's job.
type Service struct {
	store Store
	now   func() time.Time
	ttl   time.Duration
}

// NewService wires defaults: real wall clock, DefaultTTL.
func NewService(store Store) *Service {
	return &Service{store: store, now: time.Now, ttl: DefaultTTL}
}

// IssueInput is the contract for Issue. Callers (the exchange handler)
// pass everything needed to mint a session row — the API key has
// already been validated and resolved.
type IssueInput struct {
	KeyID     string
	TenantID  string
	Livemode  bool
	UserAgent string
	IP        string
}

// Issue creates a session row and returns the raw cookie value the
// caller should set on the response. The raw value is shown to the
// caller exactly once; the DB stores sha256(raw) so a snapshot can't
// be replayed.
func (s *Service) Issue(ctx context.Context, in IssueInput) (rawID string, sess Session, err error) {
	rawID, err = newRawID()
	if err != nil {
		return "", Session{}, err
	}
	now := s.now().UTC()
	sess = Session{
		IDHash:     HashID(rawID),
		KeyID:      in.KeyID,
		TenantID:   in.TenantID,
		Livemode:   in.Livemode,
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  now.Add(s.ttl),
		UserAgent:  in.UserAgent,
		IP:         in.IP,
	}
	if err := s.store.Insert(ctx, sess); err != nil {
		return "", Session{}, err
	}
	return rawID, sess, nil
}

// Resolve looks up a raw cookie value and returns the Session if it's
// active. Revoked or expired rows return ErrNotFound — the middleware
// collapses both into 401 to deny session-id enumeration.
func (s *Service) Resolve(ctx context.Context, rawID string) (Session, error) {
	if rawID == "" {
		return Session{}, ErrNotFound
	}
	sess, err := s.store.GetByIDHash(ctx, HashID(rawID))
	if err != nil {
		return Session{}, err
	}
	if !sess.IsActive(s.now()) {
		return Session{}, ErrNotFound
	}
	return sess, nil
}

// Revoke marks the session row as revoked. Idempotent — revoking an
// already-revoked or non-existent row is a no-op.
func (s *Service) Revoke(ctx context.Context, rawID string) error {
	if rawID == "" {
		return nil
	}
	return s.store.Revoke(ctx, HashID(rawID))
}

// RevokeAllForKey is invoked when an API key is revoked through the
// /v1/api-keys surface — all browser sessions minted from that key
// must die at the same instant. Idempotent.
func (s *Service) RevokeAllForKey(ctx context.Context, keyID string) error {
	if keyID == "" {
		return nil
	}
	return s.store.RevokeAllForKey(ctx, keyID)
}
