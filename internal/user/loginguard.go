package user

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/sagarsuperuser/velox/internal/platform/crypto"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

const (
	// LoginThrottleThreshold is how many failed logins from ONE client IP-prefix
	// against ONE account are tolerated within LoginThrottleWindow before the
	// guard short-circuits further attempts (pre-bcrypt) to a generic 401.
	// Because the key is (IP-prefix × account), this bounds a single source's
	// guess rate against a given account WITHOUT letting a remote party lock the
	// account's real owner out — the owner arrives from a different IP-prefix and
	// is never in this counter. Tunable; a lean default for ADR-094's first cut.
	LoginThrottleThreshold = 10
	// LoginThrottleWindow is the fixed window the counter accumulates over; a
	// subject's row self-resets once the window lapses.
	LoginThrottleWindow = 15 * time.Minute
)

// Decision is the guard's verdict on a login attempt, evaluated BEFORE the
// expensive bcrypt verify. Today it is only allow-or-deny; this struct is the
// extensibility SEAM where ADR-094's graduated ladder (tarpit → CAPTCHA →
// step-up/MFA) slots in later without changing callers.
type Decision struct {
	// Deny short-circuits the attempt to a generic 401 without running bcrypt.
	Deny bool
}

// LoginGuard is the pre-auth brute-force throttle. Implementations MUST key only
// on attacker-controlled dimensions (the client IP-prefix combined with the
// target account) — never a bare account — so the guard throttles a single
// source hammering one account without giving a remote party from a DIFFERENT
// IP-prefix a way to lock the account's real owner out. It replaces the removed
// users.locked_until auto-lockout, which was both fragmenting (Redis↔in-memory)
// and weaponizable (any known email → a 15-minute lockout on demand).
//
// Two residuals, documented in ADR-094 (the deferred ladder is the real fix):
// the non-weaponizability rests on the client IP being the real client's — set
// TRUST_PROXY behind a proxy, or all clients collapse to the proxy prefix and
// this degrades toward a bare-account lock; and a colocated attacker who SHARES
// the owner's prefix (same NAT/CGNAT/VPN/cloud region) can throttle the owner.
type LoginGuard interface {
	// Check runs BEFORE bcrypt with the raw email and the trusted client IP. A
	// Deny means "recent failures from this source against this account crossed
	// the threshold — refuse without spending bcrypt."
	Check(ctx context.Context, email, ip string) Decision
	// Record notes an attempt's outcome. A failure increments the windowed
	// counter; a success clears it (this source proved it holds the credential).
	Record(ctx context.Context, email, ip string, success bool)
}

// NoopLoginGuard disables per-account throttling entirely, leaning on the
// /v1/auth per-IP rate limiter alone. The default in unit tests and when no
// store is wired.
type NoopLoginGuard struct{}

func (NoopLoginGuard) Check(context.Context, string, string) Decision { return Decision{} }
func (NoopLoginGuard) Record(context.Context, string, string, bool)   {}

// PostgresLoginGuard is the production LoginGuard: a fixed-window failed-login
// counter in Postgres keyed on HMAC(pepper, ip_prefix|account). Login already
// hard-depends on Postgres (it reads users.password_hash), so keeping the
// throttle authoritative here adds no new SPOF and — being a single store — the
// Redis↔in-memory fragmentation of the old counter cannot re-form. All window
// math uses the DB clock, so N instances never disagree on the window boundary.
type PostgresLoginGuard struct {
	db        *postgres.DB
	blinder   *crypto.Blinder // HMAC pepper for the subject; nil/disabled → sha256 fallback (dev)
	threshold int
	window    time.Duration
}

// NewPostgresLoginGuard builds the production guard. blinder may be an
// unconfigured (noop) blinder in local dev with no key set — then subjects fall
// back to a plain SHA-256 (still no plaintext stored, just no pepper).
func NewPostgresLoginGuard(db *postgres.DB, blinder *crypto.Blinder) *PostgresLoginGuard {
	return &PostgresLoginGuard{db: db, blinder: blinder, threshold: LoginThrottleThreshold, window: LoginThrottleWindow}
}

