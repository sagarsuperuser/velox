package user

import (
	"errors"
	"time"
)

// User is the minimum shape the auth and session flows need.
type User struct {
	ID              string
	Email           string
	DisplayName     string
	Status          string
	PasswordHash    string
	EmailVerifiedAt *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Membership links a user to a tenant with a role. A single user can hold
// memberships in more than one tenant (invites); the bootstrap path
// creates one owner row.
type Membership struct {
	UserID    string
	TenantID  string
	Role      string
	CreatedAt time.Time
}

// Member is a denormalised row for the tenant's members list — joins the
// membership with the user so the handler does not have to do a second pass.
type Member struct {
	UserID      string
	Email       string
	DisplayName string
	Role        string
	JoinedAt    time.Time
}

// Invitation is a pending or past invite for a teammate to join a tenant.
// Email is stored lowercased; the raw token is only ever returned from
// CreateInvitation so the caller can email it.
type Invitation struct {
	ID              string
	TenantID        string
	Email           string
	InvitedByUserID string
	InvitedByEmail  string // hydrated by ListInvitationsForTenant for display
	Role            string
	ExpiresAt       time.Time
	AcceptedAt      *time.Time
	RevokedAt       *time.Time
	CreatedAt       time.Time
}

// Status derives a human label from the timestamps. Pending is the only
// state that accept-invite will act on; the rest are audit history.
func (i Invitation) Status(now time.Time) string {
	switch {
	case i.AcceptedAt != nil:
		return "accepted"
	case i.RevokedAt != nil:
		return "revoked"
	case !now.Before(i.ExpiresAt):
		return "expired"
	default:
		return "pending"
	}
}

var (
	ErrNotFound           = errors.New("user: not found")
	ErrEmailTaken         = errors.New("user: email already registered")
	ErrNoPassword         = errors.New("user: account has no password set")
	ErrInvalidPassword    = errors.New("user: invalid password")
	ErrDisabled           = errors.New("user: account disabled")
	ErrResetInvalid       = errors.New("user: reset token invalid or expired")
	ErrResetAlreadyUsed   = errors.New("user: reset token already consumed")
	ErrMembershipMissing  = errors.New("user: no tenant membership")
	ErrInvitationInvalid  = errors.New("user: invitation invalid or expired")
	ErrInvitationConsumed = errors.New("user: invitation already used or revoked")
	ErrAlreadyMember      = errors.New("user: email is already a member of this tenant")
	ErrPendingInvite      = errors.New("user: a pending invitation already exists for this email")
	ErrLastOwner          = errors.New("user: cannot remove the last owner of a tenant")
	ErrSelfRemoval        = errors.New("user: cannot remove yourself — transfer ownership first")
)

const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"

	StatusActive   = "active"
	StatusDisabled = "disabled"
)
