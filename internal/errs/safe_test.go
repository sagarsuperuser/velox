package errs

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// safeErr is a test double that implements SafeMessageError.
type safeErr struct{ msg string }

func (e *safeErr) Error() string               { return "wrapped: " + e.msg }
func (e *safeErr) OperatorSafeMessage() string { return e.msg }

func TestSanitizeForOperator_SafeMessageInterfaceIsSurfaced(t *testing.T) {
	err := &safeErr{msg: "stripe tax: customer has no country on billing profile"}
	got := SanitizeForOperator(err, "REQ-1")
	want := "stripe tax: customer has no country on billing profile"
	if got != want {
		t.Errorf("SafeMessageError must be passed through. got %q, want %q", got, want)
	}
}

func TestSanitizeForOperator_SQLStateIsRedacted(t *testing.T) {
	err := errors.New(`list pending charges: ERROR: column reference "id" is ambiguous (SQLSTATE 42702)`)
	got := SanitizeForOperator(err, "REQ-99")
	if strings.Contains(got, "SQLSTATE") {
		t.Errorf("SQLSTATE leaked: %q", got)
	}
	if !strings.Contains(got, "REQ-99") {
		t.Errorf("expected request_id reference in generic message; got %q", got)
	}
}

func TestSanitizeForOperator_GoFileLineRedacted(t *testing.T) {
	err := errors.New("oh no: /Users/dev/foo/bar.go:123 +0x4b")
	got := SanitizeForOperator(err, "REQ-2")
	if strings.Contains(got, ".go:") {
		t.Errorf("file:line leaked: %q", got)
	}
}

func TestSanitizeForOperator_PanicRedacted(t *testing.T) {
	err := errors.New("panic: runtime error: invalid memory address or nil pointer dereference")
	got := SanitizeForOperator(err, "REQ-3")
	if strings.Contains(got, "panic") || strings.Contains(got, "runtime error") {
		t.Errorf("panic/runtime markers leaked: %q", got)
	}
}

func TestSanitizeForOperator_PlainMessagePassesThroughWithScrub(t *testing.T) {
	// Plain message, no internal markers — passed through. PII
	// redaction (Scrub) still applies.
	err := errors.New("payment failed for ops@example.com on card ending 4242")
	got := SanitizeForOperator(err, "REQ-4")
	if strings.Contains(got, "ops@example.com") {
		t.Errorf("email leaked despite Scrub: %q", got)
	}
	if !strings.Contains(got, "payment failed") {
		t.Errorf("operator-safe substance dropped: %q", got)
	}
}

func TestSanitizeForOperator_NilReturnsEmpty(t *testing.T) {
	if got := SanitizeForOperator(nil, "REQ"); got != "" {
		t.Errorf("nil err must yield empty string; got %q", got)
	}
}

func TestSummarizeForOperator_MixedSafeAndInternal(t *testing.T) {
	errs := []error{
		&safeErr{msg: "stripe tax: customer has no country"},
		errors.New(`ERROR: column reference "id" is ambiguous (SQLSTATE 42702)`),
		errors.New("foo.go:99 nil deref"),
	}
	got := SummarizeForOperator(errs, "REQ-XYZ")
	// Safe message present.
	if !strings.Contains(got, "no country") {
		t.Errorf("safe message dropped: %q", got)
	}
	// SQLSTATE not leaked.
	if strings.Contains(got, "SQLSTATE") {
		t.Errorf("SQLSTATE leaked: %q", got)
	}
	// Internal count + reference present.
	if !strings.Contains(got, "2 internal error(s)") {
		t.Errorf("internal-count rollup missing: %q", got)
	}
	if !strings.Contains(got, "REQ-XYZ") {
		t.Errorf("request_id reference missing: %q", got)
	}
}

func TestSummarizeForOperator_EmptyReturnsEmpty(t *testing.T) {
	if got := SummarizeForOperator(nil, "REQ"); got != "" {
		t.Errorf("empty errs slice should yield empty string; got %q", got)
	}
	if got := SummarizeForOperator([]error{}, "REQ"); got != "" {
		t.Errorf("empty errs slice should yield empty string; got %q", got)
	}
}

func TestSummarizeForOperator_OnlySafe_NoInternalRollup(t *testing.T) {
	errs := []error{
		&safeErr{msg: "stripe tax: provider outage"},
		&safeErr{msg: "card declined: insufficient_funds"},
	}
	got := SummarizeForOperator(errs, "REQ-1")
	if strings.Contains(got, "internal error") {
		t.Errorf("no internal errors in input — must not append rollup. got %q", got)
	}
	if !strings.Contains(got, "stripe tax") || !strings.Contains(got, "insufficient_funds") {
		t.Errorf("safe messages must be preserved: %q", got)
	}
}

func TestSummarizeForOperator_OnlyInternal_NoSafePrefix(t *testing.T) {
	errs := []error{
		fmt.Errorf("ERROR: deadlock detected (SQLSTATE 40P01)"),
		fmt.Errorf("ERROR: relation does not exist (SQLSTATE 42P01)"),
	}
	got := SummarizeForOperator(errs, "REQ-2")
	if strings.Contains(got, "SQLSTATE") {
		t.Errorf("internal SQL leaked: %q", got)
	}
	if !strings.HasPrefix(got, "2 internal error(s)") {
		t.Errorf("expected '2 internal error(s)' prefix; got %q", got)
	}
}
