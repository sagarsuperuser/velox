package cmd

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/sagarsuperuser/velox/cmd/velox-cli/internal/client"
	"github.com/sagarsuperuser/velox/cmd/velox-cli/internal/output"
)

// newInvoiceCmd builds the `velox invoice` parent + subcommands.
func newInvoiceCmd() *cobra.Command {
	parent := &cobra.Command{
		Use:   "invoice",
		Short: "Manage invoices",
	}
	parent.AddCommand(newInvoiceSendCmd())
	return parent
}

// invoiceSendResponse is the API's reply to POST /v1/invoices/{id}/send.
// The server returns {"status": "sent"} on success; we keep extra
// fields tolerant so a future server tweak doesn't immediately break
// the CLI.
type invoiceSendResponse struct {
	Status string `json:"status"`
}

// invoiceSendDryRun is what we emit when --dry-run is set and we
// short-circuit before the network call. It's structurally compatible
// with invoiceSendResponse so JSON output stays one shape.
type invoiceSendDryRun struct {
	Status    string `json:"status"`
	InvoiceID string `json:"invoice_id"`
	Email     string `json:"email"`
	DryRun    bool   `json:"dry_run"`
}

func newInvoiceSendCmd() *cobra.Command {
	var (
		flagInvoice string
		flagEmail   string
		flagDryRun  bool
		flagOutput  string
	)

	cmd := &cobra.Command{
		Use:   "send",
		Short: "Send an existing invoice by email",
		Long: "Send an existing finalized invoice as a PDF attachment to the supplied email address.\n\n" +
			"This is a thin client over POST /v1/invoices/{id}/send. The server requires an email — pass --email " +
			"to override the customer's billing-profile email, or use --dry-run to verify the invoke shape without " +
			"hitting the send endpoint.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if flagInvoice == "" {
				return fmt.Errorf("--invoice is required")
			}
			if flagEmail == "" && !flagDryRun {
				return fmt.Errorf("--email is required (or pass --dry-run)")
			}
			format, err := output.ParseFormat(flagOutput)
			if err != nil {
				return err
			}
			c, err := resolveAuth()
			if err != nil {
				return err
			}
			return runInvoiceSend(cmdContext(cmd), cmd.OutOrStdout(), c, invoiceSendParams{
				invoiceID: flagInvoice,
				email:     flagEmail,
				dryRun:    flagDryRun,
				format:    format,
			})
		},
	}

	cmd.Flags().StringVar(&flagInvoice, "invoice", "", "Invoice ID to send (required)")
	cmd.Flags().StringVar(&flagEmail, "email", "", "Recipient email address (required unless --dry-run)")
	cmd.Flags().BoolVar(&flagDryRun, "dry-run", false, "Print the request shape without calling the API")
	cmd.Flags().StringVar(&flagOutput, "output", "text", "Output format: text or json")

	return cmd
}

type invoiceSendParams struct {
	invoiceID string
	email     string
	dryRun    bool
	format    output.Format
}

// runInvoiceSend is the testable core of `velox invoice send`.
func runInvoiceSend(ctx context.Context, w io.Writer, c *client.Client, p invoiceSendParams) error {
	path := fmt.Sprintf("/v1/invoices/%s/send", p.invoiceID)

	if p.dryRun {
		preview := invoiceSendDryRun{
			Status:    "would_send",
			InvoiceID: p.invoiceID,
			Email:     p.email,
			DryRun:    true,
		}
		if p.format == output.FormatJSON {
			return output.JSON(w, preview)
		}
		_, err := fmt.Fprintf(w, "DRY RUN: would POST %s with email=%q\n", path, p.email)
		return err
	}

	body := map[string]string{"email": p.email}
	var resp invoiceSendResponse
	if err := c.Post(ctx, path, body, &resp); err != nil {
		return err
	}
	if p.format == output.FormatJSON {
		return output.JSON(w, resp)
	}
	status := resp.Status
	if status == "" {
		status = "ok"
	}
	_, err := fmt.Fprintf(w, "invoice %s sent to %s (%s)\n", p.invoiceID, p.email, status)
	return err
}
