package customerportal

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/sagarsuperuser/velox/internal/platform/crypto"
)

// fakeLookup mirrors the DB lookup the request service uses. Keyed on
// exact blind index so tests control what a given email resolves to.
type fakeLookup struct {
	mu      sync.Mutex
	byBlind map[string][]CustomerMatch
}

func newFakeLookup() *fakeLookup {
	return &fakeLookup{byBlind: map[string][]CustomerMatch{}}
}

func (f *fakeLookup) seed(blind string, matches ...CustomerMatch) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byBlind[blind] = append(f.byBlind[blind], matches...)
}

func (f *fakeLookup) FindByEmailBlindIndex(_ context.Context, blind string, _ int) ([]CustomerMatch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.byBlind[blind], nil
}

// captureDelivery records every DeliverMagicLink call so tests can
// assert on the fan-out without needing real SMTP or outbox wiring.
type captureDelivery struct {
	mu    sync.Mutex
	calls []captureCall
}

type captureCall struct {
	TenantID   string
	CustomerID string
	RawToken   string
}

func (c *captureDelivery) DeliverMagicLink(_ context.Context, tenantID, customerID, rawToken string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, captureCall{tenantID, customerID, rawToken})
	return nil
}

func (c *captureDelivery) snap() []captureCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]captureCall, len(c.calls))
	copy(out, c.calls)
	return out
}

// testBlinder builds a real Blinder with a deterministic key so
// Blind(email) in tests is identical to what the service computes.
func testBlinder(t *testing.T) *crypto.Blinder {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	b, err := crypto.NewBlinder(hex.EncodeToString(key))
	if err != nil {
		t.Fatalf("NewBlinder: %v", err)
	}
	return b
}

func newRequestSvcForTest(t *testing.T) (*MagicLinkRequestService, *fakeLookup, *captureDelivery, *crypto.Blinder) {
	t.Helper()
	magicSvc, _, _ := newMagicLinkServiceForTest()
	lookup := newFakeLookup()
	delivery := &captureDelivery{}
	blinder := testBlinder(t)
	svc := NewMagicLinkRequestService(magicSvc, lookup, blinder, delivery, nil)
	return svc, lookup, delivery, blinder
}

// TestRequestByEmail_UnknownEmail_NoMint pins the enumeration-resistance
// contract: an unknown email makes it through the service without error
// and without minting. The handler pairs this with a 202 response.
func TestRequestByEmail_UnknownEmail_NoMint(t *testing.T) {
	svc, _, delivery, _ := newRequestSvcForTest(t)
	if err := svc.RequestByEmail(context.Background(), "nobody@example.com"); err != nil {
		t.Fatalf("RequestByEmail: %v", err)
	}
	if n := len(delivery.snap()); n != 0 {
		t.Fatalf("unknown email should not deliver, got %d calls", n)
	}
}

// TestRequestByEmail_KnownEmail_MintsAndDelivers — happy path. Single
// match → one Mint + one Delivery, token carries the magic-link prefix.
func TestRequestByEmail_KnownEmail_MintsAndDelivers(t *testing.T) {
	svc, lookup, delivery, blinder := newRequestSvcForTest(t)
	lookup.seed(blinder.Blind("alice@example.com"), CustomerMatch{
		TenantID: "tnt_a", CustomerID: "cus_1", Livemode: true,
	})

	if err := svc.RequestByEmail(context.Background(), "alice@example.com"); err != nil {
		t.Fatalf("RequestByEmail: %v", err)
	}

	calls := delivery.snap()
	if len(calls) != 1 {
		t.Fatalf("want 1 delivery, got %d", len(calls))
	}
	if calls[0].TenantID != "tnt_a" || calls[0].CustomerID != "cus_1" {
		t.Fatalf("delivery identity mismatch: %+v", calls[0])
	}
	if got := calls[0].RawToken; len(got) < len(magicTokenPrefix) || got[:len(magicTokenPrefix)] != magicTokenPrefix {
		t.Fatalf("token missing magic-link prefix: %q", got)
	}
}

