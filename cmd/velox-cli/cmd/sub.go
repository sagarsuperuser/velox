package cmd

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/sagarsuperuser/velox/cmd/velox-cli/internal/client"
	"github.com/sagarsuperuser/velox/cmd/velox-cli/internal/output"
)

// newSubCmd builds the `velox sub` parent + its subcommands. Today
// only `list` exists; future operator surfaces (cancel, pause, …)
// hang off the same parent.
func newSubCmd() *cobra.Command {
	parent := &cobra.Command{
		Use:   "sub",
		Short: "Manage subscriptions",
	}
	parent.AddCommand(newSubListCmd())
	return parent
}

// subscriptionRow is the projection of a Velox subscription into the
// fields the operator CLI displays. We deliberately decode only the
// columns we render — the API returns ~20+ fields but a CLI list
// should fit on one terminal row, so widening it is a UI decision,
// not a JSON-shape decision.
type subscriptionRow struct {
	ID                      string     `json:"id"`
	CustomerID              string     `json:"customer_id"`
	Status                  string     `json:"status"`
	CurrentBillingPeriodEnd *time.Time `json:"current_billing_period_end,omitempty"`
	// Items[].PlanID is what we render under "PLAN". Multi-item subs
	// show a comma-joined list; we keep this raw so --output json is
	// structurally identical to the server response.
	Items []struct {
		PlanID string `json:"plan_id"`
	} `json:"items,omitempty"`
}

// listResponse mirrors respond.List in the server: {"data": [...], "total": N}.
type listResponse[T any] struct {
	Data  []T `json:"data"`
	Total int `json:"total"`
}

func newSubListCmd() *cobra.Command {
	var (
		flagCustomer string
		flagPlan     string
		flagStatus   string
		flagLimit    int
		flagOutput   string
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List subscriptions",
		Long:  "List subscriptions, filterable by customer, plan, or status. Default output is a tab-aligned table; use --output json to pipe into jq or another tool.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := output.ParseFormat(flagOutput)
			if err != nil {
				return err
			}
			c, err := resolveAuth()
			if err != nil {
				return err
			}
			return runSubList(cmdContext(cmd), cmd.OutOrStdout(), c, subListParams{
				customer: flagCustomer,
				plan:     flagPlan,
				status:   flagStatus,
				limit:    flagLimit,
				format:   format,
			})
		},
	}

	cmd.Flags().StringVar(&flagCustomer, "customer", "", "Filter by customer_id")
	cmd.Flags().StringVar(&flagPlan, "plan", "", "Filter by plan_id (matches subscriptions with at least one item on this plan)")
	cmd.Flags().StringVar(&flagStatus, "status", "", "Filter by status (e.g. trialing, active, paused, canceled)")
	cmd.Flags().IntVar(&flagLimit, "limit", 20, "Maximum number of subscriptions to return")
	cmd.Flags().StringVar(&flagOutput, "output", "text", "Output format: text or json")

	return cmd
}

type subListParams struct {
	customer string
	plan     string
	status   string
	limit    int
	format   output.Format
}

// runSubList is the testable core of `velox sub list`. It is split out
// from the cobra wiring so unit tests can call it directly with a
// httptest-backed *client.Client and an in-memory io.Writer.
func runSubList(ctx context.Context, w io.Writer, c *client.Client, p subListParams) error {
	q := url.Values{}
	if p.customer != "" {
		q.Set("customer_id", p.customer)
	}
	if p.plan != "" {
		q.Set("plan_id", p.plan)
	}
	if p.status != "" {
		q.Set("status", p.status)
	}
	if p.limit > 0 {
		q.Set("limit", strconv.Itoa(p.limit))
	}

	var resp listResponse[subscriptionRow]
	if err := c.Get(ctx, "/v1/subscriptions", q, &resp); err != nil {
		return err
	}

	if p.format == output.FormatJSON {
		return output.JSON(w, resp)
	}

	rows := make([][]string, 0, len(resp.Data))
	for _, s := range resp.Data {
		rows = append(rows, []string{
			s.ID,
			s.CustomerID,
			joinPlanIDs(s.Items),
			s.Status,
			formatTimePtr(s.CurrentBillingPeriodEnd),
		})
	}
	if err := output.Table(w, []string{"ID", "CUSTOMER", "PLAN", "STATUS", "CURRENT_PERIOD_END"}, rows); err != nil {
		return err
	}
	if len(resp.Data) == 0 {
		fmt.Fprintln(w, "(no subscriptions matched)")
	}
	return nil
}

// joinPlanIDs renders the items[].plan_id slice as a single column.
// Multi-item subs comma-join; single-item subs render flat. Empty
// items returns "—" so the column is never blank (a blank cell in a
// terminal is hard to scan past).
func joinPlanIDs(items []struct {
	PlanID string `json:"plan_id"`
}) string {
	if len(items) == 0 {
		return "—"
	}
	out := items[0].PlanID
	for i := 1; i < len(items); i++ {
		out += "," + items[i].PlanID
	}
	if out == "" {
		return "—"
	}
	return out
}

// formatTimePtr renders a *time.Time as RFC3339 in UTC. A nil pointer
// (sub never had a cycle yet) renders as "—".
func formatTimePtr(t *time.Time) string {
	if t == nil {
		return "—"
	}
	return t.UTC().Format(time.RFC3339)
}
