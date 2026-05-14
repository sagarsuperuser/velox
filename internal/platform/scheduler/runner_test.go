package scheduler

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestRun_PanicRecoveryContinuesLoop pins the load-bearing claim of
// this helper: a panic in one tick must NOT kill the runner goroutine.
// Without recover(), the panic would unwind out of the for-select loop
// and the worker would silently die while the ticker channel kept
// buffering — which is exactly the bug the helper exists to prevent.
func TestRun_PanicRecoveryContinuesLoop(t *testing.T) {
	var ticks int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	work := func(_ context.Context) {
		n := atomic.AddInt32(&ticks, 1)
		// Panic on the first tick; subsequent ticks must still fire.
		if n == 1 {
			panic("boom")
		}
	}

	done := make(chan struct{})
	go func() {
		Run(ctx, "test_worker", 5*time.Millisecond, work)
		close(done)
	}()

	// Wait for at least 3 ticks to land — proving the loop survived
	// the panic on tick 1 and continued to fire ticks 2+.
	deadline := time.Now().Add(500 * time.Millisecond)
	for atomic.LoadInt32(&ticks) < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	if got := atomic.LoadInt32(&ticks); got < 3 {
		t.Fatalf("ticks fired = %d, want >= 3 (panic on tick 1 should not have stopped the loop)", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Run did not return after ctx cancel")
	}
}

// TestRun_StopsOnContextCancel asserts the runner exits cleanly when
// the parent ctx is cancelled — no leaked goroutines or stuck loops
// when cmd/velox shuts down.
func TestRun_StopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	work := func(_ context.Context) {}

	done := make(chan struct{})
	go func() {
		Run(ctx, "test_worker", 10*time.Millisecond, work)
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Run did not return after ctx cancel")
	}
}
