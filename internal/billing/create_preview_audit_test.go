package billing

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sagarsuperuser/velox/internal/audit"
)

// TestCreatePreview_OptsOutOfAudit pins the fix for the spurious "Created
// invoice" audit rows: POST /v1/invoices/create_preview is a read-only
// dry-run, so its handler must call audit.MarkSkip to suppress the audit
// middleware's catch-all write. Without it the preview (fired automatically by
// the dashboard's upcoming-invoice card) recorded a bogus row whose "View"
// link → /invoices/create_preview → GET → 405 Method Not Allowed.
//
// The request uses a blank customer_id so it errors before any store calls;
// MarkSkip is called first regardless, which is what we assert.
func TestCreatePreview_OptsOutOfAudit(t *testing.T) {
	h := &CreatePreviewHandler{
		svc: &PreviewService{
			customers:     &stubCustomers{},
			subscriptions: &stubSubscriptions{},
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/invoices/create_preview", strings.NewReader(`{"customer_id":"  "}`))
	req = req.WithContext(audit.WithRequestState(req.Context()))
	rec := httptest.NewRecorder()

	h.create(rec, req)

	if !audit.WasHandled(req.Context()) {
		t.Error("create_preview must call audit.MarkSkip so the middleware skips its catch-all write")
	}
}
