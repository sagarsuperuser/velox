package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/sagarsuperuser/velox/internal/config"
	"github.com/sagarsuperuser/velox/internal/platform/migrate"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg, err := config.Load()
	if err != nil {
		fatal("load config: %v", err)
	}

	// Run migrations first — migrate.Up manages its own short-lived pool.
	if err := migrate.Up(cfg.DB.URL); err != nil {
		fatal("migrations: %v", err)
	}

	pool, err := config.OpenPostgres(cfg.DB)
	if err != nil {
		fatal("open database: %v", err)
	}
	defer func() { _ = pool.Close() }()

	db := postgres.NewDB(pool, 5*time.Second)
	ctx := context.Background()

	// Create tenant
	tenantID := postgres.NewID("vlx_ten")
	tenantName := "Demo Tenant"
	if len(os.Args) > 1 {
		tenantName = strings.Join(os.Args[1:], " ")
	}

	_, err = db.Pool.ExecContext(ctx,
		`INSERT INTO tenants (id, name, status) VALUES ($1, $2, 'active') ON CONFLICT DO NOTHING`,
		tenantID, tenantName)
	if err != nil {
		fatal("create tenant: %v", err)
	}

	// Use bypass RLS for bootstrap.
	tx, err := db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		fatal("begin tx: %v", err)
	}

	// Mint paired test + live secret keys plus a test publishable key.
	// Bootstrap seeds both modes so a fresh install can reach live mode
	// without a `psql` detour: per Stripe's pattern, you can't mint
	// cross-mode keys post-auth (a test-mode caller mints test keys), so
	// the only path to a first live key is to mint it here. Operators
	// who don't intend to charge real money simply ignore the live key.
	testSecretKey, testSecretPrefix, testSecretID := mintKey("vlx_secret_test_")
	liveSecretKey, liveSecretPrefix, liveSecretID := mintKey("vlx_secret_live_")
	testPubKey, testPubPrefix, testPubID := mintKey("vlx_pub_test_")

	// migration 0021 installs a BEFORE INSERT trigger on api_keys that
	// overwrites NEW.livemode from the `app.livemode` session setting —
	// TxBypass doesn't set it, so the trigger would default to live for
	// every row. Set it explicitly per insert.
	insert := func(id, prefix, rawKey, keyType string, livemode bool, name string) {
		mode := "off"
		if livemode {
			mode = "on"
		}
		if _, err := tx.ExecContext(ctx,
			`SELECT set_config('app.livemode', $1, true)`, mode); err != nil {
			_ = tx.Rollback()
			fatal("set livemode: %v", err)
		}
		hash := sha256.Sum256([]byte(rawKey))
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO api_keys (id, key_prefix, key_hash, key_type, livemode, name, tenant_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			id, prefix, hex.EncodeToString(hash[:]), keyType, livemode, name, tenantID); err != nil {
			_ = tx.Rollback()
			fatal("create %s key: %v", name, err)
		}
	}

	insert(testSecretID, testSecretPrefix, testSecretKey, "secret", false, "Bootstrap Key (Test)")
	insert(liveSecretID, liveSecretPrefix, liveSecretKey, "secret", true, "Bootstrap Key (Live)")
	insert(testPubID, testPubPrefix, testPubKey, "publishable", false, "Bootstrap Publishable Key (Test)")

	if err := tx.Commit(); err != nil {
		fatal("commit: %v", err)
	}

	fmt.Println("========================================")
	fmt.Println("  Velox Bootstrap Complete")
	fmt.Println("========================================")
	fmt.Println()
	fmt.Printf("  Tenant:     %s\n", tenantName)
	fmt.Printf("  Tenant ID:  %s\n", tenantID)
	fmt.Println()
	fmt.Println("  Secret Key — TEST mode (no real money):")
	fmt.Printf("  %s\n", testSecretKey)
	fmt.Println()
	fmt.Println("  Secret Key — LIVE mode (charges real cards):")
	fmt.Printf("  %s\n", liveSecretKey)
	fmt.Println()
	fmt.Println("  Publishable Key (restricted, test mode):")
	fmt.Printf("  %s\n", testPubKey)
	fmt.Println()
	fmt.Println("  Sign in to the dashboard at http://localhost:5173 by")
	fmt.Println("  pasting the Secret Key for the mode you want to operate in.")
	fmt.Println("  Mode is determined by the key — there is no in-app toggle.")
	fmt.Println("  Switching modes means signing out and pasting the other key.")
	fmt.Println()
	fmt.Println("  Try it on the API:")
	fmt.Printf("  curl -H 'Authorization: Bearer %s' http://localhost:8080/v1/customers\n", testSecretKey)
	fmt.Println("========================================")
}

// mintKey returns a freshly generated raw key with the given mode-aware
// prefix, its DB lookup prefix (full prefix + first 12 hex chars), and
// a newly-minted vlx_key id. Matches auth.Service.CreateKey's indexed-
// prefix shape so ValidateKey can find these rows by prefix.
func mintKey(prefix string) (raw, dbPrefix, id string) {
	secret := make([]byte, 32)
	rand.Read(secret)
	secretHex := hex.EncodeToString(secret)
	return prefix + secretHex, prefix + secretHex[:12], postgres.NewID("vlx_key")
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "ERROR: "+format+"\n", args...)
	os.Exit(1)
}
