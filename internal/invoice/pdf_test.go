package invoice

import (
	"strings"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
)

func TestRenderPDF(t *testing.T) {
	now := time.Now().UTC()
	dueAt := now.AddDate(0, 0, 30)
	periodStart := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	inv := domain.Invoice{
		ID:                 "vlx_inv_test",
		InvoiceNumber:      "VLX-202604-0001",
		Status:             domain.InvoiceFinalized,
		PaymentStatus:      domain.PaymentPending,
		Currency:           "USD",
		SubtotalCents:      19900,
		TotalAmountCents:   19900,
		AmountDueCents:     19900,
		BillingPeriodStart: periodStart,
		BillingPeriodEnd:   periodEnd,
		IssuedAt:           &now,
		DueAt:              &dueAt,
		NetPaymentTermDays: 30,
		Memo:               "Thank you for your business.",
	}

	lineItems := []domain.InvoiceLineItem{
		{
			LineType:         domain.LineTypeBaseFee,
			Description:      "Pro Plan - base fee",
			Quantity:         1,
			UnitAmountCents:  4900,
			AmountCents:      4900,
			TotalAmountCents: 4900,
			Currency:         "USD",
		},
		{
			LineType:         domain.LineTypeUsage,
			MeterID:          "mtr_api",
			Description:      "API Calls - 1,500 calls",
			Quantity:         1500,
			UnitAmountCents:  8,
			AmountCents:      12500,
			TotalAmountCents: 12500,
			Currency:         "USD",
			PricingMode:      "graduated",
		},
		{
			LineType:         domain.LineTypeUsage,
			MeterID:          "mtr_storage",
			Description:      "Storage - 50 GB",
			Quantity:         50,
			UnitAmountCents:  2500,
			AmountCents:      2500,
			TotalAmountCents: 2500,
			Currency:         "USD",
			PricingMode:      "flat",
		},
	}

	pdfBytes, err := RenderPDF(inv, lineItems, BillToInfo{Name: "Acme Corp"}, nil)
	if err != nil {
		t.Fatalf("render pdf: %v", err)
	}

	// PDF should start with %PDF
	if len(pdfBytes) < 4 || string(pdfBytes[:4]) != "%PDF" {
		t.Fatal("output is not a valid PDF")
	}

	// Should be a reasonable size (> 1KB, < 1MB)
	if len(pdfBytes) < 1024 {
		t.Errorf("PDF too small: %d bytes", len(pdfBytes))
	}
	if len(pdfBytes) > 1024*1024 {
		t.Errorf("PDF too large: %d bytes", len(pdfBytes))
	}

	t.Logf("PDF generated: %d bytes", len(pdfBytes))
}

// minimalReverseChargeInvoice returns a finalized reverse-charge invoice
// suitable for legend / PDF tests. No tax amount, no positive subtotal —
// the legend branches don't depend on those.
func minimalReverseChargeInvoice() domain.Invoice {
	now := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	due := now.AddDate(0, 0, 30)
	periodStart := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	return domain.Invoice{
		ID:                 "vlx_inv_rc",
		InvoiceNumber:      "VLX-202604-0099",
		Status:             domain.InvoiceFinalized,
		PaymentStatus:      domain.PaymentPending,
		Currency:           "INR",
		SubtotalCents:      19900,
		TotalAmountCents:   19900,
		AmountDueCents:     19900,
		BillingPeriodStart: periodStart,
		BillingPeriodEnd:   periodEnd,
		IssuedAt:           &now,
		DueAt:              &due,
		NetPaymentTermDays: 30,
		TaxReverseCharge:   true,
	}
}

// TestRenderPDF_IndiaSupplierGSTIN verifies the PDF render call accepts the
// new TaxID/TaxIDType fields and produces a valid PDF with a reasonable size
// bump from the extra header line. gopdf compresses content streams and
// uses subset-font glyph IDs, so we cannot byte-substring the rendered text;
// the GSTIN-line presence is validated by the helper-level tests below
// (TestSupplierTaxIDLabel, TestRenderPDF_IndiaSupplierGSTIN_HelperPath).
//
// MANUAL: Visually verify "GSTIN: 27AAEPM1234C1Z5" appears under the company
// contact info in the rendered PDF (see MANUAL_TEST.md, FLOW B10).
func TestRenderPDF_IndiaSupplierGSTIN(t *testing.T) {
	inv := minimalReverseChargeInvoice()
	company := CompanyInfo{
		Name:      "Velox India Pvt Ltd",
		Country:   "IN",
		TaxID:     "27AAEPM1234C1Z5",
		TaxIDType: "gstin",
	}
	out, err := RenderPDF(inv, nil, BillToInfo{Name: "Acme Corp"}, nil, company)
	if err != nil {
		t.Fatalf("render pdf: %v", err)
	}
	if len(out) < 4 || string(out[:4]) != "%PDF" {
		t.Fatal("output is not a valid PDF")
	}

	// Helper-path assertion: the supplier label resolves to "GSTIN" for the
	// declared TaxIDType. This is what would be drawn into the PDF stream.
	if got := supplierTaxIDLabel(company.TaxIDType); got != "GSTIN" {
		t.Errorf("supplierTaxIDLabel(%q) = %q, want GSTIN", company.TaxIDType, got)
	}
}