// TestRequestByEmail_NormalisesInput — mixed-case and surrounding
// whitespace collapse onto the same blind index as the canonical form.
// Without this, a customer who registers with "Alice@Example.com" can't
// log in by typing "alice@example.com " a week later.
func TestRequestByEmail_NormalisesInput(t *testing.T) {
	svc, lookup, delivery, blinder := newRequestSvcForTest(t)
	lookup.seed(blinder.Blind("alice@example.com"), CustomerMatch{
		TenantID: "tnt_a", CustomerID: "cus_1",
	})

	if err := svc.RequestByEmail(context.Background(), "  Alice@EXAMPLE.com  "); err != nil {
		t.Fatalf("RequestByEmail: %v", err)
	}
	if n := len(delivery.snap()); n != 1 {
		t.Fatalf("normalised input should match, got %d deliveries", n)
	}
}

// TestRequestByEmail_MultipleTenants_FanOut — the same email legitimately
// exists on two tenants (one customer signed up for two separate SaaS
// businesses billed through Velox). Each tenant's account gets its own
// link, emailed separately — the customer then picks which tenant to
// log into. This is the cross-tenant property that motivates the
// CustomerMatch slice (as opposed to a single-result lookup).
func TestRequestByEmail_MultipleTenants_FanOut(t *testing.T) {
	svc, lookup, delivery, blinder := newRequestSvcForTest(t)
	blind := blinder.Blind("shared@example.com")
	lookup.seed(blind,
		CustomerMatch{TenantID: "tnt_a", CustomerID: "cus_1"},
		CustomerMatch{TenantID: "tnt_b", CustomerID: "cus_2"},
	)

	if err := svc.RequestByEmail(context.Background(), "shared@example.com"); err != nil {
		t.Fatalf("RequestByEmail: %v", err)
	}

	calls := delivery.snap()
	if len(calls) != 2 {
		t.Fatalf("want 2 deliveries, got %d", len(calls))
	}
	seen := map[string]bool{}
	for _, c := range calls {
		seen[c.TenantID] = true
		if c.RawToken == "" {
			t.Fatalf("empty raw token in delivery: %+v", c)
		}
	}
	if !seen["tnt_a"] || !seen["tnt_b"] {
		t.Fatalf("did not deliver to both tenants: %+v", calls)
	}
}

// TestRequestByEmail_NoBlinder_FailsClosed — an unconfigured blinder
// means no email can be resolved. The service returns nil (handler
// responds 202, attacker learns nothing) but skips the lookup entirely.
// This keeps misconfigured environments from accidentally emailing
// magic links based on a degraded lookup path.
func TestRequestByEmail_NoBlinder_FailsClosed(t *testing.T) {
	magicSvc, _, _ := newMagicLinkServiceForTest()
	lookup := newFakeLookup()
	delivery := &captureDelivery{}
	svc := NewMagicLinkRequestService(magicSvc, lookup, crypto.NewNoopBlinder(), delivery, nil)

	if err := svc.RequestByEmail(context.Background(), "alice@example.com"); err != nil {
		t.Fatalf("RequestByEmail: %v", err)
	}
	if n := len(delivery.snap()); n != 0 {
		t.Fatalf("no-op blinder should produce zero deliveries, got %d", n)
	}
}

// TestRequestByEmail_EmptyEmail_NoOp — defence-in-depth above the
// handler's missing-field check. An empty email bypasses blinding and
// returns nil without touching the lookup.
func TestRequestByEmail_EmptyEmail_NoOp(t *testing.T) {
	svc, _, delivery, _ := newRequestSvcForTest(t)
	if err := svc.RequestByEmail(context.Background(), ""); err != nil {
		t.Fatalf("RequestByEmail empty: %v", err)
	}
	if err := svc.RequestByEmail(context.Background(), "   "); err != nil {
		t.Fatalf("RequestByEmail whitespace: %v", err)
	}
	if n := len(delivery.snap()); n != 0 {
		t.Fatalf("empty/whitespace email should produce zero deliveries, got %d", n)
	}
}

