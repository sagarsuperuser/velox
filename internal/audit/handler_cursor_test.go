package audit

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A malformed ?after cursor must be a 400, never a silent fallback to the
// offset path: the old fallback restarted a paginating client at page 1,
// which a CSV page-walk read as "export complete" — silent truncation or
// duplication with zero error signal (audit e2e 2026-07-13). These requests
// must be rejected BEFORE any store access — the nil logger proves it.
func TestList_MalformedCursorIs400(t *testing.T) {
	h := NewHandler(nil)

	cases := []struct {
		name  string
		after string
	}{
		{"not base64", "!!!not-base64!!!"},
		{"base64 of non-JSON", base64.URLEncoding.EncodeToString([]byte("not json"))},
		{"structurally valid but empty id/timestamp", base64.URLEncoding.EncodeToString([]byte(`{}`))},
		{"missing timestamp", base64.URLEncoding.EncodeToString([]byte(`{"id":"vlx_aud_1"}`))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/?after="+tc.after, nil)
			rec := httptest.NewRecorder()
			h.list(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status: got %d, want 400. body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

// A cursor produced by encodeAuditCursor must round-trip through the decoder
// unchanged — the seek predicate depends on both fields surviving intact.
func TestAuditCursor_RoundTrip(t *testing.T) {
	at := time.Date(2026, 7, 13, 10, 30, 0, 123456000, time.UTC)
	token := encodeAuditCursor("vlx_aud_abc", at)

	cur, err := decodeAuditCursor(token)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cur.ID != "vlx_aud_abc" {
		t.Errorf("id: got %q, want vlx_aud_abc", cur.ID)
	}
	if !cur.CreatedAt.Equal(at) {
		t.Errorf("created_at: got %v, want %v", cur.CreatedAt, at)
	}
}
