package user

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestService_Invite_CreatesPendingRow(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	inviter, _ := svc.CreateWithPassword(context.Background(), "owner@acme.com", "Owner", "ownerpass1")
	_ = store.AddMembership(context.Background(), Membership{UserID: inviter.ID, TenantID: "ten_1", Role: RoleOwner})

	inv, raw, err := svc.Invite(context.Background(), "ten_1", inviter.ID, "new@acme.com")
	if err != nil {
		t.Fatalf("invite: %v", err)
	}
	if inv.Email != "new@acme.com" {
		t.Fatalf("email not normalised: %q", inv.Email)
	}
	if raw == "" {
		t.Fatal("raw token not returned")
	}
	// Raw token must not equal the stored hash — hashing is the whole point.
	if store.inviteByHsh[raw] != "" {
		t.Fatal("raw token stored in hash index — must be sha256(raw)")
	}
	// Preview with the raw token should find the invitation.
	got, err := svc.PreviewInvitation(context.Background(), raw)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if got.ID != inv.ID {
		t.Fatalf("preview id mismatch: got %q want %q", got.ID, inv.ID)
	}
}

func TestService_Invite_RejectsDuplicatePending(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	inviter, _ := svc.CreateWithPassword(context.Background(), "o@x.com", "O", "ownerpass1")
	_ = store.AddMembership(context.Background(), Membership{UserID: inviter.ID, TenantID: "ten_1", Role: RoleOwner})

	if _, _, err := svc.Invite(context.Background(), "ten_1", inviter.ID, "dup@x.com"); err != nil {
		t.Fatalf("first invite: %v", err)
	}
	_, _, err := svc.Invite(context.Background(), "ten_1", inviter.ID, "dup@x.com")
	if !errors.Is(err, ErrPendingInvite) {
		t.Fatalf("second invite must surface ErrPendingInvite, got %v", err)
	}
}

func TestService_Invite_RejectsExistingMember(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	inviter, _ := svc.CreateWithPassword(context.Background(), "o@x.com", "O", "ownerpass1")
	_ = store.AddMembership(context.Background(), Membership{UserID: inviter.ID, TenantID: "ten_1", Role: RoleOwner})

	// Inviting the owner's own email must fail — they're already a member.
	_, _, err := svc.Invite(context.Background(), "ten_1", inviter.ID, "o@x.com")
	if !errors.Is(err, ErrAlreadyMember) {
		t.Fatalf("expected ErrAlreadyMember, got %v", err)
	}
}

func TestService_Accept_NewUserCreatesAccount(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	inviter, _ := svc.CreateWithPassword(context.Background(), "o@x.com", "O", "ownerpass1")
	_ = store.AddMembership(context.Background(), Membership{UserID: inviter.ID, TenantID: "ten_1", Role: RoleOwner})
	_, raw, _ := svc.Invite(context.Background(), "ten_1", inviter.ID, "fresh@acme.com")

	userID, tenantID, err := svc.AcceptInvitation(context.Background(), raw, "newuserpass1", "Fresh")
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if tenantID != "ten_1" {
		t.Fatalf("tenant mismatch: %q", tenantID)
	}
	if _, err := svc.GetByID(context.Background(), userID); err != nil {
		t.Fatalf("user not persisted: %v", err)
	}
	// The new user should be able to authenticate with the chosen password.
	if _, err := svc.Authenticate(context.Background(), "fresh@acme.com", "newuserpass1"); err != nil {
		t.Fatalf("new user cannot authenticate: %v", err)
	}
}

func TestService_Accept_ReplayFails(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	inviter, _ := svc.CreateWithPassword(context.Background(), "o@x.com", "O", "ownerpass1")
	_ = store.AddMembership(context.Background(), Membership{UserID: inviter.ID, TenantID: "ten_1", Role: RoleOwner})
	_, raw, _ := svc.Invite(context.Background(), "ten_1", inviter.ID, "once@x.com")

	if _, _, err := svc.AcceptInvitation(context.Background(), raw, "passingpass", ""); err != nil {
		t.Fatalf("first accept: %v", err)
	}
	_, _, err := svc.AcceptInvitation(context.Background(), raw, "passingpass", "")
	if !errors.Is(err, ErrInvitationConsumed) {
		t.Fatalf("replay must surface ErrInvitationConsumed, got %v", err)
	}
}

func TestService_Accept_ExistingUserRequiresPassword(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	inviter, _ := svc.CreateWithPassword(context.Background(), "o@x.com", "O", "ownerpass1")
	_ = store.AddMembership(context.Background(), Membership{UserID: inviter.ID, TenantID: "ten_1", Role: RoleOwner})

	existing, _ := svc.CreateWithPassword(context.Background(), "dave@x.com", "Dave", "davespassword")
	_ = store.AddMembership(context.Background(), Membership{UserID: existing.ID, TenantID: "ten_other", Role: RoleOwner})
	_, raw, _ := svc.Invite(context.Background(), "ten_1", inviter.ID, "dave@x.com")

	// Wrong password must NOT add the membership.
	_, _, err := svc.AcceptInvitation(context.Background(), raw, "wrongguess", "")
	if !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("wrong password must surface ErrInvalidPassword, got %v", err)
	}

	// Correct password adds the membership and consumes the invite.
	userID, tenantID, err := svc.AcceptInvitation(context.Background(), raw, "davespassword", "")
	if err != nil {
		t.Fatalf("accept with valid password: %v", err)
	}
	if userID != existing.ID || tenantID != "ten_1" {
		t.Fatalf("user or tenant mismatch: user=%q tenant=%q", userID, tenantID)
	}
	mems, _ := store.ListMemberships(context.Background(), existing.ID)
	var found bool
	for _, m := range mems {
		if m.TenantID == "ten_1" {
			found = true
		}
	}
	if !found {
		t.Fatal("membership not added after accept")
	}
}

