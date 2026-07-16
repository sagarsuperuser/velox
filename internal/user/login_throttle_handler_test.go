package user

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// fakeLoginGuard records Check/Record calls and can be told to Deny, so a handler
// test can assert (a) a Deny short-circuits BEFORE Authenticate (no bcrypt) and
// (b) the outcome is recorded on the guard.
type fakeLoginGuard struct {
	deny        bool
	checkCalls  []guardCall
	recordCalls []guardRecord
}

type guardCall struct{ email, ip string }
type guardRecord struct {
	email, ip string
	success   bool
}

func (f *fakeLoginGuard) Check(_ context.Context, email, ip string) Decision {
	f.checkCalls = append(f.checkCalls, guardCall{email, ip})
	return Decision{Deny: f.deny}
}

func (f *fakeLoginGuard) Record(_ context.Context, email, ip string, success bool) {
	f.recordCalls = append(f.recordCalls, guardRecord{email, ip, success})
}

// TestLogin_ThrottleDenyShortCircuitsBeforeBcrypt: when the guard denies, the
// handler returns the generic 401 WITHOUT entering Authenticate — proven by the
// store's GetByEmail (Authenticate's first step, the gate to bcrypt) never being
// called. This is the pre-bcrypt load-shed that protects login CPU under a flood.
func TestLogin_ThrottleDenyShortCircuitsBeforeBcrypt(t *testing.T) {
	store := &fakeUserStore{}
	h, _ := newAuthHandler(t, store)
	guard := &fakeLoginGuard{deny: true}
	h.SetLoginGuard(guard)

	body, _ := json.Marshal(loginReq{Email: "op@acme.com", Password: "a-sufficiently-long-pw"})
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(string(body)))
	req.RemoteAddr = "203.0.113.9:5555"
	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (throttle deny masked as generic 401)", rr.Code)
	}
	if len(guard.checkCalls) != 1 {
		t.Fatalf("guard.Check calls = %d, want 1", len(guard.checkCalls))
	}
	if store.getByEmailCalls != 0 {
		t.Errorf("Authenticate was entered despite a throttle deny (GetByEmail called %d× → bcrypt would run); Check must precede Authenticate", store.getByEmailCalls)
	}
	if len(guard.recordCalls) != 0 {
		t.Errorf("recorded an attempt that was short-circuited before the credential check: %+v", guard.recordCalls)
	}
}

// TestLogin_BadCredentials_RecordsThrottleFailure: a wrong password records a
// failure against (IP-prefix × email) so a single source hammering the account
// is throttled, and still returns the generic 401.
func TestLogin_BadCredentials_RecordsThrottleFailure(t *testing.T) {
	store := &fakeUserStore{} // loginUser nil → GetByEmail misses → ErrBadCredentials
	h, _ := newAuthHandler(t, store)
	guard := &fakeLoginGuard{}
	h.SetLoginGuard(guard)

	body, _ := json.Marshal(loginReq{Email: "op@acme.com", Password: "a-wrong-long-password"})
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(string(body)))
	req.RemoteAddr = "198.51.100.7:5555"
	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
	if len(guard.recordCalls) != 1 || guard.recordCalls[0].success {
		t.Fatalf("record calls = %+v, want exactly one failure record", guard.recordCalls)
	}
	if guard.recordCalls[0].email != "op@acme.com" {
		t.Errorf("recorded email = %q, want op@acme.com", guard.recordCalls[0].email)
	}
	if guard.recordCalls[0].ip == "" {
		t.Error("recorded IP is empty — the throttle can't key on the source")
	}
}

// TestLogin_Success_RecordsThrottleSuccess: correct credentials record a success
// so the source's counter is cleared (an earlier fat-fingering doesn't carry).
func TestLogin_Success_RecordsThrottleSuccess(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("a-good-password-123"), bcrypt.DefaultCost)
	u := domain.User{ID: "usr_1", Email: "op@acme.com", PasswordHash: string(hash)}
	store := &fakeUserStore{loginUser: &u, tenants: []domain.UserTenant{{UserID: "usr_1", TenantID: "ten_acme"}}}
	h, _ := newAuthHandler(t, store)
	guard := &fakeLoginGuard{}
	h.SetLoginGuard(guard)

	body, _ := json.Marshal(loginReq{Email: "op@acme.com", Password: "a-good-password-123"})
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(string(body)))
	req.RemoteAddr = "203.0.113.9:5555"
	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	if len(guard.recordCalls) != 1 || !guard.recordCalls[0].success {
		t.Fatalf("record calls = %+v, want exactly one success record", guard.recordCalls)
	}
}
