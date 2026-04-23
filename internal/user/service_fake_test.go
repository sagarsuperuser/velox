package user

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeStore is an in-memory Store for exercising Service logic without a DB.
type fakeStore struct {
	users       map[string]User // key: id
	memberships map[string][]Membership
	resetTokens map[string]fakeToken  // key: hash
	invites     map[string]Invitation // key: id
	inviteByHsh map[string]string     // token_hash -> invitation id
}

type fakeToken struct {
	userID     string
	expiresAt  time.Time
	consumedAt *time.Time
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		users:       make(map[string]User),
		memberships: make(map[string][]Membership),
		resetTokens: make(map[string]fakeToken),
		invites:     make(map[string]Invitation),
		inviteByHsh: make(map[string]string),
	}
}

func (s *fakeStore) Create(_ context.Context, u User) (User, error) {
	email := strings.ToLower(strings.TrimSpace(u.Email))
	for _, existing := range s.users {
		if existing.Email == email {
			return User{}, ErrEmailTaken
		}
	}
	if u.ID == "" {
		u.ID = "vlx_usr_" + email
	}
	u.Email = email
	u.CreatedAt = time.Now().UTC()
	u.UpdatedAt = u.CreatedAt
	s.users[u.ID] = u
	return u, nil
}

func (s *fakeStore) GetByEmail(_ context.Context, email string) (User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	for _, u := range s.users {
		if u.Email == email {
			return u, nil
		}
	}
	return User{}, ErrNotFound
}

func (s *fakeStore) GetByID(_ context.Context, id string) (User, error) {
	if u, ok := s.users[id]; ok {
		return u, nil
	}
	return User{}, ErrNotFound
}

func (s *fakeStore) SetPassword(_ context.Context, userID, hash string) error {
	u, ok := s.users[userID]
	if !ok {
		return ErrNotFound
	}
	u.PasswordHash = hash
	u.UpdatedAt = time.Now().UTC()
	s.users[userID] = u
	return nil
}

func (s *fakeStore) MarkEmailVerified(_ context.Context, userID string, at time.Time) error {
	u, ok := s.users[userID]
	if !ok {
		return ErrNotFound
	}
	u.EmailVerifiedAt = &at
	s.users[userID] = u
	return nil
}

func (s *fakeStore) AddMembership(_ context.Context, m Membership) error {
	s.memberships[m.UserID] = append(s.memberships[m.UserID], m)
	return nil
}

func (s *fakeStore) ListMemberships(_ context.Context, userID string) ([]Membership, error) {
	return s.memberships[userID], nil
}

func (s *fakeStore) IssueResetToken(_ context.Context, tokenHash, userID string, expiresAt time.Time) error {
	s.resetTokens[tokenHash] = fakeToken{userID: userID, expiresAt: expiresAt}
	return nil
}

func (s *fakeStore) ConsumeResetToken(_ context.Context, tokenHash string, now time.Time) (string, error) {
	tok, ok := s.resetTokens[tokenHash]
	if !ok {
		return "", ErrResetInvalid
	}
	if tok.consumedAt != nil {
		return "", ErrResetInvalid
	}
	if !now.Before(tok.expiresAt) {
		return "", ErrResetInvalid
	}
	tok.consumedAt = &now
	s.resetTokens[tokenHash] = tok
	return tok.userID, nil
}

func (s *fakeStore) ListMembersForTenant(_ context.Context, tenantID string) ([]Member, error) {
	var out []Member
	for _, memList := range s.memberships {
		for _, m := range memList {
			if m.TenantID != tenantID {
				continue
			}
			u := s.users[m.UserID]
			out = append(out, Member{
				UserID:      m.UserID,
				Email:       u.Email,
				DisplayName: u.DisplayName,
				Role:        m.Role,
				JoinedAt:    m.CreatedAt,
			})
		}
	}
	return out, nil
}

func (s *fakeStore) RemoveMembership(_ context.Context, userID, tenantID string) error {
	list := s.memberships[userID]
	kept := list[:0]
	removed := false
	for _, m := range list {
		if m.TenantID == tenantID {
			removed = true
			continue
		}
		kept = append(kept, m)
	}
	if !removed {
		return ErrNotFound
	}
	s.memberships[userID] = kept
	return nil
}

