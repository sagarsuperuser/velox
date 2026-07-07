package recipe

import (
	"testing"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// Meter adoption is gated only on aggregation (billing-consulted reference
// data, ADR-084); Name/Unit are cosmetic. The plan is deliberately NOT gated
// (operator-owned) — its reuse-and-report behavior is covered by the
// real-Postgres TestService_Instantiate_* integration tests.
func TestMeterConformanceDiff(t *testing.T) {
	rm := domain.RecipeMeter{Key: "tokens", Name: "Tokens", Unit: "tokens", Aggregation: "sum"}
	if d := meterConformanceDiff(domain.Meter{Key: "tokens", Name: "Tokens", Unit: "tokens", Aggregation: "sum"}, rm); len(d) != 0 {
		t.Errorf("matching meter should adopt, got %v", d)
	}
	if d := meterConformanceDiff(domain.Meter{Key: "tokens", Name: "X", Unit: "tok", Aggregation: "sum"}, rm); len(d) != 0 {
		t.Errorf("cosmetic name/unit change should adopt, got %v", d)
	}
	if d := meterConformanceDiff(domain.Meter{Key: "tokens", Aggregation: "max"}, rm); len(d) == 0 {
		t.Error("divergent aggregation must be caught — an adopted meter with a different roll-up silently mis-bills")
	}
}
