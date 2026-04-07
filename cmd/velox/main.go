package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/sagarsuperuser/velox/internal/api"
	"github.com/sagarsuperuser/velox/internal/billing"
	"github.com/sagarsuperuser/velox/internal/config"
	"github.com/sagarsuperuser/velox/internal/platform/migrate"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	switch cmd {
	case "serve":
		serve()
	case "migrate":
		runMigrate()
	case "version":
		fmt.Println("velox 2026-04-07")
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func serve() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg, err := config.Load()
	if err != nil {
		slog.Error("load config", "error", err)
		os.Exit(1)
	}
	slog.Info("velox starting", "env", cfg.Env, "port", cfg.Port)

	pool, err := config.OpenPostgres(cfg.DB)
	if err != nil {
		slog.Error("open database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	if cfg.Migrate {
		if err := migrate.NewRunner(pool).Run(context.Background()); err != nil {
			slog.Error("run migrations", "error", err)
			os.Exit(1)
		}
		slog.Info("migrations applied")
	}

	db := postgres.NewDB(pool, cfg.DB.QueryTimeout)
	webhookSecret := strings.TrimSpace(os.Getenv("STRIPE_WEBHOOK_SECRET"))

	server := api.NewServer(db, webhookSecret)

	billingInterval := 1 * time.Hour
	if cfg.Env == "local" {
		billingInterval = 5 * time.Minute
	}
	scheduler := billing.NewScheduler(server.BillingEngine, billingInterval, 50)

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%s", cfg.Port),
		Handler:      server,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go scheduler.Start(ctx)

	go func() {
		slog.Info("listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown error", "error", err)
	}
}

func runMigrate() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}

	pool, err := config.OpenPostgres(cfg.DB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open database: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := migrate.NewRunner(pool).Run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "migration failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("migrations applied successfully")
}

func printUsage() {
	fmt.Println(`velox — usage-based billing engine

Commands:
  serve       Start the API server (default)
  migrate     Run database migrations
  version     Print version
  help        Show this help

Environment:
  DATABASE_URL              PostgreSQL connection string (required)
  PORT                      HTTP port (default: 8080)
  APP_ENV                   Environment: local, staging, production (default: local)
  RUN_MIGRATIONS_ON_BOOT    Run migrations on server start (default: false)
  STRIPE_WEBHOOK_SECRET     Stripe webhook signing secret
  VELOX_BOOTSTRAP_TOKEN     Token for POST /v1/bootstrap endpoint`)
}
