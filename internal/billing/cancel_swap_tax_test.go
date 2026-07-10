package billing

import (
	"context"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/platform/money"
)

// TestCreditUnusedPrebill covers the shared clawback-credit helper (ADR-048)
// that BillOnCancel + BillOnPlanSwapImmediate route through. On a PAID, taxed
// source invoice with the credit-note adjuster wired, it must credit the GROSS
// unused (net grossed up by the invoice's tax ratio) via the credit-note
// primitive — which reverses the proportional output tax — and must NOT also
// fire the bare net ledger grant (double-credit). When the adjuster is unwired
// (narrow tests) or no source invoice was resolved, it falls back to the bare
// net ledger grant.
func TestCreditUnusedPrebill(t *testing.T) {
	sub := domain.Subscription{ID: "sub_1", TenantID: "t1", CustomerID: "cus_1", Code: "starter"}
	// PAID source invoice carrying 10% tax: net 6000 + tax 600 = 6600 gross.
	src := domain.Invoice{ID: "inv_1", SubtotalCents: 6000, TaxFacts: domain.TaxFacts{TaxAmountCents: 600}, TotalAmountCents: 6600}
	const net = int64(2000)
	wantGross := money.RoundHalfToEven(net*6600, 6000) // 2200 — net + the 10% tax slice

	newE := func() *Engine {
		return NewEngine(&mockSubs{}, &mockUsage{}, &mockPricing{}, &mockInvoices{}, nil, &mockSettings{}, nil, nil, billingTestClock())
	}

	t.Run("adjuster wired → gross credit-note, no ledger grant", func(t *testing.T) {
		g := &fakeCreditGranter{}
		a := &fakeCreditNoteAdjuster{}
		e := newE()
		e.SetCreditGranter(g)
		e.SetCreditNoteAdjuster(a)

		credited, err := e.creditUnusedPrebill(context.Background(), sub, src, true, net, "subscription_cancellation", "desc", time.Unix(0, 0))
		if err != nil {
			t.Fatalf("creditUnusedPrebill: %v", err)
		}
		if wantGross != 2200 {
			t.Fatalf("setup: wantGross = %d, expected 2200", wantGross)
		}
		if len(a.calls) != 1 || a.calls[0].invoiceID != "inv_1" || a.calls[0].gross != wantGross {
			t.Errorf("credit-note adjustment = %+v, want one call on inv_1 for gross %d", a.calls, wantGross)
		}
		if len(g.grants) != 0 {
			t.Errorf("bare ledger grant fired %d times — must be REPLACED by the CN, not added", len(g.grants))
		}
		if credited != wantGross {
			t.Errorf("credited = %d, want gross %d", credited, wantGross)
		}
	})

	t.Run("adjuster unwired → fallback net ledger grant", func(t *testing.T) {
		g := &fakeCreditGranter{}
		e := newE()
		e.SetCreditGranter(g)

		credited, err := e.creditUnusedPrebill(context.Background(), sub, src, true, net, "subscription_cancellation", "desc", time.Unix(0, 0))
		if err != nil {
			t.Fatalf("creditUnusedPrebill: %v", err)
		}
		if len(g.grants) != 1 || g.grants[0].AmountCents != net {
			t.Errorf("fallback grants = %+v, want one net grant of %d", g.grants, net)
		}
		if credited != net {
			t.Errorf("credited = %d, want net %d", credited, net)
		}
	})

	t.Run("no source invoice → fallback net even with adjuster wired", func(t *testing.T) {
		g := &fakeCreditGranter{}
		a := &fakeCreditNoteAdjuster{}
		e := newE()
		e.SetCreditGranter(g)
		e.SetCreditNoteAdjuster(a)

		credited, err := e.creditUnusedPrebill(context.Background(), sub, domain.Invoice{}, false, net, "subscription_cancellation", "desc", time.Unix(0, 0))
		if err != nil {
			t.Fatalf("creditUnusedPrebill: %v", err)
		}
		if len(a.calls) != 0 {
			t.Errorf("no source invoice → must not issue a CN, got %d", len(a.calls))
		}
		if len(g.grants) != 1 || g.grants[0].AmountCents != net || credited != net {
			t.Errorf("expected net fallback grant of %d (credited %d), got grants %+v", net, credited, g.grants)
		}
	})
}

// TestBillOnCancel_PaidPrebillReversesTax proves the cancel site actually
// routes a PAID, taxed prebill clawback through the credit-note primitive
// (ADR-048): the customer is credited the GROSS unused via a CN (which reverses
// the proportional tax), not the bare net ledger grant that dropped the tax.
func TestBillOnCancel_PaidPrebillReversesTax(t *testing.T) {
	periodStart := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC) // 31-day cycle
	cancelAt := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC) // 16 unused / 31
	ps := periodStart

	sub := domain.Subscription{
		ID: "sub_1", TenantID: "t1", CustomerID: "cus_1", Code: "starter",
		Status:                    domain.SubscriptionCanceled,
		Items:                     []domain.SubscriptionItem{{PlanID: "pln_advance", Quantity: 1}},
		CurrentBillingPeriodStart: &periodStart,
		CurrentBillingPeriodEnd:   &periodEnd,
		CanceledAt:                &cancelAt,
	}
	pricing := &mockPricing{plans: map[string]domain.Plan{
		"pln_advance": {ID: "pln_advance", Name: "Advance", Currency: "USD",
			BillingInterval: domain.BillingMonthly, BaseAmountCents: 6000, BaseBillTiming: domain.BillInAdvance},
	}}
	// PAID in_advance invoice with 10% tax.
	inv := &mockInvoices{
		invoices: []domain.Invoice{{
			ID: "inv_1", TenantID: "t1", SubscriptionID: "sub_1",
			Status: domain.InvoiceFinalized, PaymentStatus: domain.PaymentSucceeded,
			SubtotalCents: 6000, TaxFacts: domain.TaxFacts{TaxAmountCents: 600}, TotalAmountCents: 6600,
			AmountDueCents: 0, AmountPaidCents: 6600,
		}},
		lineItems: []domain.InvoiceLineItem{{
			ID: "ili_1", InvoiceID: "inv_1", LineType: domain.LineTypeBaseFee, BillingPeriodStart: &ps,
		}},
	}
	g := &fakeCreditGranter{}
	a := &fakeCreditNoteAdjuster{}
	e := wireBaseTax(NewEngine(&mockSubs{}, &mockUsage{}, pricing, inv, nil, &mockSettings{}, nil, nil, billingTestClock()))
	e.SetCreditGranter(g)
	e.SetCreditNoteAdjuster(a)

	netUnused := money.RoundHalfToEven(6000*16, 31)            // pre-fix bare grant
	grossUnused := money.RoundHalfToEven(netUnused*6600, 6000) // grossed up by the invoice's tax

	cents, err := e.BillOnCancel(context.Background(), sub)
	if err != nil {
		t.Fatalf("BillOnCancel: %v", err)
	}
	if grossUnused <= netUnused {
		t.Fatalf("setup: gross (%d) must exceed net (%d)", grossUnused, netUnused)
	}
	if len(a.calls) != 1 || a.calls[0].invoiceID != "inv_1" || a.calls[0].gross != grossUnused {
		t.Errorf("credit-note adjustment = %+v, want one call on inv_1 for gross %d", a.calls, grossUnused)
	}
	if len(g.grants) != 0 {
		t.Errorf("bare ledger grant fired %d times — the paid cancel must route through the CN", len(g.grants))
	}
	if cents != grossUnused {
		t.Errorf("returned cents = %d, want gross %d", cents, grossUnused)
	}
}
