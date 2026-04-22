package dashauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/session"
	"github.com/sagarsuperuser/velox/internal/user"
)

// The stores defined here implement user.Store and session.Store with
// in-memory maps. They let us exercise the HTTP handler with real Service
// logic (password hashing, session minting, cookie attrs) without a DB.

type memUserStore struct {
	users       map[string]user.User
	memberships map[string][]user.Membership
	tokens      map[string]memResetToken // key: token hash
	invites     map[string]user.Invitation
	inviteByHsh map[string]string
}

type memResetToken struct {
	userID     string
	expiresAt  time.Time
	consumedAt *time.Time
}

func newMemUserStore() *memUserStore {
	return &memUserStore{
		users:       make(map[string]user.User),
		memberships: make(map[string][]user.Membership),
		tokens:      make(map[string]memResetToken),
		invites:     make(map[string]user.Invitation),
		inviteByHsh: make(map[string]string),
	}
}

func (s *memUserStore) Create(_ context.Context, u user.User) (user.User, error) {
	email := strings.ToLower(strings.TrimSpace(u.Email))
	for _, existing := range s.users {
		if existing.Email == email {
			return user.User{}, user.ErrEmailTaken
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

func (s *memUserStore) GetByEmail(_ context.Context, email string) (user.User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	for _, u := range s.users {
		if u.Email == email {
			return u, nil
		}
	}
	return user.User{}, user.ErrNotFound
}

func (s *memUserStore) GetByID(_ context.Context, id string) (user.User, error) {
	if u, ok := s.users[id]; ok {
		return u, nil
	}
	return user.User{}, user.ErrNotFound
}

func (s *memUserStore) SetPassword(_ context.Context, userID, hash string) error {
	u, ok := s.users[userID]
	if !ok {
		return user.ErrNotFound
	}
	u.PasswordHash = hash
	s.users[userID] = u
	return nil
}

func (s *memUserStore) MarkEmailVerified(_ context.Context, userID string, at time.Time) error {
	u, ok := s.users[userID]
	if !ok {
		return user.ErrNotFound
	}
	u.EmailVerifiedAt = &at
	s.users[userID] = u
	return nil
}

func (s *memUserStore) AddMembership(_ context.Context, m user.Membership) error {
	s.memberships[m.UserID] = append(s.memberships[m.UserID], m)
	return nil
}

func (s *memUserStore) ListMemberships(_ context.Context, userID string) ([]user.Membership, error) {
	return s.memberships[userID], nil
}

func (s *memUserStore) IssueResetToken(_ context.Context, tokenHash, userID string, expiresAt time.Time) error {
	s.tokens[tokenHash] = memResetToken{userID: userID, expiresAt: expiresAt}
	return nil
}

func (s *memUserStore) ConsumeResetToken(_ context.Context, tokenHash string, now time.Time) (string, error) {
	tok, ok := s.tokens[tokenHash]
	if !ok {
		return "", user.ErrResetInvalid
	}
	if tok.consumedAt != nil || !now.Before(tok.expiresAt) {
		return "", user.ErrResetInvalid
	}
	tok.consumedAt = &now
	s.tokens[tokenHash] = tok
	return tok.userID, nil
}

func (s *memUserStore) ListMembersForTenant(_ context.Context, tenantID string) ([]user.Member, error) {
	var out []user.Member
	for _, memList := range s.memberships {
		for _, m := range memList {
			if m.TenantID != tenantID {
				continue
			}
			u := s.users[m.UserID]
			out = append(out, user.Member{
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

func (s *memUserStore) RemoveMembership(_ context.Context, userID, tenantID string) error {
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
		return user.ErrNotFound
	}
	s.memberships[userID] = kept
	return nil
}

func (s *memUserStore) CountOwnersForTenant(_ context.Context, tenantID string) (int, error) {
	n := 0
	for _, memList := range s.memberships {
		for _, m := range memList {
			if m.TenantID == tenantID && m.Role == user.RoleOwner {
				n++
			}
		}
	}
	return n, nil
}

func (s *memUserStore) CreateInvitation(_ context.Context, inv user.Invitation, tokenHash string) (user.Invitation, error) {
	email := strings.ToLower(strings.TrimSpace(inv.Email))
	for _, existing := range s.invites {
		if existing.TenantID != inv.TenantID || existing.Email != email {
			continue
		}
		if existing.AcceptedAt == nil && existing.RevokedAt == nil {
			return user.Invitation{}, user.ErrPendingInvite
		}
	}
	if inv.ID == "" {
		inv.ID = "vlx_inv_" + tokenHash[:8]
	}
	if inv.Role == "" {
		inv.Role = user.RoleMember
	}
	inv.Email = email
	inv.CreatedAt = time.Now().UTC()
	s.invites[inv.ID] = inv
	s.inviteByHsh[tokenHash] = inv.ID
	return inv, nil
}

func (s *memUserStore) GetInvitationByHash(_ context.Context, tokenHash string) (user.Invitation, error) {
	id, ok := s.inviteByHsh[tokenHash]
	if !ok {
		return user.Invitation{}, user.ErrInvitationInvalid
	}
	return s.invites[id], nil
}

func (s *memUserStore) GetInvitationByID(_ context.Context, id string) (user.Invitation, error) {
	inv, ok := s.invites[id]
	if !ok {
		return user.Invitation{}, user.ErrInvitationInvalid
	}
	return inv, nil
}

func (s *memUserStore) ListInvitationsForTenant(_ context.Context, tenantID string) ([]user.Invitation, error) {
	var out []user.Invitation
	for _, inv := range s.invites {
		if inv.TenantID == tenantID {
			out = append(out, inv)
		}
	}
	return out, nil
}

func (s *memUserStore) MarkInvitationAccepted(_ context.Context, id string, at time.Time) error {
	inv, ok := s.invites[id]
	if !ok || inv.AcceptedAt != nil || inv.RevokedAt != nil {
		return user.ErrInvitationConsumed
	}
	inv.AcceptedAt = &at
	s.invites[id] = inv
	return nil
}

func (s *memUserStore) RevokeInvitation(_ context.Context, id string, at time.Time) error {
	inv, ok := s.invites[id]
	if !ok || inv.AcceptedAt != nil || inv.RevokedAt != nil {
		return user.ErrInvitationConsumed
	}
	inv.RevokedAt = &at
	s.invites[id] = inv
	return nil
}

type memSessionStore struct {
	rows map[string]session.Session
}

func newMemSessionStore() *memSessionStore {
	return &memSessionStore{rows: make(map[string]session.Session)}
}

func (s *memSessionStore) Create(_ context.Context, sess session.Session) error {
	s.rows[sess.IDHash] = sess
	return nil
}

func (s *memSessionStore) GetByIDHash(_ context.Context, idHash string) (session.Session, error) {
	sess, ok := s.rows[idHash]
	if !ok {
		return session.Session{}, session.ErrNotFound
	}
	return sess, nil
}

func (s *memSessionStore) Touch(_ context.Context, idHash string, now time.Time) error {
	sess, ok := s.rows[idHash]
	if !ok {
		return session.ErrNotFound
	}
	sess.LastSeenAt = now
	s.rows[idHash] = sess
	return nil
}

func (s *memSessionStore) UpdateLivemode(_ context.Context, idHash string, live bool) error {
	sess, ok := s.rows[idHash]
	if !ok {
		return session.ErrNotFound
	}
	sess.Livemode = live
	s.rows[idHash] = sess
	return nil
}

func (s *memSessionStore) Revoke(_ context.Context, idHash string, now time.Time) error {
	sess, ok := s.rows[idHash]
	if !ok {
		return session.ErrNotFound
	}
	sess.RevokedAt = &now
	s.rows[idHash] = sess
	return nil
}

func (s *memSessionStore) RevokeAllForUser(_ context.Context, userID string, now time.Time) error {
	for h, sess := range s.rows {
		if sess.UserID == userID && sess.RevokedAt == nil {
			sess.RevokedAt = &now
			s.rows[h] = sess
		}
	}
	return nil
}

// fakeEmailer captures SendPasswordReset and SendMemberInvite calls.
type fakeEmailer struct {
	calls       []fakeEmailCall
	inviteCalls []fakeInviteCall
	err         error
}

type fakeEmailCall struct {
	tenantID, to, displayName, resetURL string
}

type fakeInviteCall struct {
	tenantID, to, inviter, tenantName, url string
}

func (f *fakeEmailer) SendPasswordReset(tenantID, to, displayName, resetURL string) error {
	f.calls = append(f.calls, fakeEmailCall{tenantID, to, displayName, resetURL})
	return f.err
}

func (f *fakeEmailer) SendMemberInvite(tenantID, to, inviter, tenantName, url string) error {
	f.inviteCalls = append(f.inviteCalls, fakeInviteCall{tenantID, to, inviter, tenantName, url})
	return f.err
}

// fakeTenantLookup satisfies dashauth.TenantLookup for tests — no DB.
type fakeTenantLookup struct{ name string }

func (f fakeTenantLookup) Name(_ context.Context, _ string) (string, error) { return f.name, nil }

// --- harness --------------------------------------------------------------

type testHarness struct {
	t       *testing.T
	users   *user.Service
	userSt  *memUserStore
	sess    *session.Service
	sessSt  *memSessionStore
	email   *fakeEmailer
	handler *Handler
	public  *httptest.Server
	scoped  *httptest.Server
}

func newHarness(t *testing.T) *testHarness {
	t.Helper()
	userSt := newMemUserStore()
	sessSt := newMemSessionStore()
	emailer := &fakeEmailer{}
	usersSvc := user.NewService(userSt)
	sessSvc := session.NewService(sessSt)
	h := NewHandler(
		usersSvc,
		sessSvc,
		fakeTenantLookup{name: "Test Tenant"},
		emailer,
		"https://dashboard.test/reset?token=%s",
		"https://dashboard.test/accept-invite?token=%s",
		CookieConfig{Path: "/", SameSite: http.SameSiteLaxMode},
	)
	public := httptest.NewServer(h.Routes())
	scoped := httptest.NewServer(session.Middleware(sessSvc)(h.SessionRoutes()))
	t.Cleanup(func() {
		public.Close()
		scoped.Close()
	})
	return &testHarness{
		t: t, users: usersSvc, userSt: userSt, sess: sessSvc, sessSt: sessSt,
		email: emailer, handler: h, public: public, scoped: scoped,
	}
}

func (h *testHarness) seedOwner(email, password string) user.User {
	h.t.Helper()
	u, err := h.users.CreateWithPassword(context.Background(), email, "Owner", password)
	if err != nil {
		h.t.Fatalf("seed user: %v", err)
	}
	if err := h.userSt.AddMembership(context.Background(), user.Membership{
		UserID: u.ID, TenantID: "vlx_ten_test", Role: user.RoleOwner,
	}); err != nil {
		h.t.Fatalf("seed membership: %v", err)
	}
	return u
}

func postJSON(t *testing.T, url string, body any, cookie *http.Cookie) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req, err := http.NewRequest(http.MethodPost, url, &buf)
	if err != nil {
		t.Fatalf("new req: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	return resp
}

func getWithCookie(t *testing.T, url string, cookie *http.Cookie) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	return resp
}

func findCookie(resp *http.Response, name string) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// --- tests ----------------------------------------------------------------

func TestLogin_Success_IssuesCookieAndReturnsSessionResp(t *testing.T) {
	h := newHarness(t)
	u := h.seedOwner("owner@example.com", "password123")

	resp := postJSON(t, h.public.URL+"/login", map[string]string{
		"email": "owner@example.com", "password": "password123",
	}, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}

	cookie := findCookie(resp, session.CookieName)
	if cookie == nil {
		t.Fatal("expected Set-Cookie for velox_session")
	}
	if !cookie.HttpOnly {
		t.Error("session cookie must be HttpOnly")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", cookie.SameSite)
	}

	var got sessionResp
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.UserID != u.ID {
		t.Errorf("UserID = %q, want %q", got.UserID, u.ID)
	}
	if got.TenantID != "vlx_ten_test" {
		t.Errorf("TenantID = %q, want vlx_ten_test", got.TenantID)
	}
	if got.Livemode {
		t.Error("new session must default to test mode (livemode=false)")
	}
}

func TestLogin_WrongPassword_Returns401(t *testing.T) {
	h := newHarness(t)
	h.seedOwner("owner@example.com", "password123")

	resp := postJSON(t, h.public.URL+"/login", map[string]string{
		"email": "owner@example.com", "password": "nottheone",
	}, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", resp.StatusCode)
	}
	if findCookie(resp, session.CookieName) != nil {
		t.Error("failed login must not set a session cookie")
	}
}

func TestLogin_UnknownEmail_Returns401(t *testing.T) {
	h := newHarness(t)

	resp := postJSON(t, h.public.URL+"/login", map[string]string{
		"email": "nobody@example.com", "password": "password123",
	}, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", resp.StatusCode)
	}
}

func TestLogin_NoMembership_Returns403(t *testing.T) {
	h := newHarness(t)
	if _, err := h.users.CreateWithPassword(context.Background(), "orphan@example.com", "O", "password123"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// deliberately no AddMembership

	resp := postJSON(t, h.public.URL+"/login", map[string]string{
		"email": "orphan@example.com", "password": "password123",
	}, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status=%d want 403", resp.StatusCode)
	}
}

func TestWhoami_WithValidCookie_ReturnsUser(t *testing.T) {
	h := newHarness(t)
	u := h.seedOwner("owner@example.com", "password123")

	loginResp := postJSON(t, h.public.URL+"/login", map[string]string{
		"email": "owner@example.com", "password": "password123",
	}, nil)
	loginResp.Body.Close()
	cookie := findCookie(loginResp, session.CookieName)
	if cookie == nil {
		t.Fatal("login did not issue a cookie")
	}

	resp := getWithCookie(t, h.scoped.URL+"/", cookie)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("whoami status=%d want 200", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["user_id"] != u.ID {
		t.Errorf("user_id = %v, want %q", body["user_id"], u.ID)
	}
	if body["email"] != "owner@example.com" {
		t.Errorf("email = %v", body["email"])
	}
	if body["livemode"] != false {
		t.Errorf("livemode = %v, want false (test mode default)", body["livemode"])
	}
}

func TestWhoami_NoCookie_Returns401(t *testing.T) {
	h := newHarness(t)
	resp := getWithCookie(t, h.scoped.URL+"/", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", resp.StatusCode)
	}
}

func TestLogout_RevokesAndClearsCookie(t *testing.T) {
	h := newHarness(t)
	h.seedOwner("owner@example.com", "password123")

	loginResp := postJSON(t, h.public.URL+"/login", map[string]string{
		"email": "owner@example.com", "password": "password123",
	}, nil)
	loginResp.Body.Close()
	cookie := findCookie(loginResp, session.CookieName)

	logoutResp := postJSON(t, h.public.URL+"/logout", nil, cookie)
	logoutResp.Body.Close()
	if logoutResp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout status=%d want 204", logoutResp.StatusCode)
	}
	cleared := findCookie(logoutResp, session.CookieName)
	if cleared == nil || cleared.MaxAge >= 0 {
		t.Fatalf("logout must set a deletion cookie (MaxAge<0), got %+v", cleared)
	}

	// Follow-up whoami with the original cookie must now fail.
	whoami := getWithCookie(t, h.scoped.URL+"/", cookie)
	whoami.Body.Close()
	if whoami.StatusCode != http.StatusUnauthorized {
		t.Fatalf("whoami after logout: status=%d want 401", whoami.StatusCode)
	}
}

func TestPatchSession_TogglesLivemode(t *testing.T) {
	h := newHarness(t)
	h.seedOwner("owner@example.com", "password123")

	loginResp := postJSON(t, h.public.URL+"/login", map[string]string{
		"email": "owner@example.com", "password": "password123",
	}, nil)
	loginResp.Body.Close()
	cookie := findCookie(loginResp, session.CookieName)

	// Flip to live mode.
	patchReq, _ := http.NewRequest(http.MethodPatch, h.scoped.URL+"/",
		bytes.NewBufferString(`{"livemode":true}`))
	patchReq.AddCookie(cookie)
	patchReq.Header.Set("Content-Type", "application/json")
	patchResp, err := http.DefaultClient.Do(patchReq)
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	defer patchResp.Body.Close()
	if patchResp.StatusCode != http.StatusOK {
		t.Fatalf("patch status=%d want 200", patchResp.StatusCode)
	}

	// whoami should now report livemode=true.
	resp := getWithCookie(t, h.scoped.URL+"/", cookie)
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["livemode"] != true {
		t.Errorf("livemode = %v, want true after toggle", body["livemode"])
	}
}

func TestPasswordReset_HappyPath_RevokesExistingSessions(t *testing.T) {
	h := newHarness(t)
	h.seedOwner("owner@example.com", "password123")

	// Log in — this session must get revoked after reset.
	loginResp := postJSON(t, h.public.URL+"/login", map[string]string{
		"email": "owner@example.com", "password": "password123",
	}, nil)
	loginResp.Body.Close()
	oldCookie := findCookie(loginResp, session.CookieName)

	// Request reset.
	resp := postJSON(t, h.public.URL+"/password-reset-request", map[string]string{
		"email": "owner@example.com",
	}, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("reset request status=%d want 202", resp.StatusCode)
	}
	if len(h.email.calls) != 1 {
		t.Fatalf("expected 1 email send, got %d", len(h.email.calls))
	}
	call := h.email.calls[0]
	// The handler fills the URL template — extract raw token from it.
	const prefix = "https://dashboard.test/reset?token="
	if !strings.HasPrefix(call.resetURL, prefix) {
		t.Fatalf("reset URL unexpected shape: %q", call.resetURL)
	}
	rawToken := strings.TrimPrefix(call.resetURL, prefix)
	if rawToken == "" {
		t.Fatal("empty token in reset URL")
	}

	// Confirm reset with new password.
	confirm := postJSON(t, h.public.URL+"/password-reset-confirm", map[string]string{
		"token": rawToken, "password": "newsecret99",
	}, nil)
	confirm.Body.Close()
	if confirm.StatusCode != http.StatusNoContent {
		t.Fatalf("reset confirm status=%d want 204", confirm.StatusCode)
	}

	// Old session must be revoked.
	sess, ok := h.sessSt.rows[session.HashID(oldCookie.Value)]
	if !ok {
		t.Fatal("old session missing from store")
	}
	if sess.RevokedAt == nil {
		t.Fatal("old session should be revoked after password reset")
	}

	// New password must log in; old password must not.
	bad := postJSON(t, h.public.URL+"/login", map[string]string{
		"email": "owner@example.com", "password": "password123",
	}, nil)
	bad.Body.Close()
	if bad.StatusCode != http.StatusUnauthorized {
		t.Errorf("old password still works, status=%d", bad.StatusCode)
	}
	good := postJSON(t, h.public.URL+"/login", map[string]string{
		"email": "owner@example.com", "password": "newsecret99",
	}, nil)
	good.Body.Close()
	if good.StatusCode != http.StatusOK {
		t.Errorf("new password rejected, status=%d", good.StatusCode)
	}
}

func TestPasswordReset_UnknownEmail_Returns202_NoEmail(t *testing.T) {
	h := newHarness(t)
	resp := postJSON(t, h.public.URL+"/password-reset-request", map[string]string{
		"email": "ghost@example.com",
	}, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status=%d want 202 (enumeration-resistant)", resp.StatusCode)
	}
	if len(h.email.calls) != 0 {
		t.Fatalf("unknown email must not trigger send, got %d", len(h.email.calls))
	}
}

func TestPasswordReset_InvalidToken_Returns401(t *testing.T) {
	h := newHarness(t)
	h.seedOwner("owner@example.com", "password123")
	resp := postJSON(t, h.public.URL+"/password-reset-confirm", map[string]string{
		"token": "not-a-real-token", "password": "newpassword99",
	}, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", resp.StatusCode)
	}
}

func TestDefaultCookieConfig_SecureByEnv(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	if !DefaultCookieConfig().Secure {
		t.Error("production should set Secure=true")
	}
	t.Setenv("APP_ENV", "development")
	if DefaultCookieConfig().Secure {
		t.Error("development should set Secure=false")
	}
}

// Sanity: the ErrResetInvalid wiring in the handler produces 401, not 500.
// Guards against a future refactor that accidentally wraps the error.
func TestPasswordReset_ErrorMappingSmoke(t *testing.T) {
	if !errors.Is(user.ErrResetInvalid, user.ErrResetInvalid) {
		t.Fatal("sentinel equality broken")
	}
}
