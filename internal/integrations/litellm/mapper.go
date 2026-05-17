package litellm

import (
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/sagarsuperuser/velox/internal/usage"
)

// Meter keys the adapter writes to. Operators must create these two
// meters in Velox (typically via the anthropic_style / openai_style
// recipe). Adding new meters here means a new convention partners
// must mirror; we keep the surface minimal so cost-table changes
// downstream don't require partner re-config.
const (
	MeterKeyTokensInput  = "tokens_input"
	MeterKeyTokensOutput = "tokens_output"
)

// MapPayload converts a single LiteLLM StandardLoggingPayload into
// zero, one, or two Velox IngestInput events.
//
// Mapping (per ADR-033):
//
//	prompt_tokens     → IngestInput{meter_key: tokens_input,  quantity: N, ...}
//	completion_tokens → IngestInput{meter_key: tokens_output, quantity: N, ...}
//
// Skipped when:
//   - call_type isn't token-bearing (image_gen, moderation, etc.)
//   - Usage is nil
//   - both token counts are zero (empty response, error, etc.)
//
// Returns ErrMissingUser when payload.user is empty — the adapter
// refuses to bill anonymous usage to an unknown customer. Operator
// must set `user="<external_customer_id>"` on their LiteLLM calls.
//
// IngestInput.CustomerID carries the EXTERNAL id (payload.User); the
// handler resolves it to the internal customer_id via the existing
// usage Resolver path (same flow as POST /v1/usage-events).
func MapPayload(p StandardLoggingPayload) ([]ExternalIngest, error) {
	if !p.IsTokenBearing() {
		return nil, nil
	}
	if strings.TrimSpace(p.User) == "" {
		return nil, ErrMissingUser
	}
	if p.Usage == nil {
		return nil, nil
	}

	// Event timestamp: prefer EndTime (when the call actually
	// completed), fall back to StartTime, then time.Now. LiteLLM
	// emits floats in unix-seconds; convert to time.Time without
	// loss for sub-second resolution.
	var ts time.Time
	switch {
	case p.EndTime > 0:
		ts = unixSecondsToTime(p.EndTime)
	case p.StartTime > 0:
		ts = unixSecondsToTime(p.StartTime)
	default:
		ts = time.Now().UTC()
	}

	dims := map[string]any{
		"model":    p.Model,
		"provider": p.CustomLLMProvider,
	}
	// Forward useful operator-set metadata into dimensions for
	// pricing-rule dispatch. We promote `team_id` and `tags`
	// specifically because LiteLLM's spend page uses them as the
	// primary slice axes; everything else stays in the event's
	// metadata column for audit.
	if p.Metadata != nil {
		if v, ok := p.Metadata["team_id"]; ok {
			dims["team_id"] = v
		}
		if v, ok := p.Metadata["request_tags"]; ok {
			dims["request_tags"] = v
		}
	}

	out := make([]ExternalIngest, 0, 2)

	if p.Usage.PromptTokens > 0 {
		out = append(out, ExternalIngest{
			ExternalCustomerID: p.User,
			MeterKey:           MeterKeyTokensInput,
			Quantity:           decimal.NewFromInt(p.Usage.PromptTokens),
			Dimensions:         dims,
			IdempotencyKey:     p.ID + ":input",
			Timestamp:          &ts,
			Metadata:           buildEventMetadata(p, "input"),
		})
	}
	if p.Usage.CompletionTokens > 0 {
		out = append(out, ExternalIngest{
			ExternalCustomerID: p.User,
			MeterKey:           MeterKeyTokensOutput,
			Quantity:           decimal.NewFromInt(p.Usage.CompletionTokens),
			Dimensions:         dims,
			IdempotencyKey:     p.ID + ":output",
			Timestamp:          &ts,
			Metadata:           buildEventMetadata(p, "output"),
		})
	}
	return out, nil
}

// ErrMissingUser is returned by MapPayload when the LiteLLM payload
// has no `user` field set. Operators must configure their LiteLLM
// callers to pass user=<external_customer_id>; without it the
// adapter has no customer to attribute the spend to.
var ErrMissingUser = fmt.Errorf("litellm: payload.user is required (set user=<external_customer_id> on the litellm call)")

// ExternalIngest is the adapter's intermediate shape — uses the
// EXTERNAL customer id and meter key. The handler resolves these to
// internal IDs via the existing usage ingest path. Mirrors the
// existing usage.IngestRequest body shape so the resolver is shared.
type ExternalIngest struct {
	ExternalCustomerID string          `json:"external_customer_id"`
	MeterKey           string          `json:"meter_key"`
	Quantity           decimal.Decimal `json:"quantity"`
	Dimensions         map[string]any  `json:"dimensions,omitempty"`
	IdempotencyKey     string          `json:"idempotency_key,omitempty"`
	Timestamp          *time.Time      `json:"timestamp,omitempty"`
	Metadata           map[string]any  `json:"metadata,omitempty"`
}

// buildEventMetadata composes the per-event metadata blob: LiteLLM's
// own metadata + a velox.* namespace for adapter-injected context
// (cost figures, source identifier). Operator can query usage events
// by metadata in the audit dashboard.
func buildEventMetadata(p StandardLoggingPayload, half string) map[string]any {
	out := map[string]any{
		"velox.source":     "litellm",
		"velox.litellm_id": p.ID,
		"velox.call_type":  p.CallType,
		"velox.token_half": half, // "input" | "output"
	}
	if p.ResponseCost > 0 {
		out["velox.litellm_response_cost_usd"] = p.ResponseCost
	}
	if p.CostBreakdown != nil {
		// Surface the relevant half's cost. LiteLLM reports dollars-
		// as-float; we store verbatim so cost reconciliation against
		// the provider invoice is trivial.
		switch half {
		case "input":
			if p.CostBreakdown.InputCost > 0 {
				out["velox.litellm_cost_usd"] = p.CostBreakdown.InputCost
			}
		case "output":
			if p.CostBreakdown.OutputCost > 0 {
				out["velox.litellm_cost_usd"] = p.CostBreakdown.OutputCost
			}
		}
	}
	// Forward LiteLLM's own metadata under a namespaced key so it
	// doesn't collide with our velox.* keys. Operators can still
	// query by `metadata->litellm_metadata->>team_id` etc.
	if len(p.Metadata) > 0 {
		out["litellm_metadata"] = p.Metadata
	}
	return out
}

func unixSecondsToTime(secs float64) time.Time {
	whole := int64(secs)
	nanos := int64((secs - float64(whole)) * 1e9)
	return time.Unix(whole, nanos).UTC()
}

// _ ensures the usage package import stays — the handler uses
// usage.IngestInput (resolved form) downstream.
var _ = usage.IngestInput{}
