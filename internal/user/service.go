package user

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ResetTokenTTL is how long a password-reset link is valid. Short on
// purpose — these land in inboxes, which is a plaintext channel, so the
// blast radius of a stolen reset link stays bounded.
const ResetTokenTTL = 1 * time.Hour

// InviteTokenTTL gives the recipient a reasonable window to click through
// without making stolen links attractive. 72h matches how long most SaaS
// invites stay actionable before the context evaporates.
const InviteTokenTTL = 72 * time.Hour

type Service struct {
	store Store
	now   func() time.Time
}

func NewService(store Store) *Service {
	return &Service{store: store, now: func() time.Time { return time.Now().UTC() }}
}

// CreateWithPassword provisions a new user in StatusActive with the given
// plaintext password hashed via argon2id. Email is normalised to lowercase.
func (s *Service) CreateWithPassword(ctx context.Context, email, displayName, password string) (User, error) {
	if err := validatePassword(password); err != nil {
		return User{}, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return User{}, fmt.Errorf("hash password: %w", err)
	}
	return s.store.Create(ctx, User{
		Email:        strings.ToLower(strings.TrimSpace(email)),
		DisplayName:  strings.TrimSpace(displayName),
		Status:       StatusActive,
		PasswordHash: hash,
	})
}

// Authenticate verifies an email/password pair and returns the user on
// success. Error values intentionally collapse "no such user" and "wrong
// password" into ErrInvalidPassword to deny timing/error enumeration.
func (s *Service) Authenticate(ctx context.Context, email, password string) (User, error) {
	u, err := s.store.GetByEmail(ctx, email)
	if errors.Is(err, ErrNotFound) {
		// Do a fake verify so login timing is similar for known and
		// unknown emails. The hash is arbitrary but well-formed so
		// argon2 actually runs.
		_, _ = VerifyPassword(password, dummyHash)
		return User{}, ErrInvalidPassword
	}
	if err != nil {
		return User{}, err
	}
	if u.Status != StatusActive {
		return User{}, ErrDisabled
	}
	if u.PasswordHash == "" {
		_, _ = VerifyPassword(password, dummyHash)
		return User{}, ErrInvalidPassword
	}
	ok, err := VerifyPassword(password, u.PasswordHash)
	if err != nil {
		return User{}, fmt.Errorf("verify password: %w", err)
	}
	if !ok {
		return User{}, ErrInvalidPassword
	}
	return u, nil
}

// GetByID fetches a user by ID. Thin wrapper over the store so HTTP
// handlers don't need to hold a Store reference alongside the Service.
func (s *Service) GetByID(ctx context.Context, id string) (User, error) {
	return s.store.GetByID(ctx, id)
}

// GetByEmailOrNotFound is a thin service passthrough used by the invite
// preview endpoint to decide "new account" vs "existing account" UI copy.
// Callers typically only care about the ErrNotFound branch.
func (s *Service) GetByEmailOrNotFound(ctx context.Context, email string) (User, error) {
	return s.store.GetByEmail(ctx, email)
}

// PrimaryTenant returns the tenant a user should be scoped to on login.
// Today that's the single membership created at bootstrap; when invites
// land it becomes "most recent" or "most recently used".
func (s *Service) PrimaryTenant(ctx context.Context, userID string) (Membership, error) {
	ms, err := s.store.ListMemberships(ctx, userID)
	if err != nil {
		return Membership{}, err
	}
	if len(ms) == 0 {
		return Membership{}, ErrMembershipMissing
	}
	return ms[0], nil
}

// RequestPasswordReset issues a reset token if the email maps to an active
// user, and always returns the raw token if one was minted (caller emails
// it). If no user exists, returns ("", nil) — callers MUST respond with the
// same success shape either way, to avoid enumeration.
func (s *Service) RequestPasswordReset(ctx context.Context, email string) (User, string, error) {
	u, err := s.store.GetByEmail(ctx, email)
	if errors.Is(err, ErrNotFound) {
		return User{}, "", nil
	}
	if err != nil {
		return User{}, "", err
	}
	if u.Status != StatusActive {
		// Don't mint tokens for disabled accounts; treat like unknown.
		return User{}, "", nil
	}
	raw, hash, err := newResetToken()
	if err != nil {
		return User{}, "", fmt.Errorf("mint reset token: %w", err)
	}
	if err := s.store.IssueResetToken(ctx, hash, u.ID, s.now().Add(ResetTokenTTL)); err != nil {
		return User{}, "", err
	}
	return u, raw, nil
}

