package customerportal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/sagarsuperuser/velox/internal/platform/crypto"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// CustomerMatch is the narrow identity the request service needs per hit on
// the email blind index. Defined here — not imported from customer — so
// customerportal stays below customer in the dep graph. The wiring layer
// translates customer.EmailMatch → this.
type CustomerMatch struct {
	TenantID   string
	CustomerID string
	Livemode   bool
}

// CustomerLookup resolves an email blind index to zero or more active
// customers across tenants. Runs under TxBypass (the caller is
// unauthenticated), so correctness depends on HMAC(key, email) being
// unguessable — see internal/platform/crypto.Blinder.
type CustomerLookup interface {
	FindByEmailBlindIndex(ctx context.Context, blind string, limit int) ([]CustomerMatch, error)
}

// MagicLinkDelivery is the seam the request service uses to hand the
// freshly-minted token to whatever ships it (email outbox in production,
// a logger in tests/early wiring). Kept tiny so P5 can swap the logger
// stub for the real email sender without touching request.go.
type MagicLinkDelivery interface {
	DeliverMagicLink(ctx context.Context, tenantID, customerID, rawToken string) error
}

// maxEmailMatches bounds the fan-out from a single request. If the same
// email is registered across many tenants, we still mint at most this
// many links — a cheap guardrail against an unknown colliding index
// exploding work per request.
const maxEmailMatches = 10

// MagicLinkRequestService drives the public "I forgot my portal link"
// flow. Input is an email; output is always a 202 from the handler.
// Match-or-miss, malformed-or-clean, the service takes the same code path
// and same (bounded) time so an attacker can't learn anything from
// differential response behavior.
type MagicLinkRequestService struct {
	magic    *MagicLinkService
	lookup   CustomerLookup
	blinder  *crypto.Blinder
	delivery MagicLinkDelivery
	logger   *slog.Logger
}

func NewMagicLinkRequestService(
	magic *MagicLinkService,
	lookup CustomerLookup,
	blinder *crypto.Blinder,
	delivery MagicLinkDelivery,
	logger *slog.Logger,
) *MagicLinkRequestService {
	if logger == nil {
		logger = slog.Default()
	}
	return &MagicLinkRequestService{
		magic:    magic,
		lookup:   lookup,
		blinder:  blinder,
		delivery: delivery,
		logger:   logger,
	}
}

// RequestByEmail normalises the email, blinds it, looks up matches, and
// fires a delivery per match. Always returns nil unless an internal
// failure makes it unsafe to pretend success — callers should NOT branch
// on the match count, and the handler ignores everything but internal
// errors so no "did it match?" signal leaks to the client.
func (s *MagicLinkRequestService) RequestByEmail(ctx context.Context, email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		// Nothing to do — return nil so the handler still responds 202.
		return nil
	}
	// Blinder not configured → FindByEmailBlindIndex returns nothing. The
	// response still looks identical to a genuine no-match. Router logs
	// a startup warning when VELOX_EMAIL_BIDX_KEY is unset; we stay silent
	// per request so the endpoint can't be used to amplify log volume.
	if s.blinder == nil || !s.blinder.IsEnabled() {
		return nil
	}
	blind := s.blinder.Blind(email)
	if blind == "" {
		return nil
	}

	matches, err := s.lookup.FindByEmailBlindIndex(ctx, blind, maxEmailMatches)
	if err != nil {
		// Real DB/infra failure — surface so the handler returns 500 and
		// we don't silently drop legitimate requests.
		return err
	}

	// Industry parity (Stripe customer-portal docs verbatim: "selects
	// the most recently created customer that has both that email and
	// an active subscription"): when multiple customers share the
	// lookup email WITHIN a tenant, silently disambiguate to the
	// most-recently-created one and send ONE link. Velox is multi-
	// tenant, so we keep cross-tenant fan-out — a person who is
	// legitimately a customer of two different merchants still gets
	// one link per merchant. The dedupe happens within each tenant.
	//
	// Customer IDs are xid-based (postgres.NewID), so a string
	// descending sort is equivalent to created_at descending. No need
	// to widen the CustomerMatch struct or do a separate fetch.
	//
	// Pre-fix (caught 2026-05-25): all matches got their own email,
	// so a tenant with two customers sharing an email sent two
	// magic-link emails for one /magic-link request — confusing
	// recipients and out of step with Stripe + Chargebee shape.
	dispatch := pickOnePerTenant(matches)

	// We continue past per-match failures on purpose: one tenant's
	// broken delivery must not starve another tenant's legitimate
	// customer of their link.
	//
	// Each match carries its own livemode — pin it on ctx before downstream
	// reads. Without this, the per-match Mint and DeliverMagicLink open
	// TxTenant transactions that default to live mode, RLS hides any
	// test-mode customer row, and the email-resolution step fails with
	// a misleading "not found" — even though FindByEmailBlindIndex
	// (TxBypass) just returned the row a few lines earlier. ADR-027 +
	// the ctx-attribute audit pattern from the test-clock catchup work.
	for _, m := range dispatch {
		matchCtx := postgres.WithLivemode(ctx, m.Livemode)
		mint, err := s.magic.Mint(matchCtx, m.TenantID, m.CustomerID)
		if err != nil {
			s.logger.Error("magic-link mint failed",
				"tenant_id", m.TenantID, "customer_id", m.CustomerID, "error", err)
			continue
		}
		if err := s.delivery.DeliverMagicLink(matchCtx, m.TenantID, m.CustomerID, mint.RawToken); err != nil {
			s.logger.Error("magic-link delivery failed",
				"tenant_id", m.TenantID, "customer_id", m.CustomerID, "error", err)
			continue
		}
	}
	return nil
}

