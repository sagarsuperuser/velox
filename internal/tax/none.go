package tax

import "context"

// NoneProvider is the zero-tax backend. Tenants pick this when they don't
// collect tax at all (many B2B SaaS outside regulated jurisdictions, early
// stage). Every Calculate returns a zero-tax Result with one zeroed line per
// input; Commit is a no-op. Kept trivially simple so the engine can treat
// tax as uniform across providers without a "tax_provider == 'none'" branch
// in hot paths.
type NoneProvider struct{}

func NewNoneProvider() *NoneProvider { return &NoneProvider{} }

func (*NoneProvider) Name() string { return "none" }

func (*NoneProvider) Calculate(_ context.Context, req Request) (*Result, error) {
	lines := make([]ResultLine, len(req.LineItems))
	for i, li := range req.LineItems {
		lines[i] = ResultLine{Ref: li.Ref, NetAmountCents: li.AmountCents}
	}
	return &Result{Provider: "none", Lines: lines}, nil
}

func (*NoneProvider) Commit(_ context.Context, _, _ string) (string, error) { return "", nil }

// Reverse is a no-op — the none provider has no tax liability to reverse.
// Returns an empty ReversalResult so the credit note flow treats it as
// "nothing to record" without special-casing provider names.
func (*NoneProvider) Reverse(_ context.Context, _ ReversalRequest) (*ReversalResult, error) {
	return &ReversalResult{}, nil
}
