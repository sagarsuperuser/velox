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

	// Create test-mode secret API key. Bootstrap seeds test mode so a fresh
	// install can connect Stripe test credentials without a live-mode detour;
	// operators create live keys via the API once they're ready to charge.
	secret := make([]byte, 32)
	rand.Read(secret)
	secretHex := hex.EncodeToString(secret)
	rawKey := "vlx_secret_test_" + secretHex
	prefix := "vlx_secret_test_" + secretHex[:12]
	hash := sha256.Sum256([]byte(rawKey))
	hashHex := hex.EncodeToString(hash[:])
	keyID := postgres.NewID("vlx_key")

	// Use bypass RLS for bootstrap
	tx, err := db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		fatal("begin tx: %v", err)
	}

	// TxBypass doesn't set app.livemode, and migration 0021 installs a BEFORE
	// INSERT trigger on api_keys that overwrites NEW.livemode from the session
	// setting — defaulting to live when unset. Set it explicitly so the
	// bootstrap keys land in test mode.
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.livemode', 'off', true)`); err != nil {
		_ = tx.Rollback()
		fatal("set livemode: %v", err)
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO api_keys (id, key_prefix, key_hash, key_type, livemode, name, tenant_id)
		VALUES ($1, $2, $3, 'secret', false, 'Bootstrap Key (Test)', $4)`,
		keyID, prefix, hashHex, tenantID)
	if err != nil {
		_ = tx.Rollback()
		fatal("create api key: %v", err)
	}

	// Test-mode publishable key.
	pubSecret := make([]byte, 32)
	rand.Read(pubSecret)
	pubHex := hex.EncodeToString(pubSecret)
	pubRawKey := "vlx_pub_test_" + pubHex
	pubPrefix := "vlx_pub_test_" + pubHex[:12]
	pubHash := sha256.Sum256([]byte(pubRawKey))
	pubHashHex := hex.EncodeToString(pubHash[:])
	pubKeyID := postgres.NewID("vlx_key")

	_, err = tx.ExecContext(ctx,
		`INSERT INTO api_keys (id, key_prefix, key_hash, key_type, livemode, name, tenant_id)
		VALUES ($1, $2, $3, 'publishable', false, 'Bootstrap Publishable Key (Test)', $4)`,
		pubKeyID, pubPrefix, pubHashHex, tenantID)
	if err != nil {
		_ = tx.Rollback()
		fatal("create pub key: %v", err)
	}

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
	fmt.Println("  Secret Key (full access):")
	fmt.Printf("  %s\n", rawKey)
	fmt.Println()
	fmt.Println("  Publishable Key (restricted):")
	fmt.Printf("  %s\n", pubRawKey)
	fmt.Println()
	fmt.Println("  Sign in to the dashboard at http://localhost:5173 by")
	fmt.Println("  pasting the Secret Key above into the login screen.")
	fmt.Println()
	fmt.Println("  Try it on the API:")
	fmt.Printf("  curl -H 'Authorization: Bearer %s' http://localhost:8080/v1/customers\n", rawKey)
	fmt.Println("========================================")
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "ERROR: "+format+"\n", args...)
	os.Exit(1)
}