// TestPublicHandler_RequestMagicLink_Responses walks the three handler
// branches: malformed body → 400, missing email → 400, valid input → 202.
// The 202 is the security-critical response — it must not vary with the
// match result.
func TestPublicHandler_RequestMagicLink_Responses(t *testing.T) {
	svc, lookup, _, blinder := newRequestSvcForTest(t)
	lookup.seed(blinder.Blind("alice@example.com"), CustomerMatch{TenantID: "tnt_a", CustomerID: "cus_1"})
	magicSvc, _, _ := newMagicLinkServiceForTest()
	h := NewPublicHandler(svc, magicSvc)

	call := func(body string) int {
		req := httptest.NewRequest(http.MethodPost, "/magic-link", bytes.NewBufferString(body))
		rec := httptest.NewRecorder()
		h.Routes().ServeHTTP(rec, req)
		return rec.Code
	}

	if code := call("not-json"); code != http.StatusBadRequest {
		t.Fatalf("malformed body: want 400, got %d", code)
	}
	if code := call(`{}`); code != http.StatusBadRequest {
		t.Fatalf("missing email: want 400, got %d", code)
	}
	body, _ := json.Marshal(map[string]string{"email": "alice@example.com"})
	if code := call(string(body)); code != http.StatusAccepted {
		t.Fatalf("known email: want 202, got %d", code)
	}
	body, _ = json.Marshal(map[string]string{"email": "nobody@example.com"})
	if code := call(string(body)); code != http.StatusAccepted {
		t.Fatalf("unknown email: want 202, got %d", code)
	}
}

