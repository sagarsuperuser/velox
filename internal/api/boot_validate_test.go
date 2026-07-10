package api

import (
	"testing"

	"github.com/sagarsuperuser/velox/internal/platform/clock"
	"github.com/sagarsuperuser/velox/internal/testutil"
)

// TestNewServer_BootWiresEveryEngineCollaborator is the boot proof for
// engine.MustValidate (2026-07-10 design review, redesign #3 stage 1):
// constructing the production Server against a real database must wire all
// 24 engine collaborators — a missing Set* call panics HERE, in CI, naming
// the field, instead of silently diverging on a money path in production.
func TestNewServer_BootWiresEveryEngineCollaborator(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: needs postgres")
	}
	db := testutil.SetupTestDB(t)
	// NewServer runs engine.MustValidate() after the last collaborator is
	// wired — reaching the return without panicking IS the assertion.
	srv := NewServer(db, clock.Real())
	if srv == nil {
		t.Fatal("NewServer returned nil")
	}
}