func (s *fakeStore) CountOwnersForTenant(_ context.Context, tenantID string) (int, error) {
	n := 0
	for _, memList := range s.memberships {
		for _, m := range memList {
			if m.TenantID == tenantID && m.Role == RoleOwner {
				n++
			}
		}
	}
	return n, nil
}

func (s *fakeStore) CreateInvitation(_ context.Context, inv Invitation, tokenHash string) (Invitation, error) {
	email := strings.ToLower(strings.TrimSpace(inv.Email))
	// Enforce unique pending-invite-per-tenant-per-email.
	for _, existing := range s.invites {
		if existing.TenantID != inv.TenantID || existing.Email != email {
			continue
		}
		if existing.AcceptedAt == nil && existing.RevokedAt == nil {
			return Invitation{}, ErrPendingInvite
		}
	}
	if inv.ID == "" {
		inv.ID = "vlx_inv_" + tokenHash[:8]
	}
	if inv.Role == "" {
		inv.Role = RoleMember
	}
	inv.Email = email
	inv.CreatedAt = time.Now().UTC()
	s.invites[inv.ID] = inv
	s.inviteByHsh[tokenHash] = inv.ID
	return inv, nil
}

func (s *fakeStore) GetInvitationByHash(_ context.Context, tokenHash string) (Invitation, error) {
	id, ok := s.inviteByHsh[tokenHash]
	if !ok {
		return Invitation{}, ErrInvitationInvalid
	}
	return s.invites[id], nil
}

func (s *fakeStore) GetInvitationByID(_ context.Context, id string) (Invitation, error) {
	inv, ok := s.invites[id]
	if !ok {
		return Invitation{}, ErrInvitationInvalid
	}
	return inv, nil
}

func (s *fakeStore) ListInvitationsForTenant(_ context.Context, tenantID string) ([]Invitation, error) {
	var out []Invitation
	for _, inv := range s.invites {
		if inv.TenantID == tenantID {
			out = append(out, inv)
		}
	}
	return out, nil
}

func (s *fakeStore) MarkInvitationAccepted(_ context.Context, id string, at time.Time) error {
	inv, ok := s.invites[id]
	if !ok || inv.AcceptedAt != nil || inv.RevokedAt != nil {
		return ErrInvitationConsumed
	}
	inv.AcceptedAt = &at
	s.invites[id] = inv
	return nil
}

func (s *fakeStore) RevokeInvitation(_ context.Context, id string, at time.Time) error {
	inv, ok := s.invites[id]
	if !ok || inv.AcceptedAt != nil || inv.RevokedAt != nil {
		return ErrInvitationConsumed
	}
	inv.RevokedAt = &at
	s.invites[id] = inv
	return nil
}

// --- tests ----------------------------------------------------------------

func TestService_CreateWithPassword_HashesAndLowercases(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)

	u, err := svc.CreateWithPassword(context.Background(), "  User@Example.COM  ", "Jane", "password123")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if u.Email != "user@example.com" {
		t.Fatalf("email not normalised: %q", u.Email)
	}
	if u.PasswordHash == "password123" {
		t.Fatal("password stored in plaintext — big no")
	}
	if !strings.HasPrefix(u.PasswordHash, "$argon2id$") {
		t.Fatalf("expected argon2id hash, got %q", u.PasswordHash)
	}
}

