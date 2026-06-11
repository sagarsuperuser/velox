package payment

import "testing"

// TestAppendStatusQuery pins the success/cancel return-URL builder for the
// public payment-update Stripe Checkout flow: success and cancel must land on
// the SAME public page with a distinguishing ?status=, and a base that already
// carries a query string must use & rather than a second ?.
func TestAppendStatusQuery(t *testing.T) {
	cases := []struct {
		name   string
		base   string
		status string
		want   string
	}{
		{
			name:   "no existing query → ?status",
			base:   "http://localhost:5173/payment-method-added",
			status: "success",
			want:   "http://localhost:5173/payment-method-added?status=success",
		},
		{
			name:   "cancel variant",
			base:   "http://localhost:5173/payment-method-added",
			status: "cancel",
			want:   "http://localhost:5173/payment-method-added?status=cancel",
		},
		{
			name:   "existing query → &status",
			base:   "https://app.example.com/account?ref=email",
			status: "success",
			want:   "https://app.example.com/account?ref=email&status=success",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := appendStatusQuery(tc.base, tc.status); got != tc.want {
				t.Errorf("appendStatusQuery(%q, %q) = %q, want %q", tc.base, tc.status, got, tc.want)
			}
		})
	}
}
