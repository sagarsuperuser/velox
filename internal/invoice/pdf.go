package invoice

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/signintech/gopdf"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/pdffonts"
)

// CompanyInfo holds tenant company details for the PDF header.
type CompanyInfo struct {
	Name         string
	Email        string
	Phone        string
	AddressLine1 string
	AddressLine2 string
	City         string
	State        string
	PostalCode   string
	Country      string
}

// CreditNoteInfo holds credit note data for the totals section.
type CreditNoteInfo struct {
	Number            string
	Reason            string
	Amount            int64
	RefundAmountCents int64
	CreditAmountCents int64
	RefundStatus      string
}

// BillToInfo holds the customer's billing address for the PDF.
type BillToInfo struct {
	Name         string
	Email        string
	AddressLine1 string
	AddressLine2 string
	City         string
	State        string
	PostalCode   string
	Country      string
}

// Currency symbol map
var currencySymbols = map[string]string{
	"USD": "$", "EUR": "€", "GBP": "£", "JPY": "¥", "CNY": "¥",
	"INR": "₹", "BRL": "R$", "CAD": "CA$", "AUD": "A$", "CHF": "CHF ",
	"SGD": "S$", "HKD": "HK$", "NZD": "NZ$", "MXN": "MX$",
	"KRW": "₩", "ZAR": "R", "PLN": "zł", "AED": "AED ", "SAR": "SAR ",
	"THB": "฿", "MYR": "RM ", "IDR": "Rp ", "PHP": "₱", "VND": "₫",
}

var pdfCurrencySymbol = "$"