// TestRenderPDF_IndiaReverseChargeLegend verifies the India context resolves
// to the GST-flavoured legend, not the EU "VAT to be accounted for" wording.
func TestRenderPDF_IndiaReverseChargeLegend(t *testing.T) {
	inv := minimalReverseChargeInvoice()
	company := CompanyInfo{
		Name:      "Velox India Pvt Ltd",
		Country:   "IN",
		TaxID:     "27AAEPM1234C1Z5",
		TaxIDType: "gstin",
	}
	billTo := BillToInfo{Name: "Acme India Ltd", Country: "IN"}

	got := reverseChargeLegend(inv, company, billTo, nil)
	if !strings.Contains(got, "Tax payable on reverse charge basis: YES") {
		t.Errorf("legend = %q, want it to contain India GST wording", got)
	}
	if strings.Contains(got, "VAT to be accounted for") {
		t.Errorf("legend = %q, must not contain EU VAT wording", got)
	}

	// And the PDF still renders without error end-to-end.
	out, err := RenderPDF(inv, nil, billTo, nil, company)
	if err != nil {
		t.Fatalf("render pdf: %v", err)
	}
	if len(out) < 4 || string(out[:4]) != "%PDF" {
		t.Fatal("output is not a valid PDF")
	}
}

// TestRenderPDF_EUReverseChargeLegend_Unchanged verifies a non-Indian
// supplier (e.g. DE, EU VAT) keeps the existing EU legend wording — the fix
// must not regress EU invoices.
func TestRenderPDF_EUReverseChargeLegend_Unchanged(t *testing.T) {
	inv := minimalReverseChargeInvoice()
	inv.Currency = "EUR"
	company := CompanyInfo{
		Name:      "Velox GmbH",
		Country:   "DE",
		TaxID:     "DE123456789",
		TaxIDType: "vat",
	}
	billTo := BillToInfo{Name: "Acme GmbH", Country: "FR"}

	got := reverseChargeLegend(inv, company, billTo, nil)
	if !strings.Contains(got, "VAT to be accounted for by the recipient") {
		t.Errorf("legend = %q, want EU VAT wording preserved", got)
	}
	if strings.Contains(got, "GST") || strings.Contains(got, "CGST") {
		t.Errorf("legend = %q, must not contain India GST wording for EU invoices", got)
	}

	out, err := RenderPDF(inv, nil, billTo, nil, company)
	if err != nil {
		t.Fatalf("render pdf: %v", err)
	}
	if len(out) < 4 || string(out[:4]) != "%PDF" {
		t.Fatal("output is not a valid PDF")
	}
}

// TestRenderPDF_NoTaxID_NoGSTINLine verifies that an empty TaxID does not
// trigger the supplier-tax-ID header line — backward compatibility for
// tenants who haven't set a tax ID yet.
//
// gopdf compresses streams, so we can't byte-search; instead we render two
// PDFs with the only difference being TaxID populated, and assert the
// no-tax-ID render is strictly smaller (= header line was suppressed).
func TestRenderPDF_NoTaxID_NoGSTINLine(t *testing.T) {
	inv := minimalReverseChargeInvoice()
	base := CompanyInfo{Name: "Velox India Pvt Ltd", Country: "IN"}
	withGSTIN := base
	withGSTIN.TaxID = "27AAEPM1234C1Z5"
	withGSTIN.TaxIDType = "gstin"

	billTo := BillToInfo{Name: "Acme India Ltd", Country: "IN"}

	outNo, err := RenderPDF(inv, nil, billTo, nil, base)
	if err != nil {
		t.Fatalf("render pdf no tax id: %v", err)
	}
	outYes, err := RenderPDF(inv, nil, billTo, nil, withGSTIN)
	if err != nil {
		t.Fatalf("render pdf with tax id: %v", err)
	}

	if len(outNo) >= len(outYes) {
		t.Errorf("expected PDF without GSTIN to be smaller than PDF with GSTIN; got no=%d yes=%d", len(outNo), len(outYes))
	}
	if len(outNo) < 4 || string(outNo[:4]) != "%PDF" {
		t.Fatal("no-tax-id output is not a valid PDF")
	}
}

