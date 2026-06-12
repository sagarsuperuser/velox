package audit_test

import (
	"net/http"
	"testing"

	"github.com/sagarsuperuser/velox/internal/audit"
)

// TestExtractClientIP pins the security property: the audit IP comes from
// r.RemoteAddr (which the global TrustedRealIP middleware has already resolved
// under its proxy-trust policy) and NEVER from raw X-Forwarded-For / X-Real-IP
// — otherwise any client could forge audit_log.ip_address.
func TestExtractClientIP(t *testing.T) {
	cases := []struct {
		name       string
		remoteAddr string
		xff        string
		xRealIP    string
		want       string
	}{
		{
			name:       "host:port → host",
			remoteAddr: "203.0.113.7:54321",
			want:       "203.0.113.7",
		},
		{
			name:       "forged X-Forwarded-For is ignored",
			remoteAddr: "203.0.113.7:54321",
			xff:        "1.2.3.4",
			xRealIP:    "5.6.7.8",
			want:       "203.0.113.7",
		},
		{
			name:       "bare host (no port) returned as-is",
			remoteAddr: "203.0.113.7",
			want:       "203.0.113.7",
		},
		{
			name:       "IPv6 host:port → host",
			remoteAddr: "[2001:db8::1]:443",
			want:       "2001:db8::1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &http.Request{RemoteAddr: tc.remoteAddr, Header: http.Header{}}
			if tc.xff != "" {
				r.Header.Set("X-Forwarded-For", tc.xff)
			}
			if tc.xRealIP != "" {
				r.Header.Set("X-Real-IP", tc.xRealIP)
			}
			if got := audit.ExtractClientIP(r); got != tc.want {
				t.Errorf("ExtractClientIP = %q, want %q", got, tc.want)
			}
		})
	}
}