func (g *PostgresLoginGuard) Check(ctx context.Context, email, ip string) Decision {
	tx, err := g.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		// Fail OPEN: never turn a store blip into a login outage. If Postgres is
		// truly down, Authenticate fails on its own moments later anyway.
		slog.WarnContext(ctx, "login throttle: check begin tx failed; allowing", "error", err)
		return Decision{}
	}
	defer postgres.Rollback(tx)

	var failures int
	err = tx.QueryRowContext(ctx,
		`SELECT failures FROM login_throttle
		 WHERE subject = $1 AND window_start > now() - make_interval(secs => $2)`,
		g.subject(email, ip), g.window.Seconds(),
	).Scan(&failures)
	if errors.Is(err, sql.ErrNoRows) {
		return Decision{} // no recent failures from this source against this account
	}
	if err != nil {
		slog.WarnContext(ctx, "login throttle: check query failed; allowing", "error", err)
		return Decision{}
	}
	return Decision{Deny: failures >= g.threshold}
}

func (g *PostgresLoginGuard) Record(ctx context.Context, email, ip string, success bool) {
	tx, err := g.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		slog.WarnContext(ctx, "login throttle: record begin tx failed", "error", err)
		return
	}
	defer postgres.Rollback(tx)

	subject := g.subject(email, ip)
	if success {
		// This source proved it holds the credential — clear its counter so a
		// legitimate operator who fat-fingered a few times isn't throttled next
		// visit.
		_, err = tx.ExecContext(ctx, `DELETE FROM login_throttle WHERE subject = $1`, subject)
	} else {
		// Atomic increment within the window, or reset when the window lapsed.
		// Single statement, no advisory lock and no read-modify-write race.
		_, err = tx.ExecContext(ctx, `
			INSERT INTO login_throttle (subject, window_start, failures)
			VALUES ($1, now(), 1)
			ON CONFLICT (subject) DO UPDATE SET
			  failures = CASE WHEN login_throttle.window_start > now() - make_interval(secs => $2)
			                  THEN login_throttle.failures + 1 ELSE 1 END,
			  window_start = CASE WHEN login_throttle.window_start > now() - make_interval(secs => $2)
			                      THEN login_throttle.window_start ELSE now() END`,
			subject, g.window.Seconds())
	}
	if err != nil {
		slog.WarnContext(ctx, "login throttle: record failed", "success", success, "error", err)
		return
	}
	_ = tx.Commit()
}

// SweepExpired reclaims rows whose window lapsed beyond a retention horizon —
// the table's only growth vector (a distributed attack briefly mints one row per
// attacker prefix × account). A READY PRIMITIVE that is NOT yet wired: putting it
// on the background scheduler is a deferred follow-up (ADR-094). At operator-
// login volume the growth is negligible, and Check/Record already ignore stale
// rows via the window predicate, so an unswept row is inert, not incorrect.
func (g *PostgresLoginGuard) SweepExpired(ctx context.Context) error {
	tx, err := g.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return err
	}
	defer postgres.Rollback(tx)
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM login_throttle WHERE window_start < now() - make_interval(secs => $1)`,
		(2 * g.window).Seconds(),
	); err != nil {
		return err
	}
	return tx.Commit()
}

// subject derives the throttle key: hex HMAC(pepper, ip_prefix | lower(email)).
// The email is never stored in plaintext (the old Redis key was
// velox:login_fail:<PLAINTEXT-EMAIL>). The IP is masked to a prefix so an
// attacker rotating within a /64 (2^64 addresses) can't mint a fresh key per
// request.
func (g *PostgresLoginGuard) subject(email, ip string) string {
	raw := ipPrefix(ip) + "|" + strings.ToLower(strings.TrimSpace(email))
	if g.blinder != nil {
		if h := g.blinder.Blind(raw); h != "" {
			return h // peppered HMAC (production; keyed off VELOX_ENCRYPTION_KEY)
		}
	}
	// Blinder disabled (local dev, no key): plain SHA-256 keeps subjects distinct
	// and stores no plaintext. Weaker (no pepper → offline-dictionary-able) but
	// dev-only; production requires the encryption key.
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// ipPrefix masks a client IP to its aggregation prefix: IPv4 /24, IPv6 /64.
// Bare-IP keying is useless against IPv6 (a residential /64 is 2^64 fresh
// sources) and weak against an attacker holding a /24. An unparseable input is
// returned as-is so the key stays stable rather than collapsing to one bucket.
func ipPrefix(ipStr string) string {
	ip := net.ParseIP(strings.TrimSpace(ipStr))
	if ip == nil {
		return strings.TrimSpace(ipStr)
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.Mask(net.CIDRMask(24, 32)).String() + "/24"
	}
	return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
}
