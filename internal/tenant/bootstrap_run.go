package tenant

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// RunBootstrap is the ONLY code path that provisions a tenant (ADR-073).
// The HTTP handler and cmd/velox-bootstrap are thin callers — the two
// writers had already drifted apart once (HTTP minted no owner user, no
// live key, no settings row: a dashboard dead-end), so the shared
// sequence is the fix, not doc'd parity.
//
// Everything runs in ONE bypass transaction under
// pg_advisory_xact_lock(LockKeyBootstrap):
//
//	guards (first-tenant / owner-email uniqueness, authoritative under
//	the lock) → tenant → tenant_settings seed → 3 API keys → owner user
//
// so a failure anywhere leaves ZERO rows behind. The half-bootstrap
// shape this kills: tenant+keys committed, owner-user create fails on
// password validation, every retry 409s "already bootstrapped", and the
// operator is locked out of the dashboard forever. All inputs validate
// BEFORE the first write for the same reason.

// ErrAlreadyBootstrapped is returned when FirstTenantOnly is set and a
// tenant already exists. HTTP maps it to 409.
var ErrAlreadyBootstrapped = errs.InvalidState("bootstrap already completed — tenants exist").WithCode("already_bootstrapped")

// ErrOwnerEmailExists is returned when the owner email already has an
// account. The CLI prints re-run guidance on it; HTTP maps it to 409.
var ErrOwnerEmailExists = errs.AlreadyExists("owner_email", "an account with this email already exists")

// BootstrapDeps are the cross-domain seams RunBootstrap needs. The
// tenant package must not import its peer package user, so callers
// wire these (router.go and cmd/velox-bootstrap both pass
// user.HashPassword + user.PostgresStore.CreateInTx).
type BootstrapDeps struct {
	// HashPassword validates the plaintext (length ≥ user.MinPasswordLength,
	// bcrypt's 72-byte cap, denylist) and returns the bcrypt hash. It is
	// called BEFORE any write so validation failures cannot half-bootstrap.
	HashPassword func(plaintext string) (string, error)
	// CreateUserTx inserts the owner user + tenant attachment inside the
	// bootstrap tx.
	CreateUserTx func(ctx context.Context, tx *sql.Tx, email, passwordHash, tenantID, role string) (domain.User, error)
}

// BootstrapOpts parameterize one bootstrap run.
type BootstrapOpts struct {
	TenantName    string // default "Demo Tenant"
	OwnerEmail    string // default admin@velox.local
	OwnerPassword string // empty = generate a 96-bit one, returned once
	// FirstTenantOnly refuses when ANY tenant exists — the HTTP
	// endpoint's one-shot semantics. The CLI passes false: additional
	// tenants in the same deployment are a supported dev workflow,
	// guarded per-run by owner-email uniqueness instead.
	FirstTenantOnly bool
}

// BootstrapResult carries everything the caller prints or returns —
// the raw keys and the (possibly generated) owner password appear here
// exactly once and are not retrievable afterwards.
type BootstrapResult struct {
	Tenant             domain.Tenant
	OwnerUser          domain.User
	OwnerPassword      string
	PasswordGenerated  bool
	TestSecretKey      string
	LiveSecretKey      string
	TestPublishableKey string
}

