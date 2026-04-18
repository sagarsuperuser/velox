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
	"github.com/sagarsuperuser/velox/internal/userauth"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg, err := config.Load()
	if err != nil {
		fatal("load config: %v", err)
	}

	pool, err := config.OpenPostgres(cfg.DB)
	if err != nil {
		fatal("open database: %v", err)
	}
	defer pool.Close()

	// Run migrations
	if err := migrate.Up(pool); err != nil {
		fatal("migrations: %v", err)
	}

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

	// Create secret API key
	secret := make([]byte, 32)
	rand.Read(secret)
	secretHex := hex.EncodeToString(secret)
	rawKey := "vlx_secret_" + secretHex
	prefix := "vlx_secret_" + secretHex[:12]
	hash := sha256.Sum256([]byte(rawKey))
	hashHex := hex.EncodeToString(hash[:])
	keyID := postgres.NewID("vlx_key")

	// Use bypass RLS for bootstrap
	tx, err := db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		fatal("begin tx: %v", err)
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO api_keys (id, key_prefix, key_hash, key_type, name, tenant_id)
		VALUES ($1, $2, $3, 'secret', 'Bootstrap Key', $4)`,
		keyID, prefix, hashHex, tenantID)
	if err != nil {
		_ = tx.Rollback()
		fatal("create api key: %v", err)
	}

	// Create publishable key too
	pubSecret := make([]byte, 32)
	rand.Read(pubSecret)
	pubHex := hex.EncodeToString(pubSecret)
	pubRawKey := "vlx_pub_" + pubHex
	pubPrefix := "vlx_pub_" + pubHex[:12]
	pubHash := sha256.Sum256([]byte(pubRawKey))
	pubHashHex := hex.EncodeToString(pubHash[:])
	pubKeyID := postgres.NewID("vlx_key")

	_, err = tx.ExecContext(ctx,
		`INSERT INTO api_keys (id, key_prefix, key_hash, key_type, name, tenant_id)
		VALUES ($1, $2, $3, 'publishable', 'Bootstrap Publishable Key', $4)`,
		pubKeyID, pubPrefix, pubHashHex, tenantID)
	if err != nil {
		_ = tx.Rollback()
		fatal("create pub key: %v", err)
	}

	if err := tx.Commit(); err != nil {
		fatal("commit: %v", err)
	}

	// Create initial admin user for dashboard login
	adminEmail := os.Getenv("VELOX_ADMIN_EMAIL")
	if adminEmail == "" {
		adminEmail = "admin@velox.dev"
	}
	adminPassword := os.Getenv("VELOX_ADMIN_PASSWORD")
	if adminPassword == "" {
		adminPassword = "changeme"
	}

	userAuthSvc := userauth.NewService(db)
	_, err = userAuthSvc.Register(ctx, tenantID, adminEmail, adminPassword, "Admin")
	if err != nil {
		// Non-fatal — user might already exist from a previous bootstrap
		slog.Warn("could not create admin user (may already exist)", "error", err)
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
	fmt.Printf("  Dashboard login: %s / %s\n", adminEmail, adminPassword)
	fmt.Println()
	fmt.Println("  Try it:")
	fmt.Printf("  curl -H 'Authorization: Bearer %s' http://localhost:8080/v1/customers\n", rawKey)
	fmt.Println("========================================")
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "ERROR: "+format+"\n", args...)
	os.Exit(1)
}
