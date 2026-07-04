package api

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/api/timefilter"
	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/customer"
	"github.com/sagarsuperuser/velox/internal/invoice"
	"github.com/sagarsuperuser/velox/internal/subscription"
	"github.com/sagarsuperuser/velox/internal/usage"
)

// Sprint 2 — data export. Streaming CSV endpoints so a tenant can
// take their data out, clearing the "I take my data" review during
// procurement. RLS-scoped via the standard Bearer-key auth chain;
// each endpoint reuses the existing List method on its store.
//
// Streaming: pages of exportPageSize rows, written to csv.Writer,
// flushed each page so the HTTP buffer never holds the full result
// set. Operators can export millions of rows without OOM.
//
// Date filters: every endpoint accepts ?from=<RFC3339>&to=<RFC3339>;
// usage_events REQUIRES both (the table is unbounded — without a
// range, an export would walk the entire history).

const (
	// exportPageSize is the rows-per-store-query the export streams.
	// Capped at 100 to match every list store's clamp ceiling (see
	// invoice/postgres.go, customer/postgres.go, etc.). Pre-2026-05-28
	// this was 1000, but stores silently truncated to 50 for
	// over-cap asks — so the export was actually fetching first-page-
	// only and the pagination loop's `len(rows) < exportPageSize` check
	// always broke after one iteration. Aligning with the store cap
	// (now an honest clamp) lets the loop iterate correctly.
	//
	// Usage events tolerate higher per-page (their store caps at 1000),
	// but 100 is fine — they paginate through the same loop, just more
	// iterations for the same total throughput.
	exportPageSize = 100

	// usageExportMaxSpanDays caps the date range for usage_events
	// exports. Larger spans should be split into multiple calls so a
	// single export doesn't lock the table.
	usageExportMaxSpanDays = 366
)

type exportsHandler struct {
	customers     *customer.PostgresStore
	invoices      *invoice.PostgresStore
	subscriptions *subscription.PostgresStore
	usage         *usage.PostgresStore
}

func newExportsHandler(c *customer.PostgresStore, i *invoice.PostgresStore, s *subscription.PostgresStore, u *usage.PostgresStore) *exportsHandler {
	return &exportsHandler{
		customers:     c,
		invoices:      i,
		subscriptions: s,
		usage:         u,
	}
}

func (h *exportsHandler) Routes(customerRead, invoiceRead, subscriptionRead, usageRead func(http.Handler) http.Handler) chi.Router {
	r := chi.NewRouter()
	r.With(customerRead).Get("/customers.csv", h.exportCustomers)
	r.With(invoiceRead).Get("/invoices.csv", h.exportInvoices)
	r.With(subscriptionRead).Get("/subscriptions.csv", h.exportSubscriptions)
	r.With(usageRead).Get("/usage-events.csv", h.exportUsageEvents)
	return r
}

// parseDateRange reads ?from / ?to off the request. Thin wrapper
// around timefilter.ParseRange so exports share the same date-filter
// contract as audit-log queries + usage queries — RFC3339 OR
// YYYY-MM-DD, both accepted everywhere. Returns zero time.Time when
// missing; callers branch on IsZero to decide whether to apply the
// filter post-load (for stores that don't have native created_at
// filtering on their ListFilter).
func parseDateRange(r *http.Request) (from, to time.Time, err error) {
	return timefilter.ParseRange(r, "from", "to")
}

// writeCSVHeaders sets the response headers for a CSV download. The
// timestamp suffix on the filename means re-exports don't clobber.
func writeCSVHeaders(w http.ResponseWriter, filenameStem string) *csv.Writer {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	// UTC filename stamp — must be stable/unambiguous, not vary by operator TZ.
	stamp := time.Now().UTC().Format("20060102-150405") //tz:ok
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s-%s.csv"`, filenameStem, stamp))
	w.Header().Set("Cache-Control", "no-store")
	return csv.NewWriter(w)
}

// csvSafe neutralizes spreadsheet (CSV) formula injection. A cell whose first
// character is one of = + - @ TAB CR is interpreted as a formula by Excel,
// Google Sheets, and LibreOffice — so a customer-controlled value like
// "=HYPERLINK(...)" or "@SUM(...)" executes when the operator opens the export.
// Prefixing such values with a single quote forces them to render as text.
// Empty strings pass through. Applied to free-text, externally-controlled
// columns (display names, external IDs, emails, codes, idempotency keys).
func csvSafe(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', 0x09, 0x0d:
		return "'" + s
	}
	return s
}

