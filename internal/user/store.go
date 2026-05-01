package user

import (
	"context"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// Store is the persistence boundary for user accounts, the user↔tenant
// join, and password reset tokens. ADR-011.
type Store interface {
	// Create inserts a user. Returns ErrEmailTaken if the email is
	// already in the table (citext unique violation).
	Create(ctx context.Context, email, passwordHash string) (domain.User, error)

	// GetByEmail loads the user with the given email (case-insensitive
	// via citext). Returns errs.ErrNotFound when no row matches.
	GetByEmail(ctx context.Context, email string) (domain.User, error)

	// GetByID loads the user by id.
	GetByID(ctx context.Context, id string) (domain.User, error)

	// TouchLastLogin updates last_login_at and clears locked_until on
	// successful login. Single statement, no read-then-write race.
	TouchLastLogin(ctx context.Context, id string, at time.Time) error

	// Lock sets locked_until to the supplied deadline. Login endpoint
	// refuses login until the deadline passes.
	Lock(ctx context.Context, id string, until time.Time) error

	// SetPassword updates the password_hash. Used by the reset flow.
	SetPassword(ctx context.Context, id, passwordHash string) error

	// AttachTenant adds (user_id, tenant_id, role) to user_tenants.
	// Idempotent on (user_id, tenant_id) primary key conflict.
	AttachTenant(ctx context.Context, userID, tenantID, role string) error

	// TenantsForUser returns the tenant memberships for a user. v1 has
	// 1:1 user:tenant; the shape supports growth.
	TenantsForUser(ctx context.Context, userID string) ([]domain.UserTenant, error)

	// CreateResetToken inserts a row whose token_hash matches the
	// caller-provided hash. Plaintext token never enters the DB.
	CreateResetToken(ctx context.Context, userID, tokenHash string, expiresAt time.Time) (domain.PasswordResetToken, error)

	// ConsumeResetToken atomically looks up the token by hash, asserts
	// it isn't used or expired, and stamps used_at. Returns the token's
	// owning user_id.
	ConsumeResetToken(ctx context.Context, tokenHash string) (string, error)
}
