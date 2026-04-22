package user

import (
	"context"
	"time"
)

// Store is what the service calls. All methods run under TxBypass: auth
// tables are not tenant-partitioned (they're what *sets* the tenant context),
// so RLS would be circular.
type Store interface {
	Create(ctx context.Context, u User) (User, error)
	GetByEmail(ctx context.Context, email string) (User, error)
	GetByID(ctx context.Context, id string) (User, error)
	SetPassword(ctx context.Context, userID, hash string) error
	MarkEmailVerified(ctx context.Context, userID string, at time.Time) error

	AddMembership(ctx context.Context, m Membership) error
	ListMemberships(ctx context.Context, userID string) ([]Membership, error)
	ListMembersForTenant(ctx context.Context, tenantID string) ([]Member, error)
	RemoveMembership(ctx context.Context, userID, tenantID string) error
	CountOwnersForTenant(ctx context.Context, tenantID string) (int, error)

	IssueResetToken(ctx context.Context, tokenHash, userID string, expiresAt time.Time) error
	ConsumeResetToken(ctx context.Context, tokenHash string, now time.Time) (string, error) // returns userID

	CreateInvitation(ctx context.Context, inv Invitation, tokenHash string) (Invitation, error)
	GetInvitationByHash(ctx context.Context, tokenHash string) (Invitation, error)
	GetInvitationByID(ctx context.Context, id string) (Invitation, error)
	ListInvitationsForTenant(ctx context.Context, tenantID string) ([]Invitation, error)
	MarkInvitationAccepted(ctx context.Context, id string, at time.Time) error
	RevokeInvitation(ctx context.Context, id string, at time.Time) error
}
