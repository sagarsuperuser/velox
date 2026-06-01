package session

import (
	"context"
	"time"
)

// Service is the auth-flow facade over Store. Issue mints a session
// for an authenticated user (caller has already verified the
// password); Resolve looks up a raw cookie value and returns the
// Session if it's still active; Revoke handles logout.
type Service struct {
	store Store
	now   func() time.Time
	ttl   time.Duration
}

// NewService wires defaults: real wall clock, DefaultTTL.
func NewService(store Store) *Service {
	return &Service{store: store, now: time.Now, ttl: DefaultTTL}
}

// IssueInput is the contract for Issue. Callers (the login handler)
// pass everything needed to mint a session row — the user has already
// been authenticated by user.Service.Authenticate.
type IssueInput struct {
	UserID    string
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
		UserID:     in.UserID,
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

// RevokeAllForUser revokes every active session belonging to a user.
// The password-reset flow calls this after the new password is set so
// a session minted from a stolen cookie can't outlive the credential
// change. Idempotent — no active sessions is a no-op.
func (s *Service) RevokeAllForUser(ctx context.Context, userID string) error {
	if userID == "" {
		return nil
	}
	return s.store.RevokeAllForUser(ctx, userID)
}

// SetLivemode flips the active mode (test/live) on the cookie session.
// Same operator switches between modes without re-authenticating; every
// downstream request inherits the new mode via session.Resolve.
func (s *Service) SetLivemode(ctx context.Context, rawID string, livemode bool) error {
	if rawID == "" {
		return ErrNotFound
	}
	return s.store.UpdateLivemode(ctx, HashID(rawID), livemode)
}
