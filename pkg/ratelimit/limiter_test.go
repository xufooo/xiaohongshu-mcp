package ratelimit

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestReserveIsAtomicUnderConcurrentCallers(t *testing.T) {
	limiter := New(Config{MaxPerHour: 3})

	var allowed int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, canProceed, err := limiter.Reserve()
			if err != nil {
				t.Errorf("Reserve returned error: %v", err)
				return
			}
			if canProceed {
				atomic.AddInt32(&allowed, 1)
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&allowed); got != 3 {
		t.Fatalf("expected exactly 3 reservations, got %d", got)
	}

	info, canProceed, err := limiter.Check()
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if canProceed {
		t.Fatalf("expected limiter to reject after capacity is fully reserved")
	}
	if info.Used != 3 || info.Remaining != 0 {
		t.Fatalf("expected used=3 remaining=0, got used=%d remaining=%d", info.Used, info.Remaining)
	}
}

func TestReserveRejectsWhenLimitReached(t *testing.T) {
	limiter := New(Config{MaxPerHour: 1})

	if _, _, canProceed, err := limiter.Reserve(); err != nil || !canProceed {
		t.Fatalf("first reservation should proceed, canProceed=%v err=%v", canProceed, err)
	}
	info, _, canProceed, err := limiter.Reserve()
	if err != nil {
		t.Fatalf("second reservation returned error: %v", err)
	}
	if canProceed {
		t.Fatalf("second reservation should be rejected")
	}
	if info.Used != 1 || info.Remaining != 0 {
		t.Fatalf("rejected reservation should not be counted, got used=%d remaining=%d", info.Used, info.Remaining)
	}
}
