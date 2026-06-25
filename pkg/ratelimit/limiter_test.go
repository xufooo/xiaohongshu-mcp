package ratelimit

import (
	"context"
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
			_, _, canProceed, err := limiter.Reserve(context.Background())
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

	if _, _, canProceed, err := limiter.Reserve(context.Background()); err != nil || !canProceed {
		t.Fatalf("first reservation should proceed, canProceed=%v err=%v", canProceed, err)
	}
	info, _, canProceed, err := limiter.Reserve(context.Background())
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

func TestMultiWindowBudget(t *testing.T) {
	tests := []struct {
		name       string
		budget     BudgetConfig
		wantScope  string
		wantWindow int
	}{
		{
			name:       "10min",
			budget:     BudgetConfig{Per10Min: 2, PerHour: 100, PerDay: 100},
			wantScope:  "action:10min",
			wantWindow: 600,
		},
		{
			name:       "hour",
			budget:     BudgetConfig{Per10Min: 100, PerHour: 2, PerDay: 100},
			wantScope:  "action:hour",
			wantWindow: 3600,
		},
		{
			name:       "day",
			budget:     BudgetConfig{Per10Min: 100, PerHour: 100, PerDay: 2},
			wantScope:  "action:day",
			wantWindow: 86400,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			limiter := New(Config{
				Account:   AccountKey{AccountID: test.name},
				StorePath: t.TempDir(),
				Limits: map[Action]ActionLimitConfig{
					ActionBrowse: {
						Budget: test.budget,
					},
				},
				Global: GlobalBudgetConfig{
					All:         BudgetConfig{Per10Min: 100, PerHour: 100, PerDay: 100},
					Interaction: BudgetConfig{Per10Min: 100, PerHour: 100, PerDay: 100},
					Write:       BudgetConfig{Per10Min: 100, PerHour: 100, PerDay: 100},
					Publish:     BudgetConfig{Per10Min: 100, PerHour: 100, PerDay: 100},
				},
			})

			for i := 0; i < 2; i++ {
				if _, _, canProceed, err := limiter.Reserve(context.Background()); err != nil || !canProceed {
					t.Fatalf("reservation %d should proceed, canProceed=%v err=%v", i+1, canProceed, err)
				}
			}

			info, _, canProceed, err := limiter.Reserve(context.Background())
			if err != nil {
				t.Fatalf("third reservation returned error: %v", err)
			}
			if canProceed {
				t.Fatalf("third reservation should be rejected by %s budget", test.name)
			}
			if info.Scope != test.wantScope {
				t.Fatalf("scope = %q, want %q", info.Scope, test.wantScope)
			}
			if info.WindowSeconds != test.wantWindow {
				t.Fatalf("window = %d, want %d", info.WindowSeconds, test.wantWindow)
			}
			if info.RetryAfter == nil {
				t.Fatal("retry_after should be set when a window budget is exhausted")
			}
		})
	}
}