// pickOnePerTenant collapses multiple CustomerMatch rows that share a
// tenant down to one — the most-recently-created customer per tenant.
// Cross-tenant matches are preserved (one link per tenant). Stable
// across calls: the same input always selects the same head.
//
// Selection: customer_id descending (xid-based ids are time-sortable,
// so this is created_at desc without needing the column).
func pickOnePerTenant(matches []CustomerMatch) []CustomerMatch {
	if len(matches) <= 1 {
		return matches
	}
	// Sort: TenantID asc (group), then CustomerID desc (newest first
	// within tenant). Take the first row from each tenant group.
	sorted := make([]CustomerMatch, len(matches))
	copy(sorted, matches)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].TenantID != sorted[j].TenantID {
			return sorted[i].TenantID < sorted[j].TenantID
		}
		return sorted[i].CustomerID > sorted[j].CustomerID
	})
	out := make([]CustomerMatch, 0, len(sorted))
	var lastTenant string
	for _, m := range sorted {
		if m.TenantID == lastTenant {
			continue
		}
		out = append(out, m)
		lastTenant = m.TenantID
	}
	return out
}

// MagicLinkEmailSender is the narrow email surface the delivery needs.
// Satisfied by both *email.Sender (direct SMTP) and *email.OutboxSender
// (enqueue into email_outbox). Defined here — not imported — so
// customerportal stays independent of the email package.
type MagicLinkEmailSender interface {
	SendPortalMagicLink(ctx context.Context, tenantID, to, customerName, magicLinkURL string) error
}

// CustomerEmailResolver looks up a customer's email + display name for
// the delivery adapter. The production implementation is
// customer.PostgresStore (via an adapter) and decrypts PII on the way
// out. Defined here to avoid a customerportal→customer import.
type CustomerEmailResolver interface {
	GetCustomerEmail(ctx context.Context, tenantID, customerID string) (email, name string, err error)
}

// EmailMagicLinkDelivery is the production MagicLinkDelivery — resolves
// the customer's email, composes the login URL, and hands it to the
// email sender (which in production is the outbox-backed implementation
// so an SMTP outage doesn't drop the email).
type EmailMagicLinkDelivery struct {
	sender   MagicLinkEmailSender
	resolver CustomerEmailResolver
	baseURL  string // e.g. https://portal.velox.dev
	logger   *slog.Logger
}

func NewEmailMagicLinkDelivery(sender MagicLinkEmailSender, resolver CustomerEmailResolver, baseURL string, logger *slog.Logger) *EmailMagicLinkDelivery {
	if logger == nil {
		logger = slog.Default()
	}
	return &EmailMagicLinkDelivery{
		sender:   sender,
		resolver: resolver,
		baseURL:  baseURL,
		logger:   logger,
	}
}

// ErrPortalURLNotConfigured is returned when CUSTOMER_PORTAL_URL is
// empty. The magic-link email is useless without a clickable URL, so
// rather than emitting a tokens-only message that confuses customers
// we surface a real error and let the request log capture it.
var ErrPortalURLNotConfigured = errors.New("customerportal: CUSTOMER_PORTAL_URL not set")

// DeliverMagicLink resolves the customer's email, builds the magic-link
// URL, and enqueues the email. A missing email is logged and skipped —
// it's legitimate (not every customer has an email on file) and the
// caller must NOT surface it as a failure, or the shape of the /magic-
// link endpoint's response could leak "is there an email on file?".
func (d *EmailMagicLinkDelivery) DeliverMagicLink(ctx context.Context, tenantID, customerID, rawToken string) error {
	if d.baseURL == "" {
		return ErrPortalURLNotConfigured
	}
	email, name, err := d.resolver.GetCustomerEmail(ctx, tenantID, customerID)
	if err != nil {
		return fmt.Errorf("resolve customer email: %w", err)
	}
	if email == "" {
		d.logger.Warn("magic-link delivery skipped: customer has no email",
			"tenant_id", tenantID, "customer_id", customerID)
		return nil
	}
	url := buildMagicLinkURL(d.baseURL, rawToken)
	return d.sender.SendPortalMagicLink(ctx, tenantID, email, name, url)
}

// buildMagicLinkURL assembles the URL the email points at. The
// frontend route is /portal/magic — it reads ?token=... from the URL,
// POSTs to /v1/public/customer-portal/magic/consume, stores the
// returned session token in localStorage, then redirects into /portal
// (the customer dashboard). Caller MUST validate base != "" before
// calling.
func buildMagicLinkURL(base, rawToken string) string {
	base = strings.TrimRight(base, "/")
	return base + "/portal/magic?token=" + rawToken
}
