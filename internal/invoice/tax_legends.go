package invoice

import (
	"strings"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// exemptionLegend returns one or two lines of legend text triggered by per-line
// taxability_reason values, or "" if nothing applies. Order: customer_exempt
// before product_exempt when both appear.
//
// Decoupled from the existing reverse-charge legend (driven by the calc-level
// TaxReverseCharge boolean) — the two coexist on the PDF when both apply,
// because they disclose materially different tax treatments. customer_exempt
// is "this customer holds an exemption certificate" while reverse_charge is
// "the recipient self-accounts for VAT/GST". Don't conflate them.
//
// Pure helper — no PDF state, fully unit-testable.
func exemptionLegend(lineItems []domain.InvoiceLineItem) string {
	var hasCustomerExempt, hasProductExempt bool
	for _, li := range lineItems {
		switch strings.TrimSpace(li.TaxabilityReason) {
		case "customer_exempt":
			hasCustomerExempt = true
		case "product_exempt":
			hasProductExempt = true
		}
	}
	if !hasCustomerExempt && !hasProductExempt {
		return ""
	}
	var parts []string
	if hasCustomerExempt {
		parts = append(parts, "One or more lines are exempt from tax based on the customer's exemption certificate.")
	}
	if hasProductExempt {
		parts = append(parts, "One or more lines are exempt from tax in this jurisdiction by product category.")
	}
	return strings.Join(parts, "\n")
}
