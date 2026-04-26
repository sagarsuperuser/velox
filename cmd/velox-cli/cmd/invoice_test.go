package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sagarsuperuser/velox/cmd/velox-cli/internal/client"
	"github.com/sagarsuperuser/velox/cmd/velox-cli/internal/output"
)

func TestInvoiceSend(t *testing.T) {
	tests := []struct {
		name         string
		params       invoiceSendParams
		serverStatus int
		serverBody   string
		wantContain  []string
		wantErr      bool
		// hitServer is true when the test expects the CLI to make a
		// network call. --dry-run flips this off so we assert no
		// httptest hits.
		hitServer bool
	}{
		{
			name: "text_default",
			params: invoiceSendParams{
				invoiceID: "in_001",
				email:     "ops@example.com",
				format:    output.FormatText,
			},
			serverStatus: http.StatusOK,
			serverBody:   `{"status": "sent"}`,
			wantContain:  []string{"in_001", "ops@example.com", "sent"},
			hitServer:    true,
		},
		{
			name: "json_output",
			params: invoiceSendParams{
				invoiceID: "in_002",
				email:     "billing@example.com",
				format:    output.FormatJSON,
			},
			serverStatus: http.StatusOK,
			serverBody:   `{"status": "sent"}`,
			wantContain:  []string{`"status": "sent"`},
			hitServer:    true,
		},
		{
			name: "dry_run_text",
			params: invoiceSendParams{
				invoiceID: "in_003",
				email:     "preview@example.com",
				dryRun:    true,
				format:    output.FormatText,
			},
			wantContain: []string{"DRY RUN", "/v1/invoices/in_003/send", "preview@example.com"},
			hitServer:   false,
		},
		{
			name: "dry_run_json",
			params: invoiceSendParams{
				invoiceID: "in_004",
				email:     "preview@example.com",
				dryRun:    true,
				format:    output.FormatJSON,
			},
			wantContain: []string{`"dry_run": true`, `"invoice_id": "in_004"`, `"would_send"`},
			hitServer:   false,
		},
		{
			name: "server_validation_error",
			params: invoiceSendParams{
				invoiceID: "in_005",
				email:     "ops@example.com",
				format:    output.FormatText,
			},
			serverStatus: http.StatusUnprocessableEntity,
			serverBody:   `{"error": {"message": "email sender not configured"}}`,
			wantErr:      true,
			hitServer:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var hits int
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits++
				if r.Method != http.MethodPost {
					t.Errorf("method = %s, want POST", r.Method)
				}
				wantPath := "/v1/invoices/" + tc.params.invoiceID + "/send"
				if r.URL.Path != wantPath {
					t.Errorf("path = %s, want %s", r.URL.Path, wantPath)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
					t.Errorf("auth header = %q, want Bearer test-key", got)
				}
				// Decode body and assert email round-trips.
				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode body: %v", err)
				}
				if body["email"] != tc.params.email {
					t.Errorf("body.email = %q, want %q", body["email"], tc.params.email)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.serverStatus)
				_, _ = w.Write([]byte(tc.serverBody))
			}))
			defer ts.Close()

			c := client.New(ts.URL, "test-key")
			var buf bytes.Buffer
			err := runInvoiceSend(context.Background(), &buf, c, tc.params)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("runInvoiceSend: %v", err)
			}

			out := buf.String()
			for _, want := range tc.wantContain {
				if !strings.Contains(out, want) {
					t.Errorf("output missing %q\nfull output:\n%s", want, out)
				}
			}

			if tc.hitServer && hits != 1 {
				t.Errorf("expected 1 server hit, got %d", hits)
			}
			if !tc.hitServer && hits != 0 {
				t.Errorf("dry-run should not hit server, got %d hits", hits)
			}
		})
	}
}
