package invoice

import (
	"context"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// BuildPDFContext assembles everything RenderPDF needs beyond the
// invoice and its lines — bill-to (customer + billing profile), company
// header (tenant settings), and the invoice's issued credit notes — in
// ONE place. The emailed, dashboard-download, and hosted-page PDFs each
// hand-rolled this block and had drifted three ways: the emailed PDF
// carried no buyer address and no credit notes (an emailed invoice
// showed a different amount-due story than the downloaded one), and no
// invoice surface carried the buyer's tax ID at all (the credit-note
// PDF did — EU VAT/GST compliance needs it on invoices too).
//
// Every dependency is optional (nil-tolerant) and every lookup is
// best-effort: a missing profile or settings row degrades that block,
// never the render. inv is a pointer so the builder can stamp
// BillingPeriodDisplay when the caller's fetch path bypassed the
// service read decorator that normally sets it (the hosted
// GetByPublicToken path) — it never overwrites a non-empty value.
func BuildPDFContext(
	ctx context.Context,
	customers CustomerGetter,
	settings SettingsGetter,
	creditNotes CreditNoteLister,
	tenantID string,
	inv *domain.Invoice,
) (BillToInfo, CompanyInfo, []CreditNoteInfo) {
	bt := BillToInfo{Name: inv.CustomerID}
	if customers != nil {
		if cust, err := customers.Get(ctx, tenantID, inv.CustomerID); err == nil {
			bt.Name = cust.DisplayName
			bt.Email = cust.Email
		}
		if bp, err := customers.GetBillingProfile(ctx, tenantID, inv.CustomerID); err == nil {
			if bp.LegalName != "" {
				bt.Name = bp.LegalName
			}
			// bp.Email removed in migration 0100 — bill-to email tracks
			// customers.email (set above).
			bt.AddressLine1 = bp.AddressLine1
			bt.AddressLine2 = bp.AddressLine2
			bt.City = bp.City
			bt.State = bp.State
			bt.PostalCode = bp.PostalCode
			bt.Country = bp.Country
			bt.TaxID = bp.TaxID
		}
	}

	var ci CompanyInfo
	if settings != nil {
		if ts, err := settings.Get(ctx, tenantID); err == nil {
			ci = CompanyInfo{
				Name:         ts.CompanyName,
				Email:        ts.CompanyEmail,
				Phone:        ts.CompanyPhone,
				AddressLine1: ts.CompanyAddressLine1,
				AddressLine2: ts.CompanyAddressLine2,
				City:         ts.CompanyCity,
				State:        ts.CompanyState,
				PostalCode:   ts.CompanyPostalCode,
				Country:      ts.CompanyCountry,
				BrandColor:   ts.BrandColor,
				TaxID:        ts.TaxID,
				TaxIDType:    SupplierTaxIDTypeFromCountry(ts.CompanyCountry),
			}
			// Inclusive-last-day period string (ADR-058): fetch paths that
			// bypass the service read decorator (hosted GetByPublicToken)
			// arrive without it — author it here from the same domain helper.
			// Anchored in the invoice's own billing TZ (ADR-074 snapshot),
			// falling back to the tenant TZ for ad-hoc/legacy invoices — the
			// same resolution the service decorator (invoiceDisplayLoc) uses.
			if inv.BillingPeriodDisplay == "" {
				periodLoc := domain.LoadLocationOrUTC(ts.Timezone)
				if inv.BillingTimezone != "" {
					periodLoc = domain.LoadLocationOrUTC(inv.BillingTimezone)
				}
				inv.BillingPeriodDisplay = domain.FormatInclusivePeriod(inv.BillingPeriodStart, inv.BillingPeriodEnd, periodLoc)
			}
		}
	}

	var cnInfos []CreditNoteInfo
	if creditNotes != nil {
		if notes, err := creditNotes.List(ctx, tenantID, inv.ID); err == nil {
			for _, cn := range notes {
				if cn.Status != domain.CreditNoteIssued {
					continue
				}
				cnInfos = append(cnInfos, CreditNoteInfo{
					Number:               cn.CreditNoteNumber,
					Reason:               cn.Reason,
					Amount:               cn.TotalCents,
					RefundAmountCents:    cn.RefundAmountCents,
					CreditAmountCents:    cn.CreditAmountCents,
					OutOfBandAmountCents: cn.OutOfBandAmountCents,
					TaxAmountCents:       cn.TaxAmountCents,
					TaxTransactionID:     cn.TaxTransactionID,
					RefundStatus:         string(cn.RefundStatus),
				})
			}
		}
	}

	return bt, ci, cnInfos
}
