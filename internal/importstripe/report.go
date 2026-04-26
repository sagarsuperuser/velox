package importstripe

import (
	"encoding/csv"
	"fmt"
	"io"
	"strings"
)

// Action enumerates the four per-row outcomes of an import. Mirrors the
// table in docs/design-stripe-importer.md.
type Action string

const (
	ActionInsert          Action = "insert"
	ActionSkipEquivalent  Action = "skip-equivalent"
	ActionSkipDivergent   Action = "skip-divergent"
	ActionError           Action = "error"
)

// Resource identifies the kind of Stripe object an action applies to.
// Phase 0 only writes "customer", but the report shape supports later phases
// without a format change.
type Resource string

const (
	ResourceCustomer     Resource = "customer"
	ResourceSubscription Resource = "subscription"
	ResourceProduct      Resource = "product"
	ResourcePrice        Resource = "price"
	ResourceInvoice      Resource = "invoice"
)

// Row is one CSV line in the importer report.
type Row struct {
	StripeID string
	Resource Resource
	Action   Action
	VeloxID  string
	// Detail carries the human-readable error message for `error` rows and
	// the field-by-field diff summary for `skip-divergent` rows. Empty for
	// `insert` and `skip-equivalent`.
	Detail string
}

// Report writes import outcomes to a CSV stream and tracks per-action
// counts for the end-of-run summary line.
type Report struct {
	w *csv.Writer
	// Counts of each Action observed. Used by the CLI to print the summary
	// and decide on exit code (non-zero Errors -> exit 1).
	Inserted        int
	SkippedEquiv    int
	SkippedDivergent int
	Errored         int
}

// NewReport opens a CSV report on out. The header row is written immediately.
func NewReport(out io.Writer) (*Report, error) {
	w := csv.NewWriter(out)
	if err := w.Write([]string{"stripe_id", "resource", "action", "velox_id", "detail"}); err != nil {
		return nil, fmt.Errorf("write csv header: %w", err)
	}
	return &Report{w: w}, nil
}

// Write appends one row to the report and updates counts.
func (r *Report) Write(row Row) error {
	if err := r.w.Write([]string{
		row.StripeID,
		string(row.Resource),
		string(row.Action),
		row.VeloxID,
		strings.ReplaceAll(row.Detail, "\n", " "),
	}); err != nil {
		return fmt.Errorf("write csv row: %w", err)
	}
	switch row.Action {
	case ActionInsert:
		r.Inserted++
	case ActionSkipEquivalent:
		r.SkippedEquiv++
	case ActionSkipDivergent:
		r.SkippedDivergent++
	case ActionError:
		r.Errored++
	}
	return nil
}

// Total returns the number of rows written (excluding header).
func (r *Report) Total() int {
	return r.Inserted + r.SkippedEquiv + r.SkippedDivergent + r.Errored
}

// Close flushes the CSV buffer. Returns any error from the underlying writer.
func (r *Report) Close() error {
	r.w.Flush()
	return r.w.Error()
}