func RunBootstrap(ctx context.Context, db *postgres.DB, deps BootstrapDeps, opts BootstrapOpts) (BootstrapResult, error) {
	if deps.HashPassword == nil || deps.CreateUserTx == nil {
		return BootstrapResult{}, fmt.Errorf("bootstrap deps not wired")
	}

	tenantName := strings.TrimSpace(opts.TenantName)
	if tenantName == "" {
		tenantName = "Demo Tenant"
	}
	ownerEmail := strings.TrimSpace(opts.OwnerEmail)
	if ownerEmail == "" {
		ownerEmail = "admin@velox.local"
	}
	if !strings.Contains(ownerEmail, "@") {
		return BootstrapResult{}, errs.Invalid("owner_email", "must be an email address")
	}

	ownerPassword := opts.OwnerPassword
	generated := false
	if ownerPassword == "" {
		ownerPassword = generatePassword()
		generated = true
	}
	// Validate + hash BEFORE any write. user.HashPassword rejects
	// passwords under MinPasswordLength (12) and over bcrypt's 72-byte
	// cap — those failures must surface as a clean 422/CLI error with
	// zero rows written, never after a tenant has committed.
	passwordHash, err := deps.HashPassword(ownerPassword)
	if err != nil {
		return BootstrapResult{}, err
	}

	tenantID := postgres.NewID("vlx_ten")
	testSecretKey, testSecretPrefix, testSecretID := mintKey("vlx_secret_test_")
	liveSecretKey, liveSecretPrefix, liveSecretID := mintKey("vlx_secret_live_")
	testPubKey, testPubPrefix, testPubID := mintKey("vlx_pub_test_")

	tx, err := db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return BootstrapResult{}, err
	}
	defer postgres.Rollback(tx)

	// Serialize concurrent bootstraps. The old INSERT ... WHERE NOT
	// EXISTS was NOT race-safe (READ COMMITTED: two statement snapshots
	// each miss the other's uncommitted row — two tenants, two owner
	// credential sets). The xact lock releases at commit/rollback and
	// makes both guards below authoritative.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, postgres.LockKeyBootstrap); err != nil {
		return BootstrapResult{}, fmt.Errorf("bootstrap lock: %w", err)
	}

	if opts.FirstTenantOnly {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tenants)`).Scan(&exists); err != nil {
			return BootstrapResult{}, fmt.Errorf("check tenants: %w", err)
		}
		if exists {
			return BootstrapResult{}, ErrAlreadyBootstrapped
		}
	}

	var emailTaken bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE email = $1)`, ownerEmail).Scan(&emailTaken); err != nil {
		return BootstrapResult{}, fmt.Errorf("check owner email: %w", err)
	}
	if emailTaken {
		return BootstrapResult{}, ErrOwnerEmailExists
	}

	var createdTenant domain.Tenant
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO tenants (id, name, status)
		VALUES ($1, $2, 'active')
		RETURNING id, name, status, created_at, updated_at
	`, tenantID, tenantName).Scan(&createdTenant.ID, &createdTenant.Name, &createdTenant.Status, &createdTenant.CreatedAt, &createdTenant.UpdatedAt); err != nil {
		return BootstrapResult{}, fmt.Errorf("create tenant: %w", err)
	}

	// Every tenant has a tenant_settings row — bootstrap seeds an
	// explicit one so operators can edit settings immediately without
	// an implicit upsert side-effect on first invoice. Values match
	// tenant.DefaultSettings.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO tenant_settings (
			tenant_id, default_currency, timezone, invoice_prefix,
			net_payment_terms, tax_provider, tax_on_failure
		)
		VALUES ($1, 'USD', 'UTC', 'VLX', 30, 'manual', 'block')
		ON CONFLICT (tenant_id) DO NOTHING
	`, tenantID); err != nil {
		return BootstrapResult{}, fmt.Errorf("seed tenant settings: %w", err)
	}

	// DELIBERATELY no dunning policy is seeded here (ADR-036 amendment). A
	// generic seeded default would be is_default=true and SHADOW a recipe's
	// own policy (recipes create theirs via UpsertPolicy → auto-default-first;
	// a pre-existing bootstrap default would win the unique-index slot and
	// suppress it) — a silent wrong-policy bug. A genuinely zero-policy tenant
	// is safe: StartDunning maps "no effective policy" to a deliberate skip, so
	// the money-path enrollment sweep never errors. The operator's first policy
	// (recipe or manual) auto-becomes the default. Do not "fix" this by seeding.

	// Mint paired test + live secret keys plus a test publishable key.
	// Per Stripe's pattern you can't mint cross-mode keys post-auth (a
	// test-mode caller mints test keys), so the only path to a FIRST
	// live key is here. Operators not charging real money ignore it.
	//
	// Migration 0021 installs a BEFORE INSERT trigger on api_keys that
	// overwrites NEW.livemode from the app.livemode session setting —
	// TxBypass doesn't set it, so pin it per insert (set_config with
	// is_local=true is tx-scoped).
	insertKey := func(id, prefix, rawKey, keyType string, livemode bool, name string) error {
		mode := "off"
		if livemode {
			mode = "on"
		}
		if _, err := tx.ExecContext(ctx, `SELECT set_config('app.livemode', $1, true)`, mode); err != nil {
			return fmt.Errorf("set livemode: %w", err)
		}
		hash := sha256.Sum256([]byte(rawKey))
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO api_keys (id, key_prefix, key_hash, key_type, livemode, name, tenant_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
		`, id, prefix, hex.EncodeToString(hash[:]), keyType, livemode, name, tenantID); err != nil {
			return fmt.Errorf("create %s: %w", name, err)
		}
		return nil
	}
	if err := insertKey(testSecretID, testSecretPrefix, testSecretKey, "secret", false, "Bootstrap Key (Test)"); err != nil {
		return BootstrapResult{}, err
	}
	if err := insertKey(liveSecretID, liveSecretPrefix, liveSecretKey, "secret", true, "Bootstrap Key (Live)"); err != nil {
		return BootstrapResult{}, err
	}
	if err := insertKey(testPubID, testPubPrefix, testPubKey, "publishable", false, "Bootstrap Publishable Key (Test)"); err != nil {
		return BootstrapResult{}, err
	}

	ownerUser, err := deps.CreateUserTx(ctx, tx, ownerEmail, passwordHash, tenantID, "owner")
	if err != nil {
		return BootstrapResult{}, err
	}

	if err := tx.Commit(); err != nil {
		return BootstrapResult{}, err
	}

	return BootstrapResult{
		Tenant:             createdTenant,
		OwnerUser:          ownerUser,
		OwnerPassword:      ownerPassword,
		PasswordGenerated:  generated,
		TestSecretKey:      testSecretKey,
		LiveSecretKey:      liveSecretKey,
		TestPublishableKey: testPubKey,
	}, nil
}

// generatePassword returns 24 random hex chars (96 bits) — clears the
// 12-char minimum from internal/user.MinPasswordLength with margin.
// Returned once in the bootstrap result; not retrievable afterwards.
func generatePassword() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// mintKey returns a freshly generated raw key with the given mode-aware
// prefix, its DB lookup prefix (full prefix + first 12 hex chars), and
// a newly-minted vlx_key id — the same indexed-prefix shape
// auth.Service.CreateKey writes, so ValidateKey finds these rows.
func mintKey(prefix string) (raw, dbPrefix, id string) {
	secret := make([]byte, 32)
	_, _ = rand.Read(secret)
	secretHex := hex.EncodeToString(secret)
	return prefix + secretHex, prefix + secretHex[:12], postgres.NewID("vlx_key")
}
