package creditnote

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

type memStore struct {
	notes     map[string]domain.CreditNote
	lineItems map[string][]domain.CreditNoteLineItem
}

func newMemStore() *memStore {
	return &memStore{
		notes:     make(map[string]domain.CreditNote),
		lineItems: make(map[string][]domain.CreditNoteLineItem),
	}
}

func (m *memStore) Create(_ context.Context, tenantID string, cn domain.CreditNote) (domain.CreditNote, error) {
	cn.ID = fmt.Sprintf("vlx_cn_%d", len(m.notes)+1)
	cn.TenantID = tenantID
	cn.CreatedAt = time.Now().UTC()
	cn.UpdatedAt = cn.CreatedAt
	m.notes[cn.ID] = cn
	return cn, nil
}

func (m *memStore) Get(_ context.Context, tenantID, id string) (domain.CreditNote, error) {
	cn, ok := m.notes[id]
	if !ok || cn.TenantID != tenantID {
		return domain.CreditNote{}, errs.ErrNotFound
	}
	return cn, nil
}

func (m *memStore) List(_ context.Context, filter ListFilter) ([]domain.CreditNote, error) {
	var result []domain.CreditNote
	for _, cn := range m.notes {
		if cn.TenantID != filter.TenantID {
			continue
		}
		if filter.InvoiceID != "" && cn.InvoiceID != filter.InvoiceID {
			continue
		}
		result = append(result, cn)
	}
	return result, nil
}

func (m *memStore) UpdateStatus(_ context.Context, tenantID, id string, status domain.CreditNoteStatus) (domain.CreditNote, error) {
	cn, ok := m.notes[id]
	if !ok || cn.TenantID != tenantID {
		return domain.CreditNote{}, errs.ErrNotFound
	}
	cn.Status = status
	now := time.Now().UTC()
	if status == domain.CreditNoteIssued {
		cn.IssuedAt = &now
	}
	if status == domain.CreditNoteVoided {
		cn.VoidedAt = &now
	}
	m.notes[id] = cn
	return cn, nil
}

func (m *memStore) CreateLineItem(_ context.Context, tenantID string, item domain.CreditNoteLineItem) (domain.CreditNoteLineItem, error) {
	item.ID = fmt.Sprintf("vlx_cnli_%d", len(m.lineItems[item.CreditNoteID])+1)
	item.TenantID = tenantID
	m.lineItems[item.CreditNoteID] = append(m.lineItems[item.CreditNoteID], item)
	return item, nil
}

func (m *memStore) ListLineItems(_ context.Context, _, creditNoteID string) ([]domain.CreditNoteLineItem, error) {
	return m.lineItems[creditNoteID], nil
}

type memInvoiceReader struct {
	invoices  map[string]domain.Invoice
	lineItems map[string][]domain.InvoiceLineItem
}

func (m *memInvoiceReader) Get(_ context.Context, tenantID, id string) (domain.Invoice, error) {
	inv, ok := m.invoices[id]
	if !ok || inv.TenantID != tenantID {
		return domain.Invoice{}, errs.ErrNotFound
	}
	return inv, nil
}

func (m *memInvoiceReader) ListLineItems(_ context.Context, _, invoiceID string) ([]domain.InvoiceLineItem, error) {
	return m.lineItems[invoiceID], nil
}

func (m *memInvoiceReader) ApplyCreditNote(_ context.Context, tenantID, id string, amountCents int64) (domain.Invoice, error) {
	inv, ok := m.invoices[id]
	if !ok || inv.TenantID != tenantID {
		return domain.Invoice{}, errs.ErrNotFound
	}
	inv.AmountDueCents -= amountCents
	if inv.AmountDueCents < 0 {
		inv.AmountDueCents = 0
	}
	m.invoices[id] = inv
	return inv, nil
}

