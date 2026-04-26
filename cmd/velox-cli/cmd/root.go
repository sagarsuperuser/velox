// Package cmd holds the cobra command tree for velox-cli. Each
// subcommand lives in its own file (sub.go, invoice.go) and registers
// itself by adding to the root command.
package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/sagarsuperuser/velox/cmd/velox-cli/internal/client"
)

// CLI version. Bumped manually with notable changes; not coupled to
// the API version since the CLI is a thin client.
const version = "0.1.0"

// Global flag values populated by cobra. Subcommands read these via
// resolveAuth() so the env-var fallback is consistent across the tree.
var (
	flagAPIKey string
	flagAPIURL string
)

// Env-var names. Documented in the README and printed by --help.
const (
	envAPIKey = "VELOX_API_KEY"
	envAPIURL = "VELOX_API_URL"
)

// NewRootCmd builds the root cobra command. It is a function (not a
// package-level singleton) so tests can build a fresh tree per run
// without shared-state leaks.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "velox-cli",
		Short:         "Operator CLI for Velox",
		Long:          "velox-cli talks to a running Velox API to run common operator tasks (list subscriptions, send invoices, …) using a platform API key.",
		Version:       version,
		SilenceErrors: true, // we print the error in main; cobra would double-print.
		SilenceUsage:  true, // usage on error is noise once a real call has been made.
	}

	root.PersistentFlags().StringVar(&flagAPIKey, "api-key", "", fmt.Sprintf("Velox platform API key (env: %s)", envAPIKey))
	root.PersistentFlags().StringVar(&flagAPIURL, "api-url", "", fmt.Sprintf("Velox API base URL (env: %s, default: %s)", envAPIURL, client.DefaultBaseURL))

	root.AddCommand(newSubCmd())
	root.AddCommand(newInvoiceCmd())

	return root
}

// resolveAuth merges flag + env into the values the client needs. Flag
// wins (so an operator can override an env-set key for one invocation).
// Returns a built *client.Client and the resolved key — the latter is
// returned only so subcommand tests can assert on it without re-deriving.
func resolveAuth() (*client.Client, error) {
	apiKey := flagAPIKey
	if apiKey == "" {
		apiKey = os.Getenv(envAPIKey)
	}
	if apiKey == "" {
		return nil, fmt.Errorf("missing API key — set %s or pass --api-key (use a platform API key from the Velox dashboard)", envAPIKey)
	}

	apiURL := flagAPIURL
	if apiURL == "" {
		apiURL = os.Getenv(envAPIURL)
	}

	return client.New(apiURL, apiKey), nil
}

// cmdContext returns the request context used for an API call. Today
// it's just the cobra cmd's context so signal cancellation works; we
// keep the indirection so timeout / tracing wiring lands in one place
// later.
func cmdContext(cmd *cobra.Command) context.Context {
	if c := cmd.Context(); c != nil {
		return c
	}
	return context.Background()
}
