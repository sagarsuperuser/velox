package invoice

import (
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
