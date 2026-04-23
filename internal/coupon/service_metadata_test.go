package coupon

import (
	"context"
	"strings"
	"testing"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/metadata"
)

// TestCreate_MetadataHappyPath locks in that a well-shaped blob round-trips
// through the service into the store unmodified — the validator must not
// mutate or normalise content.
func TestCreate_MetadataHappyPath(t *testing.T) {
	t.Parallel()
	svc := NewService(newMockStore())
	raw := []byte(`{"campaign":"summer-2025"}`)

	c, err := svc.Create(context.Background(), "t1", CreateInput{
		Code: "MD-OK", Name: "ok",
		Type: domain.CouponTypePercentage, PercentOffBP: 1000,
		Metadata: raw,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if string(c.Metadata) != string(raw) {
		t.Errorf("metadata altered: got %q, want %q", c.Metadata, raw)
	}
}

func TestCreate_MetadataTooLargeRejected(t *testing.T) {
	t.Parallel()
	svc := NewService(newMockStore())

	// Generate an oversize blob — enough to trip the raw-bytes check
	// before the decoder even runs, so we also confirm the cheap guard
	// fires before the allocation-heavy path.
	big := make([]byte, metadata.MaxBytes+1)
	for i := range big {
		big[i] = 'x'
	}

	_, err := svc.Create(context.Background(), "t1", CreateInput{
		Code: "MD-BIG", Name: "big",
		Type: domain.CouponTypePercentage, PercentOffBP: 1000,
		Metadata: big,
	})
	if err == nil {
		t.Fatal("oversize metadata should be rejected")
	}
	if de, ok := err.(*errs.DomainError); !ok || de.Field != "metadata" {
		t.Errorf("error shape: got %+v, want DomainError{Field: metadata}", err)
	}
}

func TestCreate_MetadataNestedValueRejected(t *testing.T) {
	t.Parallel()
	svc := NewService(newMockStore())
	_, err := svc.Create(context.Background(), "t1", CreateInput{
		Code: "MD-NEST", Name: "nested",
		Type: domain.CouponTypePercentage, PercentOffBP: 1000,
		Metadata: []byte(`{"owner":{"team":"growth"}}`),
	})
	if err == nil {
		t.Fatal("nested metadata value should be rejected")
	}
}

func TestUpdate_MetadataValidatedOnPatch(t *testing.T) {
	t.Parallel()
	svc := NewService(newMockStore())
	cpn, err := svc.Create(context.Background(), "t1", CreateInput{
		Code: "MD-UPD", Name: "upd",
		Type: domain.CouponTypePercentage, PercentOffBP: 1000,
	})
	if err != nil {
		t.Fatalf("seed Create: %v", err)
	}

	longVal := strings.Repeat("x", metadata.MaxValueLength+1)
	bad := []byte(`{"k":"` + longVal + `"}`)
	_, err = svc.Update(context.Background(), "t1", cpn.ID, UpdateInput{
		Metadata: bad,
	})
	if err == nil {
		t.Fatal("Update with oversize value should be rejected")
	}
	if de, ok := err.(*errs.DomainError); !ok || de.Field != "metadata" {
		t.Errorf("error shape: got %+v, want DomainError{Field: metadata}", err)
	}
}