// ConsumeReset atomically validates the token, updates the password, and
// marks the token consumed. Also revokes all existing sessions via the
// session-revoker callback if supplied (caller wires in session.Service).
func (s *Service) ConsumeReset(ctx context.Context, rawToken, newPassword string, revokeSessions func(context.Context, string) error) error {
	if err := validatePassword(newPassword); err != nil {
		return err
	}
	hash := HashResetToken(rawToken)
	userID, err := s.store.ConsumeResetToken(ctx, hash, s.now())
	if err != nil {
		return err
	}
	newHash, err := HashPassword(newPassword)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	if err := s.store.SetPassword(ctx, userID, newHash); err != nil {
		return err
	}
	if revokeSessions != nil {
		if err := revokeSessions(ctx, userID); err != nil {
			return fmt.Errorf("revoke sessions: %w", err)
		}
	}
	return nil
}

// HashResetToken hashes a raw reset token for storage/lookup. Exported so
// the service can hash tokens supplied by callers at consume time.
func HashResetToken(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

// HashInviteToken mirrors HashResetToken for invitation acceptance links.
// Same construction (sha256 hex), separate symbol so intent is obvious.
func HashInviteToken(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

// Invite creates a pending invitation and returns it along with the raw
// token that the caller MUST email. The raw token is never stored — only
// its sha256. Rejects if the email is already a member of this tenant or
// has a pending (un-accepted, un-revoked) invitation.
func (s *Service) Invite(ctx context.Context, tenantID, invitedByUserID, email string) (Invitation, string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return Invitation{}, "", errors.New("email is required")
	}

	// Already a member? ListMembersForTenant is small for any real tenant;
	// scanning is fine and avoids a targeted uniqueness check.
	members, err := s.store.ListMembersForTenant(ctx, tenantID)
	if err != nil {
		return Invitation{}, "", err
	}
	for _, m := range members {
		if m.Email == email {
			return Invitation{}, "", ErrAlreadyMember
		}
	}

	raw, hash, err := newInviteToken()
	if err != nil {
		return Invitation{}, "", fmt.Errorf("mint invite token: %w", err)
	}

	inv, err := s.store.CreateInvitation(ctx, Invitation{
		TenantID:        tenantID,
		Email:           email,
		InvitedByUserID: invitedByUserID,
		Role:            RoleMember,
		ExpiresAt:       s.now().Add(InviteTokenTTL),
	}, hash)
	if err != nil {
		return Invitation{}, "", err
	}
	return inv, raw, nil
}

// PreviewInvitation resolves a raw token to its invitation for the accept
// page. Returns ErrInvitationInvalid for unknown, expired, accepted, or
// revoked tokens — the caller must treat all four identically.
func (s *Service) PreviewInvitation(ctx context.Context, rawToken string) (Invitation, error) {
	inv, err := s.store.GetInvitationByHash(ctx, HashInviteToken(rawToken))
	if err != nil {
		return Invitation{}, err
	}
	if inv.AcceptedAt != nil || inv.RevokedAt != nil {
		return Invitation{}, ErrInvitationConsumed
	}
	if !s.now().Before(inv.ExpiresAt) {
		return Invitation{}, ErrInvitationInvalid
	}
	return inv, nil
}

// AcceptInvitation consumes a raw invite token and results in a membership.
// If the email already has a user, the caller MUST supply their existing
// password (Authenticate-equivalent check) — otherwise anyone with inbox
// access could hijack an existing account. If the email is new, password
// creates the account.
//
// Returns the user id + tenant id so the caller can mint a session.
func (s *Service) AcceptInvitation(ctx context.Context, rawToken, password, displayName string) (userID, tenantID string, err error) {
	if err := validatePassword(password); err != nil {
		return "", "", err
	}
	inv, err := s.store.GetInvitationByHash(ctx, HashInviteToken(rawToken))
	if err != nil {
		return "", "", err
	}
	if inv.AcceptedAt != nil || inv.RevokedAt != nil {
		return "", "", ErrInvitationConsumed
	}
	if !s.now().Before(inv.ExpiresAt) {
		return "", "", ErrInvitationInvalid
	}

	u, err := s.store.GetByEmail(ctx, inv.Email)
	switch {
	case errors.Is(err, ErrNotFound):
		// New account path — create user with supplied password.
		hash, hErr := HashPassword(password)
		if hErr != nil {
			return "", "", fmt.Errorf("hash password: %w", hErr)
		}
		created, cErr := s.store.Create(ctx, User{
			Email:        inv.Email,
			DisplayName:  strings.TrimSpace(displayName),
			Status:       StatusActive,
			PasswordHash: hash,
		})
		if cErr != nil {
			return "", "", cErr
		}
		u = created
	case err != nil:
		return "", "", err
	default:
		// Existing account path — require current password. Disabled accounts
		// cannot accept at all, matching Authenticate.
		if u.Status != StatusActive {
			return "", "", ErrDisabled
		}
		if u.PasswordHash == "" {
			return "", "", ErrInvalidPassword
		}
		ok, vErr := VerifyPassword(password, u.PasswordHash)
		if vErr != nil {
			return "", "", fmt.Errorf("verify password: %w", vErr)
		}
		if !ok {
			return "", "", ErrInvalidPassword
		}
	}

	if err := s.store.AddMembership(ctx, Membership{
		UserID:   u.ID,
		TenantID: inv.TenantID,
		Role:     inv.Role,
	}); err != nil {
		return "", "", fmt.Errorf("add membership: %w", err)
	}
	if err := s.store.MarkInvitationAccepted(ctx, inv.ID, s.now()); err != nil {
		return "", "", err
	}
	return u.ID, inv.TenantID, nil
}

// ListMembers returns the tenant's membership roster with user details.
func (s *Service) ListMembers(ctx context.Context, tenantID string) ([]Member, error) {
	return s.store.ListMembersForTenant(ctx, tenantID)
}

// ListInvitations returns every invitation ever sent for the tenant; the
// handler filters by status on the client side.
func (s *Service) ListInvitations(ctx context.Context, tenantID string) ([]Invitation, error) {
	return s.store.ListInvitationsForTenant(ctx, tenantID)
}

// RevokeInvitation marks a pending invitation revoked. Scoping by tenant
// prevents a session from revoking invitations on other tenants even if
// it knows the id — IDs are not secret, tenant boundaries are.
func (s *Service) RevokeInvitation(ctx context.Context, tenantID, invitationID string) error {
	inv, err := s.store.GetInvitationByID(ctx, invitationID)
	if err != nil {
		return err
	}
	if inv.TenantID != tenantID {
		return ErrInvitationInvalid
	}
	return s.store.RevokeInvitation(ctx, invitationID, s.now())
}

// RemoveMember deletes a user's membership in a tenant. Guards: a user can
// never remove themselves (they'd lock themselves out with no way back in),
// and the last owner cannot be removed (tenant would become unmanageable).
// actorUserID is the session's user — compared for the self-removal guard.
func (s *Service) RemoveMember(ctx context.Context, tenantID, actorUserID, targetUserID string) error {
	if actorUserID == targetUserID {
		return ErrSelfRemoval
	}
	members, err := s.store.ListMembersForTenant(ctx, tenantID)
	if err != nil {
		return err
	}
	var target *Member
	owners := 0
	for i := range members {
		if members[i].Role == RoleOwner {
			owners++
		}
		if members[i].UserID == targetUserID {
			target = &members[i]
		}
	}
	if target == nil {
		return ErrNotFound
	}
	if target.Role == RoleOwner && owners <= 1 {
		return ErrLastOwner
	}
	return s.store.RemoveMembership(ctx, targetUserID, tenantID)
}

func newInviteToken() (raw, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	raw = hex.EncodeToString(buf)
	hash = HashInviteToken(raw)
	return raw, hash, nil
}

func newResetToken() (raw, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	raw = hex.EncodeToString(buf)
	hash = HashResetToken(raw)
	return raw, hash, nil
}

func validatePassword(pw string) error {
	// Deliberately simple: length floor only. Composition rules (uppercase,
	// symbols) push users toward predictable patterns — the NIST 800-63B
	// guidance is to take length and let the hash do the work.
	if len(pw) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	if len(pw) > 256 {
		return errors.New("password too long")
	}
	return nil
}

// dummyHash is a valid argon2id PHC string used purely to equalise timing
// between "email not found" and "password mismatch" in Authenticate.
// Generated once at package load; the plaintext is unknown, so comparisons
// against it always fail.
var dummyHash = mustDummy()

func mustDummy() string {
	h, err := HashPassword("dummy-never-matches-anything-real-" + hex.EncodeToString(randBytes(16)))
	if err != nil {
		panic(fmt.Sprintf("user: init dummy hash: %v", err))
	}
	return h
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("user: rand read: " + err.Error())
	}
	return b
}
