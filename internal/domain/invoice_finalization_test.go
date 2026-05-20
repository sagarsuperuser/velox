package domain

import "testing"

// TestInvoiceFinalizationStatus locks in the single-source-of-truth
// rule shared across all four invoice-emitting paths
// (engine.billOnePeriod, engine.BillOnCreate,
// engine.BillFinalOnImmediateCancel, subscription.handleItemProration).
// Pre-fix BillOnCreate + BillFinalOnImmediateCancel + handleItemProration
// hardcoded InvoiceFinalized regardless of tax_status, producing
// finalized invoices with TaxAmountCents=0 when Stripe Tax returned
// customer_data_invalid — lying about authoritative amounts.
func TestInvoiceFinalizationStatus(t *testing.T) {
	pause := &PauseCollection{Behavior: PauseCollectionKeepAsDraft}

	cases := []struct {
		name      string
		taxStatus InvoiceTaxStatus
		pause     *PauseCollection
		want      InvoiceStatus
	}{
		{"happy path (tax ok, no pause)", InvoiceTaxOK, nil, InvoiceFinalized},
		{"tax pending → draft", InvoiceTaxPending, nil, InvoiceDraft},
		{"tax failed → finalized (worker exhausted, operator-resolved)", InvoiceTaxFailed, nil, InvoiceFinalized},
		{"pause set + tax ok → draft", InvoiceTaxOK, pause, InvoiceDraft},
		{"pause set + tax pending → draft (both gates fire)", InvoiceTaxPending, pause, InvoiceDraft},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := InvoiceFinalizationStatus(tc.taxStatus, tc.pause)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