// flushAndContinue flushes the csv buffer to the underlying response
// so streaming clients see partial output. Without the flush, large
// exports buffer until completion.
func flushAndContinue(cw *csv.Writer, w http.ResponseWriter) error {
	cw.Flush()
	if err := cw.Error(); err != nil {
		return err
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

// exportAbort makes a mid-stream failure visible IN the file. The HTTP
// status is already 200 by the time a store error or ctx timeout hits
// (headers went out with the first row), so the stream used to just
// END — a silently truncated CSV indistinguishable from a complete
// export, feeding partial books into whatever the operator reconciles
// against. The trailing EXPORT_INCOMPLETE record is the honest,
// machine-checkable signal (documented contract: a row whose first
// cell is EXPORT_INCOMPLETE means discard the file and retry), plus a
// server-side error log. Best-effort by construction: if the client
// connection itself died, the marker write fails silently — that
// client isn't reading anyway.
func exportAbort(ctx context.Context, cw *csv.Writer, resource string, err error) {
	slog.ErrorContext(ctx, "csv export aborted mid-stream — emitted EXPORT_INCOMPLETE marker",
		"resource", resource, "error", err)
	_ = cw.Write([]string{"EXPORT_INCOMPLETE", "the export aborted before completion — discard this file and retry"})
	cw.Flush()
}

// timePtrCSV formats a *time.Time as RFC3339 or empty string. Used
// for nullable columns (canceled_at, paid_at, voided_at, etc).
func timePtrCSV(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// ---- Customers ----

func (h *exportsHandler) exportCustomers(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	if tenantID == "" {
		respond.Unauthorized(w, r, "missing tenant context")
		return
	}
	from, to, err := parseDateRange(r)
	if err != nil {
		respond.FromError(w, r, err, "export")
		return
	}

	cw := writeCSVHeaders(w, "customers")
	defer cw.Flush()

	if err := cw.Write([]string{
		"id", "external_id", "display_name", "email", "status",
		"email_status", "created_at", "updated_at",
	}); err != nil {
		return
	}

	offset := 0
	for {
		filter := customer.ListFilter{
			TenantID: tenantID,
			Limit:    exportPageSize,
			Offset:   offset,
		}
		rows, _, err := h.customers.List(r.Context(), filter)
		if err != nil {
			exportAbort(r.Context(), cw, "customers", err)
			return
		}
		if len(rows) == 0 {
			break
		}
		for _, c := range rows {
			if !from.IsZero() && c.CreatedAt.Before(from) {
				continue
			}
			if !to.IsZero() && c.CreatedAt.After(to) {
				continue
			}
			if err := cw.Write([]string{
				c.ID, csvSafe(c.ExternalID), csvSafe(c.DisplayName), csvSafe(c.Email),
				string(c.Status),
				string(c.EmailStatus),
				c.CreatedAt.UTC().Format(time.RFC3339),
				c.UpdatedAt.UTC().Format(time.RFC3339),
			}); err != nil {
				return
			}
		}
		if err := flushAndContinue(cw, w); err != nil {
			return
		}
		if len(rows) < exportPageSize {
			break
		}
		offset += exportPageSize
	}
}

// ---- Invoices ----

func (h *exportsHandler) exportInvoices(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	if tenantID == "" {
		respond.Unauthorized(w, r, "missing tenant context")
		return
	}
	from, to, err := parseDateRange(r)
	if err != nil {
		respond.FromError(w, r, err, "export")
		return
	}

	cw := writeCSVHeaders(w, "invoices")
	defer cw.Flush()

	if err := cw.Write([]string{
		"id", "invoice_number", "customer_id", "subscription_id",
		"status", "payment_status", "currency",
		"subtotal_cents", "tax_amount_cents", "discount_cents",
		"total_amount_cents", "amount_due_cents", "amount_paid_cents",
		"credits_applied_cents",
		"billing_period_start", "billing_period_end",
		"issued_at", "due_at", "paid_at", "voided_at",
		"created_at",
	}); err != nil {
		return
	}

	offset := 0
	for {
		filter := invoice.ListFilter{
			TenantID: tenantID,
			Limit:    exportPageSize,
			Offset:   offset,
		}
		rows, _, err := h.invoices.List(r.Context(), filter)
		if err != nil {
			exportAbort(r.Context(), cw, "invoices", err)
			return
		}
		if len(rows) == 0 {
			break
		}
		for _, inv := range rows {
			if !from.IsZero() && inv.CreatedAt.Before(from) {
				continue
			}
			if !to.IsZero() && inv.CreatedAt.After(to) {
				continue
			}
			if err := cw.Write([]string{
				inv.ID, csvSafe(inv.InvoiceNumber), inv.CustomerID, inv.SubscriptionID,
				string(inv.Status), string(inv.PaymentStatus), inv.Currency,
				strconv.FormatInt(inv.SubtotalCents, 10),
				strconv.FormatInt(inv.TaxAmountCents, 10),
				strconv.FormatInt(inv.DiscountCents, 10),
				strconv.FormatInt(inv.TotalAmountCents, 10),
				strconv.FormatInt(inv.AmountDueCents, 10),
				strconv.FormatInt(inv.AmountPaidCents, 10),
				strconv.FormatInt(inv.CreditsAppliedCents, 10),
				inv.BillingPeriodStart.UTC().Format(time.RFC3339),
				inv.BillingPeriodEnd.UTC().Format(time.RFC3339),
				timePtrCSV(inv.IssuedAt),
				timePtrCSV(inv.DueAt),
				timePtrCSV(inv.PaidAt),
				timePtrCSV(inv.VoidedAt),
				inv.CreatedAt.UTC().Format(time.RFC3339),
			}); err != nil {
				return
			}
		}
		if err := flushAndContinue(cw, w); err != nil {
			return
		}
		if len(rows) < exportPageSize {
			break
		}
		offset += exportPageSize
	}
}

// ---- Subscriptions ----

func (h *exportsHandler) exportSubscriptions(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	if tenantID == "" {
		respond.Unauthorized(w, r, "missing tenant context")
		return
	}
	from, to, err := parseDateRange(r)
	if err != nil {
		respond.FromError(w, r, err, "export")
		return
	}

	cw := writeCSVHeaders(w, "subscriptions")
	defer cw.Flush()

	if err := cw.Write([]string{
		"id", "code", "display_name", "customer_id",
		"status", "billing_time",
		"trial_start_at", "trial_end_at",
		"started_at", "activated_at", "canceled_at",
		"current_billing_period_start", "current_billing_period_end",
		"next_billing_at",
		"plan_ids", // pipe-delimited list of plans on the sub
		"created_at",
	}); err != nil {
		return
	}

	offset := 0
	for {
		filter := subscription.ListFilter{
			TenantID: tenantID,
			Limit:    exportPageSize,
			Offset:   offset,
		}
		rows, _, err := h.subscriptions.List(r.Context(), filter)
		if err != nil {
			exportAbort(r.Context(), cw, "subscriptions", err)
			return
		}
		if len(rows) == 0 {
			break
		}
		for _, sub := range rows {
			if !from.IsZero() && sub.CreatedAt.Before(from) {
				continue
			}
			if !to.IsZero() && sub.CreatedAt.After(to) {
				continue
			}
			planIDs := ""
			for i, item := range sub.Items {
				if i > 0 {
					planIDs += "|"
				}
				planIDs += item.PlanID
			}
			if err := cw.Write([]string{
				sub.ID, csvSafe(sub.Code), csvSafe(sub.DisplayName), sub.CustomerID,
				string(sub.Status), string(sub.BillingTime),
				timePtrCSV(sub.TrialStartAt), timePtrCSV(sub.TrialEndAt),
				timePtrCSV(sub.StartedAt), timePtrCSV(sub.ActivatedAt),
				timePtrCSV(sub.CanceledAt),
				timePtrCSV(sub.CurrentBillingPeriodStart),
				timePtrCSV(sub.CurrentBillingPeriodEnd),
				timePtrCSV(sub.NextBillingAt),
				planIDs,
				sub.CreatedAt.UTC().Format(time.RFC3339),
			}); err != nil {
				return
			}
		}
		if err := flushAndContinue(cw, w); err != nil {
			return
		}
		if len(rows) < exportPageSize {
			break
		}
		offset += exportPageSize
	}
}

// ---- Usage events ----

func (h *exportsHandler) exportUsageEvents(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())
	if tenantID == "" {
		respond.Unauthorized(w, r, "missing tenant context")
		return
	}
	from, to, err := parseDateRange(r)
	if err != nil {
		respond.FromError(w, r, err, "export")
		return
	}
	// Required range — this table is unbounded.
	if from.IsZero() || to.IsZero() {
		respond.BadRequest(w, r, "usage-events export requires `from` and `to` query params (RFC3339)")
		return
	}
	if to.Sub(from) > usageExportMaxSpanDays*24*time.Hour {
		respond.BadRequest(w, r, fmt.Sprintf("date range exceeds %d days — split into multiple calls", usageExportMaxSpanDays))
		return
	}

	cw := writeCSVHeaders(w, "usage-events")
	defer cw.Flush()

	if err := cw.Write([]string{
		"id", "customer_id", "meter_id",
		"quantity", "timestamp",
		"idempotency_key", "origin",
		"dimensions_json",
	}); err != nil {
		return
	}

	offset := 0
	for {
		filter := usage.ListFilter{
			TenantID: tenantID,
			From:     &from,
			To:       &to,
			Limit:    exportPageSize,
			Offset:   offset,
		}
		rows, _, err := h.usage.List(r.Context(), filter)
		if err != nil {
			exportAbort(r.Context(), cw, "usage_events", err)
			return
		}
		if len(rows) == 0 {
			break
		}
		for _, ev := range rows {
			dimsJSON := ""
			if len(ev.Dimensions) > 0 {
				if b, err := json.Marshal(ev.Dimensions); err == nil {
					dimsJSON = string(b)
				}
			}
			if err := cw.Write([]string{
				ev.ID, ev.CustomerID, ev.MeterID,
				ev.Quantity.String(),
				ev.Timestamp.UTC().Format(time.RFC3339),
				csvSafe(ev.IdempotencyKey),
				string(ev.Origin),
				dimsJSON,
			}); err != nil {
				return
			}
		}
		if err := flushAndContinue(cw, w); err != nil {
			return
		}
		if len(rows) < exportPageSize {
			break
		}
		offset += exportPageSize
	}
}
