package litellm

import (
	"errors"
	"testing"
)

func TestMapPayload_HappyPath(t *testing.T) {
	p := StandardLoggingPayload{
		ID:                "litellm_call_abc123",
		CallType:          "completion",
		Model:             "claude-3-5-sonnet-20241022",
		CustomLLMProvider: "anthropic",
		User:              "cus_acme",
		Usage: &Usage{
			PromptTokens:     1200,
			CompletionTokens: 350,
			TotalTokens:      1550,
		},
		ResponseCost: 0.018,
		Metadata: map[string]any{
			"team_id": "team_eng",
			"x_other": "ignored-by-dim-promotion",
		},
		StartTime: 1700000000.1,
		EndTime:   1700000003.456,
	}

	out, err := MapPayload(p)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 events (input + output), got %d", len(out))
	}

	in, outE := out[0], out[1]

	// ADR-044: one `tokens` meter; role rides on the token_type dimension.
	if in.MeterKey != MeterKeyTokens {
		t.Errorf("input meter key: got %q, want %q", in.MeterKey, MeterKeyTokens)
	}
	if in.Dimensions["token_type"] != TokenTypeInput {
		t.Errorf("input dim token_type: got %v, want %q", in.Dimensions["token_type"], TokenTypeInput)
	}
	if in.Quantity.IntPart() != 1200 {
		t.Errorf("input quantity: got %s, want 1200", in.Quantity)
	}
	if in.ExternalCustomerID != "cus_acme" {
		t.Errorf("input customer: got %q, want cus_acme", in.ExternalCustomerID)
	}
	if in.IdempotencyKey != "litellm_call_abc123:input" {
		t.Errorf("input idempotency: got %q", in.IdempotencyKey)
	}
	if in.Dimensions["model"] != "claude-3-5-sonnet-20241022" {
		t.Errorf("input dim model: got %v", in.Dimensions["model"])
	}
	if in.Dimensions["provider"] != "anthropic" {
		t.Errorf("input dim provider: got %v", in.Dimensions["provider"])
	}
	if in.Dimensions["team_id"] != "team_eng" {
		t.Errorf("input dim team_id: got %v (metadata promotion failed)", in.Dimensions["team_id"])
	}
	// x_other is NOT promoted (only the curated keys are).
	if _, ok := in.Dimensions["x_other"]; ok {
		t.Error("input dim should not carry non-promoted metadata keys")
	}

	if outE.MeterKey != MeterKeyTokens {
		t.Errorf("output meter key: got %q, want %q", outE.MeterKey, MeterKeyTokens)
	}
	if outE.Dimensions["token_type"] != TokenTypeOutput {
		t.Errorf("output dim token_type: got %v, want %q", outE.Dimensions["token_type"], TokenTypeOutput)
	}
	// The two events must NOT share one dimensions map (token_type would alias).
	if in.Dimensions["token_type"] == outE.Dimensions["token_type"] {
		t.Error("input and output events share a dimensions map — token_type aliased")
	}
	if outE.Quantity.IntPart() != 350 {
		t.Errorf("output quantity: got %s, want 350", outE.Quantity)
	}
	if outE.IdempotencyKey != "litellm_call_abc123:output" {
		t.Errorf("output idempotency: got %q", outE.IdempotencyKey)
	}
}

func TestMapPayload_NonTokenBearingSkipped(t *testing.T) {
	cases := []string{"image_generation", "moderation", "speech", "transcription"}
	for _, ct := range cases {
		p := StandardLoggingPayload{
			ID: "x", CallType: ct, User: "cus_x",
			Usage: &Usage{PromptTokens: 100},
		}
		out, err := MapPayload(p)
		if err != nil {
			t.Errorf("%s: unexpected: %v", ct, err)
		}
		if len(out) != 0 {
			t.Errorf("%s: expected 0 events, got %d", ct, len(out))
		}
	}
}

func TestMapPayload_MissingUser(t *testing.T) {
	p := StandardLoggingPayload{
		ID: "x", CallType: "completion", User: "",
		Usage: &Usage{PromptTokens: 100},
	}
	_, err := MapPayload(p)
	if !errors.Is(err, ErrMissingUser) {
		t.Fatalf("expected ErrMissingUser, got %v", err)
	}
}

func TestMapPayload_ZeroTokens(t *testing.T) {
	// Empty response / failed call → both halves zero. Mapper
	// emits no events; handler counts as "skipped" not "error."
	p := StandardLoggingPayload{
		ID: "x", CallType: "completion", User: "cus_x",
		Usage: &Usage{PromptTokens: 0, CompletionTokens: 0},
	}
	out, err := MapPayload(p)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected 0 events for zero tokens, got %d", len(out))
	}
}

func TestMapPayload_EmbeddingOnlyPromptTokens(t *testing.T) {
	// Embedding calls only have prompt_tokens — no completion.
	// Mapper emits one input event, no output event.
	p := StandardLoggingPayload{
		ID: "emb1", CallType: "embedding", User: "cus_x",
		Model:             "text-embedding-3-large",
		CustomLLMProvider: "openai",
		Usage:             &Usage{PromptTokens: 500, CompletionTokens: 0},
	}
	out, err := MapPayload(p)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 event (input only), got %d", len(out))
	}
	if out[0].MeterKey != MeterKeyTokens {
		t.Errorf("embedding event meter: got %q, want %q", out[0].MeterKey, MeterKeyTokens)
	}
	if out[0].Dimensions["token_type"] != TokenTypeInput {
		t.Errorf("embedding token_type: got %v, want %q", out[0].Dimensions["token_type"], TokenTypeInput)
	}
}

func TestMapPayload_CostBreakdownSurfaced(t *testing.T) {
	p := StandardLoggingPayload{
		ID: "x", CallType: "completion", User: "cus_x",
		Usage: &Usage{PromptTokens: 100, CompletionTokens: 50},
		CostBreakdown: &CostBreakdown{
			InputCost:  0.012,
			OutputCost: 0.045,
			TotalCost:  0.057,
		},
	}
	out, _ := MapPayload(p)
	if len(out) != 2 {
		t.Fatalf("expected 2 events, got %d", len(out))
	}
	if got := out[0].Metadata["velox.litellm_cost_usd"]; got != 0.012 {
		t.Errorf("input event input_cost: got %v, want 0.012", got)
	}
	if got := out[1].Metadata["velox.litellm_cost_usd"]; got != 0.045 {
		t.Errorf("output event output_cost: got %v, want 0.045", got)
	}
	if got := out[0].Metadata["velox.source"]; got != "litellm" {
		t.Errorf("velox.source: got %v", got)
	}
}

func TestMapPayload_TimestampFromEndTime(t *testing.T) {
	p := StandardLoggingPayload{
		ID: "x", CallType: "completion", User: "cus_x",
		Usage:     &Usage{PromptTokens: 1},
		StartTime: 1700000000,
		EndTime:   1700000003.5,
	}
	out, _ := MapPayload(p)
	if len(out) == 0 {
		t.Fatal("expected at least one event")
	}
	if out[0].Timestamp == nil {
		t.Fatal("timestamp not set")
	}
	// EndTime takes precedence over StartTime.
	if out[0].Timestamp.Unix() != 1700000003 {
		t.Errorf("timestamp seconds: got %d, want 1700000003", out[0].Timestamp.Unix())
	}
}
