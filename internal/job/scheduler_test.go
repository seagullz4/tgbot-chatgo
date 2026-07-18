package job

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestOldGenerationCannotRemoveReplacement(t *testing.T) {
	scheduler := New()
	scheduler.Once("same", time.Hour, func() {})
	scheduler.mu.Lock()
	oldGeneration := scheduler.jobs["same"].generation
	scheduler.mu.Unlock()

	scheduler.Once("same", time.Hour, func() {})
	scheduler.finish("same", oldGeneration)

	scheduler.mu.Lock()
	current, ok := scheduler.jobs["same"]
	scheduler.mu.Unlock()
	if !ok || current.generation == oldGeneration {
		t.Fatal("replacement timer was removed by the old generation")
	}
	scheduler.Cancel("same")
}

func TestStopWaitsForRunningCallback(t *testing.T) {
	scheduler := New()
	started := make(chan struct{})
	release := make(chan struct{})
	stopped := make(chan struct{})
	scheduler.Once("running", 0, func() {
		close(started)
		<-release
	})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("callback did not start")
	}
	go func() {
		scheduler.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("Stop returned while callback was still running")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop did not return after callback completed")
	}
}

func TestStopCancelsPendingCallback(t *testing.T) {
	scheduler := New()
	ran := make(chan struct{}, 1)
	scheduler.Once("pending", 50*time.Millisecond, func() { ran <- struct{}{} })
	scheduler.Stop()
	select {
	case <-ran:
		t.Fatal("pending callback ran after Stop")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestReplacingPendingJobRunsOnlyLatest(t *testing.T) {
	scheduler := New()
	var oldRuns atomic.Int32
	latestRan := make(chan struct{})
	scheduler.Once("same", time.Hour, func() { oldRuns.Add(1) })
	scheduler.Once("same", time.Millisecond, func() { close(latestRan) })
	select {
	case <-latestRan:
	case <-time.After(time.Second):
		t.Fatal("latest timer did not run")
	}
	if oldRuns.Load() != 0 {
		t.Fatalf("replaced timer ran %d times", oldRuns.Load())
	}
}