// TestSupplierTaxIDLabel covers the label-prefix mapping for the in-PDF
// "GSTIN: ..." / "VAT: ..." / "ABN: ..." / "Tax ID: ..." rendering.
func TestSupplierTaxIDLabel(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"gstin", "GSTIN"},
		{"in_gst", "GSTIN"},
		{"in_gstin", "GSTIN"},
		{"GSTIN", "GSTIN"}, // case-insensitive
		{"vat", "VAT"},
		{"eu_vat", "VAT"},
		{"abn", "ABN"},
		{"au_abn", "ABN"},
		{"", "Tax ID"},
		{"us_ein", "Tax ID"},
		{"  gstin  ", "GSTIN"}, // trims whitespace
	}
	for _, c := range cases {
		if got := supplierTaxIDLabel(c.in); got != c.want {
			t.Errorf("supplierTaxIDLabel(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestIsIndianContext covers each branch of the India-context detector that
// drives reverse-charge legend selection.
func TestIsIndianContext(t *testing.T) {
	cases := []struct {
		name     string
		company  CompanyInfo
		custCtry string
		want     bool
	}{
		{"supplier IN", CompanyInfo{Country: "IN"}, "US", true},
		{"supplier India long form", CompanyInfo{Country: "India"}, "", true},
		{"supplier GSTIN type", CompanyInfo{TaxIDType: "gstin"}, "", true},
		{"supplier in_gst type", CompanyInfo{TaxIDType: "in_gst"}, "", true},
		{"supplier in_gstin type", CompanyInfo{TaxIDType: "in_gstin"}, "", true},
		{"customer IN", CompanyInfo{Country: "DE"}, "IN", true},
		{"customer India long form", CompanyInfo{}, "India", true},
		{"EU supplier + EU customer", CompanyInfo{Country: "DE", TaxIDType: "vat"}, "FR", false},
		{"US supplier + US customer", CompanyInfo{Country: "US"}, "US", false},
		{"empty", CompanyInfo{}, "", false},
	}
	for _, c := range cases {
		if got := isIndianContext(c.company, c.custCtry); got != c.want {
			t.Errorf("%s: isIndianContext = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestReverseChargeLegend_TaxJurisdictionFallback confirms the legend
// branches on a per-line-item Indian TaxJurisdiction (e.g. "IN-MH") even
// when neither supplier nor billing country signals India.
func TestReverseChargeLegend_TaxJurisdictionFallback(t *testing.T) {
	inv := minimalReverseChargeInvoice()
	supplier := CompanyInfo{Name: "Velox", Country: "US"}
	billTo := BillToInfo{Name: "Acme", Country: "US"}
	lineItems := []domain.InvoiceLineItem{
		{TaxJurisdiction: "IN-MH"},
	}
	got := reverseChargeLegend(inv, supplier, billTo, lineItems)
	if !strings.Contains(got, "Tax payable on reverse charge basis: YES") {
		t.Errorf("expected India legend via line-item jurisdiction; got %q", got)
	}
}

// TestSupplierTaxIDTypeFromCountry covers the country→type inference used
// by handlers that build CompanyInfo from TenantSettings.
func TestSupplierTaxIDTypeFromCountry(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"IN", "gstin"},
		{"in", "gstin"},
		{"India", "gstin"},
		{"AU", "abn"},
		{"DE", "vat"},
		{"FR", "vat"},
		{"GB", "vat"},
		{"UK", "vat"},
		{"US", ""},
		{"", ""},
		{" IN ", "gstin"},
	}
	for _, c := range cases {
		if got := SupplierTaxIDTypeFromCountry(c.in); got != c.want {
			t.Errorf("SupplierTaxIDTypeFromCountry(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseBrandColor(t *testing.T) {
	cases := []struct {
		in    string
		wantR uint8
		wantG uint8
		wantB uint8
		ok    bool
	}{
		{"#1f6feb", 0x1f, 0x6f, 0xeb, true},
		{"#000000", 0, 0, 0, true},
		{"#ffffff", 0xff, 0xff, 0xff, true},
		{"#FF00AA", 0xff, 0, 0xaa, true},
		{"", 0, 0, 0, false},
		{"#fff", 0, 0, 0, false},     // short form rejected
		{"1f6feb", 0, 0, 0, false},   // missing #
		{"#zzzzzz", 0, 0, 0, false},  // non-hex
		{"#12345", 0, 0, 0, false},   // too short
		{"#1234567", 0, 0, 0, false}, // too long
	}
	for _, c := range cases {
		r, g, b, ok := parseBrandColor(c.in)
		if ok != c.ok || r != c.wantR || g != c.wantG || b != c.wantB {
			t.Errorf("parseBrandColor(%q) = (%d,%d,%d,%v), want (%d,%d,%d,%v)",
				c.in, r, g, b, ok, c.wantR, c.wantG, c.wantB, c.ok)
		}
	}
}