func TestCreate_CreditNote(t *testing.T) {
	store := newMemStore()
	invoices := &memInvoiceReader{
		invoices: map[string]domain.Invoice{
			"inv_1": {
				ID: "inv_1", TenantID: "t1", CustomerID: "cus_1",
				Status: domain.InvoiceFinalized, Currency: "USD",
				TotalAmountCents: 19900,
			},
		},
	}
	svc := NewService(store, invoices, nil)
	ctx := context.Background()

	t.Run("valid credit note", func(t *testing.T) {
		cn, err := svc.Create(ctx, "t1", CreateInput{
			InvoiceID: "inv_1",
			Reason:    "Customer overcharged for API calls",
			Lines: []CreditLineInput{
				{Description: "API call adjustment", Quantity: 500, UnitAmountCents: 5},
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cn.Status != domain.CreditNoteDraft {
			t.Errorf("status: got %q, want draft", cn.Status)
		}
		if cn.TotalCents != 2500 {
			t.Errorf("total: got %d, want 2500", cn.TotalCents)
		}
		if cn.CreditAmountCents != 2500 {
			t.Errorf("credit_amount: got %d, want 2500 (not paid, so credit not refund)", cn.CreditAmountCents)
		}
		if cn.CustomerID != "cus_1" {
			t.Errorf("customer_id: got %q", cn.CustomerID)
		}
	})

	t.Run("refund for paid invoice", func(t *testing.T) {
		invoices.invoices["inv_paid"] = domain.Invoice{
			ID: "inv_paid", TenantID: "t1", CustomerID: "cus_1",
			Status: domain.InvoicePaid, Currency: "USD",
		}
		cn, err := svc.Create(ctx, "t1", CreateInput{
			InvoiceID:  "inv_paid",
			Reason:     "Service not delivered",
			RefundType: "refund",
			Lines:      []CreditLineInput{{Description: "Full refund", Quantity: 1, UnitAmountCents: 5000}},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cn.RefundAmountCents != 5000 {
			t.Errorf("refund_amount: got %d, want 5000", cn.RefundAmountCents)
		}
	})

	t.Run("missing invoice", func(t *testing.T) {
		_, err := svc.Create(ctx, "t1", CreateInput{
			InvoiceID: "nonexistent", Reason: "test",
			Lines: []CreditLineInput{{Description: "x", Quantity: 1, UnitAmountCents: 100}},
		})
		if err == nil {
			t.Fatal("expected error for missing invoice")
		}
	})

	t.Run("draft invoice rejected", func(t *testing.T) {
		invoices.invoices["inv_draft"] = domain.Invoice{
			ID: "inv_draft", TenantID: "t1", Status: domain.InvoiceDraft,
		}
		_, err := svc.Create(ctx, "t1", CreateInput{
			InvoiceID: "inv_draft", Reason: "test",
			Lines: []CreditLineInput{{Description: "x", Quantity: 1, UnitAmountCents: 100}},
		})
		if err == nil {
			t.Fatal("expected error for draft invoice")
		}
	})

	t.Run("missing reason", func(t *testing.T) {
		_, err := svc.Create(ctx, "t1", CreateInput{
			InvoiceID: "inv_1",
			Lines:     []CreditLineInput{{Description: "x", Quantity: 1, UnitAmountCents: 100}},
		})
		if err == nil {
			t.Fatal("expected error for missing reason")
		}
	})

	t.Run("no lines", func(t *testing.T) {
		_, err := svc.Create(ctx, "t1", CreateInput{InvoiceID: "inv_1", Reason: "test"})
		if err == nil {
			t.Fatal("expected error for no lines")
		}
	})
}

func TestIssueAndVoid_CreditNote(t *testing.T) {
	store := newMemStore()
	invoices := &memInvoiceReader{
		invoices: map[string]domain.Invoice{
			"inv_1": {ID: "inv_1", TenantID: "t1", Status: domain.InvoiceFinalized, Currency: "USD"},
		},
	}
	svc := NewService(store, invoices, nil)
	ctx := context.Background()

	cn, _ := svc.Create(ctx, "t1", CreateInput{
		InvoiceID: "inv_1", Reason: "Adjustment",
		Lines: []CreditLineInput{{Description: "adj", Quantity: 1, UnitAmountCents: 1000}},
	})

	t.Run("issue", func(t *testing.T) {
		issued, err := svc.Issue(ctx, "t1", cn.ID)
		if err != nil {
			t.Fatalf("issue: %v", err)
		}
		if issued.Status != domain.CreditNoteIssued {
			t.Errorf("status: got %q, want issued", issued.Status)
		}
		if issued.IssuedAt == nil {
			t.Error("issued_at should be set")
		}
	})

	t.Run("cannot issue again", func(t *testing.T) {
		_, err := svc.Issue(ctx, "t1", cn.ID)
		if err == nil {
			t.Fatal("expected error issuing non-draft")
		}
	})

	t.Run("void", func(t *testing.T) {
		voided, err := svc.Void(ctx, "t1", cn.ID)
		if err != nil {
			t.Fatalf("void: %v", err)
		}
		if voided.Status != domain.CreditNoteVoided {
			t.Errorf("status: got %q, want voided", voided.Status)
		}
	})
}
