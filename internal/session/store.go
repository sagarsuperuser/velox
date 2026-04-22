package session

import (
	"context"
	"time"
)

// Store owns sessions persistence. Everything runs under TxBypass — sessions
// are what set the tenant ctx, so they can't depend on tenant RLS.
type Store interface {
	Create(ctx context.Context, s Session) error
	GetByIDHash(ctx context.Context, idHash string) (Session, error)
	Touch(ctx context.Context, idHash string, now time.Time) error
	UpdateLivemode(ctx context.Context, idHash string, livemode bool) error
	Revoke(ctx context.Context, idHash string, now time.Time) error
	RevokeAllForUser(ctx context.Context, userID string, now time.Time) error
}
