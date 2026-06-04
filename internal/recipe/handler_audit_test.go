package recipe

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sagarsuperuser/velox/internal/audit"
)

// TestRecipePreview_OptsOutOfAudit pins that POST /v1/recipes/{key}/preview —
// a read-only dry-run render — calls audit.MarkSkip so the audit middleware
// doesn't record a spurious "Created recipe" row. An empty registry makes the
// service return ErrNotFound, but MarkSkip is called first regardless, which
// is what we assert.
func TestRecipePreview_OptsOutOfAudit(t *testing.T) {
	h := &Handler{svc: &Service{registry: &Registry{}}}

	req := httptest.NewRequest(http.MethodPost, "/v1/recipes/anything/preview", strings.NewReader(`{}`))
	req = req.WithContext(audit.WithRequestState(req.Context()))
	rec := httptest.NewRecorder()

	h.preview(rec, req)

	if !audit.WasHandled(req.Context()) {
		t.Error("recipe preview must call audit.MarkSkip so the middleware skips its catch-all write")
	}
}
