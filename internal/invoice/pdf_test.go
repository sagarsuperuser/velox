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
