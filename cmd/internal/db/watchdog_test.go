package db

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// runWatchdog drives watchCloneProgress with a tiny poll interval and the given
// progress function, returning whether the watchdog aborted (called cancel).
func runWatchdog(t *testing.T, stall time.Duration, progress cloneProgressFunc) bool {
	t.Helper()

	orig := cloneProgressPollInterval
	cloneProgressPollInterval = 2 * time.Millisecond
	defer func() { cloneProgressPollInterval = orig }()

	ctx, realCancel := context.WithCancel(context.Background())
	defer realCancel()

	var aborted atomic.Bool
	cancel := func() {
		aborted.Store(true)
		realCancel()
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	var d *DB
	go func() {
		d.watchCloneProgress(ctx, cancel, stall, stop, progress)
		close(done)
	}()

	select {
	case <-done: // watchdog returned (it aborted, or ctx/stop fired)
	case <-time.After(2 * time.Second):
		close(stop)
		t.Fatal("watchdog did not return within 2s")
	}
	return aborted.Load()
}

// A clone that keeps reporting the same bytes and no new finished stages must be
// aborted once the stall window elapses.
func TestWatchCloneProgress_AbortsOnStall(t *testing.T) {
	progress := func(context.Context) (int64, int64, error) { return 100, 1, nil }
	if !runWatchdog(t, 40*time.Millisecond, progress) {
		t.Fatal("expected watchdog to abort a stalled (no bytes, no stage change) clone")
	}
}

// If progress cannot be read at all for a whole stall window the clone is aborted
// too, rather than waiting forever.
func TestWatchCloneProgress_AbortsWhenUnmeasurable(t *testing.T) {
	progress := func(context.Context) (int64, int64, error) { return 0, 0, errors.New("boom") }
	if !runWatchdog(t, 40*time.Millisecond, progress) {
		t.Fatal("expected watchdog to abort when progress is unmeasurable for a full stall window")
	}
}

// A byte-less tail stage (bytes flat) still counts as progress while the finished
// stage count keeps growing, so the watchdog must NOT abort.
func TestWatchCloneProgress_NoAbortWhenStagesAdvance(t *testing.T) {
	var stages atomic.Int64
	progress := func(context.Context) (int64, int64, error) {
		return 100, stages.Add(1), nil // bytes flat, a new stage finishes every poll
	}

	orig := cloneProgressPollInterval
	cloneProgressPollInterval = 2 * time.Millisecond
	defer func() { cloneProgressPollInterval = orig }()

	ctx, realCancel := context.WithCancel(context.Background())
	defer realCancel()
	var aborted atomic.Bool
	cancel := func() { aborted.Store(true); realCancel() }

	stop := make(chan struct{})
	done := make(chan struct{})
	var d *DB
	go func() { d.watchCloneProgress(ctx, cancel, 20*time.Millisecond, stop, progress); close(done) }()

	time.Sleep(200 * time.Millisecond) // many stall windows worth of polls
	close(stop)
	<-done
	if aborted.Load() {
		t.Fatal("watchdog aborted a clone that was still advancing its stage count")
	}
}
