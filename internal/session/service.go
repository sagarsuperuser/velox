package session

import (
	"context"
	"fmt"
	"time"
)

type Service struct {
	store Store
	ttl   time.Duration
	now   func() time.Time
}

func NewService(store Store) *Service {
	return &Service{
		store: store,
		ttl:   DefaultTTL,
		now:   func() time.Time { return time.Now().UTC() },
	}
}

// Issue mints a fresh session for userID scoped to tenantID. Livemode
// defaults to false (test mode) — the dashboard opens in test mode on
// login, matching Stripe's convention for fresh browser sessions.
func (s *Service) Issue(ctx context.Context, userID, tenantID, userAgent, ip string) (Session, error) {
	raw, hash, err := NewID()
	if err != nil {
		return Session{}, fmt.Errorf("mint session id: %w", err)
	}
	now := s.now()
	sess := Session{
		ID:         raw,
		IDHash:     hash,
		UserID:     userID,
		TenantID:   tenantID,
		Livemode:   false,
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  now.Add(s.ttl),
		UserAgent:  userAgent,
		IP:         ip,
	}
	if err := s.store.Create(ctx, sess); err != nil {
		return Session{}, err
	}
	return sess, nil
}

// Lookup resolves a raw session ID (from the cookie) to a Session row.
// Returns ErrRevoked or ErrExpired instead of a generic not-found so
// middleware can distinguish "log back in" from "cookie never existed".
func (s *Service) Lookup(ctx context.Context, rawID string) (Session, error) {
	sess, err := s.store.GetByIDHash(ctx, HashID(rawID))
	if err != nil {
		return Session{}, err
	}
	now := s.now()
	if sess.RevokedAt != nil {
		return Session{}, ErrRevoked
	}
	if !now.Before(sess.ExpiresAt) {
		return Session{}, ErrExpired
	}
	return sess, nil
}

// Touch bumps last_seen_at. Called by middleware best-effort on each
// authenticated request; failure is logged, not returned, so a transient
// write error doesn't boot the user.
func (s *Service) Touch(ctx context.Context, idHash string) error {
	return s.store.Touch(ctx, idHash, s.now())
}

// SetLivemode flips the active view on a session. Used by PATCH /v1/session.
func (s *Service) SetLivemode(ctx context.Context, idHash string, live bool) error {
	return s.store.UpdateLivemode(ctx, idHash, live)
}

// Revoke tears down a single session (logout).
func (s *Service) Revoke(ctx context.Context, idHash string) error {
	return s.store.Revoke(ctx, idHash, s.now())
}

// RevokeAllForUser invalidates every outstanding session for a user.
// Called after a password reset so the attacker on the old password
// cookie is immediately kicked.
func (s *Service) RevokeAllForUser(ctx context.Context, userID string) error {
	return s.store.RevokeAllForUser(ctx, userID, s.now())
}
