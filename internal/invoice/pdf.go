package invoice

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/go-pdf/fpdf"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// CompanyInfo holds tenant company details for the PDF header.
type CompanyInfo struct {
	Name    string
	Email   string
	Phone   string
	Address string
}

// CreditNoteInfo holds credit note data for the totals section.
type CreditNoteInfo struct {
	Number string
	Reason string
	Amount int64
}

func RenderPDF(inv domain.Invoice, lineItems []domain.InvoiceLineItem, customerName string, creditNotes []CreditNoteInfo, company ...CompanyInfo) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetAutoPageBreak(true, 20)
	pdf.AddPage()

	// ── Header: Company info (left) + INVOICE title (right) ──
	companyName := "Velox"
	companyAddr := ""
	companyContact := ""
	if len(company) > 0 && company[0].Name != "" {
		companyName = company[0].Name
		companyAddr = company[0].Address
		parts := []string{}
		if company[0].Email != "" {
			parts = append(parts, company[0].Email)
		}
		if company[0].Phone != "" {
			parts = append(parts, company[0].Phone)
		}
		if len(parts) > 0 {
			companyContact = strings.Join(parts, "  |  ")
		}
	}

	// Company name
	pdf.SetFont("Helvetica", "B", 18)
	pdf.SetTextColor(30, 30, 30)
	pdf.CellFormat(100, 10, companyName, "", 0, "L", false, 0, "")

	// INVOICE label (right)
	pdf.SetFont("Helvetica", "B", 18)
	pdf.SetTextColor(100, 100, 100)
	pdf.CellFormat(0, 10, "INVOICE", "", 1, "R", false, 0, "")

	// Company address
	if companyAddr != "" {
		pdf.SetFont("Helvetica", "", 8)
		pdf.SetTextColor(100, 100, 100)
		for _, line := range strings.Split(companyAddr, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				pdf.CellFormat(0, 4, line, "", 1, "L", false, 0, "")
			}
		}
	}
	if companyContact != "" {
		pdf.SetFont("Helvetica", "", 8)
		pdf.SetTextColor(120, 120, 120)
		pdf.CellFormat(0, 4, companyContact, "", 1, "L", false, 0, "")
	}

	pdf.Ln(8)

	// ── Invoice details (left) + Bill To (right) ──
	y := pdf.GetY()

	// Left column: invoice details
	pdf.SetFont("Helvetica", "B", 9)
	pdf.SetTextColor(100, 100, 100)
	pdf.CellFormat(100, 5, "INVOICE DETAILS", "", 1, "L", false, 0, "")
	pdf.Ln(1)

	pdf.SetTextColor(40, 40, 40)
	detailRow(pdf, "Number", inv.InvoiceNumber)
	if inv.IssuedAt != nil {
		detailRow(pdf, "Issued", inv.IssuedAt.Format("January 2, 2006"))
	}
	if inv.DueAt != nil {
		detailRow(pdf, "Due Date", inv.DueAt.Format("January 2, 2006"))
	}
	detailRow(pdf, "Period", fmt.Sprintf("%s - %s",
		inv.BillingPeriodStart.Format("Jan 2, 2006"),
		inv.BillingPeriodEnd.Format("Jan 2, 2006")))
	detailRow(pdf, "Currency", strings.ToUpper(inv.Currency))

	bottomLeft := pdf.GetY()

	// Right column: bill to
	pdf.SetY(y)
	pdf.SetX(120)
	pdf.SetFont("Helvetica", "B", 9)
	pdf.SetTextColor(100, 100, 100)
	pdf.CellFormat(0, 5, "BILL TO", "", 1, "L", false, 0, "")
	pdf.Ln(1)

	pdf.SetX(120)
	pdf.SetFont("Helvetica", "", 10)
	pdf.SetTextColor(40, 40, 40)
	pdf.CellFormat(0, 5, customerName, "", 1, "L", false, 0, "")

	bottomRight := pdf.GetY()
	if bottomLeft > bottomRight {
		pdf.SetY(bottomLeft)
	}

	pdf.Ln(8)

	// ── Line items table ──
	pdf.SetFillColor(245, 245, 245)
	pdf.SetFont("Helvetica", "B", 9)
	pdf.SetTextColor(80, 80, 80)

	colWidths := []float64{85, 30, 35, 40}
	headers := []string{"Description", "Qty", "Unit Price", "Amount"}
	for i, h := range headers {
		align := "L"
		if i > 0 {
			align = "R"
		}
		pdf.CellFormat(colWidths[i], 8, h, "", 0, align, true, 0, "")
	}
	pdf.Ln(-1)

	// Line items
	pdf.SetFont("Helvetica", "", 9)
	pdf.SetTextColor(40, 40, 40)

	for _, item := range lineItems {
		pdf.CellFormat(colWidths[0], 7, truncate(item.Description, 50), "", 0, "L", false, 0, "")
		pdf.CellFormat(colWidths[1], 7, formatQuantity(item.Quantity, item.LineType), "", 0, "R", false, 0, "")
		pdf.CellFormat(colWidths[2], 7, formatCents(item.UnitAmountCents), "", 0, "R", false, 0, "")
		pdf.CellFormat(colWidths[3], 7, formatCents(item.TotalAmountCents), "", 0, "R", false, 0, "")
		pdf.Ln(-1)
	}

	// ── Totals section ──
	pdf.Ln(2)
	pdf.SetDrawColor(200, 200, 200)
	pdf.Line(10, pdf.GetY(), 200, pdf.GetY())
	pdf.Ln(4)

	totalsX := 120.0
	totalsW := 50.0
	valW := 30.0

	// Subtotal
	pdf.SetFont("Helvetica", "", 10)
	pdf.SetTextColor(80, 80, 80)
	pdf.SetX(totalsX)
	pdf.CellFormat(totalsW, 6, "Subtotal", "", 0, "L", false, 0, "")
	pdf.CellFormat(valW, 6, formatCents(inv.SubtotalCents), "", 1, "R", false, 0, "")

	// Credit notes
	if len(creditNotes) > 0 {
		pdf.SetFont("Helvetica", "", 9)
		pdf.SetTextColor(0, 128, 80) // green
		for _, cn := range creditNotes {
			label := fmt.Sprintf("CN %s", cn.Number)
			if cn.Reason != "" {
				label += " - " + truncate(cn.Reason, 25)
			}
			pdf.SetX(totalsX)
			pdf.CellFormat(totalsW, 6, label, "", 0, "L", false, 0, "")
			pdf.CellFormat(valW, 6, "-"+formatCents(cn.Amount), "", 1, "R", false, 0, "")
		}
	}

	// Discount
	if inv.DiscountCents > 0 {
		pdf.SetFont("Helvetica", "", 10)
		pdf.SetTextColor(80, 80, 80)
		pdf.SetX(totalsX)
		pdf.CellFormat(totalsW, 6, "Discount", "", 0, "L", false, 0, "")
		pdf.CellFormat(valW, 6, "-"+formatCents(inv.DiscountCents), "", 1, "R", false, 0, "")
	}

	// Tax
	if inv.TaxAmountCents > 0 {
		pdf.SetFont("Helvetica", "", 10)
		pdf.SetTextColor(80, 80, 80)
		pdf.SetX(totalsX)
		pdf.CellFormat(totalsW, 6, "Tax", "", 0, "L", false, 0, "")
		pdf.CellFormat(valW, 6, formatCents(inv.TaxAmountCents), "", 1, "R", false, 0, "")
	}

	// Amount Due
	pdf.Ln(2)
	pdf.SetDrawColor(200, 200, 200)
	pdf.Line(totalsX, pdf.GetY(), 200, pdf.GetY())
	pdf.Ln(3)

	pdf.SetFont("Helvetica", "B", 12)
	pdf.SetTextColor(30, 30, 30)
	pdf.SetX(totalsX)
	pdf.CellFormat(totalsW, 8, "Amount Due", "", 0, "L", false, 0, "")
	pdf.CellFormat(valW, 8, formatCents(inv.AmountDueCents), "", 1, "R", false, 0, "")

	// ── Payment terms / paid notice ──
	pdf.Ln(8)
	pdf.SetFont("Helvetica", "", 9)
	pdf.SetTextColor(120, 120, 120)
	if inv.PaymentStatus == domain.PaymentSucceeded && inv.PaidAt != nil {
		pdf.CellFormat(0, 5, fmt.Sprintf("Paid on %s - Thank you!", inv.PaidAt.Format("January 2, 2006")), "", 1, "L", false, 0, "")
	} else {
		pdf.CellFormat(0, 5, fmt.Sprintf("Payment due within %d days of issue date.", inv.NetPaymentTermDays), "", 1, "L", false, 0, "")
	}

	if inv.Memo != "" {
		pdf.Ln(4)
		pdf.SetFont("Helvetica", "I", 9)
		pdf.MultiCell(0, 5, inv.Memo, "", "L", false)
	}

	// ── Footer ──
	pdf.Ln(10)
	pdf.SetFont("Helvetica", "", 7)
	pdf.SetTextColor(170, 170, 170)
	pdf.CellFormat(0, 4, fmt.Sprintf("Generated on %s  |  %s", time.Now().UTC().Format("Jan 2, 2006 15:04 UTC"), inv.ID), "", 1, "C", false, 0, "")

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, fmt.Errorf("render pdf: %w", err)
	}
	return buf.Bytes(), nil
}

func detailRow(pdf *fpdf.Fpdf, label, value string) {
	pdf.SetFont("Helvetica", "", 9)
	pdf.SetTextColor(100, 100, 100)
	pdf.CellFormat(25, 5, label, "", 0, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 9)
	pdf.SetTextColor(40, 40, 40)
	pdf.CellFormat(75, 5, value, "", 1, "L", false, 0, "")
}

func totalRow(pdf *fpdf.Fpdf, x float64, label, value string) {
	pdf.SetX(x)
	pdf.CellFormat(40, 7, label, "", 0, "L", false, 0, "")
	pdf.CellFormat(30, 7, value, "", 1, "R", false, 0, "")
}

func formatCents(cents int64) string {
	if cents == 0 {
		return "$0.00"
	}
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	dollars := cents / 100
	remainder := cents % 100
	return fmt.Sprintf("%s$%s.%02d", sign, formatNumber(dollars), remainder)
}

func formatQuantity(qty int64, lineType domain.InvoiceLineItemType) string {
	if lineType == domain.LineTypeBaseFee {
		return "1"
	}
	return formatNumber(qty)
}

func formatNumber(n int64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var result []byte
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			result = append(result, ',')
		}
		result = append(result, byte(c))
	}
	return string(result)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