func TestService_CreateWithPassword_RejectsShortPassword(t *testing.T) {
	svc := NewService(newFakeStore())
	_, err := svc.CreateWithPassword(context.Background(), "a@b.com", "A", "short")
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestService_Authenticate_Success(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	if _, err := svc.CreateWithPassword(context.Background(), "ok@example.com", "OK", "password123"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	u, err := svc.Authenticate(context.Background(), "ok@example.com", "password123")
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if u.Email != "ok@example.com" {
		t.Fatalf("returned user mismatch: %q", u.Email)
	}
}

func TestService_Authenticate_UnknownEmailCollapsesToInvalidPassword(t *testing.T) {
	svc := NewService(newFakeStore())
	_, err := svc.Authenticate(context.Background(), "nobody@example.com", "whatever")
	if !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("unknown email must surface as ErrInvalidPassword, got %v", err)
	}
}

func TestService_Authenticate_WrongPassword(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	_, _ = svc.CreateWithPassword(context.Background(), "a@b.com", "A", "password123")
	_, err := svc.Authenticate(context.Background(), "a@b.com", "nottheone")
	if !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("got %v, want ErrInvalidPassword", err)
	}
}

func TestService_Authenticate_DisabledRejected(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	u, _ := svc.CreateWithPassword(context.Background(), "x@y.com", "X", "password123")
	u.Status = StatusDisabled
	store.users[u.ID] = u
	_, err := svc.Authenticate(context.Background(), "x@y.com", "password123")
	if !errors.Is(err, ErrDisabled) {
		t.Fatalf("got %v, want ErrDisabled", err)
	}
}

func TestService_RequestPasswordReset_UnknownEmailReturnsEmpty(t *testing.T) {
	svc := NewService(newFakeStore())
	u, raw, err := svc.RequestPasswordReset(context.Background(), "ghost@example.com")
	if err != nil {
		t.Fatalf("want no error for unknown email, got %v", err)
	}
	if raw != "" || u.ID != "" {
		t.Fatalf("unknown email must yield zero values, got user=%q raw=%q", u.ID, raw)
	}
}

func TestService_RequestPasswordReset_DisabledSilentlyNoops(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	u, _ := svc.CreateWithPassword(context.Background(), "d@x.com", "D", "password123")
	u.Status = StatusDisabled
	store.users[u.ID] = u

	got, raw, err := svc.RequestPasswordReset(context.Background(), "d@x.com")
	if err != nil {
		t.Fatalf("disabled user: unexpected error %v", err)
	}
	if raw != "" || got.ID != "" {
		t.Fatal("disabled user must not mint a reset token")
	}
}

func TestService_ConsumeReset_Success(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	u, _ := svc.CreateWithPassword(context.Background(), "c@x.com", "C", "originalpass")

	_, raw, err := svc.RequestPasswordReset(context.Background(), "c@x.com")
	if err != nil || raw == "" {
		t.Fatalf("request reset: err=%v raw=%q", err, raw)
	}

	var revoked string
	err = svc.ConsumeReset(context.Background(), raw, "newsecret123", func(_ context.Context, userID string) error {
		revoked = userID
		return nil
	})
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if revoked != u.ID {
		t.Fatalf("revokeSessions called with %q, want %q", revoked, u.ID)
	}
	// Old password must fail, new must succeed.
	if _, err := svc.Authenticate(context.Background(), "c@x.com", "originalpass"); !errors.Is(err, ErrInvalidPassword) {
		t.Fatal("old password still works after reset")
	}
	if _, err := svc.Authenticate(context.Background(), "c@x.com", "newsecret123"); err != nil {
		t.Fatalf("new password rejected: %v", err)
	}
}

func TestService_ConsumeReset_TokenReplayFails(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	_, _ = svc.CreateWithPassword(context.Background(), "r@x.com", "R", "originalpass")
	_, raw, _ := svc.RequestPasswordReset(context.Background(), "r@x.com")

	if err := svc.ConsumeReset(context.Background(), raw, "newsecret123", nil); err != nil {
		t.Fatalf("first consume: %v", err)
	}
	err := svc.ConsumeReset(context.Background(), raw, "another123!", nil)
	if !errors.Is(err, ErrResetInvalid) {
		t.Fatalf("replay must surface ErrResetInvalid, got %v", err)
	}
}

func TestService_ConsumeReset_RejectsWeakPassword(t *testing.T) {
	svc := NewService(newFakeStore())
	err := svc.ConsumeReset(context.Background(), "anytoken", "weak", nil)
	if err == nil {
		t.Fatal("weak password must be rejected before touching the store")
	}
}
