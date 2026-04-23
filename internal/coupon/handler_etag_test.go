package coupon

import (
	"testing"
)

func TestParseIfMatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		header  string
		want    *int
		wantErr bool
	}{
		{name: "empty header → no precondition", header: "", want: nil},
		{name: "whitespace only → no precondition", header: "   ", want: nil},
		{name: "wildcard → no precondition", header: "*", want: nil},
		{name: "strong tag", header: `"42"`, want: intPtr(42)},
		{name: "weak tag is accepted leniently", header: `W/"42"`, want: intPtr(42)},
		{name: "zero is valid", header: `"0"`, want: intPtr(0)},
		{name: "unquoted value rejected", header: `42`, wantErr: true},
		{name: "partially quoted rejected", header: `"42`, wantErr: true},
		{name: "non-numeric rejected", header: `"abc"`, wantErr: true},
		{name: "empty quotes rejected", header: `""`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseIfMatch(tc.header)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got nil (parsed %v)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			switch {
			case tc.want == nil && got != nil:
				t.Errorf("got %d, want nil", *got)
			case tc.want != nil && got == nil:
				t.Errorf("got nil, want %d", *tc.want)
			case tc.want != nil && *got != *tc.want:
				t.Errorf("got %d, want %d", *got, *tc.want)
			}
		})
	}
}

func TestCouponETag(t *testing.T) {
	t.Parallel()
	if got := couponETag(42); got != `"42"` {
		t.Errorf(`couponETag(42): got %q, want %q`, got, `"42"`)
	}
	if got := couponETag(0); got != `"0"` {
		t.Errorf(`couponETag(0): got %q, want %q`, got, `"0"`)
	}
}

func intPtr(v int) *int { return &v }
