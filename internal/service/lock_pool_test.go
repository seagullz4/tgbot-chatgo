package service

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestKeyedLockPoolSerializesAndReleasesEntries(t *testing.T) {
	var pool keyedLockPool
	var active atomic.Int32
	var peak atomic.Int32
	var group sync.WaitGroup
	for index := 0; index < 20; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			unlock := pool.Lock(42)
			current := active.Add(1)
			for {
				observed := peak.Load()
				if current <= observed || peak.CompareAndSwap(observed, current) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			active.Add(-1)
			unlock()
		}()
	}
	group.Wait()
	if peak.Load() != 1 {
		t.Fatalf("same-key peak concurrency = %d", peak.Load())
	}
	if pool.Len() != 0 {
		t.Fatalf("lock entries retained = %d", pool.Len())
	}
}

func TestKeyedLockPoolAllowsDifferentKeys(t *testing.T) {
	var pool keyedLockPool
	firstUnlock := pool.Lock(1)
	acquired := make(chan func(), 1)
	go func() { acquired <- pool.Lock(2) }()
	select {
	case secondUnlock := <-acquired:
		secondUnlock()
	case <-time.After(time.Second):
		t.Fatal("different key was blocked")
	}
	firstUnlock()
}