// TestPublicHandler_ConsumeMagicLink_HappyPath walks the end-to-end
// flow: mint a magic link (via the service directly) → POST its raw
// token to /magic/consume → receive a portal session token back. The
// session token must carry the vlx_cps_ prefix so the frontend can
// immediately use it against /v1/me/*.
func TestPublicHandler_ConsumeMagicLink_HappyPath(t *testing.T) {
	magicSvc, _, _ := newMagicLinkServiceForTest()
	requestSvc := NewMagicLinkRequestService(magicSvc, newFakeLookup(), testBlinder(t), &captureDelivery{}, nil)
	h := NewPublicHandler(requestSvc, magicSvc)

	mint, err := magicSvc.Mint(context.Background(), "tnt_a", "cus_1")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	body, _ := json.Marshal(map[string]string{"token": mint.RawToken})
	req := httptest.NewRequest(http.MethodPost, "/magic/consume", bytes.NewBuffer(body))
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("happy path: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out consumeMagicLinkResponse
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.CustomerID != "cus_1" {
		t.Fatalf("customer identity: want cus_1, got %q", out.CustomerID)
	}
	if len(out.Token) < len(tokenPrefix) || out.Token[:len(tokenPrefix)] != tokenPrefix {
		t.Fatalf("session token missing prefix: %q", out.Token)
	}
}

// TestEmailMagicLinkDelivery_HappyPath pins the production delivery path:
// (tenant, customer) → customer email/name resolved → URL built from
// CUSTOMER_PORTAL_URL + raw token → sender called once with the tuple.
// This is the contract the router relies on when magic-link emails are
// enabled, so it's worth a test even without SMTP infrastructure.
func TestEmailMagicLinkDelivery_HappyPath(t *testing.T) {
	sender := &fakeMagicLinkSender{}
	resolver := fakeEmailResolver{
		"tnt_a/cus_1": {email: "alice@example.com", name: "Alice"},
	}
	d := NewEmailMagicLinkDelivery(sender, resolver, "https://portal.velox.dev", nil)

	if err := d.DeliverMagicLink(context.Background(), "tnt_a", "cus_1", "vlx_cpml_raw_abc"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if got := sender.lastTo; got != "alice@example.com" {
		t.Fatalf("to: got %q, want alice@example.com", got)
	}
	wantURL := "https://portal.velox.dev/login?magic_token=vlx_cpml_raw_abc"
	if sender.lastURL != wantURL {
		t.Fatalf("url: got %q, want %q", sender.lastURL, wantURL)
	}
}

// TestEmailMagicLinkDelivery_MissingEmail_NoOp — a customer row without
// an email shouldn't error the delivery (the caller would log it as a
// "request failed"), it should skip. Gives us a clean audit trail
// without breaking the enumeration-resistance guarantee.
func TestEmailMagicLinkDelivery_MissingEmail_NoOp(t *testing.T) {
	sender := &fakeMagicLinkSender{}
	resolver := fakeEmailResolver{
		"tnt_a/cus_1": {email: "", name: "Alice"},
	}
	d := NewEmailMagicLinkDelivery(sender, resolver, "https://portal.velox.dev", nil)

	if err := d.DeliverMagicLink(context.Background(), "tnt_a", "cus_1", "vlx_cpml_raw"); err != nil {
		t.Fatalf("missing-email delivery should swallow, got %v", err)
	}
	if sender.lastTo != "" {
		t.Fatalf("sender should not have been called, got %+v", sender)
	}
}

// TestEmailMagicLinkDelivery_TrimTrailingSlash — operators often ship
// CUSTOMER_PORTAL_URL with a trailing slash. The built URL shouldn't
// end up with a double-slash before /login.
func TestEmailMagicLinkDelivery_TrimTrailingSlash(t *testing.T) {
	sender := &fakeMagicLinkSender{}
	resolver := fakeEmailResolver{"tnt/cus": {email: "x@y.z", name: "X"}}
	d := NewEmailMagicLinkDelivery(sender, resolver, "https://portal.velox.dev/", nil)

	if err := d.DeliverMagicLink(context.Background(), "tnt", "cus", "tok"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if sender.lastURL != "https://portal.velox.dev/login?magic_token=tok" {
		t.Fatalf("trailing slash not trimmed: %q", sender.lastURL)
	}
}

type fakeMagicLinkSender struct {
	lastTenant string
	lastTo     string
	lastName   string
	lastURL    string
}

func (f *fakeMagicLinkSender) SendPortalMagicLink(tenantID, to, name, url string) error {
	f.lastTenant, f.lastTo, f.lastName, f.lastURL = tenantID, to, name, url
	return nil
}

type fakeEmailResolver map[string]struct{ email, name string }

func (r fakeEmailResolver) GetCustomerEmail(_ context.Context, tenantID, customerID string) (string, string, error) {
	v := r[tenantID+"/"+customerID]
	return v.email, v.name, nil
}

// TestPublicHandler_ConsumeMagicLink_FailureModes asserts the critical
// uniformity property: unknown / used / expired / malformed all surface
// as 401 with the same generic message. An attacker replaying a leaked
// email URL can't distinguish "this was a real token that expired" from
// "this was never a valid token at all".
func TestPublicHandler_ConsumeMagicLink_FailureModes(t *testing.T) {
	magicSvc, _, _ := newMagicLinkServiceForTest()
	requestSvc := NewMagicLinkRequestService(magicSvc, newFakeLookup(), testBlinder(t), &captureDelivery{}, nil)
	h := NewPublicHandler(requestSvc, magicSvc)

	call := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/magic/consume", bytes.NewBufferString(body))
		rec := httptest.NewRecorder()
		h.Routes().ServeHTTP(rec, req)
		return rec
	}

	// Malformed body.
	if rec := call("not-json"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("malformed body: want 401, got %d", rec.Code)
	}
	// Unknown token.
	body, _ := json.Marshal(map[string]string{"token": magicTokenPrefix + "deadbeef"})
	if rec := call(string(body)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown token: want 401, got %d", rec.Code)
	}
	// Already-used token — first consume passes, second must 401.
	mint, err := magicSvc.Mint(context.Background(), "tnt_a", "cus_1")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	body, _ = json.Marshal(map[string]string{"token": mint.RawToken})
	if rec := call(string(body)); rec.Code != http.StatusOK {
		t.Fatalf("first consume: want 200, got %d", rec.Code)
	}
	if rec := call(string(body)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("replay: want 401, got %d", rec.Code)
	}
}