func RenderPDF(inv domain.Invoice, lineItems []domain.InvoiceLineItem, billTo BillToInfo, creditNotes []CreditNoteInfo, company ...CompanyInfo) ([]byte, error) {
	// Set currency symbol
	if sym, ok := currencySymbols[strings.ToUpper(inv.Currency)]; ok {
		pdfCurrencySymbol = sym
	} else if inv.Currency != "" {
		pdfCurrencySymbol = inv.Currency + " "
	} else {
		pdfCurrencySymbol = "$"
	}

	pdf := &gopdf.GoPdf{}
	pdf.Start(gopdf.Config{PageSize: *gopdf.PageSizeA4})
	pdf.AddPage()

	if err := pdffonts.RegisterNotoSans(pdf); err != nil {
		return nil, err
	}

	// Helper functions
	setFont := func(bold bool, size int) {
		name := "noto"
		if bold {
			name = "noto-bold"
		}
		_ = pdf.SetFont(name, "", size)
	}

	setColor := func(r, g, b uint8) {
		pdf.SetTextColor(r, g, b)
	}

	textAt := func(x, y float64, text string) {
		pdf.SetXY(x, y)
		_ = pdf.Cell(nil, text)
	}

	rightAlignAt := func(x, y, width float64, text string) {
		tw, _ := pdf.MeasureTextWidth(text)
		pdf.SetXY(x+width-tw, y)
		_ = pdf.Cell(nil, text)
	}

	// ── VOID watermark ──
	if inv.Status == domain.InvoiceVoided {
		setFont(true, 72)
		setColor(230, 230, 230)
		textAt(55, 400, "VOID")
	}

	pageW := 595.28 // A4 width in points
	margin := 40.0
	contentW := pageW - margin*2
	y := 40.0

	// ── Header ──
	companyName := "Velox"
	var companyAddrLines []string
	companyContact := ""
	if len(company) > 0 && company[0].Name != "" {
		c := company[0]
		companyName = c.Name
		if c.AddressLine1 != "" {
			companyAddrLines = append(companyAddrLines, c.AddressLine1)
		}
		if c.AddressLine2 != "" {
			companyAddrLines = append(companyAddrLines, c.AddressLine2)
		}
		if cityLine := formatCityStatePostal(c.City, c.State, c.PostalCode); cityLine != "" {
			companyAddrLines = append(companyAddrLines, cityLine)
		}
		if c.Country != "" {
			companyAddrLines = append(companyAddrLines, c.Country)
		}
		parts := []string{}
		if c.Email != "" {
			parts = append(parts, c.Email)
		}
		if c.Phone != "" {
			parts = append(parts, c.Phone)
		}
		if len(parts) > 0 {
			companyContact = strings.Join(parts, "  |  ")
		}
	}

	setFont(true, 18)
	setColor(30, 30, 30)
	textAt(margin, y, companyName)

	setFont(true, 18)
	setColor(100, 100, 100)
	rightAlignAt(margin, y, contentW, "INVOICE")
	y += 24

	if len(companyAddrLines) > 0 {
		setFont(false, 8)
		setColor(100, 100, 100)
		for _, line := range companyAddrLines {
			textAt(margin, y, line)
			y += 11
		}
	}
	if companyContact != "" {
		setFont(false, 8)
		setColor(120, 120, 120)
		textAt(margin, y, companyContact)
		y += 11
	}

	y += 16

	// ── Invoice Details (left) + Bill To (right) ──
	detailStartY := y

	setFont(true, 9)
	setColor(100, 100, 100)
	textAt(margin, y, "INVOICE DETAILS")
	y += 14

	detailRow := func(label, value string) {
		setFont(false, 9)
		setColor(100, 100, 100)
		textAt(margin, y, label)
		setColor(40, 40, 40)
		textAt(margin+70, y, value)
		y += 14
	}

	detailRow("Number", inv.InvoiceNumber)
	if inv.IssuedAt != nil {
		detailRow("Issued", inv.IssuedAt.Format("January 2, 2006"))
	}
	if inv.DueAt != nil {
		detailRow("Due Date", inv.DueAt.Format("January 2, 2006"))
	}
	detailRow("Period", fmt.Sprintf("%s - %s", inv.BillingPeriodStart.Format("Jan 2, 2006"), inv.BillingPeriodEnd.Format("Jan 2, 2006")))
	detailRow("Currency", strings.ToUpper(inv.Currency))

	leftBottom := y

	// Bill To (right column)
	rightX := 340.0
	by := detailStartY

	setFont(true, 9)
	setColor(100, 100, 100)
	textAt(rightX, by, "BILL TO")
	by += 14

	setFont(true, 10)
	setColor(40, 40, 40)
	textAt(rightX, by, billTo.Name)
	by += 14

	setFont(false, 9)
	setColor(80, 80, 80)
	if billTo.AddressLine1 != "" {
		textAt(rightX, by, billTo.AddressLine1)
		by += 12
	}
	if billTo.AddressLine2 != "" {
		textAt(rightX, by, billTo.AddressLine2)
		by += 12
	}
	cityLine := formatCityStatePostal(billTo.City, billTo.State, billTo.PostalCode)
	if cityLine != "" {
		textAt(rightX, by, cityLine)
		by += 12
	}
	if billTo.Country != "" {
		textAt(rightX, by, billTo.Country)
		by += 12
	}
	if billTo.Email != "" {
		setFont(false, 8)
		setColor(100, 100, 100)
		textAt(rightX, by, billTo.Email)
		by += 12
	}

	if by > leftBottom {
		y = by
	} else {
		y = leftBottom
	}
	y += 16

	// ── Line items table ──
	colX := []float64{margin, margin + 240, margin + 320, margin + 400}
	colEnd := margin + contentW

	// Header row
	setFont(true, 9)
	setColor(80, 80, 80)
	pdf.SetFillColor(245, 245, 245)
	pdf.RectFromUpperLeftWithStyle(margin, y-2, contentW, 20, "F")

	textAt(colX[0], y, "Description")
	rightAlignAt(colX[1], y, colX[2]-colX[1], "Qty")
	rightAlignAt(colX[2], y, colX[3]-colX[2], "Unit Price")
	rightAlignAt(colX[3], y, colEnd-colX[3], "Amount")
	y += 22

	// Line items
	setFont(false, 9)
	setColor(40, 40, 40)
	for _, item := range lineItems {
		desc := item.Description
		if len([]rune(desc)) > 50 {
			desc = string([]rune(desc)[:47]) + "..."
		}
		textAt(colX[0], y, desc)
		rightAlignAt(colX[1], y, colX[2]-colX[1], formatQuantity(item.Quantity, item.LineType))
		rightAlignAt(colX[2], y, colX[3]-colX[2], formatCents(item.UnitAmountCents))
		rightAlignAt(colX[3], y, colEnd-colX[3], formatCents(item.AmountCents))
		y += 18
	}

	// Separator
	y += 4
	pdf.SetLineWidth(0.5)
	pdf.SetStrokeColor(200, 200, 200)
	pdf.Line(margin, y, margin+contentW, y)
	y += 8

	// ── Totals ──
	totalsX := 340.0
	totalsW := 140.0
	labelW := 90.0

	totalsRow := func(label string, amount string, bold bool, labelR, labelG, labelB, valR, valG, valB uint8) {
		if bold {
			setFont(true, 10)
		} else {
			setFont(false, 10)
		}
		setColor(labelR, labelG, labelB)
		textAt(totalsX, y, label)
		setColor(valR, valG, valB)
		rightAlignAt(totalsX+labelW, y, totalsW-labelW, amount)
		y += 16
	}

	totalsRow("Subtotal", formatCents(inv.SubtotalCents), false, 80, 80, 80, 40, 40, 40)

	if inv.DiscountCents > 0 {
		totalsRow("Discount", "-"+formatCents(inv.DiscountCents), false, 80, 80, 80, 40, 40, 40)
	}

	if inv.TaxAmountCents > 0 {
		taxLabel := "Tax"
		if inv.TaxName != "" {
			taxLabel = inv.TaxName
		}
		if inv.TaxRateBP > 0 {
			taxLabel = fmt.Sprintf("%s (%.4g%%)", taxLabel, float64(inv.TaxRateBP)/100)
		}
		if inv.TaxCountry != "" {
			taxLabel = fmt.Sprintf("%s [%s]", taxLabel, inv.TaxCountry)
		}
		totalsRow(taxLabel, formatCents(inv.TaxAmountCents), false, 80, 80, 80, 40, 40, 40)

		// Per-jurisdiction breakdown. Rendered indented under the aggregate
		// Tax row when lines span multiple jurisdictions (EU cross-state,
		// India CGST+SGST, US state+local). Single-jurisdiction invoices
		// skip this — the aggregate row already tells the whole story.
		jurisdictionAgg := aggregateTaxByJurisdiction(lineItems)
		if len(jurisdictionAgg) > 1 {
			setFont(false, 8)
			setColor(120, 120, 120)
			for _, row := range jurisdictionAgg {
				textAt(totalsX+12, y, row.label)
				rightAlignAt(totalsX+labelW, y, totalsW-labelW, formatCents(row.amount))
				y += 12
			}
			y += 4
		}

		// Show customer's Tax ID below the tax line
		if inv.TaxID != "" {
			setFont(false, 7)
			setColor(120, 120, 120)
			textAt(totalsX, y-12, inv.TaxID)
			y += 2
		}
	} else if inv.TaxReverseCharge {
		// Even with zero-amount tax, surface the reverse-charge row so the
		// invoice reads as a deliberate tax treatment rather than a bug.
		totalsRow("Tax (reverse charge)", formatCents(0), false, 80, 80, 80, 120, 120, 120)
	}

	// Total line
	pdf.SetStrokeColor(220, 220, 220)
	pdf.Line(totalsX, y-4, totalsX+totalsW, y-4)
	y += 2
	totalsRow("Total", formatCents(inv.TotalAmountCents), true, 30, 30, 30, 30, 30, 30)

	// Credit notes (pre-payment)
	var postPaymentCNs []CreditNoteInfo
	for _, cn := range creditNotes {
		if cn.RefundAmountCents > 0 || cn.CreditAmountCents > 0 {
			postPaymentCNs = append(postPaymentCNs, cn)
			continue
		}
		setFont(false, 8)
		setColor(0, 128, 80)
		label := cn.Number
		if cn.Reason != "" {
			r := []rune(cn.Reason)
			if len(r) > 20 {
				r = append(r[:17], []rune("...")...)
			}
			label += " - " + string(r)
		}
		textAt(totalsX, y, label)
		rightAlignAt(totalsX+labelW, y, totalsW-labelW, "-"+formatCents(cn.Amount))
		y += 16
	}

	if inv.Status == domain.InvoiceVoided {
		pdf.SetStrokeColor(200, 200, 200)
		pdf.Line(totalsX, y-4, totalsX+totalsW, y-4)
		y += 4
		setFont(true, 12)
		setColor(30, 30, 30)
		textAt(totalsX, y, "Amount Due")
		rightAlignAt(totalsX+labelW, y, totalsW-labelW, formatCents(0))
		y += 20
	} else {
		if inv.CreditsAppliedCents > 0 {
			setFont(false, 9)
			setColor(0, 128, 80)
			textAt(totalsX, y, "Prepaid credits")
			rightAlignAt(totalsX+labelW, y, totalsW-labelW, "-"+formatCents(inv.CreditsAppliedCents))
			y += 16
		}

		if inv.AmountPaidCents > 0 {
			totalsRow("Amount Paid", "-"+formatCents(inv.AmountPaidCents), false, 80, 80, 80, 40, 40, 40)
		}

		pdf.SetStrokeColor(200, 200, 200)
		pdf.Line(totalsX, y-4, totalsX+totalsW, y-4)
		y += 4
		setFont(true, 12)
		setColor(30, 30, 30)
		textAt(totalsX, y, "Amount Due")
		rightAlignAt(totalsX+labelW, y, totalsW-labelW, formatCents(inv.AmountDueCents))
		y += 20

		// Post-payment adjustments
		var completedCNs []CreditNoteInfo
		for _, cn := range postPaymentCNs {
			if cn.CreditAmountCents > 0 {
				completedCNs = append(completedCNs, cn)
			} else if cn.RefundAmountCents > 0 && cn.RefundStatus == string(domain.RefundSucceeded) {
				completedCNs = append(completedCNs, cn)
			}
		}
		if len(completedCNs) > 0 {
			y += 8
			setFont(false, 7)
			setColor(150, 150, 150)
			textAt(margin, y, "POST-PAYMENT ADJUSTMENTS")
			y += 12
			setFont(false, 8)
			setColor(120, 120, 120)
			for _, cn := range completedCNs {
				kind := "credited to balance"
				if cn.RefundAmountCents > 0 {
					kind = "refunded"
				}
				reason := cn.Reason
				if len([]rune(reason)) > 40 {
					reason = string([]rune(reason)[:37]) + "..."
				}
				textAt(margin, y, fmt.Sprintf("%s - %s (%s)", cn.Number, reason, kind))
				rightAlignAt(totalsX+labelW, y, totalsW-labelW, formatCents(cn.Amount))
				y += 12
			}
		}
	}

	// Tax treatment legend. Compliance-sensitive disclosure: reverse-charge
	// invoices must state who accounts for the tax, and exempt invoices
	// must carry the reason text (EU OSS, nonprofit certificate, etc.).
	if inv.TaxReverseCharge || inv.TaxExemptReason != "" {
		y += 8
		setFont(true, 8)
		setColor(80, 80, 80)
		if inv.TaxReverseCharge {
			textAt(margin, y, "Reverse charge — VAT to be accounted for by the recipient.")
			y += 12
		}
		if inv.TaxExemptReason != "" {
			setFont(false, 8)
			setColor(100, 100, 100)
			textAt(margin, y, "Tax-exempt: "+inv.TaxExemptReason)
			y += 12
		}
	}

	// ── Footer ──
	y += 16
	setFont(false, 9)
	setColor(120, 120, 120)
	if inv.Status == domain.InvoiceVoided {
		voidDate := "N/A"
		if inv.VoidedAt != nil {
			voidDate = inv.VoidedAt.Format("January 2, 2006")
		}
		setColor(200, 80, 80)
		textAt(margin, y, fmt.Sprintf("This invoice was voided on %s. No payment is due.", voidDate))
	} else if inv.PaymentStatus == domain.PaymentSucceeded && inv.PaidAt != nil {
		textAt(margin, y, fmt.Sprintf("Paid on %s - Thank you!", inv.PaidAt.Format("January 2, 2006")))
	} else {
		textAt(margin, y, fmt.Sprintf("Payment due within %d days of issue date.", inv.NetPaymentTermDays))
	}

	if inv.Memo != "" {
		y += 12
		setFont(false, 9)
		setColor(120, 120, 120)
		textAt(margin, y, inv.Memo)
	}

	y += 24
	setFont(false, 7)
	setColor(170, 170, 170)
	footer := fmt.Sprintf("Generated on %s  |  %s", time.Now().UTC().Format("Jan 2, 2006 15:04 UTC"), inv.ID)
	fw, _ := pdf.MeasureTextWidth(footer)
	textAt((pageW-fw)/2, y, footer)

	var buf bytes.Buffer
	if _, err := pdf.WriteTo(&buf); err != nil {
		return nil, fmt.Errorf("render pdf: %w", err)
	}
	return buf.Bytes(), nil
}