func TestService_Accept_ExpiredFails(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	// Pin the service's clock to the past so the invite expires before accept.
	base := time.Now().UTC().Add(-10 * 24 * time.Hour)
	svc.now = func() time.Time { return base }

	inviter, _ := svc.CreateWithPassword(context.Background(), "o@x.com", "O", "ownerpass1")
	_ = store.AddMembership(context.Background(), Membership{UserID: inviter.ID, TenantID: "ten_1", Role: RoleOwner})
	_, raw, _ := svc.Invite(context.Background(), "ten_1", inviter.ID, "late@x.com")

	// Fast-forward past expiration.
	svc.now = func() time.Time { return base.Add(InviteTokenTTL + time.Hour) }

	_, _, err := svc.AcceptInvitation(context.Background(), raw, "somepass123", "")
	if !errors.Is(err, ErrInvitationInvalid) {
		t.Fatalf("expired invite must surface ErrInvitationInvalid, got %v", err)
	}
}

func TestService_Revoke_PreventsAccept(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	inviter, _ := svc.CreateWithPassword(context.Background(), "o@x.com", "O", "ownerpass1")
	_ = store.AddMembership(context.Background(), Membership{UserID: inviter.ID, TenantID: "ten_1", Role: RoleOwner})
	inv, raw, _ := svc.Invite(context.Background(), "ten_1", inviter.ID, "kick@x.com")

	if err := svc.RevokeInvitation(context.Background(), "ten_1", inv.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	_, _, err := svc.AcceptInvitation(context.Background(), raw, "newuserpass1", "")
	if !errors.Is(err, ErrInvitationConsumed) {
		t.Fatalf("revoked invite must surface ErrInvitationConsumed, got %v", err)
	}
}

func TestService_Revoke_TenantScoped(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	inviter, _ := svc.CreateWithPassword(context.Background(), "o@x.com", "O", "ownerpass1")
	_ = store.AddMembership(context.Background(), Membership{UserID: inviter.ID, TenantID: "ten_1", Role: RoleOwner})
	inv, _, _ := svc.Invite(context.Background(), "ten_1", inviter.ID, "cross@x.com")

	// A session scoped to ten_other must not be able to revoke ten_1's invite.
	err := svc.RevokeInvitation(context.Background(), "ten_other", inv.ID)
	if !errors.Is(err, ErrInvitationInvalid) {
		t.Fatalf("cross-tenant revoke must fail, got %v", err)
	}
}

func TestService_RemoveMember_BlocksLastOwner(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	owner, _ := svc.CreateWithPassword(context.Background(), "solo@x.com", "Solo", "ownerpass1")
	_ = store.AddMembership(context.Background(), Membership{UserID: owner.ID, TenantID: "ten_1", Role: RoleOwner})

	// A second owner removing themselves hits self-removal first, so we need
	// a second actor to attempt removal of the last owner.
	actor, _ := svc.CreateWithPassword(context.Background(), "actor@x.com", "Actor", "actorpass1")
	_ = store.AddMembership(context.Background(), Membership{UserID: actor.ID, TenantID: "ten_1", Role: RoleMember})

	err := svc.RemoveMember(context.Background(), "ten_1", actor.ID, owner.ID)
	if !errors.Is(err, ErrLastOwner) {
		t.Fatalf("removing last owner must surface ErrLastOwner, got %v", err)
	}
}

func TestService_RemoveMember_BlocksSelfRemoval(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	u, _ := svc.CreateWithPassword(context.Background(), "me@x.com", "Me", "mypass1234")
	_ = store.AddMembership(context.Background(), Membership{UserID: u.ID, TenantID: "ten_1", Role: RoleOwner})

	err := svc.RemoveMember(context.Background(), "ten_1", u.ID, u.ID)
	if !errors.Is(err, ErrSelfRemoval) {
		t.Fatalf("self removal must surface ErrSelfRemoval, got %v", err)
	}
}

func TestService_RemoveMember_Succeeds(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	owner, _ := svc.CreateWithPassword(context.Background(), "o@x.com", "O", "ownerpass1")
	_ = store.AddMembership(context.Background(), Membership{UserID: owner.ID, TenantID: "ten_1", Role: RoleOwner})
	m, _ := svc.CreateWithPassword(context.Background(), "m@x.com", "M", "memberpass1")
	_ = store.AddMembership(context.Background(), Membership{UserID: m.ID, TenantID: "ten_1", Role: RoleMember})

	if err := svc.RemoveMember(context.Background(), "ten_1", owner.ID, m.ID); err != nil {
		t.Fatalf("remove member: %v", err)
	}
	mems, _ := store.ListMemberships(context.Background(), m.ID)
	for _, mm := range mems {
		if mm.TenantID == "ten_1" {
			t.Fatal("membership still present after remove")
		}
	}
}
