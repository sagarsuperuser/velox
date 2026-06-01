package testclock

import (
	"context"
	"testing"
)

// TestCatchupProcess_RecoversPanic covers the medium-severity audit finding:
// the catchup worker goroutine had no panic recovery, so a panic in the runner
// (a nil-deref in some billing path) would unwind the goroutine and crash the
// whole process, taking down the API for every tenant. process() now recovers,
// logs, and returns so the drain loop continues.
func TestCatchupProcess_RecoversPanic(t *testing.T) {
	w := NewCatchupWorker(NewCatchupQueue(1), func(_ context.Context, _ CatchupJob) error {
		panic("boom: nil deref in billing path")
	})

	// If process did not recover, the panic would propagate and fail the test.
	// Reaching the line after the call proves recovery.
	w.process(CatchupJob{TenantID: "t1", ClockID: "c1"})
}