// formatCityStatePostal joins "City, State Postal" with graceful handling
// when any component is missing. Shared between the company "From" block
// and the "Bill To" block for consistent formatting across the PDF.
func formatCityStatePostal(city, state, postal string) string {
	line := city
	if state != "" {
		if line != "" {
			line += ", "
		}
		line += state
	}
	if postal != "" {
		if line != "" {
			line += " "
		}
		line += postal
	}
	return line
}

func formatCents(cents int64) string {
	if cents == 0 {
		return pdfCurrencySymbol + "0.00"
	}
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	dollars := cents / 100
	remainder := cents % 100
	return fmt.Sprintf("%s%s%s.%02d", sign, pdfCurrencySymbol, formatNumber(dollars), remainder)
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

type jurisdictionTaxRow struct {
	label  string
	amount int64
}

// aggregateTaxByJurisdiction sums tax by (jurisdiction, rate_bp) across line
// items so the PDF can render a multi-row breakdown when the invoice spans
// more than one jurisdiction. Lines without a jurisdiction or without tax
// are ignored. Results are sorted by label for deterministic output.
func aggregateTaxByJurisdiction(lineItems []domain.InvoiceLineItem) []jurisdictionTaxRow {
	type key struct {
		jurisdiction string
		rateBP       int64
	}
	agg := make(map[key]int64)
	order := make([]key, 0)
	for _, li := range lineItems {
		if li.TaxAmountCents == 0 || li.TaxJurisdiction == "" {
			continue
		}
		k := key{jurisdiction: li.TaxJurisdiction, rateBP: li.TaxRateBP}
		if _, seen := agg[k]; !seen {
			order = append(order, k)
		}
		agg[k] += li.TaxAmountCents
	}
	rows := make([]jurisdictionTaxRow, 0, len(order))
	for _, k := range order {
		label := k.jurisdiction
		if k.rateBP > 0 {
			label = fmt.Sprintf("%s (%.4g%%)", label, float64(k.rateBP)/100)
		}
		rows = append(rows, jurisdictionTaxRow{label: label, amount: agg[k]})
	}
	return rows
}
